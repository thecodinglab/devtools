package server

import (
	"fmt"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
)

func TestTheDeltaComesBeforeTheReply(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	s, _, p := ts.open(i)
	for _, req := range []proto.Typed{
		proto.TabNew{Session: s},
		proto.PaneSplit{Pane: p, Direction: proto.SplitDown},
		proto.TabSelect{Session: s, Step: 1},
	} {
		from := len(i.Received())
		r := ok(t, i, req)
		// On ok the client's state already holds the result (W2).
		st := i.State()
		switch q := req.(type) {
		case proto.TabNew:
			var res proto.TabNewResult
			r.Msg.Decode(&res)
			if _, has := st.Tabs[res.Tab]; !has || st.Sessions[s].Current != res.Tab {
				t.Fatalf("tab.new answered %+v before its delta arrived", res)
			}
		case proto.PaneSplit:
			var res proto.PaneSplitResult
			r.Msg.Decode(&res)
			if _, has := st.Panes[res.Pane]; !has || st.Tabs[st.Panes[q.Pane].Tab].Focused != res.Pane {
				t.Fatalf("pane.split answered %+v before its delta arrived", res)
			}
		}
		got := i.Received()[from:]
		delta, reply := -1, -1
		for n, m := range got {
			if m.T == proto.TypeStateDelta && delta < 0 {
				delta = n
			}
			if m.T == proto.TypeOK && m.ID == r.Msg.ID {
				reply = n
			}
		}
		if delta < 0 || reply < 0 || delta > reply {
			t.Fatalf("%s: messages %v: the delta (%d) must come before the reply (%d)", req.Type(), typesOf(got), delta, reply)
		}
	}
}

func typesOf(ms []proto.Msg) []string {
	var ts []string
	for _, m := range ms {
		ts = append(ts, m.T)
	}
	return ts
}

// revisions returns the revision of the snapshot of c and of every delta it
// received, in order.
func revisions(t *testing.T, c *prototest.Client) (snap uint64, deltas []uint64) {
	t.Helper()
	for _, m := range c.Received() {
		switch {
		case m.T == proto.TypeStateSnapshot && m.ID == 0:
			var s proto.Snapshot
			m.Decode(&s)
			snap = s.Rev
		case m.T == proto.TypeStateDelta:
			var d proto.Delta
			m.Decode(&d)
			deltas = append(deltas, d.Rev)
		}
	}
	return snap, deltas
}

func TestRevisionsAreConsecutive(t *testing.T) {
	ts := newTestServer(t)
	first := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ts.display(s)
	var second *prototest.Client
	for n := 0; n < 20; n++ {
		if n == 10 {
			second = ts.interactive()
		}
		ok(t, c, proto.TabNew{Session: s})
		ts.panes.get(p).setTitle(fmt.Sprintf("title %d", n)) // cosmetic, coalesced
	}
	// The titles reach the loop through the observer, so some may be applied
	// after state.get, and a flush of held ones may follow it: a client can
	// pass last between two looks at its state. Wait for at least last and
	// for the last title, not for last exactly.
	last := ts.stateGet(c).Rev
	for name, cl := range map[string]*prototest.Client{"first": first, "second": second} {
		cl.WaitState("the last revision and title", func(st proto.State) bool {
			return st.Rev >= last && st.Panes[p].Title == "title 19"
		})
		snap, ds := revisions(t, cl)
		want := snap + 1
		for _, rev := range ds {
			if rev != want {
				t.Fatalf("%s connection: snapshot %d, deltas %v: not consecutive", name, snap, ds)
			}
			want++
		}
		if len(ds) == 0 {
			t.Fatalf("%s connection received no delta", name)
		}
	}
}

func TestStructuralChangesAreSentAtOnce(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.CosmeticDelay = time.Hour })
	i := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	i.WaitState("the session", func(st proto.State) bool { _, ok := st.Sessions[s]; return ok })
	ts.display(s)
	i.WaitState("the pane running", func(st proto.State) bool { return st.Panes[p].State == proto.PaneRunning })
	// busy and editor are structural, also when no request caused them
	// (the process scan, P1-16, sets them this way).
	ts.do(func(c *core) {
		c.apply(c.model.SetPane(p, func(x *proto.Pane) { x.Busy, x.Program = true, "make" }), false)
	})
	st := i.WaitState("busy", func(st proto.State) bool { return st.Panes[p].Busy })
	if st.Panes[p].Program != "make" {
		t.Fatalf("pane %+v", st.Panes[p])
	}
}

func TestCosmeticChangesAreCoalesced(t *testing.T) {
	const delay = 300 * time.Millisecond
	ts := newTestServer(t, func(o *Options) { o.CosmeticDelay = delay })
	i := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ts.display(s)
	i.WaitState("the pane running", func(st proto.State) bool { return st.Panes[p].State == proto.PaneRunning })
	f := ts.panes.get(p)

	from := len(i.Received())
	start := time.Now()
	for n := 0; n < 10; n++ {
		f.setTitle(fmt.Sprintf("title %d", n))
	}
	i.WaitState("the last title", func(st proto.State) bool { return st.Panes[p].Title == "title 9" })
	if d := time.Since(start); d < delay-10*time.Millisecond {
		t.Fatalf("the titles arrived after %v, before the cosmetic delay of %v", d, delay)
	}
	n := 0
	for _, d := range deltasOf(t, i, from) {
		if touches(d, proto.KindPane, p) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("ten titles within the delay came in %d deltas, want 1", n)
	}

	// A structural delta sends the cosmetic ones first, without waiting.
	slow := newTestServer(t, func(o *Options) { o.CosmeticDelay = time.Hour })
	i = slow.interactive()
	c = slow.oneshot()
	s, _, p = slow.open(c)
	slow.display(s)
	i.WaitState("the pane running", func(st proto.State) bool { return st.Panes[p].State == proto.PaneRunning })
	from = len(i.Received())
	slow.panes.get(p).setTitle("vim")
	slow.do(func(*core) {}) // the title is in the model
	var tr proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tr)
	st := i.WaitState("the new tab", func(st proto.State) bool { _, ok := st.Tabs[tr.Tab]; return ok })
	if st.Panes[p].Title != "vim" {
		t.Fatalf("the title did not come with the structural delta: %+v", st.Panes[p])
	}
	ds := deltasOf(t, i, from)
	if len(ds) != 2 || !touches(ds[0], proto.KindPane, p) || touches(ds[0], proto.KindTab, tr.Tab) || !touches(ds[1], proto.KindTab, tr.Tab) {
		t.Fatalf("want the cosmetic delta, then the structural one: %+v", ds)
	}
}
