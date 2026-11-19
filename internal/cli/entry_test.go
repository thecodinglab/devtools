package cli

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/testkit"
	"devtools/internal/ui"
)

// runApp runs f on a new app as Run runs a command: an error is printed on
// standard error and is status 1.
func runApp(f func(a *app) error) (code int, stderr string) {
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut}
	if err := f(a); err != nil {
		fmt.Fprintln(&errOut, err)
		return 1, errOut.String()
	}
	return 0, errOut.String()
}

func wantCalls(t *testing.T, r *recordingMux, want ...string) {
	t.Helper()
	if got := r.Calls(); !slices.Equal(got, want) {
		t.Fatalf("calls of the multiplexer = %q, want %q", got, want)
	}
}

// The answers of the probe (guard, 3.4 c). ordinaryTerminal stands in for
// a plain terminal that names itself.
//
// provisional E6: that ghostty answers XTVERSION so is UNVERIFIED; the
// fixture is the form of 3.4, as in the tests of internal/client/guard, not
// a recording (E6's rows with a real ghostty window are pending [GO]).
const (
	ordinaryTerminal = "\x1bP>|ghostty 1.3.1\x1b\\\x1b[?62;22c"
	tmuxPane         = "\x1bP>|tmux 3.8\x1b\\\x1b[?1;2;4c" // recorded by E6
	probeQueries     = "\x1b[>q\x1b[c"
)

