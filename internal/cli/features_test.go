package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"devtools/internal/backend"
	"devtools/internal/config"
	"devtools/internal/devgit"
	"devtools/internal/mux"
	"devtools/internal/ui"

	"github.com/spf13/cobra"
)

// The outcomes of a cell of the feature table on a backend (10.2.7).
const (
	works   = "works"
	refused = string(mux.Refused)
	absent  = string(mux.Absent)
	ignored = string(mux.Ignored)
)

// outcomeOf is computed from the row and backend.CapsOf, never written
// into the test: works when the row needs nothing, when its need is late
// (cmd.work, which fails inside Show only) or when the backend has the
// capability; otherwise what the row says.
func outcomeOf(f mux.Feature, b mux.Backend) string {
	if f.Needs == "" || f.Late || backend.CapsOf(b).Has(f.Needs) {
		return works
	}
	return string(f.Without)
}

// refusedHere asserts the outcome error: status 1, exactly the unsupported
// words, and no fake saw a call.
func (w *world) refusedHere(t *testing.T, b mux.Backend, c mux.Capability, args ...string) {
	t.Helper()
	w.inside(b)
	wantResult(t, runCommand(args...), 1, ui.Message("unsupported", string(b), string(c)))
	if got := w.tmux(); len(got) != 0 {
		t.Errorf("the fake tmux ran %q", got)
	}
	if got := w.herdr(); got != "" {
		t.Errorf("the fake herdr ran %q", got)
	}
	if got := w.serverWrites(); len(got) != 0 {
		t.Errorf("the server was asked %q", got)
	}
}

// hubScreen runs a UI command inside b's pane, waits for text on the
// standalone hub and closes it; it returns the frames the hub drew.
func (w *world) hubScreen(t *testing.T, b mux.Backend, text string, args ...string) string {
	t.Helper()
	w.inside(b)
	term := scriptedTerminal(t, "")
	r := runWithHub(t, term, text, "\x1b", args...)
	wantStatus(t, r, 0)
	w.noClient()
	return term.Screen()
}

// uiHub runs a UI command inside a devtools pane and returns the one
// ui.hub request it sent.
func (w *world) uiHub(t *testing.T, args ...string) string {
	t.Helper()
	w.inDevtools()
	wantStatus(t, runCommand(args...), 0)
	got := w.serverWrites()
	if len(got) != 1 || !strings.HasPrefix(got[0], "ui.hub ") {
		t.Fatalf("requests = %q, want one ui.hub", got)
	}
	w.noClient()
	return got[0]
}

// entered runs a UI command in a plain terminal on devtools and asserts
// that it reached Enter of the devtools backend.
func (w *world) entered(t *testing.T, args ...string) {
	t.Helper()
	w.use(mux.Devtools)
	scriptedTerminal(t, ordinaryTerminal)
	wantStatus(t, runCommand(args...), 0)
	if got := w.rec.Enters(); len(got) != 1 || !strings.HasPrefix(got[0], "Enter valid=true") {
		t.Fatalf("enters = %q, want one with a valid pass", got)
	}
}

