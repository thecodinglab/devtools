package server

import (
	"slices"
	"sync"
	"time"

	"devtools/internal/pane"
	"devtools/internal/proto"
	"devtools/internal/server/lane"
	"devtools/internal/server/model"
)

// Views (4.2.3, 3.2.3). A view is the one session an interactive connection
// displays. The loop owns the views; the frame lane of the connection holds
// one slot per pane of the session's current tab, and the reader of the
// connection reads the view as an immutable value the loop replaces
// (viewState), so input never waits for the loop.

// handle is one pane as the connections see it: the frame source of their
// lanes, the target of their input, and the scroll positions of the
// connections scrolled in it. It outlives the pane objects of one pane id:
// a respawn (Enter on an exited pane) replaces the object inside, so the
// frames of the pane go on with their sequence (4.2.4).
type handle struct {
	id string

	mu      sync.Mutex
	p       Pane // the current pane object; nil once the pane went
	last    pane.Snap
	cols    int                // the newest width the pane was given
	viewers map[*conn]bool     // connections whose displayed set holds the pane
	scrolls map[*conn]*scrolls // connections scrolled in the pane, or holding marks in it

	// fmu orders the Follow and Unfollow calls of one pane.scroll after
	// another; it is never held while h.mu is wanted by the pane's own
	// goroutine (OnReflow), so a pane that blocks in Follow cannot deadlock.
	fmu sync.Mutex

	// focus reports to the program (mode 1004), sent by a goroutine of
	// their own because Pane.Focus blocks while the pane's input queue is
	// full; only the newest wish is sent.
	focusWant, focusDone, focusBusy bool
}

// scrolls is the scroll position and the marks of one connection in one
// pane (4.2.7), and the lines the pane follows for it: the top line when
// scrolled, then the line of each mark; one Follow each.
type scrolls struct {
	scrolled bool
	top      int64
	marks    []proto.Pos
	follows  []int64
}

func newHandle(id string) *handle {
	return &handle{id: id, viewers: map[*conn]bool{}, scrolls: map[*conn]*scrolls{}}
}

// pane returns the current pane object, nil once the pane went.
func (h *handle) pane() Pane {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.p
}

// Snapshot is the lane's Source. Once the pane went it repeats the last
// snapshot, so a frame that is built in the moment between the pane's end
// and the Show that takes it off the lane changes nothing.
func (h *handle) Snapshot(top int64, scrolled bool) pane.Snap {
	p := h.pane()
	if p == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.last
	}
	s := p.Snapshot(top, scrolled)
	h.mu.Lock()
	h.last = s
	h.mu.Unlock()
	return s
}

// wake marks the pane dirty on the lane of every connection that displays
// it. It is the pane's Watch callback and never blocks.
func (h *handle) wake() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for cn := range h.viewers {
		cn.lane.Dirty(h.id)
	}
}

func (h *handle) setCols(cols int) {
	h.mu.Lock()
	h.cols = cols
	h.mu.Unlock()
}

// swap puts a new pane object in. The scroll positions belonged to the
// lines of the old object; they end, and each connection that was scrolled
// is told that it is live again. The new program has not been told about
// focus yet.
func (h *handle) swap(p Pane) {
	h.mu.Lock()
	h.p = p
	h.focusDone = false
	ended := h.scrolls
	h.scrolls = map[*conn]*scrolls{}
	h.mu.Unlock()
	for cn, st := range ended {
		cn.reflow(h.id, live(st), 0, false)
	}
	h.wake()
	h.applyFocus()
}

// end is the pane's end: frames repeat the last snapshot, input and
// scrolling find no pane.
func (h *handle) end() {
	h.mu.Lock()
	h.p = nil
	h.scrolls = map[*conn]*scrolls{}
	h.mu.Unlock()
}

// live is the event.reflow that returns a connection to live: no top, every
// mark gone.
func live(st *scrolls) proto.Reflow {
	r := proto.Reflow{}
	for range st.marks {
		r.Marks = append(r.Marks, proto.Pos{Line: -1})
	}
	return r
}

