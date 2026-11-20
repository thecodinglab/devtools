package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	dialer *dialer
	clock  *fakeClock // nil: the wall clock
}

func newSSHRig(t *testing.T, mode string) *sshRig {
	t.Helper()
	r := &sshRig{t: t, ssh: fakeSSHPath(t), srv: prototest.NewServer(t), dir: testkit.ShortTempDir(t)}
	r.argv = filepath.Join(r.dir, "argv")
	r.pid = filepath.Join(r.dir, "pid")
	t.Setenv("FAKE_SSH_MODE", mode)
	t.Setenv("FAKE_SSH_SOCKET", r.srv.Socket())
	t.Setenv("FAKE_SSH_ARGV", r.argv)
	t.Setenv("FAKE_SSH_PID", r.pid)
	r.dialer = NewDialer(DialOptions{SSH: r.ssh, Hello: localHello()}).(*dialer)
	return r
}

func (r *sshRig) useFakeClock() *fakeClock {
	r.clock = newFakeClock()
	r.dialer.clk = r.clock
	return r.clock
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

func dialAsync(d Dialer, machine string) <-chan dialResult {
	ch := make(chan dialResult, 1)
	go func() {
		l, f := d.Dial(context.Background(), machine)
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
