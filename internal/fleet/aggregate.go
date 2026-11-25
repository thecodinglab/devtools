package fleet

import (
	"reflect"
	"time"

	"devtools/internal/agent"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// The roll-up and the bell of 3.6.3.

// rank orders agent statuses as they come from the wire: blocked 4, done 3,
// working 2, idle 1, unknown and anything else 0 (agent.Rank, 5.3.8).
func rank(s string) int { return agent.Rank(agent.Status(s)) }

// urgent reports whether a status is one the bell rings for.
func urgent(s string) bool { return s == proto.StatusBlocked || s == proto.StatusDone }

// urgentPanes is pane → status of every agent of st that is blocked or done.
func urgentPanes(st *proto.State) map[string]string {
	out := map[string]string{}
	if st == nil {
		return out
	}
	for id, a := range st.Agents {
		if urgent(a.Status) {
			out[id] = a.Status
		}
	}
	return out
}

// notIdle counts the agents of st that are working, blocked or done: the
// Busy of a machine (3.6.3).
func notIdle(st *proto.State) int {
	if st == nil {
		return 0
	}
	n := 0
	for _, a := range st.Agents {
		if a.Status == proto.StatusWorking || urgent(a.Status) {
			n++
		}
	}
	return n
}

// summaryState is what the status row was last sent.
type summaryState struct {
	have  bool
	last  SummaryData
	at    time.Time
	timer *timer
}

// emitSummary sends the summary when it changed, at most once per 50 ms: a
// change inside that time is sent when it ends, as the newest summary then.
func (f *Fleet) emitSummary() {
	if f.sum.timer.active() {
		return
	}
	s := f.computeSummary()
	if f.sum.have && reflect.DeepEqual(s, f.sum.last) {
		return
	}
	now := f.clock.Now()
	if f.sum.have && now.Sub(f.sum.at) < summaryEvery {
		f.sum.timer = f.after(f.sum.at.Add(summaryEvery).Sub(now), func() {
			f.sum.timer = nil
			f.dirty = true
		})
		return
	}
	f.sum.have, f.sum.last, f.sum.at = true, s, now
	f.update(Update{Kind: Summary, Target: s.Target, Summary: s})
}

// computeSummary is the roll-up (3.6.3): counts over every online machine,
// the most urgent status, every machine with its state, and the most urgent
// blocked or done agent that is not on screen here.
func (f *Fleet) computeSummary() SummaryData {
	mode, target, stale := f.view.state()
	s := SummaryData{
		Mode:   mode,
		Target: target,
		Stale:  stale,
		Counts: map[agent.Status]int{},
	}
	top := ""
	var best *proto.Agent
	var bestMC *machine
	for _, mc := range f.list() {
		ms := MachineState{
			Name:    mc.name,
			Host:    mc.host,
			State:   mc.status,
			Problem: mc.problem,
			Agents:  map[agent.Status]int{},
			Busy:    mc.busy,

			Version:   mc.version,
			Installed: mc.installed,
		}
		if mc.online() && mc.st != nil {
			ms.Busy = notIdle(mc.st)
			for _, a := range mc.st.Agents {
				st := agent.Status(a.Status)
				ms.Agents[st]++
				s.Counts[st]++
				if top == "" || rank(a.Status) > rank(top) {
					top = a.Status
				}
				if !urgent(a.Status) || f.onScreen(mc, a.Session, a.Tab) {
					continue
				}
				if best == nil || f.moreUrgent(mc, a, bestMC, *best) {
					a := a
					best, bestMC = &a, mc
				}
			}
		}
		s.Machines = append(s.Machines, ms)
	}
	s.Top = agent.Status(top)
	if best != nil {
		s.Urgent = session.Target{Machine: bestMC.name, Session: best.Session, Tab: best.Tab, Pane: best.Pane}
	}
	return s
}

// moreUrgent orders agents as the agents scope does (3.6.4): by urgency,
// then local first and config order, then session label, then pane id.
func (f *Fleet) moreUrgent(mc *machine, a proto.Agent, bmc *machine, b proto.Agent) bool {
	if ra, rb := rank(a.Status), rank(b.Status); ra != rb {
		return ra > rb
	}
	if mc.index != bmc.index {
		return mc.index < bmc.index
	}
	la, lb := mc.st.Sessions[a.Session].Label, bmc.st.Sessions[b.Session].Label
	if la != lb {
		return la < lb
	}
	return a.Pane < b.Pane
}

// onScreen reports whether a tab of mc is on screen here (3.6.3 rule 2):
// its session is displayed in this window, it is the session's current tab,
// and the outer terminal has focus.
func (f *Fleet) onScreen(mc *machine, sess, tab string) bool {
	if !f.focused {
		return false
	}
	mode, t, _ := f.view.state()
	if mode != "showing" || t.Machine != mc.name || t.Session != sess || mc.st == nil {
		return false
	}
	s, ok := mc.st.Sessions[sess]
	return ok && s.Current == tab
}

// bellState is the rate limit of the bell (3.6.3 rule 5).
type bellState struct {
	have bool
	at   time.Time // when the last bell was written
	held *timer
	wait []heldBell
}

// heldBell is a bell held back by the rate limit: the agent of pane (in tab
// and session) of mc entered status.
type heldBell struct {
	mc              *machine
	sess, tab, pane string
	status          string
}

// agentEvent is event.agent (3.6.3 rule 1): one bell when nobody is looking
// at the agent, it has not rung in this status, and the rate limit allows.
// Transitions in deltas do not ring by themselves: the server sends
// event.agent for each of them, with the shown flag a delta does not carry.
func (f *Fleet) agentEvent(mc *machine, e proto.AgentEvent) {
	if !mc.online() || !urgent(e.Status) || e.Shown {
		return
	}
	if !validID('p', e.Pane) || !validID('t', e.Tab) || !validID('s', e.Session) {
		return
	}
	if f.onScreen(mc, e.Session, e.Tab) {
		return
	}
	f.ring(mc, heldBell{mc: mc, sess: e.Session, tab: e.Tab, pane: e.Pane, status: e.Status})
}

// ring writes the bell for a pane in a status, or holds it until the
// second after the last bell ends.
func (f *Fleet) ring(mc *machine, b heldBell) {
	if mc.rung == nil {
		mc.rung = map[string]string{}
	}
	if mc.rung[b.pane] == b.status {
		return
	}
	for _, h := range f.bell.wait {
		if h.mc == mc && h.pane == b.pane && h.status == b.status {
			return
		}
	}
	now := f.clock.Now()
	if !f.bell.have || now.Sub(f.bell.at) >= bellEvery {
		f.writeBell(now)
		mc.rung[b.pane] = b.status
		return
	}
	f.bell.wait = append(f.bell.wait, b)
	if !f.bell.held.active() {
		f.bell.held = f.after(f.bell.at.Add(bellEvery).Sub(now), f.flushBells)
	}
}

// flushBells writes the held bells when the second after the last bell
// ends, once for all of them. A held bell is only what is still waiting:
// one whose agent left the status or went away meanwhile was dropped then
// (endHeld, forgetHeld), and is neither written nor marked as written
// (rule 3), so the agent's next real transition rings.
func (f *Fleet) flushBells() {
	f.bell.held = nil
	wait := f.bell.wait
	f.bell.wait = nil
	var due []heldBell
	for _, h := range wait {
		if !h.mc.dropped {
			due = append(due, h)
		}
	}
	if len(due) == 0 {
		return
	}
	f.writeBell(f.clock.Now())
	for _, h := range due {
		if h.mc.rung == nil {
			h.mc.rung = map[string]string{}
		}
		h.mc.rung[h.pane] = h.status
	}
}

// dropHeld removes the held bells of mc for which gone is true.
func (f *Fleet) dropHeld(mc *machine, gone func(heldBell) bool) {
	kept := f.bell.wait[:0]
	for _, h := range f.bell.wait {
		if h.mc != mc || !gone(h) {
			kept = append(kept, h)
		}
	}
	clear(f.bell.wait[len(kept):])
	f.bell.wait = kept
}

// forgetHeld follows a snapshot of mc, the whole truth of its server: a
// held bell whose agent is not in the held status there is over. With all
// set, every held bell of mc is: a new server (3.6.3 rule 4), whose pane
// ids may name other agents.
func (f *Fleet) forgetHeld(mc *machine, all bool) {
	f.dropHeld(mc, func(h heldBell) bool {
		if all || mc.st == nil {
			return true
		}
		a, ok := mc.st.Agents[h.pane]
		return !ok || a.Status != h.status
	})
}

// endHeld follows a delta of mc: a held bell is over when the delta puts its
// agent in another status, or removes the agent, its pane, its tab or its
// session. Only what the delta says counts: event.agent may come before
// the delta that has the same transition, so a state that does not show the
// held status yet does not end the bell.
func (f *Fleet) endHeld(mc *machine, d proto.Delta) {
	if len(f.bell.wait) == 0 {
		return
	}
	for _, c := range d.Changes {
		f.dropHeld(mc, func(h heldBell) bool {
			if c.Op == proto.OpDel {
				switch c.Kind {
				case proto.KindAgent, proto.KindPane:
					return c.ID == h.pane
				case proto.KindTab:
					return c.ID == h.tab
				case proto.KindSession:
					return c.ID == h.sess
				}
				return false
			}
			if c.Kind != proto.KindAgent || c.ID != h.pane {
				return false
			}
			a, ok := mc.st.Agents[c.ID]
			return !ok || a.Status != h.status
		})
	}
}

func (f *Fleet) writeBell(now time.Time) {
	f.bell.have, f.bell.at = true, now
	f.update(Update{Kind: Bell})
}

// knownStatuses takes what mc's state holds as known (3.6.3 rule 4): at the
// first connect and after a restart of its server nothing rings for it.
func (f *Fleet) knownStatuses(mc *machine) {
	f.forgetHeld(mc, false)
	mc.rung = urgentPanes(mc.st)
}

// newServer is the first snapshot of a server this client has not seen
// (3.6.3 rule 4): a bell held for the server before it is dropped, because
// its pane ids may name other agents now, and what the snapshot holds is
// known.
func (f *Fleet) newServer(mc *machine) {
	f.forgetHeld(mc, true)
	f.knownStatuses(mc)
}

// reconnectBells rings once for the agents of mc that turned blocked or
// done while its link was down (3.6.3 rule 4, NT11): a snapshot carries no
// shown flag, so rule 2 is judged by "on screen here" alone.
func (f *Fleet) reconnectBells(mc *machine) {
	f.forgetHeld(mc, false)
	now := urgentPanes(mc.st)
	for pane, status := range now {
		if mc.atDrop[pane] == status {
			continue
		}
		a := mc.st.Agents[pane]
		if f.onScreen(mc, a.Session, a.Tab) {
			continue
		}
		f.ring(mc, heldBell{mc: mc, sess: a.Session, tab: a.Tab, pane: pane, status: status})
	}
	// what the snapshot holds is known from now on
	for pane, status := range now {
		if _, held := f.holding(mc, pane); !held {
			if mc.rung == nil {
				mc.rung = map[string]string{}
			}
			mc.rung[pane] = status
		}
	}
	for pane := range mc.rung {
		if _, ok := now[pane]; !ok {
			delete(mc.rung, pane)
		}
	}
}

// holding reports whether a bell of mc's pane waits for the rate limit.
func (f *Fleet) holding(mc *machine, pane string) (string, bool) {
	for _, h := range f.bell.wait {
		if h.mc == mc && h.pane == pane {
			return h.status, true
		}
	}
	return "", false
}

// deltaBells clears the bell of a pane whose agent left blocked or done, or
// is gone (3.6.3 rule 3), and ends a held bell whose agent left the status
// it was held for: the bell of a status that ended is never written later.
func (f *Fleet) deltaBells(mc *machine, d proto.Delta) {
	f.endHeld(mc, d)
	if len(mc.rung) == 0 {
		return
	}
	for _, c := range d.Changes {
		if c.Kind != proto.KindAgent {
			continue
		}
		if c.Op == proto.OpDel {
			delete(mc.rung, c.ID)
			continue
		}
		if a, ok := mc.st.Agents[c.ID]; !ok || !urgent(a.Status) {
			delete(mc.rung, c.ID)
		}
	}
}
