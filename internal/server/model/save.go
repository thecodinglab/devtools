package model

import (
	"devtools/internal/proto"
	"devtools/internal/server/persist"
)

// Load builds the state of a restored server (3.9). Every pane is dormant
// and no session is displayed, so nothing is spawned until a view is set;
// p is repaired first (persist.Repair), so the invariants of 3.3 hold
// whatever the file said. The counters continue where p left them.
func Load(p persist.State) *State {
	p = persist.Repair(p)
	s := New()
	s.next = p.Counters
	for _, ps := range p.Sessions {
		x := &sess{
			Session: proto.Session{ID: ps.ID, Dir: ps.Dir, Label: ps.Label, Current: ps.Current, Shown: ps.Shown},
			cols:    ps.Cols, rows: ps.Rows,
		}
		for i, pt := range ps.Tabs {
			t := &proto.Tab{ID: pt.ID, Session: ps.ID, Index: i + 1, Split: pt.Split, Focused: pt.Focused}
			for _, pp := range pt.Panes {
				s.panes[pp.ID] = &pane{Pane: proto.Pane{ID: pp.ID, Tab: pt.ID, Session: ps.ID, State: proto.PaneDormant, Cwd: pp.Cwd}}
				t.Panes = append(t.Panes, pp.ID)
			}
			s.tabs[pt.ID] = t
			x.Tabs = append(x.Tabs, pt.ID)
		}
		s.sessions[ps.ID] = x
		s.byDir[ps.Dir] = ps.ID
	}
	return s
}

// Save returns the structure of the state for state.json: the counters,
// and per session its directory, label, current tab, last-shown time, size
// and tabs in index order with their split, focused pane and each pane's id
// and directory. Pane states, titles, programs and agents are not saved.
func (s *State) Save() persist.State {
	out := persist.State{Version: persist.Version, Counters: s.next, Sessions: []persist.Session{}}
	for _, sid := range sortedIDs(s.sessions) {
		x := s.sessions[sid]
		ps := persist.Session{ID: x.ID, Dir: x.Dir, Label: x.Label, Current: x.Current, Shown: x.Shown, Cols: x.cols, Rows: x.rows}
		for _, tid := range x.Tabs {
			t := s.tabs[tid]
			pt := persist.Tab{ID: t.ID, Split: t.Split, Focused: t.Focused}
			for _, pid := range t.Panes {
				pt.Panes = append(pt.Panes, persist.Pane{ID: pid, Cwd: s.panes[pid].Cwd})
			}
			ps.Tabs = append(ps.Tabs, pt)
		}
		out.Sessions = append(out.Sessions, ps)
	}
	return out
}
