package cli

// The test harness of the commands on the three backends (P2-08b): a world
// with every fake installed (the fake tmux and herdr programs of
// cli_test.go and the scripted devtools server of fakeserver_test.go), a
// terminal that is a pty of testkit scripted by a goroutine (the probe
// answers, the keys of a hub), and a recorder of the client start of the
// devtools backend at Enter (the brief's decision: record at Enter in
// internal/cli, at runClient in internal/backend).

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/backend"
	"devtools/internal/client/guard"
	"devtools/internal/mux"
	"devtools/internal/proto"
	"devtools/internal/testkit"
)

// scripted is the terminal of the commands of a test: stdin and stdout are
// the slave of a pty; a goroutine on the master answers every probe of the
// guard with answer ("" for none) and keeps everything drawn.
type scripted struct {
	master *os.File
	mu     sync.Mutex
	screen bytes.Buffer
}

func scriptedTerminal(t *testing.T, answer string) *scripted {
	t.Helper()
	unsetEmptyMarks(t)
	master, slave := testkit.OpenPTY(t, 100, 30)
	savedIn, savedOut := terminalIn, terminalOut
	terminalIn, terminalOut = slave, slave
	t.Cleanup(func() { terminalIn, terminalOut = savedIn, savedOut })
	s := &scripted{master: master}
	go func() {
		var seen []byte
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			s.mu.Lock()
			s.screen.Write(buf[:n])
			s.mu.Unlock()
			seen = append(seen, buf[:n]...)
			if answer != "" && bytes.Contains(seen, []byte(probeQueries)) {
				seen = nil
				if _, err := master.WriteString(answer); err != nil {
					return
				}
			}
			if len(seen) > 1<<16 {
				seen = seen[len(seen)-64:]
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

// Screen is everything drawn so far, escape sequences included.
func (s *scripted) Screen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.screen.String()
}

// waitFor waits until the screen holds text.
func (s *scripted) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(s.Screen(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("the terminal never showed %q; it shows:\n%q", text, s.Screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// typeKeys writes keys to the terminal.
func (s *scripted) typeKeys(t *testing.T, keys string) {
	t.Helper()
	if _, err := s.master.WriteString(keys); err != nil {
		t.Fatal(err)
	}
}

// result is what a command did.
type result struct {
	code           int
	stdout, stderr string
}

func runCommand(args ...string) result {
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

// runWithHub runs the command while a hub is on the terminal: once the
// screen shows text, it types keys, and returns what the command did.
func runWithHub(t *testing.T, term *scripted, text, keys string, args ...string) result {
	t.Helper()
	from := len(term.Screen())
	done := make(chan result, 1)
	go func() { done <- runCommand(args...) }()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(term.Screen()[from:], text) {
		select {
		case r := <-done:
			t.Fatalf("%v ended before the hub showed %q: %+v; screen %q", args, text, r, term.Screen())
		case <-deadline:
			t.Fatalf("the hub never showed %q; it shows:\n%q", text, term.Screen())
		case <-time.After(10 * time.Millisecond):
		}
	}
	term.typeKeys(t, keys)
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("%v did not end after %q; screen %q", args, keys, term.Screen())
	}
	return result{}
}

// enterRecorder stands for the devtools backend in the tests of this
// package: openBackend opens the real backend, and for devtools wraps it in
// a multiplexer that records Enter with its pass and its entry, does not
// call through (until P1-17a client.Run is a stub), and passes every other
// method on. The tmux and the herdr backend are the real ones.
type enterRecorder struct {
	mux.Multiplexer
	mu     sync.Mutex
	enters []string
	opened []mux.Backend
	deps   backend.Deps
	ctx    mux.Context
}

func (r *enterRecorder) Enter(p *guard.Pass, e mux.Entry) error {
	what := "last"
	switch {
	case e.Hub != nil:
		what = fmt.Sprintf("hub %d %q", e.Hub.Mode, e.Hub.Query)
	case e.Target != nil:
		what = fmt.Sprintf("target %s %s", e.Target.Dir, e.Target.Name)
	}
	r.mu.Lock()
	r.enters = append(r.enters, fmt.Sprintf("Enter valid=%v %s", p.Valid(), what))
	r.mu.Unlock()
	if !p.Valid() {
		return mux.ErrNoPass
	}
	return nil
}

func (r *enterRecorder) Enters() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.enters...)
}

func (r *enterRecorder) Opened() []mux.Backend {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mux.Backend(nil), r.opened...)
}

func (r *enterRecorder) Deps() backend.Deps {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deps
}

func useEnterRecorder(t *testing.T) *enterRecorder {
	t.Helper()
	r := &enterRecorder{}
	saved := openBackend
	openBackend = func(b mux.Backend, c mux.Context, d backend.Deps) (mux.Multiplexer, error) {
		m, err := backend.Open(b, c, d)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.opened = append(r.opened, b)
		r.deps, r.ctx = d, c
		r.mu.Unlock()
		if b != mux.Devtools {
			return m, nil
		}
		r.Multiplexer = m
		return r, nil
	}
	t.Cleanup(func() { openBackend = saved })
	return r
}

// world is one test's machine: a workspace root with the repository
// "small", the fake tmux and herdr installed (herdr outside, its server not
// running), a scripted devtools server at DEVTOOLS_SOCKET that knows the
// pane p1, and the Enter recorder. No mark is set: the context is a
// command without a terminal until the test sets marks or a terminal.
type world struct {
	t        *testing.T
	root     string
	project  string
	tmuxLog  string
	herdrLog string
	running  string // the marker file of the fake herdr's server
	server   *fakeServer
	rec      *enterRecorder
}

// worldSnapshot is the state of the scripted server: the session of the
// repository small with the pane p1, and an agent in it.
func worldSnapshot(project string) proto.Snapshot {
	return proto.Snapshot{
		Rev:      1,
		Sessions: []proto.Session{{ID: "s1", Dir: project, Label: "small-main", Tabs: []string{"t1"}, Current: "t1"}},
		Tabs:     []proto.Tab{{ID: "t1", Session: "s1", Index: 1, Panes: []string{"p1", "p2"}, Split: proto.SplitRight, Focused: "p1"}},
		Panes: []proto.Pane{
			{ID: "p1", Tab: "t1", Session: "s1", State: proto.PaneRunning, Cwd: project, Program: "zsh"},
			{ID: "p2", Tab: "t1", Session: "s1", State: proto.PaneRunning, Cwd: project, Program: "claude"},
		},
		Agents: []proto.Agent{{Pane: "p2", Tab: "t1", Session: "s1", Kind: proto.AgentClaude, Status: proto.StatusBlocked, Title: "Approve rm?", Cwd: project, Since: 1700000000000}},
	}
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, root: t.TempDir()}
	w.project = filepath.Join(w.root, "small")
	makeRepo(t, w.project)
	t.Setenv("DEVTOOLS_ROOT", w.root)
	t.Setenv("DEVTOOLS_CONFIG", filepath.Join(t.TempDir(), "config"))
	w.tmuxLog = installFakeTmux(t)
	w.herdrLog, w.running = installFakeHerdrOutside(t, "")
	w.server = newFakeServer(t, worldSnapshot(w.project))
	t.Setenv("DEVTOOLS_SOCKET", w.server.socket)
	w.rec = useEnterRecorder(t)
	// a world starts with no mark, whatever the test set before
	for _, name := range []string{proto.EnvPane, "DEVTOOLS_MULTIPLEXER", "STY", "ZELLIJ", "ZELLIJ_SESSION_NAME"} {
		t.Setenv(name, "")
	}
	unsetEmptyMarks(t)
	return w
}

// use names the backend by DEVTOOLS_MULTIPLEXER.
func (w *world) use(b mux.Backend) { w.t.Setenv("DEVTOOLS_MULTIPLEXER", string(b)) }

// config writes the config file.
func (w *world) config(lines ...string) {
	w.t.Helper()
	if err := os.WriteFile(os.Getenv("DEVTOOLS_CONFIG"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// The marks of the contexts.

func (w *world) inDevtools() { w.t.Setenv(proto.EnvPane, "p1") }
func (w *world) inTmux()     { insideFakeTmux(w.t) }
func (w *world) tmuxPopup() {
	insideFakeTmux(w.t)
	w.t.Setenv("TMUX_PANE", "")
}
func (w *world) inHerdr() {
	w.t.Setenv("HERDR_ENV", "1")
	w.t.Setenv("HERDR_SOCKET_PATH", fakeHerdrSocket(w.t))
	w.t.Setenv("HERDR_RUNNING", "")
}

// inside sets the mark of b's pane.
func (w *world) inside(b mux.Backend) {
	switch b {
	case mux.Devtools:
		w.inDevtools()
	case mux.Tmux:
		w.inTmux()
	case mux.Herdr:
		w.inHerdr()
	}
}

// The records of the three fakes.

func (w *world) tmux() []string { return tmuxCommands(w.t, w.tmuxLog) }
func (w *world) herdr() string  { return herdrCalls(w.t, w.herdrLog) }

// tmuxWrites are the calls of the fake tmux other than the listing the
// standalone hub's feed reads.
func (w *world) tmuxWrites() []string {
	var out []string
	for _, c := range w.tmux() {
		if !strings.HasPrefix(c, "list-sessions") {
			out = append(out, c)
		}
	}
	return out
}

// herdrWrites are the calls of the fake herdr other than the reads of the
// standalone hub's feed (status, workspace list, agent list).
func (w *world) herdrWrites() []string {
	var out []string
	for _, c := range strings.Split(w.herdr(), "\n") {
		switch {
		case c == "", strings.HasPrefix(c, "status server"), c == "workspace list", c == "agent list":
		default:
			out = append(out, c)
		}
	}
	return out
}

// quiet fails the test when any fake saw anything.
func (w *world) quiet() {
	w.t.Helper()
	if got := w.tmux(); len(got) != 0 {
		w.t.Errorf("the fake tmux ran %q", got)
	}
	if got := w.herdr(); got != "" {
		w.t.Errorf("the fake herdr ran %q", got)
	}
	if got := w.server.Requests(); len(got) != 0 {
		w.t.Errorf("the server was asked %q", got)
	}
	if got := w.rec.Enters(); len(got) != 0 {
		w.t.Errorf("the devtools client started: %q", got)
	}
}

// noClient fails the test when any backend started a client.
func (w *world) noClient() {
	w.t.Helper()
	for _, c := range w.tmux() {
		if strings.HasPrefix(c, "new-session -A") || strings.HasPrefix(c, "attach-session") {
			w.t.Errorf("tmux ran %q, which starts a client", c)
		}
	}
	if strings.Contains(w.herdr(), "<attach>") {
		w.t.Errorf("herdr attached: %q", w.herdr())
	}
	if got := w.rec.Enters(); len(got) != 0 {
		w.t.Errorf("the devtools client started: %q", got)
	}
}

// hasCall reports whether one of calls starts with prefix.
func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// silentServer listens at a new socket, accepts and never answers: a
// server that is stopped or busy beyond the limit of a check.
func silentServer(t *testing.T) string {
	f := newFakeServer(t, proto.Snapshot{})
	f.set(func(f *fakeServer) { f.silent = true })
	return f.socket
}

// unsetEmptyMarks unsets every marker variable of the guard that the fakes
// set to the empty string. The guard refuses on a marker that is set at
// all, empty or not (guard.Env), while mux.Detect reads an empty one as
// unset; a test of a plain terminal wants neither to see one.
func unsetEmptyMarks(t *testing.T) {
	t.Helper()
	for _, name := range []string{"DEVTOOLS_PANE", "TMUX", "TMUX_PANE", "STY", "ZELLIJ", "ZELLIJ_SESSION_NAME", "HERDR_ENV", "HERDR_SOCKET_PATH"} {
		if v, ok := os.LookupEnv(name); ok && v == "" {
			os.Unsetenv(name)
			t.Cleanup(func() { os.Setenv(name, "") })
		}
	}
}
