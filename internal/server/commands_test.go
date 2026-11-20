package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
)

// fixture is the state every case of the table starts from: one session in
// dir with one tab and one pane; other is a second directory, file a file.
type fixture struct {
	dir, other, file string
	s, t, p          string
	t2, p2           string // set by the prep of a case
}

func TestEveryRequestOfTheTable(t *testing.T) {
	type check func(t *testing.T, f *fixture, r proto.Reply, st proto.State)
	tabNew := func(t *testing.T, c *prototest.Client, f *fixture) {
		var res proto.TabNewResult
		ok(t, c, proto.TabNew{Session: f.s}).Msg.Decode(&res)
		f.t2, f.p2 = res.Tab, res.Pane
	}
	split := func(t *testing.T, c *prototest.Client, f *fixture) {
		var res proto.PaneSplitResult
		ok(t, c, proto.PaneSplit{Pane: f.p, Direction: proto.SplitRight}).Msg.Decode(&res)
		f.p2 = res.Pane
	}
	noSession := func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
		if len(st.Sessions) != 0 || len(st.Tabs) != 0 || len(st.Panes) != 0 {
			t.Fatalf("the session is not gone: %+v", st)
		}
	}
	unchanged := func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
		if len(st.Sessions) != 1 || len(st.Tabs) != 1 || len(st.Panes) != 1 || st.Sessions[f.s].Current != f.t {
			t.Fatalf("a refused request changed the state: %+v", st)
		}
	}
	cases := []struct {
		name  string
		prep  func(t *testing.T, c *prototest.Client, f *fixture)
		req   func(f *fixture) proto.Typed
		code  string // "" for ok
		check check
	}{
		{name: "session.open of a new directory", req: func(f *fixture) proto.Typed { return proto.SessionOpen{Dir: f.other, Name: "other-main"} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.SessionOpenResult
				r.Msg.Decode(&res)
				sess := st.Sessions[res.Session]
				if !res.Created || res.Session == f.s || sess.Dir != f.other || sess.Label != "other-main" || len(sess.Tabs) != 1 {
					t.Fatalf("result %+v, session %+v", res, sess)
				}
				if p := st.Panes[st.Tabs[sess.Current].Focused]; p.State != proto.PaneDormant || p.Cwd != f.other {
					t.Fatalf("the new session's pane is %+v, want dormant in its directory", p)
				}
			}},
		{name: "session.open of an open directory", req: func(f *fixture) proto.Typed { return proto.SessionOpen{Dir: f.dir} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.SessionOpenResult
				r.Msg.Decode(&res)
				if res.Created || res.Session != f.s || len(st.Sessions) != 1 {
					t.Fatalf("result %+v", res)
				}
			}},
		{name: "session.open of a missing directory", req: func(f *fixture) proto.Typed { return proto.SessionOpen{Dir: f.dir + "/missing"} }, code: proto.CodeNoDir, check: unchanged},
		{name: "session.open of a file", req: func(f *fixture) proto.Typed { return proto.SessionOpen{Dir: f.file} }, code: proto.CodeNoDir, check: unchanged},
		{name: "session.open without a directory", req: func(f *fixture) proto.Typed { return proto.SessionOpen{} }, code: proto.CodeInvalid, check: unchanged},
		{name: "session.open of a relative directory", req: func(f *fixture) proto.Typed { return proto.SessionOpen{Dir: "work"} }, code: proto.CodeInvalid, check: unchanged},

		{name: "session.close by id", req: func(f *fixture) proto.Typed { return proto.SessionClose{Session: f.s} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.SessionCloseResult
				r.Msg.Decode(&res)
				if !res.Closed {
					t.Fatal("closed is false")
				}
				noSession(t, f, r, st)
			}},
		{name: "session.close by directory", req: func(f *fixture) proto.Typed { return proto.SessionClose{Dir: f.dir} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.SessionCloseResult
				r.Msg.Decode(&res)
				if !res.Closed {
					t.Fatal("closed is false")
				}
				noSession(t, f, r, st)
			}},
		{name: "session.close of no session", req: func(f *fixture) proto.Typed { return proto.SessionClose{Session: "s99"} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.SessionCloseResult
				r.Msg.Decode(&res)
				if res.Closed {
					t.Fatal("closed is true")
				}
				unchanged(t, f, r, st)
			}},
		{name: "session.close of a directory without a session", req: func(f *fixture) proto.Typed { return proto.SessionClose{Dir: f.other} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.SessionCloseResult
				r.Msg.Decode(&res)
				if res.Closed {
					t.Fatal("closed is true")
				}
				unchanged(t, f, r, st)
			}},
		{name: "session.close of nothing", req: func(f *fixture) proto.Typed { return proto.SessionClose{} }, code: proto.CodeInvalid, check: unchanged},

		{name: "tab.new", req: func(f *fixture) proto.Typed { return proto.TabNew{Session: f.s} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.TabNewResult
				r.Msg.Decode(&res)
				tab := st.Tabs[res.Tab]
				if st.Sessions[f.s].Current != res.Tab || tab.Index != 2 || tab.Focused != res.Pane || st.Panes[res.Pane].Cwd != f.dir {
					t.Fatalf("result %+v, tab %+v, pane %+v", res, tab, st.Panes[res.Pane])
				}
			}},
		{name: "tab.new with a directory", req: func(f *fixture) proto.Typed { return proto.TabNew{Session: f.s, Cwd: f.other} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.TabNewResult
				r.Msg.Decode(&res)
				if st.Panes[res.Pane].Cwd != f.other {
					t.Fatalf("pane %+v", st.Panes[res.Pane])
				}
			}},
		{name: "tab.new in no session", req: func(f *fixture) proto.Typed { return proto.TabNew{Session: "s99"} }, code: proto.CodeNoSession, check: unchanged},
		{name: "tab.new with a relative directory", req: func(f *fixture) proto.Typed { return proto.TabNew{Session: f.s, Cwd: "x"} }, code: proto.CodeInvalid, check: unchanged},

		{name: "tab.select by index", prep: tabNew, req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s, Index: 1} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.TabSelectResult
				r.Msg.Decode(&res)
				if res.Tab != f.t || st.Sessions[f.s].Current != f.t {
					t.Fatalf("result %+v, session %+v", res, st.Sessions[f.s])
				}
			}},
		{name: "tab.select by id", prep: tabNew, req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s, Tab: f.t} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				if st.Sessions[f.s].Current != f.t {
					t.Fatalf("session %+v", st.Sessions[f.s])
				}
			}},
		{name: "tab.select by step wraps", prep: tabNew, req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s, Step: 1} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				if st.Sessions[f.s].Current != f.t {
					t.Fatalf("a step past the last tab did not wrap to the first: %+v", st.Sessions[f.s])
				}
			}},
		{name: "tab.select in no session", req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: "s99", Index: 1} }, code: proto.CodeNoSession, check: unchanged},
		{name: "tab.select of no index", req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s, Index: 2} }, code: proto.CodeNoTab, check: unchanged},
		{name: "tab.select of no tab", req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s, Tab: "t99"} }, code: proto.CodeNoTab, check: unchanged},
		{name: "tab.select of a step of two", req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s, Step: 2} }, code: proto.CodeInvalid, check: unchanged},
		{name: "tab.select of nothing", req: func(f *fixture) proto.Typed { return proto.TabSelect{Session: f.s} }, code: proto.CodeInvalid, check: unchanged},

		{name: "tab.close", prep: tabNew, req: func(f *fixture) proto.Typed { return proto.TabClose{Tab: f.t2} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				if _, has := st.Tabs[f.t2]; has || st.Sessions[f.s].Current != f.t || len(st.Sessions[f.s].Tabs) != 1 {
					t.Fatalf("state %+v", st)
				}
				if _, has := st.Panes[f.p2]; has {
					t.Fatal("the tab's pane stayed")
				}
			}},
		{name: "tab.close of the last tab", req: func(f *fixture) proto.Typed { return proto.TabClose{Tab: f.t} }, check: noSession},
		{name: "tab.close of no tab", req: func(f *fixture) proto.Typed { return proto.TabClose{Tab: "t99"} }, code: proto.CodeNoTab, check: unchanged},

		{name: "pane.split", req: func(f *fixture) proto.Typed { return proto.PaneSplit{Pane: f.p, Direction: proto.SplitDown} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.PaneSplitResult
				r.Msg.Decode(&res)
				tab := st.Tabs[f.t]
				if tab.Split != proto.SplitDown || !reflect.DeepEqual(tab.Panes, []string{f.p, res.Pane}) || tab.Focused != res.Pane || st.Panes[res.Pane].Cwd != f.dir {
					t.Fatalf("result %+v, tab %+v", res, tab)
				}
			}},
		{name: "pane.split of a split tab nests", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneSplit{Pane: f.p, Direction: proto.SplitDown} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.PaneSplitResult
				r.Msg.Decode(&res)
				tab := st.Tabs[f.t]
				want := proto.Node{Split: proto.SplitRight, Nodes: []proto.Node{
					{Split: proto.SplitDown, Weight: 1, Nodes: []proto.Node{{Pane: f.p, Weight: 1}, {Pane: res.Pane, Weight: 1}}},
					{Pane: f.p2, Weight: 1},
				}}
				if tab.Split != proto.SplitRight || !reflect.DeepEqual(tab.Panes, []string{f.p, res.Pane, f.p2}) || !reflect.DeepEqual(tab.Layout, want) || tab.Focused != res.Pane {
					t.Fatalf("result %+v, tab %+v", res, tab)
				}
			}},
		{name: "pane.resize", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneResize{Pane: f.p, Direction: "right", Cells: 5} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.PaneResizeResult
				r.Msg.Decode(&res)
				l := st.Tabs[f.t].Layout
				if !res.Moved || len(l.Nodes) != 2 || l.Nodes[0].Weight != l.Nodes[1].Weight+11 {
					t.Fatalf("result %+v, layout %+v", res, l)
				}
			}},
		{name: "pane.resize without a border that way", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneResize{Pane: f.p, Direction: "up", Cells: 1} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.PaneResizeResult
				r.Msg.Decode(&res)
				want := proto.Row([]string{f.p, f.p2}, proto.SplitRight)
				want.Nodes[0].Weight, want.Nodes[1].Weight = 1, 1
				if res.Moved || !reflect.DeepEqual(st.Tabs[f.t].Layout, want) {
					t.Fatalf("result %+v, layout %+v", res, st.Tabs[f.t].Layout)
				}
			}},
		{name: "pane.resize of no pane", req: func(f *fixture) proto.Typed { return proto.PaneResize{Pane: "p99", Direction: "left", Cells: 1} }, code: proto.CodeNoPane, check: unchanged},
		{name: "pane.resize sideways", req: func(f *fixture) proto.Typed { return proto.PaneResize{Pane: f.p, Direction: "north", Cells: 1} }, code: proto.CodeInvalid, check: unchanged},
		{name: "pane.resize by nothing", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneResize{Pane: f.p, Direction: "left", Cells: 0} }, code: proto.CodeInvalid},
		{name: "pane.split of no pane", req: func(f *fixture) proto.Typed { return proto.PaneSplit{Pane: "p99", Direction: proto.SplitRight} }, code: proto.CodeNoPane, check: unchanged},
		{name: "pane.split sideways", req: func(f *fixture) proto.Typed { return proto.PaneSplit{Pane: f.p, Direction: "left"} }, code: proto.CodeInvalid, check: unchanged},

		{name: "pane.focus", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneFocus{Pane: f.p} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.PaneFocusResult
				r.Msg.Decode(&res)
				if !res.Moved || res.Pane != f.p || st.Tabs[f.t].Focused != f.p {
					t.Fatalf("result %+v, tab %+v", res, st.Tabs[f.t])
				}
			}},
		{name: "pane.focus with a direction", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneFocus{Pane: f.p2, Direction: "left"} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var res proto.PaneFocusResult
				r.Msg.Decode(&res)
				if !res.Moved || res.Pane != f.p || st.Tabs[f.t].Focused != f.p {
					t.Fatalf("result %+v, tab %+v", res, st.Tabs[f.t])
				}
			}},
		{name: "pane.focus of no pane", req: func(f *fixture) proto.Typed { return proto.PaneFocus{Pane: "p99"} }, code: proto.CodeNoPane, check: unchanged},
		{name: "pane.focus diagonally", req: func(f *fixture) proto.Typed { return proto.PaneFocus{Pane: f.p, Direction: "north"} }, code: proto.CodeInvalid, check: unchanged},

		{name: "pane.close of one of two", prep: split, req: func(f *fixture) proto.Typed { return proto.PaneClose{Pane: f.p} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				tab := st.Tabs[f.t]
				if tab.Split != proto.SplitNone || !reflect.DeepEqual(tab.Panes, []string{f.p2}) || tab.Focused != f.p2 {
					t.Fatalf("tab %+v", tab)
				}
				if _, has := st.Panes[f.p]; has {
					t.Fatal("the closed pane stayed")
				}
			}},
		{name: "pane.close of the last pane", req: func(f *fixture) proto.Typed { return proto.PaneClose{Pane: f.p} }, check: noSession},
		{name: "pane.close of no pane", req: func(f *fixture) proto.Typed { return proto.PaneClose{Pane: "p99"} }, code: proto.CodeNoPane, check: unchanged},

		{name: "state.get", req: func(f *fixture) proto.Typed { return proto.StateGet{} },
			check: func(t *testing.T, f *fixture, r proto.Reply, st proto.State) {
				var snap proto.Snapshot
				r.Msg.Decode(&snap)
				if r.Msg.T != proto.TypeStateSnapshot || snap.Rev != st.Rev || len(snap.Sessions) != 1 || snap.Sessions[0].ID != f.s {
					t.Fatalf("reply %s %+v", r.Msg.T, snap)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			tmp := filepath.Dir(ts.dir)
			f := &fixture{dir: ts.dir, other: mkdir(t, filepath.Join(tmp, "other")), file: filepath.Join(tmp, "file")}
			if err := os.WriteFile(f.file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			c := ts.interactive()
			f.s, f.t, f.p = ts.open(c)
			if tc.prep != nil {
				tc.prep(t, c, f)
			}
			req := tc.req(f)
			r := c.Request(req)
			switch {
			case tc.code == "" && !r.OK:
				t.Fatalf("%s answered %s: %s", req.Type(), r.Code, r.Text)
			case tc.code != "" && (r.OK || r.Code != tc.code):
				t.Fatalf("%s answered %s %s (%s), want %s", req.Type(), r.Msg.T, r.Code, r.Text, tc.code)
			case tc.code != "" && r.Text == "":
				t.Fatalf("the %s error has no text", tc.code)
			}
			if tc.check != nil {
				tc.check(t, f, r, c.State()) // the delta came before the reply (W2)
			}
		})
	}
}

