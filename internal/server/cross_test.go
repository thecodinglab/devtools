package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/config"
	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// The tests of cross.go: a server of the core with fake panes, talked to
// over the wire with prototest. The seams of cross.go are set on the loop,
// which reads them, and restored there before the server ends.

type catalogFunc = func(context.Context, func() (config.Config, error), func(proto.Msg))

func setCatalogRun(ts *testServer, f catalogFunc) {
	var old catalogFunc
	ts.do(func(*core) { old, catalogRun = catalogRun, f })
	ts.t.Cleanup(func() { ts.do(func(*core) { catalogRun = old }) })
}

func setCatalogLoad(ts *testServer, cfg config.Config) {
	var old func() (config.Config, error)
	ts.do(func(*core) {
		old = catalogLoad
		catalogLoad = func() (config.Config, error) { return cfg, nil }
	})
	ts.t.Cleanup(func() { ts.do(func(*core) { catalogLoad = old }) })
}

func setCatalogCheck(ts *testServer, d time.Duration) {
	var old time.Duration
	ts.do(func(*core) { old, catalogCheck = catalogCheck, d })
	ts.t.Cleanup(func() { ts.do(func(*core) { catalogCheck = old }) })
}

// fakeCatalog is a catalog stream the test controls: each run announces
// itself, waits for its release or its cancel, then emits one item and done.
type fakeCatalog struct{ started chan *fakeRun }

type fakeRun struct {
	ctx     context.Context
	emit    func(proto.Msg)
	release chan struct{}
}

func newFakeCatalog() *fakeCatalog { return &fakeCatalog{started: make(chan *fakeRun, 8)} }

func (f *fakeCatalog) run(ctx context.Context, _ func() (config.Config, error), emit func(proto.Msg)) {
	r := &fakeRun{ctx: ctx, emit: emit, release: make(chan struct{})}
	f.started <- r
	select {
	case <-ctx.Done():
		return
	case <-r.release:
	}
	emit(proto.Encode(proto.CatalogItem{Items: []proto.CatalogEntry{{Kind: proto.CatalogRepo, Project: "x", Label: "x", Path: "/x", Session: "x"}}}))
	emit(proto.Encode(proto.CatalogDone{}))
}

func (f *fakeCatalog) next(t *testing.T) *fakeRun {
	t.Helper()
	select {
	case r := <-f.started:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the catalog stream did not start")
		return nil
	}
}

func cancelled(t *testing.T, r *fakeRun, what string) {
	t.Helper()
	select {
	case <-r.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the scan was not cancelled", what)
	}
}

func watchCatalog(c *prototest.Client, id uint64) {
	c.Send(proto.Request(id, proto.CatalogWatch{}))
}

// catalogOf returns the catalog messages c received from index from on.
func catalogOf(c *prototest.Client, from int) []proto.Msg {
	var out []proto.Msg
	for i, m := range c.Received() {
		if i >= from && strings.HasPrefix(m.T, "catalog.") {
			out = append(out, m)
		}
	}
	return out
}

func TestCatalogStreamsToTheAskingConnectionOnly(t *testing.T) {
	ts := newTestServer(t)
	root := mkdir(t, filepath.Join(ts.dir, "root"))
	notes := mkdir(t, filepath.Join(ts.dir, "notes"))
	setCatalogLoad(ts, config.Config{Roots: []string{root}, Bookmarks: []config.Bookmark{{Name: "notes", Path: notes}}})
	a, b := ts.interactive(), ts.interactive()

	watchCatalog(a, 7)
	a.WaitMessage(proto.TypeCatalogDone, 0)
	got := catalogOf(a, 0)
	var bookmark bool
	for i, m := range got {
		if m.ID != 7 {
			t.Fatalf("%s carries id %d, not the id of its watch", m.T, m.ID)
		}
		if m.T == proto.TypeCatalogDone && i != len(got)-1 {
			t.Fatalf("catalog.done is not the last message of the stream: %v", a.Types())
		}
		if m.T == proto.TypeCatalogItem {
			var item proto.CatalogItem
			if err := m.Decode(&item); err != nil {
				t.Fatal(err)
			}
			for _, e := range item.Items {
				bookmark = bookmark || (e.Kind == proto.CatalogBookmark && e.Path == notes)
			}
		}
	}
	if !bookmark {
		t.Fatalf("the stream did not list the bookmark %s: %v", notes, a.Types())
	}
	ts.sync(b)
	if msgs := catalogOf(b, 0); len(msgs) != 0 {
		t.Fatalf("a connection that asked for nothing received %d catalog messages", len(msgs))
	}

	// A one-shot has no stream.
	if r := ts.oneshot().Request(proto.CatalogWatch{}); r.OK || r.Code != proto.CodeInvalid {
		t.Fatalf("catalog.watch of a one-shot answered %+v, want invalid", r)
	}
}