// reflow queues an event.reflow for pane and sets the lane's scroll
// position; from any goroutine. A full control queue closes the connection.
func (cn *conn) reflow(paneID string, r proto.Reflow, top int64, scrolled bool) {
	r.Pane = paneID
	if !cn.lane.Send(proto.Encode(r)) {
		cn.nc.Close()
	}
	cn.lane.Scroll(paneID, top, scrolled)
}

// onReflow is the pane's OnReflow: a resize renumbered the lines. It runs on
// the pane's goroutine (in the tests of the core on the loop), takes only
// the handle's lock and calls no method of the pane, so it can neither block
// nor reenter the terminal. Every connection scrolled in the pane gets its
// top and its marks moved with their text and an event.reflow on its control
// lane, which therefore precedes the first frame built after the resize
// (W8). A top the remap cannot place returns the connection to live; a mark
// it cannot place is -1; a mark keeps its column where the line at the new
// width has one, else it gets the last column (4.2.7).
func (h *handle) onReflow(remap func(abs int64) (now int64, ok bool)) {
	type note struct {
		cn       *conn
		r        proto.Reflow
		top      int64
		scrolled bool
	}
	var notes []note
	var unfollow []int64
	h.mu.Lock()
	cols := h.cols
	p := h.p
	for cn, st := range h.scrolls {
		var keep []int64
		for _, f := range st.follows {
			// The engine keeps every Follow of a line it can place under the
			// line's new number and ends the others.
			if n, ok := remap(f); ok {
				keep = append(keep, n)
			}
		}
		st.follows = keep
		var r proto.Reflow
		lost := false
		if st.scrolled {
			if n, ok := remap(st.top); ok {
				st.top = n
				top := n
				r.Top = &top
			} else {
				lost = true
			}
		}
		for i := range st.marks {
			m := &st.marks[i]
			if lost || m.Line < 0 {
				m.Line = -1
				continue
			}
			n, ok := remap(m.Line)
			if !ok {
				m.Line = -1
				continue
			}
			m.Line = n
			if cols > 0 && m.Col > cols-1 {
				m.Col = cols - 1
			}
		}
		r.Marks = slices.Clone(st.marks)
		anyMark := false
		for _, m := range st.marks {
			if m.Line >= 0 {
				anyMark = true
			}
		}
		if lost || (!st.scrolled && !anyMark) {
			// Live again: what is still followed is let go.
			unfollow = append(unfollow, st.follows...)
			delete(h.scrolls, cn)
			notes = append(notes, note{cn: cn, r: r})
			continue
		}
		notes = append(notes, note{cn: cn, r: r, top: st.top, scrolled: st.scrolled})
	}
	h.mu.Unlock()
	for _, n := range notes {
		n.cn.reflow(h.id, n.r, n.top, n.scrolled)
	}
	if len(unfollow) > 0 && p != nil {
		go unfollowAll(p, unfollow)
	}
}

func unfollowAll(p Pane, lines []int64) {
	for _, l := range lines {
		p.Unfollow(l)
	}
}

// endScroll returns cn to live in the pane: its lines are let go and the
// lane shows the live screen. It reports whether cn was scrolled.
func (h *handle) endScroll(cn *conn) bool {
	h.mu.Lock()
	st := h.scrolls[cn]
	delete(h.scrolls, cn)
	p := h.p
	h.mu.Unlock()
	if st == nil {
		return false
	}
	if p != nil && len(st.follows) > 0 {
		go unfollowAll(p, st.follows)
	}
	cn.lane.Scroll(h.id, 0, false)
	return true
}

// viewState is a connection's view as its reader sees it: the session and
// the panes of the displayed set. The loop replaces it as a whole.
type viewState struct {
	session string
	panes   map[string]*handle
}

// --- on the loop ---

// viewMsg handles the view messages (4.2.3); false when m is none of them.
func (c *core) viewMsg(cn *conn, m proto.Msg) bool {
	switch m.T {
	case proto.TypeViewSet:
		if m.ID != 0 {
			c.viewSet(cn, m)
		}
	case proto.TypeViewResize:
		if m.ID == 0 {
			c.viewResize(cn, m)
		}
	case proto.TypeViewColors:
		if m.ID == 0 {
			c.viewColors(cn, m)
		}
	case proto.TypeViewClear:
		if m.ID == 0 {
			c.viewClear(cn)
		}
	default:
		return false
	}
	if m.ID != 0 && m.T != proto.TypeViewSet {
		c.Fail(cn, m.ID, proto.CodeInvalid, m.T+" is a notification")
	}
	return true
}