func TestSessionOpenIsIdempotent(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	link := filepath.Join(filepath.Dir(ts.dir), "link")
	if err := os.Symlink(ts.dir, link); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for n, dir := range []string{ts.dir, ts.dir, ts.dir + "/", link, ts.dir + "/../work"} {
		var res proto.SessionOpenResult
		ok(t, c, proto.SessionOpen{Dir: dir}).Msg.Decode(&res)
		if res.Created != (n == 0) {
			t.Fatalf("open %d of %s: created %v", n, dir, res.Created)
		}
		ids = append(ids, res.Session)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("one directory gave the sessions %v", ids)
		}
	}
	snap := ts.stateGet(c)
	if len(snap.Sessions) != 1 || len(snap.Tabs) != 1 || len(snap.Panes) != 1 || snap.Sessions[0].Label != "work" {
		t.Fatalf("snapshot %+v", snap)
	}
}

func TestSessionOpenOfAMissingDirectoryIsNoDir(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	r := c.Request(proto.SessionOpen{Dir: filepath.Join(ts.dir, "gone"), Name: "gone"})
	if r.OK || r.Code != proto.CodeNoDir {
		t.Fatalf("answered %+v, want no-dir", r)
	}
	if snap := ts.stateGet(c); len(snap.Sessions) != 0 {
		t.Fatalf("a session was opened: %+v", snap.Sessions)
	}
	// Through the extension seam the same.
	var code string
	ts.do(func(c *core) { _, _, code = c.OpenSession(filepath.Join(ts.dir, "gone"), "") })
	if code != proto.CodeNoDir {
		t.Fatalf("Core.OpenSession answered %q", code)
	}
}

