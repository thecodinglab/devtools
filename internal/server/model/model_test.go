package model

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket, a state directory or a multiplexer reaches
// them. The model reads no variable; this is the convention of every
// package (10.1).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-model-test")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "DEVTOOLS_TEST_NO_PTY" {
			continue
		}
		if strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "XDG_") ||
			name == "TMUX" || name == "TMUX_PANE" {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// clock is the fixed time of the tests' models.
var clock = time.UnixMilli(1795000000000)

// newModel returns a model whose clock is fixed and for which every
// absolute directory exists except those under /gone.
func newModel() *State {
	s := New()
	s.now = func() time.Time { return clock }
	s.exists = func(dir string) bool { return strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, "/gone") }
	return s
}

func open(t *testing.T, s *State, dir string) string {
	t.Helper()
	id, _, _ := s.OpenSession(dir, "")
	return id
}

// splitPane splits a pane and returns the new one; an error fails the test.
func splitPane(t *testing.T, s *State, pane, direction, cwd string) string {
	t.Helper()
	id, _, err := s.Split(pane, direction, cwd)
	if err != nil {
		t.Fatalf("split %s %s: %v", pane, direction, err)
	}
	return id
}

func ok(t *testing.T, err *Error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
}

func wantCode(t *testing.T, err *Error, code string) {
	t.Helper()
	if err == nil || err.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func (s *State) tab(t *testing.T, id string) proto.Tab {
	t.Helper()
	tb, found := s.tabs[id]
	if !found {
		t.Fatalf("tab %s does not exist", id)
	}
	return copyTab(tb)
}

func (s *State) pane(t *testing.T, id string) proto.Pane {
	t.Helper()
	p, found := s.panes[id]
	if !found {
		t.Fatalf("pane %s does not exist", id)
	}
	return p.copy()
}

// effects writes effects in a short form for comparisons.
func effects(es []Effect) string {
	var parts []string
	for _, e := range es {
		switch e.Kind {
		case Spawn:
			parts = append(parts, fmt.Sprintf("spawn %s %dx%d %s", e.Pane, e.Cols, e.Rows, e.Dir))
		case Resize:
			parts = append(parts, fmt.Sprintf("resize %s %dx%d", e.Pane, e.Cols, e.Rows))
		case Kill:
			parts = append(parts, "kill "+e.Pane)
		case Retab:
			parts = append(parts, "retab "+e.Session)
		case Ended:
			parts = append(parts, "ended "+e.Session+" "+e.Dir)
		}
	}
	return strings.Join(parts, "; ")
}

func wantEffects(t *testing.T, r Result, want string) {
	t.Helper()
	if got := effects(r.Effects); got != want {
		t.Fatalf("effects\n got: %s\nwant: %s", got, want)
	}
}

// changes writes changes in a short form for comparisons.
func changes(cs []proto.Change) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, c.Op+" "+c.Kind+" "+c.ID)
	}
	return strings.Join(parts, "; ")
}

func TestOpenSessionIsIdempotentPerDirectory(t *testing.T) {
	s := newModel()
	id, created, r := s.OpenSession("/home/u/dev/x/main", "x-main")
	if !created || id != "s1" {
		t.Fatalf("first open = %s, %v; want s1, true", id, created)
	}
	if got, want := changes(r.Changes), "put pane p1; put tab t1; put session s1"; got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
	if len(r.Effects) != 0 {
		t.Fatalf("opening spawns nothing, got %s", effects(r.Effects))
	}
	x := s.sessions[id]
	if x.Label != "x-main" || x.Dir != "/home/u/dev/x/main" || x.cols != 120 || x.rows != 40 {
		t.Fatalf("session = %+v %dx%d", x.Session, x.cols, x.rows)
	}
	if p := s.pane(t, "p1"); p.State != proto.PaneDormant || p.Cwd != x.Dir {
		t.Fatalf("pane = %+v, want dormant in the session directory", p)
	}

	again, created, r := s.OpenSession("/home/u/dev/x/main/", "other")
	if again != id || created || len(r.Changes) != 0 || len(r.Effects) != 0 {
		t.Fatalf("second open = %s, %v, %+v; want %s, false, nothing", again, created, r, id)
	}
	if s.sessions[id].Label != "x-main" {
		t.Fatalf("a second open changed the label to %q", s.sessions[id].Label)
	}
	other, created, _ := s.OpenSession("/home/u/dev/y/main", "")
	if other == id || !created {
		t.Fatalf("another directory = %s, %v", other, created)
	}
	if got := s.sessions[other].Label; got != "main" {
		t.Fatalf("label without a name = %q, want the directory's name", got)
	}
	if n := len(s.Snapshot().Sessions); n != 2 {
		t.Fatalf("%d sessions, want 2", n)
	}
}

