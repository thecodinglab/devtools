package model

import (
	"path/filepath"
	"reflect"

	"devtools/internal/proto"
	"devtools/internal/session"
)

// OpenSession returns the session of dir, a canonical directory (the
// caller resolves it with session.Key). When none exists, it opens one with
// one tab and one dormant pane in dir, labelled label (the directory's name
// when label is empty), and created is true. Nothing is spawned until the
// session is displayed.
func (s *State) OpenSession(dir, label string) (id string, created bool, r Result) {
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	if id, ok := s.byDir[dir]; ok {
		return id, false, Result{}
	}
	b := s.batch()
	sid := issue("s", &s.next.Session)
	x := &sess{
		Session: proto.Session{ID: sid, Dir: dir, Label: labelOf(dir, label)},
		cols:    DefaultCols, rows: DefaultRows,
	}
	s.sessions[sid] = x
	s.byDir[dir] = sid
	tid, _ := s.addTab(b, x, dir)
	x.Current = tid
	b.touch(sid)
	return sid, true, b.result()
}

// addTab appends a tab with one dormant pane in dir to x. It does not make
// it current.
func (s *State) addTab(b *batch, x *sess, dir string) (tab, pane string) {
	tid := issue("t", &s.next.Tab)
	pid := s.addPane(b, x.ID, tid, dir)
	s.tabs[tid] = &proto.Tab{ID: tid, Session: x.ID, Index: len(x.Tabs) + 1, Panes: []string{pid}, Split: proto.SplitNone, Focused: pid}
	x.Tabs = append(x.Tabs, tid)
	b.touch(tid)
	b.touch(x.ID)
	return tid, pid
}

func (s *State) addPane(b *batch, session, tab, dir string) string {
	pid := issue("p", &s.next.Pane)
	s.panes[pid] = &pane{Pane: proto.Pane{ID: pid, Tab: tab, Session: session, State: proto.PaneDormant, Cwd: dir}}
	b.touch(pid)
	return pid
}

// CloseSession ends a session: every pane, tab and agent of it goes. A
// session that does not exist is an empty result.
func (s *State) CloseSession(id string) Result {
	x, ok := s.sessions[id]
	if !ok {
		return Result{}
	}
	b := s.batch()
	s.endSession(b, x)
	return b.result()
}

func (s *State) endSession(b *batch, x *sess) {
	for _, tid := range x.Tabs {
		s.removeTab(b, tid)
	}
	delete(s.sessions, x.ID)
	delete(s.byDir, x.Dir)
	b.gone(x.ID)
	b.effect(Effect{Kind: Ended, Session: x.ID, Dir: x.Dir})
}

// removeTab removes a tab and its panes; the session's list is the
// caller's.
func (s *State) removeTab(b *batch, tid string) {
	t := s.tabs[tid]
	for _, pid := range t.Panes {
		s.removePane(b, pid)
	}
	delete(s.tabs, tid)
	b.gone(tid)
}

// removePane removes a pane and its agent; the tab's list is the caller's.
func (s *State) removePane(b *batch, pid string) {
	p := s.panes[pid]
	if p.State != proto.PaneDormant {
		b.effect(Effect{Kind: Kill, Session: p.Session, Pane: pid})
	}
	if _, ok := s.agents[pid]; ok {
		delete(s.agents, pid)
		b.goneAgent(pid)
	}
	delete(s.panes, pid)
	b.gone(pid)
}

// NewTab appends a tab with one pane to a session and makes it current. The
// pane starts in cwd, or in the session directory when cwd is empty. Its
// program is spawned when the session is displayed.
func (s *State) NewTab(session, cwd string) (tab, pane string, r Result, err *Error) {
	x, ok := s.sessions[session]
	if !ok {
		return "", "", Result{}, errf(proto.CodeNoSession, "session %s does not exist", session)
	}
	if cwd == "" {
		cwd = x.Dir
	}
	b := s.batch()
	tab, pane = s.addTab(b, x, cwd)
	s.makeCurrent(b, x, tab)
	return tab, pane, b.result(), nil
}

// makeCurrent makes tab the current tab of x: its panes are spawned or
// resized, and the viewers' frames reset.
func (s *State) makeCurrent(b *batch, x *sess, tab string) {
	x.Current = tab
	b.touch(x.ID)
	s.reconcile(b, x)
	b.effect(Effect{Kind: Retab, Session: x.ID})
}

