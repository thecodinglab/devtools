package integration

// Level 3: persistence (3.9). A restart brings the structure and the
// directories back, a killed server restores from its last save, and
// restored panes start only when a client displays their tab.

import (
	"slices"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit"
)

// layout is what a restart must bring back of a session: its tabs in order,
// their splits and focused panes, and the panes with their directories.
type layout struct {
	session, dir, label, current string
	tabs                         []proto.Tab
	cwd                          map[string]string
}

func layoutOf(t *testing.T, st proto.State, dir string) layout {
	t.Helper()
	se, ok := sessionOf(st, dir)
	if !ok {
		t.Fatalf("no session of %s in %+v", dir, st)
	}
	l := layout{session: se.ID, dir: se.Dir, label: se.Label, current: se.Current, cwd: map[string]string{}}
	for _, id := range se.Tabs {
		tab := st.Tabs[id]
		l.tabs = append(l.tabs, tab)
		for _, p := range tab.Panes {
			l.cwd[p] = st.Panes[p].Cwd
		}
	}
	return l
}

func sameLayout(t *testing.T, got, want layout) {
	t.Helper()
	if got.session != want.session || got.dir != want.dir || got.label != want.label || got.current != want.current {
		t.Fatalf("the session came back as %s %s %q current %s, want %s %s %q current %s",
			got.session, got.dir, got.label, got.current, want.session, want.dir, want.label, want.current)
	}
	if len(got.tabs) != len(want.tabs) {
		t.Fatalf("%d tabs came back, want %d", len(got.tabs), len(want.tabs))
	}
	for i := range want.tabs {
		g, w := got.tabs[i], want.tabs[i]
		if g.ID != w.ID || g.Index != w.Index || g.Split != w.Split || g.Focused != w.Focused || !slices.Equal(g.Panes, w.Panes) {
			t.Fatalf("tab %d came back as %+v, want %+v", i, g, w)
		}
	}
	for p, cwd := range want.cwd {
		if got.cwd[p] != cwd {
			t.Fatalf("pane %s came back in %q, want %q", p, got.cwd[p], cwd)
		}
	}
}

// buildLayout makes, in the session of dir, a first tab split right whose
// second pane starts in sub, and a second tab in other; the first tab is
// current again at the end. It returns the layout as the client sees it.
func buildLayout(t *testing.T, c *prototest.Client, dir, sub, other string) layout {
	t.Helper()
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 100, Rows: 30})
	r := c.Request(proto.PaneSplit{Pane: v.Panes[0], Direction: proto.SplitRight, Cwd: sub})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	r = c.Request(proto.TabNew{Session: v.Session, Cwd: other})
	if !r.OK {
		t.Fatalf("tab.new: %s: %s", r.Code, r.Text)
	}
	r = c.Request(proto.TabSelect{Session: v.Session, Index: 1})
	if !r.OK {
		t.Fatalf("tab.select: %s: %s", r.Code, r.Text)
	}
	l := layoutOf(t, c.State(), dir)
	if len(l.tabs) != 2 || l.current != l.tabs[0].ID || l.cwd[l.tabs[1].Panes[0]] != other {
		t.Fatalf("the layout before the restart: %+v", l)
	}
	return l
}

func TestARestartRestoresStructureAndDirectories(t *testing.T) {
	home := testkit.ShortTempDir(t)
	dir, sub, other := work(t, home), work(t, home, "sub"), work(t, home, "other")
	var before layout
	var seen []string
	t.Run("before", func(t *testing.T) {
		socket := servertest.Start(t, home, servertest.WithHelper("envwait"))
		c := prototest.Dial(t, socket, prototest.Interactive())
		before = buildLayout(t, c, dir, sub, other)
		for p := range before.cwd {
			seen = append(seen, p)
		}
		// The server stops at the end of the subtest: Shutdown saves.
	})
	if t.Failed() {
		return
	}
	socket := servertest.Start(t, home, servertest.WithHelper("envwait"))
	c := prototest.Dial(t, socket, prototest.Interactive())
	after := layoutOf(t, c.State(), dir)
	sameLayout(t, after, before)

	// The panes start in the directories they had.
	v := c.View(proto.ViewSet{Session: before.session, Cols: 100, Rows: 30})
	if v.Tab != before.tabs[0].ID {
		t.Fatalf("the view shows %s, want the current tab %s", v.Tab, before.tabs[0].ID)
	}
	first := before.tabs[0].Panes
	waitWd(t, c, first[0], dir)
	waitWd(t, c, first[1], sub)
	if r := c.Request(proto.TabSelect{Session: before.session, Index: 2}); !r.OK {
		t.Fatalf("tab.select: %s: %s", r.Code, r.Text)
	}
	waitWd(t, c, before.tabs[1].Panes[0], other)

	// Ids are never reused, across restarts too (3.3).
	r := c.Request(proto.TabNew{Session: before.session})
	if !r.OK {
		t.Fatalf("tab.new: %s: %s", r.Code, r.Text)
	}
	var tn proto.TabNewResult
	r.Msg.Decode(&tn)
	if slices.Contains(seen, tn.Pane) {
		t.Fatalf("the new pane reuses the id %s", tn.Pane)
	}
	for _, tab := range before.tabs {
		if tab.ID == tn.Tab {
			t.Fatalf("the new tab reuses the id %s", tn.Tab)
		}
	}
}