func TestIdsAreNeverReused(t *testing.T) {
	s := newModel()
	seen := map[string]bool{}
	note := func(ids ...string) {
		t.Helper()
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("id %s issued twice", id)
			}
			seen[id] = true
		}
	}
	for i := 0; i < 20; i++ {
		sid, created, _ := s.OpenSession("/d", "")
		if !created {
			t.Fatalf("round %d: the session of /d still exists", i)
		}
		x := s.sessions[sid]
		tab := x.Tabs[0]
		note(sid, tab, s.tabs[tab].Panes[0])
		tb, p, _, err := s.NewTab(sid, "")
		ok(t, err)
		note(tb, p)
		sp, _, err := s.Split(p, "right", "")
		ok(t, err)
		note(sp)
		if _, err := s.ClosePane(sp); err != nil {
			t.Fatal(err)
		}
		s.CloseSession(sid)
		if len(s.sessions)+len(s.tabs)+len(s.panes) != 0 {
			t.Fatalf("round %d: objects left after closing the session", i)
		}
	}
}

// twoOf is the tree of panes a and b in split ("" is a alone).
func twoOf(split string) proto.Node {
	if split == "" {
		return proto.Node{Pane: "a"}
	}
	return proto.Row([]string{"a", "b"}, split)
}

func TestLayoutGivesTheFirstPaneTheExtraCell(t *testing.T) {
	cases := []struct {
		cols, rows int
		split      string
		want       []Rect
	}{
		{80, 24, "", []Rect{{0, 0, 80, 24}}},
		{80, 24, "right", []Rect{{0, 0, 40, 24}, {41, 0, 39, 24}}},
		{81, 24, "right", []Rect{{0, 0, 40, 24}, {41, 0, 40, 24}}},
		{80, 24, "down", []Rect{{0, 0, 80, 12}, {0, 13, 80, 11}}},
		{80, 25, "down", []Rect{{0, 0, 80, 12}, {0, 13, 80, 12}}},
		{200, 49, "right", []Rect{{0, 0, 100, 49}, {101, 0, 99, 49}}},
	}
	for _, c := range cases {
		if got := Layout(c.cols, c.rows, twoOf(c.split)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Layout(%d, %d, %q) = %v, want %v", c.cols, c.rows, c.split, got, c.want)
		}
	}
}

func TestLayoutSharesByWeightAndNests(t *testing.T) {
	tree := proto.Node{Split: "right", Nodes: []proto.Node{
		{Pane: "a", Weight: 3},
		{Split: "down", Weight: 1, Nodes: []proto.Node{{Pane: "b"}, {Pane: "c"}, {Pane: "d"}}},
	}}
	// 81 columns: 80 to share 3:1 after the divider; 24 rows: 22 to share
	// in three after two dividers, the first two get the extra cell.
	want := []Rect{{0, 0, 60, 24}, {61, 0, 20, 8}, {61, 9, 20, 7}, {61, 17, 20, 7}}
	if got := Layout(81, 24, tree); !reflect.DeepEqual(got, want) {
		t.Fatalf("Layout = %v, want %v", got, want)
	}
}

func TestLayoutNeverGoesBelowTheMinimum(t *testing.T) {
	grid := proto.Node{Split: "right", Nodes: []proto.Node{
		{Pane: "a", Weight: 50},
		{Split: "down", Nodes: []proto.Node{{Pane: "b"}, {Pane: "c", Weight: 9}}},
		{Pane: "d"},
	}}
	for cols := 0; cols <= 30; cols++ {
		for rows := 0; rows <= 12; rows++ {
			for _, tree := range []proto.Node{twoOf(""), twoOf("right"), twoOf("down"), grid} {
				rs := Layout(cols, rows, tree)
				if len(rs) != len(tree.Leaves()) {
					t.Fatalf("Layout(%d, %d, %+v) has %d rectangles", cols, rows, tree, len(rs))
				}
				for _, r := range rs {
					if r.W < MinPaneCols || r.H < MinPaneRows {
						t.Fatalf("Layout(%d, %d, %+v) = %v: below %d by %d", cols, rows, tree, rs, MinPaneCols, MinPaneRows)
					}
				}
			}
		}
	}
	// Room enough: the weight of a gives way so that no pane is below the
	// minimum, and the panes do not overlap.
	rs := Layout(16, 6, grid)
	if rs[2].W < MinPaneCols || rs[3].W < MinPaneCols || rs[1].H < MinPaneRows {
		t.Fatalf("Layout(16, 6) = %v", rs)
	}
	if rs[0].X+rs[0].W >= rs[1].X || rs[1].X+rs[1].W >= rs[3].X {
		t.Fatalf("Layout(16, 6) = %v: panes overlap", rs)
	}
}

// rectsOf is the layout of a tab at its session's size, by pane.
func (s *State) rectsOf(t *testing.T, tid string) map[string]Rect {
	t.Helper()
	tb := s.tabs[tid]
	x := s.sessions[tb.Session]
	out := map[string]Rect{}
	for i, r := range Layout(x.cols, x.rows, tb.Layout) {
		out[tb.Panes[i]] = r
	}
	return out
}

