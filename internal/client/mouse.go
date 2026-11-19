package client

// The mouse (5a.12, 3.2.4). The status row, the divider, the pad and the
// banner have no hit region: an event there does nothing (F9). In a pane a
// press focuses that pane; a program that asked for the mouse (frame
// modes.mouse, 4.2.4) gets the events in its own coordinates, motion
// without a button only in mode "all"; in a pane whose program does not
// read the mouse a drag selects by character and the release copies; the
// wheel becomes arrow keys in the alternate screen and scrolls history
// otherwise. The hub reads no mouse; it is handed what falls inside it.

import (
	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/copymode"
	"devtools/internal/client/panes"
	"devtools/internal/proto"
)

// pressState is a button held since a press in a pane.
type pressState struct {
	pane    string
	x, y    int  // the press, in the pane's cells
	forward bool // the program reads the mouse: it gets the release and the drag
	copy    bool // the drag selects in copy mode
}

// mouse is a mouse event of the outer terminal, in window cells.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	mo := msg.Mouse()
	if m.overlay != nil {
		return m.overlayMouse(msg, mo)
	}
	if m.tooSmall() || m.view != "showing" || m.pending || m.confirm != nil {
		m.press = nil
		return nil
	}
	layout := m.panes.Layout(m.paneArea())
	switch msg.(type) {
	case tea.MouseClickMsg:
		return m.mousePress(mo, layout)
	case tea.MouseMotionMsg:
		return m.mouseMotion(mo, layout)
	case tea.MouseReleaseMsg:
		return m.mouseRelease(mo, layout)
	case tea.MouseWheelMsg:
		return m.mouseWheel(mo, layout)
	}
	return nil
}

// mousePress focuses the pane under the pointer and starts what the press
// can become.
func (m *Model) mousePress(mo tea.Mouse, layout panes.Layout) tea.Cmd {
	pane, px, py, ok := layout.Hit(mo.X, mo.Y)
	if !ok {
		m.press = nil
		return nil
	}
	if c := m.copy; c != nil {
		if c.pane == pane && mo.Button == tea.MouseLeft {
			m.press = &pressState{pane: pane, x: px, y: py, copy: true}
			return m.copyEffects(c.m.Mouse(copymode.MousePress, py, px))
		}
		m.exitCopy(true)
	}
	var cmd tea.Cmd
	if pane != m.focusedPane() {
		cmd = m.request(proto.PaneFocus{Pane: pane})
	}
	sc := m.panes.Screen(pane)
	switch {
	case sc.Modes().Mouse != "":
		m.press = &pressState{pane: pane, x: px, y: py, forward: true}
		m.sendMouse(pane, "press", buttonName(mo.Button), px, py, mo.Mod)
	case mo.Button == tea.MouseLeft:
		m.press = &pressState{pane: pane, x: px, y: py}
	default:
		m.press = nil
	}
	return cmd
}

// mouseMotion is a motion: a drag of a held press, or, without a button, a
// motion for a program in mode "all".
func (m *Model) mouseMotion(mo tea.Mouse, layout panes.Layout) tea.Cmd {
	p := m.press
	if p == nil || mo.Button == tea.MouseNone {
		pane, px, py, ok := layout.Hit(mo.X, mo.Y)
		if !ok {
			return nil
		}
		if sc := m.panes.Screen(pane); sc != nil && sc.Modes().Mouse == "all" {
			m.sendMouse(pane, "motion", buttonName(mo.Button), px, py, mo.Mod)
		}
		return nil
	}
	r, ok := layout.Panes[p.pane]
	sc := m.panes.Screen(p.pane)
	if !ok || sc == nil {
		m.press = nil
		return nil
	}
	px, py := mo.X-r.X, mo.Y-r.Y
	switch {
	case p.forward:
		if mode := sc.Modes().Mouse; (mode == "drag" || mode == "all") && r.Contains(mo.X, mo.Y) {
			m.sendMouse(p.pane, "motion", buttonName(mo.Button), px, py, mo.Mod)
		}
		return nil
	case p.copy:
		if m.copy == nil || m.copy.pane != p.pane {
			m.press = nil
			return nil
		}
		return m.copyEffects(m.copy.m.Mouse(copymode.MouseDrag, py, px))
	}
	// the first drag in a pane whose program does not read the mouse
	// enters copy mode at the press
	if px == p.x && py == p.y {
		return nil
	}
	if m.copy == nil {
		cols, rows := sc.Size()
		if cols <= 0 || rows <= 0 || m.pending || m.view != "showing" {
			return nil
		}
		m.startCopy(p.pane, sc, copymode.Pos{Line: sc.Top() + int64(p.y), Col: p.x})
	}
	p.copy = true
	effs := m.copy.m.Mouse(copymode.MousePress, p.y, p.x)
	effs = append(effs, m.copy.m.Mouse(copymode.MouseDrag, py, px)...)
	return m.copyEffects(effs)
}

