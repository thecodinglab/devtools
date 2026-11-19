package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/session"
	"devtools/internal/testkit"
)

func TestViewSetIsAtomic(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s1, _, p1 := ts.open(c)
	ts.view(i, s1, 80, 24)
	f := ts.pane(p1)
	f.write(0, "before")
	i.WaitFor(p1, "before")

	s2, _, _ := ts.openDir(c, mkdir(t, filepath.Join(ts.dir, "two")))
	other := mkdir(t, filepath.Join(ts.dir, "three"))
	tab1 := ts.stateGet(c).Sessions[0].Current
	for _, q := range []struct {
		v    proto.ViewSet
		code string
	}{
		{proto.ViewSet{Session: "s999", Cols: 100, Rows: 30}, proto.CodeNoSession},
		{proto.ViewSet{Session: s2, Tab: tab1, Cols: 100, Rows: 30}, proto.CodeNoTab},
		{proto.ViewSet{Session: s2, Pane: p1, Cols: 100, Rows: 30}, proto.CodeNoPane},
		{proto.ViewSet{Dir: other, Name: "three", Tab: tab1, Cols: 100, Rows: 30}, proto.CodeNoTab},
		{proto.ViewSet{Dir: filepath.Join(ts.dir, "missing"), Cols: 100, Rows: 30}, proto.CodeNoDir},
		{proto.ViewSet{Session: s1, Dir: ts.dir, Cols: 100, Rows: 30}, proto.CodeInvalid},
	} {
		r := i.Request(q.v)
		if r.OK || r.Code != q.code {
			t.Fatalf("view.set %+v was answered %s %s, want err %s", q.v, r.Msg.T, r.Code, q.code)
		}
	}

	// The previous view goes on: same frames, same size, no new pane.
	f.write(0, "after")
	i.WaitFor(p1, "after")
	if got := ts.size(s1); got != [2]int{80, 24} {
		t.Fatalf("a refused view.set changed the size to %v", got)
	}
	if got := f.lastResize(); got != [2]int{80, 24} {
		t.Fatalf("a refused view.set resized the pane to %v", got)
	}
	// The session that step 1 opened by dir stays, and nothing of it runs.
	st := ts.stateGet(c)
	var three proto.Session
	for _, sess := range st.Sessions {
		if sess.Dir == other {
			three = sess
		}
	}
	if three.ID == "" {
		t.Fatalf("the session opened by dir before the refusal is gone: %+v", st.Sessions)
	}
	for _, p := range st.Panes {
		if p.Session == three.ID && p.State != proto.PaneDormant {
			t.Fatalf("a pane of the refused view runs: %+v", p)
		}
	}
	if v, _ := ts.viewOf(i); v != s1 {
		t.Fatalf("the view is %q after the refusals, want %s", v, s1)
	}
}

// viewOf returns the session the server holds as the view of the
// interactive connection that c is (the newest interactive connection).
func (ts *testServer) viewOf(cl *prototest.Client) (string, bool) {
	var view string
	var found bool
	ts.do(func(c *core) {
		id := cl.Welcome().Client
		for _, cn := range c.reg.list() {
			if cn.id == id {
				view, found = cn.view, true
			}
		}
	})
	return view, found
}

func TestViewOKComesBeforeTheFirstFrame(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, tab, p := ts.open(c)
	for range 20 {
		i := ts.interactive()
		from := len(i.Received())
		v := ts.view(i, s, 80, 24)
		if v.Session != s || v.Tab != tab || len(v.Panes) != 1 || v.Panes[0] != p || v.Dir != ts.dir {
			t.Fatalf("view.ok %+v", v)
		}
		testkit.Eventually(t, 5*time.Second, "the first frame", func() bool { return i.Frames(p) > 0 })
		_, oks := messagesOf(i, from, proto.TypeViewOK)
		_, at := framesOf(t, i, from, p)
		if len(oks) != 1 || at[0] < oks[0] {
			t.Fatalf("view.ok at %v, the frames at %v: %v", oks, at, i.Types())
		}
		i.Close()
	}
}

