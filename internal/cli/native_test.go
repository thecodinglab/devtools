package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"devtools/internal/backend"
	"devtools/internal/devgit"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
	"devtools/internal/ui"
)

// N1: attach in a devtools pane starts no client; the window's hub opens
// and the command says it is already inside devtools.
func TestAttachInsideAPaneOpensTheHubOfTheWindowInstead(t *testing.T) {
	w := newWorld(t)
	w.inDevtools()
	r := runCommand("attach")
	wantStatus(t, r, 0)
	if got := strings.TrimSpace(r.stdout); got != alreadyInside {
		t.Fatalf("stdout = %q, want %q", got, alreadyInside)
	}
	got := w.serverWrites()
	if len(got) != 1 || !strings.HasPrefix(got[0], "ui.hub ") || !strings.Contains(got[0], `"mode":"all"`) {
		t.Fatalf("requests = %q, want one ui.hub in mode all", got)
	}
	w.noClient()
}

// N2: in a pane pick, sessions, agents and work each send one ui.hub with
// its scope and the pane id; no client starts.
func TestUICommandsInsideAPaneAskTheWindowsClient(t *testing.T) {
	for _, c := range []struct {
		args []string
		mode string
	}{
		{[]string{"pick"}, proto.HubAll},
		{[]string{"sessions"}, proto.HubOpen},
		{[]string{"agents"}, proto.HubAgents},
		{[]string{"work"}, proto.HubNew},
	} {
		t.Run(c.args[0], func(t *testing.T) {
			w := newWorld(t)
			w.inDevtools()
			wantStatus(t, runCommand(c.args...), 0)
			got := w.serverWrites()
			if len(got) != 1 || !strings.Contains(got[0], `"mode":"`+c.mode+`"`) {
				t.Fatalf("requests = %q, want one ui.hub in mode %s", got, c.mode)
			}
			paned := 0
			for _, h := range w.server.Hellos() {
				if h.Pane == "p1" {
					paned++
				}
			}
			if paned != 1 {
				t.Fatalf("%d hellos named the pane, want the one of the ui.hub", paned)
			}
			w.noClient()
		})
	}
}

// N5: a plain terminal that is a devtools pane on another machine (reached
// by ssh) answers XTVERSION with devtools: every UI command refuses with
// the nested words and the host, exits 1 and asks nothing of the backend.
func TestANestedAnswerBecomesARefusalForUICommands(t *testing.T) {
	for _, args := range [][]string{{"attach"}, {"pick"}, {"sessions"}, {"agents"}} {
		t.Run(args[0], func(t *testing.T) {
			w := newWorld(t)
			w.use(mux.Devtools)
			scriptedTerminal(t, devtoolsPaneAnswer)
			wantResult(t, runCommand(args...), 1, ui.Message("nested", "devtools", "macmini"))
			w.nothingRan()
		})
	}
}

// N6: in a devtools pane of D, ssh M, then devtools new: the worktree is
// made on M, the session opened on M's server, no client, exit 0.
func TestNewOverSSHFromAPaneOpensWithoutAttaching(t *testing.T) {
	w := newWorld(t)
	w.use(mux.Devtools)
	scriptedTerminal(t, devtoolsPaneAnswer)
	r := runCommand("new", "feature-x")
	wantStatus(t, r, 0)
	wantContains(t, "stderr", r.stderr, ui.Message("nested", "devtools", "macmini"))
	got := w.serverWrites()
	if len(got) != 1 || !strings.HasPrefix(got[0], "session.open ") || !strings.Contains(got[0], "feature-x") {
		t.Fatalf("requests = %q, want one session.open", got)
	}
	if _, err := os.Stat(filepath.Join(w.root, "feature-x")); err != nil {
		t.Fatalf("the project was not made: %v", err)
	}
	w.noClient()
}

