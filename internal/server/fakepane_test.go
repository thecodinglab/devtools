package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// fakePane is a Pane without a program: it records what the server asks of
// it, and the test makes it report through the Notify of its options, change
// its screen, exit, block its input or renumber its lines.
type fakePane struct {
	o pane.Options

	mu          sync.Mutex
	info        pane.Info
	closed      bool
	resizes     [][2]int
	cols, rows  int
	lines       []string
	first, base int64
	watch       func()
	inputs      []string // every Key, Mouse and Paste, in the order applied
	focusLog    []bool   // every Focus
	colors      []vt.Colors
	follows     map[int64]int // followed lines and how often
	followLog   []string      // "follow N", "unfollow N" in the order called
	remap       func(abs int64) (int64, bool)
	gate        chan struct{} // while set, Key waits until it is closed
	text        func(from, to vt.Pos, block bool) (string, bool)
	onClose     func(*fakePane)
}

var _ Pane = (*fakePane)(nil)

func (f *fakePane) ID() string { return f.o.ID }

func (f *fakePane) record(s string) {
	f.mu.Lock()
	f.inputs = append(f.inputs, s)
	f.mu.Unlock()
}

func (f *fakePane) Key(k vt.Key) bool {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	s := "key " + k.Name
	if k.Mod != 0 {
		s += fmt.Sprintf(" mod %d", k.Mod)
	}
	if k.Release {
		s += " release"
	}
	f.record(s)
	return true
}

func (f *fakePane) Mouse(e vt.Mouse) bool {
	f.record(fmt.Sprintf("mouse %s %s %d %d", e.Kind, e.Button, e.X, e.Y))
	return true
}

func (f *fakePane) Paste(text string, first, last bool) bool {
	f.record(fmt.Sprintf("paste %q first=%t last=%t", text, first, last))
	return true
}

func (f *fakePane) Focus(focused bool) bool {
	f.mu.Lock()
	f.focusLog = append(f.focusLog, focused)
	f.mu.Unlock()
	return true
}

func (f *fakePane) SetColors(c vt.Colors) {
	f.mu.Lock()
	f.colors = append(f.colors, c)
	f.mu.Unlock()
}

func (f *fakePane) Follow(abs int64) {
	f.mu.Lock()
	f.follows[abs]++
	f.followLog = append(f.followLog, fmt.Sprintf("follow %d", abs))
	f.mu.Unlock()
}

func (f *fakePane) Unfollow(abs int64) {
	f.mu.Lock()
	if f.follows[abs] > 0 {
		f.follows[abs]--
		if f.follows[abs] == 0 {
			delete(f.follows, abs)
		}
	}
	f.followLog = append(f.followLog, fmt.Sprintf("unfollow %d", abs))
	f.mu.Unlock()
}

// Resize records the size and, when the test set a remap, renumbers the
// followed lines through OnReflow as the adapter does at the end of a
// resize: a line the remap places keeps its follows under the new number.
func (f *fakePane) Resize(cols, rows int) {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	f.cols, f.rows = cols, rows
	f.lines = make([]string, rows)
	remap := f.remap
	if remap != nil {
		next := map[int64]int{}
		for abs, n := range f.follows {
			if now, ok := remap(abs); ok {
				next[now] += n
			}
		}
		f.follows = next
	}
	f.mu.Unlock()
	if remap != nil && f.o.OnReflow != nil {
		f.o.OnReflow(remap)
	}
	if wake := f.wakeFunc(); wake != nil {
		wake() // the screen changed
	}
}

func (f *fakePane) Snapshot(top int64, scrolled bool) pane.Snap {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := pane.Snap{
		Cols: f.cols, Rows: f.rows,
		Lines:  append([]string(nil), f.lines...),
		Cursor: vt.Cursor{Visible: true, Shape: "block"},
		First:  f.first, Base: f.base,
	}
	if scrolled {
		s.Scrolled = true
		s.Top = min(max(top, f.first), f.base)
		for i := range s.Lines {
			s.Lines[i] = fmt.Sprintf("line %d", s.Top+int64(i))
		}
	}
	return s
}