func TestTheFirstFrameOfEachPaneIsFull(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&sp)
	i := ts.interactive()
	from := len(i.Received())
	v := ts.view(i, s, 81, 24)
	if len(v.Panes) != 2 {
		t.Fatalf("view.ok %+v", v)
	}
	want := map[string][2]int{p: {40, 24}, sp.Pane: {40, 24}}
	for _, id := range v.Panes {
		testkit.Eventually(t, 5*time.Second, "the first frame of "+id, func() bool { return i.Frames(id) > 0 })
		fs, _ := framesOf(t, i, from, id)
		f := fs[0]
		if f.Seq != 1 || !f.Full || len(f.Lines) != f.Rows || [2]int{f.Cols, f.Rows} != want[id] {
			t.Fatalf("the first frame of %s: seq %d full %t, %d lines, %dx%d; want seq 1, full, every row of %v",
				id, f.Seq, f.Full, len(f.Lines), f.Cols, f.Rows, want[id])
		}
	}
}

func TestViewSetByDirectoryOpensTheSession(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	dir := mkdir(t, filepath.Join(ts.dir, "x", "main"))
	link := filepath.Join(ts.dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	v := i.View(proto.ViewSet{Dir: link, Name: "x-main", Cols: 100, Rows: 30})
	key, _ := session.Key(dir)
	if v.Session == "" || v.Dir != key || len(v.Panes) != 1 {
		t.Fatalf("view.ok %+v; want the session of %s", v, key)
	}
	st := i.WaitState("the session", func(st proto.State) bool { _, ok := st.Sessions[v.Session]; return ok })
	if sess := st.Sessions[v.Session]; sess.Label != "x-main" || sess.Dir != key || sess.Shown == 0 {
		t.Fatalf("session %+v", sess)
	}
	if f := ts.pane(v.Panes[0]); f.o.Cols != 100 || f.o.Rows != 30 || f.o.Dir != key {
		t.Fatalf("the pane started at %dx%d in %s", f.o.Cols, f.o.Rows, f.o.Dir)
	}
	// The same directory again finds the session.
	again := i.View(proto.ViewSet{Dir: dir, Name: "other", Cols: 100, Rows: 30})
	if again.Session != v.Session {
		t.Fatalf("view.set by the same dir gave %s, then %s", v.Session, again.Session)
	}
	for _, q := range []proto.ViewSet{{Cols: 10, Rows: 10}, {Dir: "relative/dir", Cols: 10, Rows: 10}} {
		if r := i.Request(q); r.OK || r.Code != proto.CodeInvalid {
			t.Fatalf("view.set %+v answered %s %s, want invalid", q, r.Msg.T, r.Code)
		}
	}
}

func TestViewSetWithTabAndPane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, t1, p1 := ts.open(c)
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p1, Direction: proto.SplitDown}).Msg.Decode(&sp)
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)

	i := ts.interactive()
	v := i.View(proto.ViewSet{Session: s, Tab: t1, Pane: p1, Cols: 80, Rows: 24})
	if v.Tab != t1 || len(v.Panes) != 2 || v.Panes[0] != p1 || v.Panes[1] != sp.Pane {
		t.Fatalf("view.ok %+v", v)
	}
	st := ts.stateGet(c)
	for _, tab := range st.Tabs {
		if tab.ID == t1 && tab.Focused != p1 {
			t.Fatalf("tab %s focuses %s, want %s", t1, tab.Focused, p1)
		}
	}
	if st.Sessions[0].Current != t1 {
		t.Fatalf("the current tab is %s, want %s", st.Sessions[0].Current, t1)
	}
	// Both panes of the tab were spawned before view.ok at their final size.
	if a, b := ts.panes.get(p1), ts.panes.get(sp.Pane); a == nil || b == nil || a.o.Rows != 12 || b.o.Rows != 11 {
		t.Fatalf("the panes of the tab were not spawned at 80x12 and 80x11 before view.ok")
	}

	// A pane alone selects its tab.
	v = i.View(proto.ViewSet{Session: s, Pane: tn.Pane, Cols: 80, Rows: 24})
	if v.Tab != tn.Tab || len(v.Panes) != 1 || v.Panes[0] != tn.Pane {
		t.Fatalf("view.ok %+v; want tab %s", v, tn.Tab)
	}
	// A pane of another tab than the one named is no-pane.
	if r := i.Request(proto.ViewSet{Session: s, Tab: tn.Tab, Pane: p1, Cols: 80, Rows: 24}); r.OK || r.Code != proto.CodeNoPane {
		t.Fatalf("view.set with a pane of another tab: %s %s", r.Msg.T, r.Code)
	}
}

