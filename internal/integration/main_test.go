package integration

// The harness of the level 3 tests (10.1.1): the helper programs this
// package adds to those of testkit, a server of its own process run through
// boot.Run, a raw connection for the handshake and tolerance tests, and the
// small readers of frames, state and processes the tests share.
//
// TestMain is in slice_test.go (P1-16, written before this file existed):
// it calls testkit.RunHelper first and cleans the environment of the test
// process. The helpers below are registered from init, before it runs.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server/boot"
	"devtools/internal/server/persist"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
)

// Variables of the helpers of this file.
const (
	// envPaneSpec names the helper, with its arguments, that the panes of a
	// boot server run: "hex", "flood 64".
	envPaneSpec = "DEVTOOLS_TEST_PANE"
	// envTestDir names a directory where helpers leave files for the test.
	envTestDir = "DEVTOOLS_TEST_DIR"
)

// bootVersion is the version of a boot server.
const bootVersion = "0.0.0-l3"

func init() {
	// bootserver is `devtools server` in a process of its own: boot.Run,
	// with every start step of 3.9. Its panes run the test binary as their
	// login shell ($SHELL), which then becomes the helper "pane".
	testkit.RegisterHelper("bootserver", func([]string) int {
		os.Setenv(testkit.HelperVar, "pane")
		return boot.Run(bootVersion)
	})
	testkit.RegisterHelper("pane", helperPane)
	testkit.RegisterHelper("envwait", helperEnvWait)
	testkit.RegisterHelper("killonce", helperKillOnce)
	testkit.RegisterHelper("pidwait", helperPidWait)
	testkit.RegisterHelper("hupcheck", helperHupCheck)
}

// helperPane runs the helper DEVTOOLS_TEST_PANE names, with its arguments:
// a boot server starts its panes' program without arguments, as a login
// shell, so the arguments travel in the variable.
func helperPane([]string) int {
	spec := strings.Fields(os.Getenv(envPaneSpec))
	if len(spec) == 0 {
		spec = []string{"hex"}
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pane: %v\n", err)
		return 127
	}
	env := []string{testkit.HelperVar + "=" + spec[0]}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, testkit.HelperVar+"=") {
			env = append(env, kv)
		}
	}
	err = syscall.Exec(exe, append([]string{exe}, spec[1:]...), env)
	fmt.Fprintf(os.Stderr, "pane: exec %s: %v\n", exe, err)
	return 127
}

// waitInput reads and drops standard input until it ends.
func waitInput() {
	buf := make([]byte, 4096)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			return
		}
	}
}

// helperEnvWait prints its environment sorted, then "argv0 <argv[0]>" and
// "wd <working directory>", then "end", and waits for the end of its input;
// unlike the helper env it does not exit, so its pane stays.
func helperEnvWait([]string) int {
	env := os.Environ()
	sort.Strings(env)
	w := bufio.NewWriter(os.Stdout)
	for _, kv := range env {
		fmt.Fprintf(w, "%s\r\n", kv)
	}
	wd, err := os.Getwd()
	if err != nil {
		wd = "?" + err.Error()
	}
	fmt.Fprintf(w, "argv0 %s\r\nwd %s\r\nend\r\n", os.Args[0], wd)
	w.Flush()
	waitInput()
	return 0
}

// helperKillOnce ends by SIGTERM the first time it runs, when the marker
// file does not exist yet (it creates it); any later time it prints
// "respawned" and waits for the end of its input.
func helperKillOnce(args []string) int {
	if len(args) == 0 {
		return 2
	}
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		os.Stdout.WriteString("respawned\r\n")
		waitInput()
		return 0
	}
	f.Close()
	os.Stdout.WriteString("dying\r\n")
	signal.Reset(syscall.SIGTERM)
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	time.Sleep(10 * time.Second)
	return 125
}

// helperPidWait starts a child of its own (the helper sighup, in its
// process group), prints "pids <own pid> <child's pid>" and waits for the
// end of its input. The two are what TestShutdownLeavesNoProcessBehind
// looks for afterwards.
func helperPidWait([]string) int {
	exe, err := os.Executable()
	if err != nil {
		return 127
	}
	child := exec.Command(exe)
	child.Env = append(os.Environ(), testkit.HelperVar+"=sighup")
	if err := child.Start(); err != nil {
		fmt.Printf("pidwait: %v\r\n", err)
		return 1
	}
	fmt.Printf("pids %d %d\r\n", os.Getpid(), child.Process.Pid)
	waitInput()
	return 0
}

// helperHupCheck prints whether it was started with SIGHUP ignored and its
// pid, then waits for SIGHUP or SIGTERM; the first that arrives is written
// as the file "<pid>.<SIGHUP|SIGTERM>" into DEVTOOLS_TEST_DIR, and it exits.
func helperHupCheck([]string) int {
	ignored := signal.Ignored(syscall.SIGHUP)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM)
	fmt.Printf("sighup-ignored=%v pid=%d\r\n", ignored, os.Getpid())
	sig := <-sigs
	name := "SIGTERM"
	if sig == syscall.SIGHUP {
		name = "SIGHUP"
	}
	os.WriteFile(filepath.Join(os.Getenv(envTestDir), fmt.Sprintf("%d.%s", os.Getpid(), name)), nil, 0o600)
	return 0
}

