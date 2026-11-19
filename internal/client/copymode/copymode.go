// Package copymode is copy mode as a pure model (3.2.4, 5.3.7 and the keys
// of 5a.11.4 of the plan): a copy cursor that moves over the rows a pane
// shows, a selection by character, by line or as a block, and the effects
// the client carries out. The model sends nothing and draws nothing; the
// client turns its effects into pane.scroll and pane.text and draws the
// cursor and the selection from Cursor and Selection.
//
// Positions are absolute line numbers (4.2.7), so a selection stays on its
// text while the program prints. The model reads cells only through its
// Source, which holds the rows that are shown; a position on a line that is
// not shown is still a valid position, its cells are simply not known yet.
//
// Search is not built (F31).
package copymode

import "devtools/internal/client/screen"

// Source is the pane as copy mode sees it; panes.Screen satisfies it.
type Source interface {
	Size() (cols, rows int)
	Top() int64 // absolute line in row 0 of what is shown
	Range() (first, base int64)
	Cell(row, col int) screen.Cell
}

// Pos is one cell of the pane's history or screen: an absolute line number
// and a column.
type Pos struct {
	Line int64
	Col  int
}

// EffectKind says what the client does with an Effect.
type EffectKind int

const (
	// ScrollTo: show the pane from line Top (pane.scroll with that top and
	// the Marks). A Top equal to the base of Range is the live bottom.
	ScrollTo EffectKind = iota + 1
	// Copy: ask for the text from From to To (pane.text), both inclusive,
	// with Block as given, and write it to the clipboard.
	Copy
	// Exit: copy mode ended; the model accepts nothing more.
	Exit
)

func (k EffectKind) String() string {
	switch k {
	case ScrollTo:
		return "ScrollTo"
	case Copy:
		return "Copy"
	case Exit:
		return "Exit"
	}
	return "EffectKind(?)"
}

// Effect is one thing the client does for the model.
type Effect struct {
	Kind     EffectKind // ScrollTo, Copy, Exit
	Top      int64
	From, To Pos
	Block    bool
}

// Selection kinds, as Selection returns them and as chrome.Copy.Selecting
// names them.
const (
	Char  = "char"
	Line  = "line"
	Block = "block"
)

// Mouse kinds that Mouse understands. Rows and columns are cells inside the
// pane, from 0, as in input.mouse (4.2.6).
const (
	MousePress     = "press"      // a press of the left button
	MouseDrag      = "drag"       // motion with the left button held; "motion" is the same
	MouseRelease   = "release"    // the left button was released
	MouseWheelUp   = "wheel-up"   // one notch towards older lines
	MouseWheelDown = "wheel-down" // one notch towards the live bottom
)

// WheelLines is how far one notch of the wheel scrolls.
const WheelLines = 3

// Modifier bits of the key, in kitty's order (4.2.6).
const (
	modShift = 1
	modAlt   = 2
	modCtrl  = 4
	modSuper = 8
)

// Model is the state of copy mode in one pane of one client.
type Model struct {
	src         Source
	cols, rows  int
	first, base int64

	top    int64 // the line the model wants in row 0
	cursor Pos
	want   int // the column vertical moves aim for

	sel    string // "", Char, Line, Block
	anchor Pos

	pressed bool // a press was seen and its release not yet
	adopt   bool // a reflow renumbered the lines: take the Source's top at the next Sync
	done    bool
}

// Enter starts copy mode in src with the copy cursor at at, clamped to the
// lines src retains. The model starts at the top src shows (or at the
// nearest top that shows the cursor); the client sends that top and the
// Marks as its first pane.scroll.
func Enter(src Source, at Pos) *Model {
	m := &Model{}
	m.read(src)
	m.top = clamp(src.Top(), m.first, m.base)
	m.cursor = m.clampPos(at)
	m.want = m.cursor.Col
	m.reveal()
	return m
}

// Top is the line the model wants in row 0 of the pane.
func (m *Model) Top() int64 { return m.top }

// Done reports whether copy mode ended.
func (m *Model) Done() bool { return m.done }

// Cursor is the copy cursor.
func (m *Model) Cursor() Pos { return m.cursor }

// Marks are the cursor, then the selection anchor if any: what the client
// sends as `marks` of pane.scroll (4.2.7).
func (m *Model) Marks() []Pos {
	if m.sel == "" {
		return []Pos{m.cursor}
	}
	return []Pos{m.cursor, m.anchor}
}

// Selection is the selected range, ordered: for Char the first and the last
// cell in reading order, for Line the start of the first and the end of the
// last line, for Block the top left and the bottom right corner. Both ends
// are inclusive, and To covers both cells of a wide character.
func (m *Model) Selection() (from, to Pos, kind string, ok bool) {
	if m.sel == "" {
		return Pos{}, Pos{}, "", false
	}
	a, c := m.anchor, m.cursor
	switch m.sel {
	case Line:
		if c.Line < a.Line {
			a, c = c, a
		}
		return Pos{a.Line, 0}, Pos{c.Line, m.cols - 1}, Line, true
	case Block:
		from = Pos{min(a.Line, c.Line), min(a.Col, c.Col)}
		to = Pos{max(a.Line, c.Line), max(a.Col, c.Col)}
	default:
		if before(c, a) {
			a, c = c, a
		}
		from, to = a, c
	}
	if m.width(to) == 2 && to.Col+1 < m.cols {
		to.Col++
	}
	return from, to, m.sel, true
}

