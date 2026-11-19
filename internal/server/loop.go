package server

import (
	"path/filepath"
	"time"

	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/server/model"
	"devtools/internal/session"
	"devtools/internal/vt"
)

// The state loop (3.2.3). One goroutine owns the model, the connection
// registry, the panes, sizing and timers; everything else reaches them by
// posting a function into its mailbox. It blocks on nothing but the
// mailbox: sending is queueing onto a lane, closing a pane happens on a
// goroutine of its own, and so does reading the process table.

// core is the state the loop owns. It is the Core of the extensions.
type core struct {
	s *Server

	model *model.State
	known *proto.State // the model as the deltas describe it, for classifying changes and lookups

	panes    map[string]paneRef   // running and exited panes, by id
	handles  map[string]*handle   // the same panes as connections see them (view.go)
	gen      uint64               // the number of the last pane object started
	helloEnv map[string]string    // display variables of the newest local interactive hellos (3.2.1)
	colors   map[string]vt.Colors // per session, the colours its panes were last given (size.go)
	seen     map[string]bool      // panes a focused connection displays (focus.go)
	ticks    uint64               // the order of interactions (size.go)

	reg registry
	out deltas

	observer Observer // extensionObserver() at New; tests of the core set their own

	// The process scan (scan.go).
	procs        func() ([]platform.Proc, error)
	fg           map[string]int       // per pane, the foreground group last read
	lastObserved map[string]time.Time // per pane, the last Observer.Foreground
	lastFgAll    time.Time
	lastScan     time.Time
	scanning     bool
	scanAgain    bool

	saveDirty bool // a change since the last save was handed to the saver
	saveArmed bool // a save is scheduled
	final     bool // the stop took the state: nothing is saved any more
}

// paneRef is one pane object with the cancel of its watch and the number
// of the start that made it.
type paneRef struct {
	p      Pane
	cancel func()
	gen    uint64
}

// procTable reads the process table; the tests of the core replace it
// before they start a server.
var procTable = platform.Procs

func newCore(s *Server, m *model.State) *core {
	c := &core{
		s:            s,
		model:        m,
		known:        proto.NewState(m.Snapshot()),
		panes:        map[string]paneRef{},
		handles:      map[string]*handle{},
		helloEnv:     map[string]string{},
		colors:       map[string]vt.Colors{},
		seen:         map[string]bool{},
		observer:     extensionObserver(),
		procs:        procTable,
		fg:           map[string]int{},
		lastObserved: map[string]time.Time{},
	}
	c.reg.init()
	return c
}

// loop runs the functions of the mailbox, in order, until quit. After each
// it brings what follows from the state up to date: sizes and colours,
// displayed sets, focus and what the user sees (sync).
func (s *Server) loop() {
	defer s.wg.Done()
	for {
		select {
		case f := <-s.mail:
			f(s.core)
			s.core.sync()
		case <-s.quit:
			return
		}
	}
}

// sync brings sizes, colours, displayed sets, focus reports and seen panes
// in line with the model and the connections. It runs after every function
// of the mailbox, never inside one, so no delta it causes can come between
// the parts of another. Applying a size can change what else is due, so it
// runs until nothing changes, at most a few rounds.
func (c *core) sync() {
	for range 4 {
		rev := c.out.rev
		c.syncSizes()
		c.syncViews()
		c.syncFocus()
		c.syncSeen()
		if c.out.rev == rev {
			return
		}
	}
}

// post hands f to the loop and waits while the mailbox is full; it gives up
// when the loop has ended. The readers of connections post with it, which is
// what keeps the requests of one connection in order (W6).
func (s *Server) post(f func(*core)) {
	select {
	case s.mail <- f:
	case <-s.quit:
	}
}

// postSoon hands f to the loop without blocking the caller: when the mailbox
// is full the hand-over waits on a goroutine of its own. For callers that
// must return quickly (pane notifications, timers, Core.Post).
func (s *Server) postSoon(f func(*core)) {
	select {
	case s.mail <- f:
	default:
		go s.post(f)
	}
}

// after runs f on the loop once d has passed.
func (c *core) after(d time.Duration, f func(*core)) {
	s := c.s
	time.AfterFunc(d, func() { s.postSoon(f) })
}

// apply carries out the result of one model operation, in the order of
// 3.2.3: the effects Spawn, Resize and Kill (a new pane exists before anyone
// is told), then the state delta into every interactive connection's
// control queue, with the displayed set of every viewer whose set it changes
// (broadcast), then Ended. The reply to the request that caused it is queued
// by the caller after apply returned (W2). A change caused by a request is
// sent at once (now); one caused by a pane or an extension may be cosmetic
// and wait for CosmeticDelay (4.2.2).
func (c *core) apply(r model.Result, now bool) {
	for _, e := range r.Effects {
		switch e.Kind {
		case model.Spawn:
			c.spawn(e)
		case model.Resize:
			if ref, ok := c.panes[e.Pane]; ok {
				if h := c.handles[e.Pane]; h != nil {
					h.setCols(e.Cols)
				}
				ref.p.Resize(e.Cols, e.Rows)
			}
		case model.Kill:
			c.kill(e.Pane)
		}
	}
	if len(r.Changes) > 0 {
		c.publish(r.Changes, now)
		c.changed(r.Changes)
	}
	for _, e := range r.Effects {
		if e.Kind == model.Ended {
			c.ended(e.Session, e.Dir)
		}
	}
	for _, ch := range r.Changes {
		if ch.Op == proto.OpDel && ch.Kind == proto.KindPane {
			delete(c.fg, ch.ID)
			delete(c.lastObserved, ch.ID)
			delete(c.seen, ch.ID)
			if c.observer != nil {
				c.observer.Gone(c, ch.ID)
			}
		}
	}
}

