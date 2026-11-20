package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"devtools/internal/agent"
	"devtools/internal/catalog"
	"devtools/internal/config"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/vt"
)

// The server side of the cross-machine messages (3.2.3, 4.2.8, 4.2.10, 4.3,
// 5.3.6): the catalog stream, the requests a command in a pane routes to the
// window that displays it, and the agents of the panes. Everything here is
// registered through the extension seam from init, runs on the loop and sees
// the server only through Core and Conn.

func init() {
	Extend(proto.TypeCatalogWatch, proto.CapCatalog, catalogWatch)
	Extend(proto.TypeCatalogStop, proto.CapCatalog, catalogStop)
	Extend(proto.TypeUIHub, proto.CapRoute, routeHub)
	Extend(proto.TypeUISwitch, proto.CapRoute, routeSwitch)
	// The agents family has no message a client sends: the records travel in
	// the state and event.agent goes from the server to the client. Its type
	// is registered so that the family is announced in welcome.caps; a client
	// that sends it as a request is told it is not one.
	Extend(proto.TypeEventAgent, proto.CapAgents, agentEventFromClient)
	Observe(agentObserver{})
}

// The seams of the tests: the catalog stream, the configuration it reads,
// the clock of the agent trackers, and how often a running catalog stream
// looks whether its connection is still there.
var (
	catalogRun   = catalog.Watch
	catalogLoad  = loadConfig
	agentClock   = time.Now
	catalogCheck = 500 * time.Millisecond
)

// loadConfig reads the configuration of the server's user (4.6) at the start
// of every catalog stream, so roots and bookmarks changed after the server
// started are in the next one.
func loadConfig() (config.Config, error) {
	path, err := config.Path()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
}

// crossState is what this file keeps for one server. Every field belongs to
// that server's loop.
type crossState struct {
	watches map[Conn]*catalogStream // the running catalog stream of each connection

	agents  *agent.Set
	timer   uint64    // the number of the armed timer of agents
	timerAt time.Time // when it fires; zero when none is armed

	resumes map[string]*resumeWatch // per agent pane, what is known of its conversation (resume.go)
}

// crossStates holds the state of every server of the process, by its Core.
// A process runs one server; the tests run many, each with its own state.
var crossStates struct {
	mu sync.Mutex
	m  map[Core]*crossState
}

func stateOf(core Core) *crossState {
	crossStates.mu.Lock()
	defer crossStates.mu.Unlock()
	if crossStates.m == nil {
		crossStates.m = map[Core]*crossState{}
	}
	st := crossStates.m[core]
	if st == nil {
		st = &crossState{watches: map[Conn]*catalogStream{}, agents: agent.NewSet(agentClock), resumes: map[string]*resumeWatch{}}
		crossStates.m[core] = st
	}
	return st
}

// connected reports whether c is still one of the server's connections.
func connected(core Core, c Conn) bool {
	for _, x := range core.Conns() {
		if x == c {
			return true
		}
	}
	return false
}

// --- 4.3 Catalog stream ---

// catalogStream is one running catalog.watch. The scan emits from its own
// goroutines; what it emits waits in queue, in order, until the loop sends it
// on the connection with the watch's id.
type catalogStream struct {
	id     uint64
	conn   Conn
	cancel context.CancelFunc

	mu       sync.Mutex
	queue    []proto.Msg
	posted   bool // a drain is posted to the loop and has not run
	finished bool // the scan returned; nothing more is queued
	stopped  bool // stopped or replaced: nothing more is sent
}