// Key handles one key of 5a.11.4. name, mod and text are those of
// input.key (4.2.6). A key copy mode has no use for yields nothing.
func (m *Model) Key(name string, mod int, text string) []Effect {
	if m.done {
		return nil
	}
	switch keyToken(name, mod, text) {
	case "h", "left":
		m.setCol(m.prevStart(m.cursor))
	case "l", "right":
		m.setCol(m.nextStart(m.cursor))
	case "j", "down":
		m.vertical(1)
	case "k", "up":
		m.vertical(-1)
	case "0", "home":
		m.setCol(0)
	case "$", "end":
		m.setCol(m.lineEnd(m.cursor.Line))
	case "w":
		m.moveTo(m.wordForward(false))
	case "e":
		m.moveTo(m.wordForward(true))
	case "b":
		m.moveTo(m.wordBack())
	case "g":
		m.cursor = Pos{m.first, 0}
		m.want = 0
		return m.scroll(m.first)
	case "G":
		m.cursor = Pos{m.last(), 0}
		m.want = 0
		return m.scroll(m.base)
	case "ctrl+u":
		return m.page(-m.half())
	case "ctrl+d":
		return m.page(m.half())
	case "ctrl+b", "pageup":
		return m.page(-m.rows)
	case "ctrl+f", "pagedown":
		return m.page(m.rows)
	case "v":
		m.toggle(Char)
	case "V":
		m.toggle(Line)
	case "ctrl+v":
		m.toggle(Block)
	case "y", "enter":
		return m.copyAndExit()
	case "q":
		return m.exit()
	case "escape", "esc":
		if m.sel != "" {
			m.sel = ""
			return nil
		}
		return m.exit()
	default:
		return nil
	}
	return m.reveal()
}

// Mouse handles one mouse event in the pane (5a.12): a press moves the
// cursor and clears the selection, a drag selects by character from the
// press (or from the cursor when no press was seen, as when a drag entered
// copy mode), a release after a drag copies and leaves, and the wheel
// scrolls. kind is one of the Mouse constants.
func (m *Model) Mouse(kind string, row, col int) []Effect {
	if m.done {
		return nil
	}
	row = clampInt(row, 0, m.rows-1)
	col = clampInt(col, 0, m.cols-1)
	at := m.clampPos(Pos{m.src.Top() + int64(row), col})
	switch kind {
	case MousePress:
		m.pressed = true
		m.sel = ""
		m.moveTo(at)
	case MouseDrag, "motion":
		if m.sel == "" {
			m.sel, m.anchor = Char, m.cursor
		}
		m.pressed = true
		m.moveTo(at)
	case MouseRelease:
		pressed := m.pressed
		m.pressed = false
		if pressed && m.sel != "" {
			return m.copyAndExit()
		}
		return nil
	case MouseWheelUp:
		return m.wheel(-WheelLines)
	case MouseWheelDown:
		return m.wheel(WheelLines)
	default:
		return nil
	}
	return m.reveal()
}

// Sync is called after every frame. It takes the size and the Range of
// src, clamps the cursor, the selection and the top to what src retains (a
// selection whose start was trimmed keeps what remains of it), and yields
// ScrollTo when the top it wants changed.
func (m *Model) Sync(src Source) []Effect {
	if m.done {
		return nil
	}
	m.read(src)
	old := m.top
	if m.adopt {
		m.adopt = false
		old = src.Top()
		m.top = src.Top()
	}
	m.top = clamp(m.top, m.first, m.base)
	if m.cursor.Line < m.first {
		m.cursor = Pos{m.first, 0}
	}
	m.cursor = m.clampPos(m.cursor)
	if m.sel != "" {
		if m.anchor.Line < m.first {
			col := 0
			if m.sel == Block {
				col = m.anchor.Col
			}
			m.anchor = Pos{m.first, col}
		}
		m.anchor = m.clampPos(m.anchor)
	}
	m.reveal()
	if m.top != old {
		return []Effect{{Kind: ScrollTo, Top: m.top}}
	}
	return nil
}

// Reflow takes the positions of event.reflow, in the order of Marks, after
// a resize renumbered the lines (3.2.4). A Line of -1, or a missing mark,
// means the text is gone and yields Exit. The next Sync takes the top the
// server moved with the text.
func (m *Model) Reflow(marks []Pos) []Effect {
	if m.done {
		return nil
	}
	want := 1
	if m.sel != "" {
		want = 2
	}
	if len(marks) < want {
		return m.exit()
	}
	for _, p := range marks[:want] {
		if p.Line < 0 {
			return m.exit()
		}
	}
	m.cursor = marks[0]
	m.want = m.cursor.Col
	if m.sel != "" {
		m.anchor = marks[1]
	}
	m.adopt = true
	return nil
}