func TestSplitNestsWithoutAPaneLimit(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	tid := s.sessions[sid].Current
	// 120 x 40: p1 | p2, then p2 split right again joins the split, then
	// p3 is split down.
	p2 := splitPane(t, s, "p1", "right", "")
	p3 := splitPane(t, s, p2, "right", "")
	p4 := splitPane(t, s, p3, "down", "")
	tb := s.tab(t, tid)
	if !reflect.DeepEqual(tb.Panes, []string{"p1", p2, p3, p4}) || tb.Split != "right" || tb.Focused != p4 {
		t.Fatalf("tab = %+v", tb)
	}
	want := proto.Node{Split: "right", Nodes: []proto.Node{
		{Pane: "p1", Weight: 2},
		{Pane: p2, Weight: 1},
		{Split: "down", Weight: 1, Nodes: []proto.Node{{Pane: p3, Weight: 1}, {Pane: p4, Weight: 1}}},
	}}
	if !reflect.DeepEqual(tb.Layout, want) {
		t.Fatalf("layout = %+v, want %+v", tb.Layout, want)
	}
	got := s.rectsOf(t, tid)
	wantRects := map[string]Rect{
		"p1": {0, 0, 60, 40}, p2: {61, 0, 29, 40},
		p3: {91, 0, 29, 20}, p4: {91, 21, 29, 19},
	}
	if !reflect.DeepEqual(got, wantRects) {
		t.Fatalf("rects = %v, want %v", got, wantRects)
	}
	// Splitting goes on as long as the panes keep their minimum.
	n := 4
	for {
		_, _, err := s.Split(s.tabs[tid].Focused, "down", "")
		if err != nil {
			wantCode(t, err, proto.CodeLimit)
			break
		}
		n++
	}
	if n < 6 {
		t.Fatalf("only %d panes before the limit", n)
	}
	if err := checkInvariants(s); err != nil {
		t.Fatal(err)
	}
}

func TestSplitRefusesWhenThePaneWouldBeTooSmall(t *testing.T) {
	cases := []struct {
		cols, rows int
		dir        string
		fits       bool
	}{
		{8, 24, "right", false}, // 4 + 1 + 3
		{9, 24, "right", true},  // 4 + 1 + 4
		{80, 4, "down", false},  // 2 + 1 + 1
		{80, 5, "down", true},   // 2 + 1 + 2
		{2, 2, "right", false},
		{2, 2, "down", false},
	}
	for _, c := range cases {
		s := newModel()
		sid := open(t, s, "/d")
		s.SetSize(sid, c.cols, c.rows)
		p := s.tabs[s.sessions[sid].Current].Panes[0]
		before := s.Snapshot()
		_, r, err := s.Split(p, c.dir, "")
		if c.fits && err != nil {
			t.Errorf("%dx%d %s: %v, want a split", c.cols, c.rows, c.dir, err)
		}
		if !c.fits {
			if err == nil || err.Code != proto.CodeLimit {
				t.Errorf("%dx%d %s: %v, want limit", c.cols, c.rows, c.dir, err)
			}
			if len(r.Changes)+len(r.Effects) != 0 || !reflect.DeepEqual(before, s.Snapshot()) {
				t.Errorf("%dx%d %s: a refused split changed the state", c.cols, c.rows, c.dir)
			}
		}
	}
	// A pane too narrow to split in a tab that is wide enough.
	s := newModel()
	sid := open(t, s, "/d")
	s.SetSize(sid, 20, 10)
	p2 := splitPane(t, s, "p1", "right", "") // 10 | 9
	_, _, err := s.Split(p2, "right", "")    // 9 = 4 + 1 + 4
	ok(t, err)
	_, _, err = s.Split(p2, "right", "") // 4
	wantCode(t, err, proto.CodeLimit)

	s = newModel()
	open(t, s, "/d")
	_, _, err = s.Split("p1", "left", "")
	wantCode(t, err, proto.CodeInvalid)
	_, _, err = s.Split("p9", "right", "")
	wantCode(t, err, proto.CodeNoPane)
}

func TestSplitFocusesTheNewPane(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	tid := s.sessions[sid].Current
	id, r, err := s.Split("p1", "down", "")
	ok(t, err)
	tb := s.tab(t, tid)
	if tb.Focused != id || tb.Split != "down" || !reflect.DeepEqual(tb.Panes, []string{"p1", id}) {
		t.Fatalf("tab = %+v, want p1 and %s below it, %s focused", tb, id, id)
	}
	if got, want := changes(r.Changes), "put pane "+id+"; put tab "+tid; got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
}

func TestSplitStartsInTheSplitPanesDirectory(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.SetPane("p1", func(p *proto.Pane) { p.Cwd = "/d/sub" })
	id := splitPane(t, s, "p1", "right", "")
	if got := s.pane(t, id).Cwd; got != "/d/sub" {
		t.Fatalf("new pane in %q, want the split pane's /d/sub", got)
	}

	_, _, _, err := s.NewTab(sid, "")
	ok(t, err)
	p := s.tabs[s.sessions[sid].Current].Panes[0]
	s.SetPane(p, func(x *proto.Pane) { x.Cwd = "/gone/away" })
	id = splitPane(t, s, p, "right", "")
	if got := s.pane(t, id).Cwd; got != "/d" {
		t.Fatalf("split of a pane whose directory is gone in %q, want the session directory", got)
	}

	_, _, _, err = s.NewTab(sid, "")
	ok(t, err)
	p = s.tabs[s.sessions[sid].Current].Panes[0]
	s.SetPane(p, func(x *proto.Pane) { x.Cwd = "" })
	id = splitPane(t, s, p, "down", "")
	if got := s.pane(t, id).Cwd; got != "/d" {
		t.Fatalf("split of a pane without a directory in %q, want the session directory", got)
	}

	_, _, _, err = s.NewTab(sid, "")
	ok(t, err)
	p = s.tabs[s.sessions[sid].Current].Panes[0]
	id = splitPane(t, s, p, "down", "/elsewhere")
	if got := s.pane(t, id).Cwd; got != "/elsewhere" {
		t.Fatalf("split with a cwd in %q, want /elsewhere", got)
	}
}