// catalogWatch starts the stream of a connection. A second watch on the same
// connection replaces the first, whose scan is cancelled (4.3).
func catalogWatch(core Core, c Conn, m proto.Msg) {
	if c.Kind() != proto.KindInteractive {
		if m.ID != 0 {
			core.Fail(c, m.ID, proto.CodeInvalid, "catalog.watch is for interactive connections only")
		}
		return
	}
	if m.ID == 0 {
		return // the stream is answered under the id of its watch; without one nobody waits
	}
	st := stateOf(core)
	if old := st.watches[c]; old != nil {
		old.stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &catalogStream{id: m.ID, conn: c, cancel: cancel}
	st.watches[c] = w
	run, load := catalogRun, catalogLoad
	go func() {
		run(ctx, load, func(msg proto.Msg) { w.push(core, msg) })
		w.finish(core)
	}()
	st.armCheck(core, w)
}

// catalogStop ends the stream of a connection; it has no reply (4.3). Its id
// names the watch; a stop for another watch than the running one changes
// nothing.
func catalogStop(core Core, c Conn, m proto.Msg) {
	st := stateOf(core)
	w := st.watches[c]
	if w == nil || (m.ID != 0 && m.ID != w.id) {
		return
	}
	w.stop()
	delete(st.watches, c)
}

// armCheck looks, every catalogCheck while w runs, whether its connection is
// still there; a closed connection cancels the scan.
func (st *crossState) armCheck(core Core, w *catalogStream) {
	core.After(catalogCheck, func(core Core) {
		if st.watches[w.conn] != w {
			return
		}
		if !connected(core, w.conn) {
			w.stop()
			delete(st.watches, w.conn)
			return
		}
		st.armCheck(core, w)
	})
}

// stop cancels the scan; nothing it emits afterwards is sent.
func (w *catalogStream) stop() {
	w.mu.Lock()
	w.stopped = true
	w.queue = nil
	w.mu.Unlock()
	w.cancel()
}

// push queues one message of the scan and makes sure a drain is posted. It
// runs on a goroutine of the scan.
func (w *catalogStream) push(core Core, msg proto.Msg) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.finished {
		return
	}
	w.queue = append(w.queue, msg)
	w.post(core)
}

// finish says the scan returned.
func (w *catalogStream) finish(core Core) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.finished = true
	w.post(core)
}

// post hands a drain to the loop unless one waits there; w.mu is held. At
// most one drain is outstanding, and each takes the whole queue, so the
// messages reach the connection in the order the scan emitted them however
// the loop orders the posts.
func (w *catalogStream) post(core Core) {
	if w.posted {
		return
	}
	w.posted = true
	core.Post(w.drain)
}

// drain sends what the scan queued, on the loop. A stream whose connection
// is gone is cancelled; a stream that finished is forgotten.
func (w *catalogStream) drain(core Core) {
	w.mu.Lock()
	queue, finished, stopped := w.queue, w.finished, w.stopped
	w.queue, w.posted = nil, false
	w.mu.Unlock()
	st := stateOf(core)
	if stopped || st.watches[w.conn] != w {
		return
	}
	if !connected(core, w.conn) {
		w.stop()
		delete(st.watches, w.conn)
		return
	}
	for _, msg := range queue {
		if out, ok := withID(w.id, msg); ok {
			w.conn.Send(out)
		}
	}
	if finished {
		w.cancel()
		delete(st.watches, w.conn)
	}
}

// withID returns msg with the id of its watch (4.3: the messages of the
// stream carry it). The catalog emits only message structs of proto.
func withID(id uint64, msg proto.Msg) (proto.Msg, bool) {
	v, ok := proto.NewMessage(msg.T)
	if !ok || msg.Decode(v) != nil {
		return proto.Msg{}, false
	}
	return proto.Request(id, v), true
}

// --- 4.2.10 Requests routed to the window ---

// routeHub is ui.hub of a one-shot: the window that displays the caller's
// pane opens its hub.
func routeHub(core Core, c Conn, m proto.Msg) {
	if m.ID == 0 {
		return // a routed copy goes from the server to a window, never back
	}
	var q proto.UIHub
	if m.Decode(&q) != nil {
		core.Fail(c, m.ID, proto.CodeInvalid, "the request ui.hub does not decode")
		return
	}
	switch q.Mode {
	case proto.HubAll, proto.HubOpen, proto.HubNew, proto.HubAgents:
	default:
		core.Fail(c, m.ID, proto.CodeInvalid, "ui.hub has no mode "+session.Sanitize(q.Mode, 32))
		return
	}
	here := "" // a working directory that is not absolute is not passed on
	if filepath.IsAbs(q.Here) {
		here = filepath.Clean(q.Here)
	}
	target, code, text := routeTarget(core, c)
	if code != "" {
		core.Fail(c, m.ID, code, text)
		return
	}
	target.Send(proto.Encode(proto.UIHub{
		Mode:  q.Mode,
		Query: session.Sanitize(q.Query, session.MaxText),
		Here:  here,
		Pane:  c.Pane(),
	}))
	core.Reply(c, m.ID, nil)
}