func TestCatalogStopsWhenTheConnectionCloses(t *testing.T) {
	ts := newTestServer(t)
	fc := newFakeCatalog()
	setCatalogRun(ts, fc.run)
	setCatalogCheck(ts, 20*time.Millisecond)
	a := ts.interactive()
	watchCatalog(a, 1)
	r := fc.next(t)
	a.Close()
	cancelled(t, r, "the connection closed")
	ts.do(func(c *core) {
		if n := len(stateOf(c).watches); n != 0 {
			t.Errorf("%d streams are still held after their connection closed", n)
		}
	})
}

func TestCatalogStopCancelsTheScan(t *testing.T) {
	ts := newTestServer(t)
	fc := newFakeCatalog()
	setCatalogRun(ts, fc.run)
	a := ts.interactive()
	watchCatalog(a, 3)
	r := fc.next(t)
	a.Send(proto.Request(9, proto.CatalogStop{}))
	ts.sync(a)
	if r.ctx.Err() != nil {
		t.Fatal("a stop for another watch cancelled the running one")
	}
	a.Send(proto.Request(3, proto.CatalogStop{}))
	cancelled(t, r, "catalog.stop")
	close(r.release)
	ts.sync(a)
	if msgs := catalogOf(a, 0); len(msgs) != 0 {
		t.Fatalf("a stopped stream sent %d messages", len(msgs))
	}
}

func TestASecondWatchReplacesTheFirst(t *testing.T) {
	ts := newTestServer(t)
	fc := newFakeCatalog()
	setCatalogRun(ts, fc.run)
	a := ts.interactive()
	watchCatalog(a, 1)
	first := fc.next(t)
	watchCatalog(a, 2)
	second := fc.next(t)
	cancelled(t, first, "the second watch")
	if second.ctx.Err() != nil {
		t.Fatal("the second watch was cancelled")
	}
	// What the first scan still emits is dropped.
	first.emit(proto.Encode(proto.CatalogDone{}))
	close(second.release)
	a.WaitMessage(proto.TypeCatalogDone, 0)
	ts.sync(a)
	var types []string
	for _, m := range catalogOf(a, 0) {
		if m.ID != 2 {
			t.Fatalf("%s with id %d after the watch 2 replaced it", m.T, m.ID)
		}
		types = append(types, m.T)
	}
	if !slices.Equal(types, []string{proto.TypeCatalogItem, proto.TypeCatalogDone}) {
		t.Fatalf("the second stream sent %v", types)
	}
}

// paneCaller is a one-shot connection of a command running in pane p: its
// hello names the pane, and the pid of the pane's child is among its
// ancestors, as the server checks.
func paneCaller(ts *testServer, p string) *prototest.Client {
	ts.t.Helper()
	h := prototest.Oneshot(p)
	h.Ancestors = []int{os.Getpid(), ts.pane(p).Info().Pid}
	return prototest.Dial(ts.t, ts.socket, h)
}

func bridgeHello() proto.Hello {
	h := prototest.Interactive()
	h.Remote = true
	h.Caps = []string{proto.CapRoute}
	return h
}

// claiming is an interactive connection whose hello claims caps: the server
// sends event.agent only where agents is claimed and routes ui.* only to a
// window that claims route (4.1, 4.2.8, 4.2.10).
func claiming(ts *testServer, caps ...string) *prototest.Client {
	ts.t.Helper()
	h := prototest.Interactive()
	h.Caps = caps
	return prototest.Dial(ts.t, ts.socket, h)
}

