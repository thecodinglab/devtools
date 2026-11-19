package panes

import (
	"strings"
	"testing"

	"devtools/internal/client/screen"
	"devtools/internal/proto"
)

// twoPanes is a set showing tab t1 with p1 and p2 split as given, after a
// full frame of each with the given sizes.
func twoPanes(t *testing.T, split string, c1, r1, c2, r2 int) *Set {
	t.Helper()
	var s Set
	s.Reset(proto.ViewOK{Session: "s1", Tab: "t1", Panes: []string{"p1", "p2"}})
	s.Retab(proto.Tab{ID: "t1", Session: "s1", Index: 1, Panes: []string{"p1", "p2"}, Split: split, Focused: "p1"})
	if err := s.Apply(full("p1", 1, c1, r1, strings.Repeat("a", c1))); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(full("p2", 1, c2, r2, strings.Repeat("b", c2))); err != nil {
		t.Fatal(err)
	}
	return &s
}

func onePane(t *testing.T, cols, rows int, lines ...string) *Set {
	t.Helper()
	var s Set
	s.Reset(proto.ViewOK{Session: "s1", Tab: "t1", Panes: []string{"p1"}})
	if err := s.Apply(full("p1", 1, cols, rows, lines...)); err != nil {
		t.Fatal(err)
	}
	return &s
}

func wantRect(t *testing.T, what string, got, want screen.Rect) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %+v, want %+v", what, got, want)
	}
}

// partition checks that every cell of area is exactly one of a pane, the
// divider or the pad, and returns the number of pad cells.
func partition(t *testing.T, l Layout, area screen.Rect) int {
	t.Helper()
	pad := 0
	for y := area.Y; y < area.Y+area.H; y++ {
		for x := area.X; x < area.X+area.W; x++ {
			n := 0
			for _, r := range l.Panes {
				if r.Contains(x, y) {
					n++
				}
			}
			if l.Divider.Contains(x, y) {
				n++
			}
			for _, r := range l.Pad {
				if r.Contains(x, y) {
					n++
					pad++
				}
			}
			if n != 1 {
				t.Fatalf("cell %d,%d is covered %d times in %+v", x, y, n, l)
			}
		}
	}
	for _, r := range append(append([]screen.Rect{l.Divider}, l.Pad...), values(l.Panes)...) {
		if r.W == 0 && r.H == 0 {
			continue
		}
		if r.X < area.X || r.Y < area.Y || r.X+r.W > area.X+area.W || r.Y+r.H > area.Y+area.H {
			t.Fatalf("rectangle %+v reaches outside the area %+v", r, area)
		}
	}
	return pad
}

func values(m map[string]screen.Rect) []screen.Rect {
	var out []screen.Rect
	for _, r := range m {
		out = append(out, r)
	}
	return out
}