// N15: the actions handed to the backend create a feature and merge a
// worktree with git only; the multiplexer is not asked anything.
func TestHubActionsUseTheInClientMux(t *testing.T) {
	// the backend today's default chose here, named since the cutover
	// made devtools the default (P2-09)
	t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
	root := t.TempDir()
	t.Setenv("DEVTOOLS_ROOT", root)
	identity(t)
	clone, err := devgit.Clone(root, createRemote(t), "widgets")
	if err != nil {
		t.Fatal(err)
	}
	r := &recordingMux{name: mux.Tmux}
	useRecording(t, r)
	a := &app{stdout: io.Discard, stderr: io.Discard}
	if _, _, err := a.multiplexer(); err != nil {
		t.Fatal(err)
	}
	actions := r.deps.Actions
	if actions == nil {
		t.Fatal("no hub actions were handed to the backend")
	}
	item, err := actions.Create(hub.CreateRequest{Root: root, Project: "widgets", Branch: "feature/api", From: "main", FromPath: clone.WorktreePath})
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != hub.KindWorktree || item.Session != "widgets-feature-api" {
		t.Fatalf("created %+v", item)
	}
	commitFile(t, item.Path, "api.txt", "api\n", "api")
	main, err := actions.Merge(item)
	if err != nil {
		t.Fatal(err)
	}
	if main.Path != clone.WorktreePath || main.Session != "widgets-main" {
		t.Fatalf("merged into %+v, want main", main)
	}
	if _, err := os.Stat(item.Path); !os.IsNotExist(err) {
		t.Fatalf("the feature worktree is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone.WorktreePath, "api.txt")); err != nil {
		t.Fatalf("main lacks the feature's commit: %v", err)
	}
	wantCalls(t, r)
}

// N26: devtools selected, no server answering, tmux installed: the
// noserver words with the tmux line, status 1, and tmux never runs, with
// and without machine lines (F45).
func TestNoServerNamesTheTmuxFallbackOnlyAsWords(t *testing.T) {
	for _, machines := range [][]string{nil, {"machine macmini", "machine server"}} {
		t.Run(fmt.Sprintf("%d machine lines", len(machines)), func(t *testing.T) {
			w := newWorld(t)
			t.Setenv("DEVTOOLS_SOCKET", filepath.Join(t.TempDir(), "no.sock"))
			w.use(mux.Devtools)
			if machines != nil {
				w.config(machines...)
			}
			scriptedTerminal(t, ordinaryTerminal)
			wantResult(t, runCommand("attach"), 1, ui.Message("noserver", "tmux"))
			w.nothingRan()
			if len(w.rec.Opened()) != 0 {
				t.Fatalf("a backend was opened: %v", w.rec.Opened())
			}
		})
	}
}

// Every command that opens a hub goes through enter on every backend:
// inside the backend Hub once and Enter never, in a plain terminal Enter
// once with the hub; and no non-test file outside hub.go calls the legacy
// hub.
func TestHubCommandsGoThroughEnterOnEveryBackend(t *testing.T) {
	for _, b := range backends {
		for _, args := range [][]string{{"pick"}, {"sessions"}, {"switch"}, {"work"}} {
			t.Run(string(b)+"/"+args[0], func(t *testing.T) {
				w := newWorld(t)
				r := &recordingMux{name: b}
				useRecording(t, r)
				w.inside(b)
				wantStatus(t, runCommand(args...), 0)
				if r.count("Hub") != 1 || r.count("Enter") != 0 {
					t.Fatalf("inside %s: calls %q, want Hub once", b, r.Calls())
				}

				w = newWorld(t)
				r = &recordingMux{name: b}
				useRecording(t, r)
				w.use(b)
				scriptedTerminal(t, ordinaryTerminal)
				wantStatus(t, runCommand(args...), 0)
				calls := r.Calls()
				if len(calls) != 1 || !strings.HasPrefix(calls[0], "Enter valid=true hub ") {
					t.Fatalf("in a plain terminal: calls %q, want Enter once with a hub", calls)
				}
			})
		}
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
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
			var fn string
			switch e := call.Fun.(type) {
			case *ast.Ident:
				fn = e.Name
			case *ast.SelectorExpr:
				fn = e.Sel.Name
			}
			if fn == "openHub" || fn == "openHubWith" || fn == "runHub" {
				t.Errorf("%s: a call of the legacy hub %s", fset.Position(call.Pos()), fn)
			}
			return true
		})
	}
}