func TestNewTabStartsInTheSessionDirectory(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.SetPane("p1", func(p *proto.Pane) { p.Cwd = "/d/sub" })
	tab, p, r, err := s.NewTab(sid, "")
	ok(t, err)
	if got := s.pane(t, p).Cwd; got != "/d" {
		t.Fatalf("new tab's pane in %q, want the session directory", got)
	}
	if tb := s.tab(t, tab); tb.Index != 2 || tb.Focused != p || s.sessions[sid].Current != tab {
		t.Fatalf("tab = %+v, current %s", tb, s.sessions[sid].Current)
	}
	if got, want := changes(r.Changes), "put pane "+p+"; put tab "+tab+"; put session "+sid; got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
	_, p, _, err = s.NewTab(sid, "/other")
	ok(t, err)
	if got := s.pane(t, p).Cwd; got != "/other" {
		t.Fatalf("new tab with a cwd in %q, want /other", got)
	}
	_, _, _, err = s.NewTab("s9", "")
	wantCode(t, err, proto.CodeNoSession)
}

func TestClosingAPaneGivesTheSurvivorTheWholeTab(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		s := newModel()
		sid := open(t, s, "/d")
		s.Display(sid)
		tid := s.sessions[sid].Current
		second := splitPane(t, s, "p1", "right", "")
		gone, stays := second, "p1"
		if closeFirst {
			gone, stays = "p1", second
		}
		r, err := s.ClosePane(gone)
		ok(t, err)
		tb := s.tab(t, tid)
		if tb.Split != "" || tb.Focused != stays || !reflect.DeepEqual(tb.Panes, []string{stays}) {
			t.Fatalf("tab = %+v, want %s alone and focused", tb, stays)
		}
		wantEffects(t, r, "resize "+stays+" 120x40; kill "+gone)
		if got, want := changes(r.Changes), "put tab "+tid+"; del pane "+gone; got != want {
			t.Fatalf("changes = %s, want %s", got, want)
		}
	}
}

func TestClosingTheLastPaneClosesTheTab(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	tab, p, _, err := s.NewTab(sid, "")
	ok(t, err)
	r, err := s.ClosePane(p)
	ok(t, err)
	if _, found := s.tabs[tab]; found {
		t.Fatalf("tab %s still exists", tab)
	}
	if x := s.sessions[sid]; len(x.Tabs) != 1 || x.Current != "t1" {
		t.Fatalf("session = %+v, want only t1", x.Session)
	}
	if got, want := changes(r.Changes), "put session "+sid+"; del tab "+tab+"; del pane "+p; got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
}

func TestClosingTheLastTabEndsTheSession(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.Display(sid)
	s.PutAgent("p1", proto.Agent{Kind: "claude", Status: "working"})
	r, err := s.ClosePane("p1")
	ok(t, err)
	wantEffects(t, r, "kill p1; ended "+sid+" /d")
	if got, want := changes(r.Changes), "del agent p1; del session s1; del tab t1; del pane p1"; got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
	snap := s.Snapshot()
	if len(snap.Sessions)+len(snap.Tabs)+len(snap.Panes)+len(snap.Agents) != 0 {
		t.Fatalf("state after the last tab closed = %+v", snap)
	}
	if _, created, _ := s.OpenSession("/d", ""); !created {
		t.Fatal("the directory still has a session")
	}

	// CloseTab and CloseSession end it the same way.
	sid = open(t, s, "/e")
	r, err = s.CloseTab(s.sessions[sid].Current)
	ok(t, err)
	wantEffects(t, r, "ended "+sid+" /e")
	sid = open(t, s, "/f")
	s.NewTab(sid, "")
	r = s.CloseSession(sid)
	wantEffects(t, r, "ended "+sid+" /f")
	if r := s.CloseSession(sid); len(r.Changes)+len(r.Effects) != 0 {
		t.Fatalf("closing a closed session returned %+v", r)
	}
}