// featureCells holds one function per feature id that performs the
// feature on a backend and asserts the outcome the row gives it there.
var featureCells = map[string]func(t *testing.T, w *world, b mux.Backend, want string){
	"cmd.attach": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.inside(b)
		w.hubInPane(t, b, "attach")
	},
	"cmd.pick": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.inside(b)
		w.hubInPane(t, b, "pick")
	},
	"cmd.sessions": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			wantContains(t, "ui.hub", w.uiHub(t, "sessions"), `"mode":"open"`)
			return
		}
		w.hubScreen(t, b, ui.Placeholder("sessions", false), "sessions")
	},
	"cmd.agents": func(t *testing.T, w *world, b mux.Backend, want string) {
		switch {
		case want == refused:
			w.refusedHere(t, b, mux.CapAgents, "agents")
		case b == mux.Devtools:
			wantContains(t, "ui.hub", w.uiHub(t, "agents"), `"mode":"agents"`)
		default:
			w.hubScreen(t, b, ui.Placeholder("agents", false), "agents")
		}
	},
	"cmd.agents.json": func(t *testing.T, w *world, b mux.Backend, want string) {
		if want == refused {
			w.refusedHere(t, b, mux.CapAgents, "agents", "--json")
			return
		}
		t.Setenv("HERDR_AGENTS", testAgents)
		w.inside(b)
		r := runCommand("agents", "--json")
		wantStatus(t, r, 0)
		wantContains(t, "stdout", r.stdout, `"machine":""`)
		wantContains(t, "stdout", r.stdout, `"status":"blocked"`)
	},
	"cmd.work": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.inside(b)
		w.wantShownInside(t, b, runCommand("switch", "small"))
		if b != mux.Herdr {
			return
		}
		// herdr without a terminal: the late error, after the git work
		w2 := newWorld(t)
		w2.use(mux.Herdr)
		r := runCommand("new", "fresh")
		wantResult(t, r, 1, ui.Message("unsupported", "herdr", "detached-open"))
		if _, err := os.Stat(filepath.Join(w2.root, "fresh")); err != nil {
			t.Fatalf("the git work of new is missing: %v", err)
		}
	},
	"cmd.merge": func(t *testing.T, w *world, b mux.Backend, want string) {
		identity(t)
		remote := createRemote(t)
		clone, err := devgit.Clone(w.root, remote, "widgets")
		if err != nil {
			t.Fatal(err)
		}
		head, err := devgit.CurrentHEAD(clone.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		feat, err := devgit.AddWorktree(w.root, "widgets", "feature/x", head)
		if err != nil {
			t.Fatal(err)
		}
		changeCwd(t, feat.WorktreePath)
		w.inside(b)
		wantStatus(t, runCommand("done", "--force"), 0)
		if _, err := os.Stat(feat.WorktreePath); !os.IsNotExist(err) {
			t.Fatalf("the worktree is still there: %v", err)
		}
		switch b {
		case mux.Devtools:
			got := w.serverWrites()
			if len(got) == 0 || got[len(got)-1] != `session.close {"dir":"`+feat.WorktreePath+`"}` {
				t.Fatalf("requests = %q, want the close of the worktree's session last", got)
			}
		case mux.Tmux:
			if !hasCall(w.tmux(), "switch-client -t widgets-main") {
				t.Fatalf("tmux = %q, want the client moved to main", w.tmux())
			}
		case mux.Herdr:
			wantContains(t, "herdr", w.herdr(), "workspace list")
		}
		w.noClient()
	},
	"cmd.bookmark": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.inside(b)
		wantStatus(t, runCommand("bookmark", "add", "bm", w.project), 0)
		w.nothingRan()
	},
	"cmd.session.list": func(t *testing.T, w *world, b mux.Backend, want string) {
		if want == refused {
			w.refusedHere(t, b, mux.CapLayout, "session", "list", "--json")
			return
		}
		w.use(b)
		r := runCommand("session", "list", "--json")
		wantStatus(t, r, 0)
		wantContains(t, "stdout", r.stdout, `"id":"s1"`)
	},
	"cmd.pane.list": func(t *testing.T, w *world, b mux.Backend, want string) {
		if want == refused {
			w.refusedHere(t, b, mux.CapLayout, "pane", "list", "--json")
			return
		}
		w.use(b)
		r := runCommand("pane", "list", "--json")
		wantStatus(t, r, 0)
		wantContains(t, "stdout", r.stdout, `"id":"p2"`)
	},
	"cmd.pane.focus": func(t *testing.T, w *world, b mux.Backend, want string) {
		if want == refused {
			w.refusedHere(t, b, mux.CapPaneFocus, "pane", "focus", "--direction", "left")
			return
		}
		w.server.set(func(f *fakeServer) { f.moved = true })
		w.inside(b)
		wantStatus(t, runCommand("pane", "focus", "--direction", "left"), 0)
		if got := w.serverWrites(); !slices.Equal(got, []string{`pane.focus {"direction":"left","pane":"p1"}`}) {
			t.Fatalf("requests = %q", got)
		}
	},
	"cmd.keys": func(t *testing.T, w *world, b mux.Backend, want string) {
		if want == refused {
			w.refusedHere(t, b, mux.CapWindow, "keys")
			return
		}
		w.use(b)
		r := runCommand("keys")
		wantStatus(t, r, 0)
		wantContains(t, "stdout", r.stdout, "prefix+o")
	},
	"cmd.server": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.inside(b)
		r := runCommand("server", "status")
		wantStatus(t, r, 0)
		wantContains(t, "stdout", r.stdout, "running")
		if got := w.rec.Opened(); len(got) != 0 {
			t.Fatalf("server status opened %v", got)
		}
	},
	"hub.scope.all": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			wantContains(t, "ui.hub", w.uiHub(t, "pick"), `"mode":"all"`)
			return
		}
		w.hubScreen(t, b, ui.Placeholder("all", false), "pick")
	},
	"hub.scope.sessions": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			wantContains(t, "ui.hub", w.uiHub(t, "sessions"), `"mode":"open"`)
			return
		}
		w.hubScreen(t, b, ui.Placeholder("sessions", false), "sessions")
	},
	"hub.scope.agents": func(t *testing.T, w *world, b mux.Backend, want string) {
		switch {
		case b == mux.Devtools:
			wantContains(t, "ui.hub", w.uiHub(t, "agents"), `"mode":"agents"`)
		case want == absent:
			if s := w.hubScreen(t, b, "small", "pick"); strings.Contains(s, "agents") {
				t.Fatal("the scope agents is drawn without agent-status")
			}
		default:
			wantContains(t, "the hub", w.hubScreen(t, b, "small", "pick"), "agents")
		}
	},
	"hub.row.local": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			w.uiHub(t, "pick")
			return
		}
		w.hubScreen(t, b, "small", "pick")
	},
	"hub.row.agent": func(t *testing.T, w *world, b mux.Backend, want string) {
		t.Setenv("HERDR_AGENTS", testAgents)
		switch {
		case b == mux.Devtools:
			w.uiHub(t, "agents")
		case want == absent:
			if s := w.hubScreen(t, b, "small", "pick"); strings.Contains(s, "Approve rm?") {
				t.Fatal("an agent row is drawn without agent-status")
			}
		default:
			w.hubScreen(t, b, "Approve rm?", "agents")
		}
	},
	"hub.row.remote": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.config("machine macmini")
		if b == mux.Devtools {
			w.uiHub(t, "pick")
			return
		}
		if s := w.hubScreen(t, b, "small", "pick"); strings.Contains(s, "macmini") {
			t.Fatal("a row of another machine is drawn without machines")
		}
	},
	"hub.heading": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.config("machine macmini connect")
		if b == mux.Devtools {
			w.uiHub(t, "pick")
			return
		}
		if s := w.hubScreen(t, b, "small", "pick"); strings.Contains(s, "macmini") {
			t.Fatal("a machine heading is drawn without machines")
		}
	},
	"hub.create": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			w.uiHub(t, "pick")
		} else {
			wantContains(t, "the hub", w.hubScreen(t, b, "small", "pick"), "new feature")
		}
		if w.rec.Deps().Actions == nil {
			t.Fatal("the backend was handed no hub actions")
		}
	},
	"hub.overlay": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			w.uiHub(t, "pick")
			return
		}
		// the hub ran as a full-screen program on the pty, and no ui.hub
		// reached any socket (hubScreen asserts both)
		w.hubScreen(t, b, "small", "pick")
		if got := w.serverWrites(); len(got) != 0 {
			t.Fatalf("requests = %q", got)
		}
	},
	"key.table": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			w.entered(t, "attach")
			return
		}
		if s := w.hubScreen(t, b, "small", "attach"); strings.Contains(s, "ctrl+space") {
			t.Fatal("the prefix of the key table is drawn without window")
		}
	},
	"status.row": func(t *testing.T, w *world, b mux.Backend, want string) {
		if b == mux.Devtools {
			w.entered(t, "attach")
			return
		}
		if s := w.hubScreen(t, b, "small", "attach"); strings.Contains(s, ui.Message("no-client")) {
			t.Fatal("unexpected words on the hub")
		}
		// no client started (hubScreen): nothing draws a status row
	},
	"cfg.multiplexer": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.config("multiplexer " + string(b))
		scriptedTerminal(t, ordinaryTerminal)
		wantStatus(t, runCommand("switch", "small"), 0)
		if got := w.rec.Opened(); !slices.Equal(got, []mux.Backend{b}) {
			t.Fatalf("opened %v, want %s", got, b)
		}
		if !w.started() {
			t.Fatal("no client started in a plain terminal")
		}
	},
	"cfg.root": func(t *testing.T, w *world, b mux.Backend, want string) {
		t.Setenv("DEVTOOLS_ROOT", "")
		w.config("root " + w.root)
		w.inside(b)
		r := runCommand("list")
		wantStatus(t, r, 0)
		wantContains(t, "stdout", r.stdout, w.project)
		w.nothingRan()
	},
	"cfg.machine": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.config("machine macmini")
		configHolds(t, w, b, want, func(c *config.Config) bool {
			return len(c.Machines) == 1 && c.Machines[0].Name == "macmini"
		})
		w.config("machine not/a/machine")
		if r := runCommand("switch", "small"); r.code != 1 || !strings.Contains(r.stderr, "machine") {
			t.Fatalf("a malformed machine line: %+v, want a start error", r)
		}
	},
	"cfg.bind": func(t *testing.T, w *world, b mux.Backend, want string) {
		w.config("bind prefix+y tab-new")
		configHolds(t, w, b, want, func(c *config.Config) bool {
			return len(c.Binds) == 1 && c.Binds[0].Chord == "prefix+y"
		})
		w.config("bind prefix+y")
		if r := runCommand("switch", "small"); r.code != 1 || !strings.Contains(r.stderr, "bind") {
			t.Fatalf("a malformed bind line: %+v, want a start error", r)
		}
	},
}

