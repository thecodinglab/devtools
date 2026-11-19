// Package integration holds the thin slice of K2 (6.7): the real server
// process with one real pane, driven over the wire, its frames drawn by the
// client's real row filter into the real canvas. It is a test-only package.
package integration

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/client/rowfilter"
	"devtools/internal/client/screen"
	"devtools/internal/pane"
	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server/boot"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
)

// TestMain turns the test binary into the server when the helper "server"
// is asked for: boot.Run, the body of `devtools server`, in a process of its
// own, so that its setsid, chdir, umask and signals never touch the test
// process. Every such process gets an environment from testkit.CleanEnv in
// a ShortTempDir: its socket and state directory are the test's.
func TestMain(m *testing.M) {
	testkit.RegisterHelper("server", func([]string) int { return boot.Run("0.0.0-slice") })
	testkit.RunHelper()

	home, err := os.MkdirTemp("", "devtools-integration-test")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "XDG_") ||
			strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "TMUX") {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// requireRealPanes fails the test at once when pane.Start is still the stub
// of wave 0: the slice needs the pane actor (P1-12).
func requireRealPanes(t *testing.T, home string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("pane.Start is not available (%v); the slice needs the pane actor of P1-12", r)
		}
	}()
	p := pane.Start(pane.Options{
		ID: "p0", Argv: []string{"/bin/sh", "-c", "exit 0"}, Dir: home,
		Env: testkit.CleanEnv(home), Cols: 20, Rows: 5, History: 100,
		Notify: func(pane.Event) {}, KillGrace: 250 * time.Millisecond, SyncHold: 150 * time.Millisecond,
	})
	p.Close()
}

// draw parses the rows of a pane's screen with the client's row filter and
// draws them into a canvas; it returns the canvas's text, one string per
// row, and the first row that did not parse.
func draw(t *testing.T, cols, rows int, lines []string) ([]string, error) {
	t.Helper()
	c := screen.New(cols, rows)
	for y, s := range lines {
		cells, err := rowfilter.Parse(s, cols)
		if err != nil {
			return nil, err
		}
		c.Cells(0, y, cells)
	}
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		var b strings.Builder
		for x := 0; x < cols; x++ {
			b.WriteString(c.At(x, y).S)
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out, nil
}

func canvasHas(rows []string, text string) bool {
	for _, r := range rows {
		if strings.Contains(r, text) {
			return true
		}
	}
	return false
}

func TestSliceOneRealPaneEndToEnd(t *testing.T) {
	testkit.RequirePTY(t)
	home := testkit.ShortTempDir(t)
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	requireRealPanes(t, home)

	argv, helper := testkit.HelperCommand("server")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = testkit.CleanEnv(home, append(helper, "GORACE=atexit_sleep_ms=0")...)
	cmd.Dir = home
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			cmd.Process.Kill()
			<-exited
		}
	})

	socket := filepath.Join(home, ".local", "state", "devtools", "server.sock")
	testkit.Eventually(t, 10*time.Second, "the server listens on "+socket, func() bool {
		select {
		case <-exited:
			t.Fatalf("the server exited early: %s", stderr.String())
		default:
		}
		nc, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return false
		}
		nc.Close()
		return true
	})

	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work, Name: "work", Cols: 80, Rows: 24})
	if len(v.Panes) != 1 {
		t.Fatalf("view.ok %+v", v)
	}
	p := v.Panes[0]

	// What the shell prints reaches the canvas, through the real filter.
	var shown []string
	shownWith := func(text string) func(s golden.Screen) bool {
		return func(s golden.Screen) bool {
			rows, err := draw(t, s.Cols, s.Rows, s.Lines)
			if err != nil {
				t.Fatalf("a row of the frame does not parse: %v", err)
			}
			shown = rows
			return canvasHas(rows, text)
		}
	}
	// A key sent over the wire arrives in the shell: the shell computes what
	// the canvas then shows, so the text cannot be the echo of the keys.
	c.Type(p, "echo slice-$((6*7))\r")
	c.WaitScreen(p, "the shell's answer", shownWith("slice-42"))
	if !canvasHas(shown, "echo slice-$((6*7))") {
		t.Fatalf("the typed line is not on the canvas:\n%s", strings.Join(shown, "\n"))
	}
	c.Type(p, "cd "+work+" && pwd\r")
	c.WaitScreen(p, "the directory", shownWith(work))

	// The orderly stop: SIGTERM, exit 0, the socket removed.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("the server did not stop: %s", stderr.String())
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the server exited %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("the socket is still there after the stop: %v", err)
	}
	c.WaitClosed()
}
