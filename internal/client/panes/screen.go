// Package panes turns the frames a server sends into pane screens and
// places the screens of the displayed tab on the client's canvas (3.2.4,
// 4.2.4 and 5.3.7 of the plan).
//
// A Screen is one pane's copy of what the server last presented: its rows
// as cells, parsed by rowfilter.Parse (nothing a server sent reaches the
// terminal as bytes), its cursor, its modes and its place in history. A Set
// holds the screens of the panes of the displayed tab, checks that every
// frame belongs to one of them, and computes the Layout: where each pane
// lies in the pane area, where the divider is, and which cells belong to no
// pane (the pad). Panes are placed from the sizes their frames carry and the
// tab's split; no layout arithmetic of the server is repeated here.
//
// The package draws only pane cells. The divider, the pad and everything
// else around the panes are the chrome's.
package panes

import (
	"errors"
	"fmt"
	"slices"

	"devtools/internal/client/rowfilter"
	"devtools/internal/client/screen"
	"devtools/internal/proto"
)

// Errors a frame can be refused with. Every refusal wraps errInvalid, so a
// caller can tell a bad frame from nothing else: the client drops the link
// the frame came on and shows a notice (3.2.4).
var (
	errInvalid  = errors.New("invalid frame")
	errSequence = fmt.Errorf("%w: sequence", errInvalid)
	errSize     = fmt.Errorf("%w: size", errInvalid)
	errNotFull  = fmt.Errorf("%w: not full", errInvalid)
	errRow      = fmt.Errorf("%w: row", errInvalid)
	errPiece    = fmt.Errorf("%w: piece", errInvalid)
	errNotShown = fmt.Errorf("%w: pane not displayed", errInvalid)
)

// blank is a cell the encoder left out: trailing cells of a row that are
// blank in the default style, and every cell of an empty row.
var blank = screen.Cell{S: " ", W: 1}

// Screen is one pane's rows as cells, as the last complete frame left them.
// It satisfies copymode.Source. The zero value is a pane that has received
// no frame: size 0 by 0, expecting seq 1.
type Screen struct {
	cols, rows int
	// lines holds one entry per row: the cells rowfilter.Parse returned,
	// without the trailing blanks the encoder omits.
	lines [][]screen.Cell
	// dirty marks rows that changed since the last Draw.
	dirty []bool

	cursor      proto.Cursor
	modes       proto.Modes
	scrolled    bool
	top         int64
	first, base int64

	// seq is the seq of the last complete frame; 0 before the first.
	seq uint64
	// pend is the frame being assembled from its pieces (4.2.4 `more`).
	pend *pending
}

// pending is a frame whose last piece has not arrived. Its rows are applied
// to the screen only with the last piece, so a split frame is presented
// whole or not at all.
type pending struct {
	seq        uint64
	cols, rows int
	full       bool
	lines      map[int][]screen.Cell
}

// Apply applies one frame or one piece of a frame. complete is true when
// the frame is whole: a frame without `more`, or the last piece of a split
// one; only then do the screen's rows, cursor and modes change.
//
// A frame is refused, and the screen keeps what it had before the frame's
// first piece, when its seq is not one more than the last frame's (the
// pieces of a split frame share theirs), when it is the first frame or
// changes the size and is not full, when a piece does not match the piece
// before it, when a row lies outside the pane or does not parse
// (rowfilter.Parse, 4.2.5), or when its size is outside the limits of 4.1.
// A refused frame also discards the pieces before it: the caller drops the
// link, and a screen is not used again before Set.Reset or Set.Retab gives
// the pane a new one.
func (s *Screen) Apply(f proto.Frame) (complete bool, err error) {
	p, err := s.admit(f)
	if err != nil {
		s.pend = nil
		return false, fmt.Errorf("panes: frame %d of pane %s: %w", f.Seq, f.Pane, err)
	}
	rows := make(map[int][]screen.Cell, len(f.Lines))
	for _, ln := range f.Lines {
		if ln.Y < 0 || ln.Y >= p.rows {
			s.pend = nil
			return false, fmt.Errorf("panes: frame %d of pane %s: %w %d of %d", f.Seq, f.Pane, errRow, ln.Y, p.rows)
		}
		cells, perr := rowfilter.Parse(ln.S, p.cols)
		if perr != nil {
			s.pend = nil
			return false, fmt.Errorf("panes: frame %d of pane %s: %w %d: %w", f.Seq, f.Pane, errRow, ln.Y, perr)
		}
		rows[ln.Y] = cells
	}
	// Every row of the piece parsed: only now does the piece count.
	for y, cells := range rows {
		p.lines[y] = cells
	}
	if f.More {
		s.pend = p
		return false, nil
	}
	s.pend = nil
	s.commit(p, f)
	return true, nil
}