func TestATabChangeSendsTheDeltaThenFullFrames(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, t1, p1 := ts.open(c)
	ts.view(i, s, 80, 24)
	a := ts.pane(p1)
	a.write(0, "first tab")
	i.WaitFor(p1, "first tab")

	for round := 0; round < 3; round++ {
		from := len(i.Received())
		var tn proto.TabNewResult
		ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
		b := ts.pane(tn.Pane)
		b.write(1, "second tab")
		i.WaitFor(tn.Pane, "second tab")
		ds, at := messagesOf(i, from, proto.TypeStateDelta)
		cur := -1
		for k, m := range ds {
			var d proto.Delta
			m.Decode(&d)
			if touches(d, proto.KindSession, s) {
				cur = at[k]
				break
			}
		}
		fs, fat := framesOf(t, i, from, tn.Pane)
		if cur < 0 || fat[0] < cur || fs[0].Seq != 1 || !fs[0].Full {
			t.Fatalf("the delta of the tab change at %d, the first frame of the new tab at %d (seq %d, full %t)", cur, fat[0], fs[0].Seq, fs[0].Full)
		}
		if _, old := framesOf(t, i, cur, p1); len(old) != 0 {
			t.Fatalf("a frame of the old tab came after the delta that changed the tab")
		}
		// Back to the first tab: its pane starts again at seq 1.
		from = len(i.Received())
		ok(t, c, proto.TabSelect{Session: s, Tab: t1})
		a.write(0, "first tab again "+string(rune('a'+round)))
		i.WaitFor(p1, "first tab again "+string(rune('a'+round)))
		if fs, _ := framesOf(t, i, from, p1); fs[0].Seq != 1 || !fs[0].Full {
			t.Fatalf("the first frame after the return is seq %d full %t", fs[0].Seq, fs[0].Full)
		}
		ok(t, c, proto.TabClose{Tab: tn.Tab})
	}
}

func TestViewEndedWhenTheSessionCloses(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ts.view(i, s, 80, 24)
	testkit.Eventually(t, 5*time.Second, "the first frame", func() bool { return i.Frames(p) > 0 })
	from := len(i.Received())
	ok(t, c, proto.SessionClose{Session: s})
	m := i.WaitMessage(proto.TypeViewEnded, from)
	var e proto.ViewEnded
	m.Decode(&e)
	if e.Session != s || e.Dir != ts.dir {
		t.Fatalf("view.ended %+v", e)
	}
	ds, dat := messagesOf(i, from, proto.TypeStateDelta)
	_, eat := messagesOf(i, from, proto.TypeViewEnded)
	deleted := -1
	for k, dm := range ds {
		var d proto.Delta
		dm.Decode(&d)
		if touches(d, proto.KindSession, s) {
			deleted = dat[k]
		}
	}
	if deleted < 0 || eat[0] < deleted {
		t.Fatalf("view.ended at %d, the delta that deleted the session at %d", eat[0], deleted)
	}
	if v, _ := ts.viewOf(i); v != "" {
		t.Fatalf("the connection still views %s", v)
	}
	// The last tab closed ends the view as well.
	s2, t2, _ := ts.open(c)
	ts.view(i, s2, 80, 24)
	from = len(i.Received())
	ok(t, c, proto.TabClose{Tab: t2})
	i.WaitMessage(proto.TypeViewEnded, from)
}

func TestNoFrameAfterViewEnded(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.FrameInterval = time.Millisecond })
	c := ts.oneshot()
	for round := 0; round < 10; round++ {
		i := ts.interactive()
		s, _, p := ts.open(c)
		ts.view(i, s, 80, 24)
		f := ts.pane(p)
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				f.write(n%24, "output "+string(rune('a'+n%26)))
				time.Sleep(100 * time.Microsecond)
			}
		}()
		testkit.Eventually(t, 5*time.Second, "frames", func() bool { return i.Frames(p) > 2 })
		from := len(i.Received())
		ok(t, c, proto.SessionClose{Session: s})
		i.WaitMessage(proto.TypeViewEnded, from)
		time.Sleep(20 * time.Millisecond)
		close(stop)
		<-done
		ts.sync(c)
		_, eat := messagesOf(i, from, proto.TypeViewEnded)
		if _, at := framesOf(t, i, eat[0], p); len(at) != 0 {
			t.Fatalf("round %d: %d frames after view.ended", round, len(at))
		}
		i.Close()
	}
}