func TestLayoutPlacesTheSecondPaneAfterTheDivider(t *testing.T) {
	t.Run("right", func(t *testing.T) {
		s := twoPanes(t, proto.SplitRight, 9, 5, 10, 5)
		area := screen.Rect{X: 0, Y: 0, W: 20, H: 5}
		l := s.Layout(area)
		wantRect(t, "p1", l.Panes["p1"], screen.Rect{X: 0, Y: 0, W: 9, H: 5})
		wantRect(t, "divider", l.Divider, screen.Rect{X: 9, Y: 0, W: 1, H: 5})
		wantRect(t, "p2", l.Panes["p2"], screen.Rect{X: 10, Y: 0, W: 10, H: 5})
		if n := partition(t, l, area); n != 0 {
			t.Fatalf("%d pad cells, want none", n)
		}
	})
	t.Run("down", func(t *testing.T) {
		s := twoPanes(t, proto.SplitDown, 20, 2, 20, 2)
		area := screen.Rect{X: 0, Y: 0, W: 20, H: 5}
		l := s.Layout(area)
		wantRect(t, "p1", l.Panes["p1"], screen.Rect{X: 0, Y: 0, W: 20, H: 2})
		wantRect(t, "divider", l.Divider, screen.Rect{X: 0, Y: 2, W: 20, H: 1})
		wantRect(t, "p2", l.Panes["p2"], screen.Rect{X: 0, Y: 3, W: 20, H: 2})
		if n := partition(t, l, area); n != 0 {
			t.Fatalf("%d pad cells, want none", n)
		}
	})
	t.Run("an area away from the origin", func(t *testing.T) {
		s := twoPanes(t, proto.SplitRight, 4, 3, 5, 3)
		area := screen.Rect{X: 2, Y: 1, W: 10, H: 3}
		l := s.Layout(area)
		wantRect(t, "p1", l.Panes["p1"], screen.Rect{X: 2, Y: 1, W: 4, H: 3})
		wantRect(t, "divider", l.Divider, screen.Rect{X: 6, Y: 1, W: 1, H: 3})
		wantRect(t, "p2", l.Panes["p2"], screen.Rect{X: 7, Y: 1, W: 5, H: 3})
		partition(t, l, area)
	})
	t.Run("one pane has no divider", func(t *testing.T) {
		s := onePane(t, 20, 5, "x")
		l := s.Layout(screen.Rect{W: 20, H: 5})
		wantRect(t, "p1", l.Panes["p1"], screen.Rect{W: 20, H: 5})
		if l.Divider.W != 0 || l.Divider.H != 0 {
			t.Fatalf("divider %+v with one pane", l.Divider)
		}
	})
}

func TestLayoutClipsAtTheAreaEdge(t *testing.T) {
	s := twoPanes(t, proto.SplitRight, 15, 10, 14, 10)
	area := screen.Rect{X: 0, Y: 0, W: 20, H: 6}
	l := s.Layout(area)
	wantRect(t, "p1", l.Panes["p1"], screen.Rect{X: 0, Y: 0, W: 15, H: 6})
	wantRect(t, "divider", l.Divider, screen.Rect{X: 15, Y: 0, W: 1, H: 6})
	wantRect(t, "p2", l.Panes["p2"], screen.Rect{X: 16, Y: 0, W: 4, H: 6})
	if n := partition(t, l, area); n != 0 {
		t.Fatalf("%d pad cells in a clipped view", n)
	}

	// The second pane lies wholly outside a narrow area.
	l = s.Layout(screen.Rect{W: 12, H: 6})
	wantRect(t, "p1 in 12 columns", l.Panes["p1"], screen.Rect{W: 12, H: 6})
	if r := l.Panes["p2"]; r.W != 0 || r.H != 0 {
		t.Fatalf("p2 outside the area = %+v, want empty", r)
	}
	if l.Divider.W != 0 || l.Divider.H != 0 {
		t.Fatalf("divider outside the area = %+v, want empty", l.Divider)
	}
	partition(t, l, screen.Rect{W: 12, H: 6})

	// Drawing clips at the rectangle: a wide character the edge cuts
	// becomes a blank, and nothing is written outside the area.
	one := onePane(t, 6, 2, "ab日x", "abcdef")
	c := screen.New(4, 3)
	c.Fill(screen.Rect{W: 4, H: 3}, screen.Cell{S: "#", W: 1})
	ll := one.Layout(screen.Rect{W: 3, H: 1})
	one.Draw(c, ll, true)
	if got := strings.Join(c.Lines(), "|"); got != "ab #|####|####" {
		t.Fatalf("canvas = %q", got)
	}
}

func TestLayoutLeavesPadForASmallerSession(t *testing.T) {
	s := onePane(t, 10, 4, "x")
	area := screen.Rect{X: 0, Y: 0, W: 20, H: 6}
	l := s.Layout(area)
	wantRect(t, "p1", l.Panes["p1"], screen.Rect{W: 10, H: 4})
	if n := partition(t, l, area); n != 20*6-10*4 {
		t.Fatalf("%d pad cells, want %d", n, 20*6-10*4)
	}

	// Two panes of a session smaller in both directions: pad right of the
	// second pane and below both.
	two := twoPanes(t, proto.SplitRight, 5, 3, 4, 3)
	area = screen.Rect{W: 16, H: 5}
	l = two.Layout(area)
	wantRect(t, "divider", l.Divider, screen.Rect{X: 5, Y: 0, W: 1, H: 3})
	if n := partition(t, l, area); n != 16*5-(5*3+1*3+4*3) {
		t.Fatalf("%d pad cells, want %d", n, 16*5-(5*3+1*3+4*3))
	}

	// A pane without a frame yet is pad.
	var w Set
	w.Reset(proto.ViewOK{Tab: "t1", Panes: []string{"p1"}})
	l = w.Layout(screen.Rect{W: 8, H: 3})
	if n := partition(t, l, screen.Rect{W: 8, H: 3}); n != 24 {
		t.Fatalf("%d pad cells before the first frame, want 24", n)
	}
}