// reconcile brings the panes of the current tab of x to its layout: when x
// is displayed every dormant pane is spawned, and every running pane whose
// size differs from its rectangle is resized. Panes of other tabs keep
// their size until their tab becomes current.
func (s *State) reconcile(b *batch, x *sess) {
	t := s.tabs[x.Current]
	rects := Layout(x.cols, x.rows, t.Split)
	for i, pid := range t.Panes {
		p, r := s.panes[pid], rects[i]
		switch p.State {
		case proto.PaneDormant:
			if x.displayed {
				s.spawn(b, x, p, r)
			}
		case proto.PaneRunning:
			if p.cols != r.W || p.rows != r.H {
				p.cols, p.rows = r.W, r.H
				b.effect(Effect{Kind: Resize, Session: x.ID, Pane: pid, Cols: r.W, Rows: r.H})
			}
		}
	}
}

// spawn marks p running at r and asks for its program. It starts in the
// pane's directory, or in the session directory when that is gone.
func (s *State) spawn(b *batch, x *sess, p *pane, r Rect) {
	p.State = proto.PaneRunning
	p.Exit = nil
	p.cols, p.rows = r.W, r.H
	dir := p.Cwd
	if !s.exists(dir) {
		dir = x.Dir
	}
	b.touch(p.ID)
	b.effect(Effect{Kind: Spawn, Session: x.ID, Pane: p.ID, Cols: r.W, Rows: r.H, Dir: dir})
}

// rectOf returns the rectangle of a pane in its tab's layout.
func (s *State) rectOf(x *sess, p *pane) Rect {
	t := s.tabs[p.Tab]
	rects := Layout(x.cols, x.rows, t.Split)
	for i, pid := range t.Panes {
		if pid == p.ID {
			return rects[i]
		}
	}
	return rects[0]
}

// SelectTab makes a tab of a session current: by id when tab is not
// empty, else by index (from 1) when index is not 0, else by step (+1 or
// -1, wrapping around). It returns the id of the tab that is current. When
// that tab is current already nothing changes.
func (s *State) SelectTab(session, tab string, index, step int) (id string, r Result, err *Error) {
	x, ok := s.sessions[session]
	if !ok {
		return "", Result{}, errf(proto.CodeNoSession, "session %s does not exist", session)
	}
	n := len(x.Tabs)
	switch {
	case tab != "":
		t, ok := s.tabs[tab]
		if !ok || t.Session != session {
			return "", Result{}, errf(proto.CodeNoTab, "tab %s does not exist in session %s", tab, session)
		}
		id = tab
	case index != 0:
		if index < 1 || index > n {
			return "", Result{}, errf(proto.CodeNoTab, "session %s has no tab %d", session, index)
		}
		id = x.Tabs[index-1]
	case step != 0:
		cur := s.tabs[x.Current].Index - 1
		id = x.Tabs[((cur+step)%n+n)%n]
	default:
		return "", Result{}, errf(proto.CodeInvalid, "tab.select needs a tab, an index or a step")
	}
	if id == x.Current {
		return id, Result{}, nil
	}
	b := s.batch()
	s.makeCurrent(b, x, id)
	return id, b.result(), nil
}

// CloseTab closes a tab: its panes go, the later tabs are renumbered, and
// when it was current the tab to its left becomes current (the new first
// tab when it was the first). Closing the last tab ends the session.
func (s *State) CloseTab(tab string) (Result, *Error) {
	t, ok := s.tabs[tab]
	if !ok {
		return Result{}, errf(proto.CodeNoTab, "tab %s does not exist", tab)
	}
	b := s.batch()
	s.closeTab(b, t)
	return b.result(), nil
}

func (s *State) closeTab(b *batch, t *proto.Tab) {
	x := s.sessions[t.Session]
	if len(x.Tabs) == 1 {
		s.endSession(b, x)
		return
	}
	at := t.Index - 1
	s.removeTab(b, t.ID)
	x.Tabs = append(x.Tabs[:at:at], x.Tabs[at+1:]...)
	for i, tid := range x.Tabs {
		if o := s.tabs[tid]; o.Index != i+1 {
			o.Index = i + 1
			b.touch(tid)
		}
	}
	b.touch(x.ID)
	if x.Current == t.ID {
		s.makeCurrent(b, x, x.Tabs[max(at-1, 0)])
	}
}