func TestClosingTheCurrentTabSelectsItsLeftNeighbour(t *testing.T) {
	setup := func() (*State, string, []string) {
		s := newModel()
		sid := open(t, s, "/d")
		s.NewTab(sid, "")
		s.NewTab(sid, "")
		return s, sid, append([]string(nil), s.sessions[sid].Tabs...)
	}
	// The current middle tab: its left neighbour.
	s, sid, tabs := setup()
	_, _, err := s.SelectTab(sid, tabs[1], 0, 0)
	ok(t, err)
	r, err := s.CloseTab(tabs[1])
	ok(t, err)
	if cur := s.sessions[sid].Current; cur != tabs[0] {
		t.Fatalf("current = %s, want %s", cur, tabs[0])
	}
	wantEffects(t, r, "retab "+sid)

	// The current first tab: the new first tab.
	s, sid, tabs = setup()
	s.SelectTab(sid, tabs[0], 0, 0)
	s.CloseTab(tabs[0])
	if cur := s.sessions[sid].Current; cur != tabs[1] {
		t.Fatalf("current = %s, want %s", cur, tabs[1])
	}

	// A tab that is not current: the current one stays.
	s, sid, tabs = setup()
	r, _ = s.CloseTab(tabs[0])
	if cur := s.sessions[sid].Current; cur != tabs[2] {
		t.Fatalf("current = %s, want %s", cur, tabs[2])
	}
	if len(r.Effects) != 0 {
		t.Fatalf("closing a dormant tab that is not current: %s", effects(r.Effects))
	}
	_, err = s.CloseTab("t99")
	wantCode(t, err, proto.CodeNoTab)
}

func TestTabIndexesAreRenumbered(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	for i := 0; i < 3; i++ {
		s.NewTab(sid, "")
	}
	tabs := append([]string(nil), s.sessions[sid].Tabs...)
	r, err := s.CloseTab(tabs[1])
	ok(t, err)
	for i, tid := range s.sessions[sid].Tabs {
		if got := s.tabs[tid].Index; got != i+1 {
			t.Fatalf("tab %s has index %d, want %d", tid, got, i+1)
		}
	}
	if !reflect.DeepEqual(s.sessions[sid].Tabs, []string{tabs[0], tabs[2], tabs[3]}) {
		t.Fatalf("tabs = %v", s.sessions[sid].Tabs)
	}
	want := fmt.Sprintf("put tab %s; put tab %s; put session %s; del tab %s; del pane %s",
		tabs[2], tabs[3], sid, tabs[1], "p2")
	if got := changes(r.Changes); got != want {
		t.Fatalf("changes = %s, want %s", got, want)
	}
}

func TestSelectTabByStepWrapsAround(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.NewTab(sid, "")
	s.NewTab(sid, "")
	tabs := s.sessions[sid].Tabs
	steps := []struct {
		step int
		want string
	}{{1, tabs[0]}, {1, tabs[1]}, {-1, tabs[0]}, {-1, tabs[2]}, {4, tabs[0]}}
	for _, st := range steps {
		id, _, err := s.SelectTab(sid, "", 0, st.step)
		ok(t, err)
		if id != st.want || s.sessions[sid].Current != st.want {
			t.Fatalf("step %d = %s, want %s", st.step, id, st.want)
		}
	}
	id, r, err := s.SelectTab(sid, "", 1, 0)
	ok(t, err)
	if id != tabs[0] || len(r.Changes) != 0 || len(r.Effects) != 0 {
		t.Fatalf("selecting the current tab: %s, %+v", id, r)
	}
	id, _, err = s.SelectTab(sid, "", 2, 0)
	if err != nil || id != tabs[1] {
		t.Fatalf("index 2 = %s, %v", id, err)
	}
	_, _, err = s.SelectTab(sid, "", 4, 0)
	wantCode(t, err, proto.CodeNoTab)
	other := open(t, s, "/e")
	_, _, err = s.SelectTab(sid, s.sessions[other].Current, 0, 0)
	wantCode(t, err, proto.CodeNoTab)
	_, _, err = s.SelectTab("s99", "", 0, 1)
	wantCode(t, err, proto.CodeNoSession)
	_, _, err = s.SelectTab(sid, "", 0, 0)
	wantCode(t, err, proto.CodeInvalid)
}

func TestFocusDirectionHasNoWraparound(t *testing.T) {
	for _, split := range []string{"right", "down"} {
		s := newModel()
		sid := open(t, s, "/d")
		tid := s.sessions[sid].Current
		if target, moved, _, err := s.FocusDir("p1", "right"); err != nil || moved || target != "p1" {
			t.Fatalf("one pane: %s, %v, %v", target, moved, err)
		}
		second := splitPane(t, s, "p1", split, "")
		forward, back := "right", "left"
		others := []string{"up", "down"}
		if split == "down" {
			forward, back = "down", "up"
			others = []string{"left", "right"}
		}
		cases := []struct {
			from, dir, target string
			moved             bool
		}{
			{"p1", forward, second, true},
			{second, forward, second, false},
			{second, back, "p1", true},
			{"p1", back, "p1", false},
			{"p1", others[0], "p1", false},
			{"p1", others[1], "p1", false},
			{second, others[0], second, false},
			{second, others[1], second, false},
		}
		for _, c := range cases {
			s.Focus(c.from)
			target, moved, r, err := s.FocusDir(c.from, c.dir)
			ok(t, err)
			if target != c.target || moved != c.moved {
				t.Fatalf("%s split, from %s %s = %s, %v; want %s, %v", split, c.from, c.dir, target, moved, c.target, c.moved)
			}
			if got := s.tabs[tid].Focused; got != c.target {
				t.Fatalf("%s split, from %s %s: focused %s", split, c.from, c.dir, got)
			}
			if !c.moved && len(r.Changes) != 0 {
				t.Fatalf("a move that did not happen changed %s", changes(r.Changes))
			}
		}
		_, _, _, err := s.FocusDir("p1", "north")
		wantCode(t, err, proto.CodeInvalid)
		_, _, _, err = s.FocusDir("p99", "up")
		wantCode(t, err, proto.CodeNoPane)
		_, err = s.Focus("p99")
		wantCode(t, err, proto.CodeNoPane)
	}
}

