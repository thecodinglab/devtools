package fleet

import (
	"encoding/json"
	"strconv"

	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// What the servers ask of the window, and what the window asks of the shown
// server:
//
//   - ui.hub and ui.switch routed by a server (a command typed in a pane,
//     4.2.10) are honoured only when they arrive on the link of the shown
//     view, and the machine of a routed switch is that link's machine: a
//     routed message cannot name one (SEC11). From any other link they are
//     dropped;
//   - event.clipboard and event.bell reach the client only from the link of
//     the shown view (SEC15);
//   - Request sends a command of the client to the shown server and hands
//     back its one reply. A request whose link is lost is answered locally
//     with err{code:"lost"} and never sent again (3.5.2).

// request is a request of the client in flight on a link.
type request struct {
	id    uint64 // the client's id
	reply chan proto.Msg
}

// routed is a ui.hub or ui.switch on mc's link.
func (s *switcher) routed(mc *machine, m proto.Msg) {
	c := s.cur
	if m.ID != 0 || c == nil || c.stale || c.mc != mc {
		// not a routed copy, or not from the shown view's link: dropped
		// (SEC11). The fleet has no log; the drop is silent.
		return
	}
	switch m.T {
	case proto.TypeUISwitch:
		var u proto.UISwitch
		if m.Decode(&u) != nil {
			return
		}
		// the machine is the link's, whatever the message says
		t := session.Target{Machine: mc.name, Session: u.Session, Dir: u.Dir, Name: u.Name, Tab: u.Tab, Pane: u.Pane}
		s.switchTo(t, kindUser)
	case proto.TypeUIHub:
		var u proto.UIHub
		if m.Decode(&u) != nil {
			return
		}
		mode, ok := hubMode(u.Mode)
		if !ok {
			return
		}
		here := session.Target{Machine: mc.name}
		if validPath(u.Here) {
			here.Dir = u.Here
		}
		s.f.update(Update{Kind: OpenHub, Hub: HubRequest{Mode: mode, Query: cleanText(u.Query), Here: here}})
	}
}

// hubMode maps the mode of ui.hub to the hub's (4.2.10).
func hubMode(m string) (hub.Mode, bool) {
	switch m {
	case proto.HubAll:
		return hub.ModeAll, true
	case proto.HubOpen:
		return hub.ModeSessions, true
	case proto.HubNew:
		return hub.ModeNew, true
	case proto.HubAgents:
		return hub.ModeAgents, true
	}
	return 0, false
}

// clipboard is event.clipboard on mc's link: written only when it comes
// from the shown view (SEC15), and never empty.
func (s *switcher) clipboard(mc *machine, m proto.Msg) {
	if !s.displays(mc) {
		return
	}
	var c proto.Clipboard
	if m.Decode(&c) != nil || len(c.Data) == 0 || len(c.Data) > proto.MaxClip || !validID('p', c.Pane) {
		return
	}
	s.f.update(Update{Kind: Clipboard, Target: s.cur.target, Data: c.Data})
}

// bell is event.bell on mc's link: a program of the shown view rang. It
// reaches the client as a Bell whose Frame is the event, so the client
// can ring only when that pane is on screen (4.2.8). The agent bell of
// 3.6.3 is a Bell without a Frame.
func (s *switcher) bell(mc *machine, m proto.Msg) {
	if !s.displays(mc) {
		return
	}
	var b proto.Bell
	if m.Decode(&b) != nil || !validID('p', b.Pane) {
		return
	}
	s.f.update(Update{Kind: Bell, Target: s.cur.target, Frame: proto.Encode(b)})
}

// notForRequest are the message types the client does not send through
// Request: the window's own messages are the fleet's.
var notForRequest = map[string]bool{
	proto.TypeHello: true, proto.TypeViewSet: true, proto.TypeViewClear: true, proto.TypeViewResize: true,
	proto.TypeViewColors: true, proto.TypeInputKey: true, proto.TypeInputMouse: true, proto.TypeInputPaste: true,
	proto.TypeInputFocus: true, proto.TypePaneScroll: true, proto.TypeUIHub: true, proto.TypeUISwitch: true,
	proto.TypeCatalogWatch: true, proto.TypeCatalogStop: true, proto.TypePing: true, proto.TypePong: true,
}

// request is Request(m): m goes to the shown server under an id of its
// link; the reply comes back under m's id.
func (s *switcher) request(m proto.Msg, reply chan proto.Msg) {
	if notForRequest[m.T] || m.T == "" {
		reply <- proto.ErrReply(m.ID, proto.CodeInvalid, "not a request for the shown server")
		return
	}
	c := s.cur
	if c == nil || c.stale || !c.mc.online() {
		reply <- proto.ErrReply(m.ID, proto.CodeLost, "no machine is shown")
		return
	}
	id := c.mc.newID()
	out, ok := withID(m, id)
	if !ok {
		reply <- proto.ErrReply(m.ID, proto.CodeInvalid, "unreadable request")
		return
	}
	if !s.f.send(c.mc, out) {
		reply <- proto.ErrReply(m.ID, proto.CodeLost, "the connection ended")
		return
	}
	if s.reqs[c.mc] == nil {
		s.reqs[c.mc] = map[uint64]request{}
	}
	s.reqs[c.mc][id] = request{id: m.ID, reply: reply}
}

// reply hands a reply on mc's link to the request it answers; false when
// it answers none of the client's.
func (s *switcher) reply(mc *machine, m proto.Msg) bool {
	r, ok := s.reqs[mc][m.ID]
	if !ok {
		return false
	}
	delete(s.reqs[mc], m.ID)
	if m.T == proto.TypeErr {
		e, _ := proto.AsReply(m)
		r.reply <- proto.ErrReply(r.id, cleanText(e.Code), cleanText(e.Text))
		return true
	}
	out, ok := withID(m, r.id)
	if !ok {
		out = proto.ErrReply(r.id, proto.CodeInvalid, "unreadable reply")
	}
	r.reply <- out
	return true
}

// lost answers every request in flight on mc's link with err lost; none is
// sent again.
func (s *switcher) lost(mc *machine) {
	for _, r := range s.reqs[mc] {
		r.reply <- proto.ErrReply(r.id, proto.CodeLost, "the connection to "+s.name(mc)+" was lost")
	}
	delete(s.reqs, mc)
}

// withID is m with the id id (0: without one), its other fields as they
// were.
func withID(m proto.Msg, id uint64) (proto.Msg, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(m.Body, &fields) != nil || fields == nil {
		return proto.Msg{}, false
	}
	delete(fields, "t")
	delete(fields, "id")
	b, err := json.Marshal(fields)
	if err != nil {
		return proto.Msg{}, false
	}
	tq, _ := json.Marshal(m.T)
	body := append([]byte(`{"t":`), tq...)
	if id != 0 {
		body = append(body, `,"id":`...)
		body = strconv.AppendUint(body, id, 10)
	}
	if len(b) > 2 {
		body = append(body, ',')
		body = append(body, b[1:len(b)-1]...)
	}
	body = append(body, '}')
	return proto.Msg{T: m.T, ID: id, Body: body}, true
}