// viewSet is view.set, in the four steps of 3.2.3. Steps 1 and 2 touch only
// the model, so a refused request leaves the previous view, its sizes and
// its frames as they were; a session that step 1 opened stays.
func (c *core) viewSet(cn *conn, m proto.Msg) {
	var q proto.ViewSet
	if err := decode(m, &q); err != nil {
		c.Fail(cn, m.ID, err.Code, err.Text)
		return
	}
	if err := c.viewTarget(cn, &q); err != nil {
		c.Fail(cn, m.ID, err.Code, err.Text)
		return
	}
	sid := q.Session

	// Step 3: the view, the size and the interaction; the owner of the size
	// is recomputed before the dormant panes are spawned, so they start at
	// their final size.
	c.leave(cn)
	cn.view = sid
	cn.pending = true
	cn.cols, cn.rows = proto.ClampSize(q.Cols, q.Rows)
	c.interacted(cn)
	c.syncSizes()
	c.apply(c.model.Display(sid), true)

	// Step 4: view.ok and the new displayed set in one step (W4).
	cn.pending = false
	sess := c.known.Sessions[sid]
	tab := c.known.Tabs[sess.Current]
	ok := proto.Request(m.ID, proto.ViewOK{Session: sid, Dir: sess.Dir, Tab: tab.ID, Panes: slices.Clone(tab.Panes)})
	c.show(cn, &ok)
}

// viewTarget is steps 1 and 2 of view.set: it resolves q to a session in
// q.Session and makes q.Tab current and q.Pane focused. Everything is
// checked before anything changes, except the session that a dir opens.
func (c *core) viewTarget(cn *conn, q *proto.ViewSet) *model.Error {
	if (q.Session == "") == (q.Dir == "") {
		return invalid("view.set needs exactly one of session and dir")
	}
	if q.Dir != "" {
		id, _, err := c.openSession(q.Dir, q.Name)
		if err != nil {
			return err
		}
		q.Session = id
	} else if _, ok := c.known.Sessions[q.Session]; !ok {
		return &model.Error{Code: proto.CodeNoSession, Text: "session " + q.Session + " does not exist"}
	}
	tab := q.Tab
	if tab != "" {
		if t, ok := c.known.Tabs[tab]; !ok || t.Session != q.Session {
			return &model.Error{Code: proto.CodeNoTab, Text: "tab " + tab + " does not exist in session " + q.Session}
		}
	}
	if q.Pane != "" {
		p, ok := c.known.Panes[q.Pane]
		if !ok || p.Session != q.Session || (tab != "" && p.Tab != tab) {
			return &model.Error{Code: proto.CodeNoPane, Text: "pane " + q.Pane + " does not exist in that tab"}
		}
		tab = p.Tab
	}
	if tab != "" {
		_, r, err := c.model.SelectTab(q.Session, tab, 0, 0)
		if err != nil {
			return err
		}
		c.apply(r, true)
	}
	if q.Pane != "" {
		r, err := c.model.Focus(q.Pane)
		if err != nil {
			return err
		}
		c.apply(r, true)
	}
	return nil
}

// viewResize is view.resize: the pane area changed. A resize is stored; it
// changes the session only when cn owns the size (3.2.3). It therefore does
// not count as an interaction of a connection that does not own the size:
// a window that merely changes its size does not take the session from the
// window the user works in (TestANonOwnersResizeChangesNothing).
func (c *core) viewResize(cn *conn, m proto.Msg) {
	if cn.view == "" {
		return
	}
	var q proto.ViewResize
	if m.Decode(&q) != nil {
		return
	}
	cn.cols, cn.rows = proto.ClampSize(q.Cols, q.Rows)
	if c.owner(cn.view) == cn {
		c.interacted(cn)
	}
}

// viewColors is view.colors; valid with or without a view.
func (c *core) viewColors(cn *conn, m proto.Msg) {
	var q proto.ViewColors
	if m.Decode(&q) != nil || q.Fg == "" || q.Bg == "" {
		return
	}
	cn.colors.Fg, cn.colors.Bg, cn.colors.Dark = q.Fg, q.Bg, q.Dark
}