// mouseRelease ends a held press: the program gets the release, or the
// selection of a drag is copied.
func (m *Model) mouseRelease(mo tea.Mouse, layout panes.Layout) tea.Cmd {
	p := m.press
	m.press = nil
	if p == nil {
		return nil
	}
	r, ok := layout.Panes[p.pane]
	if !ok || r.W == 0 || r.H == 0 {
		return nil
	}
	px := min(max(mo.X-r.X, 0), r.W-1)
	py := min(max(mo.Y-r.Y, 0), r.H-1)
	switch {
	case p.forward:
		m.sendMouse(p.pane, "release", buttonName(mo.Button), px, py, mo.Mod)
	case p.copy && m.copy != nil && m.copy.pane == p.pane:
		return m.copyEffects(m.copy.m.Mouse(copymode.MouseRelease, py, px))
	}
	return nil
}

// mouseWheel is one notch of the wheel over a pane.
func (m *Model) mouseWheel(mo tea.Mouse, layout panes.Layout) tea.Cmd {
	pane, px, py, ok := layout.Hit(mo.X, mo.Y)
	if !ok {
		return nil
	}
	up, down := mo.Button == tea.MouseWheelUp, mo.Button == tea.MouseWheelDown
	if c := m.copy; c != nil {
		if c.pane != pane {
			return nil
		}
		switch {
		case up:
			return m.copyEffects(c.m.Mouse(copymode.MouseWheelUp, py, px))
		case down:
			return m.copyEffects(c.m.Mouse(copymode.MouseWheelDown, py, px))
		}
		return nil
	}
	sc := m.panes.Screen(pane)
	switch {
	case sc.Modes().Mouse != "":
		m.sendMouse(pane, "wheel", buttonName(mo.Button), px, py, mo.Mod)
	case sc.Modes().Alt:
		// the alternate screen has no history: the wheel is arrow keys
		key := ""
		switch {
		case up:
			key = "up"
		case down:
			key = "down"
		}
		if key == "" {
			return nil
		}
		for range copymode.WheelLines {
			m.fleet.Input(proto.Encode(proto.Key{Pane: pane, Key: key}))
		}
	case up:
		m.wheelScroll(pane, -copymode.WheelLines)
	case down:
		m.wheelScroll(pane, copymode.WheelLines)
	}
	return nil
}

// sendMouse sends input.mouse to pane, in the pane's cells (4.2.6).
func (m *Model) sendMouse(pane, kind, button string, x, y int, mod tea.KeyMod) {
	m.fleet.Input(proto.Encode(proto.Mouse{Pane: pane, Kind: kind, Button: button, X: x, Y: y, Mod: wireMod(mod)}))
}

// buttonName is a button as input.mouse names it.
func buttonName(b tea.MouseButton) string {
	switch b {
	case tea.MouseLeft:
		return "left"
	case tea.MouseMiddle:
		return "middle"
	case tea.MouseRight:
		return "right"
	case tea.MouseWheelUp:
		return "up"
	case tea.MouseWheelDown:
		return "down"
	case tea.MouseWheelLeft:
		return "left-wheel"
	case tea.MouseWheelRight:
		return "right-wheel"
	}
	return "none"
}

// wireMod is a modifier set in the kitty order of the wire.
func wireMod(mod tea.KeyMod) int {
	out := 0
	for _, b := range modBits {
		if mod&b.tea != 0 {
			out |= b.wire
		}
	}
	return out
}

// mouseMode is what the outer terminal is asked to report: every motion
// when a displayed program asked for mode "all", else presses, releases,
// drags and the wheel; nothing while nothing is shown.
func (m *Model) mouseMode() tea.MouseMode {
	if m.overlay == nil && m.view == "none" {
		return tea.MouseModeNone
	}
	if m.overlay == nil && m.view == "showing" {
		t, _ := m.shownTab()
		for _, id := range t.Panes {
			if sc := m.panes.Screen(id); sc != nil && sc.Modes().Mouse == "all" {
				return tea.MouseModeAllMotion
			}
		}
	}
	return tea.MouseModeCellMotion
}

// overlayMouse hands the hub an event inside its bounds, in its own cells.
func (m *Model) overlayMouse(msg tea.MouseMsg, mo tea.Mouse) tea.Cmd {
	if m.tooSmall() {
		return nil
	}
	r := m.overlayBounds()
	if !r.Contains(mo.X, mo.Y) {
		return nil
	}
	mo.X, mo.Y = mo.X-r.X, mo.Y-r.Y
	switch msg.(type) {
	case tea.MouseClickMsg:
		return m.toOverlay(tea.MouseClickMsg(mo))
	case tea.MouseReleaseMsg:
		return m.toOverlay(tea.MouseReleaseMsg(mo))
	case tea.MouseMotionMsg:
		return m.toOverlay(tea.MouseMotionMsg(mo))
	case tea.MouseWheelMsg:
		return m.toOverlay(tea.MouseWheelMsg(mo))
	}
	return nil
}
