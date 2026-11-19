package pane

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// The identity every test terminal answers XTVERSION with.
const testVersion, testHost = "9.8.7-test", "test-host"

var testColors = vt.Colors{Fg: "#112233", Bg: "#445566", Dark: true}

// The helper programs of this package, beside those of testkit (10.1.3).
func init() {
	testkit.RegisterHelper("session", helperSession)
	testkit.RegisterHelper("tty", helperTTY)
	testkit.RegisterHelper("fg", helperFg)
	testkit.RegisterHelper("stubborn", helperStubborn)
	testkit.RegisterHelper("burst", helperBurst)
	testkit.RegisterHelper("rawsleep", helperRawSleep)
	testkit.RegisterHelper("ticks", helperTicks)
}

func TestMain(m *testing.M) {
	testkit.RunHelper()
	// Hermetic: nothing of the developer's multiplexer or devtools session
	// reaches a test (10.1).
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == testkit.NoPTY {
			continue
		}
		if strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "ZELLIJ") ||
			name == "TMUX" || name == "TMUX_PANE" || name == "STY" || name == "XDG_STATE_HOME" || name == "XDG_CONFIG_HOME" {
			os.Unsetenv(name)
		}
	}
	home, err := os.MkdirTemp("", "devtools-pane-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	vt.SetIdentity("devtools", testVersion, testHost)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// --- helpers of this package, run inside the pane ---

// helperSession prints the session, process group and foreground group of
// itself and its terminal, then waits for the end of its input.
func helperSession(args []string) int {
	fg, err := platform.ForegroundGroup(os.Stdin)
	if err != nil {
		fg = -1
	}
	sid, _ := unix.Getsid(0)
	fmt.Printf("sid=%d pid=%d pgrp=%d fg=%d\n", sid, os.Getpid(), unix.Getpgrp(), fg)
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// helperTTY prints DEVTOOLS_TTY, the name of its terminal, and whether the
// two are the same device, then waits for the end of its input.
func helperTTY(args []string) int {
	env := os.Getenv(proto.EnvTTY)
	name, err := platform.TTYName(os.Stdin)
	if err != nil {
		name = "error: " + err.Error()
	}
	fmt.Printf("env=%s\ntty=%s\nsame=%v\n", env, name, platform.SameTTY(os.Stdin, env))
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// helperFg starts the helper sighup in a process group of its own, puts that
// group in the foreground of the terminal, prints "child <pid>" and waits
// for it.
func helperFg(args []string) int {
	signal.Ignore(syscall.SIGTTOU)
	argv, env := testkit.HelperCommand("sighup")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		fmt.Printf("fg: %v\n", err)
		return 1
	}
	fmt.Printf("child %d\n", cmd.Process.Pid)
	cmd.Wait()
	return 0
}

// helperStubborn ignores SIGHUP and SIGTERM, prints "ready" and sleeps.
func helperStubborn(args []string) int {
	signal.Ignore(syscall.SIGHUP, syscall.SIGTERM)
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}

// helperBurst prints <n> lines, then "last words", and exits at once.
func helperBurst(args []string) int {
	n := 1000
	if len(args) > 0 {
		n, _ = strconv.Atoi(args[0])
	}
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "burst %d\n", i)
	}
	b.WriteString("last words\n")
	os.Stdout.WriteString(b.String())
	return 0
}

// rawNoRead puts the terminal in raw mode; the helper then never reads its
// input, so what is typed fills the kernel's buffer and the writer blocks.
func rawNoRead() {
	if fd := os.Stdin.Fd(); term.IsTerminal(fd) {
		term.MakeRaw(fd)
	}
}

// helperRawSleep puts its terminal in raw mode, prints "ready" and sleeps
// without reading its input.
func helperRawSleep(args []string) int {
	rawNoRead()
	os.Stdout.WriteString("ready\r\n")
	for {
		time.Sleep(time.Hour)
	}
}