func TestSessionCloseKillsItsPanes(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, _, p := ts.open(c)
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&sp)
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn) // current: the new tab
	ok(t, c, proto.TabSelect{Session: s, Index: 1})
	ts.display(s)
	a, b := ts.panes.get(p), ts.panes.get(sp.Pane)
	if a == nil || b == nil || ts.panes.get(tn.Pane) != nil {
		t.Fatal("displaying spawned the wrong panes")
	}
	// The interactive connection displays the session (view.set is P1-16's).
	id := i.Welcome().Client
	ts.do(func(c *core) {
		for _, cn := range c.reg.list() {
			if cn.id == id {
				cn.view = s
			}
		}
	})
	from := len(i.Received())
	var res proto.SessionCloseResult
	ok(t, c, proto.SessionClose{Session: s}).Msg.Decode(&res)
	if !res.Closed {
		t.Fatal("closed is false")
	}
	testkit.Eventually(t, 5*time.Second, "both panes closed", func() bool { return a.isClosed() && b.isClosed() })
	ended := i.WaitMessage(proto.TypeViewEnded, from)
	var ve proto.ViewEnded
	ended.Decode(&ve)
	if ve.Session != s || ve.Dir != ts.dir {
		t.Fatalf("view.ended %+v", ve)
	}
	got := i.Received()[from:]
	at := -1
	for n, m := range got {
		if m.T == proto.TypeViewEnded {
			at = n
			break
		}
	}
	var d proto.Delta
	if at < 1 || got[at-1].T != proto.TypeStateDelta || got[at-1].Decode(&d) != nil || !touches(d, proto.KindSession, s) {
		t.Fatalf("want the delta that deletes the session, then view.ended: %v", typesOf(got))
	}
	ts.do(func(c *core) {
		if len(c.panes) != 0 {
			t.Errorf("the server still holds %d panes", len(c.panes))
		}
	})
	var again proto.SessionCloseResult
	ok(t, c, proto.SessionClose{Session: s}).Msg.Decode(&again)
	if again.Closed {
		t.Fatal("a second close closed something")
	}
}

