package client

// Hosting the hub overlay (3.2.4, 5.3.5, 5a.10.2): it is opened by the hub
// actions, by a routed ui.hub (fleet update OpenHub) and at the start in a
// plain terminal; it lies at its Bounds inside the pane area and covers the
// panes without resizing them; it gets the keys, the paste, the mouse in
// its own cells, its size and the theme; the terminal cursor is its cursor;
// an Outcome closes it and, with a choice, switches. Closing it while no
// session is displayed ends the client, because there is nothing to show
// under it.

import (
	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/screen"
	"devtools/internal/fleet"
	"devtools/internal/session"
)

// here is the machine and directory of what is displayed, for the hub.
func (m *Model) here() session.Target {
	if m.view == "none" {
		return session.Target{}
	}
	return session.Target{Machine: m.target.Machine, Dir: m.ok.Dir}
}

// openHub opens the hub overlay, or replaces an open one; a running switch
// is dropped (3.5.4: opening the hub is a client action), and so are a
// pending prefix and the confirm.
func (m *Model) openHub(req fleet.HubRequest) tea.Cmd {
	if m.o.OpenHub == nil {
		return nil
	}
	if m.summary.Mode == "switching" {
		m.fleet.Cancel()
	}
	m.prefix = false
	m.keys.Reset()
	m.confirm = nil
	m.press = nil
	// an open hub is replaced: the fleet ends its feed when the new one opens
	events := m.fleet.HubOpened()
	m.overlay = m.o.OpenHub(req, events, m.theme)
	if m.overlay == nil {
		m.fleet.HubClosed()
		return nil
	}
	m.comp.full = true
	return tea.Batch(m.overlay.Init(), m.sizeOverlay())
}

// overlayBounds is where the overlay lies inside the pane area, clipped to
// it.
func (m *Model) overlayBounds() screen.Rect {
	w, h := m.paneSize()
	r := m.overlay.Bounds(w, h)
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, w), min(r.Y+r.H, h)
	if x1 <= x0 || y1 <= y0 {
		return screen.Rect{}
	}
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// sizeOverlay tells the overlay its size.
func (m *Model) sizeOverlay() tea.Cmd {
	if m.overlay == nil || !m.sized {
		return nil
	}
	r := m.overlayBounds()
	return m.toOverlay(tea.WindowSizeMsg{Width: r.W, Height: r.H})
}

// toOverlay hands a message to the open overlay and acts on its outcome:
// a choice is shown, and closing it with no session displayed ends the
// client (5a.10.2).
func (m *Model) toOverlay(msg tea.Msg) tea.Cmd {
	if m.overlay == nil {
		return nil
	}
	cmd, out := m.overlay.Update(msg)
	if out == nil {
		return cmd
	}
	m.overlay = nil
	m.fleet.HubClosed()
	m.comp.full = true
	if out.Switch != nil {
		m.fleet.Switch(*out.Switch)
		return cmd
	}
	if m.view == "none" {
		return tea.Batch(cmd, m.quit())
	}
	return cmd
}

// drawOverlay draws the overlay over the pane area pc: its rectangle is
// blanked first, so nothing of the panes shows through it.
func (m *Model) drawOverlay(pc *screen.Canvas) {
	oc := pc.Sub(m.overlayBounds())
	oc.Clear()
	m.overlay.Draw(oc)
}