func TestHitGivesPaneCoordinates(t *testing.T) {
	s := twoPanes(t, proto.SplitRight, 9, 5, 10, 5)
	l := s.Layout(screen.Rect{X: 0, Y: 0, W: 20, H: 5})
	cases := []struct {
		x, y   int
		pane   string
		px, py int
	}{
		{0, 0, "p1", 0, 0},
		{8, 4, "p1", 8, 4},
		{10, 0, "p2", 0, 0},
		{12, 3, "p2", 2, 3},
		{19, 4, "p2", 9, 4},
	}
	for _, c := range cases {
		pane, px, py, ok := l.Hit(c.x, c.y)
		if !ok || pane != c.pane || px != c.px || py != c.py {
			t.Errorf("Hit(%d, %d) = %q %d %d %v, want %q %d %d", c.x, c.y, pane, px, py, ok, c.pane, c.px, c.py)
		}
	}
	d := twoPanes(t, proto.SplitDown, 20, 2, 20, 2)
	l = d.Layout(screen.Rect{X: 0, Y: 1, W: 20, H: 5})
	if pane, px, py, ok := l.Hit(3, 5); !ok || pane != "p2" || px != 3 || py != 1 {
		t.Errorf("Hit(3, 5) in a split down = %q %d %d %v, want p2 3 1", pane, px, py, ok)
	}
}

func TestHitOnTheDividerOrThePadIsNothing(t *testing.T) {
	s := twoPanes(t, proto.SplitRight, 5, 3, 4, 3)
	l := s.Layout(screen.Rect{W: 16, H: 5})
	for _, p := range [][2]int{{5, 0}, {5, 2}, {12, 1}, {0, 3}, {15, 4}, {-1, 0}, {0, -1}, {16, 0}, {0, 5}} {
		if pane, _, _, ok := l.Hit(p[0], p[1]); ok {
			t.Errorf("Hit(%d, %d) = %q, want nothing", p[0], p[1], pane)
		}
	}
}

func TestDrawCopiesOnlyDirtyRows(t *testing.T) {
	s := onePane(t, 6, 3, "aaa", "bbbbbb", "ccc")
	c := screen.New(6, 3)
	l := s.Layout(screen.Rect{W: 6, H: 3})
	s.Draw(c, l, true)
	if got := strings.Join(c.Lines(), "|"); got != "aaa|bbbbbb|ccc" {
		t.Fatalf("after the first draw: %q", got)
	}

	// Mark rows 1 and 2 on the canvas, then change row 0 and row 2: a draw
	// of dirty rows writes 0 and 2 and leaves the mark in row 1.
	c.Set(5, 1, screen.Cell{S: "#", W: 1})
	c.Set(5, 2, screen.Cell{S: "#", W: 1})
	if err := s.Apply(partial("p1", 2, 6, 3, proto.Line{Y: 0, S: "A"}, proto.Line{Y: 2, S: "C"})); err != nil {
		t.Fatal(err)
	}
	s.Draw(c, l, false)
	if got := strings.Join(c.Lines(), "|"); got != "A|bbbbb#|C" {
		t.Fatalf("after a draw of dirty rows: %q", got)
	}

	// A frame that repeats a row as it is makes nothing dirty.
	c.Set(5, 0, screen.Cell{S: "#", W: 1})
	if err := s.Apply(partial("p1", 3, 6, 3, proto.Line{Y: 0, S: "A"})); err != nil {
		t.Fatal(err)
	}
	s.Draw(c, l, false)
	if got := strings.Join(c.Lines(), "|"); got != "A    #|bbbbb#|C" {
		t.Fatalf("after an unchanged row: %q", got)
	}

	// all draws every row.
	s.Draw(c, l, true)
	if got := strings.Join(c.Lines(), "|"); got != "A|bbbbbb|C" {
		t.Fatalf("after a full draw: %q", got)
	}
}