func TestDormantPanesSpawnOnlyWhenDisplayed(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	_, p2, r, _ := s.NewTab(sid, "")
	if len(r.Effects) != 1 || r.Effects[0].Kind != Retab {
		t.Fatalf("new tab of a session never displayed: %s", effects(r.Effects))
	}
	_, r, _ = s.Split(p2, "right", "/d/x")
	if len(r.Effects) != 0 {
		t.Fatalf("split of a session never displayed: %s", effects(r.Effects))
	}
	for id, p := range s.panes {
		if p.State != proto.PaneDormant {
			t.Fatalf("pane %s is %s before the session is displayed", id, p.State)
		}
	}
	r = s.Display(sid)
	wantEffects(t, r, "spawn p2 60x40 /d; spawn p3 59x40 /d/x")
	if got := changes(r.Changes); got != "put pane p2; put pane p3; put session "+sid {
		t.Fatalf("changes = %s", got)
	}
	if s.sessions[sid].Shown != clock.UnixMilli() {
		t.Fatalf("shown = %d", s.sessions[sid].Shown)
	}
	if s.pane(t, "p1").State != proto.PaneDormant {
		t.Fatal("the pane of a tab that is not current was spawned")
	}
	_, r, _ = s.SelectTab(sid, "", 1, 0)
	wantEffects(t, r, "spawn p1 120x40 /d; retab "+sid)
	_, p4, r, _ := s.NewTab(sid, "")
	wantEffects(t, r, "spawn "+p4+" 120x40 /d; retab "+sid)
	if r := s.Display(sid); len(r.Changes)+len(r.Effects) != 0 {
		t.Fatalf("displaying again: %+v", r)
	}
}

func TestATabIsResizedWhenItBecomesCurrent(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.Display(sid)
	first := s.sessions[sid].Current
	s.NewTab(sid, "")
	s.SetSize(sid, 100, 30)
	_, r, err := s.SelectTab(sid, first, 0, 0)
	ok(t, err)
	wantEffects(t, r, "resize p1 100x30; retab "+sid)
	_, r, _ = s.SelectTab(sid, "", 0, 1)
	if len(r.Effects) != 1 || r.Effects[0].Kind != Retab {
		t.Fatalf("a tab already at the size was resized: %s", effects(r.Effects))
	}
}

func TestSetSizeResizesOnlyTheCurrentTab(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.Display(sid)
	_, p2, _, _ := s.NewTab(sid, "")
	p3 := splitPane(t, s, p2, "right", "")
	r := s.SetSize(sid, 81, 30)
	wantEffects(t, r, "resize "+p2+" 40x30; resize "+p3+" 40x30")
	if len(r.Changes) != 0 {
		t.Fatalf("a size change is not on the wire, got %s", changes(r.Changes))
	}
	if r := s.SetSize(sid, 81, 30); len(r.Effects) != 0 {
		t.Fatalf("the same size again: %s", effects(r.Effects))
	}
	r = s.SetSize(sid, 5000, 0)
	wantEffects(t, r, "resize "+p2+" 500x2; resize "+p3+" 499x2")
	if c, rows, _ := s.Size(sid); c != 1000 || rows != 2 {
		t.Fatalf("size = %dx%d, want it clamped to 1000x2", c, rows)
	}
	if r := s.SetSize("s99", 80, 24); len(r.Effects)+len(r.Changes) != 0 {
		t.Fatalf("size of a missing session: %+v", r)
	}
}

func TestEffectsComeInTheOrderTheServerMustApplyThem(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.Display(sid)
	first := s.sessions[sid].Current

	// A split: the new pane is spawned before the old one shrinks.
	_, r, _ := s.Split("p1", "right", "")
	wantEffects(t, r, "spawn p2 59x40 /d; resize p1 60x40")
	s.ClosePane("p2")

	// Closing the current tab: spawn and resize of the left neighbour's
	// panes, then the kill, then the frames of the viewers.
	t2, p3, _, _ := s.NewTab(sid, "")
	s.SetSize(sid, 100, 30)
	p4 := splitPane(t, s, "p1", "down", "") // dormant: its tab is not current
	r, _ = s.CloseTab(t2)
	wantEffects(t, r, fmt.Sprintf("spawn %s 100x14 /d; resize p1 100x15; kill %s; retab %s", p4, p3, sid))
	if s.sessions[sid].Current != first {
		t.Fatal("the left neighbour is not current")
	}

	// Closing the last tab: the kills, then the end.
	r = s.CloseSession(sid)
	wantEffects(t, r, fmt.Sprintf("kill p1; kill %s; ended %s /d", p4, sid))
}

