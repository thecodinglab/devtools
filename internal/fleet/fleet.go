package fleet

import (
	"context"
	"sync"
	"time"

	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// The loop of the fleet (3.5, 5.3.8 of the plan). One goroutine, the one
// that runs Run, owns every field of Fleet that is not marked otherwise.
// Everything else talks to it through post: the public methods, the timers
// of the clock, the dial goroutines. The messages of the links arrive on
// linkCh, read by one pump goroutine per link. Nothing the loop does waits
// on anybody: updates leave through a pipe whose queue grows instead of
// blocking, hub events through a queue that is bounded per machine
// (hubqueue.go).

// Bounds and timings of 3.5 that the loop applies.
const (
	updatesCap   = 256              // capacity of Updates (5.3.8)
	hubEventsCap = 256              // capacity of the channel of HubOpened
	attemptLimit = 30 * time.Second // a dial that is not online by then is given up (3.5.2)
	summaryEvery = 50 * time.Millisecond
	bellEvery    = time.Second
)

// Fleet is the client's model of all machines: the local server and every
// configured machine, their links, what they report, and what the window,
// the status row and the hub get of it.
type Fleet struct {
	o     Options
	clock Clock

	// safe from any goroutine
	updates *pipe[Update]
	mu      sync.Mutex
	inbox   []func()
	wake    chan struct{}
	linkCh  chan linkMsg
	stop    chan struct{} // closed when Run ends
	started bool          // under mu: Run was called

	// owned by the loop
	ctx      context.Context
	machines []*machine // local first, then config order
	byName   map[string]*machine
	view     viewer
	hub      *hubQueue // nil while the hub is closed
	focused  bool
	cols     int
	rows     int
	colors   *proto.Msg
	sum      summaryState
	bell     bellState
	dirty    bool // something the summary shows may have changed
}

// linkMsg is one message of a link, or its end, for the loop.
type linkMsg struct {
	mc  *machine
	gen int
	msg proto.Msg
	end bool
}

// viewer is what the switch state machine (P2-06, switch.go) does inside
// the loop: what the window shows, and its share of every link's messages.
// The core calls it and never looks into it. Until P2-06 installs its own
// through newViewer, noView shows nothing.
type viewer interface {
	// state is what the window shows: the Mode of SummaryData ("showing",
	// "switching", "stale", "none"), the target displayed or being switched
	// to, and whether the displayed view is stale.
	state() (mode string, target session.Target, stale bool)
	// holds reports whether the window displays a session of the machine,
	// or shows it stale, or waits for a switch to it: such a link is
	// supervised (3.5.2) and gets no idle timer (3.5.1).
	holds(machine string) bool
	// receive is a message of mc's link that the core does not handle:
	// view.ok, view.ended, frame, event.clipboard, event.bell, ui.hub,
	// ui.switch, and the replies ok, err, state.snapshot with an id.
	receive(mc *machine, m proto.Msg)
	// linkUp: mc's link is online with a new snapshot; sameBoot is true
	// when it is the same server as before the last drop.
	linkUp(mc *machine, sameBoot bool)
	// linkDown: mc's link ended or its dial failed, for the reason p.
	linkDown(mc *machine, p hub.Problem)
	// resized: the pane area is now f.cols by f.rows.
	resized()
	// focusChanged: the outer terminal gained or lost focus (f.focused).
	focusChanged()
}

// newViewer, when set (by an init function of P2-06), makes the viewer of a
// new fleet.
var newViewer func(f *Fleet) viewer

// noView is the viewer of a fleet without the switch state machine: it
// shows nothing.
type noView struct{}

func (noView) state() (string, session.Target, bool) { return "none", session.Target{}, false }
func (noView) holds(string) bool                     { return false }
func (noView) receive(*machine, proto.Msg)           {}
func (noView) linkUp(*machine, bool)                 {}
func (noView) linkDown(*machine, hub.Problem)        {}
func (noView) resized()                              {}
func (noView) focusChanged()                         {}

// New makes a fleet of the local machine and the machines of o. Nothing is
// dialled before Run. A machine named twice is kept once, at its first
// line, permanent when either line says connect; a name that is not a
// valid machine is left out (config.Load refuses it before).
func New(o Options) *Fleet {
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	f := &Fleet{
		o:       o,
		clock:   o.Clock,
		updates: newPipe[Update](updatesCap, func(u Update) bool { return u.Kind == Summary }),
		wake:    make(chan struct{}, 1),
		linkCh:  make(chan linkMsg, 64),
		stop:    make(chan struct{}),
		byName:  map[string]*machine{},
		focused: true,
	}
	f.cols, f.rows = proto.ClampSize(o.Cols, o.Rows)
	f.add(&machine{name: "", local: true})
	for _, m := range o.Machines {
		if !session.ValidMachine(m.Name) {
			continue
		}
		if old, ok := f.byName[m.Name]; ok {
			old.connect = old.connect || m.Connect
			continue
		}
		f.add(&machine{name: m.Name, connect: m.Connect})
	}
	f.view = noView{}
	if newViewer != nil {
		f.view = newViewer(f)
	}
	return f
}

func (f *Fleet) add(mc *machine) {
	mc.index = len(f.machines)
	mc.status = "off"
	mc.backoff = backoffMin
	f.machines = append(f.machines, mc)
	f.byName[mc.name] = mc
}

// Run owns all state in one goroutine and returns when ctx ends, after every
// link is closed. Updates is closed when it returns. A second call returns
// at once.
func (f *Fleet) Run(ctx context.Context) error {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return nil
	}
	f.started = true
	f.mu.Unlock()

	f.ctx = ctx
	defer f.shutdown()
	for _, mc := range f.machines {
		if mc.local || mc.connect {
			f.dial(mc)
		}
	}
	f.dirty = true
	f.settle()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.wake:
			f.drain()
		case lm := <-f.linkCh:
			f.linkMessage(lm)
		}
		f.settle()
	}
}