// T1 for the standalone hub (10.4): devtools pick on a pty inside the fake
// tmux and inside the fake herdr, both answering at once; the first local
// row is on the pty within 100 ms of the start.
func TestHubFirstLocalRow(t *testing.T) {
	for _, b := range []mux.Backend{mux.Tmux, mux.Herdr} {
		t.Run(string(b), func(t *testing.T) {
			w := newWorld(t)
			w.inside(b)
			term := scriptedTerminal(t, "")
			start := time.Now()
			done := make(chan result, 1)
			go func() { done <- runCommand("pick") }()
			term.waitFor(t, "small")
			elapsed := time.Since(start)
			term.typeKeys(t, "\x1b")
			select {
			case r := <-done:
				wantStatus(t, r, 0)
			case <-time.After(10 * time.Second):
				t.Fatal("pick did not end")
			}
			t.Logf("first local row after %v", elapsed)
			if elapsed > 100*time.Millisecond {
				t.Fatalf("the first local row took %v, want at most 100 ms", elapsed)
			}
		})
	}
}

// A final show never fails a command whose git work succeeded, on a
// backend with detached-open: without a terminal, with a refusing guard,
// and in a pane no window shows.
func TestShowNeverFailsACommandWhoseGitWorkSucceeded(t *testing.T) {
	t.Run("tmux without a terminal", func(t *testing.T) {
		w := newWorld(t)
		w.use(mux.Tmux)
		wantStatus(t, runCommand("new", "fresh"), 0)
		if !hasCall(w.tmux(), "new-session -d -s fresh-main") {
			t.Fatalf("tmux = %q", w.tmux())
		}
	})
	t.Run("tmux, the guard refuses", func(t *testing.T) {
		w := newWorld(t)
		w.use(mux.Tmux)
		scriptedTerminal(t, tmuxPane)
		wantStatus(t, runCommand("new", "fresh"), 0)
	})
	t.Run("devtools, no window shows the pane", func(t *testing.T) {
		w := newWorld(t)
		w.server.set(func(f *fakeServer) { f.noClient = true })
		w.inDevtools()
		wantStatus(t, runCommand("new", "fresh"), 0)
		if _, err := os.Stat(filepath.Join(w.root, "fresh")); err != nil {
			t.Fatal(err)
		}
	})
}

// devtools pane focus: 0 moved, 1 no pane that way, 2 an error or outside a
// pane; the unsupported words and 1 on a backend without pane-focus.
func TestPaneFocusExitCodes(t *testing.T) {
	focus := []string{"pane", "focus", "--direction", "left"}
	t.Run("moved", func(t *testing.T) {
		w := newWorld(t)
		w.server.set(func(f *fakeServer) { f.moved = true })
		w.inDevtools()
		wantResult(t, runCommand(focus...), 0, "")
	})
	t.Run("no pane that way", func(t *testing.T) {
		w := newWorld(t)
		w.inDevtools()
		wantResult(t, runCommand(focus...), 1, "")
	})
	t.Run("the server refuses the pane", func(t *testing.T) {
		w := newWorld(t)
		w.inDevtools()
		w.server.set(func(f *fakeServer) { f.refuse = proto.CodeNoPane })
		wantStatus(t, runCommand(focus...), 2)
	})
	t.Run("outside a pane", func(t *testing.T) {
		w := newWorld(t)
		w.use(mux.Devtools)
		wantStatus(t, runCommand(focus...), 2)
		if got := w.serverWrites(); len(got) != 0 {
			t.Fatalf("requests = %q", got)
		}
	})
	t.Run("a wrong direction", func(t *testing.T) {
		w := newWorld(t)
		w.inDevtools()
		wantStatus(t, runCommand("pane", "focus", "--direction", "sideways"), 2)
	})
	t.Run("tmux", func(t *testing.T) {
		w := newWorld(t)
		w.refusedHere(t, mux.Tmux, mux.CapPaneFocus, focus...)
	})
}