// configHolds runs a command with the config of the test: on a backend
// with the capability the client is entered with it, on another the value
// is loaded, handed on and unused, and nothing is printed.
func configHolds(t *testing.T, w *world, b mux.Backend, want string, holds func(*config.Config) bool) {
	t.Helper()
	if want == works {
		w.entered(t, "attach")
	} else {
		w.inside(b)
		r := runCommand("switch", "small")
		wantResult(t, r, 0, "")
	}
	if cfg := w.rec.Deps().Config; cfg == nil || !holds(cfg) {
		t.Fatalf("the backend was handed the config %+v", cfg)
	}
}

// TestEveryGatedFeatureOnEveryBackend runs every row of mux.Features on the
// three backends (10.2.7). The expected outcome of a cell is computed from
// the row and backend.CapsOf; the counts are printed, not asserted.
func TestEveryGatedFeatureOnEveryBackend(t *testing.T) {
	counts := map[string]map[mux.Backend]int{}
	for _, f := range mux.Features {
		perform, ok := featureCells[f.ID]
		if !ok {
			t.Errorf("the feature %s has no function in this test", f.ID)
			continue
		}
		for _, b := range backends {
			want := outcomeOf(f, b)
			if counts[want] == nil {
				counts[want] = map[mux.Backend]int{}
			}
			counts[want][b]++
			t.Run(f.ID+"/"+string(b)+"/"+want, func(t *testing.T) {
				w := newWorld(t)
				perform(t, w, b, want)
			})
		}
	}
	var line []string
	for _, o := range []string{works, refused, absent, ignored} {
		c := counts[o]
		line = append(line, fmt.Sprintf("%s %d (devtools %d, tmux %d, herdr %d)", o, c[mux.Devtools]+c[mux.Tmux]+c[mux.Herdr], c[mux.Devtools], c[mux.Tmux], c[mux.Herdr]))
	}
	t.Logf("%d rows on 3 backends: %s", len(mux.Features), strings.Join(line, ", "))
}