func TestSetPaneKeepsTheIdentityAndRestartsAnExitedPane(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	s.Display(sid)
	r := s.SetPane("p1", func(p *proto.Pane) {
		p.ID, p.Tab, p.Session = "p9", "t9", "s9"
		p.Title = "vim\x1b[2J main.go"
	})
	if got := s.pane(t, "p1"); got.ID != "p1" || got.Tab != "t1" || got.Session != sid || got.Title != "vim[2J main.go" {
		t.Fatalf("pane = %+v", got)
	}
	if got := changes(r.Changes); got != "put pane p1" {
		t.Fatalf("changes = %s", got)
	}
	r = s.SetPane("p1", func(p *proto.Pane) {
		p.State = proto.PaneExited
		p.Exit = &proto.PaneExit{Code: 0, Signal: "SIGKILL", Text: "killed"}
	})
	if len(r.Effects) != 0 || s.pane(t, "p1").State != proto.PaneExited {
		t.Fatalf("exit: %+v", r)
	}
	if r := s.SetPane("p1", func(p *proto.Pane) { p.State = proto.PaneDormant }); len(r.Changes) != 0 {
		t.Fatalf("an exited pane became dormant: %s", changes(r.Changes))
	}
	r = s.SetPane("p1", func(p *proto.Pane) { p.State = proto.PaneRunning })
	wantEffects(t, r, "spawn p1 120x40 /d")
	if got := s.pane(t, "p1"); got.State != proto.PaneRunning || got.Exit != nil {
		t.Fatalf("restarted pane = %+v", got)
	}
	if r := s.SetPane("p1", func(*proto.Pane) {}); len(r.Changes)+len(r.Effects) != 0 {
		t.Fatalf("no change: %+v", r)
	}
}

func TestAgentsFollowTheirPane(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/d")
	r := s.PutAgent("p1", proto.Agent{Pane: "p7", Session: "s7", Kind: "gemini", Status: "working", Title: "fix", Since: 5})
	if got := changes(r.Changes); got != "put agent p1" {
		t.Fatalf("changes = %s", got)
	}
	a := s.agents["p1"]
	if a.Pane != "p1" || a.Tab != "t1" || a.Session != sid || a.Kind != proto.Unknown || a.Status != proto.StatusWorking {
		t.Fatalf("agent = %+v", a)
	}
	if r := s.PutAgent("p1", a); len(r.Changes) != 0 {
		t.Fatalf("the same record again: %s", changes(r.Changes))
	}
	if r := s.PutAgent("p99", a); len(r.Changes) != 0 {
		t.Fatalf("an agent of a missing pane: %s", changes(r.Changes))
	}
	if got := changes(s.DelAgent("p1").Changes); got != "del agent p1" {
		t.Fatalf("changes = %s", got)
	}
	if r := s.DelAgent("p1"); len(r.Changes) != 0 {
		t.Fatalf("deleting twice: %s", changes(r.Changes))
	}
}

// grid opens a session at 121 x 41 with p1 left, and p2 above p3 right of
// it: p1 | (p2 / p3).
func grid(t *testing.T) (*State, string, string) {
	t.Helper()
	s := newModel()
	sid := open(t, s, "/d")
	s.SetSize(sid, 121, 41)
	s.Display(sid)
	p2 := splitPane(t, s, "p1", "right", "")
	splitPane(t, s, p2, "down", "")
	return s, sid, s.sessions[sid].Current
}

func TestResizeMovesTheClosestBorder(t *testing.T) {
	s, _, tid := grid(t)
	// p1 | p2, p3 at 121 x 41: p1 60 wide, p2 and p3 60 wide, 20 high each.
	if got := s.rectsOf(t, tid); got["p1"] != (Rect{0, 0, 60, 41}) || got["p2"] != (Rect{61, 0, 60, 20}) || got["p3"] != (Rect{61, 21, 60, 20}) {
		t.Fatalf("rects = %v", got)
	}
	steps := []struct {
		pane, dir string
		cells     int
		moved     bool
		p1W, p2H  int
	}{
		{"p1", "right", 5, true, 65, 20},    // p1's own border: it grows
		{"p1", "left", 10, true, 55, 20},    // no border left of p1: its right one moves left
		{"p2", "left", 3, true, 52, 20},     // p2 grows into p1
		{"p3", "up", 4, true, 52, 16},       // p3 grows into p2
		{"p2", "up", 2, true, 52, 14},       // no border above p2: the one below it moves up
		{"p1", "up", 1, false, 52, 14},      // no border across p1
		{"p2", "up", 100, true, 52, 2},      // only to p2's minimum
		{"p2", "up", 1, false, 52, 2},       // at the minimum
		{"p1", "left", 1000, true, 4, 2},    // only to p1's minimum
		{"p3", "right", 1000, true, 116, 2}, // to the minimum of p2 and p3 together
	}
	for _, st := range steps {
		before := s.tab(t, tid)
		moved, r, err := s.Resize(st.pane, st.dir, st.cells)
		ok(t, err)
		got := s.rectsOf(t, tid)
		if moved != st.moved || got["p1"].W != st.p1W || got["p2"].H != st.p2H {
			t.Fatalf("resize %s %s %d = %v: rects %v; want moved %v, p1 %d wide, p2 %d high", st.pane, st.dir, st.cells, moved, got, st.moved, st.p1W, st.p2H)
		}
		if !moved && (len(r.Changes)+len(r.Effects) != 0 || !reflect.DeepEqual(before, s.tab(t, tid))) {
			t.Fatalf("resize %s %s that did not move changed %s", st.pane, st.dir, changes(r.Changes))
		}
		if err := checkInvariants(s); err != nil {
			t.Fatal(err)
		}
	}
	// The running panes follow their rectangles.
	_, r, err := s.Resize("p1", "left", 10)
	ok(t, err)
	wantEffects(t, r, "resize p1 106x41; resize p2 14x2; resize p3 14x38")

	_, _, err = s.Resize("p1", "north", 1)
	wantCode(t, err, proto.CodeInvalid)
	_, _, err = s.Resize("p1", "left", 0)
	wantCode(t, err, proto.CodeInvalid)
	_, _, err = s.Resize("p99", "left", 1)
	wantCode(t, err, proto.CodeNoPane)
}

