package boot

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"devtools/internal/server"
	"devtools/internal/testkit"
)

// TestMain keeps the tests hermetic and turns the test binary into the
// helper programs of this package: "boot" runs Run with a fake server, and
// "sigstate" prints whether SIGHUP is ignored. Every test of Run starts the
// helper as its own process with an environment from testkit.CleanEnv, so
// the setsid, the chdir, the umask and the signals of Run never touch the
// test process, and no path of the developer is reached.
func TestMain(m *testing.M) {
	testkit.RegisterHelper("boot", helperBoot)
	testkit.RegisterHelper("sigstate", helperSigstate)
	testkit.RunHelper()

	home, err := os.MkdirTemp("", "devtools-boot-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "DEVTOOLS_SERVICE", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH", "STY", "ZELLIJ",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// fakeLog is the file in the state directory where the fake server writes
// one line per thing it did.
const fakeLog = "fake.log"

// helperBoot runs Run with a fake server. Flags:
//
//	-hang          Shutdown never returns
//	-watchdog d    the watchdog delay
//	-uid-offset n  the uid a peer must have is this user's plus n
//	-child         Serve starts a child in a process group of its own
//	-sigstate      Serve runs the helper sigstate and records its answer
func helperBoot(args []string) int {
	fs := flag.NewFlagSet("boot", flag.ContinueOnError)
	hang := fs.Bool("hang", false, "")
	wd := fs.Duration("watchdog", Watchdog, "")
	uidOffset := fs.Int("uid-offset", 0, "")
	child := fs.Bool("child", false, "")
	sigstate := fs.Bool("sigstate", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	watchdogAfter = *wd
	if *uidOffset != 0 {
		real := uint32(os.Getuid())
		ownUID = func() uint32 { return real + uint32(*uidOffset) }
	}
	newServer = func(o server.Options) (Server, error) {
		f, err := os.OpenFile(filepath.Join(o.StateDir, fakeLog), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		s := &fakeServer{log: f, hang: *hang, child: *child, sigstate: *sigstate, done: make(chan struct{})}
		s.record("new version=%s host=%t", o.Version, o.Host != "")
		return s, nil
	}
	return Run("test-version")
}

// helperSigstate prints "ignored=<bool>" for SIGHUP as this process
// inherited it.
func helperSigstate(args []string) int {
	fmt.Printf("ignored=%t\n", signal.Ignored(syscall.SIGHUP))
	return 0
}

// fakeServer stands in for server.Server (P1-13, P1-16) in the helper.
type fakeServer struct {
	mu       sync.Mutex
	log      *os.File
	hang     bool
	child    bool
	sigstate bool

	l        net.Listener
	stopping atomic.Bool
	done     chan struct{}
}

func (s *fakeServer) record(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.log, format+"\n", args...)
	s.log.Sync()
}

func (s *fakeServer) Serve(l net.Listener) error {
	s.mu.Lock()
	s.l = l
	s.mu.Unlock()
	s.record("serve")
	if s.child {
		argv, env := testkit.HelperCommand("sighup")
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			s.record("child error %v", err)
		} else {
			s.record("child %d", cmd.Process.Pid)
		}
	}
	if s.sigstate {
		argv, env := testkit.HelperCommand("sigstate")
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		s.record("sigstate %s %v", strings.TrimSpace(string(out)), err)
	}
	s.record("ready")
	for {
		c, err := l.Accept()
		if err != nil {
			if s.stopping.Load() {
				<-s.done
				return nil
			}
			return err
		}
		s.record("accept")
		c.Write([]byte("ok\n"))
		c.Close()
	}
}

func (s *fakeServer) Shutdown() {
	s.record("shutdown")
	if s.hang {
		select {}
	}
	if !s.stopping.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	l := s.l
	s.mu.Unlock()
	if l != nil {
		l.Close()
	}
	s.record("saved")
	close(s.done)
}

// --- the side of the test ---

// syncBuffer collects the stderr of a helper.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// bootProc is one helper "boot" process.
type bootProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stderr *syncBuffer
	exited chan struct{}
	code   int
	sig    syscall.Signal
}

// world is a home directory for one test and the paths Run derives from
// it.
type world struct {
	home, stateDir, socket, lock string
}

func newWorld(t *testing.T) world {
	t.Helper()
	home := testkit.ShortTempDir(t)
	state := filepath.Join(home, ".local", "state", "devtools")
	return world{
		home:     home,
		stateDir: state,
		socket:   filepath.Join(state, "server.sock"),
		lock:     filepath.Join(state, LockName),
	}
}

// env is the environment of a helper "boot" in this world; each extra
// replaces or adds a variable, and "NAME" alone removes it. GORACE drops
// the second the race runtime waits at every exit, so that a run under
// -race does not spend a second per server.
func (w world) env(extra ...string) []string {
	_, helper := testkit.HelperCommand("boot")
	helper = append(helper, "GORACE=atexit_sleep_ms=0")
	var add, drop []string
	for _, kv := range extra {
		if strings.Contains(kv, "=") {
			add = append(add, kv)
		} else {
			drop = append(drop, kv)
		}
	}
	env := testkit.CleanEnv(w.home, append(helper, add...)...)
	out := env[:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, d := range drop {
			if d == name {
				keep = false
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// start starts a helper "boot" with env and args. The process is killed
// when the test ends, if it still runs.
func start(t *testing.T, env []string, args ...string) *bootProc {
	t.Helper()
	argv, _ := testkit.HelperCommand("boot", args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = "/"
	p := &bootProc{t: t, cmd: cmd, stderr: &syncBuffer{}, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	go func() {
		cmd.Wait()
		p.code, p.sig = testkit.ExitStatus(cmd.ProcessState)
		close(p.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.exited:
		default:
			cmd.Process.Kill()
			<-p.exited
		}
	})
	return p
}

// wait waits for the process to end and returns its exit code; a signal
// or a timeout fails the test.
func (p *bootProc) wait(timeout time.Duration) int {
	p.t.Helper()
	select {
	case <-p.exited:
	case <-time.After(timeout):
		p.t.Fatalf("the server did not exit within %v; stderr:\n%s", timeout, p.stderr.String())
	}
	if p.sig != 0 {
		p.t.Fatalf("the server died of %v; stderr:\n%s", p.sig, p.stderr.String())
	}
	return p.code
}

func (p *bootProc) running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

func (p *bootProc) signal(sig syscall.Signal) {
	p.t.Helper()
	if err := p.cmd.Process.Signal(sig); err != nil {
		p.t.Fatalf("signal %v: %v", sig, err)
	}
}

// refused runs a helper that must fail a start step: it returns the one
// line it wrote on stderr.
func refused(t *testing.T, env []string, args ...string) string {
	t.Helper()
	p := start(t, env, args...)
	if code := p.wait(10 * time.Second); code != 1 {
		t.Fatalf("exit status %d, want 1; stderr:\n%s", code, p.stderr.String())
	}
	out := p.stderr.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("stderr has %d lines, want exactly one:\n%s", len(lines), out)
	}
	return lines[0]
}

// greet connects to the socket and reads the fake server's "ok".
func greet(socket string) error {
	c, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return err
	}
	if line != "ok\n" {
		return fmt.Errorf("got %q", line)
	}
	return nil
}

// ready waits until the server of p answers at the socket.
func (w world) ready(t *testing.T, p *bootProc) {
	t.Helper()
	testkit.Eventually(t, 10*time.Second, "the server answers at its socket", func() bool {
		if !p.running() {
			t.Fatalf("the server exited early (status %d); stderr:\n%s", p.code, p.stderr.String())
		}
		return greet(w.socket) == nil
	})
}

// log returns what the fake server recorded.
func (w world) log() string {
	b, _ := os.ReadFile(filepath.Join(w.stateDir, fakeLog))
	return string(b)
}

func (w world) logHas(line string) bool {
	for _, l := range strings.Split(w.log(), "\n") {
		if l == line {
			return true
		}
	}
	return false
}