func TestPaneFocusAnswersMovedFalseAtAnEdge(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	focus := func(pane, dir string) proto.PaneFocusResult {
		t.Helper()
		var res proto.PaneFocusResult
		ok(t, c, proto.PaneFocus{Pane: pane, Direction: dir}).Msg.Decode(&res)
		return res
	}
	if r := focus(p, "right"); r.Moved || r.Pane != p {
		t.Fatalf("a single pane moved: %+v", r)
	}
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&sp)
	q := sp.Pane
	for _, tc := range []struct {
		from, dir string
		moved     bool
		to        string
	}{
		{q, "right", false, q}, {q, "up", false, q}, {q, "down", false, q},
		{q, "left", true, p},
		{p, "left", false, p}, {p, "up", false, p},
		{p, "right", true, q},
	} {
		r := focus(tc.from, tc.dir)
		if r.Moved != tc.moved || r.Pane != tc.to {
			t.Fatalf("focus %s from %s: %+v, want moved %v to %s", tc.dir, tc.from, r, tc.moved, tc.to)
		}
		if got := ts.stateGet(c).Tabs; got[0].Focused != tc.to && tc.moved {
			t.Fatalf("focus %s from %s: the tab focuses %s", tc.dir, tc.from, got[0].Focused)
		}
	}
	// A down split: down from the first, up from the second.
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	ok(t, c, proto.PaneSplit{Pane: tn.Pane, Direction: proto.SplitDown}).Msg.Decode(&sp)
	if r := focus(tn.Pane, "right"); r.Moved {
		t.Fatalf("right in a down split moved: %+v", r)
	}
	if r := focus(sp.Pane, "up"); !r.Moved || r.Pane != tn.Pane {
		t.Fatalf("up from the second pane of a down split: %+v", r)
	}
	if r := focus(tn.Pane, "up"); r.Moved || r.Pane != tn.Pane {
		t.Fatalf("up from the first pane wrapped around: %+v", r)
	}
}