func (f *fakePane) Text(from, to vt.Pos, block bool) (string, bool) {
	f.mu.Lock()
	text := f.text
	f.mu.Unlock()
	if text == nil {
		return "", false
	}
	return text(from, to, block)
}

func (f *fakePane) Info() pane.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info
}

func (f *fakePane) Watch(wake func()) func() {
	f.mu.Lock()
	f.watch = wake
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.watch = nil
		f.mu.Unlock()
	}
}

func (f *fakePane) Close() {
	f.mu.Lock()
	hook := f.onClose
	f.onClose = nil
	already := f.closed
	f.closed = true
	if !already && f.info.State == proto.PaneRunning {
		f.info.State = proto.PaneExited
		f.info.Exit = &pane.Exit{Signal: "SIGHUP", ByServer: true}
	}
	gate := f.gate
	f.gate = nil
	f.mu.Unlock()
	if gate != nil {
		close(gate)
	}
	if hook != nil {
		hook(f)
	}
}

func (f *fakePane) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// setTitle changes the title and reports it, as the pane actor does after
// an OSC 0 or 2.
func (f *fakePane) setTitle(title string) {
	f.mu.Lock()
	f.info.Title = title
	f.mu.Unlock()
	f.o.Notify(pane.Event{Pane: f.o.ID, Kind: pane.InfoChanged})
}

// report reports an OSC 7501 record.
func (f *fakePane) report(r vt.AgentReport) {
	f.o.Notify(pane.Event{Pane: f.o.ID, Kind: pane.AgentReport, Agent: r})
}

// write puts text into row y and wakes the watcher, as output does.
func (f *fakePane) write(y int, text string) {
	f.mu.Lock()
	if y < len(f.lines) {
		f.lines[y] = text
	}
	wake := f.watch
	f.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// exit ends the program as the waiter reports it: with a code, or killed by
// a signal.
func (f *fakePane) exit(code int, signal string) {
	f.mu.Lock()
	f.info.State = proto.PaneExited
	f.info.Exit = &pane.Exit{Code: code, Signal: signal}
	f.mu.Unlock()
	f.o.Notify(pane.Event{Pane: f.o.ID, Kind: pane.Exited})
}

// setFg sets the foreground process group of the PTY.
func (f *fakePane) setFg(pgid int) {
	f.mu.Lock()
	f.info.Fg = pgid
	f.mu.Unlock()
}

func (f *fakePane) setRange(first, base int64) {
	f.mu.Lock()
	f.first, f.base = first, base
	f.mu.Unlock()
}

func (f *fakePane) setRemap(remap func(abs int64) (int64, bool)) {
	f.mu.Lock()
	f.remap = remap
	f.mu.Unlock()
}

// block makes Key wait until unblock (or Close).
func (f *fakePane) block() {
	f.mu.Lock()
	f.gate = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakePane) unblock() {
	f.mu.Lock()
	gate := f.gate
	f.gate = nil
	f.mu.Unlock()
	if gate != nil {
		close(gate)
	}
}

func (f *fakePane) inputLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}

func (f *fakePane) hasInput(s string) bool {
	for _, in := range f.inputLog() {
		if in == s {
			return true
		}
	}
	return false
}

func (f *fakePane) followed() map[int64]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64]int{}
	for k, v := range f.follows {
		out[k] = v
	}
	return out
}

func (f *fakePane) followCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.followLog...)
}

func (f *fakePane) colorLog() []vt.Colors {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]vt.Colors(nil), f.colors...)
}

func (f *fakePane) lastResize() [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return [2]int{f.cols, f.rows}
}

// fakePanes is Options.NewPane of the tests. Pids are above any pid_max of
// Linux and macOS, so no real process of an ancestor chain has one.
type fakePanes struct {
	mu      sync.Mutex
	n       int
	byID    map[string]*fakePane
	all     []*fakePane
	onStart func(*fakePane)
}