// noMultiplexer are the commands that reach no multiplexer (the test file
// of P2-08b settles them): each runs with openBackend never called.
var noMultiplexer = [][]string{
	{"update"}, {"push"}, {"rebase"}, {"nuke"}, {"list"}, {"status"}, {"help"}, {"completion", "bash"},
}

// directiveRows maps each directive of internal/config to its row.
var directiveRows = map[string]string{
	"multiplexer": "cfg.multiplexer", "root": "cfg.root", "bookmark": "cfg.root", "color": "cfg.root",
	"machine": "cfg.machine", "bind": "cfg.bind", "unbind": "cfg.bind",
}

func TestFeatureTableCoversEveryCommand(t *testing.T) {
	root := newRootCommand(&strings.Builder{}, &strings.Builder{})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	isNoMux := func(c *cobra.Command) bool {
		for p := c; p != nil && p != root; p = p.Parent() {
			for _, n := range noMultiplexer {
				if p.Parent() == root && p.Name() == n[0] {
					return true
				}
			}
		}
		return false
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
			switch {
			case isNoMux(sub):
			case featureOf(sub) != "":
				if _, ok := featureRow(featureOf(sub)); !ok {
					t.Errorf("%s names the feature %s, which has no row", sub.CommandPath(), featureOf(sub))
				}
			case sub.HasSubCommands():
				// a group: its commands carry the rows
			default:
				t.Errorf("the command %s has no row in the feature table", sub.CommandPath())
			}
		}
	}
	walk(root)

	// the directives, read from the switch of internal/config/config.go
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "config", "config.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var directives []string
	ast.Inspect(f, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok {
			return true
		}
		for _, e := range cc.List {
			if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				s, _ := strconv.Unquote(lit.Value)
				directives = append(directives, s)
			}
		}
		return true
	})
	if len(directives) == 0 {
		t.Fatal("no directive found in internal/config/config.go")
	}
	for _, d := range directives {
		row, ok := directiveRows[d]
		if !ok {
			t.Errorf("the directive %s has no row in the feature table", d)
			continue
		}
		if row == "" {
			continue
		}
		if _, ok := featureRow(row); !ok {
			t.Errorf("the directive %s names the row %s, which does not exist", d, row)
		}
	}

	// every command that reaches no multiplexer runs without openBackend
	repo := t.TempDir()
	makeRepo(t, repo)
	changeCwd(t, repo)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	r := &recordingMux{}
	useRecording(t, r)
	for _, args := range noMultiplexer {
		runCommand(args...)
		if len(r.opened) != 0 {
			t.Errorf("%v opened a backend: %v", args, r.opened)
		}
	}
}
