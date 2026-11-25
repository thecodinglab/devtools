package model

import (
	"fmt"

	"devtools/internal/handover"
	"devtools/internal/proto"
	"devtools/internal/server/persist"
)

// Handover returns the whole state for a live upgrade of the server
// (internal/handover): unlike Save it keeps the live states of the panes,
// their sizes, titles and agent records, and whether each session was
// displayed, so the new server image goes on exactly where this one is.
func (s *State) Handover() handover.State {
	out := handover.State{Counters: handover.Counters{Session: s.next.Session, Tab: s.next.Tab, Pane: s.next.Pane}}
	for _, id := range sortedIDs(s.sessions) {
		x := s.sessions[id]
		out.Sessions = append(out.Sessions, handover.Session{
			ID: x.ID, Dir: x.Dir, Label: x.Label, Tabs: append([]string(nil), x.Tabs...), Current: x.Current,
			Shown: x.Shown, Cols: x.cols, Rows: x.rows, Displayed: x.displayed,
		})
	}
	for _, id := range sortedIDs(s.tabs) {
		t := s.tabs[id]
		out.Tabs = append(out.Tabs, handover.Tab{
			ID: t.ID, Session: t.Session, Index: t.Index, Panes: append([]string(nil), t.Panes...),
			Split: t.Split, Focused: t.Focused, Layout: nodeOut(t.Layout),
		})
	}
	for _, id := range sortedIDs(s.panes) {
		p := s.panes[id]
		r := handover.PaneRec{
			ID: p.ID, Tab: p.Tab, Session: p.Session, State: p.State, Cwd: p.Cwd, Program: p.Program,
			Editor: p.Editor, Busy: p.Busy, Title: p.Title, Cols: p.cols, Rows: p.rows, Handed: p.handed,
		}
		if p.Exit != nil {
			r.Exit = &handover.PaneExit{Code: p.Exit.Code, Signal: p.Exit.Signal, Text: p.Exit.Text}
		}
		if p.resume != nil {
			r.Resume = &handover.Resume{Kind: p.resume.Kind, ID: p.resume.ID}
		}
		out.Panes = append(out.Panes, r)
	}
	for _, id := range sortedIDs(s.agents) {
		a := s.agents[id]
		out.Agents = append(out.Agents, handover.Agent{Pane: a.Pane, Tab: a.Tab, Session: a.Session, Kind: a.Kind, Status: a.Status, Title: a.Title, Cwd: a.Cwd, Since: a.Since})
	}
	return out
}

// Adopt builds the state a new server image takes over from Handover. It
// checks that every reference holds (a tab's session and panes, a session's
// tabs, an agent's pane): a state that does not is refused, and the new
// image gives the process back to the old one.
func Adopt(h handover.State) (*State, error) {
	s := New()
	s.next = persist.Counters{Session: h.Counters.Session, Tab: h.Counters.Tab, Pane: h.Counters.Pane}
	for _, x := range h.Sessions {
		if x.ID == "" || s.sessions[x.ID] != nil {
			return nil, fmt.Errorf("model: session %q missing or twice", x.ID)
		}
		s.sessions[x.ID] = &sess{
			Session:   proto.Session{ID: x.ID, Dir: x.Dir, Label: x.Label, Tabs: append([]string(nil), x.Tabs...), Current: x.Current, Shown: x.Shown},
			cols:      x.Cols,
			rows:      x.Rows,
			displayed: x.Displayed,
		}
		s.byDir[x.Dir] = x.ID
	}
	for _, t := range h.Tabs {
		if t.ID == "" || s.tabs[t.ID] != nil || s.sessions[t.Session] == nil || len(t.Panes) == 0 {
			return nil, fmt.Errorf("model: tab %q", t.ID)
		}
		s.tabs[t.ID] = &proto.Tab{ID: t.ID, Session: t.Session, Index: t.Index, Panes: append([]string(nil), t.Panes...), Split: t.Split, Focused: t.Focused, Layout: nodeIn(t.Layout)}
	}
	for _, p := range h.Panes {
		if p.ID == "" || s.panes[p.ID] != nil || s.tabs[p.Tab] == nil || s.tabs[p.Tab].Session != p.Session {
			return nil, fmt.Errorf("model: pane %q", p.ID)
		}
		x := &pane{
			Pane:   proto.Pane{ID: p.ID, Tab: p.Tab, Session: p.Session, State: p.State, Cwd: p.Cwd, Program: p.Program, Editor: p.Editor, Busy: p.Busy, Title: p.Title},
			cols:   p.Cols,
			rows:   p.Rows,
			handed: p.Handed,
		}
		if p.Exit != nil {
			x.Exit = &proto.PaneExit{Code: p.Exit.Code, Signal: p.Exit.Signal, Text: p.Exit.Text}
		}
		if p.Resume != nil {
			x.resume = &persist.Resume{Kind: p.Resume.Kind, ID: p.Resume.ID}
		}
		s.panes[p.ID] = x
	}
	for _, x := range s.sessions {
		for _, tid := range x.Tabs {
			if t := s.tabs[tid]; t == nil || t.Session != x.ID {
				return nil, fmt.Errorf("model: session %s lists tab %q", x.ID, tid)
			}
		}
	}
	for _, t := range s.tabs {
		for _, pid := range t.Panes {
			if p := s.panes[pid]; p == nil || p.Tab != t.ID {
				return nil, fmt.Errorf("model: tab %s lists pane %q", t.ID, pid)
			}
		}
	}
	for _, a := range h.Agents {
		if s.panes[a.Pane] == nil {
			return nil, fmt.Errorf("model: an agent of pane %q", a.Pane)
		}
		s.agents[a.Pane] = proto.Agent{Pane: a.Pane, Tab: a.Tab, Session: a.Session, Kind: a.Kind, Status: a.Status, Title: a.Title, Cwd: a.Cwd, Since: a.Since}
	}
	return s, nil
}

func nodeOut(n proto.Node) handover.Node {
	o := handover.Node{Pane: n.Pane, Split: n.Split, Weight: n.Weight}
	for _, c := range n.Nodes {
		o.Nodes = append(o.Nodes, nodeOut(c))
	}
	return o
}

func nodeIn(n handover.Node) proto.Node {
	o := proto.Node{Pane: n.Pane, Split: n.Split, Weight: n.Weight}
	for _, c := range n.Nodes {
		o.Nodes = append(o.Nodes, nodeIn(c))
	}
	return o
}