const fakePidBase = 1 << 30

func (fp *fakePanes) start(o pane.Options) Pane {
	fp.mu.Lock()
	fp.n++
	f := &fakePane{o: o, cols: o.Cols, rows: o.Rows, lines: make([]string, max(o.Rows, 0)), follows: map[int64]int{}, info: pane.Info{
		State: proto.PaneRunning,
		Cwd:   o.Dir,
		Pid:   fakePidBase + fp.n,
		Shell: fakePidBase + fp.n,
		Fg:    fakePidBase + fp.n,
		TTY:   fmt.Sprintf("/dev/pts/fake%d", fp.n),
	}}
	f.colors = []vt.Colors{o.Colors}
	if fp.byID == nil {
		fp.byID = map[string]*fakePane{}
	}
	fp.byID[o.ID] = f
	fp.all = append(fp.all, f)
	hook := fp.onStart
	fp.mu.Unlock()
	if hook != nil {
		hook(f)
	}
	return f
}

// get returns the newest pane object of an id, nil if none was started.
func (fp *fakePanes) get(id string) *fakePane {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.byID[id]
}

// started returns how many pane objects were started.
func (fp *fakePanes) started() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.n
}

// fakeTable is the process table of the tests (procTable): what the test
// puts in, and how often it was read.
var fakeTable struct {
	mu    sync.Mutex
	procs []platform.Proc
	reads int
}

func readFakeTable() ([]platform.Proc, error) {
	fakeTable.mu.Lock()
	defer fakeTable.mu.Unlock()
	fakeTable.reads++
	return append([]platform.Proc(nil), fakeTable.procs...), nil
}

// setProcs replaces the process table for the running test.
func setProcs(t *testing.T, procs ...platform.Proc) {
	fakeTable.mu.Lock()
	fakeTable.procs = procs
	fakeTable.mu.Unlock()
	t.Cleanup(func() {
		fakeTable.mu.Lock()
		fakeTable.procs = nil
		fakeTable.mu.Unlock()
	})
}

func tableReads() int {
	fakeTable.mu.Lock()
	defer fakeTable.mu.Unlock()
	return fakeTable.reads
}

// testServer is a server of the core on a socket in a short temporary
// directory, with fake panes.
type testServer struct {
	t      *testing.T
	s      *Server
	socket string
	dir    string // a directory for sessions
	state  string // the state directory
	panes  *fakePanes
	served chan error
}

// short shortens the intervals a test waits for.
func short(o *Options) {
	o.CosmeticDelay = 20 * time.Millisecond
	o.SaveDelay = 50 * time.Millisecond
	o.HelloTimeout = 2 * time.Second
}

