package screen

import (
	"reflect"
	"testing"
)

// row returns the cells of row y of c.
func cellsOf(c *Canvas, y int) []Cell {
	w, _ := c.Size()
	out := make([]Cell, w)
	for x := range w {
		out[x] = c.At(x, y)
	}
	return out
}

// checkInvariant fails the test when a wide cell is not followed by its
// second cell or a second cell is not preceded by a wide cell.
func checkInvariant(t *testing.T, c *Canvas) {
	t.Helper()
	w, h := c.Size()
	for y := range h {
		for x := range w {
			cell := c.At(x, y)
			switch cell.W {
			case 2:
				if x+1 >= w || c.At(x+1, y).W != 0 {
					t.Errorf("cell %d,%d is wide but %d,%d is %+v", x, y, x+1, y, c.At(x+1, y))
				}
			case 0:
				if x == 0 || c.At(x-1, y).W != 2 {
					t.Errorf("cell %d,%d is a second half without a wide cell before it", x, y)
				}
			}
		}
	}
}

func TestSetOutsideIsANoOp(t *testing.T) {
	c := New(4, 3)
	before := c.Lines()
	x := Cell{S: "x", W: 1}
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {4, 0}, {0, 3}, {100, 100}, {-5, -5}} {
		c.Set(p[0], p[1], x)
	}
	if got := c.Lines(); !reflect.DeepEqual(got, before) {
		t.Fatalf("Set outside changed the canvas: %q", got)
	}
	if got := c.At(-1, 0); got != (Cell{}) {
		t.Errorf("At outside = %+v, want the zero cell", got)
	}
	// a Sub's outside is its own, even where the parent has cells
	s := c.Sub(Rect{1, 1, 2, 1})
	s.Set(2, 0, x)
	s.Set(-1, 0, x)
	s.Set(0, 1, x)
	if got := c.Lines(); !reflect.DeepEqual(got, before) {
		t.Fatalf("Set outside a Sub changed the parent: %q", got)
	}
}

func TestSetRepairsAWideCharacterItCuts(t *testing.T) {
	st := Style{Fg: Color{Kind: 1, V: 2}}
	wide := Cell{S: "日", W: 2, Style: st}
	x := Cell{S: "x", W: 1}

	// overwriting the first half leaves no orphan second half
	c := New(6, 1)
	c.Set(1, 0, wide)
	c.Set(1, 0, x)
	if got := c.At(2, 0); got != (Cell{S: " ", W: 1, Style: st}) {
		t.Errorf("after overwriting the first half, cell 2 = %+v", got)
	}
	checkInvariant(t, c)

	// overwriting the second half blanks the first half, in its style
	c = New(6, 1)
	c.Set(1, 0, wide)
	c.Set(2, 0, x)
	if got := c.At(1, 0); got != (Cell{S: " ", W: 1, Style: st}) {
		t.Errorf("after overwriting the second half, cell 1 = %+v", got)
	}
	if got := c.At(2, 0); got != x {
		t.Errorf("cell 2 = %+v, want x", got)
	}
	checkInvariant(t, c)

	// a wide cell over the second half of another wide cell
	c = New(6, 1)
	c.Set(0, 0, wide)
	c.Set(1, 0, wide)
	if got := c.Lines()[0]; got != "\x1b[0;32m 日\x1b[0m" {
		t.Errorf("row = %q", got)
	}
	checkInvariant(t, c)

	// a wide cell over the first half of another wide cell
	c = New(6, 1)
	c.Set(2, 0, wide)
	c.Set(1, 0, Cell{S: "語", W: 2})
	if got := cellsOf(c, 0)[3]; got != (Cell{S: " ", W: 1, Style: st}) {
		t.Errorf("cell 3 = %+v", got)
	}
	checkInvariant(t, c)

	// a wide cell that does not fit at the right edge is a blank
	c = New(3, 1)
	c.Set(2, 0, wide)
	if got := c.At(2, 0); got != (Cell{S: " ", W: 1, Style: st}) {
		t.Errorf("wide cell at the last column = %+v", got)
	}
	checkInvariant(t, c)

	// the same at the right edge of a Sub: the neighbour is not touched
	c = New(4, 1)
	c.Set(2, 0, x)
	c.Sub(Rect{0, 0, 2, 1}).Set(1, 0, wide)
	if got := c.At(2, 0); got != x {
		t.Errorf("a wide cell spilled out of its Sub: %+v", got)
	}
	checkInvariant(t, c)

	// a second half set on its own is a blank
	c = New(3, 1)
	c.Set(1, 0, Cell{S: "", W: 0, Style: st})
	if got := c.At(1, 0); got != (Cell{S: " ", W: 1, Style: st}) {
		t.Errorf("a lone second half = %+v", got)
	}
	checkInvariant(t, c)
}