// viewClear is view.clear: frames stop, the state goes on.
func (c *core) viewClear(cn *conn) {
	c.leave(cn)
	cn.view = ""
	c.show(cn, nil)
}

// leave ends what cn's view holds in the panes: it is no viewer of them any
// more and every line it followed is let go (3.2.3). The view itself and the
// lane are the caller's.
func (c *core) leave(cn *conn) {
	for _, h := range cn.shown {
		if h == nil {
			continue
		}
		h.mu.Lock()
		delete(h.viewers, cn)
		h.mu.Unlock()
		h.endScroll(cn)
	}
	cn.shown, cn.shownTab = nil, ""
}

// displayed returns the current tab of a session and the handles of its
// panes, nil for a pane without one (dormant).
func (c *core) displayed(sessionID string) (string, map[string]*handle) {
	sess, ok := c.known.Sessions[sessionID]
	if !ok {
		return "", nil
	}
	tab := c.known.Tabs[sess.Current]
	out := make(map[string]*handle, len(tab.Panes))
	for _, id := range tab.Panes {
		out[id] = c.handles[id]
	}
	return tab.ID, out
}

// stale reports whether the displayed set of cn's lane is no longer the
// current tab of its session.
func (c *core) stale(cn *conn) bool {
	if cn.view == "" || cn.pending {
		return false
	}
	tab, now := c.displayed(cn.view)
	if tab != cn.shownTab || len(now) != len(cn.shown) {
		return true
	}
	for id, h := range now {
		if have, ok := cn.shown[id]; !ok || have != h {
			return true
		}
	}
	return false
}

// show replaces the displayed set of cn's lane with the current tab of its
// view, or empties it without a view, and queues m in the same step (W4).
// Every pane of the set starts again at seq 1, full. A scroll position in a
// pane that stays is given to the new slot again; one in a pane that left
// ends.
func (c *core) show(cn *conn, m *proto.Msg) {
	if cn.gone {
		return
	}
	var tab string
	var now map[string]*handle
	if cn.view != "" {
		tab, now = c.displayed(cn.view)
	}
	for id, h := range cn.shown {
		if h == nil {
			continue
		}
		if now[id] != h {
			h.mu.Lock()
			delete(h.viewers, cn)
			h.mu.Unlock()
			h.endScroll(cn)
		}
	}
	sources := make(map[string]lane.Source, len(now))
	panes := make(map[string]*handle, len(now))
	for id, h := range now {
		if h == nil {
			continue
		}
		sources[id] = h
		panes[id] = h
	}
	if cn.view == "" {
		cn.viewp.Store(nil)
	} else {
		cn.viewp.Store(&viewState{session: cn.view, panes: panes})
	}
	cn.shown, cn.shownTab = now, tab
	if !cn.lane.Show(m, sources) {
		c.s.log.Warn("connection closed: its control queue is full", "conn", cn.id)
		c.drop(cn)
		return
	}
	for _, h := range panes {
		h.mu.Lock()
		h.viewers[cn] = true
		st := h.scrolls[cn]
		var top int64
		var scrolled bool
		if st != nil {
			top, scrolled = st.top, st.scrolled
		}
		h.mu.Unlock()
		if scrolled {
			cn.lane.Scroll(h.id, top, true)
		}
	}
}

// syncViews gives every connection whose displayed set went stale without a
// delta to carry the change the current set; the deltas carry it in
// broadcast, so this is a net under it.
func (c *core) syncViews() {
	for _, cn := range c.reg.list() {
		if c.stale(cn) {
			c.show(cn, nil)
		}
	}
}

// ended: the session is gone (effect Ended). Its viewers get view.ended,
// after the deltas that deleted it, and have no view from then on (4.2.3).
func (c *core) ended(sessionID, dir string) {
	m := proto.Encode(proto.ViewEnded{Session: sessionID, Dir: dir})
	for _, cn := range c.reg.list() {
		if cn.view == sessionID {
			c.leave(cn)
			cn.view = ""
			c.show(cn, &m)
		}
	}
}

// interacted records an interaction of cn for the size owner (3.2.3).
func (c *core) interacted(cn *conn) {
	c.ticks++
	cn.interact = c.ticks
	cn.lastInput = time.Now()
}