func TestResizeKeepsProportionsWhenTheWindowChanges(t *testing.T) {
	s, sid, tid := grid(t)
	s.Resize("p1", "right", 20) // p1 80 of 120 cells
	s.SetSize(sid, 61, 41)
	if got := s.rectsOf(t, tid)["p1"].W; got != 40 {
		t.Fatalf("p1 is %d wide at 61 columns, want 40 (two thirds)", got)
	}
}

func TestFocusMovesToTheNeighbourInTheGrid(t *testing.T) {
	s, _, tid := grid(t)
	cases := []struct {
		from, dir, target string
		moved             bool
	}{
		{"p1", "right", "p2", true}, // p2 and p3 share as many rows with p1: the top one
		{"p2", "left", "p1", true},
		{"p3", "left", "p1", true},
		{"p2", "down", "p3", true},
		{"p3", "up", "p2", true},
		{"p3", "right", "p3", false},
		{"p1", "up", "p1", false},
	}
	for _, c := range cases {
		s.Focus(c.from)
		target, moved, _, err := s.FocusDir(c.from, c.dir)
		ok(t, err)
		if target != c.target || moved != c.moved || s.tabs[tid].Focused != c.target {
			t.Fatalf("from %s %s = %s, %v; want %s, %v", c.from, c.dir, target, moved, c.target, c.moved)
		}
	}
	// p3 grown upward shares more rows with p1 than p2 does.
	s.Resize("p3", "up", 10)
	if target, _, _, _ := s.FocusDir("p1", "right"); target != "p3" {
		t.Fatalf("from p1 right = %s, want p3, the larger neighbour", target)
	}
}

func TestClosingANestedPaneGivesItsSpaceToItsNeighbour(t *testing.T) {
	s, _, tid := grid(t)
	// Closing p3 leaves p1 | p2: the split of p2 and p3 goes.
	r, err := s.ClosePane("p3")
	ok(t, err)
	tb := s.tab(t, tid)
	want := proto.Node{Split: "right", Nodes: []proto.Node{{Pane: "p1", Weight: 1}, {Pane: "p2", Weight: 1}}}
	if !reflect.DeepEqual(tb.Layout, want) || tb.Focused != "p2" || !reflect.DeepEqual(tb.Panes, []string{"p1", "p2"}) {
		t.Fatalf("tab = %+v", tb)
	}
	wantEffects(t, r, "resize p2 60x41; kill p3")

	// p1 | p2 | p4 with p1 focused: closing p2 gives its space to p1, and
	// the focus stays.
	p4 := splitPane(t, s, "p2", "right", "")
	s.Focus("p1")
	_, err = s.ClosePane("p2")
	ok(t, err)
	tb = s.tab(t, tid)
	if tb.Focused != "p1" || s.rectsOf(t, tid)["p1"].W != 90 {
		t.Fatalf("tab = %+v, rects %v", tb, s.rectsOf(t, tid))
	}

	// A split that keeps one node joins the split above it when they have
	// the same direction: p1 | (p5 / (p4 | p6)), closing p5.
	p5 := splitPane(t, s, p4, "down", "")
	p6 := splitPane(t, s, p4, "right", "")
	_, err = s.ClosePane(p5)
	ok(t, err)
	tb = s.tab(t, tid)
	if tb.Layout.Split != "right" || len(tb.Layout.Nodes) != 3 || !reflect.DeepEqual(tb.Panes, []string{"p1", p4, p6}) {
		t.Fatalf("layout = %+v", tb.Layout)
	}
	rs := s.rectsOf(t, tid)
	if rs["p1"].W != 90 || rs[p4].W+rs[p6].W+1 != 30 {
		t.Fatalf("rects = %v", rs)
	}
	if err := checkInvariants(s); err != nil {
		t.Fatal(err)
	}
}