// answeringTerminal makes a pty of testkit the terminal of the commands
// (stdin and stdout) and answers every probe the guard writes to it with
// answer, as a terminal does.
func answeringTerminal(t *testing.T, answer string) {
	t.Helper()
	master, slave := testkit.OpenPTY(t, 80, 24)
	savedIn, savedOut := terminalIn, terminalOut
	terminalIn, terminalOut = slave, slave
	t.Cleanup(func() { terminalIn, terminalOut = savedIn, savedOut })
	go func() {
		var seen []byte
		buf := make([]byte, 256)
		for {
			n, err := master.Read(buf)
			seen = append(seen, buf[:n]...)
			if bytes.Contains(seen, []byte(probeQueries)) {
				seen = nil
				if _, err := master.WriteString(answer); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

// N25: attach typed inside tmux opens the tmux hub in the pane and starts
// no client.
func TestAttachInsideTmuxOpensTheTmuxHub(t *testing.T) {
	installFakeTmux(t)
	insideFakeTmux(t)
	r := &recordingMux{name: mux.Tmux}
	useRecording(t, r)

	code, stderr := runApp(func(a *app) error { return a.enter(mux.Entry{}) })
	if code != 0 {
		t.Fatalf("enter returned %d, stderr=%s", code, stderr)
	}
	if !slices.Equal(r.opened, []mux.Backend{mux.Tmux}) || r.ctx.Where != mux.InTmux {
		t.Fatalf("opened %v in %+v, want tmux inside tmux", r.opened, r.ctx)
	}
	wantCalls(t, r, fmt.Sprintf("Hub %d %q", hub.ModeAll, ""))
	if n := r.count("Enter"); n != 0 {
		t.Fatalf("Enter was called %d times inside tmux", n)
	}
}

// N3: inside screen or zellij a UI command refuses and touches nothing; a
// command that shows a session opens it without a window and says why.
func TestUICommandsInsideAForeignMultiplexerRefuse(t *testing.T) {
	for _, tt := range []struct{ name, variable, value string }{
		{"screen", "STY", "1234.pts-0.host"},
		{"zellij", "ZELLIJ", "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.variable, tt.value)
			// the backend today's default chose here, named since the cutover
			// made devtools the default (P2-09)
			t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
			othermux := ui.Message("othermux", "tmux", tt.name)

			r := &recordingMux{name: mux.Tmux}
			useRecording(t, r)
			code, stderr := runApp(func(a *app) error { return a.enter(mux.Entry{}) })
			if code != 1 || strings.TrimSpace(stderr) != othermux {
				t.Fatalf("enter = %d, %q; want 1 and %q", code, stderr, othermux)
			}
			wantCalls(t, r)

			r = &recordingMux{name: mux.Tmux, showShown: mux.OpenedOnly}
			useRecording(t, r)
			dir := t.TempDir()
			code, stderr = runApp(func(a *app) error {
				_, err := a.show(dir, "small-main")
				return err
			})
			if code != 0 || strings.TrimSpace(stderr) != othermux {
				t.Fatalf("show = %d, %q; want 0 and %q", code, stderr, othermux)
			}
			wantCalls(t, r, "Show "+dir+" small-main")
		})
	}
}

// N4: devtools new inside tmux creates the session detached and switches
// the tmux client to it; it never starts a tmux client.
func TestNewInsideTmuxSwitchesTheTmuxClient(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DEVTOOLS_ROOT", root)
	log := installFakeTmux(t)
	insideFakeTmux(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"new", "fresh"}, &stdout, &stderr); code != 0 {
		t.Fatalf("new returned %d, stderr=%s", code, stderr.String())
	}
	project := filepath.Join(root, "fresh")
	want := []string{
		"has-session -t fresh-main",
		"new-session -d -s fresh-main -c " + project,
		"switch-client -t fresh-main",
	}
	got := tmuxCommands(t, log)
	if !slices.Equal(got, want) {
		t.Fatalf("tmux commands = %q, want %q", got, want)
	}
	for _, c := range got {
		if strings.HasPrefix(c, "new-session -A") || strings.HasPrefix(c, "attach-session") {
			t.Fatalf("tmux ran %q, which starts a client", c)
		}
	}
}

// In a plain terminal show enters the backend only with a pass from the
// guard; a terminal that is a tmux pane is refused, the session is opened
// without a window, and the command succeeds with the othermux words.
func TestShowInAPlainTerminalEntersOnlyWithAPass(t *testing.T) {
	dir := t.TempDir()

	t.Run("an ordinary terminal", func(t *testing.T) {
		// the backend today's default chose here, named since the cutover
		// made devtools the default (P2-09)
		t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
		answeringTerminal(t, ordinaryTerminal)
		r := &recordingMux{name: mux.Tmux}
		useRecording(t, r)
		code, stderr := runApp(func(a *app) error {
			_, err := a.show(dir, "small-main")
			return err
		})
		if code != 0 {
			t.Fatalf("show returned %d, stderr=%s", code, stderr)
		}
		if r.ctx.Where != mux.InTerminal {
			t.Fatalf("context = %+v, want InTerminal", r.ctx)
		}
		wantCalls(t, r, "Enter valid=true target "+dir+" small-main")
		if len(r.passes) != 1 || !r.passes[0].Valid() {
			t.Fatalf("passes = %v, want one valid pass", r.passes)
		}
	})

	t.Run("a tmux pane across ssh", func(t *testing.T) {
		// the backend today's default chose here, named since the cutover
		// made devtools the default (P2-09)
		t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
		answeringTerminal(t, tmuxPane)
		r := &recordingMux{name: mux.Tmux, showShown: mux.OpenedOnly}
		useRecording(t, r)
		code, stderr := runApp(func(a *app) error {
			_, err := a.show(dir, "small-main")
			return err
		})
		if want := ui.Message("othermux", "tmux", "tmux"); code != 0 || strings.TrimSpace(stderr) != want {
			t.Fatalf("show = %d, %q; want 0 and %q", code, stderr, want)
		}
		wantCalls(t, r, "Show "+dir+" small-main")
	})
}

// Without a terminal enter starts nothing and says why.
func TestEnterWithoutATerminalSaysSo(t *testing.T) {
	// the backend today's default chose here, named since the cutover
	// made devtools the default (P2-09)
	t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
	r := &recordingMux{name: mux.Tmux}
	useRecording(t, r)
	code, stderr := runApp(func(a *app) error { return a.enter(mux.Entry{}) })
	if want := ui.Message("notty"); code != 1 || strings.TrimSpace(stderr) != want {
		t.Fatalf("enter = %d, %q; want 1 and %q", code, stderr, want)
	}
	if r.ctx.Where != mux.NoTerminal {
		t.Fatalf("context = %+v, want NoTerminal", r.ctx)
	}
	wantCalls(t, r)
}

// A backend that cannot open a session without a window (herdr outside
// herdr) fails a work command after its git work, with the words that say
// the session was not opened.
func TestWorkWithoutDetachedOpenFailsAfterTheGitWork(t *testing.T) {
	// the backend today's default chose here, named since the cutover
	// made devtools the default (P2-09)
	t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
	root := t.TempDir()
	remote := createRemote(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	unsupported := &mux.ErrUnsupported{Backend: mux.Herdr, Capability: mux.CapDetached}
	words := ui.Message("unsupported", "herdr", "detached-open")
	r := &recordingMux{name: mux.Herdr, showErr: unsupported}
	useRecording(t, r)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 1 {
		t.Fatalf("clone returned %d, want 1; stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	stderr.Reset()
	if code := Run([]string{"work", "feature/x"}, &stdout, &stderr); code != 1 {
		t.Fatalf("work returned %d, want 1; stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-x")
	if _, err := os.Stat(featurePath); err != nil {
		t.Fatalf("the worktree of work is missing: %v", err)
	}
	if got := strings.TrimSpace(stderr.String()); got != words {
		t.Fatalf("stderr = %q, want %q", got, words)
	}
	if n := r.count("Show"); n != 2 {
		t.Fatalf("Show was called %d times, want once per command", n)
	}
}

// The errors of the selection are words and status 1, before the command
// does anything.
func TestSelectionErrorsAreWordsAndStatusOne(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{"mismatch", func(t *testing.T) {
			installFakeHerdr(t, "")
			t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
		}, ui.Message("mismatch", "tmux", "herdr")},
		{"unavailable", func(t *testing.T) {
			t.Setenv("DEVTOOLS_MULTIPLEXER", "herdr")
		}, ui.Message("unavailable", "herdr")},
		// P2-08b: no server answers, so the noserver words; TestMain puts
		// a program named tmux on PATH, so with the tmux line
		{"unavailable devtools", func(t *testing.T) {
			t.Setenv("DEVTOOLS_MULTIPLEXER", "devtools")
		}, ui.Message("noserver", "tmux")},
		{"ambiguous", func(t *testing.T) {
			installFakeHerdr(t, "")
			installFakeTmux(t)
			insideFakeTmux(t)
		}, ui.Message("ambiguous", "tmux", "herdr")},
		{"unknown name", func(t *testing.T) {
			t.Setenv("DEVTOOLS_MULTIPLEXER", "screen")
		}, `DEVTOOLS_MULTIPLEXER: unknown multiplexer "screen", want devtools, tmux or herdr`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DEVTOOLS_ROOT", root)
			tt.setup(t)
			r := &recordingMux{}
			useRecording(t, r)
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"new", "fresh"}, &stdout, &stderr); code != 1 {
				t.Fatalf("new returned %d, want 1; stderr=%s", code, stderr.String())
			}
			if got := strings.TrimSpace(stderr.String()); got != tt.want {
				t.Fatalf("stderr = %q, want %q", got, tt.want)
			}
			if len(r.opened) != 0 {
				t.Fatalf("a backend was opened: %v", r.opened)
			}
			if _, err := os.Stat(filepath.Join(root, "fresh")); !os.IsNotExist(err) {
				t.Fatalf("new did its git work before the selection error: %v", err)
			}
		})
	}
}

// merge and done call the function Leave returned last, after the worktree
// is gone.
func TestLeaveCommandsCloseLast(t *testing.T) {
	// the backend today's default chose here, named since the cutover
	// made devtools the default (P2-09)
	t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
	for _, command := range [][]string{{"merge"}, {"done", "--force"}} {
		t.Run(command[0], func(t *testing.T) {
			root := t.TempDir()
			remote := createRemote(t)
			t.Setenv("DEVTOOLS_ROOT", root)
			identity(t)
			r := &recordingMux{name: mux.Tmux, showShown: mux.OpenedOnly}
			useRecording(t, r)
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
				t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
			}
			mainPath := filepath.Join(root, "widgets", "main")
			changeCwd(t, mainPath)
			if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
				t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
			}
			featurePath := filepath.Join(root, "widgets", "feature-test")
			commitFile(t, featurePath, "feature.txt", "feature\n", "feature")
			changeCwd(t, featurePath)

			goneAtClose := false
			r.closeLast = func() error {
				_, err := os.Stat(featurePath)
				goneAtClose = os.IsNotExist(err)
				return nil
			}
			r.mu.Lock()
			r.calls = nil
			r.mu.Unlock()
			if code := Run(command, &stdout, &stderr); code != 0 {
				t.Fatalf("%s returned %d, stderr=%s", command[0], code, stderr.String())
			}
			calls := r.Calls()
			if len(calls) != 2 || !strings.HasPrefix(calls[0], "Leave ") || calls[1] != "closeLast" {
				t.Fatalf("calls = %q, want Leave and then the function it returned", calls)
			}
			fields := strings.Fields(calls[0])
			if len(fields) != 4 || filepath.Base(fields[1]) != "feature-test" || filepath.Base(fields[2]) != "main" || fields[3] != "widgets-main" {
				t.Fatalf("Leave = %q, want the feature's dir, then main's dir and session", calls[0])
			}
			if !goneAtClose {
				t.Fatal("the function Leave returned ran while the worktree was still there")
			}
		})
	}
}

// Rule 10: a method named Enter is called in entry.go only, among the
// non-test files of internal/cli.
func TestEnterIsCalledOnlyInEntryGo(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	inEntry := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Enter" {
				return true
			}
			if name != "entry.go" {
				t.Errorf("%s: a call of Enter outside entry.go", fset.Position(call.Pos()))
			} else {
				inEntry++
			}
			return true
		})
	}
	if inEntry == 0 {
		t.Fatal("entry.go calls no Enter: the test would prove nothing")
	}
}