func TestStateGetAnswersTheSnapshot(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, _, _ := ts.open(c)
	ok(t, c, proto.TabNew{Session: s})
	ts.do(func(c *core) {}) // every delta is queued
	r := c.Request(proto.StateGet{})
	if !r.OK || r.Msg.T != proto.TypeStateSnapshot || r.Msg.ID == 0 {
		t.Fatalf("state.get answered %s (id %d)", r.Msg.T, r.Msg.ID)
	}
	var snap proto.Snapshot
	r.Msg.Decode(&snap)
	st := i.WaitState("the revision of the snapshot", func(st proto.State) bool { return st.Rev == snap.Rev })
	want := proto.NewState(snap)
	if !reflect.DeepEqual(want.Sessions, st.Sessions) || !reflect.DeepEqual(want.Tabs, st.Tabs) || !reflect.DeepEqual(want.Panes, st.Panes) {
		t.Fatalf("state.get %+v\ndiffers from the deltas %+v", want, st)
	}
	for _, m := range c.Received() {
		if m.T == proto.TypeStateDelta {
			t.Fatal("deltas followed state.get on a one-shot")
		}
	}
}

func TestAnUnknownRequestIsAnsweredUnsupportedByTheCore(t *testing.T) {
	ts := newTestServer(t)
	for _, c := range []*prototest.Client{ts.interactive(), ts.oneshot()} {
		r := c.Request(proto.Msg{T: "search.history"})
		if r.OK || r.Code != proto.CodeUnsupported {
			t.Fatalf("an unknown request was answered %+v", r)
		}
		c.Send(proto.Msg{T: "event.nobody.knows"}) // a notification is ignored
		ok(t, c, proto.StateGet{})
		if c.Closed() {
			t.Fatal("an unknown message closed the connection")
		}
	}
}