// drain runs every command posted so far.
func (f *Fleet) drain() {
	for {
		f.mu.Lock()
		cmds := f.inbox
		f.inbox = nil
		f.mu.Unlock()
		if len(cmds) == 0 {
			return
		}
		for _, c := range cmds {
			c()
		}
	}
}

// settle runs after every step of the loop: what the step changed reaches
// the status row.
func (f *Fleet) settle() {
	if f.dirty {
		f.dirty = false
		f.emitSummary()
	}
}

// shutdown closes every link and stops every dial, then the pipes.
func (f *Fleet) shutdown() {
	close(f.stop)
	for _, mc := range f.machines {
		f.disconnect(mc)
	}
	// what was posted meanwhile runs once more: a dial that ended now
	// closes its link, and nothing dials again, because ctx is done
	f.drain()
	for _, mc := range f.machines {
		f.disconnect(mc)
	}
	if f.hub != nil {
		f.hub.close()
		f.hub = nil
	}
	f.updates.close()
}

// post queues c for the loop. It never blocks.
func (f *Fleet) post(c func()) {
	f.mu.Lock()
	f.inbox = append(f.inbox, c)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// timer is a timer of the fleet's clock whose function runs on the loop,
// and not at all once stopped, even when the clock already fired.
type timer struct {
	t    Timer
	dead bool // owned by the loop
}

// after runs fn on the loop once d has passed.
func (f *Fleet) after(d time.Duration, fn func()) *timer {
	tm := &timer{}
	tm.t = f.clock.AfterFunc(d, func() {
		f.post(func() {
			if !tm.dead {
				tm.dead = true
				fn()
			}
		})
	})
	return tm
}

// stop stops tm; a nil tm is stopped already.
func (tm *timer) stop() {
	if tm == nil {
		return
	}
	tm.dead = true
	tm.t.Stop()
}

// active reports whether tm is set and has not run or been stopped.
func (tm *timer) active() bool { return tm != nil && !tm.dead }

// Updates is what the client renders. Capacity 256. Summary updates are
// coalesced. Frames keep their order; when the channel is full the fleet
// stops reading the displayed link, so back-pressure reaches the server.
func (f *Fleet) Updates() <-chan Update { return f.updates.out }

// update hands u to the client.
func (f *Fleet) update(u Update) { f.updates.put(u) }

// notice puts one line in the status row.
func (f *Fleet) notice(text string) { f.update(Update{Kind: Notice, Notice: text}) }

// Resize records the pane area; the displayed link learns it from the
// viewer.
func (f *Fleet) Resize(cols, rows int) {
	f.post(func() {
		c, r := proto.ClampSize(cols, rows)
		if c == f.cols && r == f.rows {
			return
		}
		f.cols, f.rows = c, r
		f.view.resized()
	})
}

// Focus records whether the outer terminal has focus and forwards the
// change to the link of what is displayed (3.6.3: "seen" on that server).
func (f *Fleet) Focus(focused bool) {
	f.post(func() {
		if f.focused == focused {
			return
		}
		f.focused = focused
		if mode, t, _ := f.view.state(); mode == "showing" {
			if mc := f.byName[t.Machine]; mc != nil {
				f.send(mc, proto.Encode(proto.Focus{Focused: focused}))
			}
		}
		f.view.focusChanged()
		f.dirty = true
	})
}

// Colors sends a view.colors message to every link, and to every link that
// comes online later. Any other message is ignored.
func (f *Fleet) Colors(m proto.Msg) {
	if m.T != proto.TypeViewColors {
		return
	}
	var c proto.ViewColors
	if m.Decode(&c) != nil {
		return
	}
	msg := proto.Encode(c)
	f.post(func() {
		f.colors = &msg
		for _, mc := range f.machines {
			if mc.status == "online" {
				f.send(mc, msg)
			}
		}
	})
}

// HubOpened starts discovery (3.5.3) and returns the events of every
// machine. The channel is closed by HubClosed, or by a later HubOpened.
func (f *Fleet) HubOpened() <-chan hub.Event {
	var p *hubQueue
	p = newHubQueue(hubEventsCap, func(machine string) {
		f.post(func() { f.hubCaughtUp(p, machine) })
	})
	f.post(func() { f.openHub(p) })
	return p.out
}

// HubClosed ends discovery: the catalog streams stop and on-demand machines
// start their idle timers.
func (f *Fleet) HubClosed() { f.post(f.closeHub) }

// Switch shows t in the window (3.5.4, switch.go).
func (f *Fleet) Switch(t session.Target) {
	f.post(func() {
		if s := switcherOf(f); s != nil {
			s.switchTo(t, kindUser)
		}
	})
}

// Cancel drops a pending switch; what is displayed stays.
func (f *Fleet) Cancel() {
	f.post(func() {
		if s := switcherOf(f); s != nil {
			s.cancel()
		}
	})
}

// Input sends an input.* message (or pane.scroll) to what is displayed;
// keys and paste are held while switching (3.5.4), everything is dropped
// while the view is stale.
func (f *Fleet) Input(m proto.Msg) {
	f.post(func() {
		if s := switcherOf(f); s != nil {
			s.input(m)
		}
	})
}

// Request sends a command to the displayed server; the channel gets its one
// reply, with m's id. Without a displayed server, and when the link is lost
// before the reply, the reply is err{code:"lost"} (route.go).
func (f *Fleet) Request(m proto.Msg) <-chan proto.Msg {
	reply := make(chan proto.Msg, 1)
	f.post(func() {
		if s := switcherOf(f); s != nil {
			s.request(m, reply)
			return
		}
		reply <- proto.ErrReply(m.ID, proto.CodeLost, "no machine is shown")
	})
	return reply
}

// Step steps through the sessions of the displayed machine
// (session-next, session-prev).
func (f *Fleet) Step(delta int) {
	f.post(func() {
		if s := switcherOf(f); s != nil {
			s.step(delta)
		}
	})
}

// Drop ends the link of the committed view when a frame of it was
// unreadable (3.2.4, 4.2.5): t is the Target of the Shown the client shows.
// The view turns stale and the link is supervised as after any loss
// (3.5.2, 3.5.5). Nothing happens when t is no longer the committed view,
// or when that view is stale already. Contract change 14.
func (f *Fleet) Drop(t session.Target) {
	f.post(func() {
		s := switcherOf(f)
		if s == nil {
			return
		}
		c := s.cur
		if c == nil || c.stale || c.mc.name != t.Machine || c.target.Session != t.Session {
			return
		}
		f.lost(c.mc, &remote.Failure{Reason: remote.Protocol, Text: "an unreadable frame"})
	})
}

// realClock is the clock of production.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, fn func()) Timer {
	return time.AfterFunc(d, fn)
}

