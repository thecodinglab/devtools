package client

// What the fleet's updates do to the model (3.2.4, 3.5.4, 3.5.5).

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/fleet"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// refreshEvery bounds how often frames make the client ask the displayed
// server for its state.
//
// provisional (P1-17a, contract change 14): the client needs the displayed
// tab, its split and focused pane, and the pane fields `editor`, `busy`
// and `program` (4.2.2, W2; panes.Set.Retab). Contract change 14 has the
// fleet forward the displayed target's state as an update of kind Frame,
// but the fleet does not send it yet (a fix lane of the fleet writes it).
// Until it does, the client asks for the state with state.get through
// Fleet.Request: after a view commits, after the reply of each request
// that changes focus, when a frame names a pane it does not show, and, at
// most once per refreshEvery, after frames arrived (a program that starts
// or ends prints, so its `editor`, `busy` and `program` are read soon
// after). A state.snapshot or state.delta that arrives as an update of
// kind Frame is applied as well, so the forwarding needs no change here.
const refreshEvery = time.Second

// maxHeld bounds the frames kept while the state is fetched.
const maxHeld = 64

// stateReply is the reply of a state.get the client sent, with the view
// (Model.viewGen) it was asked for. A reply of an earlier view came from
// the server that view showed, which may be another machine whose ids are
// the same as the displayed one's, so it is never applied.
type stateReply struct {
	gen int
	msg proto.Msg
}

// updates applies one batch of the fleet's updates.
func (m *Model) updates(batch UpdatesMsg) tea.Cmd {
	var cmds []tea.Cmd
	framed := false
	for _, u := range batch {
		switch u.Kind {
		case fleet.Shown:
			cmds = append(cmds, m.shown(u))
		case fleet.Frame:
			cmds = append(cmds, m.frameUpdate(u.Frame))
			framed = true
		case fleet.Summary:
			cmds = append(cmds, m.summaryUpdate(u.Summary))
		case fleet.Stale:
			m.stale()
		case fleet.Notice:
			cmds = append(cmds, m.setNotice(u.Notice))
		case fleet.Bell:
			cmds = append(cmds, m.bell(u))
		case fleet.OpenHub:
			cmds = append(cmds, m.openHub(u.Hub))
		case fleet.Clipboard:
			cmds = append(cmds, m.programClipboard(u))
		}
	}
	if framed && m.view == "showing" && m.hk.Now().Sub(m.comp.refreshed) >= refreshEvery {
		cmds = append(cmds, m.refreshState())
	}
	return tea.Batch(cmds...)
}

// shown is a committed view: its panes start empty and the first frame of
// each is full (4.2.3).
func (m *Model) shown(u fleet.Update) tea.Cmd {
	if u.Target.Machine != m.target.Machine {
		m.state = nil
	}
	// a state read still in flight belongs to the view before: its reply
	// is ignored, and this view asks for its own at once
	m.viewGen++
	m.refresh = false
	m.comp.again = false
	m.view = "showing"
	m.target = u.Target
	m.ok = u.View
	m.broken = false
	m.held = nil
	m.confirm = nil
	m.endView()
	m.unscroll = ""
	switched := m.switchMode(false) // the switch committed
	clear(m.bells)
	m.panes.Reset(u.View)
	if m.state != nil {
		if t, ok := m.shownTab(); ok && t.ID == u.View.Tab {
			m.panes.Retab(t)
		}
	}
	m.comp.full = true
	return tea.Batch(switched, m.refreshState())
}

// stale is the displayed machine lost (3.5.5): the picture stays, typing
// is dropped, and copy mode, the scrolled view and the confirm, which need
// the machine, end. A pending prefix and a running switch stay (5a.10.1).
func (m *Model) stale() {
	if m.view == "none" {
		return
	}
	m.view = "stale"
	m.confirm = nil
	m.endView()
	m.unscroll = ""
	m.gate.drop()
	m.comp.full = true
}

