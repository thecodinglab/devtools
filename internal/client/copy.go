package client

// Copy mode (3.2.4, 5a.10.1, 5a.11.4), wired to the pure model of
// internal/client/copymode: prefix+[ or a drag in a pane whose program does
// not read the mouse enters it; every move sends the copy cursor and the
// selection anchor as `marks` of pane.scroll, so the server's pane follows
// those lines through a reflow and event.reflow brings their new numbers
// back (4.2.7); a copy asks for the text with pane.text and writes it with
// OSC 52 (clipboard.go). A mark whose text is gone, a view that turns stale
// and a switch end it.

import (
	"slices"
	"strconv"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/chrome"
	"devtools/internal/client/copymode"
	"devtools/internal/client/keys"
	"devtools/internal/client/panes"
	"devtools/internal/client/screen"
	"devtools/internal/proto"
	"devtools/internal/ui"
)

// copyState is copy mode in one pane.
type copyState struct {
	pane string
	m    *copymode.Model

	// what the last pane.scroll said, so that only a change is sent
	sent      bool
	sentTop   int64
	sentMarks []copymode.Pos
	// an event.reflow moved the marks: the model takes the server's top
	// with the next frame of the pane, and nothing is sent before it
	adopting bool
}

// enterCopy starts copy mode in the focused pane, or in the scrolled pane
// at its position (5a.10.1: prefix+[ in the scrolled view). It cannot start
// while the view is stale or a switch runs.
func (m *Model) enterCopy() tea.Cmd {
	if m.copy != nil || m.view != "showing" || m.pending {
		return nil
	}
	pane := m.focusedPane()
	if m.scroll != nil {
		pane = m.scroll.pane
	}
	sc := m.panes.Screen(pane)
	if sc == nil {
		return nil
	}
	cols, rows := sc.Size()
	if cols <= 0 || rows <= 0 {
		return nil
	}
	_, base := sc.Range()
	at := copymode.Pos{Line: base + int64(sc.Cursor().Y), Col: sc.Cursor().X}
	if m.scroll != nil {
		at = copymode.Pos{Line: m.scroll.top + int64(rows) - 1}
	}
	m.startCopy(pane, sc, at)
	return nil
}

// startCopy makes copy mode in pane with its cursor at at, taking over the
// scrolled view, and sends its first pane.scroll.
func (m *Model) startCopy(pane string, sc *panes.Screen, at copymode.Pos) {
	if m.scroll != nil && m.scroll.pane != pane {
		m.toLive(m.scroll.pane)
	}
	m.scroll = nil
	m.copy = &copyState{pane: pane, m: copymode.Enter(sc, at)}
	m.comp.full = true
	m.copySync()
}

// exitCopy ends copy mode; live tells the server that the pane is shown
// live again (it is not told while the view is stale or a switch runs:
// nothing reaches it then).
func (m *Model) exitCopy(live bool) {
	c := m.copy
	if c == nil {
		return
	}
	m.copy = nil
	m.comp.full = true
	if m.press != nil && m.press.pane == c.pane {
		m.press = nil
	}
	if live {
		m.toLive(c.pane)
	}
}

// copyKey hands a key to copy mode.
func (m *Model) copyKey(ev keys.Event) tea.Cmd {
	return m.copyEffects(m.copy.m.Key(ev.Name, ev.Mod, ev.Text))
}

// copyEffects carries out what the model asked for, then sends the new
// position and marks when they changed.
func (m *Model) copyEffects(effs []copymode.Effect) tea.Cmd {
	c := m.copy
	if c == nil {
		return nil
	}
	var cmds []tea.Cmd
	exit := false
	for _, e := range effs {
		switch e.Kind {
		case copymode.Copy:
			cmds = append(cmds, m.request(proto.PaneText{
				Pane:  c.pane,
				From:  proto.Pos{Line: e.From.Line, Col: e.From.Col},
				To:    proto.Pos{Line: e.To.Line, Col: e.To.Col},
				Block: e.Block,
			}))
		case copymode.Exit:
			exit = true
		}
	}
	if exit || c.m.Done() {
		m.exitCopy(true)
		return tea.Batch(cmds...)
	}
	m.copySync()
	return tea.Batch(cmds...)
}

// copySync sends pane.scroll with the model's top and marks when either
// changed since the last one (3.2.4: with every move).
func (m *Model) copySync() {
	c := m.copy
	if c == nil || c.adopting || m.view != "showing" {
		return
	}
	top := c.m.Top()
	marks := c.m.Marks()
	if c.sent && top == c.sentTop && slices.Equal(marks, c.sentMarks) {
		return
	}
	c.sent, c.sentTop, c.sentMarks = true, top, slices.Clone(marks)
	ps := proto.PaneScroll{Pane: c.pane, Top: &top}
	for _, p := range marks {
		ps.Marks = append(ps.Marks, proto.Pos{Line: p.Line, Col: p.Col})
	}
	m.fleet.Input(proto.Encode(ps))
}