// pipe moves values from the loop to a channel without ever blocking the
// loop: what the channel cannot take waits in a queue. Values for which
// coalesce is true replace a waiting one of their kind, so only the newest
// waits.
type pipe[T any] struct {
	out      chan T
	coalesce func(T) bool
	mu       sync.Mutex
	q        []T
	closed   bool
	wake     chan struct{}
	done     chan struct{}
}

func newPipe[T any](capacity int, coalesce func(T) bool) *pipe[T] {
	p := &pipe[T]{
		out:      make(chan T, capacity),
		coalesce: coalesce,
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *pipe[T]) put(v T) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if p.coalesce != nil && p.coalesce(v) {
		for i, w := range p.q {
			if p.coalesce(w) {
				p.q = append(p.q[:i], p.q[i+1:]...)
				break
			}
		}
	}
	p.q = append(p.q, v)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// close stops the pipe; what still waits is dropped and out is closed.
func (p *pipe[T]) close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	p.mu.Unlock()
}

func (p *pipe[T]) run() {
	defer close(p.out)
	for {
		p.mu.Lock()
		if len(p.q) == 0 {
			p.mu.Unlock()
			select {
			case <-p.wake:
				continue
			case <-p.done:
				return
			}
		}
		v := p.q[0]
		var zero T
		p.q[0] = zero
		p.q = p.q[1:]
		p.mu.Unlock()
		select {
		case p.out <- v:
		case <-p.done:
			return
		}
	}
}
