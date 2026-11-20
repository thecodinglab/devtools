package fleet

import (
	"maps"
	"slices"
	"time"

	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// The switch state machine of 3.5.4 and the stale view of 3.5.5. It is the
// viewer of the fleet (fleet.go): the core hands it every message of a link
// it does not handle itself, and tells it when a link comes up or goes
// down. Everything here runs on the loop.
//
// The window is in one of four states:
//
//	none       nothing is shown (cur == nil, no switch)
//	showing    cur is shown and its link is up
//	switching  a switch to pend.target runs; cur, if any, stays on screen
//	stale      cur's link is lost; its last picture stays
//
// A switch commits only once its view.ok and one full frame of every pane
// it lists arrived; until then not one of its frames reaches the client.

// Bounds and timings of 3.5.4.
const (
	holdFor     = 2 * time.Second  // keys typed this long after a switch starts are held (F14)
	holdMax     = 256              // held input events
	linkWait    = 10 * time.Second // a switch waits this long for its machine's link
	answerWait  = 10 * time.Second // and this long for view.ok once view.set is sent (not in 3.5.4: a server that never answers)
	framesWait  = 5 * time.Second  // and this long for the full frames after view.ok
	maxPartials = 512              // partial frames buffered before the last full one
	maxViewPane = proto.MaxPanes   // the panes of a tab (4.1)

	// What a switch buffers in all, the pieces of full frames included
	// (SEC12): the 512 partial frames of 3.5.4 plus the first full frame of
	// each pane, and at most 64 MiB of message bodies. A link that sends
	// more before the last full frame is one that did not answer.
	maxFrames   = maxPartials + maxViewPane
	maxBufBytes = 64 << 20
)

// kind is why a switch runs.
type kind int

const (
	kindUser     kind = iota // Switch, a routed ui.switch, Step
	kindFallback             // the displayed session ended or did not come back
	kindResume               // the stale view's machine is back (3.5.5)
)

func init() {
	newViewer = func(f *Fleet) viewer { return newSwitcher(f) }
}

// view is the committed view: what the window shows, or showed before its
// link was lost.
type view struct {
	mc     *machine
	target session.Target // Machine, Session (the server's id), Tab, Pane
	ok     proto.ViewOK
	id     uint64 // the view.set that made the server show it
	boot   string // welcome.boot of the server that showed it
	label  string
	stale  bool
	prob   hub.Problem
	gone   bool // the session cannot come back: a rollback to it ends in none
	resume bool // the link came back while a switch away from the stale view ran

	// view.set requests for other targets sent on mc's link that were not
	// refused yet: while one is out, the server may show something else
	foreign map[uint64]bool
	restore uint64 // id of a view.set that shows this view again; 0 none
	again   uint64 // id of a view.set that selects a tab or pane of it (3.5.4 step 2)
}

// pending is the switch that runs.
type pending struct {
	kind   kind
	mc     *machine
	target session.Target // as asked; Machine is mc's name
	id     uint64         // the view.set; 0 until it is sent
	cols   int            // the size the target's server was last told: by view.set, or a view.resize after it
	rows   int
	gotOK  bool
	ok     proto.ViewOK
	need   map[string]bool     // panes of view.ok still without a complete full frame
	panes  map[string]*paneSeq // where each pane of view.ok is in its sequence
	frames []bufFrame          // buffered frames of the target, in arrival order
	parts  int                 // partial frames among them
	bytes  int                 // the bodies of the buffered frames
	wait   *timer              // the timer of the current phase
	notice string              // what the commit tells the user (restarted)
}

// bufFrame is one buffered frame message of a pending target.
type bufFrame struct {
	pane string
	full bool
	msg  proto.Msg
}

// paneSeq is the sequence of one pane of a pending target as the client's
// receiver will check it (4.2.4): what is buffered for the pane starts with
// its frame of seq 1, which is full, and goes on without a gap.
type paneSeq struct {
	seq  uint64 // the last complete frame; 0 before the first
	cols int    // its size
	rows int

	// the frame whose pieces are buffered (more), when one is
	piece bool
	pseq  uint64
	pcols int
	prows int
	pfull bool
}

// switcher is the viewer of a fleet with the switch state machine.
type switcher struct {
	f    *Fleet
	cur  *view
	pend *pending
	hist history

	// the hold buffer of 3.5.4 step 3: open for the first 2 s of a switch
	hold     []proto.Msg
	holdOpen bool
	holdLost bool // events were dropped: beyond 256, or after the 2 s
	holdTime *timer

	// per machine: the id of the last view.ok on its current link, which
	// says what the server shows on that connection now (0: nothing)
	lastOK map[*machine]uint64

	// requests of the client in flight, per machine (route.go)
	reqs map[*machine]map[uint64]request

	// the state of the displayed machine the client was handed last
	// (syncState): the object and its revision. replaceState makes a new
	// object, applyDelta moves the revision, so the pair changes with every
	// change of the state.
	sentSt  *proto.State
	sentRev uint64
}

func newSwitcher(f *Fleet) *switcher {
	return &switcher{
		f:      f,
		lastOK: map[*machine]uint64{},
		reqs:   map[*machine]map[uint64]request{},
	}
}

// switcherOf is the switch state machine of f, nil when a test replaced it.
func switcherOf(f *Fleet) *switcher {
	s, _ := f.view.(*switcher)
	return s
}

// --- the viewer interface (fleet.go) ---

func (s *switcher) state() (string, session.Target, bool) {
	// The core asks for the mode after every step of the loop that set it
	// dirty, which every state.snapshot and state.delta does (stateChanged),
	// at the latest when the 50 ms of the summary's rate limit end: a change
	// of the displayed state that no message of the link follows reaches
	// the client here.
	s.syncState()
	stale := s.cur != nil && s.cur.stale
	switch {
	case s.pend != nil && s.pend.kind != kindResume:
		return "switching", s.pend.target, stale
	case s.cur == nil:
		return "none", session.Target{}, false
	case stale:
		return "stale", s.cur.target, true
	}
	return "showing", s.cur.target, false
}

func (s *switcher) holds(machine string) bool {
	return (s.cur != nil && s.cur.mc.name == machine) || (s.pend != nil && s.pend.mc.name == machine)
}

func (s *switcher) receive(mc *machine, m proto.Msg) {
	// a change of the displayed state precedes what the link sent after it
	// (W4: the state of a tab change comes before its frames)
	if c := s.cur; c != nil && c.mc == mc {
		s.syncState()
	}
	switch m.T {
	case proto.TypeViewOK:
		if m.ID != 0 {
			s.viewOK(mc, m)
		}
	case proto.TypeOK, proto.TypeErr, proto.TypeStateSnapshot:
		if m.ID == 0 {
			return
		}
		if s.reply(mc, m) {
			return
		}
		if m.T == proto.TypeErr {
			s.viewErr(mc, m)
		}
	case proto.TypeFrame:
		s.frame(mc, m)
	case proto.TypeEventReflow:
		if s.displays(mc) {
			s.f.update(Update{Kind: Frame, Target: s.cur.target, Frame: m})
		}
	case proto.TypeViewEnded:
		s.viewEnded(mc, m)
	case proto.TypeEventClipboard:
		s.clipboard(mc, m)
	case proto.TypeEventBell:
		s.bell(mc, m)
	case proto.TypeUIHub, proto.TypeUISwitch:
		s.routed(mc, m)
	}
	// anything else is not for the window
}

func (s *switcher) linkUp(mc *machine, sameBoot bool) {
	s.lastOK[mc] = 0
	if p := s.pend; p != nil && p.mc == mc && p.id == 0 {
		s.sendViewSet(p)
	}
	if c := s.cur; c != nil && c.mc == mc && c.stale {
		if s.pend != nil {
			// a switch away from the stale view runs: its rollback resumes
			c.resume = true
			return
		}
		s.resumeStale()
	}
}

func (s *switcher) linkDown(mc *machine, p hub.Problem) {
	s.lastOK[mc] = 0
	s.lost(mc)
	if pd := s.pend; pd != nil && pd.mc == mc {
		if pd.kind == kindResume {
			// the stale view stays stale; the link is supervised
			s.endPending()
		} else {
			s.rollback(ui.Notice("machine", s.name(mc), p.Reason, p.Text), false)
		}
	}
	if c := s.cur; c != nil && c.mc == mc {
		c.restore, c.again, c.foreign = 0, 0, nil
		c.resume = false
		c.stale, c.prob = true, p
		s.f.update(Update{Kind: Stale, Target: c.target, Problem: p})
		s.f.dirty = true
	}
}

func (s *switcher) resized() {
	msg := proto.Encode(proto.ViewResize{Cols: s.f.cols, Rows: s.f.rows})
	sent := map[*machine]bool{}
	if c := s.cur; c != nil && !c.stale && s.lastOK[c.mc] != 0 {
		s.f.send(c.mc, msg)
		sent[c.mc] = true
	}
	p := s.pend
	if p == nil || p.id == 0 {
		// a view.set not sent yet carries the size of the moment it is sent
		return
	}
	if p.gotOK && !sent[p.mc] && s.lastOK[p.mc] == p.id && s.f.send(p.mc, msg) {
		sent[p.mc] = true
	}
	// The target's server knows the new size only when the view.resize went
	// out on its link after the view.set (on a shared link, the one of the
	// shown view). Otherwise its view.ok sends it (viewOK).
	if sent[p.mc] {
		p.cols, p.rows = s.f.cols, s.f.rows
	}
}

func (s *switcher) focusChanged() {
	// Focus (fleet.go) told the displayed link. A switch tells its link the
	// focus right before its view.set (sendViewSet).
}

// --- switching (3.5.4) ---

// switchTo is Switch(t) of 3.5.4.
func (s *switcher) switchTo(t session.Target, k kind) {
	mc, ok := s.valid(t)
	if !ok {
		name := t.Machine
		if name == "" {
			name = s.name(s.f.machines[0])
		}
		s.f.notice(ui.Notice("refused", cleanLabel(name), "not a session that can be shown"))
		return
	}
	t.Machine = mc.name
	if t.Dir != "" {
		t.Name = cleanLabel(t.Name)
	}
	// step 2: the target is what is shown
	if c := s.cur; c != nil && !c.stale && s.pend == nil && c.mc == mc && s.sameSession(c, t) {
		if t.Tab != "" || t.Pane != "" {
			s.selectAgain(t)
		}
		return
	}
	// the same switch again is the switch that runs
	if p := s.pend; p != nil && p.kind != kindResume && p.mc == mc && sameTarget(p.target, t) {
		return
	}
	s.start(mc, t, k, "")
}

// valid is step 1 of 3.5.4: the machine is "" or configured, the ids have
// the server's form, and exactly one of Session and Dir is set.
func (s *switcher) valid(t session.Target) (*machine, bool) {
	mc := s.f.byName[t.Machine]
	if mc == nil || mc.dropped {
		return nil, false
	}
	if (t.Session == "") == (t.Dir == "") {
		return nil, false
	}
	if t.Session != "" && !validID('s', t.Session) {
		return nil, false
	}
	if t.Dir != "" && !validPath(t.Dir) {
		return nil, false
	}
	if t.Tab != "" && !validID('t', t.Tab) {
		return nil, false
	}
	if t.Pane != "" && !validID('p', t.Pane) {
		return nil, false
	}
	return mc, true
}

// sameSession reports whether t names the session c shows.
func (s *switcher) sameSession(c *view, t session.Target) bool {
	if t.Session != "" {
		return t.Session == c.target.Session
	}
	return t.Dir == c.ok.Dir
}

func sameTarget(a, b session.Target) bool {
	return a.Machine == b.Machine && a.Session == b.Session && a.Dir == b.Dir && a.Tab == b.Tab && a.Pane == b.Pane
}

// selectAgain sends view.set for the shown session with a tab or a pane, so
// they are selected (3.5.4 step 2). Its view.ok shows the view again.
func (s *switcher) selectAgain(t session.Target) {
	c := s.cur
	id := c.mc.newID()
	vs := proto.ViewSet{Session: c.target.Session, Tab: t.Tab, Pane: t.Pane, Cols: s.f.cols, Rows: s.f.rows}
	if !s.f.send(c.mc, proto.Request(id, vs)) {
		return
	}
	c.again = id
	c.target.Tab, c.target.Pane = t.Tab, t.Pane
}

// start enters Switching (step 3) towards t on mc, replacing a switch that
// runs.
func (s *switcher) start(mc *machine, t session.Target, k kind, notice string) {
	if k != kindResume && (s.pend == nil || s.pend.kind == kindResume) {
		s.openHold()
	}
	if s.pend != nil {
		s.replace(mc)
	}
	p := &pending{kind: k, mc: mc, target: t, notice: notice}
	s.pend = p
	s.f.dirty = true
	if mc.online() {
		s.sendViewSet(p)
		return
	}
	// step 4: dial, and wait at most 10 s
	s.f.dial(mc)
	p.wait = s.f.after(linkWait, func() {
		if s.pend != p {
			return
		}
		if mc.dropped {
			s.rollback("", false)
			return
		}
		s.rollback(ui.Notice("still-connecting", s.name(mc)), false)
	})
}

// replace forgets the switch that runs because another one starts on mc:
// its link is cleared when it is neither the shown one nor the new one.
func (s *switcher) replace(next *machine) {
	p := s.pend
	p.wait.stop()
	s.pend = nil
	if p.id != 0 && p.mc != next && (s.cur == nil || s.cur.mc != p.mc) {
		s.f.send(p.mc, proto.Encode(proto.ViewClear{}))
	}
	if p.mc != next {
		s.f.startIdle(p.mc)
	}
}

// sendViewSet is step 5: view.set on the target's link.
func (s *switcher) sendViewSet(p *pending) {
	p.wait.stop()
	t := p.target
	vs := proto.ViewSet{Session: t.Session, Dir: t.Dir, Name: t.Name, Tab: t.Tab, Pane: t.Pane, Cols: s.f.cols, Rows: s.f.rows}
	id := p.mc.newID()
	s.f.send(p.mc, proto.Encode(proto.Focus{Focused: s.f.focused}))
	if !s.f.send(p.mc, proto.Request(id, vs)) {
		// the link is over; its end arrives as linkDown
		return
	}
	p.id, p.cols, p.rows = id, vs.Cols, vs.Rows
	if c := s.cur; c != nil && c.mc == p.mc && !c.stale {
		if c.foreign == nil {
			c.foreign = map[uint64]bool{}
		}
		c.foreign[id] = true
	}
	p.wait = s.f.after(answerWait, func() {
		if s.pend == p && !p.gotOK {
			s.rollback(ui.Notice("no-answer", s.name(p.mc)), true)
		}
	})
}

// viewOK is a view.ok on mc's link.
func (s *switcher) viewOK(mc *machine, m proto.Msg) {
	s.lastOK[mc] = m.ID
	if c := s.cur; c != nil && c.mc == mc {
		delete(c.foreign, m.ID)
	}
	var ok proto.ViewOK
	readable := m.Decode(&ok) == nil && cleanViewOK(&ok)
	if c := s.cur; c != nil && c.mc == mc && (m.ID == c.restore || m.ID == c.again) {
		if !readable {
			s.ended(c)
			return
		}
		c.restore, c.again = 0, 0
		c.id, c.ok = m.ID, ok
		c.target.Session = ok.Session
		c.foreign = nil
		if s.pend == nil {
			s.f.update(Update{Kind: Shown, Target: c.target, View: ok})
			s.sendState(c)
		}
		return
	}
	p := s.pend
	if p == nil || p.mc != mc || p.id != m.ID || p.gotOK {
		return // a reply of a switch that was replaced
	}
	if !readable {
		s.rollback(ui.Notice("no-answer", s.name(mc)), true)
		return
	}
	p.wait.stop()
	p.gotOK, p.ok = true, ok
	p.need = map[string]bool{}
	p.panes = map[string]*paneSeq{}
	for _, id := range ok.Panes {
		p.need[id] = true
		p.panes[id] = &paneSeq{}
	}
	if (p.cols != s.f.cols || p.rows != s.f.rows) &&
		s.f.send(mc, proto.Encode(proto.ViewResize{Cols: s.f.cols, Rows: s.f.rows})) {
		p.cols, p.rows = s.f.cols, s.f.rows
	}
	if len(p.need) == 0 {
		s.commit()
		return
	}
	// step 6: one full frame per pane, at most 5 s
	p.wait = s.f.after(framesWait, func() {
		if s.pend == p {
			s.rollback(ui.Notice("no-answer", s.name(mc)), true)
		}
	})
}

// cleanViewOK checks the ids of a view.ok and bounds it (SEC10); a
// directory that is not a path is left out.
func cleanViewOK(ok *proto.ViewOK) bool {
	if !validID('s', ok.Session) || !validID('t', ok.Tab) {
		return false
	}
	if len(ok.Panes) > maxViewPane {
		return false
	}
	for _, id := range ok.Panes {
		if !validID('p', id) {
			return false
		}
	}
	if !validPath(ok.Dir) {
		ok.Dir = ""
	}
	return true
}

// viewErr is an err reply on mc's link that answers no request of the
// client: a view.set was refused, and the server changed nothing.
func (s *switcher) viewErr(mc *machine, m proto.Msg) {
	r, _ := proto.AsReply(m)
	if c := s.cur; c != nil && c.mc == mc {
		delete(c.foreign, m.ID)
		switch m.ID {
		case c.restore:
			// the view to go back to is gone
			c.restore = 0
			s.ended(c)
			return
		case c.again:
			c.again = 0
			s.f.notice(ui.Notice("refused", s.name(mc), cleanText(r.Text)))
			return
		}
	}
	p := s.pend
	if p == nil || p.mc != mc || p.id != m.ID || p.gotOK {
		return
	}
	if p.kind == kindResume {
		s.endPending()
		s.lostView()
		return
	}
	s.rollback(ui.Notice("refused", s.name(mc), cleanText(r.Text)), false)
}

// frameHead is the part of a frame the fleet reads.
type frameHead struct {
	Pane string `json:"pane"`
	Seq  uint64 `json:"seq"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Full bool   `json:"full"`
	More bool   `json:"more"`
}

// frame is a frame on mc's link: forwarded when it is of the shown view,
// buffered when it is of the switch's target after its view.ok, dropped
// otherwise.
func (s *switcher) frame(mc *machine, m proto.Msg) {
	if p := s.pend; p != nil && p.mc == mc && p.gotOK && s.lastOK[mc] == p.id {
		s.buffer(p, m)
		return
	}
	if s.displays(mc) {
		s.f.update(Update{Kind: Frame, Target: s.cur.target, Frame: m})
	}
}

// displays reports whether mc's link shows the committed view now.
func (s *switcher) displays(mc *machine) bool {
	c := s.cur
	return c != nil && !c.stale && c.mc == mc && s.lastOK[mc] == c.id
}

// buffer keeps a frame of the target (step 6) so that the frames forwarded
// at the commit are what the client's receiver accepts after Shown (4.2.4):
// for each pane of view.ok its full frame of seq 1 and every frame after it,
// with no gap. A full frame of seq 1 starts the pane again (the server's
// displayed set changed), and what the pane had is dropped; a frame of a
// pane view.ok does not list, or one that does not continue its pane's
// sequence, is dropped. What is kept is bounded whether the frames are full
// or not (SEC12): beyond the bounds the switch rolls back.
func (s *switcher) buffer(p *pending, m proto.Msg) {
	var h frameHead
	if m.Decode(&h) != nil {
		return
	}
	ps := p.panes[h.Pane]
	if ps == nil {
		return // view.ok said which panes the frames are of (4.2.3)
	}
	ok, restart := ps.fits(h)
	if !ok {
		return
	}
	if restart {
		s.forget(p, h.Pane)
		*ps = paneSeq{}
		p.need[h.Pane] = true
	}
	p.frames = append(p.frames, bufFrame{pane: h.Pane, full: h.Full, msg: m})
	p.bytes += len(m.Body)
	if !h.Full {
		p.parts++
	}
	if h.More {
		ps.piece, ps.pseq, ps.pcols, ps.prows, ps.pfull = true, h.Seq, h.Cols, h.Rows, h.Full
	} else {
		ps.piece = false
		ps.seq, ps.cols, ps.rows = h.Seq, h.Cols, h.Rows
		delete(p.need, h.Pane)
	}
	if len(p.need) == 0 {
		s.commit()
		return
	}
	if p.parts > maxPartials || len(p.frames) > maxFrames || p.bytes > maxBufBytes {
		s.rollback(ui.Notice("no-answer", s.name(p.mc)), true)
	}
}

// fits reports whether the frame h continues the pane's sequence the way
// the client's receiver checks it (panes.Screen.Apply): a further piece of
// the frame being assembled, or the next seq, full when it is the first or
// changes the size. restart is a full frame of seq 1 that is not such a
// piece: the pane starts again with it.
func (ps *paneSeq) fits(h frameHead) (ok, restart bool) {
	if h.Cols < 0 || h.Cols > proto.MaxCols || h.Rows < 0 || h.Rows > proto.MaxRows {
		return false, false
	}
	if ps.piece && h.Seq == ps.pseq && h.Cols == ps.pcols && h.Rows == ps.prows && h.Full == ps.pfull {
		return true, false
	}
	if h.Seq == 1 && h.Full {
		return true, true
	}
	if ps.piece || ps.seq == 0 || h.Seq != ps.seq+1 {
		return false, false
	}
	if !h.Full && (h.Cols != ps.cols || h.Rows != ps.rows) {
		return false, false
	}
	return true, false
}

// forget drops the buffered frames of pane.
func (s *switcher) forget(p *pending, pane string) {
	kept := p.frames[:0]
	for _, b := range p.frames {
		if b.pane == pane {
			p.bytes -= len(b.msg.Body)
			if !b.full {
				p.parts--
			}
			continue
		}
		kept = append(kept, b)
	}
	clear(p.frames[len(kept):])
	p.frames = kept
}

// commit is step 7 to 9: the target is shown.
func (s *switcher) commit() {
	p := s.pend
	p.wait.stop()
	s.pend = nil
	old := s.cur
	if old != nil && old.mc != p.mc && !old.stale {
		s.f.send(old.mc, proto.Encode(proto.ViewClear{}))
	}
	t := session.Target{Machine: p.mc.name, Session: p.ok.Session, Tab: p.ok.Tab, Pane: p.target.Pane}
	c := &view{mc: p.mc, target: t, ok: p.ok, id: p.id, boot: p.mc.welcome.Boot}
	if p.mc.st != nil {
		c.label = p.mc.st.Sessions[p.ok.Session].Label
	}
	s.cur = c
	s.f.update(Update{Kind: Shown, Target: t, View: p.ok})
	s.sendState(c) // the state comes right after Shown, before the frames
	for _, b := range p.frames {
		s.f.update(Update{Kind: Frame, Target: t, Frame: b.msg})
	}
	s.hist.push(histEntry{machine: p.mc.name, session: p.ok.Session, dir: p.ok.Dir, boot: c.boot, label: c.label})
	// step 8: what was held goes to the target, in order
	held, lost := s.hold, s.holdLost
	s.closeHold()
	if len(held) > 0 {
		pane := s.focusedPane(p)
		for _, m := range held {
			if r, ok := retarget(m, pane); ok {
				s.f.send(p.mc, r)
			}
		}
	}
	if lost {
		s.f.notice(ui.Notice("keys-dropped"))
	}
	if p.notice != "" {
		s.f.notice(p.notice)
	}
	// step 9: the machine shown before may now go idle
	if old != nil && old.mc != p.mc {
		s.f.startIdle(old.mc)
	}
	s.f.dirty = true
}

// focusedPane is the pane of the target that gets the held keys: the pane
// asked for, else the focused pane of the shown tab, else its first.
func (s *switcher) focusedPane(p *pending) string {
	for _, id := range p.ok.Panes {
		if id == p.target.Pane {
			return id
		}
	}
	if st := p.mc.st; st != nil {
		if tab, ok := st.Tabs[p.ok.Tab]; ok {
			for _, id := range p.ok.Panes {
				if id == tab.Focused {
					return id
				}
			}
		}
	}
	if len(p.ok.Panes) > 0 {
		return p.ok.Panes[0]
	}
	return ""
}

// rollback ends the switch without showing its target (the table of 3.5.4).
// unanswered is set when the target's server may show it: its link is
// cleared, or, when it is the link of the shown view, that view is set
// again. cancelled is a rollback the client asked for.
func (s *switcher) rollback(notice string, unanswered bool) {
	s.rollbackAs(notice, unanswered, false)
}

func (s *switcher) rollbackAs(notice string, unanswered, cancelled bool) {
	p := s.pend
	if p == nil {
		return
	}
	p.wait.stop()
	s.pend = nil
	s.closeHold()
	c := s.cur
	if p.id != 0 && (c == nil || c.mc != p.mc) && (unanswered || cancelled) {
		s.f.send(p.mc, proto.Encode(proto.ViewClear{}))
	}
	if notice != "" {
		s.f.notice(notice)
	}
	s.f.dirty = true
	switch {
	case c == nil:
		// Switching(none→T): the hub opens, unless the client acted
		if !cancelled {
			s.f.update(Update{Kind: OpenHub, Hub: HubRequest{Mode: hub.ModeAll}})
		}
	case c.gone:
		// the view to go back to ended meanwhile: the history decides
		s.cur = nil
		s.fallback(c.mc, cancelled)
	case c.stale:
		// a resume that failed is not tried again at once: the view stays
		// stale until its link comes back, or the user picks
		if p.kind != kindResume && (c.resume || c.mc.online()) {
			c.resume = false
			s.resumeStale()
		}
	default:
		s.restoreIfReplaced()
	}
	if s.pend == nil || s.pend.mc != p.mc {
		s.f.startIdle(p.mc)
	}
}

// restoreIfReplaced sets the shown view again on its link when a view.set
// for another target went out there and was not refused (SW7).
func (s *switcher) restoreIfReplaced() {
	c := s.cur
	if c == nil || c.stale || c.restore != 0 {
		return
	}
	if len(c.foreign) == 0 && s.lastOK[c.mc] == c.id {
		return
	}
	id := c.mc.newID()
	vs := proto.ViewSet{Session: c.target.Session, Cols: s.f.cols, Rows: s.f.rows}
	if s.f.send(c.mc, proto.Request(id, vs)) {
		c.restore = id
		c.foreign = nil
	}
}

// endPending forgets the switch that runs without any other effect.
func (s *switcher) endPending() {
	if s.pend != nil {
		s.pend.wait.stop()
		s.pend = nil
		s.f.dirty = true
	}
	s.closeHold()
}

// cancel is Cancel(): a switch the user started is dropped, V stays.
func (s *switcher) cancel() {
	if p := s.pend; p != nil && p.kind != kindResume {
		s.rollbackAs("", true, true)
	}
}

// --- the hold buffer (3.5.4 step 3, F14) ---

func (s *switcher) openHold() {
	s.closeHold()
	s.holdOpen = true
	s.holdTime = s.f.after(holdFor, func() {
		s.holdTime = nil
		if len(s.hold) > 0 {
			s.holdLost = true
		}
		s.hold, s.holdOpen = nil, false
	})
}

func (s *switcher) closeHold() {
	s.holdTime.stop()
	s.holdTime = nil
	s.hold, s.holdOpen, s.holdLost = nil, false, false
}

// input is Input(m): input.* and pane.scroll for what is shown. While a
// switch runs, keys and paste of its first 2 s are held for the target;
// everything else typed then is dropped. A stale view gets nothing.
func (s *switcher) input(m proto.Msg) {
	switch m.T {
	case proto.TypeInputKey, proto.TypeInputMouse, proto.TypeInputPaste, proto.TypeInputFocus, proto.TypePaneScroll:
	default:
		return
	}
	if p := s.pend; p != nil && p.kind != kindResume {
		if m.T != proto.TypeInputKey && m.T != proto.TypeInputPaste {
			return
		}
		if s.holdOpen && len(s.hold) < holdMax {
			s.hold = append(s.hold, m)
			return
		}
		s.holdLost = true
		return
	}
	c := s.cur
	if c == nil || c.stale || s.pend != nil {
		return
	}
	s.f.send(c.mc, m)
}

// retarget addresses a held key or paste to pane.
func retarget(m proto.Msg, pane string) (proto.Msg, bool) {
	if pane == "" {
		return proto.Msg{}, false
	}
	switch m.T {
	case proto.TypeInputKey:
		var k proto.Key
		if m.Decode(&k) != nil {
			return proto.Msg{}, false
		}
		k.Pane = pane
		return proto.Encode(k), true
	case proto.TypeInputPaste:
		var p proto.Paste
		if m.Decode(&p) != nil {
			return proto.Msg{}, false
		}
		p.Pane = pane
		return proto.Encode(p), true
	}
	return proto.Msg{}, false
}

// --- stepping ---

// step is Step(delta): the next or previous session of the shown machine
// in snapshot order, never another machine's. A switch that runs is
// dropped first (a client action other than typing, 3.5.4).
func (s *switcher) step(delta int) {
	if delta == 0 {
		return
	}
	s.cancel()
	c := s.cur
	if c == nil || c.stale || c.mc.st == nil {
		return
	}
	order := c.mc.order
	i := -1
	for j, id := range order {
		if id == c.target.Session {
			i = j
			break
		}
	}
	if i < 0 || len(order) < 2 {
		return
	}
	n := len(order)
	next := ((i+delta)%n + n) % n
	if next == i {
		return
	}
	s.switchTo(session.Target{Machine: c.mc.name, Session: order[next]}, kindUser)
}

// --- the shown session ends, and the stale view (3.5.5) ---

// viewEnded is view.ended on mc's link.
func (s *switcher) viewEnded(mc *machine, m proto.Msg) {
	var e proto.ViewEnded
	if m.Decode(&e) != nil {
		return
	}
	if p := s.pend; p != nil && p.mc == mc && p.gotOK && s.lastOK[mc] == p.id && e.Session == p.ok.Session {
		s.lastOK[mc] = 0
		if p.kind == kindResume {
			s.endPending()
			s.lostView()
			return
		}
		s.rollback("", false)
		return
	}
	c := s.cur
	if c == nil || c.mc != mc || s.lastOK[mc] != c.id || e.Session != c.target.Session {
		return
	}
	s.lastOK[mc] = 0
	s.ended(c)
}

// ended: the shown session is gone. The newest history entry that is an
// existing session of an online machine is shown; with none, the hub opens.
func (s *switcher) ended(c *view) {
	s.hist.remove(histEntry{machine: c.mc.name, session: c.target.Session, dir: c.ok.Dir, boot: c.boot})
	c.gone = true
	if s.pend != nil {
		// a switch runs: its rollback finds the view gone
		return
	}
	s.cur = nil
	s.f.dirty = true
	s.fallback(c.mc, false)
}

// fallback shows the newest history entry that is an existing session of
// an online machine; with none the window shows nothing and the hub opens,
// unless quiet (the client itself ended the switch). was is the machine of
// the view that ended.
func (s *switcher) fallback(was *machine, quiet bool) {
	if t, ok := s.hist.fallback(s.f); ok {
		s.start(s.f.byName[t.Machine], t, kindFallback, "")
	} else if !quiet {
		s.f.update(Update{Kind: OpenHub, Hub: HubRequest{Mode: hub.ModeAll}})
	}
	s.f.startIdle(was)
}

// resumeStale is the table of 3.5.5: the stale view's machine is online
// again.
func (s *switcher) resumeStale() {
	c := s.cur
	mc := c.mc
	st := mc.st
	if st == nil {
		return
	}
	if mc.welcome.Boot == c.boot {
		if _, ok := st.Sessions[c.target.Session]; ok {
			t := session.Target{Machine: mc.name, Session: c.target.Session}
			s.start(mc, t, kindResume, "")
			return
		}
	} else if _, ok := sessionOfDir(mc, c.ok.Dir); ok {
		t := session.Target{Machine: mc.name, Dir: c.ok.Dir, Name: c.label}
		s.start(mc, t, kindResume, ui.Notice("restarted", s.name(mc)))
		return
	}
	s.lostView()
}

// lostView: the stale view did not come back. The history decides.
func (s *switcher) lostView() {
	c := s.cur
	if c == nil {
		return
	}
	s.ended(c)
}

// --- the state of the displayed server (5.3.8, contract change 14) ---

// The client reads the displayed tab, its split and focused pane, and the
// pane fields editor, busy, program and title from the state of the
// displayed server. The fleet hands it on as an update of kind Frame whose
// Frame is a state.snapshot of the machine's cleaned state: once right after
// each Shown, before the frames that follow it, and after every change of
// that state while the committed view is shown and not stale: before
// anything the link sent after the change (receive), and, when the link
// sends nothing more, when the core next asks for the mode (state), which
// it does at the end of the step that changed the state or, while the
// summary is held back by its rate limit, when those 50 ms end. Changes
// with nothing else of the link between them may be handed on as one
// snapshot of the newest state: each update carries the whole state.

// syncState hands the client the displayed machine's state when it changed
// since it was handed on last.
func (s *switcher) syncState() {
	c := s.cur
	if c == nil || !s.displays(c.mc) {
		return
	}
	if st := c.mc.st; st == nil || (st == s.sentSt && st.Rev == s.sentRev) {
		return
	}
	s.sendState(c)
}

// sendState hands the client the state of c's machine.
func (s *switcher) sendState(c *view) {
	st := c.mc.st
	if st == nil {
		return
	}
	s.sentSt, s.sentRev = st, st.Rev
	s.f.update(Update{Kind: Frame, Target: c.target, Frame: proto.Encode(snapshotOf(c.mc))})
}

// snapshotOf is mc's cleaned state as a state.snapshot: the sessions in the
// server's order, everything else by id.
func snapshotOf(mc *machine) proto.Snapshot {
	st := mc.st
	snap := proto.Snapshot{
		Rev:      st.Rev,
		Sessions: make([]proto.Session, 0, len(st.Sessions)),
		Tabs:     make([]proto.Tab, 0, len(st.Tabs)),
		Panes:    make([]proto.Pane, 0, len(st.Panes)),
		Agents:   make([]proto.Agent, 0, len(st.Agents)),
	}
	seen := make(map[string]bool, len(st.Sessions))
	for _, id := range mc.order {
		if o, ok := st.Sessions[id]; ok && !seen[id] {
			seen[id] = true
			snap.Sessions = append(snap.Sessions, o)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(st.Sessions)) {
		if !seen[id] {
			snap.Sessions = append(snap.Sessions, st.Sessions[id])
		}
	}
	for _, id := range slices.Sorted(maps.Keys(st.Tabs)) {
		snap.Tabs = append(snap.Tabs, st.Tabs[id])
	}
	for _, id := range slices.Sorted(maps.Keys(st.Panes)) {
		snap.Panes = append(snap.Panes, st.Panes[id])
	}
	for _, id := range slices.Sorted(maps.Keys(st.Agents)) {
		snap.Agents = append(snap.Agents, st.Agents[id])
	}
	return snap
}

// --- helpers ---

// name is how a machine is named in a notice: its config alias, or for
// this machine its host name.
func (s *switcher) name(mc *machine) string {
	if mc.name != "" {
		return mc.name
	}
	if mc.host != "" {
		return mc.host
	}
	return "this machine"
}