// summaryUpdate takes the roll-up and machine states. A summary whose
// mode is "none" means nothing is displayed any more (3.5.4: the session
// ended and there was nothing to fall back to).
func (m *Model) summaryUpdate(s fleet.SummaryData) tea.Cmd {
	m.summary = s
	cmd := m.switchMode(s.Mode == "switching")
	if s.Mode == "none" && m.view != "none" {
		m.view = "none"
		m.viewGen++ // a read in flight belongs to the view that ended
		m.refresh = false
		m.comp.again = false
		m.ok = proto.ViewOK{}
		m.panes.Reset(proto.ViewOK{})
		m.state = nil
		m.confirm = nil
		m.endView()
		m.comp.full = true
		return cmd
	}
	if m.view == "showing" && m.hk.Now().Sub(m.comp.refreshed) >= refreshEvery {
		return tea.Batch(cmd, m.refreshState())
	}
	return cmd
}

// switchDue: a switch has been pending for ui.Slow.
type switchDue struct{ gen int }

// switchMode follows a switch of the fleet (3.5.4, 5a.10.1). From its
// first moment the old view is drawn inactive and its keys go to the fleet,
// which holds them for the target; copy mode ends. The status row says
// "switching" only after ui.Slow, so a switch that commits at once never
// shows it. When the switch ends without a new view (a rollback), the pane
// whose copy mode it ended is told to show live again.
func (m *Model) switchMode(pending bool) tea.Cmd {
	if pending == m.pending {
		return nil
	}
	m.pending = pending
	m.switchGen++
	m.comp.full = true
	if pending {
		if m.copy != nil {
			m.unscroll = m.copy.pane
			m.exitCopy(false)
		}
		m.press = nil
		return m.hk.After(ui.Slow, switchDue{gen: m.switchGen})
	}
	m.switching = false
	if m.unscroll != "" && m.view == "showing" {
		m.toLive(m.unscroll)
	}
	m.unscroll = ""
	return nil
}

// switchSlow is the timer of ui.Slow: the switch still runs.
func (m *Model) switchSlow(d switchDue) {
	if m.pending && d.gen == m.switchGen {
		m.switching = true
	}
}

// frameUpdate applies one message of the displayed target: a frame, or the
// displayed server's state, or event.reflow (copy.go).
func (m *Model) frameUpdate(msg proto.Msg) tea.Cmd {
	switch msg.T {
	case proto.TypeFrame:
		var f proto.Frame
		if err := msg.Decode(&f); err != nil {
			return m.invalidFrame()
		}
		return m.frame(f)
	case proto.TypeEventReflow:
		return m.reflow(msg)
	case proto.TypeStateSnapshot:
		var s proto.Snapshot
		if msg.Decode(&s) == nil {
			return m.setState(proto.NewState(s))
		}
	case proto.TypeStateDelta:
		var d proto.Delta
		if msg.Decode(&d) != nil || m.state == nil || m.state.Apply(d) != nil {
			return m.refreshState()
		}
		return m.setState(m.state)
	}
	return nil
}

// frame applies a frame to the screen of its pane. A frame of a pane the
// client does not show, or one that starts a pane again, can follow a
// change of the tab the client has not read yet: it is kept, and applied
// once the state is read. Any other refusal makes the frame invalid.
func (m *Model) frame(f proto.Frame) tea.Cmd {
	if m.view != "showing" || m.broken {
		return nil
	}
	if len(m.held) > 0 {
		// frames keep their order
		return m.hold(f)
	}
	err := m.panes.Apply(f)
	if err == nil {
		return m.afterFrame(f.Pane)
	}
	if m.panes.Screen(f.Pane) == nil || (f.Seq == 1 && f.Full) {
		return m.hold(f)
	}
	return m.invalidFrame()
}

// hold keeps a frame until the state is read.
func (m *Model) hold(f proto.Frame) tea.Cmd {
	if len(m.held) >= maxHeld {
		return m.invalidFrame()
	}
	m.held = append(m.held, heldFrame{f: f, after: m.comp.refreshes})
	return m.refreshState()
}

// heldFrame is a frame kept for the state, with the number of state reads
// that had been asked for when it arrived.
type heldFrame struct {
	f     proto.Frame
	after int
}

