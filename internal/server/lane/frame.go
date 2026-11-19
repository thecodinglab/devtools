package lane

import (
	"fmt"

	"devtools/internal/pane"
	"devtools/internal/proto"
)

// pieceBase is a generous bound on the bytes of a frame piece without its
// lines: the envelope, the pane id, seq, size, cursor, modes, top, first and
// base.
const pieceBase = 512

// build turns a snapshot into the frame this connection needs for slot s:
// every row when the frame is full (the first after Show, or a size change),
// else the rows that differ from what was last sent. Cursor, modes, the
// scroll position and the history range are compared too, so a change of any
// of them alone is a frame without rows. No pieces and no error mean nothing
// changed.
//
// The frame gets seq s.seq+1: only Run changes s.seq, and a frame built for a
// slot that Show replaced meanwhile is never written, so the number is the
// one the commit assigns.
//
// build reads s.seq and s.sent, which only Run touches, and returns the new
// sent state for the commit.
func build(s *slot, snap pane.Snap) ([][]byte, sent, error) {
	cols, rows := snap.Cols, snap.Rows
	if rows < 0 {
		rows = 0
	}
	lines := make([]string, rows)
	copy(lines, snap.Lines)

	st := sent{
		cols: cols, rows: rows,
		lines:    lines,
		modes:    proto.Modes{Alt: snap.Modes.Alt, Mouse: snap.Modes.Mouse, Keys: snap.Modes.Kitty},
		scrolled: snap.Scrolled,
		first:    snap.First, base: snap.Base,
		cursor: proto.Cursor{
			X: snap.Cursor.X, Y: snap.Cursor.Y,
			// The cursor is hidden while this connection is scrolled (4.2.4).
			Visible: snap.Cursor.Visible && !snap.Scrolled,
			Shape:   snap.Cursor.Shape, Blink: snap.Cursor.Blink,
		},
	}
	if snap.Scrolled {
		st.top = snap.Top
	}

	prev := &s.sent
	full := s.seq == 0 || cols != prev.cols || rows != prev.rows
	var changed []proto.Line
	if full {
		changed = make([]proto.Line, rows)
		for y, ln := range lines {
			changed[y] = proto.Line{Y: y, S: ln}
		}
	} else {
		for y, ln := range lines {
			if ln != prev.lines[y] {
				changed = append(changed, proto.Line{Y: y, S: ln})
			}
		}
		if len(changed) == 0 && st.cursor == prev.cursor && st.modes == prev.modes &&
			st.scrolled == prev.scrolled && st.top == prev.top &&
			st.first == prev.first && st.base == prev.base {
			return nil, st, nil
		}
	}

	f := proto.Frame{
		Pane:   s.pane,
		Seq:    s.seq + 1,
		Cols:   cols,
		Rows:   rows,
		Full:   full,
		Cursor: st.cursor,
		Modes:  st.modes,
		First:  st.first,
		Base:   st.base,
	}
	if st.scrolled {
		top := st.top
		f.Top = &top
	}
	out, err := pieces(f, changed)
	return out, st, err
}

// pieces encodes f with lines, split so that a new piece starts before the
// lines of one piece would pass proto.FramePiece (4.1). Every piece but the
// last has More; all share seq and the other fields. A frame without lines is
// one piece with an empty list.
//
// A row is at most proto.MaxRow (128 KiB) encoded, so a piece stays far below
// the message limit. A piece above it can only come from a row far beyond
// that bound, which the receiver would refuse anyway (4.2.5); the error ends
// the connection instead of sending a frame that leaves out a row.
func pieces(f proto.Frame, lines []proto.Line) ([][]byte, error) {
	var out [][]byte
	emit := func(part []proto.Line, more bool) error {
		p := f
		p.Lines = part
		if p.Lines == nil {
			p.Lines = []proto.Line{}
		}
		p.More = more
		b, err := frame(proto.Encode(p))
		if err != nil {
			return fmt.Errorf("lane: frame of pane %s: %w", f.Pane, err)
		}
		out = append(out, b)
		return nil
	}
	start, size := 0, pieceBase
	for i, ln := range lines {
		n := lineSize(ln)
		if i > start && size+n > proto.FramePiece {
			if err := emit(lines[start:i], true); err != nil {
				return nil, err
			}
			start, size = i, pieceBase
		}
		size += n
	}
	if err := emit(lines[start:], false); err != nil {
		return nil, err
	}
	return out, nil
}

// lineSize bounds the bytes of one encoded line, {"y":N,"s":"..."} and its
// comma, with the escapes encoding/json writes: \u00XX for control bytes and
// for <, > and &, a backslash before " and \.
func lineSize(l proto.Line) int {
	n := 20
	s := l.S
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x20, c == '<', c == '>', c == '&':
			n += 6
		case c == '"', c == '\\':
			n += 2
		default:
			n++
		}
	}
	return n
}