// --- servers ---

// work creates the directory "work" in home and returns it.
func work(t *testing.T, home string, sub ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{home, "work"}, sub...)...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// startServer runs a real server in this process (servertest) in a new
// short directory and returns that directory and the socket.
func startServer(t *testing.T, opts ...servertest.Option) (home, socket string) {
	t.Helper()
	home = testkit.ShortTempDir(t)
	return home, servertest.Start(t, home, opts...)
}

// longer raises prototest.Timeout for the rest of the test.
func longer(t *testing.T, d time.Duration) {
	old := prototest.Timeout
	prototest.Timeout = d
	t.Cleanup(func() { prototest.Timeout = old })
}

// bootServer is `devtools server` (boot.Run) in a process of its own, with
// an environment of testkit.CleanEnv in home: its state directory and
// socket are under home/.local/state/devtools.
type bootServer struct {
	t      *testing.T
	home   string
	cmd    *exec.Cmd
	exited chan struct{}
	log    *lockedBuffer
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startBoot starts a boot server whose panes run the helper of paneSpec
// ("hex", "flood 64"); extra are further variables of its environment, and
// each of them reaches its panes. It returns once the socket answers.
func startBoot(t *testing.T, home, paneSpec string, extra ...string) *bootServer {
	t.Helper()
	testkit.RequirePTY(t)
	argv, helper := testkit.HelperCommand("bootserver")
	pass := []string{testkit.HelperVar, envPaneSpec, envTestDir, "GORACE"}
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		pass = append(pass, name)
	}
	env := append(helper,
		"SHELL="+argv[0],
		envPaneSpec+"="+paneSpec,
		"GORACE=atexit_sleep_ms=0",
		"DEVTOOLS_PASS_ENV="+strings.Join(pass, ","))
	env = append(env, extra...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = testkit.CleanEnv(home, env...)
	cmd.Dir = home
	b := &bootServer{t: t, home: home, cmd: cmd, exited: make(chan struct{}), log: &lockedBuffer{}}
	cmd.Stdout, cmd.Stderr = b.log, b.log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		cmd.Wait()
		close(b.exited)
	}()
	t.Cleanup(func() {
		if !b.done() {
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-b.exited:
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				<-b.exited
			}
		}
		if t.Failed() {
			t.Logf("the boot server's log:\n%s", b.log.String())
		}
	})
	testkit.Eventually(t, 15*time.Second, "the boot server listens on "+b.socket(), func() bool {
		if b.done() {
			t.Fatalf("the boot server exited early: %s", b.log.String())
		}
		nc, err := net.DialTimeout("unix", b.socket(), time.Second)
		if err != nil {
			return false
		}
		nc.Close()
		return true
	})
	return b
}

func (b *bootServer) stateDir() string { return filepath.Join(b.home, ".local", "state", "devtools") }
func (b *bootServer) socket() string   { return filepath.Join(b.stateDir(), "server.sock") }
func (b *bootServer) pid() int         { return b.cmd.Process.Pid }

func (b *bootServer) done() bool {
	select {
	case <-b.exited:
		return true
	default:
		return false
	}
}

// stop sends SIGTERM and returns the exit status.
func (b *bootServer) stop() int {
	b.t.Helper()
	b.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-b.exited:
	case <-time.After(10 * time.Second):
		b.t.Fatalf("the boot server did not stop within 10 s: %s", b.log.String())
	}
	return b.cmd.ProcessState.ExitCode()
}

// kill ends the server with SIGKILL: none of the stop steps run.
func (b *bootServer) kill() {
	b.t.Helper()
	b.cmd.Process.Kill()
	select {
	case <-b.exited:
	case <-time.After(10 * time.Second):
		b.t.Fatal("the boot server did not end after SIGKILL")
	}
}

// --- state and frames ---

// sessionOf returns the session of dir in a state.
func sessionOf(s proto.State, dir string) (proto.Session, bool) {
	for _, se := range s.Sessions {
		if se.Dir == dir {
			return se, true
		}
	}
	return proto.Session{}, false
}

// savedState reads state.json of a state directory as it is on disk, ok
// false while it does not exist or does not decode. It does not use
// persist.Load, which would set a bad file aside.
func savedState(dir string) (persist.State, bool) {
	b, err := os.ReadFile(filepath.Join(dir, persist.FileName))
	if err != nil {
		return persist.State{}, false
	}
	var st persist.State
	if err := json.Unmarshal(b, &st); err != nil {
		return persist.State{}, false
	}
	return st, true
}

// lastFrame returns the newest frame of the pane among the messages the
// client received.
func lastFrame(t *testing.T, c *prototest.Client, pane string) proto.Frame {
	t.Helper()
	msgs := c.Received()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].T != proto.TypeFrame {
			continue
		}
		var f proto.Frame
		if err := msgs[i].Decode(&f); err != nil {
			t.Fatal(err)
		}
		if f.Pane == pane {
			return f
		}
	}
	t.Fatalf("no frame of pane %s arrived", pane)
	return proto.Frame{}
}