// copied is the reply of pane.text: the text goes to the clipboard and the
// notice counts its characters. An empty text writes nothing.
func (m *Model) copied(msg proto.Msg) tea.Cmd {
	var r proto.PaneTextResult
	if msg.Decode(&r) != nil || r.Text == "" {
		return nil
	}
	n := utf8.RuneCountInString(r.Text)
	return tea.Batch(m.writeClipboard(r.Text), m.setNotice(ui.Notice("copied", strconv.Itoa(n))))
}

// afterFrame is called after a frame of pane was applied: copy mode and the
// scrolled view follow what the pane now retains.
func (m *Model) afterFrame(pane string) tea.Cmd {
	m.scrollFrame(pane)
	c := m.copy
	if c == nil || c.pane != pane {
		return nil
	}
	sc := m.panes.Screen(pane)
	if sc == nil {
		m.exitCopy(false)
		return nil
	}
	if cols, rows := sc.Size(); cols <= 0 || rows <= 0 {
		return nil
	}
	effs := c.m.Sync(sc)
	if c.adopting {
		// the top the server moved with the text is the model's now
		c.adopting = false
		c.sentTop = c.m.Top()
	}
	return m.copyEffects(effs)
}

// reflow is event.reflow (4.2.7): the new numbers of the scrolled position
// and of the marks after a resize renumbered the lines. A mark whose text
// is gone ends copy mode.
func (m *Model) reflow(msg proto.Msg) tea.Cmd {
	var r proto.Reflow
	if msg.Decode(&r) != nil || m.view != "showing" {
		return nil
	}
	m.scrollReflow(r)
	c := m.copy
	if c == nil || c.pane != r.Pane {
		return nil
	}
	marks := make([]copymode.Pos, 0, len(r.Marks))
	for _, p := range r.Marks {
		marks = append(marks, copymode.Pos{Line: p.Line, Col: p.Col})
	}
	effs := c.m.Reflow(marks)
	if c.m.Done() {
		return m.copyEffects(effs)
	}
	// the server holds these marks already
	c.adopting = true
	c.sent = r.Top != nil
	if r.Top != nil {
		c.sentTop = *r.Top
	}
	c.sentMarks = slices.Clone(c.m.Marks())
	return nil
}

// endView ends what belongs to the view on screen: copy mode, the scrolled
// view and a held press. Nothing is sent: the view is stale, or another one
// replaced it.
func (m *Model) endView() {
	if m.copy != nil {
		m.copy = nil
		m.comp.full = true
	}
	m.scroll = nil
	m.press = nil
}

// followTab ends copy mode and the scrolled view of a pane that is no
// longer displayed.
func (m *Model) followTab() {
	if m.copy != nil && m.panes.Screen(m.copy.pane) == nil {
		m.exitCopy(false)
	}
	if m.scroll != nil && m.panes.Screen(m.scroll.pane) == nil {
		m.scroll = nil
	}
}

// drawCopy draws the selection and the copy cursor over the copy pane
// (5.3.5: Chrome.Selection and Chrome.CopyCursor). pc is the pane area.
func (m *Model) drawCopy(pc *screen.Canvas, layout panes.Layout, st chrome.State) {
	c := m.copy
	if c == nil {
		return
	}
	r, ok := layout.Panes[c.pane]
	sc := m.panes.Screen(c.pane)
	if !ok || sc == nil || r.W == 0 || r.H == 0 {
		return
	}
	top := sc.Top()
	if from, to, kind, ok := c.m.Selection(); ok {
		for y := 0; y < r.H; y++ {
			a, b, ok := selected(top+int64(y), from, to, kind, r.W)
			if !ok {
				continue
			}
			pc.Restyle(screen.Rect{X: r.X + a, Y: r.Y + y, W: b - a + 1, H: 1},
				func(s screen.Style) screen.Style { return m.chrome.Selection(st, s) })
		}
	}
	cur := c.m.Cursor()
	if y := cur.Line - top; y >= 0 && y < int64(r.H) && cur.Col >= 0 && cur.Col < r.W {
		pc.Restyle(screen.Rect{X: r.X + cur.Col, Y: r.Y + int(y), W: 1, H: 1},
			func(s screen.Style) screen.Style { return m.chrome.CopyCursor(st, s) })
	}
}

// selected is the range of columns of line that a selection covers, both
// inclusive, inside a pane of w columns.
func selected(line int64, from, to copymode.Pos, kind string, w int) (a, b int, ok bool) {
	if line < from.Line || line > to.Line {
		return 0, 0, false
	}
	switch kind {
	case copymode.Line:
		a, b = 0, w-1
	case copymode.Block:
		a, b = from.Col, to.Col
	default:
		a, b = 0, w-1
		if line == from.Line {
			a = from.Col
		}
		if line == to.Line {
			b = to.Col
		}
	}
	a, b = max(a, 0), min(b, w-1)
	return a, b, a <= b
}