// jsonKeys are the field names of v as the wire encodes it.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AG4: the JSON lists use the wire field names; agents --json adds
// "machine":"".
func TestJSONListsUseTheWireFieldNames(t *testing.T) {
	snap := worldSnapshot("/work/small")
	agentKeys := append(jsonKeys(t, snap.Agents[0]), "machine")
	sort.Strings(agentKeys)
	for _, c := range []struct {
		args []string
		keys []string
	}{
		{[]string{"session", "list", "--json"}, jsonKeys(t, snap.Sessions[0])},
		{[]string{"pane", "list", "--json"}, jsonKeys(t, snap.Panes[0])},
		{[]string{"agents", "--json"}, agentKeys},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			w := newWorld(t)
			w.use(mux.Devtools)
			r := runCommand(c.args...)
			wantStatus(t, r, 0)
			lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
			for _, line := range lines {
				var m map[string]any
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					t.Fatalf("not one JSON object per line: %q", line)
				}
				var keys []string
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				if !slices.Equal(keys, c.keys) {
					t.Fatalf("fields %v, want the wire's %v", keys, c.keys)
				}
				if c.args[0] == "agents" && m["machine"] != "" {
					t.Fatalf("machine = %v, want \"\"", m["machine"])
				}
			}
		})
	}
}

// No server: the noserver words, never a Go error or a trace.
func TestNoServerIsAHintNotAStackTrace(t *testing.T) {
	for _, args := range [][]string{{"attach"}, {"switch", "small"}, {"session", "list", "--json"}} {
		t.Run(args[0], func(t *testing.T) {
			w := newWorld(t)
			t.Setenv("DEVTOOLS_SOCKET", filepath.Join(t.TempDir(), "no.sock"))
			w.use(mux.Devtools)
			r := runCommand(args...)
			wantResult(t, r, 1, ui.Message("noserver", "tmux"))
			for _, bad := range []string{"goroutine", "dial unix", "panic", ".go:"} {
				if strings.Contains(r.stderr, bad) {
					t.Fatalf("stderr holds %q: %q", bad, r.stderr)
				}
			}
		})
	}
}

// A command in a pane no window shows (a background process) opens the
// session, says so in two lines, and succeeds.
func TestABackgroundCommandPrintsNoClientAndSucceeds(t *testing.T) {
	w := newWorld(t)
	w.server.set(func(f *fakeServer) { f.noClient = true })
	w.inDevtools()
	r := runCommand("switch", "small")
	wantResult(t, r, 0, ui.Message("no-client")+"\n"+ui.Message("opened-only", "small-main"))
	dir, name := w.switchSmall()
	want := []string{
		`ui.switch {"dir":"` + dir + `","name":"` + name + `"}`,
		`session.open {"dir":"` + dir + `","name":"` + name + `"}`,
	}
	if got := w.serverWrites(); !slices.Equal(got, want) {
		t.Fatalf("requests = %q, want %q", got, want)
	}
	w.noClient()
}

// executeWith runs the command tree with stdin, as Run does.
func executeWith(stdin string, args ...string) result {
	var stdout, stderr bytes.Buffer
	cmd := newRootCommand(&stdout, &stderr)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	code := 0
	if err := cmd.Execute(); err != nil {
		code = 1
		var e *exit
		if ok := errorsAs(err, &e); ok {
			code = e.code
		}
	}
	return result{code, stdout.String(), stderr.String()}
}

func errorsAs(err error, target **exit) bool {
	e, ok := errAs[*exit](err)
	if ok {
		*target = e
	}
	return ok
}

// server restart asks [y/N] when programs run, unless --yes.
func TestServerRestartAsksUnlessYes(t *testing.T) {
	restarts := 0
	saved := restartService
	restartService = func() error { restarts++; return nil }
	t.Cleanup(func() { restartService = saved })
	newWorld(t)
	question := ui.Message("restart", "2", "1")

	r := executeWith("n\n", "server", "restart")
	if r.code != 1 || strings.TrimSpace(r.stdout) != question || restarts != 0 {
		t.Fatalf("answered n: %+v, %d restarts; want the question, status 1 and none", r, restarts)
	}
	r = executeWith("\n", "server", "restart")
	if r.code != 1 || restarts != 0 {
		t.Fatalf("answered nothing: %+v, %d restarts", r, restarts)
	}
	r = executeWith("y\n", "server", "restart")
	if r.code != 0 || strings.TrimSpace(r.stdout) != question || restarts != 1 {
		t.Fatalf("answered y: %+v, %d restarts", r, restarts)
	}
	r = executeWith("", "server", "restart", "--yes")
	if r.code != 0 || r.stdout != "" || restarts != 2 {
		t.Fatalf("--yes: %+v, %d restarts; want no question", r, restarts)
	}
}

