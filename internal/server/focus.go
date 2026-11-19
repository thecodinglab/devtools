package server

import "devtools/internal/proto"

// Focus (3.2.3, 4.2.6). A pane is focused for its program (mode 1004)
// exactly when it is the focused pane of the current tab of a session that
// at least one interactive connection with a focused window displays. A
// connection's window counts as focused from its hello until it says
// otherwise with input.focus. The same set of panes, every pane of those
// tabs, is what the observer is told the user sees (Observer.Seen).

// windowFocus is input.focus of cn. A window that gains the focus counts as
// an interaction for the size owner (3.2.3: any input message); one that
// loses it does not, so the window the user left does not take the size.
func (c *core) windowFocus(cn *conn, focused bool) {
	cn.focused = focused
	if focused && cn.view != "" {
		c.interacted(cn)
	}
}

// watching reports whether cn is an interactive connection with a focused
// window that displays a session.
func watching(cn *conn) bool {
	return cn.kind == proto.KindInteractive && !cn.gone && cn.focused && cn.view != "" && !cn.pending
}

// syncFocus tells every pane's program whether it is focused, when that
// changed.
func (c *core) syncFocus() {
	want := map[string]bool{}
	for _, cn := range c.reg.list() {
		if !watching(cn) {
			continue
		}
		if sess, ok := c.known.Sessions[cn.view]; ok {
			want[c.known.Tabs[sess.Current].Focused] = true
		}
	}
	for id, h := range c.handles {
		h.setFocus(want[id])
	}
}

// setFocus records the wish and lets the focus goroutine of the pane send it.
func (h *handle) setFocus(focused bool) {
	h.mu.Lock()
	h.focusWant = focused
	h.mu.Unlock()
	h.applyFocus()
}

// applyFocus starts the goroutine that sends the newest wish, unless it
// runs. It calls Pane.Focus outside every lock, one call at a time.
func (h *handle) applyFocus() {
	h.mu.Lock()
	if h.focusBusy || h.focusWant == h.focusDone || h.p == nil {
		h.mu.Unlock()
		return
	}
	h.focusBusy = true
	h.mu.Unlock()
	go func() {
		for {
			h.mu.Lock()
			p, v := h.p, h.focusWant
			if p == nil || v == h.focusDone {
				h.focusBusy = false
				h.mu.Unlock()
				return
			}
			h.mu.Unlock()
			p.Focus(v)
			h.mu.Lock()
			if h.p == p {
				h.focusDone = v
			}
			h.mu.Unlock()
		}
	}()
}

// syncSeen calls Observer.Seen for every pane that has just become seen:
// its tab is the current tab of a session a focused connection displays.
func (c *core) syncSeen() {
	now := map[string]bool{}
	for _, cn := range c.reg.list() {
		if !watching(cn) {
			continue
		}
		if sess, ok := c.known.Sessions[cn.view]; ok {
			for _, id := range c.known.Tabs[sess.Current].Panes {
				now[id] = true
			}
		}
	}
	var fresh []string
	for id := range now {
		if !c.seen[id] {
			fresh = append(fresh, id)
		}
	}
	c.seen = now
	if c.observer == nil {
		return
	}
	sortIDs(fresh)
	for _, id := range fresh {
		c.observer.Seen(c, id)
	}
}

// seenAgain calls Observer.Seen for a pane that reported while it is seen
// (5.3.6).
func (c *core) seenAgain(paneID string) {
	if c.seen[paneID] && c.observer != nil {
		c.observer.Seen(c, paneID)
	}
}
