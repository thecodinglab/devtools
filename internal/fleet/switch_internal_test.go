package fleet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/config"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// TestSwitchStateMachineUnderRandomEvents (SW20) drives the switch state
// machine with 5,000 seeded sequences of picks, drops, reconnects,
// timeouts and replies, the late and duplicated ones included. The loop's
// functions run on the test goroutine, so every sequence is exactly
// reproducible from its seed. The servers are modelled as the protocol
// promises (W1, W4): each answers the messages of its connection in the
// order they were sent and sends frames only for what it shows. After
// every event:
//
//   - the state is one of the four;
//   - at most one view is committed, and a showing state has one;
//   - no frame reaches the client that is not of the committed view;
//   - no held key is delivered twice.
func TestSwitchStateMachineUnderRandomEvents(t *testing.T) {
	sequences := 5000
	if testing.Short() {
		sequences = 500
	}
	for seed := range sequences {
		runSequence(t, uint64(seed))
		if t.Failed() {
			return
		}
	}
}

// --- a world of three machines on a clock of the test ---

// stepClock is a clock whose timers run when the test advances it, on the
// test goroutine.
type stepClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*stepTimer
}

type stepTimer struct {
	c   *stepClock
	at  time.Time
	seq int
	fn  func()
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) AfterFunc(d time.Duration, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &stepTimer{c: c, at: c.now.Add(d), seq: c.seq, fn: fn}
	c.timers = append(c.timers, t)
	return t
}

func (t *stepTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	for i, o := range t.c.timers {
		if o == t {
			t.c.timers = append(t.c.timers[:i], t.c.timers[i+1:]...)
			return true
		}
	}
	return false
}

// advance moves the clock by d, running each due timer and then the loop's
// commands it posted.
func (c *stepClock) advance(d time.Duration, f *Fleet) {
	target := c.Now().Add(d)
	for {
		c.mu.Lock()
		best := -1
		for i, t := range c.timers {
			if !t.at.After(target) && (best < 0 || t.at.Before(c.timers[best].at) || (t.at.Equal(c.timers[best].at) && t.seq < c.timers[best].seq)) {
				best = i
			}
		}
		if best < 0 {
			c.now = target
			c.mu.Unlock()
			return
		}
		t := c.timers[best]
		c.timers = append(c.timers[:best], c.timers[best+1:]...)
		if t.at.After(c.now) {
			c.now = t.at
		}
		c.mu.Unlock()
		t.fn()
		f.drain()
		f.settle()
	}
}

// waitDialer dials nothing: an attempt waits until the test completes or
// fails it, or until the fleet gives it up.
type waitDialer struct{}

func (waitDialer) Dial(ctx context.Context, machine string, login bool) (remote.Link, *remote.Failure) {
	<-ctx.Done()
	return nil, &remote.Failure{Reason: remote.Unreachable, Text: "given up"}
}

// testLink is a link whose sent messages the server model reads.
type testLink struct {
	welcome proto.Welcome
	recv    chan proto.Msg
	mu      sync.Mutex
	sent    []proto.Msg
	closed  bool
}

var errClosed = errors.New("closed")

func (l *testLink) Send(m proto.Msg) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errClosed
	}
	l.sent = append(l.sent, m)
	return nil
}
func (l *testLink) Recv() <-chan proto.Msg { return l.recv }
func (l *testLink) Welcome() proto.Welcome { return l.welcome }
func (l *testLink) Err() *remote.Failure   { return nil }
func (l *testLink) Close()                 { l.end() }
func (l *testLink) messages() []proto.Msg {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]proto.Msg(nil), l.sent...)
}
func (l *testLink) end() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		close(l.recv)
	}
}

// server is the model of one connection's server: it consumes what the
// fleet sent in order, answers view.set, and shows one view at a time.
type server struct {
	name     string
	link     *testLink
	sessions []proto.Session
	consumed int
	view     uint64 // the view.set it shows; 0 none
	panes    []string
	last     proto.Msg // the last reply, for duplicates
}

// world is one sequence.
type world struct {
	t      *testing.T
	seed   uint64
	rng    *rand.Rand
	f      *Fleet
	clock  *stepClock
	cancel context.CancelFunc
	srv    map[string]*server
	boots  map[string]int
	links  []*testLink

	// what view.set id n on a machine asked for: its session
	asked map[string]string // "machine/id" -> session id

	// the client's side
	shownMachine string
	shownSession string
	shown        bool
	keys         int
	log          []string
}

var names = []string{"", "M", "K"}