// routeSwitch is ui.switch of a one-shot: the window that displays the
// caller's pane switches to the session named, or to the session of a
// directory, which the window's view.set opens when there is none. The
// target is checked here, so a caller learns of a wrong one from its reply
// and the window is not moved.
func routeSwitch(core Core, c Conn, m proto.Msg) {
	if m.ID == 0 {
		return
	}
	var q proto.UISwitch
	if m.Decode(&q) != nil {
		core.Fail(c, m.ID, proto.CodeInvalid, "the request ui.switch does not decode")
		return
	}
	if (q.Session == "") == (q.Dir == "") {
		core.Fail(c, m.ID, proto.CodeInvalid, "ui.switch needs exactly one of session and dir")
		return
	}
	out := proto.UISwitch{Session: q.Session, Tab: q.Tab, Pane: q.Pane}
	if q.Dir != "" {
		if !filepath.IsAbs(q.Dir) {
			core.Fail(c, m.ID, proto.CodeInvalid, "dir must be an absolute path")
			return
		}
		if fi, err := os.Stat(q.Dir); err != nil || !fi.IsDir() {
			core.Fail(c, m.ID, proto.CodeNoDir, "directory "+q.Dir+" does not exist")
			return
		}
		out.Dir, out.Name = filepath.Clean(q.Dir), session.Sanitize(q.Name, session.MaxLabel)
	}
	// The caller is checked before the session: a command outside a pane
	// learns that first, whatever it names.
	target, code, text := routeTarget(core, c)
	if code != "" {
		core.Fail(c, m.ID, code, text)
		return
	}
	if q.Session != "" && !hasSession(core.State(), q.Session) {
		core.Fail(c, m.ID, proto.CodeNoSession, "there is no session "+session.Sanitize(q.Session, 32))
		return
	}
	target.Send(proto.Encode(out))
	core.Reply(c, m.ID, nil)
}

// routeTarget is the routing rule of 4.2.10: S is the session of the
// caller's pane; among the interactive connections whose view is S and whose
// hello claims route (4.1), local or through a bridge, the one with the
// latest input gets the routed copy. A
// caller without a pane, or whose pane is gone, is no-pane; no candidate is
// no-client.
func routeTarget(core Core, c Conn) (Conn, string, string) {
	pane := c.Pane()
	if pane == "" {
		return nil, proto.CodeNoPane, "the request does not come from a devtools pane"
	}
	sess, ok := core.PaneSession(pane)
	if !ok {
		return nil, proto.CodeNoPane, "pane " + session.Sanitize(pane, 32) + " is gone"
	}
	var best Conn
	for _, x := range core.Conns() {
		if x == c || x.Kind() != proto.KindInteractive || !x.Has(proto.CapRoute) {
			continue
		}
		if view, ok := x.View(); !ok || view != sess {
			continue
		}
		if best == nil || x.LastInput().After(best.LastInput()) ||
			(x.LastInput().Equal(best.LastInput()) && x.Focused() && !best.Focused()) {
			best = x
		}
	}
	if best == nil {
		return nil, proto.CodeNoClient, "no window displays the session of pane " + session.Sanitize(pane, 32)
	}
	return best, "", ""
}

func hasSession(snap proto.Snapshot, id string) bool {
	for _, s := range snap.Sessions {
		if s.ID == id {
			return true
		}
	}
	return false
}

// --- Agents (3.6, 4.2.8) ---

// agentEventFromClient answers event.agent sent by a client: it is a message
// of the server.
func agentEventFromClient(core Core, c Conn, m proto.Msg) {
	if m.ID != 0 {
		core.Fail(c, m.ID, proto.CodeInvalid, "event.agent is sent by the server only")
	}
}

// agentObserver feeds what the panes report into the agent trackers of the
// server, writes every change into the state, drives the trackers' timers
// and sends event.agent on a transition into blocked or done.
type agentObserver struct{}

func (agentObserver) Title(core Core, pane, title string) {
	st := stateOf(core)
	st.agentChanges(core, st.agents.Title(pane, title))
}