// routed returns the routed ui.* messages c received from index from on.
func routed(c *prototest.Client, from int) []proto.Msg {
	var out []proto.Msg
	for i, m := range c.Received() {
		if i >= from && (m.T == proto.TypeUIHub || m.T == proto.TypeUISwitch) {
			out = append(out, m)
		}
	}
	return out
}

func noRouted(t *testing.T, ts *testServer, c *prototest.Client, from int, who string) {
	t.Helper()
	ts.sync(c)
	if ms := routed(c, from); len(ms) != 0 {
		t.Fatalf("%s received %d routed messages", who, len(ms))
	}
}

func TestRoutedHubReachesTheConnectionDisplayingThePane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	s2, _, _ := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b := claiming(ts, proto.CapRoute), claiming(ts, proto.CapRoute)
	ts.view(a, s, 80, 24)
	ts.view(b, s2, 80, 24)
	pc := paneCaller(ts, p)

	from := len(a.Received())
	r := ok(t, pc, proto.UIHub{Mode: proto.HubAgents, Query: "fix", Here: ts.dir})
	if r.Msg.T != proto.TypeOK {
		t.Fatalf("ui.hub answered %s", r.Msg.T)
	}
	m := a.WaitMessage(proto.TypeUIHub, from)
	var u proto.UIHub
	if err := m.Decode(&u); err != nil {
		t.Fatal(err)
	}
	if m.ID != 0 || u != (proto.UIHub{Mode: proto.HubAgents, Query: "fix", Here: ts.dir, Pane: p}) {
		t.Fatalf("the routed copy is %+v with id %d", u, m.ID)
	}
	noRouted(t, ts, b, 0, "the window of another session")
}

func TestRoutedSwitchPrefersTheConnectionWithTheLatestInput(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	s2, _, _ := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b := claiming(ts, proto.CapRoute), claiming(ts, proto.CapRoute)
	ts.view(a, s, 80, 24)
	ts.view(b, s, 80, 24) // b's view.set is the latest interaction
	pc := paneCaller(ts, p)

	fromA, fromB := len(a.Received()), len(b.Received())
	ok(t, pc, proto.UISwitch{Session: s2})
	m := b.WaitMessage(proto.TypeUISwitch, fromB)
	var u proto.UISwitch
	m.Decode(&u)
	if m.ID != 0 || u != (proto.UISwitch{Session: s2}) {
		t.Fatalf("the routed copy is %+v with id %d", u, m.ID)
	}
	noRouted(t, ts, a, fromA, "the window with the older input")

	a.Key(p, "a", 0, "a")
	ts.sync(a)
	fromA, fromB = len(a.Received()), len(b.Received())
	ok(t, pc, proto.UISwitch{Session: s2, Tab: "t9", Pane: "p9"})
	m = a.WaitMessage(proto.TypeUISwitch, fromA)
	m.Decode(&u)
	if u != (proto.UISwitch{Session: s2, Tab: "t9", Pane: "p9"}) {
		t.Fatalf("the routed copy is %+v", u)
	}
	noRouted(t, ts, b, fromB, "the window with the older input")
}

func TestNoClientWhenNothingDisplaysThePane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	s2, _, _ := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	ts.display(s) // the pane runs, no window shows it
	b := claiming(ts, proto.CapRoute)
	ts.view(b, s2, 80, 24)
	pc := paneCaller(ts, p)

	for _, req := range []proto.Typed{
		proto.UIHub{Mode: proto.HubAll},
		proto.UISwitch{Dir: ts.dir, Name: "work"},
		proto.UISwitch{Session: s2},
	} {
		if r := pc.Request(req); r.OK || r.Code != proto.CodeNoClient {
			t.Fatalf("%s answered %+v, want no-client", req.Type(), r)
		}
	}
	noRouted(t, ts, b, 0, "the window of another session")
	if n := len(ts.stateGet(c).Sessions); n != 2 {
		t.Fatalf("%d sessions after the routed requests, want the 2 there were", n)
	}
}