// Split adds a second pane to the pane's tab, right of it or below it, and
// focuses it. The new pane starts in cwd; when cwd is empty, in the split
// pane's directory when it is known and exists, else in the session
// directory. A tab with two panes, or a split whose panes would be below
// the minimum at the session's size, is a limit.
func (s *State) Split(pane, direction, cwd string) (id string, r Result, err *Error) {
	if direction != proto.SplitRight && direction != proto.SplitDown {
		return "", Result{}, errf(proto.CodeInvalid, "direction %q is not right or down", direction)
	}
	p, ok := s.panes[pane]
	if !ok {
		return "", Result{}, errf(proto.CodeNoPane, "pane %s does not exist", pane)
	}
	t, x := s.tabs[p.Tab], s.sessions[p.Session]
	if len(t.Panes) == 2 {
		return "", Result{}, errf(proto.CodeLimit, "tab %d already has two panes", t.Index)
	}
	if !splitFits(x.cols, x.rows, direction) {
		return "", Result{}, errf(proto.CodeLimit, "a pane cannot be smaller than %d by %d", MinPaneCols, MinPaneRows)
	}
	if cwd == "" {
		cwd = x.Dir
		if s.exists(p.Cwd) {
			cwd = p.Cwd
		}
	}
	b := s.batch()
	id = s.addPane(b, x.ID, t.ID, cwd)
	t.Panes = append(t.Panes, id)
	t.Split = direction
	t.Focused = id
	b.touch(t.ID)
	if x.Current == t.ID {
		s.reconcile(b, x)
	}
	return id, b.result(), nil
}

// Focus makes a pane the focused pane of its tab.
func (s *State) Focus(pane string) (Result, *Error) {
	p, ok := s.panes[pane]
	if !ok {
		return Result{}, errf(proto.CodeNoPane, "pane %s does not exist", pane)
	}
	t := s.tabs[p.Tab]
	if t.Focused == pane {
		return Result{}, nil
	}
	b := s.batch()
	t.Focused = pane
	b.touch(t.ID)
	return b.result(), nil
}

// FocusDir moves the focus of the pane's tab from pane in a direction (3.3):
// in a right split "right" from the first and "left" from the second pane
// move, in a down split "down" from the first and "up" from the second.
// Anything else does not move (no wraparound): target is pane and moved is
// false.
func (s *State) FocusDir(pane, direction string) (target string, moved bool, r Result, err *Error) {
	switch direction {
	case "left", "right", "up", "down":
	default:
		return "", false, Result{}, errf(proto.CodeInvalid, "direction %q is not left, right, up or down", direction)
	}
	p, ok := s.panes[pane]
	if !ok {
		return "", false, Result{}, errf(proto.CodeNoPane, "pane %s does not exist", pane)
	}
	t := s.tabs[p.Tab]
	if len(t.Panes) != 2 {
		return pane, false, Result{}, nil
	}
	first := t.Panes[0] == pane
	var forward, back string
	switch t.Split {
	case proto.SplitRight:
		forward, back = "right", "left"
	case proto.SplitDown:
		forward, back = "down", "up"
	}
	switch {
	case first && direction == forward:
		target = t.Panes[1]
	case !first && direction == back:
		target = t.Panes[0]
	default:
		return pane, false, Result{}, nil
	}
	b := s.batch()
	if t.Focused != target {
		t.Focused = target
		b.touch(t.ID)
	}
	return target, true, b.result(), nil
}

// ClosePane closes a pane, also when its program ended normally. With two
// panes the other one stays, takes the whole tab and is focused; with one
// it is CloseTab of its tab.
func (s *State) ClosePane(pane string) (Result, *Error) {
	p, ok := s.panes[pane]
	if !ok {
		return Result{}, errf(proto.CodeNoPane, "pane %s does not exist", pane)
	}
	t, x := s.tabs[p.Tab], s.sessions[p.Session]
	b := s.batch()
	if len(t.Panes) == 1 {
		s.closeTab(b, t)
		return b.result(), nil
	}
	s.removePane(b, pane)
	other := t.Panes[0]
	if other == pane {
		other = t.Panes[1]
	}
	t.Panes = []string{other}
	t.Split = proto.SplitNone
	t.Focused = other
	b.touch(t.ID)
	if x.Current == t.ID {
		s.reconcile(b, x)
	}
	return b.result(), nil
}