// screensOf replays the frames of a pane among msgs and returns the screen
// after every complete frame, in order.
func screensOf(t *testing.T, msgs []proto.Msg, pane string) []golden.Screen {
	t.Helper()
	var out []golden.Screen
	var cur golden.Screen
	for _, m := range msgs {
		if m.T != proto.TypeFrame {
			continue
		}
		var f proto.Frame
		if err := m.Decode(&f); err != nil {
			t.Fatal(err)
		}
		if f.Pane != pane {
			continue
		}
		if f.Full || len(cur.Lines) != f.Rows {
			if f.Full {
				cur.Lines = make([]string, f.Rows)
			} else {
				lines := make([]string, f.Rows)
				copy(lines, cur.Lines)
				cur.Lines = lines
			}
		} else {
			cur.Lines = append([]string(nil), cur.Lines...)
		}
		cur.Cols, cur.Rows = f.Cols, f.Rows
		for _, l := range f.Lines {
			cur.Lines[l.Y] = l.S
		}
		cur.CursorX, cur.CursorY, cur.CursorVisible = f.Cursor.X, f.Cursor.Y, f.Cursor.Visible
		if !f.More {
			out = append(out, cur)
		}
	}
	return out
}

// plainRows returns the rows of a screen without styles, trailing blanks
// removed.
func plainRows(s golden.Screen) []string {
	out := make([]string, len(s.Lines))
	for i, l := range s.Lines {
		out[i] = golden.PlainRow(l)
	}
	return out
}

// field returns the value after "name " or "name=" on the first row of the
// screen that starts with it.
func field(s golden.Screen, name string) (string, bool) {
	for _, r := range plainRows(s) {
		for _, sep := range []string{" ", "="} {
			if v, ok := strings.CutPrefix(r, name+sep); ok {
				return v, true
			}
		}
	}
	return "", false
}

// sizePattern is what the helper size prints; the terminal echoes typed
// keys in front of it.
var sizePattern = regexp.MustCompile(`(\d+) x (\d+)$`)

// sizeOf reads the newest "cols x rows" the helper size printed.
func sizeOf(s golden.Screen) (cols, rows int, ok bool) {
	rs := plainRows(s)
	for i := len(rs) - 1; i >= 0; i-- {
		if m := sizePattern.FindStringSubmatch(rs[i]); m != nil {
			cols, _ = strconv.Atoi(m[1])
			rows, _ = strconv.Atoi(m[2])
			return cols, rows, true
		}
	}
	return 0, 0, false
}

// waitSize waits until the pane's helper size printed cols x rows last.
func waitSize(c *prototest.Client, pane string, cols, rows int) golden.Screen {
	return c.WaitScreen(pane, fmt.Sprintf("the program prints %d x %d", cols, rows), func(s golden.Screen) bool {
		x, y, ok := sizeOf(s)
		return ok && x == cols && y == rows
	})
}

// --- raw connections ---

// rawConn is a connection that speaks the framing directly, for the tests
// of the handshake and of the tolerance rules (4.1), where prototest would
// refuse to send what they send.
type rawConn struct {
	t  *testing.T
	nc net.Conn
}

func dialRaw(t *testing.T, socket string) *rawConn {
	t.Helper()
	return &rawConn{t: t, nc: prototest.DialRaw(t, socket)}
}

// send writes one message whose body is the JSON object obj.
func (r *rawConn) send(obj string) {
	r.t.Helper()
	if !json.Valid([]byte(obj)) {
		r.t.Fatalf("not JSON: %s", obj)
	}
	r.nc.SetWriteDeadline(time.Now().Add(prototest.Timeout))
	if err := proto.WriteMsg(r.nc, proto.Msg{Body: json.RawMessage(obj)}); err != nil {
		r.t.Fatalf("send %s: %v", obj, err)
	}
}

// next reads the next message.
func (r *rawConn) next() proto.Msg {
	r.t.Helper()
	r.nc.SetReadDeadline(time.Now().Add(prototest.Timeout))
	m, err := proto.ReadMsg(r.nc)
	if err != nil {
		r.t.Fatalf("read: %v", err)
	}
	return m
}

// reply reads messages until the one with the id, skipping pings and the
// state of an interactive connection.
func (r *rawConn) reply(id uint64) proto.Msg {
	r.t.Helper()
	for {
		m := r.next()
		if m.ID == id {
			return m
		}
		if m.ID != 0 {
			r.t.Fatalf("a reply with id %d while waiting for %d: %s", m.ID, id, m.Body)
		}
	}
}

// helloJSON returns a hello as JSON text: a generation 1 hello of the
// kind, with the further fields extra.
func helloJSON(kind string, extra ...string) string {
	fields := append([]string{`"t":"hello"`, `"gen":1`, `"version":"0.0.0-raw"`, `"kind":` + strconv.Quote(kind)}, extra...)
	return "{" + strings.Join(fields, ",") + "}"
}

// --- processes ---

// alive reports whether a process with the pid exists. A zombie counts as
// gone only once its parent reaped it, so callers wait with Eventually.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) != syscall.ESRCH
}
