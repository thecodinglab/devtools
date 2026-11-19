package server

import (
	"devtools/internal/proto"
	"devtools/internal/vt"
)

// Sizes and colours (3.2.3). The size owner of a session is, among the
// interactive connections that display it, the one that interacted most
// recently: view.set, an input message, pane.scroll, and view.resize of the
// owner itself. Receiving frames or answering pings is no interaction. The
// owner's pane area is the session's size; a session nobody displays keeps
// the size it has (a session a one-shot opened and nobody displayed is 120
// by 40, the model's default). The colours of the owner's terminal are the
// ones the panes of the session answer OSC 10 and 11 with.

// owner returns the size owner of a session, nil when no interactive
// connection displays it. A connection between steps 3 and 4 of its
// view.set counts: the panes it displays are spawned at its size.
func (c *core) owner(sessionID string) *conn {
	var best *conn
	for _, cn := range c.reg.list() {
		if cn.kind != proto.KindInteractive || cn.view != sessionID || cn.gone {
			continue
		}
		if best == nil || cn.interact > best.interact {
			best = cn
		}
	}
	return best
}

// syncSizes gives every displayed session the size and the colours of its
// owner.
func (c *core) syncSizes() {
	viewed := map[string]bool{}
	for _, cn := range c.reg.list() {
		if cn.view != "" {
			viewed[cn.view] = true
		}
	}
	for sid := range viewed {
		o := c.owner(sid)
		if o == nil {
			continue
		}
		cols, rows := proto.ClampSize(o.cols, o.rows)
		if have, haveRows, ok := c.model.Size(sid); ok && (have != cols || haveRows != rows) {
			c.apply(c.model.SetSize(sid, cols, rows), true)
		}
		if c.colors[sid] != o.colors {
			c.colors[sid] = o.colors
			c.paintSession(sid, o.colors)
		}
	}
	for sid := range c.colors {
		if _, ok := c.known.Sessions[sid]; !ok {
			delete(c.colors, sid)
		}
	}
}

// paintSession hands the colours to every pane of the session that runs.
func (c *core) paintSession(sessionID string, col vt.Colors) {
	sess, ok := c.known.Sessions[sessionID]
	if !ok {
		return
	}
	for _, tid := range sess.Tabs {
		for _, pid := range c.known.Tabs[tid].Panes {
			if ref, ok := c.panes[pid]; ok {
				ref.p.SetColors(col)
			}
		}
	}
}

// colorsFor are the colours a new pane of the session starts with: its
// owner's, or the ones held for it, or the default of 4.2.1.
func (c *core) colorsFor(sessionID string) vt.Colors {
	if o := c.owner(sessionID); o != nil {
		return o.colors
	}
	if col, ok := c.colors[sessionID]; ok {
		return col
	}
	return defaultColors
}

// helloColors are the colours of a hello, the default without them.
func helloColors(h proto.Hello) vt.Colors {
	if h.Colors == nil || h.Colors.Fg == "" || h.Colors.Bg == "" {
		return defaultColors
	}
	return vt.Colors{Fg: h.Colors.Fg, Bg: h.Colors.Bg, Dark: h.Colors.Dark}
}
