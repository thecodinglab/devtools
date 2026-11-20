package fleet

import (
	"strings"
	"unicode/utf8"

	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// The fleet boundary (SEC7, SEC10, SEC12 of 3.8). Everything a link sends
// is cleaned here before anything else of the fleet reads it:
//
//   - every string that can reach the screen passes session.Sanitize:
//     labels, titles, program names, error texts, host names;
//   - ids are checked with session.ValidID and the letter of their kind;
//     an object with an invalid id, or that refers to one, is dropped;
//   - paths stay as they were received, because they are keys: a catalog
//     row and a session are joined by them, and view.set{dir} sends them
//     back. They are bounded and checked to be absolute, so a path that
//     lost a character on screen still opens (TestAPathWithRemovedCharactersStillOpens);
//   - lists are bounded: 2,000 catalog rows, 500 sessions, 1,000 panes per
//     machine.

const (
	maxHost     = 64
	maxPath     = 4096 // bytes; a path longer than PATH_MAX is not a path
	maxCatalog  = 2000
	maxSessions = 500
	maxPanes    = 1000
	maxTabs     = 1000 // every tab has a pane, so this bound is never the one that cuts
	maxSignal   = 16
)

// cleanText is a text of another machine as it may be shown.
func cleanText(s string) string { return session.Sanitize(s, session.MaxText) }

// cleanLabel is a label of another machine as it may be shown.
func cleanLabel(s string) string { return session.Sanitize(s, session.MaxLabel) }

// cleanTitle is a title of another machine as it may be shown.
func cleanTitle(s string) string { return session.Sanitize(s, session.MaxTitle) }

// validPath reports whether p can be the key of a row: absolute, valid
// UTF-8, without NUL, and at most maxPath bytes. It is not changed.
func validPath(p string) bool {
	return p != "" && len(p) <= maxPath && p[0] == '/' && utf8.ValidString(p) && !strings.ContainsRune(p, 0)
}

// validID reports whether id is a server id of the kind of its letter:
// 's' a session, 't' a tab, 'p' a pane.
func validID(kind byte, id string) bool {
	return session.ValidID(id) && id[0] == kind
}

func cleanSession(s proto.Session) (proto.Session, bool) {
	if !validID('s', s.ID) || !validPath(s.Dir) {
		return s, false
	}
	s.Label = cleanLabel(s.Label)
	tabs := make([]string, 0, len(s.Tabs))
	for _, t := range s.Tabs {
		if validID('t', t) {
			tabs = append(tabs, t)
		}
	}
	s.Tabs = tabs
	if !validID('t', s.Current) {
		s.Current = ""
	}
	return s, true
}

func cleanTab(t proto.Tab) (proto.Tab, bool) {
	if !validID('t', t.ID) || !validID('s', t.Session) {
		return t, false
	}
	panes := make([]string, 0, min(len(t.Panes), proto.MaxPanes))
	for _, p := range t.Panes {
		if validID('p', p) && len(panes) < proto.MaxPanes {
			panes = append(panes, p)
		}
	}
	t.Panes = panes
	if !validID('p', t.Focused) {
		t.Focused = ""
	}
	switch t.Split {
	case proto.SplitNone, proto.SplitRight, proto.SplitDown:
	default:
		t.Split = proto.SplitNone
	}
	// a layout that does not fit the panes kept is one split of them
	t.Layout = t.Tree()
	if len(t.Panes) > 0 {
		t.Split = t.Layout.Split
	}
	return t, true
}

func cleanPane(p proto.Pane) (proto.Pane, bool) {
	if !validID('p', p.ID) || !validID('t', p.Tab) || !validID('s', p.Session) {
		return p, false
	}
	if !validPath(p.Cwd) {
		p.Cwd = ""
	}
	p.Program = cleanLabel(p.Program)
	p.Title = cleanTitle(p.Title)
	if p.Exit != nil {
		e := *p.Exit
		e.Signal = session.Sanitize(e.Signal, maxSignal)
		e.Text = cleanText(e.Text)
		p.Exit = &e
	}
	return p, true
}

func cleanAgent(a proto.Agent) (proto.Agent, bool) {
	if !validID('p', a.Pane) || !validID('t', a.Tab) || !validID('s', a.Session) {
		return a, false
	}
	if !validPath(a.Cwd) {
		a.Cwd = ""
	}
	a.Title = cleanTitle(a.Title)
	return a, true
}

// cleanState cleans every object of st in place.
func cleanState(st *proto.State) {
	for id, o := range st.Sessions {
		if c, ok := cleanSession(o); ok && c.ID == id {
			st.Sessions[id] = c
		} else {
			delete(st.Sessions, id)
		}
	}
	for id, o := range st.Tabs {
		if c, ok := cleanTab(o); ok && c.ID == id {
			st.Tabs[id] = c
		} else {
			delete(st.Tabs, id)
		}
	}
	for id, o := range st.Panes {
		if c, ok := cleanPane(o); ok && c.ID == id {
			st.Panes[id] = c
		} else {
			delete(st.Panes, id)
		}
	}
	for id, o := range st.Agents {
		if c, ok := cleanAgent(o); ok && c.Pane == id {
			st.Agents[id] = c
		} else {
			delete(st.Agents, id)
		}
	}
}

// cleanChanged cleans the objects a delta put.
func cleanChanged(st *proto.State, d proto.Delta) {
	for _, c := range d.Changes {
		if c.Op != proto.OpPut {
			continue
		}
		switch c.Kind {
		case proto.KindSession:
			if o, ok := st.Sessions[c.ID]; ok {
				if o, ok = cleanSession(o); ok {
					st.Sessions[c.ID] = o
				} else {
					delete(st.Sessions, c.ID)
				}
			}
		case proto.KindTab:
			if o, ok := st.Tabs[c.ID]; ok {
				if o, ok = cleanTab(o); ok {
					st.Tabs[c.ID] = o
				} else {
					delete(st.Tabs, c.ID)
				}
			}
		case proto.KindPane:
			if o, ok := st.Panes[c.ID]; ok {
				if o, ok = cleanPane(o); ok {
					st.Panes[c.ID] = o
				} else {
					delete(st.Panes, c.ID)
				}
			}
		case proto.KindAgent:
			if o, ok := st.Agents[c.ID]; ok {
				if o, ok = cleanAgent(o); ok {
					st.Agents[c.ID] = o
				} else {
					delete(st.Agents, c.ID)
				}
			}
		}
	}
}

// replaceState makes s the whole truth of mc (3.5.2: after a reconnect the
// new snapshot is all there is), cleaned and bounded.
func (f *Fleet) replaceState(mc *machine, s proto.Snapshot) {
	cut := false
	if len(s.Sessions) > maxSessions {
		s.Sessions, cut = s.Sessions[:maxSessions], true
	}
	if len(s.Tabs) > maxTabs {
		s.Tabs, cut = s.Tabs[:maxTabs], true
	}
	if len(s.Panes) > maxPanes {
		s.Panes, cut = s.Panes[:maxPanes], true
	}
	if len(s.Agents) > maxPanes {
		s.Agents, cut = s.Agents[:maxPanes], true
	}
	st := proto.NewState(s)
	cleanState(st)
	mc.st = st
	mc.order = mc.order[:0]
	seen := map[string]bool{}
	for _, o := range s.Sessions {
		if _, ok := st.Sessions[o.ID]; ok && !seen[o.ID] {
			seen[o.ID] = true
			mc.order = append(mc.order, o.ID)
		}
	}
	if cut {
		f.listCut(mc, maxSessions)
	}
}

// applyDelta applies d to mc's state, cleans what it put and keeps the
// bounds: an object beyond a bound that d would add is left out.
func (f *Fleet) applyDelta(mc *machine, d proto.Delta) error {
	st := mc.st
	type added struct{ kind, id string }
	var fresh []added
	for _, c := range d.Changes {
		if c.Op != proto.OpPut {
			continue
		}
		var exists bool
		switch c.Kind {
		case proto.KindSession:
			_, exists = st.Sessions[c.ID]
		case proto.KindTab:
			_, exists = st.Tabs[c.ID]
		case proto.KindPane:
			_, exists = st.Panes[c.ID]
		case proto.KindAgent:
			_, exists = st.Agents[c.ID]
		}
		if !exists {
			fresh = append(fresh, added{c.Kind, c.ID})
		}
	}
	if err := st.Apply(d); err != nil {
		return err
	}
	cleanChanged(st, d)
	cut := false
	for i := len(fresh) - 1; i >= 0; i-- {
		a := fresh[i]
		switch {
		case a.kind == proto.KindSession && len(st.Sessions) > maxSessions:
			delete(st.Sessions, a.id)
			cut = true
		case a.kind == proto.KindTab && len(st.Tabs) > maxTabs:
			delete(st.Tabs, a.id)
			cut = true
		case a.kind == proto.KindPane && len(st.Panes) > maxPanes:
			delete(st.Panes, a.id)
			cut = true
		case a.kind == proto.KindAgent && len(st.Agents) > maxPanes:
			delete(st.Agents, a.id)
			cut = true
		}
	}
	// the order of sessions: kept ones in their place, new ones at the end
	kept := mc.order[:0]
	in := map[string]bool{}
	for _, id := range mc.order {
		if _, ok := st.Sessions[id]; ok && !in[id] {
			in[id] = true
			kept = append(kept, id)
		}
	}
	mc.order = kept
	for _, a := range fresh {
		if _, ok := st.Sessions[a.id]; ok && a.kind == proto.KindSession && !in[a.id] {
			in[a.id] = true
			mc.order = append(mc.order, a.id)
		}
	}
	f.deltaBells(mc, d)
	if cut {
		f.listCut(mc, maxSessions)
	}
	return nil
}

// cleanEntry is a catalog row of another machine as the hub gets it: the
// path as received is its key, every text is sanitised.
func cleanEntry(machine string, e proto.CatalogEntry) (hub.Item, bool) {
	var kind hub.Kind
	switch e.Kind {
	case proto.CatalogWorktree:
		kind = hub.KindWorktree
	case proto.CatalogRepo:
		kind = hub.KindRepo
	case proto.CatalogBookmark:
		kind = hub.KindBookmark
	default:
		return hub.Item{}, false
	}
	if !validPath(e.Path) {
		return hub.Item{}, false
	}
	root := e.Root
	if !validPath(root) {
		root = ""
	}
	label := cleanLabel(e.Label)
	if label == "" {
		label = cleanLabel(e.Path)
	}
	return hub.Item{
		Kind:     kind,
		Project:  cleanLabel(e.Project),
		Worktree: cleanLabel(e.Worktree),
		Label:    label,
		Path:     e.Path,
		Root:     root,
		Session:  cleanLabel(e.Session),
		Machine:  machine,
	}, true
}

// cleanGit is a git state of another machine as it may be shown.
func cleanGit(g proto.Git) ui.Git {
	return ui.Git{
		Changes:     max(g.Changes, 0),
		Ahead:       max(g.Ahead, 0),
		Behind:      max(g.Behind, 0),
		HasUpstream: g.HasUpstream,
		Detached:    g.Detached,
		Branch:      cleanLabel(g.Branch),
	}
}