func TestRetabStartsEveryPaneAtSequenceOne(t *testing.T) {
	var s Set
	s.Reset(proto.ViewOK{Session: "s1", Tab: "t1", Panes: []string{"p1"}})
	tab1 := proto.Tab{ID: "t1", Session: "s1", Index: 1, Panes: []string{"p1"}, Focused: "p1"}
	s.Retab(tab1)
	for seq := uint64(1); seq <= 2; seq++ {
		if err := s.Apply(full("p1", seq, 10, 3, "one")); err != nil {
			t.Fatal(err)
		}
	}

	// The same tab again, or only another focused pane: nothing restarts.
	s.Retab(tab1)
	if err := s.Apply(partial("p1", 3, 10, 3, proto.Line{Y: 0, S: "one"})); err != nil {
		t.Fatalf("an unchanged tab restarted its pane: %v", err)
	}

	// A split of the displayed tab: both panes start at seq 1, and p1 keeps
	// its cells until its full frame arrives.
	split := proto.Tab{ID: "t1", Session: "s1", Index: 1, Panes: []string{"p1", "p2"}, Split: proto.SplitRight, Focused: "p2"}
	s.Retab(split)
	if got := text(s.Screen("p1"), 0); got != "one" {
		t.Fatalf("p1 lost its cells: %q", got)
	}
	wantInvalid(t, s.Apply(partial("p1", 4, 10, 3, proto.Line{Y: 0, S: "x"})))
	s.Retab(proto.Tab{ID: "t1", Panes: []string{"p1"}})
	s.Retab(split)
	wantInvalid(t, s.Apply(partial("p1", 1, 10, 3, proto.Line{Y: 0, S: "x"})))
	s.Retab(proto.Tab{ID: "t1", Panes: []string{"p1"}})
	s.Retab(split)
	if err := s.Apply(full("p1", 1, 4, 3, "left")); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(full("p2", 1, 5, 3, "right")); err != nil {
		t.Fatal(err)
	}

	// Another tab: its panes start at seq 1; the old ones are gone.
	s.Retab(proto.Tab{ID: "t2", Session: "s1", Index: 2, Panes: []string{"p3"}, Focused: "p3"})
	wantInvalid(t, s.Apply(partial("p1", 2, 4, 3, proto.Line{Y: 0, S: "x"})))
	wantInvalid(t, s.Apply(full("p3", 2, 10, 3, "x")))
	if err := s.Apply(full("p3", 1, 10, 3, "three")); err != nil {
		t.Fatal(err)
	}
	if s.Screen("p1") != nil || s.Screen("p2") != nil {
		t.Fatal("a pane of the old tab still has a screen")
	}

	// Back to the first tab: p1 starts at seq 1 although it was shown
	// before, and its old cells do not come back.
	s.Retab(split)
	if got := text(s.Screen("p1"), 0); got != "" {
		t.Fatalf("p1 kept cells across another tab: %q", got)
	}
	if err := s.Apply(full("p1", 1, 4, 3, "back")); err != nil {
		t.Fatal(err)
	}

	// Reset starts a new view the same way.
	s.Reset(proto.ViewOK{Session: "s2", Tab: "t9", Panes: []string{"p1"}})
	if err := s.Apply(full("p1", 1, 4, 3, "new")); err != nil {
		t.Fatalf("Reset did not restart p1: %v", err)
	}
	// Retab right after Reset with the tab's split restarts nothing.
	s.Retab(proto.Tab{ID: "t9", Panes: []string{"p1"}, Split: ""})
	if err := s.Apply(partial("p1", 2, 4, 3, proto.Line{Y: 0, S: "x"})); err != nil {
		t.Fatalf("Retab after Reset restarted p1: %v", err)
	}
}