// read takes size and range from src.
func (m *Model) read(src Source) {
	m.src = src
	m.cols, m.rows = src.Size()
	m.cols = max(m.cols, 1)
	m.rows = max(m.rows, 1)
	m.first, m.base = src.Range()
	if m.base < m.first {
		m.base = m.first
	}
}

func (m *Model) exit() []Effect {
	m.done = true
	m.sel = ""
	return []Effect{{Kind: Exit}}
}

// copyAndExit copies the selection, if there is one, and leaves.
func (m *Model) copyAndExit() []Effect {
	from, to, kind, ok := m.Selection()
	if !ok {
		return m.exit()
	}
	return append([]Effect{{Kind: Copy, From: from, To: to, Block: kind == Block}}, m.exit()...)
}

// toggle starts a selection of kind at the cursor, changes the kind of the
// one there is, or ends it when it already has that kind.
func (m *Model) toggle(kind string) {
	switch m.sel {
	case "":
		m.sel, m.anchor = kind, m.cursor
	case kind:
		m.sel = ""
	default:
		m.sel = kind
	}
}

// last is the newest line: the bottom row of the live screen.
func (m *Model) last() int64 { return m.base + int64(m.rows) - 1 }

func (m *Model) half() int { return max(m.rows/2, 1) }

// scroll sets the top and yields ScrollTo when it changed.
func (m *Model) scroll(top int64) []Effect {
	top = clamp(top, m.first, m.base)
	old := m.top
	m.top = top
	m.reveal()
	if m.top != old {
		return []Effect{{Kind: ScrollTo, Top: m.top}}
	}
	return nil
}

// reveal moves the top as little as possible so that the cursor's line is
// shown, and yields ScrollTo when the top moved.
func (m *Model) reveal() []Effect {
	old := m.top
	switch {
	case m.cursor.Line < m.top:
		m.top = m.cursor.Line
	case m.cursor.Line > m.top+int64(m.rows)-1:
		m.top = m.cursor.Line - int64(m.rows) + 1
	}
	m.top = clamp(m.top, m.first, m.base)
	if m.top != old {
		return []Effect{{Kind: ScrollTo, Top: m.top}}
	}
	return nil
}

// page moves the view and the cursor by delta lines.
func (m *Model) page(delta int) []Effect {
	m.cursor.Line = clamp(m.cursor.Line+int64(delta), m.first, m.last())
	m.cursor.Col = m.snap(m.cursor.Line, m.want)
	old := m.top
	m.top = clamp(m.top+int64(delta), m.first, m.base)
	m.reveal()
	if m.top != old {
		return []Effect{{Kind: ScrollTo, Top: m.top}}
	}
	return nil
}

// wheel scrolls the view by delta lines and keeps the cursor inside it.
func (m *Model) wheel(delta int) []Effect {
	old := m.top
	m.top = clamp(m.top+int64(delta), m.first, m.base)
	bottom := m.top + int64(m.rows) - 1
	if l := clamp(m.cursor.Line, m.top, bottom); l != m.cursor.Line {
		m.cursor.Line = l
		m.cursor.Col = m.snap(l, m.want)
	}
	if m.top != old {
		return []Effect{{Kind: ScrollTo, Top: m.top}}
	}
	return nil
}

func (m *Model) vertical(delta int64) {
	l := clamp(m.cursor.Line+delta, m.first, m.last())
	m.cursor = Pos{l, m.snap(l, m.want)}
}

func (m *Model) setCol(col int) {
	m.cursor.Col = m.snap(m.cursor.Line, col)
	m.want = m.cursor.Col
}

func (m *Model) moveTo(p Pos) {
	m.cursor = m.clampPos(p)
	m.want = m.cursor.Col
}

// clampPos keeps p inside the retained lines and the columns, on the first
// cell of a character.
func (m *Model) clampPos(p Pos) Pos {
	p.Line = clamp(p.Line, m.first, m.last())
	p.Col = m.snap(p.Line, p.Col)
	return p
}

// keyToken names a key for the table of 5a.11.4: the text it produces when
// only shift is held, "ctrl+" and the name with ctrl, the name otherwise.
// Keys with alt or super have no meaning here.
func keyToken(name string, mod int, text string) string {
	if mod&(modAlt|modSuper) != 0 {
		return ""
	}
	if mod&modCtrl != 0 {
		return "ctrl+" + lower(name)
	}
	if text != "" && text != " " {
		return text
	}
	if mod&modShift != 0 && len(name) == 1 && name[0] >= 'a' && name[0] <= 'z' {
		return string(name[0] - 'a' + 'A')
	}
	return name
}

func lower(s string) string {
	if len(s) == 1 && s[0] >= 'A' && s[0] <= 'Z' {
		return string(s[0] - 'A' + 'a')
	}
	return s
}

func before(a, b Pos) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col
}

func clamp(v, lo, hi int64) int64 {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}
