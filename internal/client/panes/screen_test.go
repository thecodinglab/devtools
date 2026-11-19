package panes

import (
	"errors"
	"strings"
	"testing"

	"devtools/internal/client/copymode"
	"devtools/internal/client/screen"
	"devtools/internal/proto"
)

// Screen is what copy mode reads (5.3.7).
var _ copymode.Source = (*Screen)(nil)

// full is a full frame of pane with the given rows from row 0.
func full(pane string, seq uint64, cols, rows int, lines ...string) proto.Frame {
	f := proto.Frame{Pane: pane, Seq: seq, Cols: cols, Rows: rows, Full: true, Lines: []proto.Line{}}
	for y, s := range lines {
		f.Lines = append(f.Lines, proto.Line{Y: y, S: s})
	}
	return f
}

// partial is a frame of pane that changes the rows given as y, s pairs.
func partial(pane string, seq uint64, cols, rows int, ls ...proto.Line) proto.Frame {
	return proto.Frame{Pane: pane, Seq: seq, Cols: cols, Rows: rows, Lines: append([]proto.Line{}, ls...)}
}

// text is row y of the screen as plain text, every cell of the row,
// trailing blanks removed.
func text(sc *Screen, y int) string {
	cols, _ := sc.Size()
	var b strings.Builder
	for x := range cols {
		c := sc.Cell(y, x)
		if c.W == 0 {
			continue
		}
		b.WriteString(c.S)
	}
	return strings.TrimRight(b.String(), " ")
}

func rowsOf(sc *Screen) []string {
	_, rows := sc.Size()
	out := make([]string, rows)
	for y := range out {
		out[y] = text(sc, y)
	}
	return out
}

func wantRows(t *testing.T, sc *Screen, want ...string) {
	t.Helper()
	got := rowsOf(sc)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %q, want %q", got, want)
	}
}

func mustApply(t *testing.T, sc *Screen, f proto.Frame) {
	t.Helper()
	complete, err := sc.Apply(f)
	if err != nil {
		t.Fatalf("Apply(seq %d): %v", f.Seq, err)
	}
	if complete == f.More {
		t.Fatalf("Apply(seq %d, more %v): complete = %v", f.Seq, f.More, complete)
	}
}

func wantInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the frame was accepted, want an error")
	}
	if !errors.Is(err, errInvalid) {
		t.Fatalf("error %v does not wrap errInvalid", err)
	}
}

func TestAFullFrameReplacesTheScreen(t *testing.T) {
	var sc Screen
	f := full("p1", 1, 10, 3, "\x1b[0;1mhello\x1b[0m w", "world", "x")
	f.Cursor = proto.Cursor{X: 4, Y: 2, Visible: true, Shape: "block"}
	f.Modes = proto.Modes{Alt: true, Mouse: "press", Keys: 3}
	f.First, f.Base = 2000, 12000
	mustApply(t, &sc, f)
	if c, r := sc.Size(); c != 10 || r != 3 {
		t.Fatalf("Size = %d, %d, want 10, 3", c, r)
	}
	wantRows(t, &sc, "hello w", "world", "x")
	if c := sc.Cell(0, 0); c.S != "h" || c.Style.Attrs != screen.Bold {
		t.Fatalf("cell 0,0 = %+v, want a bold h", c)
	}
	if c := sc.Cell(0, 6); c.S != "w" || c.Style.Attrs != 0 {
		t.Fatalf("cell 0,6 = %+v, want a plain w", c)
	}
	if c := sc.Cell(2, 9); c != blank {
		t.Fatalf("an omitted trailing cell = %+v, want a blank", c)
	}
	if sc.Cursor() != f.Cursor || sc.Modes() != f.Modes {
		t.Fatalf("cursor %+v modes %+v, want %+v %+v", sc.Cursor(), sc.Modes(), f.Cursor, f.Modes)
	}
	if first, base := sc.Range(); first != 2000 || base != 12000 {
		t.Fatalf("Range = %d, %d", first, base)
	}

	// A second full frame names only row 0: the rows it leaves out are blank.
	mustApply(t, &sc, full("p1", 2, 10, 3, "bye"))
	wantRows(t, &sc, "bye", "", "")

	// A full frame of another size replaces the screen with its size.
	mustApply(t, &sc, full("p1", 3, 4, 2, "ab", "日本"))
	if c, r := sc.Size(); c != 4 || r != 2 {
		t.Fatalf("Size after the resize = %d, %d, want 4, 2", c, r)
	}
	wantRows(t, &sc, "ab", "日本")
	if c := sc.Cell(1, 1); c.W != 0 {
		t.Fatalf("the second cell of a wide character = %+v, want width 0", c)
	}
}