func TestSubClipsAndSharesCells(t *testing.T) {
	c := New(10, 4)
	s := c.Sub(Rect{2, 1, 5, 2})
	if w, h := s.Size(); w != 5 || h != 2 {
		t.Fatalf("Sub size = %dx%d", w, h)
	}
	s.Text(0, 0, "abcdefgh", Style{}, 100)
	if got := c.Lines()[1]; got != "  abcde" {
		t.Errorf("parent row 1 = %q, want the text clipped at the Sub's edge", got)
	}
	c.Set(3, 2, Cell{S: "z", W: 1})
	if got := s.At(1, 1); got.S != "z" {
		t.Errorf("Sub does not see the parent's write: %+v", got)
	}
	s.Clear()
	if got := c.Lines()[1]; got != "" {
		t.Errorf("Clear of the Sub left %q", got)
	}

	// a Sub of a Sub is relative to the inner one and clipped to it
	c = New(10, 4)
	inner := c.Sub(Rect{2, 1, 5, 2}).Sub(Rect{3, 0, 10, 10})
	inner.Fill(Rect{0, 0, 10, 10}, Cell{S: "#", W: 1})
	want := []string{"", "     ##", "     ##", ""}
	if got := c.Lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("nested Sub filled %q, want %q", got, want)
	}

	// a rectangle partly outside: coordinates stay relative to it
	c = New(4, 2)
	o := c.Sub(Rect{-2, 0, 4, 1})
	o.Text(0, 0, "abcd", Style{}, 4)
	if got := c.Lines()[0]; got != "cd" {
		t.Errorf("Sub at -2 drew %q, want %q", got, "cd")
	}
	if got := o.Lines(); !reflect.DeepEqual(got, []string{"cd"}) {
		t.Errorf("Lines of the Sub = %q", got)
	}
}

func TestRestyleKeepsTheText(t *testing.T) {
	c := New(8, 2)
	c.Text(0, 0, "a日b", Style{Fg: Color{Kind: 1, V: 1}}, 8)
	before := cellsOf(c, 0)
	c.Restyle(Rect{0, 0, 3, 1}, func(s Style) Style { s.Attrs |= Reverse; return s })
	after := cellsOf(c, 0)
	for x := range before {
		if before[x].S != after[x].S || before[x].W != after[x].W {
			t.Errorf("cell %d: text %q/%d became %q/%d", x, before[x].S, before[x].W, after[x].S, after[x].W)
		}
	}
	for x := range 3 {
		if after[x].Style.Attrs&Reverse == 0 || after[x].Style.Fg != (Color{Kind: 1, V: 1}) {
			t.Errorf("cell %d style = %+v", x, after[x].Style)
		}
	}
	if after[3].Style.Attrs&Reverse != 0 {
		t.Errorf("cell 3 outside the rectangle was restyled")
	}
	if got := c.Lines()[0]; got != "\x1b[0;7;31ma日\x1b[0;31mb\x1b[0m" {
		t.Errorf("row = %q", got)
	}
	// outside the canvas nothing happens and nothing panics
	c.Restyle(Rect{-3, -3, 100, 100}, func(Style) Style { return Style{} })
}

func TestResizeKeepsTheTopLeft(t *testing.T) {
	c := New(6, 3)
	c.Text(0, 0, "abcdef", Style{}, 6)
	c.Text(0, 1, "ab日d", Style{}, 6)
	c.Text(0, 2, "zzzzzz", Style{}, 6)
	c.Resize(3, 2)
	if w, h := c.Size(); w != 3 || h != 2 {
		t.Fatalf("size = %dx%d", w, h)
	}
	if got, want := c.Lines(), []string{"abc", "ab"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after shrinking: %q, want %q (the cut wide character is a blank)", got, want)
	}
	checkInvariant(t, c)
	c.Resize(5, 3)
	if got, want := c.Lines(), []string{"abc", "ab", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("after growing: %q, want %q", got, want)
	}
	if got := c.At(4, 2); got != blank {
		t.Errorf("a new cell is %+v, want a blank", got)
	}
	c.Resize(-1, -1)
	if w, h := c.Size(); w != 0 || h != 0 {
		t.Errorf("negative size gives %dx%d", w, h)
	}
	if got := c.Render(); got != "" {
		t.Errorf("empty canvas renders %q", got)
	}
}

func TestCellsWritesARowClipped(t *testing.T) {
	c := New(4, 1)
	st := Style{Attrs: Bold}
	c.Cells(1, 0, []Cell{{S: "日", W: 2, Style: st}, {S: "", W: 0, Style: st}, {S: "a", W: 1}, {S: "b", W: 1}})
	want := []Cell{blank, {S: "日", W: 2, Style: st}, {S: "", W: 0, Style: st}, {S: "a", W: 1}}
	if got := cellsOf(c, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("cells = %+v, want %+v", got, want)
	}
	checkInvariant(t, c)
}