func (agentObserver) Report(core Core, pane string, r vt.AgentReport) {
	st := stateOf(core)
	st.agentChanges(core, st.agents.Report(pane, agent.Report{State: r.State, Kind: r.Kind}))
}

func (agentObserver) Foreground(core Core, pane string, group []platform.Proc) {
	procs := make([]agent.Proc, len(group))
	for i, p := range group {
		procs[i] = agent.Proc{Pid: p.Pid, Name: p.Name, Args: p.Args}
	}
	st := stateOf(core)
	st.agentChanges(core, st.agents.Procs(pane, procs))
	st.lookUpConversation(core, pane, procs)
}

func (agentObserver) Seen(core Core, pane string) {
	st := stateOf(core)
	st.agentChanges(core, st.agents.Seen(pane))
}

func (agentObserver) Gone(core Core, pane string) {
	st := stateOf(core)
	delete(st.resumes, pane)
	st.agentChanges(core, st.agents.Gone(pane))
}

// agentChanges applies the changes of one input, then lets the trackers'
// waiting changes that are due take effect and arms the timer for the next.
func (st *crossState) agentChanges(core Core, changes []agent.Change) {
	st.apply(core, changes)
	st.tick(core)
}

func (st *crossState) tick(core Core) {
	changes, next := st.agents.Tick()
	st.apply(core, changes)
	st.arm(core, next)
}

// arm makes sure a timer fires at next. An armed timer that fires earlier
// is kept: its tick arms the next one.
func (st *crossState) arm(core Core, next time.Time) {
	if next.IsZero() || (!st.timerAt.IsZero() && !next.Before(st.timerAt)) {
		return
	}
	st.timer++
	n := st.timer
	st.timerAt = next
	core.After(max(time.Until(next), 0), func(core Core) {
		if st.timer != n {
			return // replaced by an earlier one
		}
		st.timerAt = time.Time{}
		st.tick(core)
	})
}

// apply writes the changes into the state: PutAgent for an agent, DelAgent
// for a pane that is no longer one (whose conversation is not resumed any
// more); a transition into blocked or done is
// also sent as event.agent to every interactive connection that claims
// agents.
func (st *crossState) apply(core Core, changes []agent.Change) {
	if len(changes) == 0 {
		return
	}
	state := proto.NewState(core.State())
	for _, ch := range changes {
		if ch.State.Kind == "" {
			core.DelAgent(ch.Pane)
			st.forgetConversation(core, ch.Pane)
			continue
		}
		p, ok := state.Panes[ch.Pane]
		if !ok {
			continue // a pane the state no longer has has no record
		}
		var since int64
		if !ch.State.Since.IsZero() {
			since = ch.State.Since.UnixMilli()
		}
		core.PutAgent(ch.Pane, proto.Agent{
			Pane:   ch.Pane,
			Kind:   string(ch.State.Kind),
			Status: string(ch.State.Status),
			Title:  ch.State.Title,
			Cwd:    p.Cwd,
			Since:  since,
		})
		if ch.Notify {
			notifyAgent(core, state, p, ch.State)
		}
	}
}

// notifyAgent sends event.agent to every interactive connection whose hello
// claims agents (4.1: never a message a peer did not claim). shown says
// whether a focused interactive connection displays the pane's tab now
// (4.2.8): its session is the connection's view and the tab is the
// session's current one.
func notifyAgent(core Core, state *proto.State, p proto.Pane, a agent.State) {
	conns := core.Conns()
	shown := false
	if sess, ok := state.Sessions[p.Session]; ok && sess.Current == p.Tab {
		for _, c := range conns {
			if view, ok := c.View(); ok && view == p.Session && c.Kind() == proto.KindInteractive && c.Focused() {
				shown = true
				break
			}
		}
	}
	m := proto.Encode(proto.AgentEvent{
		Session: p.Session,
		Tab:     p.Tab,
		Pane:    p.ID,
		Kind:    string(a.Kind),
		Status:  string(a.Status),
		Title:   session.Sanitize(a.Title, session.MaxTitle),
		Shown:   shown,
	})
	for _, c := range conns {
		if c.Kind() == proto.KindInteractive && c.Has(proto.CapAgents) {
			c.Send(m)
		}
	}
}