// replayHeld applies the kept frames after a state read. A frame that is
// still refused after a read that was asked for after it arrived is
// invalid.
func (m *Model) replayHeld(read int) tea.Cmd {
	held := m.held
	m.held = nil
	var cmds []tea.Cmd
	for i, h := range held {
		if err := m.panes.Apply(h.f); err == nil {
			cmds = append(cmds, m.afterFrame(h.f.Pane))
			continue
		}
		if read > h.after {
			return tea.Batch(append(cmds, m.invalidFrame())...)
		}
		m.held = append(m.held, held[i:]...)
		return tea.Batch(append(cmds, m.refreshState())...)
	}
	return tea.Batch(cmds...)
}

// invalidFrame: a frame the client cannot read (3.2.4, 4.2.5). Nothing more
// of this view is applied, the notice says so, and the link the frame came
// on is dropped (Fleet.Drop, contract change 14): the fleet turns the view
// stale and supervises the link, and the Shown that follows its reconnect
// starts every pane from a full frame.
func (m *Model) invalidFrame() tea.Cmd {
	if m.broken {
		return nil
	}
	m.broken = true
	m.held = nil
	m.fleet.Drop(m.target)
	return m.setNotice(ui.Notice("bad-frame", m.machineName(m.target.Machine)))
}

// refreshState asks the displayed server for its state; one read at a
// time.
func (m *Model) refreshState() tea.Cmd {
	if m.view == "none" || m.refresh {
		if m.refresh {
			m.comp.again = true
		}
		return nil
	}
	m.refresh = true
	m.comp.refreshes++
	m.comp.refreshed = m.hk.Now()
	id := m.newID()
	reply := m.fleet.Request(proto.Request(id, proto.StateGet{}))
	gen := m.viewGen
	return m.hk.Await(reply, func(msg proto.Msg) tea.Msg { return stateReply{gen: gen, msg: msg} })
}

// stateRead is the reply of a state.get. The reply of a read asked for an
// earlier view is dropped (it neither ends this view's read nor touches its
// state), and a snapshot older than the state this view already holds, as
// a state.delta forwarded while the read ran makes it, does not replace
// that state.
func (m *Model) stateRead(r stateReply) tea.Cmd {
	if r.gen != m.viewGen {
		return nil
	}
	m.refresh = false
	read := m.comp.refreshes
	var cmds []tea.Cmd
	if r.msg.T == proto.TypeStateSnapshot {
		var s proto.Snapshot
		if r.msg.Decode(&s) == nil {
			if m.state == nil || m.stateGen != m.viewGen || s.Rev >= m.state.Rev {
				cmds = append(cmds, m.setState(proto.NewState(s)))
			}
		}
	}
	cmds = append(cmds, m.replayHeld(read))
	if m.comp.again {
		m.comp.again = false
		cmds = append(cmds, m.refreshState())
	}
	// a gate waits for the state that holds its request's result (W2)
	cmds = append(cmds, m.gateRead())
	return tea.Batch(cmds...)
}

// setState takes the state of the displayed server and follows its
// displayed tab.
func (m *Model) setState(st *proto.State) tea.Cmd {
	m.state = st
	m.stateGen = m.viewGen
	if m.view == "none" {
		return nil
	}
	if t, ok := m.shownTab(); ok {
		m.panes.Retab(t)
		delete(m.bells, t.ID)
		m.followTab()
	}
	if m.confirm != nil {
		if _, ok := m.pane(m.confirm.pane); !ok {
			m.confirm = nil
		}
	}
	return nil
}

// bell is a bell of the displayed server or of an agent (3.6.3, 4.2.8). A
// program's bell is written only when its pane is on screen; a bell in
// another tab marks the tab.
func (m *Model) bell(u fleet.Update) tea.Cmd {
	if u.Frame.T == proto.TypeEventBell {
		var b proto.Bell
		if u.Frame.Decode(&b) != nil {
			return nil
		}
		if m.view != "showing" || m.panes.Screen(b.Pane) == nil {
			if p, ok := m.pane(b.Pane); ok && session.ValidID(p.Tab) {
				m.bells[p.Tab] = true
			}
			return nil
		}
	}
	return tea.Raw("\a")
}