// admit checks the header of f against the screen and the pieces before
// it, and returns the pending frame f belongs to.
func (s *Screen) admit(f proto.Frame) (*pending, error) {
	if p := s.pend; p != nil {
		switch {
		case f.Seq != p.seq:
			return nil, fmt.Errorf("%w: seq %d inside the pieces of frame %d", errSequence, f.Seq, p.seq)
		case f.Cols != p.cols || f.Rows != p.rows || f.Full != p.full:
			return nil, fmt.Errorf("%w: its size or fullness differs from the piece before it", errPiece)
		}
		return p, nil
	}
	if want := s.seq + 1; f.Seq != want {
		return nil, fmt.Errorf("%w: seq %d, want %d", errSequence, f.Seq, want)
	}
	if f.Cols < 0 || f.Cols > proto.MaxCols || f.Rows < 0 || f.Rows > proto.MaxRows {
		return nil, fmt.Errorf("%w: %d by %d is outside the limits", errSize, f.Cols, f.Rows)
	}
	if !f.Full {
		if s.seq == 0 {
			return nil, fmt.Errorf("%w: the first frame of a pane must be full", errNotFull)
		}
		if f.Cols != s.cols || f.Rows != s.rows {
			return nil, fmt.Errorf("%w: the size changed from %d by %d to %d by %d without a full frame",
				errNotFull, s.cols, s.rows, f.Cols, f.Rows)
		}
	}
	return &pending{seq: f.Seq, cols: f.Cols, rows: f.Rows, full: f.Full, lines: map[int][]screen.Cell{}}, nil
}

// commit makes the assembled frame p the screen's content; last is the
// frame's last piece, whose cursor, modes and history fields count.
func (s *Screen) commit(p *pending, last proto.Frame) {
	resized := p.cols != s.cols || p.rows != s.rows || len(s.lines) != p.rows
	if resized {
		s.cols, s.rows = p.cols, p.rows
		s.lines = make([][]screen.Cell, p.rows)
		s.dirty = make([]bool, p.rows)
		for y := range s.dirty {
			s.dirty[y] = true
		}
	}
	if p.full {
		// A full frame replaces the copy: a row it leaves out is blank.
		for y := range s.lines {
			s.set(y, p.lines[y])
		}
	} else {
		for y, cells := range p.lines {
			s.set(y, cells)
		}
	}
	s.cursor = last.Cursor
	s.modes = last.Modes
	s.first, s.base = last.First, last.Base
	s.scrolled = last.Top != nil
	if s.scrolled {
		s.top = *last.Top
	}
	s.seq = p.seq
}

// set replaces row y and marks it dirty when its cells changed.
func (s *Screen) set(y int, cells []screen.Cell) {
	if slices.Equal(s.lines[y], cells) {
		return
	}
	s.lines[y] = cells
	s.dirty[y] = true
}

// Size returns the pane's size from its last complete frame.
func (s *Screen) Size() (cols, rows int) { return s.cols, s.rows }

// Top returns the absolute number of the line in row 0 of what is shown:
// the frame's `top` while this connection is scrolled in the pane, else
// `base`, the first line of the live screen.
func (s *Screen) Top() int64 {
	if s.scrolled {
		return s.top
	}
	return s.base
}

// Range returns the absolute numbers of the oldest retained history line and
// of the line in row 0 of the live screen.
func (s *Screen) Range() (first, base int64) { return s.first, s.base }

// Cell returns the cell at row, col of what is shown. A cell the encoder
// left out, and a cell outside the pane, is a blank in the default style.
func (s *Screen) Cell(row, col int) screen.Cell {
	if row < 0 || row >= len(s.lines) || col < 0 || col >= s.cols {
		return blank
	}
	ln := s.lines[row]
	if col >= len(ln) {
		return blank
	}
	return ln[col]
}

// Cursor returns the cursor of the last complete frame.
func (s *Screen) Cursor() proto.Cursor { return s.cursor }

// Modes returns the modes of the last complete frame.
func (s *Screen) Modes() proto.Modes { return s.modes }

// draw copies the screen's rows onto c, the pane's rectangle of the
// canvas: every row when all is set, else the rows that changed since the
// last draw. A row is padded with blanks to the width of c, so a shorter
// row clears what an earlier one left. Cells outside c are clipped by the
// canvas. Afterwards no row is dirty.
func (s *Screen) draw(c *screen.Canvas, all bool) {
	w, h := c.Size()
	for y := 0; y < min(h, len(s.lines)); y++ {
		if !all && !s.dirty[y] {
			continue
		}
		ln := s.lines[y]
		c.Cells(0, y, ln)
		if n := len(ln); n < w {
			c.Fill(screen.Rect{X: n, Y: y, W: w - n, H: 1}, blank)
		}
	}
	clear(s.dirty)
}