func TestViewClearStopsFramesAndKeepsTheState(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ts.view(i, s, 80, 24)
	f := ts.pane(p)
	f.write(0, "shown")
	i.WaitFor(p, "shown")
	i.Send(proto.ViewClear{})
	ts.sync(c)
	n := i.Frames(p)
	f.write(0, "hidden")
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	i.WaitState("the new tab", func(st proto.State) bool { _, ok := st.Tabs[tn.Tab]; return ok })
	ts.sync(c)
	if got := i.Frames(p); got != n {
		t.Fatalf("%d frames after view.clear", got-n)
	}
	if v, _ := ts.viewOf(i); v != "" {
		t.Fatalf("the connection views %s after view.clear", v)
	}
}

// rawReader reads the messages of a raw interactive connection and answers
// its pings, for tests that check what prototest would refuse to apply.
type rawReader struct {
	t  *testing.T
	nc net.Conn
}

func rawInteractive(t *testing.T, socket string) *rawReader {
	t.Helper()
	nc, m := rawHello(t, socket, prototest.Interactive())
	if m.T != proto.TypeWelcome {
		t.Fatalf("hello answered %s", m.T)
	}
	r := &rawReader{t: t, nc: nc}
	if m := r.next(); m.T != proto.TypeStateSnapshot {
		t.Fatalf("after welcome %s", m.T)
	}
	return r
}

func (r *rawReader) send(m proto.Msg) {
	r.t.Helper()
	r.nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := proto.WriteMsg(r.nc, m); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rawReader) next() proto.Msg {
	r.t.Helper()
	for {
		r.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
		m, err := proto.ReadMsg(r.nc)
		if err != nil {
			r.t.Fatalf("read: %v", err)
		}
		if m.T == proto.TypePing {
			r.send(proto.Encode(proto.Pong{}))
			continue
		}
		return m
	}
}

// until reads messages until pred holds for one, and returns all read.
func (r *rawReader) until(pred func(proto.Msg) bool) []proto.Msg {
	r.t.Helper()
	var out []proto.Msg
	for {
		m := r.next()
		out = append(out, m)
		if pred(m) {
			return out
		}
	}
}

func frameOf(m proto.Msg) (proto.Frame, bool) {
	var f proto.Frame
	if m.T != proto.TypeFrame || m.Decode(&f) != nil {
		return f, false
	}
	return f, true
}

// A split of the displayed tab changes the connection's displayed set: the
// delta and the new set go out in one step and every pane of the tab, the
// one that stays too, starts again at seq 1 with a full frame, which is what
// the client expects (internal/client/panes Set.Retab).
func TestASplitOfTheDisplayedTabStartsEveryPaneAgain(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	r := rawInteractive(t, ts.socket)
	r.send(proto.Request(1, proto.ViewSet{Session: s, Cols: 80, Rows: 24}))
	r.until(func(m proto.Msg) bool { f, ok := frameOf(m); return ok && f.Pane == p })
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&sp)
	seen := map[string]proto.Frame{}
	delta := false
	r.until(func(m proto.Msg) bool {
		if m.T == proto.TypeStateDelta {
			delta = true
		}
		if f, ok := frameOf(m); ok {
			if !delta {
				return false // a frame of the old set before the delta
			}
			if _, have := seen[f.Pane]; !have {
				seen[f.Pane] = f
			}
		}
		return len(seen) == 2
	})
	width := map[string]int{p: 40, sp.Pane: 39}
	for id, f := range seen {
		if f.Seq != 1 || !f.Full || f.Cols != width[id] {
			t.Fatalf("after the split the first frame of %s is seq %d full %t %dx%d", id, f.Seq, f.Full, f.Cols, f.Rows)
		}
	}
}

// TestASplitAndACloseWhileAClientViews splits and closes panes of the tab a
// prototest client displays: after each change every pane starts again at
// seq 1, full, and the client's frame check accepts it (contract change 11).
func TestASplitAndACloseWhileAClientViews(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ts.view(i, s, 80, 24)
	a := ts.pane(p)
	a.write(0, "before the split")
	i.WaitFor(p, "before the split")

	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&sp)
	b := ts.pane(sp.Pane)
	a.write(1, "left after the split")
	b.write(0, "right after the split")
	i.WaitFor(p, "left after the split")
	i.WaitFor(sp.Pane, "right after the split")

	ok(t, c, proto.PaneClose{Pane: sp.Pane})
	a.write(2, "alone again")
	i.WaitFor(p, "alone again")
	if err := i.Err(); err != nil || i.Closed() {
		t.Fatalf("the viewer failed after a split and a close: %v (closed %t)", err, i.Closed())
	}
}