// helperTicks puts its terminal in raw mode and prints "tick <n>" every 5
// ms without reading its input.
func helperTicks(args []string) int {
	rawNoRead()
	for i := 0; ; i++ {
		fmt.Printf("tick %06d\r\n", i)
		time.Sleep(5 * time.Millisecond)
	}
}

// --- test utilities ---

// recorder collects the events of a pane.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) notify(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *recorder) count(k EventKind) int {
	n := 0
	for _, e := range r.all() {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// options returns the options of a pane that runs the helper name with
// args in a clean environment, 200 by 24.
func options(t *testing.T, name string, args ...string) (Options, *recorder) {
	t.Helper()
	testkit.RequirePTY(t)
	home := testkit.ShortTempDir(t)
	argv, env := testkit.HelperCommand(name, args...)
	r := &recorder{}
	return Options{
		ID:        "p1",
		Argv:      argv,
		Dir:       home,
		Env:       testkit.CleanEnv(home, env...),
		Cols:      200,
		Rows:      24,
		History:   1000,
		Colors:    testColors,
		Notify:    r.notify,
		KillGrace: 100 * time.Millisecond,
	}, r
}

// start starts a pane that is closed when the test ends.
func start(t *testing.T, o Options) *Pane {
	t.Helper()
	p := Start(o)
	t.Cleanup(p.Close)
	return p
}

// startHelper starts a pane running the helper name.
func startHelper(t *testing.T, name string, args ...string) (*Pane, *recorder) {
	t.Helper()
	o, r := options(t, name, args...)
	return start(t, o), r
}

// screen returns the whole text of a pane, history and screen, with soft
// wraps joined.
func screen(p *Pane) string {
	s := p.Snapshot(0, false)
	text, _ := p.Text(vt.Pos{Line: s.First}, vt.Pos{Line: s.Base + int64(s.Rows) - 1, Col: s.Cols - 1}, false)
	return strings.TrimRight(text, "\n ")
}

// waitScreen waits until the pane's text contains want and returns it.
func waitScreen(t *testing.T, p *Pane, want string) string {
	t.Helper()
	var got string
	testkit.Eventually(t, 20*time.Second, fmt.Sprintf("%q on the screen", want), func() bool {
		got = screen(p)
		return strings.Contains(got, want)
	})
	return got
}

// afterReady returns what the pane shows after its line "ready".
func afterReady(p *Pane) string {
	_, after, _ := strings.Cut(screen(p), "ready\n")
	return strings.TrimSpace(after)
}

// waitBytes waits until a hex helper printed exactly want after "ready".
func waitBytes(t *testing.T, p *Pane, want string) {
	t.Helper()
	var got string
	ok := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if got = afterReady(p); got == want {
			ok = true
			break
		}
		time.Sleep(testkit.PollInterval)
	}
	if !ok {
		t.Fatalf("the program read %q, want %q", got, want)
	}
}

// hex writes s as the hex helper prints it.
func hex(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%02x", s[i])
	}
	return b.String()
}

// probe runs f on the actor and waits for it.
func probe(p *Pane, f func(a *actor)) {
	done := make(chan struct{})
	p.ops.push(func(a *actor) {
		f(a)
		close(done)
	})
	select {
	case <-done:
	case <-p.actorDone:
	}
}

// findLine returns the absolute number of the line whose text is text.
func findLine(t *testing.T, p *Pane, text string) int64 {
	t.Helper()
	s := p.Snapshot(0, false)
	for l := s.First; l < s.Base+int64(s.Rows); l++ {
		if got, _ := p.Text(vt.Pos{Line: l}, vt.Pos{Line: l, Col: s.Cols - 1}, false); got == text {
			return l
		}
	}
	t.Fatalf("no line %q in [%d, %d)", text, s.First, s.Base+int64(s.Rows))
	return 0
}

// waitExit waits until the pane reported its exit and returns its Info.
func waitExit(t *testing.T, p *Pane, r *recorder) Info {
	t.Helper()
	testkit.Eventually(t, 20*time.Second, "the Exited event", func() bool { return r.count(Exited) > 0 })
	return p.Info()
}
