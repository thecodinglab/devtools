package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
)

// TestMain is hermetic: nothing of the developer's shell (a herdr, tmux or
// devtools pane, the state directory) reaches the tests. When the test
// binary runs under the name "ssh" it is the fake ssh of these tests.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "ssh" {
		os.Exit(fakeSSH(os.Args[1:]))
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range []string{"DEVTOOLS_", "HERDR_", "TMUX", "XDG_", "FAKE_SSH_", "STY", "ZELLIJ"} {
			if strings.HasPrefix(name, p) {
				os.Unsetenv(name)
			}
		}
	}
	home, err := os.MkdirTemp("/tmp", "dt-remote-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// --- the fake ssh ---
//
// The test binary itself, re-executed through a symlink named "ssh"
// (DialOptions.SSH points to it). FAKE_SSH_MODE chooses what it does:
//
//	ok           runs Bridge against FAKE_SSH_SOCKET, like a remote host
//	hang         prints nothing and never ends
//	exit255      prints FAKE_SSH_STDERR on stderr and exits 255
//	exit         prints FAKE_SSH_STDERR and exits with FAKE_SSH_EXIT
//	garbage      prints FAKE_SSH_GARBAGE bytes of banner, then runs Bridge
//	slow-marker  runs Bridge, writing its ready line one byte at a time
//	no-bridge    an old build: unknown command "bridge", exit 1
//
// An invocation with "-O check" or a ControlPath first goes to the shared
// connection, as the mux client of OpenSSH does (mux.c, muxclient).
// FAKE_SSH_CHECK is the state of its master:
//
//	up      it runs: the check exits 0, a bridge runs over it
//	refuse  it runs but takes no new session (its MaxSessions are used):
//	        the check exits 0, a bridge falls back to a login
//	hang    it answers nothing: the invocation never ends
//	else    there is none: the check exits 255, a bridge logs in
//
// FAKE_SSH_CONFIG_MASTER is the ControlMaster of the user's ssh config,
// which counts when the command line sets none. With yes or ask ssh skips
// the master, even for "-O check", and logs in.
//
// A login offers the methods of PreferredAuthentications: with "none" it
// fails as "Permission denied", else the agent is asked, which appends a
// line to the file FAKE_SSH_LOGINS names, and the invocation goes on.
//
// FAKE_SSH_ARGV names a file that gets one JSON line with the arguments of
// every invocation; FAKE_SSH_PID a file that gets the pid.
func fakeSSH(args []string) int {
	if f := os.Getenv("FAKE_SSH_ARGV"); f != "" {
		b, _ := json.Marshal(args)
		out, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			out.Write(append(b, '\n'))
			out.Close()
		}
	}
	if f := os.Getenv("FAKE_SSH_PID"); f != "" {
		os.WriteFile(f, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if slices.Contains(args, "-O") || fakeOption(args, "ControlPath") != "" {
		if code, done := fakeMux(args); done {
			return code
		}
	}
	socket := os.Getenv("FAKE_SSH_SOCKET")
	switch mode := os.Getenv("FAKE_SSH_MODE"); mode {
	case "ok":
		return Bridge(os.Stdin, os.Stdout, socket, false)
	case "hang":
		time.Sleep(time.Hour)
		return 0
	case "exit255":
		fmt.Fprint(os.Stderr, os.Getenv("FAKE_SSH_STDERR"))
		return 255
	case "exit":
		fmt.Fprint(os.Stderr, os.Getenv("FAKE_SSH_STDERR"))
		code, _ := strconv.Atoi(os.Getenv("FAKE_SSH_EXIT"))
		return code
	case "garbage":
		n, _ := strconv.Atoi(os.Getenv("FAKE_SSH_GARBAGE"))
		os.Stdout.Write(garbage(n, os.Getenv("FAKE_SSH_NOLINE") == "1"))
		return Bridge(os.Stdin, os.Stdout, socket, false)
	case "slow-marker":
		return Bridge(os.Stdin, &slowWriter{w: os.Stdout, slow: len(readyLine) + 1}, socket, false)
	case "no-bridge":
		fmt.Fprint(os.Stderr, "Error: unknown command \"bridge\" for \"devtools\"\nRun 'devtools --help' for usage.\n")
		return 1
	default:
		fmt.Fprintf(os.Stderr, "fake ssh: unknown mode %q\n", mode)
		return 99
	}
}

// fakeMux is the shared connection of an invocation: done is false when
// the invocation goes on as the bridge.
func fakeMux(args []string) (code int, done bool) {
	check := slices.Contains(args, "-O")
	master := fakeOption(args, "ControlMaster")
	if master == "" {
		master = os.Getenv("FAKE_SSH_CONFIG_MASTER")
	}
	if master == "yes" || master == "ask" {
		return fakeLogin(args, check)
	}
	switch state := os.Getenv("FAKE_SSH_CHECK"); {
	case state == "hang":
		time.Sleep(time.Hour)
		return 0, true
	case (state == "up" || state == "refuse") && check:
		fmt.Fprintf(os.Stderr, "Master running (pid=%d)\r\n", os.Getpid())
		return 0, true
	case state == "up":
		return 0, false
	case state == "refuse":
		fmt.Fprint(os.Stderr, "mux_client_request_session: session request failed: Session open refused by peer\r\n")
	case check:
		fmt.Fprint(os.Stderr, "Control socket connect(/x/%C): No such file or directory\r\n")
		return 255, true
	}
	return fakeLogin(args, check)
}

// fakeLogin is the login ssh starts when no master served the invocation.
// A check that logged in ran a shell on /dev/null, which exits 0.
func fakeLogin(args []string, check bool) (code int, done bool) {
	if fakeOption(args, "PreferredAuthentications") == "none" {
		fmt.Fprint(os.Stderr, "florian@macmini: Permission denied (publickey).\r\n")
		return 255, true
	}
	if f := os.Getenv("FAKE_SSH_LOGINS"); f != "" {
		out, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(out, "the agent was asked")
			out.Close()
		}
	}
	return 0, check
}

// fakeOption is the value of the first "-o name=value" of args, as ssh
// takes the first value it gets; "" when there is none.
func fakeOption(args []string, name string) string {
	for i := 0; i+1 < len(args) && args[i] != "--"; i++ {
		if k, v, ok := strings.Cut(args[i+1], "="); args[i] == "-o" && ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// garbage is n bytes of login banner: escape sequences, binary bytes and a
// mention of the ready line that is not at the start of a line. Each line
// ends in "\n" unless noline is set, and so does the whole.
func garbage(n int, noline bool) []byte {
	line := []byte("motd \x1b[1;31mWelcome\x1b[0m \x00\xff\xfe say devtools-bridge-ready 1 here\x07\n")
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, line...)
	}
	out = out[:n]
	if noline {
		for i := range out {
			if out[i] == '\n' {
				out[i] = '.'
			}
		}
	} else if n > 0 {
		out[n-1] = '\n'
	}
	return out
}

// slowWriter writes its first slow bytes one at a time, with a pause.
type slowWriter struct {
	w    io.Writer
	slow int
}

func (s *slowWriter) Write(b []byte) (int, error) {
	written := 0
	for len(b) > 0 && s.slow > 0 {
		if _, err := s.w.Write(b[:1]); err != nil {
			return written, err
		}
		time.Sleep(5 * time.Millisecond)
		b = b[1:]
		s.slow--
		written++
	}
	if len(b) == 0 {
		return written, nil
	}
	n, err := s.w.Write(b)
	return written + n, err
}

// fakeSSHPath makes the symlink named "ssh" and returns its path.
func fakeSSHPath(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(testkit.ShortTempDir(t), "ssh")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// sshRig is a fake ssh in front of a fake server.
type sshRig struct {
	t      *testing.T
	ssh    string
	srv    *prototest.Server
	dir    string
	argv   string
	pid    string
	login  string // FAKE_SSH_LOGINS
	dialer *dialer
	clock  *fakeClock // nil: the wall clock
}

func newSSHRig(t *testing.T, mode string) *sshRig {
	t.Helper()
	r := &sshRig{t: t, ssh: fakeSSHPath(t), srv: prototest.NewServer(t), dir: testkit.ShortTempDir(t)}
	r.argv = filepath.Join(r.dir, "argv")
	r.pid = filepath.Join(r.dir, "pid")
	r.login = filepath.Join(r.dir, "logins")
	t.Setenv("FAKE_SSH_MODE", mode)
	t.Setenv("FAKE_SSH_SOCKET", r.srv.Socket())
	t.Setenv("FAKE_SSH_ARGV", r.argv)
	t.Setenv("FAKE_SSH_PID", r.pid)
	t.Setenv("FAKE_SSH_LOGINS", r.login)
	r.dialer = NewDialer(DialOptions{SSH: r.ssh, Hello: localHello()}).(*dialer)
	return r
}

// share gives the rig's dialer a ControlDir and returns it.
func (r *sshRig) share() string {
	dir := filepath.Join(r.dir, "c")
	r.dialer.opts.ControlDir = dir
	return dir
}

func (r *sshRig) useFakeClock() *fakeClock {
	r.clock = newFakeClock()
	r.dialer.clk = r.clock
	return r.clock
}

// logins is how many invocations asked the agent: each one a prompt.
func (r *sshRig) logins() int {
	b, _ := os.ReadFile(r.login)
	return strings.Count(string(b), "\n")
}

// argvs returns the recorded argument vectors, one per invocation.
func (r *sshRig) argvs() [][]string {
	r.t.Helper()
	b, err := os.ReadFile(r.argv)
	if err != nil {
		r.t.Fatalf("the fake ssh recorded nothing: %v", err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var a []string
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// fakePid waits until the fake wrote its pid and returns it.
func (r *sshRig) fakePid() int {
	r.t.Helper()
	var pid int
	testkit.Eventually(r.t, 10*time.Second, "the fake ssh writes its pid", func() bool {
		b, err := os.ReadFile(r.pid)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(b))
		return err == nil
	})
	return pid
}

// gone reports whether the process pid no longer exists (it was reaped).
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// localHello is the hello of an interactive client on this machine, with
// the fields that must not cross to another machine set.
func localHello() proto.Hello {
	h := prototest.Interactive()
	h.TTY = "/dev/pts/7"
	h.Ancestors = []int{4121, 4100, 1}
	h.Env = map[string]string{"DISPLAY": ":0"}
	h.Caps = []string{proto.CapCatalog, proto.CapAgents}
	return h
}

type dialResult struct {
	link Link
	fail *Failure
}

// dialAsync dials machine with login, as the first dial of a fleet does.
func dialAsync(d Dialer, machine string) <-chan dialResult {
	return dialLogin(d, machine, true)
}

func dialLogin(d Dialer, machine string, login bool) <-chan dialResult {
	ch := make(chan dialResult, 1)
	go func() {
		l, f := d.Dial(context.Background(), machine, login)
		ch <- dialResult{l, f}
	}()
	return ch
}

func waitDial(t *testing.T, ch <-chan dialResult) dialResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("Dial did not return")
		return dialResult{}
	}
}

// online waits for a successful dial.
func online(t *testing.T, ch <-chan dialResult) Link {
	t.Helper()
	r := waitDial(t, ch)
	if r.fail != nil {
		t.Fatalf("Dial failed: %s: %s", r.fail.Reason, r.fail.Text)
	}
	t.Cleanup(r.link.Close)
	return r.link
}

// failed waits for a failed dial.
func failed(t *testing.T, ch <-chan dialResult) *Failure {
	t.Helper()
	r := waitDial(t, ch)
	if r.fail == nil {
		r.link.Close()
		t.Fatal("Dial succeeded, want a failure")
	}
	if r.link != nil {
		t.Fatal("a failed Dial returned a link")
	}
	return r.fail
}

// recv returns the next message of the link.
func recv(t *testing.T, l Link) proto.Msg {
	t.Helper()
	select {
	case m, ok := <-l.Recv():
		if !ok {
			t.Fatalf("the link ended: %v", l.Err())
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no message on the link")
		return proto.Msg{}
	}
}

// ended waits until Recv is closed and returns Err.
func ended(t *testing.T, l Link) *Failure {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-l.Recv():
			if !ok {
				return l.Err()
			}
		case <-deadline:
			t.Fatal("the link did not end")
			return nil
		}
	}
}

func snapshot() proto.Snapshot {
	return proto.Snapshot{Rev: 41, Sessions: []proto.Session{}, Tabs: []proto.Tab{}, Panes: []proto.Pane{}, Agents: []proto.Agent{}}
}

// --- the fake clock ---

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
	calls   int
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{c.now.Add(d), ch})
	return ch
}

// Advance moves the clock and fires every timer that is due.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

// waitCalls waits until After was called at least n times.
func (c *fakeClock) waitCalls(t *testing.T, n int) {
	t.Helper()
	testkit.Eventually(t, 10*time.Second, fmt.Sprintf("%d timers are set", n), func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.calls >= n
	})
}

// readRaw reads one message from a connection without answering pings.
func readRaw(t *testing.T, r io.Reader) proto.Msg {
	t.Helper()
	m, err := proto.ReadMsg(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return m
}
