package client

// Composition (3.2.4): panes, chrome and overlay drawn onto the one canvas
// in painter's order, incrementally. A full redraw happens only when the
// layout, the mode, the view, the overlay or the window size changed;
// otherwise only the pane rows that changed are copied and the status row
// is drawn again only when chrome.State changed (5a.16.2). The canvas is
// rendered into tea.View.Content; bubbletea writes what differs.

import (
	"reflect"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/chrome"
	"devtools/internal/client/panes"
	"devtools/internal/client/screen"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// composition is what the last View drew.
type composition struct {
	full      bool // the next composition draws everything
	have      bool // something was drawn
	w, h      int
	small     bool
	state     chrome.State
	layout    panes.Layout
	overlayOn bool
	divider   dividerKey // what the dividers were last drawn from

	refreshes int       // state reads asked for
	refreshed time.Time // when the last one was asked for
	again     bool      // a read was asked for while one ran

	statusDraws int // how often the status row was drawn; read by tests
}

// View composes what changed and returns it to bubbletea.
func (m *Model) View() tea.View {
	m.compose()
	v := tea.NewView(m.canvas.Render())
	v.AltScreen = true
	v.ReportFocus = true
	v.WindowTitle = m.title()
	v.Cursor = m.cursor()
	v.KeyboardEnhancements = m.enhancements()
	v.MouseMode = m.mouseMode()
	return v
}

// title is the outer window's title (3.2.4): the label of the displayed
// session, after "{machine}: " for another machine's; "devtools" with no
// view. Pane titles never reach it.
func (m *Model) title() string {
	if m.view == "none" {
		return "devtools"
	}
	label := m.sessionLabel()
	if label == "" {
		return "devtools"
	}
	if m.target.Machine != "" {
		return session.Sanitize(m.target.Machine, session.MaxLabel) + ": " + label
	}
	return label
}

// enhancements are the kitty keyboard flags asked of the outer terminal:
// those of the focused pane (4.2.4 modes.keys, 3.2.4), and none while the
// hub, copy mode or a confirm is open or no pane is shown.
func (m *Model) enhancements() tea.KeyboardEnhancements {
	if m.overlay != nil || m.copy != nil || m.confirm != nil || m.view == "none" || m.tooSmall() {
		return tea.KeyboardEnhancements{}
	}
	sc := m.panes.Screen(m.focusedPane())
	if sc == nil {
		return tea.KeyboardEnhancements{}
	}
	k := sc.Modes().Keys
	return tea.KeyboardEnhancements{
		ReportEventTypes:           k&2 != 0,
		ReportAlternateKeys:        k&4 != 0,
		ReportAllKeysAsEscapeCodes: k&8 != 0,
		ReportAssociatedText:       k&16 != 0,
	}
}

// cursor is the terminal cursor: the overlay's while the hub is open,
// hidden in copy mode, else the focused pane's offset by the pane's
// rectangle, hidden when its cell is clipped away.
func (m *Model) cursor() *tea.Cursor {
	if m.tooSmall() || !m.sized {
		return nil
	}
	if m.overlay != nil {
		x, y, ok := m.overlay.Cursor()
		if !ok {
			return nil
		}
		r := m.overlayBounds()
		if x < 0 || y < 0 || x >= r.W || y >= r.H {
			return nil
		}
		return tea.NewCursor(r.X+x, r.Y+y)
	}
	if m.copy != nil || m.view != "showing" || m.confirm != nil || m.pending {
		return nil
	}
	id := m.focusedPane()
	sc := m.panes.Screen(id)
	if sc == nil {
		return nil
	}
	c := sc.Cursor()
	if !c.Visible {
		return nil
	}
	r, ok := m.comp.layout.Panes[id]
	if !ok || c.X < 0 || c.Y < 0 || c.X >= r.W || c.Y >= r.H {
		return nil
	}
	cur := tea.NewCursor(r.X+c.X, r.Y+c.Y)
	switch c.Shape {
	case "underline":
		cur.Shape = tea.CursorUnderline
	case "bar":
		cur.Shape = tea.CursorBar
	}
	cur.Blink = c.Blink
	return cur
}

// compose draws onto the canvas what changed since the last composition.
func (m *Model) compose() {
	if !m.sized {
		return
	}
	c := &m.comp
	st := m.chromeState()
	if m.tooSmall() {
		if !c.small || c.w != m.w || c.h != m.h || !reflect.DeepEqual(st, c.state) {
			m.canvas.Resize(m.w, m.h)
			m.chrome.TooSmall(m.canvas, st)
		}
		c.small, c.w, c.h, c.state, c.have = true, m.w, m.h, st, true
		c.full = true
		return
	}
	area := m.paneArea()
	var layout panes.Layout
	if m.view != "none" {
		layout = m.panes.Layout(area)
	}
	full := c.full || !c.have || c.small || c.w != m.w || c.h != m.h ||
		st.Mode != c.state.Mode || st.View != c.state.View ||
		c.overlayOn != (m.overlay != nil) || !reflect.DeepEqual(layout, c.layout)
	if full {
		if cw, ch := m.canvas.Size(); cw != m.w || ch != m.h {
			m.canvas.Resize(m.w, m.h)
		}
		m.canvas.Clear()
	}
	pc := m.canvas.Sub(area)
	switch {
	case m.overlay != nil:
		m.drawOverlay(pc)
	case m.view != "none":
		m.drawPanes(pc, area, layout, st, full)
	}
	if full || !reflect.DeepEqual(st, c.state) {
		sr := m.chrome.StatusRows()
		sc := m.canvas.Sub(screen.Rect{X: 0, Y: m.h - sr, W: m.w, H: sr})
		sc.Clear()
		m.chrome.Status(sc, st)
		c.statusDraws++
	}
	c.full, c.have, c.small = false, true, false
	c.w, c.h, c.state, c.layout = m.w, m.h, st, layout
	c.overlayOn = m.overlay != nil
}

// drawPanes draws the panes of the displayed tab, and on a full redraw the
// divider, the pad and the banner. pc is the pane area; layout is in its
// coordinates (its origin is the canvas origin). A stale view, and the old
// view while a switch is pending (3.5.4 step 3, F14), is drawn inactive;
// only the stale one has the banner. Copy mode draws its selection and
// cursor over its pane, so its pane is drawn whole each time.
func (m *Model) drawPanes(pc *screen.Canvas, area screen.Rect, layout panes.Layout, st chrome.State, full bool) {
	inactive := m.view == "stale" || m.pending
	m.panes.Draw(pc, layout, full || inactive || m.copy != nil)
	for id, r := range layout.Panes {
		if p, ok := m.pane(id); ok && p.State == proto.PaneExited && r.W > 0 && r.H > 0 {
			m.chrome.PaneMessage(pc.Sub(r), st, exitText(p))
		}
	}
	// A divider's pointer shows which pane has the keys and its style
	// whether the window has the terminal's focus (5a.6.5); neither changes
	// the layout, so the dividers are drawn again whenever they change too.
	// Only a divider the focused pane touches points.
	if len(layout.Dividers) > 0 {
		focused, has := layout.Panes[m.focusedPane()]
		k := dividerKey{pointers: make([]chrome.Pointer, len(layout.Dividers)), windowFocused: st.WindowFocused}
		for i, d := range layout.Dividers {
			if has {
				k.pointers[i] = pointerOf(d, focused)
			}
		}
		if full || inactive || !k.equal(m.comp.divider) {
			for i, d := range layout.Dividers {
				m.chrome.Divider(pc.Sub(d.Rect), st, d.Vertical, k.pointers[i])
			}
		}
		m.comp.divider = k
	}
	if full || inactive {
		for _, r := range layout.Pad {
			m.chrome.Pad(pc.Sub(r), st)
		}
	}
	if inactive {
		for _, r := range layout.Panes {
			pc.Restyle(r, func(s screen.Style) screen.Style { return m.chrome.Inactive(st, s) })
		}
		if m.view == "stale" {
			m.chrome.Banner(pc, st)
		}
		return
	}
	m.drawCopy(pc, layout, st)
}

// dividerKey is what the dividers are drawn from besides the layout.
type dividerKey struct {
	pointers      []chrome.Pointer // one per divider of the layout
	windowFocused bool
}

func (k dividerKey) equal(o dividerKey) bool {
	return k.windowFocused == o.windowFocused && slices.Equal(k.pointers, o.pointers)
}

// pointerOf is the pointer of divider d when the focused pane lies at f: in
// the middle of the cells the two share along the divider, towards f, when f
// touches d; else none.
func pointerOf(d panes.Divider, f screen.Rect) chrome.Pointer {
	r := d.Rect
	if f.W == 0 || f.H == 0 {
		return chrome.Pointer{}
	}
	var toward, lo, hi, base int
	if d.Vertical {
		switch {
		case f.X+f.W == r.X:
			toward = chrome.PointBefore
		case f.X == r.X+r.W:
			toward = chrome.PointAfter
		}
		lo, hi, base = max(f.Y, r.Y), min(f.Y+f.H, r.Y+r.H), r.Y
	} else {
		switch {
		case f.Y+f.H == r.Y:
			toward = chrome.PointBefore
		case f.Y == r.Y+r.H:
			toward = chrome.PointAfter
		}
		lo, hi, base = max(f.X, r.X), min(f.X+f.W, r.X+r.W), r.X
	}
	if toward == chrome.PointNone || hi <= lo {
		return chrome.Pointer{}
	}
	return chrome.Pointer{Toward: toward, At: (lo+hi)/2 - base}
}

// exitText is the first line of the pane message of an ended program
// (5a.6.6).
func exitText(p proto.Pane) string {
	program := programName(session.Sanitize(p.Program, session.MaxLabel))
	if p.Exit == nil {
		return program + " ended."
	}
	if p.Exit.Text != "" {
		return session.Sanitize(p.Exit.Text, session.MaxText)
	}
	if p.Exit.Signal != "" {
		return program + " was killed (" + session.Sanitize(p.Exit.Signal, session.MaxLabel) + ")."
	}
	return program + " ended."
}