// newTestServer starts a server; each of opts changes its options first.
func newTestServer(t *testing.T, opts ...func(*Options)) *testServer {
	t.Helper()
	tmp := testkit.ShortTempDir(t)
	ts := &testServer{t: t, socket: filepath.Join(tmp, "s.sock"), state: filepath.Join(tmp, "state"), panes: &fakePanes{}, served: make(chan error, 1)}
	ts.dir = mkdir(t, filepath.Join(tmp, "work"))
	mkdir(t, ts.state)
	o := Defaults()
	o.StateDir = ts.state
	o.Version = "0.0.0-test"
	o.Host = "testhost"
	o.Env = testkit.CleanEnv(tmp)
	o.NewPane = ts.panes.start
	short(&o)
	for _, f := range opts {
		f(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts.s = s
	l, err := net.Listen("unix", ts.socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ts.serve(l)
	return ts
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// serve runs Serve on l and ends the server when the test ends: Serve must
// then have returned nil.
func (ts *testServer) serve(l net.Listener) {
	go func() { ts.served <- ts.s.Serve(l) }()
	ts.t.Cleanup(func() {
		ts.s.halt()
		select {
		case err := <-ts.served:
			if err != nil {
				ts.t.Errorf("Serve returned %v after the server was halted", err)
			}
		case <-time.After(5 * time.Second):
			ts.t.Errorf("Serve did not return after the server was halted")
		}
	})
}

// do runs f on the loop and waits for it.
func (ts *testServer) do(f func(c *core)) {
	ts.t.Helper()
	done := make(chan struct{})
	ts.s.post(func(c *core) {
		defer close(done)
		f(c)
	})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		ts.t.Fatal("the loop did not run a posted function within 10 s")
	}
}

// display marks a session displayed, as view.set does: the dormant panes of
// its current tab are spawned.
func (ts *testServer) display(session string) {
	ts.t.Helper()
	ts.do(func(c *core) { c.apply(c.model.Display(session), true) })
}

func (ts *testServer) interactive() *prototest.Client {
	ts.t.Helper()
	return prototest.Dial(ts.t, ts.socket, prototest.Interactive())
}

func (ts *testServer) oneshot() *prototest.Client {
	ts.t.Helper()
	return prototest.Dial(ts.t, ts.socket, prototest.Oneshot(""))
}

// open opens the session of ts.dir through c and returns its session, tab
// and pane.
func (ts *testServer) open(c *prototest.Client) (s, t, p string) {
	ts.t.Helper()
	return ts.openDir(c, ts.dir)
}

func (ts *testServer) openDir(c *prototest.Client, dir string) (s, t, p string) {
	ts.t.Helper()
	r := ok(ts.t, c, proto.SessionOpen{Dir: dir, Name: "work"})
	var res proto.SessionOpenResult
	r.Msg.Decode(&res)
	st := ts.stateGet(c)
	for _, sess := range st.Sessions {
		if sess.ID != res.Session {
			continue
		}
		for _, tab := range st.Tabs {
			if tab.ID == sess.Current {
				return sess.ID, tab.ID, tab.Focused
			}
		}
	}
	ts.t.Fatalf("session %s is not in the state", res.Session)
	return
}

// view sets the view of c on a session at cols x rows.
func (ts *testServer) view(c *prototest.Client, session string, cols, rows int) proto.ViewOK {
	ts.t.Helper()
	return c.View(proto.ViewSet{Session: session, Cols: cols, Rows: rows})
}

// pane waits for the pane object of id and returns it.
func (ts *testServer) pane(id string) *fakePane {
	ts.t.Helper()
	var f *fakePane
	testkit.Eventually(ts.t, 5*time.Second, "pane "+id+" is started", func() bool {
		f = ts.panes.get(id)
		return f != nil
	})
	return f
}

// size returns the size of a session as the model holds it.
func (ts *testServer) size(session string) [2]int {
	ts.t.Helper()
	var out [2]int
	ts.do(func(c *core) {
		cols, rows, _ := c.model.Size(session)
		out = [2]int{cols, rows}
	})
	return out
}

// sync waits until the loop has run everything posted before; the posts of
// a connection's reader are in the mailbox once a later request of the same
// connection is answered.
func (ts *testServer) sync(c *prototest.Client) {
	ts.t.Helper()
	ts.stateGet(c)
	ts.do(func(*core) {})
}

// stateGet asks for the snapshot.
func (ts *testServer) stateGet(c *prototest.Client) proto.Snapshot {
	ts.t.Helper()
	r := c.Request(proto.StateGet{})
	if !r.OK || r.Msg.T != proto.TypeStateSnapshot {
		ts.t.Fatalf("state.get answered %s %s: %s", r.Msg.T, r.Code, r.Text)
	}
	var snap proto.Snapshot
	if err := r.Msg.Decode(&snap); err != nil {
		ts.t.Fatal(err)
	}
	return snap
}

// ok sends a request and fails the test unless it is answered ok.
func ok(t *testing.T, c *prototest.Client, req proto.Typed) proto.Reply {
	t.Helper()
	r := c.Request(req)
	if !r.OK {
		t.Fatalf("%s: %s: %s", req.Type(), r.Code, r.Text)
	}
	return r
}

// rawHello sends a hello on a new connection and returns the first answer.
func rawHello(t *testing.T, socket string, h proto.Hello) (net.Conn, proto.Msg) {
	t.Helper()
	nc := prototest.DialRaw(t, socket)
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := proto.WriteMsg(nc, proto.Encode(h)); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	m, err := proto.ReadMsg(nc)
	if err != nil {
		t.Fatalf("read the answer to hello: %v", err)
	}
	return nc, m
}

// refused sends a hello that must be refused with code and returns the
// text; the server must close the connection after it.
func refused(t *testing.T, socket string, h proto.Hello, code string) string {
	t.Helper()
	nc, m := rawHello(t, socket, h)
	if m.T != proto.TypeRefuse {
		t.Fatalf("the hello was answered %s, not refuse %s", m.T, code)
	}
	var r proto.Refuse
	m.Decode(&r)
	if r.Code != code {
		t.Fatalf("refuse %s (%s), want %s", r.Code, r.Text, code)
	}
	if _, err := proto.ReadMsg(nc); err != io.EOF {
		t.Fatalf("after refuse the connection gave %v, not the end", err)
	}
	return r.Text
}

// welcomed sends a hello that must be welcomed.
func welcomed(t *testing.T, socket string, h proto.Hello) proto.Welcome {
	t.Helper()
	_, m := rawHello(t, socket, h)
	if m.T != proto.TypeWelcome {
		var r proto.Refuse
		m.Decode(&r)
		t.Fatalf("the hello was answered %s %s: %s, not welcome", m.T, r.Code, r.Text)
	}
	var w proto.Welcome
	m.Decode(&w)
	return w
}

// deltas returns the state.delta messages of c received from index from on.
func deltasOf(t *testing.T, c *prototest.Client, from int) []proto.Delta {
	t.Helper()
	var out []proto.Delta
	for i, m := range c.Received() {
		if i < from || m.T != proto.TypeStateDelta {
			continue
		}
		var d proto.Delta
		if err := m.Decode(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// touches reports whether a delta changes the object of kind and id.
func touches(d proto.Delta, kind, id string) bool {
	for _, ch := range d.Changes {
		if ch.Kind == kind && ch.ID == id {
			return true
		}
	}
	return false
}

// framesOf returns the frames of a pane among the messages of c from index
// from on, with the index of each.
func framesOf(t *testing.T, c *prototest.Client, from int, paneID string) (frames []proto.Frame, at []int) {
	t.Helper()
	for i, m := range c.Received() {
		if i < from || m.T != proto.TypeFrame {
			continue
		}
		var f proto.Frame
		if err := m.Decode(&f); err != nil {
			t.Fatal(err)
		}
		if f.Pane == paneID {
			frames = append(frames, f)
			at = append(at, i)
		}
	}
	return frames, at
}

// messagesOf returns the messages of type typ of c from index from on.
func messagesOf(c *prototest.Client, from int, typ string) (ms []proto.Msg, at []int) {
	for i, m := range c.Received() {
		if i >= from && m.T == typ {
			ms = append(ms, m)
			at = append(at, i)
		}
	}
	return ms, at
}

// sortedKeys returns the keys of m in order, for messages.
func sortedKeys(m map[int64]int) []int64 {
	var out []int64
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func joined(list []string) string { return strings.Join(list, ", ") }

func (f *fakePane) focusCalls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.focusLog...)
}

func (f *fakePane) wakeFunc() func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watch
}

// list returns every pane object started, the oldest first.
func (fp *fakePanes) list() []*fakePane {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return append([]*fakePane(nil), fp.all...)
}

// The live upgrade: a fake pane cannot be handed over.
func (f *fakePane) Freeze(ctx context.Context) (pane.Frozen, error) {
	return pane.Frozen{}, errors.New("fake pane: no live upgrade")
}
func (f *fakePane) Thaw()    {}
func (f *fakePane) Abandon() {}