func TestNoPaneForAnUnknownCaller(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := claiming(ts, proto.CapRoute)
	ts.view(a, s, 80, 24)

	for _, req := range []proto.Typed{proto.UIHub{Mode: proto.HubAll}, proto.UISwitch{Session: s}} {
		if r := c.Request(req); r.OK || r.Code != proto.CodeNoPane {
			t.Fatalf("%s outside a pane answered %+v, want no-pane", req.Type(), r)
		}
		if r := a.Request(req); r.OK || r.Code != proto.CodeNoPane {
			t.Fatalf("%s of a window answered %+v, want no-pane", req.Type(), r)
		}
	}

	// A pane that closed after the caller connected.
	var split proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&split)
	gone := paneCaller(ts, split.Pane)
	ok(t, c, proto.PaneClose{Pane: split.Pane})
	if r := gone.Request(proto.UIHub{Mode: proto.HubAll}); r.OK || r.Code != proto.CodeNoPane {
		t.Fatalf("ui.hub from a closed pane answered %+v, want no-pane", r)
	}
	noRouted(t, ts, a, 0, "the window")
}

func TestARoutedSwitchIsCheckedBeforeAWindowMoves(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := claiming(ts, proto.CapRoute)
	ts.view(a, s, 80, 24)
	pc := paneCaller(ts, p)

	for _, tc := range []struct {
		req  proto.Typed
		code string
	}{
		{proto.UISwitch{Session: "s999"}, proto.CodeNoSession},
		{proto.UISwitch{Dir: ts.dir + "/missing", Name: "x"}, proto.CodeNoDir},
		{proto.UISwitch{Dir: "relative/dir"}, proto.CodeInvalid},
		{proto.UISwitch{}, proto.CodeInvalid},
		{proto.UISwitch{Session: s, Dir: ts.dir}, proto.CodeInvalid},
		{proto.UIHub{Mode: "everything"}, proto.CodeInvalid},
	} {
		if r := pc.Request(tc.req); r.OK || r.Code != tc.code {
			t.Errorf("%s %+v answered %+v, want %s", tc.req.Type(), tc.req, r, tc.code)
		}
	}
	// A routed copy sent by a client is not a request: nothing happens.
	pc.Send(proto.UIHub{Mode: proto.HubAll})
	noRouted(t, ts, a, 0, "the window")
}

func TestARoutedRequestReachesABridgeConnection(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	other := mkdir(t, ts.dir+"/other")
	bridge := prototest.Dial(t, ts.socket, bridgeHello())
	ts.view(bridge, s, 80, 24)
	pc := paneCaller(ts, p)

	from := len(bridge.Received())
	ok(t, pc, proto.UISwitch{Dir: other, Name: "work-other"})
	m := bridge.WaitMessage(proto.TypeUISwitch, from)
	var u proto.UISwitch
	m.Decode(&u)
	if u != (proto.UISwitch{Dir: other, Name: "work-other"}) {
		t.Fatalf("the bridge received %+v", u)
	}
}

func TestSessionCloseByDirectoryEndsItsViewers(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, _ := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	dir := a.State().Sessions[s].Dir

	from := len(a.Received())
	var res proto.SessionCloseResult
	ok(t, c, proto.SessionClose{Dir: ts.dir}).Msg.Decode(&res)
	if !res.Closed {
		t.Fatal("session.close of a session's directory answered closed false")
	}
	var ended proto.ViewEnded
	if err := a.WaitMessage(proto.TypeViewEnded, from).Decode(&ended); err != nil {
		t.Fatal(err)
	}
	if ended.Session != s || ended.Dir != dir {
		t.Fatalf("view.ended %+v, want session %s in %s", ended, s, dir)
	}
	if _, ok := a.State().Sessions[s]; ok {
		t.Fatal("the closed session is still in the viewer's state")
	}
}