func TestAPartialFrameChangesOnlyItsRows(t *testing.T) {
	var sc Screen
	mustApply(t, &sc, full("p1", 1, 10, 3, "one", "two", "three"))
	f := partial("p1", 2, 10, 3, proto.Line{Y: 1, S: "TWO"})
	f.Cursor = proto.Cursor{X: 3, Y: 1, Visible: true}
	mustApply(t, &sc, f)
	wantRows(t, &sc, "one", "TWO", "three")
	if sc.Cursor() != f.Cursor {
		t.Fatalf("cursor = %+v, want %+v", sc.Cursor(), f.Cursor)
	}
	// An empty string is a blank row.
	mustApply(t, &sc, partial("p1", 3, 10, 3, proto.Line{Y: 2, S: ""}))
	wantRows(t, &sc, "one", "TWO", "")
	// A frame without rows changes nothing but its other fields.
	g := partial("p1", 4, 10, 3)
	g.Modes = proto.Modes{Mouse: "all"}
	mustApply(t, &sc, g)
	wantRows(t, &sc, "one", "TWO", "")
	if sc.Modes().Mouse != "all" {
		t.Fatalf("modes = %+v", sc.Modes())
	}
}

func TestASplitFrameIsCompleteAfterItsLastPiece(t *testing.T) {
	var sc Screen
	a := full("p1", 1, 10, 3, "a0", "a1")
	a.More = true
	mustApply(t, &sc, a)
	if c, r := sc.Size(); c != 0 || r != 0 {
		t.Fatalf("a first piece changed the screen to %d by %d", c, r)
	}
	b := proto.Frame{Pane: "p1", Seq: 1, Cols: 10, Rows: 3, Full: true,
		Lines: []proto.Line{{Y: 2, S: "a2"}}, Cursor: proto.Cursor{X: 2, Y: 2, Visible: true}}
	mustApply(t, &sc, b)
	wantRows(t, &sc, "a0", "a1", "a2")
	if sc.Cursor() != b.Cursor {
		t.Fatalf("cursor = %+v, the last piece's is %+v", sc.Cursor(), b.Cursor)
	}

	// A split partial frame: nothing changes before its last piece.
	p := partial("p1", 2, 10, 3, proto.Line{Y: 0, S: "b0"})
	p.More = true
	mustApply(t, &sc, p)
	wantRows(t, &sc, "a0", "a1", "a2")
	mustApply(t, &sc, partial("p1", 2, 10, 3, proto.Line{Y: 2, S: "b2"}))
	wantRows(t, &sc, "b0", "a1", "b2")

	// The next frame after a split one has the next seq.
	mustApply(t, &sc, partial("p1", 3, 10, 3, proto.Line{Y: 1, S: "c1"}))
	wantRows(t, &sc, "b0", "c1", "b2")

	// A piece whose seq is not its frame's is an error.
	q := partial("p1", 4, 10, 3, proto.Line{Y: 0, S: "d0"})
	q.More = true
	mustApply(t, &sc, q)
	_, err := sc.Apply(partial("p1", 5, 10, 3, proto.Line{Y: 0, S: "e0"}))
	wantInvalid(t, err)
	wantRows(t, &sc, "b0", "c1", "b2")
}

func TestASequenceGapIsAnError(t *testing.T) {
	t.Run("the first frame is not seq 1", func(t *testing.T) {
		var sc Screen
		_, err := sc.Apply(full("p1", 2, 10, 3, "x"))
		wantInvalid(t, err)
	})
	t.Run("a gap", func(t *testing.T) {
		var sc Screen
		mustApply(t, &sc, full("p1", 1, 10, 3, "x"))
		_, err := sc.Apply(partial("p1", 3, 10, 3, proto.Line{Y: 0, S: "y"}))
		wantInvalid(t, err)
		wantRows(t, &sc, "x", "", "")
	})
	t.Run("a repeated seq", func(t *testing.T) {
		var sc Screen
		mustApply(t, &sc, full("p1", 1, 10, 3, "x"))
		mustApply(t, &sc, partial("p1", 2, 10, 3, proto.Line{Y: 0, S: "y"}))
		_, err := sc.Apply(partial("p1", 2, 10, 3, proto.Line{Y: 0, S: "z"}))
		wantInvalid(t, err)
		// A full frame at seq 1 again is no restart either: only Retab or
		// Reset restarts a pane.
		_, err = sc.Apply(full("p1", 1, 10, 3, "z"))
		wantInvalid(t, err)
		wantRows(t, &sc, "y", "", "")
	})
}