func TestRestoredPanesStartOnlyWhenDisplayed(t *testing.T) {
	home := testkit.ShortTempDir(t)
	dir, sub, other := work(t, home), work(t, home, "sub"), work(t, home, "other")
	var before layout
	t.Run("before", func(t *testing.T) {
		socket := servertest.Start(t, home, servertest.WithHelper("envwait"))
		before = buildLayout(t, prototest.Dial(t, socket, prototest.Interactive()), dir, sub, other)
	})
	if t.Failed() {
		return
	}
	socket := servertest.Start(t, home, servertest.WithHelper("envwait"))

	// A one-shot asks for the state: every restored pane is dormant, so
	// restoring started no program.
	o := prototest.Dial(t, socket, prototest.Oneshot(""))
	r := o.Request(proto.StateGet{})
	var snap proto.Snapshot
	if err := r.Msg.Decode(&snap); err != nil || !r.OK {
		t.Fatalf("state.get: %+v %v", r, err)
	}
	if len(snap.Panes) != 3 {
		t.Fatalf("%d panes came back, want 3", len(snap.Panes))
	}
	for _, p := range snap.Panes {
		if p.State != proto.PaneDormant {
			t.Fatalf("pane %s came back %s, not dormant", p.ID, p.State)
		}
	}

	// Displaying the session spawns the panes of its current tab only.
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Session: before.session, Cols: 100, Rows: 30})
	if !slices.Equal(v.Panes, before.tabs[0].Panes) {
		t.Fatalf("view.ok shows %v, want %v", v.Panes, before.tabs[0].Panes)
	}
	st := c.State()
	for _, p := range before.tabs[0].Panes {
		if st.Panes[p].State != proto.PaneRunning {
			t.Fatalf("pane %s of the displayed tab is %s after view.ok", p, st.Panes[p].State)
		}
	}
	hidden := before.tabs[1].Panes[0]
	if st.Panes[hidden].State != proto.PaneDormant {
		t.Fatalf("pane %s of the other tab is %s", hidden, st.Panes[hidden].State)
	}
	waitWd(t, c, before.tabs[0].Panes[0], dir)

	// Selecting the other tab starts its pane, at the size of the view.
	if r := c.Request(proto.TabSelect{Session: before.session, Index: 2}); !r.OK {
		t.Fatalf("tab.select: %s: %s", r.Code, r.Text)
	}
	if st := c.State(); st.Panes[hidden].State != proto.PaneRunning {
		t.Fatalf("pane %s is %s after its tab was selected", hidden, st.Panes[hidden].State)
	}
	s := c.WaitFor(hidden, "end")
	if s.Cols != 100 || s.Rows != 30 {
		t.Fatalf("the pane started at %dx%d", s.Cols, s.Rows)
	}
}

func TestAKilledServerRestoresFromItsLastSave(t *testing.T) {
	home := testkit.ShortTempDir(t)
	dir, sub, other := work(t, home), work(t, home, "sub"), work(t, home, "other")
	b := startBoot(t, home, "hex")
	c := prototest.Dial(t, b.socket(), prototest.Interactive())
	before := buildLayout(t, c, dir, sub, other)

	// The saver writes within SaveDelay (5 s) of the last structural
	// change; the test waits for the file to hold the layout.
	testkit.Eventually(t, 15*time.Second, "state.json holds both tabs", func() bool {
		st, ok := savedState(b.stateDir())
		if !ok || len(st.Sessions) != 1 {
			return false
		}
		se := st.Sessions[0]
		return len(se.Tabs) == 2 && se.Current == before.tabs[0].ID && len(se.Tabs[0].Panes) == 2
	})

	// SIGKILL: none of the stop steps run, the socket and the lock stay
	// behind as a crash leaves them.
	b.kill()
	c.WaitClosed()

	// The next start clears the stale socket (step 5), takes the lock and
	// restores the layout of the last save, every pane dormant.
	b2 := startBoot(t, home, "hex")
	c2 := prototest.Dial(t, b2.socket(), prototest.Interactive())
	if c2.Welcome().Boot == c.Welcome().Boot {
		t.Fatal("the restarted server has the boot id of the killed one")
	}
	st := c2.State()
	sameLayout(t, layoutOf(t, st, dir), before)
	for id, p := range st.Panes {
		if p.State != proto.PaneDormant {
			t.Fatalf("pane %s came back %s", id, p.State)
		}
	}
	v := c2.View(proto.ViewSet{Session: before.session, Cols: 100, Rows: 30})
	c2.WaitFor(v.Panes[0], "ready")
	if code := b2.stop(); code != 0 {
		t.Fatalf("the restarted server exited %d", code)
	}
}