func TestClosingAMissingSessionIsNotAnError(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	ts.open(c)
	for _, req := range []proto.SessionClose{
		{Dir: ts.dir + "/never-opened"},
		{Dir: mkdir(t, ts.dir+"/exists")},
		{Session: "s999"},
	} {
		var res proto.SessionCloseResult
		ok(t, c, req).Msg.Decode(&res)
		if res.Closed {
			t.Fatalf("session.close %+v answered closed true", req)
		}
	}
	if n := len(ts.stateGet(c).Sessions); n != 1 {
		t.Fatalf("%d sessions, want the one that was open", n)
	}
}

// --- agents ---

// agentServer is a server whose process table is read often enough for an
// agent to be found and to end within a test.
func agentServer(t *testing.T) *testServer {
	return newTestServer(t, func(o *Options) { o.ProcInterval = 50 * time.Millisecond })
}

// runClaude puts Claude Code into the foreground of pane p and waits until
// w's state holds the agent.
func runClaude(t *testing.T, ts *testServer, w *prototest.Client, p string, pgid int) *fakePane {
	t.Helper()
	f := ts.pane(p)
	setProcs(t, append(group(pgid, "claude"), group(f.Info().Shell, "zsh")...)...)
	f.setFg(pgid)
	w.WaitState("the pane runs claude", func(st proto.State) bool { return st.Agents[p].Kind == proto.AgentClaude })
	return f
}

func waitStatus(w *prototest.Client, p, status string) proto.Agent {
	st := w.WaitState("the agent is "+status, func(st proto.State) bool { return st.Agents[p].Status == status })
	return st.Agents[p]
}