// SetSize sets a session's pane area, clamped to the limits of 4.1. The
// running panes of its current tab are resized; the other tabs follow when
// they become current. A session that does not exist is an empty result.
func (s *State) SetSize(session string, cols, rows int) Result {
	x, ok := s.sessions[session]
	if !ok {
		return Result{}
	}
	cols, rows = proto.ClampSize(cols, rows)
	if cols == x.cols && rows == x.rows {
		return Result{}
	}
	x.cols, x.rows = cols, rows
	b := s.batch()
	s.reconcile(b, x)
	return b.result()
}

// Size returns a session's pane area.
func (s *State) Size(session string) (cols, rows int, ok bool) {
	x, ok := s.sessions[session]
	if !ok {
		return 0, 0, false
	}
	return x.cols, x.rows, true
}

// Display records that a view was set on a session: shown becomes now, and
// every dormant pane of the current tab is spawned at its layout size. From
// then on new panes of the session are spawned when their tab is current.
func (s *State) Display(session string) Result {
	x, ok := s.sessions[session]
	if !ok {
		return Result{}
	}
	b := s.batch()
	x.displayed = true
	if ms := s.nowMs(); ms != x.Shown {
		x.Shown = ms
		b.touch(x.ID)
	}
	s.reconcile(b, x)
	return b.result()
}

// SetPane changes a pane through f: title, cwd, program, editor, busy, and
// the lifecycle (state and exit). Its id, tab and session cannot change.
// Of the state only two steps of the lifecycle of 3.3 are taken: running
// to exited (the program ended by a signal the server did not send, or
// could not be started), and exited to running (Enter started it again: it
// is spawned at its layout size in its directory); a dormant pane is
// spawned only by its tab being displayed. Exit is kept only while the
// pane is exited. Strings are sanitised. A pane that does not exist is an
// empty result.
func (s *State) SetPane(pane string, f func(*proto.Pane)) Result {
	p, ok := s.panes[pane]
	if !ok {
		return Result{}
	}
	old := p.copy()
	v := p.copy()
	f(&v)
	v.ID, v.Tab, v.Session = old.ID, old.Tab, old.Session
	switch {
	case old.State == proto.PaneRunning && v.State == proto.PaneExited:
	case old.State == proto.PaneExited && v.State == proto.PaneRunning:
	default:
		v.State = old.State
	}
	if v.State == proto.PaneExited && v.Exit != nil {
		e := *v.Exit
		e.Signal = session.Sanitize(e.Signal, session.MaxLabel)
		e.Text = session.Sanitize(e.Text, session.MaxText)
		v.Exit = &e
	} else if v.State != proto.PaneExited {
		v.Exit = nil
	}
	v.Title = session.Sanitize(v.Title, session.MaxTitle)
	v.Program = session.Sanitize(v.Program, session.MaxLabel)
	if reflect.DeepEqual(v, old) {
		return Result{}
	}
	b := s.batch()
	start := v.State == proto.PaneRunning && old.State != proto.PaneRunning
	if start {
		v.State = old.State // spawn sets it
	}
	p.Pane = v
	b.touch(pane)
	if start {
		x := s.sessions[p.Session]
		s.spawn(b, x, p, s.rectOf(x, p))
	}
	return b.result()
}

// PutAgent sets the agent record of a pane. Its pane, tab and session are
// the pane's; kind and status values the wire does not know are "unknown".
// A pane that does not exist is an empty result.
func (s *State) PutAgent(pane string, a proto.Agent) Result {
	p, ok := s.panes[pane]
	if !ok {
		return Result{}
	}
	a.Pane, a.Tab, a.Session = pane, p.Tab, p.Session
	switch a.Kind {
	case proto.AgentClaude, proto.AgentCodex:
	default:
		a.Kind = proto.Unknown
	}
	switch a.Status {
	case proto.StatusIdle, proto.StatusWorking, proto.StatusBlocked, proto.StatusDone:
	default:
		a.Status = proto.Unknown
	}
	a.Title = session.Sanitize(a.Title, session.MaxTitle)
	if have, ok := s.agents[pane]; ok && have == a {
		return Result{}
	}
	b := s.batch()
	s.agents[pane] = a
	b.touchAgent(pane)
	return b.result()
}

// DelAgent removes the agent record of a pane.
func (s *State) DelAgent(pane string) Result {
	if _, ok := s.agents[pane]; !ok {
		return Result{}
	}
	b := s.batch()
	delete(s.agents, pane)
	b.goneAgent(pane)
	return b.result()
}