// --- panes ---

// paneEnv builds a pane's environment; pane.Env, replaced by the tests of
// the core, whose panes are fakes.
var paneEnv = pane.Env

// defaultColors are the colours of hello.colors when a hello carries none
// (4.2.1).
var defaultColors = vt.Colors{Fg: "#d0d0d0", Bg: "#000000", Dark: true}

// spawn starts the program of a pane (effect Spawn) with the colours of its
// session's size owner. An exited pane that is spawned again keeps its id
// and its handle, so its viewers' frames go on; its old pane object is
// closed.
func (c *core) spawn(e model.Effect) {
	if old, ok := c.panes[e.Pane]; ok {
		c.release(old)
	}
	h := c.handles[e.Pane]
	if h == nil {
		h = newHandle(e.Pane)
		c.handles[e.Pane] = h
	}
	h.setCols(e.Cols)
	o := c.s.o
	po := pane.Options{
		ID:        e.Pane,
		Dir:       e.Dir,
		Env:       paneEnv(o.Env, c.helloEnvCopy(), e.Pane, c.s.socketPath()),
		Cols:      e.Cols,
		Rows:      e.Rows,
		History:   o.History,
		Colors:    c.colorsFor(e.Session),
		OnReflow:  h.onReflow,
		KillGrace: o.KillGrace,
		SyncHold:  o.SyncHold,
	}
	po.Argv, po.Arg0 = c.program()
	c.gen++
	s, id, gen := c.s, e.Pane, c.gen
	po.Notify = func(ev pane.Event) {
		if ev.Pane == "" {
			ev.Pane = id
		}
		s.postSoon(func(c *core) { c.paneEvent(ev, gen) })
	}
	p := o.NewPane(po)
	c.s.track(p)
	cancel := p.Watch(h.wake)
	c.panes[e.Pane] = paneRef{p: p, cancel: cancel, gen: gen}
	delete(c.fg, e.Pane)
	h.swap(p)
}

// program is the program of a new pane: Options.Shell, or $SHELL of the
// server's environment as a login shell (argv[0] is "-" and its base name,
// 3.2.1).
func (c *core) program() (argv []string, arg0 string) {
	if len(c.s.o.Shell) > 0 {
		return append([]string(nil), c.s.o.Shell...), ""
	}
	shell := "/bin/sh"
	for _, kv := range c.s.o.Env {
		if len(kv) > 6 && kv[:6] == "SHELL=" {
			shell = kv[6:]
		}
	}
	return []string{shell}, "-" + filepath.Base(shell)
}

func (c *core) helloEnvCopy() map[string]string {
	m := make(map[string]string, len(c.helloEnv))
	for k, v := range c.helloEnv {
		m[k] = v
	}
	return m
}

// kill closes a pane's program (effect Kill) and forgets the pane object
// and its handle.
func (c *core) kill(id string) {
	if ref, ok := c.panes[id]; ok {
		delete(c.panes, id)
		c.release(ref)
	}
	if h := c.handles[id]; h != nil {
		delete(c.handles, id)
		h.end()
	}
}

// release cancels a pane's watch and closes it on a goroutine of its own:
// Close waits for the child, the loop does not.
func (c *core) release(ref paneRef) {
	if ref.cancel != nil {
		ref.cancel()
	}
	p, s := ref.p, c.s
	go func() {
		p.Close()
		s.untrack(p)
	}()
}

// closePanes closes every pane without waiting; for halt.
func (c *core) closePanes() {
	for id := range c.panes {
		c.kill(id)
	}
}

// --- Core ---

var _ Core = (*core)(nil)

func (c *core) State() proto.Snapshot {
	snap := c.model.Snapshot()
	snap.Rev = c.out.rev
	return snap
}

func (c *core) Conns() []Conn {
	list := c.reg.list()
	out := make([]Conn, len(list))
	for i, cn := range list {
		out[i] = cn
	}
	return out
}

func (c *core) Reply(cn Conn, id uint64, result any) {
	if x, ok := cn.(*conn); ok {
		c.send(x, proto.OKReply(id, result))
	}
}

func (c *core) Fail(cn Conn, id uint64, code, text string) {
	if x, ok := cn.(*conn); ok {
		c.send(x, proto.ErrReply(id, code, session.Sanitize(text, session.MaxText)))
	}
}

func (c *core) PaneSession(paneID string) (string, bool) {
	p, ok := c.known.Panes[paneID]
	if !ok {
		return "", false
	}
	return p.Session, true
}

func (c *core) OpenSession(dir, label string) (string, bool, string) {
	id, created, err := c.openSession(dir, label)
	if err != nil {
		return "", false, err.Code
	}
	return id, created, ""
}

func (c *core) CloseSession(sessionID string) bool {
	if _, ok := c.known.Sessions[sessionID]; !ok {
		return false
	}
	c.apply(c.model.CloseSession(sessionID), true)
	return true
}

func (c *core) PutAgent(paneID string, a proto.Agent) {
	c.apply(c.model.PutAgent(paneID, a), false)
}

func (c *core) DelAgent(paneID string) {
	c.apply(c.model.DelAgent(paneID), false)
}

func (c *core) After(d time.Duration, f func(Core)) {
	c.after(d, func(c *core) { f(c) })
}

func (c *core) Post(f func(Core)) {
	c.s.postSoon(func(c *core) { f(c) })
}
