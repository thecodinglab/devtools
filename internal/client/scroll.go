package client

// The scrolled view (3.2.4, 5a.10.1): the wheel in a pane on the primary
// screen whose program does not read the mouse scrolls that pane's history.
// Scrolling is per client: the client sends pane.scroll with the absolute
// line it wants in row 0, the server clamps it and sends frames from there
// (4.2.7). Any key that goes to the pane returns the view to the live
// bottom first; event.reflow moves the position with its text.

import (
	"devtools/internal/client/chrome"
	"devtools/internal/proto"
)

// scrollState is the scrolled view of one pane.
type scrollState struct {
	pane string
	top  int64 // the absolute line in row 0, below base
}

// wheelScroll scrolls the history of pane by delta lines (negative: towards
// older lines). Reaching the live bottom ends the scrolled view.
func (m *Model) wheelScroll(pane string, delta int) {
	if m.view != "showing" || m.pending {
		return // a stale view cannot start it (5a.10.1)
	}
	sc := m.panes.Screen(pane)
	if sc == nil {
		return
	}
	first, base := sc.Range()
	top := base
	if m.scroll != nil && m.scroll.pane == pane {
		top = m.scroll.top
	} else if m.scroll != nil {
		// another pane was scrolled: it returns to live
		m.toLive(m.scroll.pane)
		m.scroll = nil
	}
	top = min(max(top+int64(delta), first), base)
	if top >= base {
		if m.scroll != nil {
			m.scroll = nil
			m.toLive(pane)
		}
		return
	}
	if m.scroll != nil && m.scroll.top == top {
		return
	}
	m.scroll = &scrollState{pane: pane, top: top}
	t := top
	m.fleet.Input(proto.Encode(proto.PaneScroll{Pane: pane, Top: &t}))
}

// toLive tells the displayed server that this client shows pane live again.
func (m *Model) toLive(pane string) {
	if m.view != "showing" || pane == "" {
		return
	}
	m.fleet.Input(proto.Encode(proto.PaneScroll{Pane: pane}))
}

// endScroll returns a scrolled view to live before a key or paste for pane
// is delivered (4.2.7: the server ends it with the input as well; the
// client says so first, so that the order is plain on the wire, W6).
func (m *Model) endScroll() {
	if m.scroll == nil {
		return
	}
	pane := m.scroll.pane
	m.scroll = nil
	m.toLive(pane)
}

// scrollFrame keeps the scrolled view inside what the pane retains after a
// frame: a position below first moves up to it, and one at base is live.
func (m *Model) scrollFrame(pane string) {
	s := m.scroll
	if s == nil || s.pane != pane {
		return
	}
	sc := m.panes.Screen(pane)
	if sc == nil {
		m.scroll = nil
		return
	}
	first, base := sc.Range()
	if s.top < first {
		s.top = first
	}
	if s.top >= base {
		m.scroll = nil
	}
}

// scrollReflow takes the position event.reflow names (4.2.7): null is live.
func (m *Model) scrollReflow(r proto.Reflow) {
	s := m.scroll
	if s == nil || s.pane != r.Pane {
		return
	}
	if r.Top == nil {
		m.scroll = nil
		return
	}
	s.top = *r.Top
}

// copyIndicator is the position the copy and scrolled rows show (5a.6.3):
// the line of the copy cursor, or the line in row 0 of a scrolled view,
// counted from the oldest retained line, of every line the pane holds.
func (m *Model) copyIndicator() *chrome.Copy {
	var pane string
	var line int64
	selecting := ""
	switch {
	case m.copy != nil:
		pane = m.copy.pane
		line = m.copy.m.Cursor().Line
		if _, _, kind, ok := m.copy.m.Selection(); ok {
			selecting = kind
		}
	case m.scroll != nil:
		pane = m.scroll.pane
		line = m.scroll.top
	default:
		return nil
	}
	sc := m.panes.Screen(pane)
	if sc == nil {
		return &chrome.Copy{Selecting: selecting}
	}
	first, base := sc.Range()
	_, rows := sc.Size()
	total := base - first + int64(rows)
	return &chrome.Copy{Line: min(max(line-first+1, 1), max(total, 1)), Total: total, Selecting: selecting}
}