func agentEvents(t *testing.T, c *prototest.Client, from int) []proto.AgentEvent {
	t.Helper()
	ms, _ := messagesOf(c, from, proto.TypeEventAgent)
	out := make([]proto.AgentEvent, len(ms))
	for i, m := range ms {
		if err := m.Decode(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestAgentStateEntersTheSnapshotAndTheDelta(t *testing.T) {
	ts := agentServer(t)
	c := ts.oneshot()
	s, tab, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	from := len(a.Received())

	f := runClaude(t, ts, a, p, fakePidBase+9000)
	f.report(vt.AgentReport{State: "working"})
	waitStatus(a, p, proto.StatusWorking)
	if ds := deltasOf(t, a, from); !slices.ContainsFunc(ds, func(d proto.Delta) bool { return touches(d, proto.KindAgent, p) }) {
		t.Fatal("no delta carried the agent")
	}
	snap := ts.stateGet(c)
	cwd := a.State().Panes[p].Cwd
	if len(snap.Agents) != 1 {
		t.Fatalf("state.get has agents %+v", snap.Agents)
	}
	got := snap.Agents[0]
	if got.Pane != p || got.Session != s || got.Tab != tab || got.Kind != proto.AgentClaude ||
		got.Status != proto.StatusWorking || got.Cwd != cwd || got.Since == 0 {
		t.Fatalf("the agent in the snapshot is %+v", got)
	}
	if late := ts.interactive(); late.State().Agents[p] != got {
		t.Fatalf("a new connection's first snapshot has %+v, want %+v", late.State().Agents[p], got)
	}

	// Claude leaves the foreground for another program: after two polls
	// without it the pane is no agent any more.
	g := fakePidBase + 9100
	setProcs(t, group(g, "make")...)
	f.setFg(g)
	a.WaitState("the agent is gone", func(st proto.State) bool { _, ok := st.Agents[p]; return !ok })
	if snap := ts.stateGet(c); len(snap.Agents) != 0 {
		t.Fatalf("state.get still has agents %+v", snap.Agents)
	}
}

func TestAgentTransitionReachesEveryInteractiveConnection(t *testing.T) {
	ts := agentServer(t)
	c := ts.oneshot()
	s, tab, p := ts.open(c)
	s2, _, _ := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b, idle := claiming(ts, proto.CapAgents), claiming(ts, proto.CapAgents), claiming(ts, proto.CapAgents)
	ts.view(a, s, 80, 24)
	ts.view(b, s2, 80, 24)
	f := runClaude(t, ts, a, p, fakePidBase+9200)
	f.setTitle("fix the tests")
	f.report(vt.AgentReport{State: "working"})
	waitStatus(a, p, proto.StatusWorking)
	fa, fb, fi := len(a.Received()), len(b.Received()), len(idle.Received())

	f.report(vt.AgentReport{State: "blocked", Kind: "permission"})
	want := proto.AgentEvent{Session: s, Tab: tab, Pane: p, Kind: proto.AgentClaude, Status: proto.StatusBlocked, Title: "fix the tests", Shown: true}
	for _, w := range []*prototest.Client{a, b, idle} {
		w.WaitMessage(proto.TypeEventAgent, 0)
	}
	f.report(vt.AgentReport{State: "blocked", Kind: "permission"}) // no transition
	for name, w := range map[string]struct {
		c    *prototest.Client
		from int
	}{"the viewer": {a, fa}, "the window of another session": {b, fb}, "the window without a view": {idle, fi}} {
		ts.sync(w.c)
		evs := agentEvents(t, w.c, w.from)
		if len(evs) != 1 || evs[0] != want {
			t.Errorf("%s received %+v, want once %+v", name, evs, want)
		}
	}
	if ms, _ := messagesOf(c, 0, proto.TypeEventAgent); len(ms) != 0 {
		t.Fatal("a one-shot received event.agent")
	}
}

func TestAnAgentEventSaysWhetherAConnectionShowsThePane(t *testing.T) {
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := claiming(ts, proto.CapAgents)
	ts.view(a, s, 80, 24)
	f := runClaude(t, ts, a, p, fakePidBase+9300)

	event := func(state, want string) proto.AgentEvent {
		t.Helper()
		f.report(vt.AgentReport{State: "working"})
		waitStatus(a, p, proto.StatusWorking)
		from := len(a.Received())
		f.report(vt.AgentReport{State: state})
		var e proto.AgentEvent
		a.WaitMessage(proto.TypeEventAgent, from).Decode(&e)
		if e.Status != want {
			t.Fatalf("event.agent %+v, want status %s", e, want)
		}
		return e
	}
	if e := event("blocked", proto.StatusBlocked); !e.Shown {
		t.Fatal("a focused window displays the pane's tab: shown must be true")
	}

	a.Send(proto.Focus{Focused: false})
	ts.sync(a)
	if e := event("blocked", proto.StatusBlocked); e.Shown {
		t.Fatal("the only window lost the focus: shown must be false")
	}
	// Idle after working is done once the debounce has passed, unseen: the
	// tracker's timer runs on the loop.
	if e := event("idle", proto.StatusDone); e.Shown {
		t.Fatal("done while no focused window shows the pane: shown must be false")
	}
	waitStatus(a, p, proto.StatusDone)

	// Another tab of the session is current: the window no longer shows it.
	a.Send(proto.Focus{Focused: true})
	ts.sync(a)
	ok(t, c, proto.TabNew{Session: s})
	a.WaitState("the new tab is current", func(st proto.State) bool { return st.Sessions[s].Current != st.Panes[p].Tab })
	if e := event("blocked", proto.StatusBlocked); e.Shown {
		t.Fatal("the pane's tab is not the current one: shown must be false")
	}
}

func TestAFocusedBridgeConnectionMarksTheAgentSeen(t *testing.T) {
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	a.Send(proto.Focus{Focused: false})
	ts.sync(a)
	f := runClaude(t, ts, a, p, fakePidBase+9400)
	f.report(vt.AgentReport{State: "working"})
	waitStatus(a, p, proto.StatusWorking)
	f.report(vt.AgentReport{State: "idle"})
	waitStatus(a, p, proto.StatusDone)

	bridge := prototest.Dial(t, ts.socket, bridgeHello())
	ts.view(bridge, s, 80, 24)
	waitStatus(a, p, proto.StatusIdle)
	testkit.Eventually(t, 5*time.Second, "the bridge's state has the agent idle", func() bool {
		return bridge.State().Agents[p].Status == proto.StatusIdle
	})
}

func TestCapabilitiesAreAnnounced(t *testing.T) {
	ts := newTestServer(t)
	for _, w := range []proto.Welcome{ts.interactive().Welcome(), ts.oneshot().Welcome(), prototest.Dial(t, ts.socket, bridgeHello()).Welcome()} {
		for _, cap := range []string{proto.CapCatalog, proto.CapAgents, proto.CapRoute, proto.CapStateGet} {
			if n := countOf(w.Caps, cap); n != 1 {
				t.Fatalf("welcome.caps %v has %s %d times, want once", w.Caps, cap, n)
			}
		}
	}
	// The agents family has no request of a client.
	if r := ts.oneshot().Request(proto.AgentEvent{}); r.OK || r.Code != proto.CodeInvalid {
		t.Fatalf("event.agent from a client answered %+v, want invalid", r)
	}
}

func countOf(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

// The stream of a watch keeps the order of its scan however the loop
// orders the posts: many messages from many goroutines arrive in the order
// emit was called, done last.
func TestTheCatalogStreamKeepsTheOrderOfItsScan(t *testing.T) {
	ts := newTestServer(t)
	const n = 300
	setCatalogRun(ts, func(ctx context.Context, _ func() (config.Config, error), emit func(proto.Msg)) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		next := 0
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					mu.Lock()
					if next == n {
						mu.Unlock()
						return
					}
					next++
					emit(proto.Encode(proto.CatalogError{Path: "/p", Text: strings.Repeat("x", next)}))
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		emit(proto.Encode(proto.CatalogDone{}))
	})
	a := ts.interactive()
	watchCatalog(a, 5)
	a.WaitMessage(proto.TypeCatalogDone, 0)
	msgs := catalogOf(a, 0)
	if len(msgs) != n+1 {
		t.Fatalf("%d messages, want %d", len(msgs), n+1)
	}
	for i, m := range msgs[:n] {
		var e proto.CatalogError
		m.Decode(&e)
		if len(e.Text) != i+1 {
			t.Fatalf("message %d is the %d-th the scan emitted", i, len(e.Text))
		}
	}
}

func TestARoutedRequestSkipsAWindowThatDoesNotClaimRoute(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	bare := ts.interactive() // claims no capability
	ts.view(bare, s, 80, 24)
	pc := paneCaller(ts, p)

	if r := pc.Request(proto.UIHub{Mode: proto.HubAll}); r.OK || r.Code != proto.CodeNoClient {
		t.Fatalf("ui.hub with only a window without route answered %+v, want no-client", r)
	}
	noRouted(t, ts, bare, 0, "the window without route")

	a := claiming(ts, proto.CapRoute)
	ts.view(a, s, 80, 24)
	bare.Key(p, "a", 0, "a") // the latest input is the bare window's
	ts.sync(bare)
	from := len(a.Received())
	ok(t, pc, proto.UIHub{Mode: proto.HubAll})
	a.WaitMessage(proto.TypeUIHub, from)
	noRouted(t, ts, bare, 0, "the window without route")
}

// A window whose hello does not claim agents receives no event.agent (4.1,
// 4.2.8), but the agent record still reaches its state: records are objects
// of the generation-1 state that every receiver applies.
func TestAnAgentEventSkipsAConnectionThatDoesNotClaimAgents(t *testing.T) {
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := claiming(ts, proto.CapAgents)
	bare := claiming(ts, proto.CapCatalog, proto.CapRoute, proto.CapStateGet) // every capability but agents
	ts.view(a, s, 80, 24)
	ts.view(bare, s, 80, 24)
	f := runClaude(t, ts, a, p, fakePidBase+9500)
	f.report(vt.AgentReport{State: "working"})
	waitStatus(a, p, proto.StatusWorking)

	from := len(a.Received())
	f.report(vt.AgentReport{State: "blocked", Kind: "permission"})
	a.WaitMessage(proto.TypeEventAgent, from)
	waitStatus(bare, p, proto.StatusBlocked)
	ts.sync(bare)
	if evs := agentEvents(t, bare, 0); len(evs) != 0 {
		t.Fatalf("a window without %s received event.agent %+v", proto.CapAgents, evs)
	}
}
