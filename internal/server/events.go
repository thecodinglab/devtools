package server

import (
	"devtools/internal/pane"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// What the panes report (3.3, 4.2.8, 5.3.6), on the loop: the end of a
// program, the bell and the clipboard; titles, directories and agent
// reports are in paneEvent (loop.go).

// exited is a pane's program ending (3.3). An exit the server caused (Close)
// changes nothing, and neither does any exit while the server shuts down,
// so state.json keeps the layout (3.9). A program that ends with an exit
// status, whatever the code, closes its pane. One killed by a signal the
// server did not send, or one that could not be started, leaves the pane
// exited with its reason, until Enter starts it again or pane-close
// removes it.
func (c *core) exited(id string, ref paneRef) {
	if c.s.stopping.Load() {
		return
	}
	info := ref.p.Info()
	ex := info.Exit
	if ex == nil || ex.ByServer {
		return
	}
	if ex.Signal != "" || ex.Text != "" {
		c.apply(c.model.SetPane(id, func(p *proto.Pane) {
			p.State = proto.PaneExited
			p.Exit = &proto.PaneExit{Code: ex.Code, Signal: ex.Signal, Text: ex.Text}
			p.Busy, p.Editor = false, false
		}), true)
		return
	}
	if r, err := c.model.ClosePane(id); err == nil {
		c.apply(r, true)
	}
}

// respawn starts the program of an exited pane again: same id, same
// directory, at its layout size (3.3). A pane that is not exited (a second
// Enter that came before the first one's spawn) is left alone.
func (c *core) respawn(id string) {
	p, ok := c.known.Panes[id]
	if !ok || p.State != proto.PaneExited {
		return
	}
	c.apply(c.model.SetPane(id, func(p *proto.Pane) { p.State = proto.PaneRunning }), true)
}

// bell is event.bell: sent to every interactive connection (4.2.8); the
// fleet and the client decide whether it rings.
func (c *core) bell(id string) {
	m := proto.Encode(proto.Bell{Pane: id})
	for _, cn := range c.reg.list() {
		if cn.kind == proto.KindInteractive {
			c.send(cn, m)
		}
	}
}

// clipboard is event.clipboard: sent to the connections that display the
// pane's tab; never empty, at most MaxClip bytes (4.2.8).
func (c *core) clipboard(id string, data []byte) {
	if len(data) == 0 || len(data) > proto.MaxClip {
		return
	}
	p, ok := c.known.Panes[id]
	if !ok {
		return
	}
	m := proto.Encode(proto.Clipboard{Pane: id, Data: data})
	for _, cn := range c.reg.list() {
		if cn.kind != proto.KindInteractive || cn.view != p.Session || cn.pending {
			continue
		}
		if sess := c.known.Sessions[cn.view]; sess.Current == p.Tab {
			c.send(cn, m)
		}
	}
}

// paneEvent is what a pane reported through Notify, on the loop. gen tells
// the pane object that reported: a report of an object a respawn replaced is
// stale.
func (c *core) paneEvent(ev pane.Event, gen uint64) {
	ref, ok := c.panes[ev.Pane]
	if !ok || ref.gen != gen {
		return // a pane that went meanwhile, or an object replaced
	}
	switch ev.Kind {
	case pane.InfoChanged:
		info := ref.p.Info()
		old := c.known.Panes[ev.Pane]
		c.apply(c.model.SetPane(ev.Pane, func(p *proto.Pane) {
			p.Title = info.Title
			if info.Cwd != "" {
				p.Cwd = info.Cwd
			}
		}), false)
		if title := session.Sanitize(info.Title, session.MaxTitle); title != old.Title && c.observer != nil {
			c.observer.Title(c, ev.Pane, title)
			c.seenAgain(ev.Pane)
		}
		c.foreground(ev.Pane, ref, info)
	case pane.AgentReport:
		if c.observer != nil {
			c.observer.Report(c, ev.Pane, ev.Agent)
			c.seenAgain(ev.Pane)
		}
	case pane.Exited:
		c.exited(ev.Pane, ref)
	case pane.Bell:
		c.bell(ev.Pane)
	case pane.Clipboard:
		c.clipboard(ev.Pane, ev.Clipboard)
	}
}