func runSequence(t *testing.T, seed uint64) {
	w := &world{
		t:     t,
		seed:  seed,
		rng:   rand.New(rand.NewPCG(seed, 0x5eed)),
		clock: &stepClock{now: time.Unix(1795000000, 0)},
		srv:   map[string]*server{},
		boots: map[string]int{},
		asked: map[string]string{},
	}
	w.f = New(Options{
		Machines: []config.Machine{{Name: "M", Connect: true}, {Name: "K"}},
		Dialer:   waitDialer{},
		Clock:    w.clock,
		Cols:     80, Rows: 23,
	})
	// the updates stay in the pipe's queue, where the test reads them
	w.f.updates.close()
	w.f.updates = &pipe[Update]{wake: make(chan struct{}, 1), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.f.ctx = ctx
	defer w.finish()
	// Run's start: the local machine and M are dialled
	for _, mc := range w.f.machines {
		if mc.local || mc.connect {
			w.f.dial(mc)
		}
	}
	w.step()
	for range 60 {
		w.event()
		w.step()
		if t.Failed() {
			return
		}
	}
}

func (w *world) finish() {
	w.cancel()
	for _, l := range w.links {
		l.end()
	}
	close(w.f.stop)
}

func (w *world) fail(format string, args ...any) {
	w.t.Helper()
	w.t.Fatalf("seed %d: %s\nevents:\n  %s", w.seed, fmt.Sprintf(format, args...), strings.Join(w.log, "\n  "))
}

// step runs what the event posted and checks the invariants.
func (w *world) step() {
	w.f.drain()
	w.f.settle()
	w.check()
}

func (w *world) mc(name string) *machine { return w.f.byName[name] }

func (w *world) event() {
	r := w.rng.IntN(100)
	name := names[w.rng.IntN(len(names))]
	if p := switcherOf(w.f).pend; p != nil && r >= 39 && w.rng.IntN(2) == 0 {
		// the machine a switch waits for answers more often than the others
		name = p.mc.name
	}
	switch {
	case r < 18:
		t := session.Target{Machine: name, Session: "s" + strconv.Itoa(1+w.rng.IntN(3))}
		if w.rng.IntN(5) == 0 {
			t = session.Target{Machine: name, Dir: "/d/" + strconv.Itoa(1+w.rng.IntN(3)), Name: "x"}
		}
		if w.rng.IntN(6) == 0 {
			t.Tab, t.Pane = "t1", "p1"
		}
		w.log = append(w.log, fmt.Sprintf("pick %+v", t))
		w.f.Switch(t)
	case r < 22:
		w.log = append(w.log, "cancel")
		w.f.Cancel()
	case r < 25:
		w.log = append(w.log, "step")
		w.f.Step(1 - 2*w.rng.IntN(2))
	case r < 33:
		w.keys++
		w.log = append(w.log, fmt.Sprintf("key k%d", w.keys))
		w.f.Input(proto.Encode(proto.Key{Pane: "p1", Key: "a", Text: "k" + strconv.Itoa(w.keys)}))
	case r < 39:
		w.log = append(w.log, "drop "+name)
		w.drop(name)
	case r < 49:
		w.log = append(w.log, "reconnect "+name)
		w.reconnect(name)
	case r < 57:
		d := []time.Duration{100 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}[w.rng.IntN(6)]
		w.log = append(w.log, "advance "+d.String())
		w.clock.advance(d, w.f)
	case r < 80:
		w.log = append(w.log, "reply "+name)
		w.reply(name)
	case r < 94:
		w.log = append(w.log, "frame "+name)
		w.frame(name)
	case r < 97:
		w.log = append(w.log, "duplicate "+name)
		if s := w.srv[name]; s != nil && s.last.T != "" {
			w.deliver(name, s.last)
		}
	default:
		w.log = append(w.log, "ended "+name)
		w.ended(name)
	}
}

// deliver hands m to the fleet as a message of name's current link.
func (w *world) deliver(name string, m proto.Msg) {
	mc := w.mc(name)
	if mc.link == nil {
		return
	}
	if s := w.srv[name]; s == nil || s.link != mc.link {
		return
	}
	w.f.linkMessage(linkMsg{mc: mc, gen: mc.gen, msg: m})
}

func (w *world) drop(name string) {
	mc := w.mc(name)
	switch {
	case mc.link != nil:
		w.f.linkMessage(linkMsg{mc: mc, gen: mc.gen, end: true})
	case mc.dialing:
		w.f.dialed(mc, mc.gen, nil, &remote.Failure{Reason: remote.Unreachable, Text: "down"})
	}
}

// reconnect completes a running dial with a new server: the same boot, or
// a restarted one that may have lost sessions.
func (w *world) reconnect(name string) {
	mc := w.mc(name)
	if !mc.dialing || mc.link != nil || mc.dropped {
		return
	}
	if w.rng.IntN(3) == 0 {
		w.boots[name]++
	}
	var ss []proto.Session
	for n := 1; n <= 3; n++ {
		if w.boots[name] == 0 || w.rng.IntN(3) > 0 {
			ss = append(ss, proto.Session{ID: "s" + strconv.Itoa(n+3*w.boots[name]), Dir: "/d/" + strconv.Itoa(n), Label: "l", Tabs: []string{"t1"}, Current: "t1"})
		}
	}
	l := &testLink{
		welcome: proto.Welcome{Gen: proto.Gen, Boot: fmt.Sprintf("boot-%s-%d", name, w.boots[name]), Host: "h" + name},
		recv:    make(chan proto.Msg, 1),
	}
	w.links = append(w.links, l)
	w.srv[name] = &server{name: name, link: l, sessions: ss}
	w.f.dialed(mc, mc.gen, l, nil)
	if mc.link != l {
		return
	}
	snap := proto.Snapshot{Rev: 1, Sessions: ss, Tabs: []proto.Tab{}, Panes: []proto.Pane{}, Agents: []proto.Agent{}}
	w.f.linkMessage(linkMsg{mc: mc, gen: mc.gen, msg: proto.Encode(snap)})
}

// consume reads what the fleet sent on name's link up to the next view.set,
// which it returns; view.clear ends the server's view on the way.
func (w *world) consume(s *server) (proto.Msg, bool) {
	sent := s.link.messages()
	for s.consumed < len(sent) {
		m := sent[s.consumed]
		s.consumed++
		switch m.T {
		case proto.TypeViewClear:
			s.view, s.panes = 0, nil
		case proto.TypeViewSet:
			return m, true
		}
	}
	return proto.Msg{}, false
}

func (w *world) reply(name string) {
	s := w.srv[name]
	if s == nil || s.link != w.mc(name).link || w.mc(name).link == nil {
		return
	}
	m, ok := w.consume(s)
	if !ok {
		return
	}
	var vs proto.ViewSet
	if m.Decode(&vs) != nil {
		w.fail("unreadable view.set %s", m.Body)
	}
	sess := ""
	for _, x := range s.sessions {
		if (vs.Session != "" && x.ID == vs.Session) || (vs.Dir != "" && x.Dir == vs.Dir) {
			sess = x.ID
		}
	}
	if vs.Dir != "" && sess == "" {
		sess = "s9" // opened by its directory
	}
	if sess == "" || w.rng.IntN(8) == 0 {
		s.last = proto.ErrReply(m.ID, proto.CodeNoSession, "no")
		w.deliver(name, s.last)
		return
	}
	panes := []string{fmt.Sprintf("p%d1", m.ID)}
	if w.rng.IntN(3) == 0 {
		panes = append(panes, fmt.Sprintf("p%d2", m.ID))
	}
	s.view, s.panes = m.ID, panes
	w.asked[name+"/"+strconv.FormatUint(m.ID, 10)] = sess
	s.last = proto.Request(m.ID, proto.ViewOK{Session: sess, Dir: "/d/1", Tab: "t" + strconv.FormatUint(m.ID, 10), Panes: panes})
	w.deliver(name, s.last)
	// often the full frames follow at once
	if w.rng.IntN(2) == 0 {
		for _, p := range panes {
			w.deliver(name, w.frameOf(name, s, p, true))
		}
	}
}

func (w *world) frameOf(name string, s *server, pane string, full bool) proto.Msg {
	tag := fmt.Sprintf("%s/%d", name, s.view)
	return proto.Encode(proto.Frame{Pane: pane, Seq: 1, Cols: 80, Rows: 23, Full: full, Lines: []proto.Line{{Y: 0, S: tag}}})
}

func (w *world) frame(name string) {
	s := w.srv[name]
	if s == nil || s.link != w.mc(name).link || w.mc(name).link == nil {
		return
	}
	// what the fleet sent before the frame is read first (a clear ends it)
	sent := s.link.messages()
	for s.consumed < len(sent) && sent[s.consumed].T != proto.TypeViewSet {
		if sent[s.consumed].T == proto.TypeViewClear {
			s.view, s.panes = 0, nil
		}
		s.consumed++
	}
	if s.view == 0 {
		return
	}
	w.deliver(name, w.frameOf(name, s, s.panes[w.rng.IntN(len(s.panes))], w.rng.IntN(3) > 0))
}

func (w *world) ended(name string) {
	s := w.srv[name]
	if s == nil || s.view == 0 || s.link != w.mc(name).link {
		return
	}
	sess := w.asked[name+"/"+strconv.FormatUint(s.view, 10)]
	s.view, s.panes = 0, nil
	w.deliver(name, proto.Encode(proto.ViewEnded{Session: sess, Dir: "/d/1"}))
}

// check reads the updates the event produced and tests the invariants.
func (w *world) check() {
	w.t.Helper()
	sw := switcherOf(w.f)
	mode, _, _ := sw.state()
	switch mode {
	case "showing":
		if sw.cur == nil || sw.cur.stale {
			w.fail("showing without a committed view")
		}
	case "stale":
		if sw.cur == nil || !sw.cur.stale {
			w.fail("stale without a stale view")
		}
	case "switching":
		if sw.pend == nil {
			w.fail("switching without a switch")
		}
	case "none":
		if sw.cur != nil || sw.pend != nil {
			w.fail("none with a view or a switch")
		}
	default:
		w.fail("state %q", mode)
	}
	w.f.updates.mu.Lock()
	us := w.f.updates.q
	w.f.updates.q = nil
	w.f.updates.mu.Unlock()
	for _, u := range us {
		switch u.Kind {
		case Shown:
			w.shown, w.shownMachine, w.shownSession = true, u.Target.Machine, u.View.Session
			if u.Target.Session != u.View.Session {
				w.fail("Shown target %+v with view %+v", u.Target, u.View)
			}
		case Frame:
			if u.Frame.T == proto.TypeStateSnapshot {
				// the state of the displayed server, only ever of the shown machine
				if !w.shown || u.Target.Machine != w.shownMachine {
					w.fail("state of %q reached the client while %q is shown (%v)", u.Target.Machine, w.shownMachine, w.shown)
				}
				continue
			}
			if u.Frame.T != proto.TypeFrame {
				continue
			}
			var fr proto.Frame
			if u.Frame.Decode(&fr) != nil || len(fr.Lines) == 0 {
				w.fail("unreadable frame update")
			}
			m, id, _ := strings.Cut(fr.Lines[0].S, "/")
			if !w.shown || m != w.shownMachine || w.asked[m+"/"+id] != w.shownSession {
				w.fail("frame of %s view %s (session %q) reached the client while %q %q is shown (%v)", m, id, w.asked[m+"/"+id], w.shownMachine, w.shownSession, w.shown)
			}
		}
	}
	seen := map[string]bool{}
	for _, l := range w.links {
		for _, m := range l.messages() {
			if m.T != proto.TypeInputKey {
				continue
			}
			var k proto.Key
			_ = m.Decode(&k)
			if seen[k.Text] {
				w.fail("key %s delivered twice", k.Text)
			}
			seen[k.Text] = true
		}
	}
}

func TestTheRandomWorldReachesEveryState(t *testing.T) {
	// the generator is only worth its invariants if the sequences reach
	// every state and commit switches
	reached := map[string]int{}
	commits := 0
	for seed := range 300 {
		w := &world{t: t, seed: uint64(seed), rng: rand.New(rand.NewPCG(uint64(seed), 0x5eed)),
			clock: &stepClock{now: time.Unix(1795000000, 0)}, srv: map[string]*server{}, boots: map[string]int{}, asked: map[string]string{}}
		w.f = New(Options{Machines: []config.Machine{{Name: "M", Connect: true}, {Name: "K"}}, Dialer: waitDialer{}, Clock: w.clock, Cols: 80, Rows: 23})
		w.f.updates.close()
		w.f.updates = &pipe[Update]{wake: make(chan struct{}, 1), done: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		w.f.ctx = ctx
		for _, mc := range w.f.machines {
			if mc.local || mc.connect {
				w.f.dial(mc)
			}
		}
		w.step()
		for range 60 {
			w.event()
			w.f.drain()
			w.f.settle()
			mode, _, _ := switcherOf(w.f).state()
			reached[mode]++
			w.f.updates.mu.Lock()
			for _, u := range w.f.updates.q {
				if u.Kind == Shown {
					commits++
				}
			}
			w.f.updates.q = nil
			w.f.updates.mu.Unlock()
		}
		w.finish()
	}
	for _, m := range []string{"none", "showing", "switching", "stale"} {
		if reached[m] == 0 {
			t.Errorf("no sequence reached %q: %v", m, reached)
		}
	}
	if commits < 100 {
		t.Errorf("%d commits in 300 sequences", commits)
	}
}