func TestAFrameForAPaneOutsideTheViewIsAnError(t *testing.T) {
	var s Set
	wantInvalid(t, s.Apply(full("p1", 1, 10, 3, "x")))

	s.Reset(proto.ViewOK{Session: "s1", Tab: "t1", Panes: []string{"p1"}})
	if err := s.Apply(full("p1", 1, 10, 3, "x")); err != nil {
		t.Fatal(err)
	}
	wantInvalid(t, s.Apply(full("p9", 1, 10, 3, "x")))
	if s.Screen("p9") != nil {
		t.Fatal("a pane outside the view has a screen")
	}
	if got := text(s.Screen("p1"), 0); got != "x" {
		t.Fatalf("p1 row 0 = %q", got)
	}
}

func TestAnInvalidRowIsAnErrorAndChangesNothing(t *testing.T) {
	cases := []struct {
		name string
		line proto.Line
	}{
		{"an sgr that does not start from reset", proto.Line{Y: 1, S: "\x1b[31mred"}},
		{"a control character", proto.Line{Y: 1, S: "a\x07b"}},
		{"a cursor movement", proto.Line{Y: 1, S: "\x1b[2Jx"}},
		{"a row wider than the pane", proto.Line{Y: 1, S: "0123456789abc"}},
		{"a row below the pane", proto.Line{Y: 3, S: "x"}},
		{"a row above the pane", proto.Line{Y: -1, S: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sc Screen
			mustApply(t, &sc, full("p1", 1, 10, 3, "one", "two", "three"))
			cur := sc.Cursor()
			f := partial("p1", 2, 10, 3, proto.Line{Y: 0, S: "ONE"}, c.line)
			f.Cursor = proto.Cursor{X: 9, Y: 2, Visible: true}
			_, err := sc.Apply(f)
			wantInvalid(t, err)
			wantRows(t, &sc, "one", "two", "three")
			if sc.Cursor() != cur {
				t.Fatalf("the cursor moved to %+v", sc.Cursor())
			}
		})
	}
	t.Run("in the last piece of a split frame", func(t *testing.T) {
		var sc Screen
		mustApply(t, &sc, full("p1", 1, 10, 3, "one", "two", "three"))
		p := partial("p1", 2, 10, 3, proto.Line{Y: 0, S: "ONE"})
		p.More = true
		mustApply(t, &sc, p)
		_, err := sc.Apply(partial("p1", 2, 10, 3, proto.Line{Y: 1, S: "\x1b]0;title\x07"}))
		wantInvalid(t, err)
		wantRows(t, &sc, "one", "two", "three")
	})
	t.Run("a size outside the limits", func(t *testing.T) {
		var sc Screen
		_, err := sc.Apply(full("p1", 1, proto.MaxCols+1, 3, "x"))
		wantInvalid(t, err)
	})
}

func TestASizeChangeWithoutAFullFrameIsAnError(t *testing.T) {
	t.Run("the first frame is not full", func(t *testing.T) {
		var sc Screen
		_, err := sc.Apply(partial("p1", 1, 10, 3, proto.Line{Y: 0, S: "x"}))
		wantInvalid(t, err)
	})
	for _, size := range [][2]int{{12, 3}, {10, 4}} {
		var sc Screen
		mustApply(t, &sc, full("p1", 1, 10, 3, "one"))
		_, err := sc.Apply(partial("p1", 2, size[0], size[1], proto.Line{Y: 0, S: "x"}))
		wantInvalid(t, err)
		if c, r := sc.Size(); c != 10 || r != 3 {
			t.Fatalf("Size = %d, %d after a refused frame", c, r)
		}
		wantRows(t, &sc, "one", "", "")
	}
	t.Run("a piece of another size", func(t *testing.T) {
		var sc Screen
		a := full("p1", 1, 10, 3, "x")
		a.More = true
		mustApply(t, &sc, a)
		_, err := sc.Apply(full("p1", 1, 11, 3, "y"))
		wantInvalid(t, err)
	})
}

func TestScrolledScreenReportsItsTop(t *testing.T) {
	var sc Screen
	f := full("p1", 1, 10, 3, "live")
	f.First, f.Base = 2000, 12000
	mustApply(t, &sc, f)
	if sc.Top() != 12000 {
		t.Fatalf("Top of a live screen = %d, want its base 12000", sc.Top())
	}
	top := int64(11950)
	g := partial("p1", 2, 10, 3, proto.Line{Y: 0, S: "old"})
	g.Top, g.First, g.Base = &top, 2000, 12000
	mustApply(t, &sc, g)
	if sc.Top() != 11950 {
		t.Fatalf("Top while scrolled = %d, want 11950", sc.Top())
	}
	if first, base := sc.Range(); first != 2000 || base != 12000 {
		t.Fatalf("Range = %d, %d", first, base)
	}
	h := partial("p1", 3, 10, 3, proto.Line{Y: 0, S: "live"})
	h.First, h.Base = 2001, 12001
	mustApply(t, &sc, h)
	if sc.Top() != 12001 {
		t.Fatalf("Top back at live = %d, want the base 12001", sc.Top())
	}
}
