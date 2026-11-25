package server

import (
	"unicode/utf8"

	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/vt"
)

// The input path (3.2.3, 4.2.6). The reader of a connection applies
// input.key, input.mouse and input.paste to the pane itself, looked up in
// the connection's current view: typing does not wait for the loop, and a
// program that does not read holds only the connection that types into it.
// pane.scroll and pane.text are answered in the reader too. Each of these is
// an interaction for the size owner, which the reader posts to the loop in
// the order of the messages.

// inReader handles the messages the reader answers itself (3.2.3) and the
// rule for one-shots (4.2.1 rule 4); true when m needs nothing more.
func (s *Server) inReader(cn *conn, m proto.Msg) bool {
	switch m.T {
	case proto.TypePing:
		cn.lane.Send(proto.Encode(proto.Pong{}))
		return true
	case proto.TypePong:
		return true
	}
	if cn.kind == proto.KindOneshot && (hasPrefix(m.T, "view.") || hasPrefix(m.T, "input.") || m.T == proto.TypePaneScroll) {
		if m.ID != 0 {
			cn.lane.Send(proto.ErrReply(m.ID, proto.CodeInvalid, m.T+" is not allowed on a one-shot connection"))
		}
		return true
	}
	switch m.T {
	case proto.TypeInputKey, proto.TypeInputMouse, proto.TypeInputPaste, proto.TypeInputFocus, proto.TypePaneScroll:
		if m.ID != 0 {
			return false // a request nobody knows: the loop answers it
		}
		s.input(cn, m)
		return true
	case proto.TypePaneText:
		if m.ID != 0 {
			s.paneText(cn, m)
		}
		return true
	}
	return false
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// input applies one input message or pane.scroll of cn. A message for a pane
// that is not displayed on cn is dropped, so input never reaches a pane the
// client cannot see.
func (s *Server) input(cn *conn, m proto.Msg) {
	// The gate of a live upgrade: after the handover closed it no input
	// reaches a pane (upgrade.go).
	s.inputMu.RLock()
	defer s.inputMu.RUnlock()
	if s.gated {
		return
	}
	if m.T == proto.TypeInputFocus {
		var q proto.Focus
		if m.Decode(&q) == nil {
			s.post(func(c *core) {
				if !cn.gone {
					c.windowFocus(cn, q.Focused)
				}
			})
		}
		return
	}
	v := cn.viewp.Load()
	if v == nil {
		return
	}
	var paneID string
	var q struct {
		key    proto.Key
		mouse  proto.Mouse
		paste  proto.Paste
		scroll proto.PaneScroll
	}
	var err error
	switch m.T {
	case proto.TypeInputKey:
		err = m.Decode(&q.key)
		paneID = q.key.Pane
	case proto.TypeInputMouse:
		err = m.Decode(&q.mouse)
		paneID = q.mouse.Pane
	case proto.TypeInputPaste:
		err = m.Decode(&q.paste)
		paneID = q.paste.Pane
	case proto.TypePaneScroll:
		err = m.Decode(&q.scroll)
		paneID = q.scroll.Pane
	}
	h := v.panes[paneID]
	if err != nil || h == nil {
		return
	}
	s.post(func(c *core) {
		if !cn.gone {
			c.interacted(cn)
		}
	})
	switch m.T {
	case proto.TypeInputKey:
		s.key(cn, h, q.key)
	case proto.TypeInputMouse:
		if p := h.pane(); p != nil {
			p.Mouse(vt.Mouse{Kind: q.mouse.Kind, Button: q.mouse.Button, X: q.mouse.X, Y: q.mouse.Y, Mod: q.mouse.Mod})
		}
	case proto.TypeInputPaste:
		s.paste(cn, h, q.paste)
	case proto.TypePaneScroll:
		s.scroll(cn, v, h, q.scroll)
	}
}

// key applies input.key. Typing ends the connection's scrolling in the pane
// (4.2.7). Enter without a modifier on an exited pane starts its program
// again instead of reaching it (3.3); an unknown key name is ignored.
func (s *Server) key(cn *conn, h *handle, k proto.Key) {
	if !proto.ValidKey(k.Key) {
		return
	}
	h.endScroll(cn)
	p := h.pane()
	if p == nil {
		return
	}
	if k.Key == "enter" && k.Mod&^(proto.ModCaps|proto.ModNum) == 0 && !k.Release && p.Info().State == proto.PaneExited {
		id := h.id
		s.post(func(c *core) { c.respawn(id) })
		return
	}
	p.Key(vt.Key{
		Name: k.Key, Mod: k.Mod, Text: k.Text,
		Shifted: firstRune(k.Shifted), Base: firstRune(k.Base),
		Repeat: k.Repeat, Release: k.Release,
	})
}

func firstRune(s string) rune {
	if s == "" {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// paste applies one chunk of input.paste. The bracket opens before the
// first chunk and closes after the one without more (4.2.6); the reader
// alone keeps which pane has a paste open on cn. A chunk above the limit is
// cut at a rune boundary.
func (s *Server) paste(cn *conn, h *handle, q proto.Paste) {
	h.endScroll(cn)
	p := h.pane()
	if p == nil {
		return
	}
	text := cutRunes(q.Text, proto.MaxPaste)
	first := !cn.pasting[h.id]
	if q.More {
		cn.pasting[h.id] = true
	} else {
		delete(cn.pasting, h.id)
	}
	cn.pasteOpen.Store(len(cn.pasting) > 0)
	p.Paste(text, first, !q.More)
}

// cutRunes cuts s to at most n bytes at a rune boundary.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// scroll applies pane.scroll (4.2.7): the connection's position and marks in
// the pane, clamped to the pane's history; top at or below base, or none, is
// live. The pane follows the top line when scrolled and the line of each
// mark; the lines of the message replaced are let go first. A pane.scroll
// without a top and without marks returns the connection to live.
func (s *Server) scroll(cn *conn, v *viewState, h *handle, q proto.PaneScroll) {
	h.fmu.Lock()
	defer h.fmu.Unlock()
	p := h.pane()
	if p == nil {
		return
	}
	marks := q.Marks
	if len(marks) > 2 {
		marks = marks[:2]
	}
	if q.Top == nil && len(marks) == 0 {
		h.endScroll(cn)
		return
	}
	r := p.Snapshot(0, false)
	st := &scrolls{marks: append([]proto.Pos(nil), marks...)}
	if q.Top != nil && *q.Top < r.Base {
		st.scrolled, st.top = true, max(*q.Top, r.First)
		st.follows = append(st.follows, st.top)
	}
	for _, m := range st.marks {
		if m.Line >= 0 {
			st.follows = append(st.follows, m.Line)
		}
	}
	h.mu.Lock()
	if cn.viewp.Load() != v || h.p != p {
		h.mu.Unlock() // the view or the pane changed meanwhile
		return
	}
	var old []int64
	if have := h.scrolls[cn]; have != nil {
		old = have.follows
	}
	h.scrolls[cn] = st
	h.mu.Unlock()
	for _, l := range old {
		p.Unfollow(l)
	}
	for _, l := range st.follows {
		p.Follow(l)
	}
	cn.lane.Scroll(h.id, st.top, st.scrolled)
}

// paneText answers pane.text in the reader (3.2.3). The pane is looked up on
// the loop, after every request cn sent before; the text is read here,
// because reading a long selection waits for the pane.
func (s *Server) paneText(cn *conn, m proto.Msg) {
	var q proto.PaneText
	if err := m.Decode(&q); err != nil {
		cn.lane.Send(proto.ErrReply(m.ID, proto.CodeInvalid, "the request pane.text does not decode"))
		return
	}
	type found struct {
		h      *handle
		exists bool
	}
	answer := make(chan found, 1)
	s.post(func(c *core) {
		_, ok := c.known.Panes[q.Pane]
		answer <- found{c.handles[q.Pane], ok}
	})
	var f found
	select {
	case f = <-answer:
	case <-s.quit:
		return
	}
	if !f.exists {
		cn.lane.Send(proto.ErrReply(m.ID, proto.CodeNoPane, "pane "+session.Sanitize(q.Pane, 32)+" does not exist"))
		return
	}
	var res proto.PaneTextResult
	if f.h != nil {
		if p := f.h.pane(); p != nil {
			res.Text, res.Cut = p.Text(vt.Pos{Line: q.From.Line, Col: q.From.Col}, vt.Pos{Line: q.To.Line, Col: q.To.Col}, q.Block)
		}
	}
	if len(res.Text) > proto.MaxText {
		res.Text, res.Cut = cutRunes(res.Text, proto.MaxText), true
	}
	if !cn.lane.Send(proto.OKReply(m.ID, res)) {
		cn.nc.Close()
	}
}