// A bind line with an action outside the vocabulary is a start error that
// names the line and the chord.
func TestUnknownActionIsAStartErrorNamingTheChord(t *testing.T) {
	w := newWorld(t)
	w.config("# keys", "bind prefix+z zoom")
	w.use(mux.Devtools)
	r := runCommand("keys")
	wantStatus(t, r, 1)
	wantContains(t, "stderr", r.stderr, "line 2")
	wantContains(t, "stderr", r.stderr, "prefix+z")
}

// A DEVTOOLS_PANE the server does not know is a dead mark: Context.Dead
// holds it, the command runs as in a plain terminal, and the guard does
// not refuse on the variable.
func TestAStalePaneVariableIsADeadMark(t *testing.T) {
	w := newWorld(t)
	t.Setenv(proto.EnvPane, "p9")
	w.use(mux.Tmux)
	scriptedTerminal(t, ordinaryTerminal)
	r := &recordingMux{name: mux.Tmux}
	useRecording(t, r)
	wantStatus(t, runCommand("attach"), 0)
	if r.ctx.Where != mux.InTerminal || !slices.Contains(r.ctx.Dead, proto.EnvPane) {
		t.Fatalf("context = %+v, want a plain terminal with DEVTOOLS_PANE dead", r.ctx)
	}
	wantCalls(t, r, "Enter valid=true last")
}

// N18: the server accepts and does not answer within 500 ms, the config
// says multiplexer tmux: the mark is not dead, the command stays with
// devtools and prints the noserver words; tmux never runs.
func TestAnUnansweredPaneVariableIsNotADeadMark(t *testing.T) {
	w := newWorld(t)
	t.Setenv("DEVTOOLS_SOCKET", silentServer(t))
	w.inDevtools()
	w.config("multiplexer tmux")
	c := mux.Detect(backend.Facts(os.Environ(), terminalIn, terminalOut))
	if c.Where != mux.InDevtools || len(c.Dead) != 0 {
		t.Fatalf("context = %+v, want inside devtools and nothing dead", c)
	}
	wantResult(t, runCommand("switch", "small"), 1, ui.Message("noserver", "tmux"))
	w.nothingRan()
}

// devtools keys prints every effective binding: the defaults after the
// unbind and bind lines, then the keys of the hub and of copy mode.
func TestKeysPrintsEveryEffectiveBinding(t *testing.T) {
	w := newWorld(t)
	w.config("unbind prefix+c", "bind prefix+y tab-new")
	w.use(mux.Devtools)
	r := runCommand("keys")
	wantStatus(t, r, 0)
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	fields := func(line string) []string { return strings.Fields(line) }
	has := func(chord, action string) bool {
		for _, l := range lines {
			f := fields(l)
			if len(f) >= 2 && f[0] == chord && f[1] == action {
				return true
			}
		}
		return false
	}
	for _, want := range [][2]string{{"ctrl+space", "prefix"}, {"prefix+o", "hub"}, {"prefix+y", "tab-new"}, {"prefix+d", "detach"}, {"ctrl+n", "hub"}, {"y", "copy-mode"}} {
		if !has(want[0], want[1]) {
			t.Errorf("no line %s %s in\n%s", want[0], want[1], r.stdout)
		}
	}
	if has("prefix+c", "tab-new") {
		t.Errorf("the unbound prefix+c is printed:\n%s", r.stdout)
	}
	if n := len(lines); n != 38-1+1+len(hubKeys)+len(copyKeys) {
		t.Errorf("%d lines, want the 38 defaults less one plus one, and the hub and copy keys", n)
	}
	w.nothingRan()
}

// agents --json says in its help that it lists this machine only (F34).
func TestAgentsJSONHelpSaysThisMachineOnly(t *testing.T) {
	r := runCommand("agents", "--help")
	wantStatus(t, r, 0)
	if !strings.Contains(r.stdout, "this machine only") {
		t.Fatalf("help = %q", r.stdout)
	}
}

// The gate runs before the command: a refused command made no worktree,
// sent no request and called nothing on the multiplexer.
func TestTheGateRunsBeforeTheCommand(t *testing.T) {
	for _, args := range [][]string{{"agents"}, {"agents", "--json"}, {"keys"}, {"session", "list", "--json"}, {"pane", "list", "--json"}, {"pane", "focus", "--direction", "up"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			w := newWorld(t)
			r := &recordingMux{name: mux.Tmux}
			useRecording(t, r)
			w.inTmux()
			res := runCommand(args...)
			wantStatus(t, res, 1)
			wantContains(t, "stderr", res.stderr, "tmux cannot ")
			wantCalls(t, r)
			w.nothingRan()
			entries, err := os.ReadDir(w.root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("the root holds %v, want only small", entries)
			}
		})
	}
}

// server, server status, server restart and bridge open no backend,
// whatever multiplexer the caller sits in.
func TestServerCommandsOpenNoBackend(t *testing.T) {
	savedRestart, savedServer, savedBridge := restartService, runServer, runBridge
	t.Cleanup(func() { restartService, runServer, runBridge = savedRestart, savedServer, savedBridge })
	var ran []string
	restartService = func() error { ran = append(ran, "restart"); return nil }
	runServer = func(v string) int { ran = append(ran, "server "+v); return 0 }
	runBridge = func(in io.Reader, out io.Writer, socket string, tty bool) int {
		ran = append(ran, "bridge "+socket)
		return 0
	}
	for _, b := range backends {
		t.Run(string(b), func(t *testing.T) {
			ran = nil
			w := newWorld(t)
			w.inside(b)
			r := runCommand("server", "status", "--json")
			wantStatus(t, r, 0)
			var st map[string]any
			if err := json.Unmarshal([]byte(r.stdout), &st); err != nil {
				t.Fatalf("status --json = %q", r.stdout)
			}
			want := []string{"boot", "clients", "gen", "panes", "pid", "sessions", "socket", "version"}
			var keys []string
			for k := range st {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(keys, want) || st["socket"] != w.server.socket {
				t.Fatalf("status = %v", st)
			}
			for _, args := range [][]string{{"server", "status"}, {"server", "restart", "--yes"}, {"server"}, {"bridge"}} {
				wantStatus(t, runCommand(args...), 0)
			}
			if len(ran) != 3 || ran[0] != "restart" || !strings.HasPrefix(ran[1], "server ") || ran[2] != "bridge "+w.server.socket {
				t.Fatalf("ran %q", ran)
			}
			if got := w.rec.Opened(); len(got) != 0 {
				t.Fatalf("a backend was opened: %v", got)
			}
			if got := w.tmux(); len(got) != 0 {
				t.Fatalf("tmux ran %q", got)
			}
		})
	}
	t.Run("no server", func(t *testing.T) {
		newWorld(t)
		t.Setenv("DEVTOOLS_SOCKET", filepath.Join(t.TempDir(), "no.sock"))
		wantResult(t, runCommand("server", "status"), 1, ui.Message("noserver"))
	})
}

// AG3: agents in a pane is ui.hub in the agents scope; in a plain terminal
// the client starts with the hub in that scope.
func TestAgentsOpensTheHubInAgentsMode(t *testing.T) {
	w := newWorld(t)
	wantContains(t, "ui.hub", w.uiHub(t, "agents"), `"mode":"agents"`)

	w = newWorld(t)
	w.use(mux.Devtools)
	scriptedTerminal(t, ordinaryTerminal)
	wantStatus(t, runCommand("agents"), 0)
	if got := w.rec.Enters(); !slices.Equal(got, []string{fmt.Sprintf("Enter valid=true hub %d %q", hub.ModeAgents, "")}) {
		t.Fatalf("enters = %q", got)
	}
}
