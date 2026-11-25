package model

import (
	"reflect"
	"testing"

	"devtools/internal/handover"
	"devtools/internal/proto"
	"devtools/internal/server/persist"
)

func TestHandoverAndAdoptKeepTheWholeState(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/w")
	s.SetSize(sid, 120, 40)
	s.Display(sid)
	first := s.sessions[sid].Tabs[0]
	p1 := s.tabs[first].Panes[0]
	p2 := splitPane(t, s, p1, "right", "/w/sub")
	splitPane(t, s, p2, "down", "")
	s.NewTab(sid, "/w")
	open(t, s, "/other")
	s.SetPane(p2, func(p *proto.Pane) {
		p.State = proto.PaneExited
		p.Exit = &proto.PaneExit{Code: -1, Signal: "SIGKILL"}
		p.Title = "t"
	})
	s.PutAgent(p1, proto.Agent{Kind: proto.AgentClaude, Status: proto.StatusWorking, Since: 5})
	s.SetResume(p1, &persist.Resume{Kind: "claude", ID: "0b9bd2a5-6d2f-4a8c-9e57-1c64b1b6f5a1"})

	got, err := Adopt(s.Handover())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Snapshot(), s.Snapshot()) {
		t.Fatalf("snapshot after adoption\n%+v\nwant\n%+v", got.Snapshot(), s.Snapshot())
	}
	if !reflect.DeepEqual(got.Save(), s.Save()) {
		t.Fatalf("saved state after adoption\n%+v\nwant\n%+v", got.Save(), s.Save())
	}
	for id, p := range s.panes {
		q := got.panes[id]
		if q.cols != p.cols || q.rows != p.rows || q.handed != p.handed || !reflect.DeepEqual(q.resume, p.resume) {
			t.Errorf("pane %s: %+v, want %+v", id, q, p)
		}
	}
	for id, x := range s.sessions {
		y := got.sessions[id]
		if y.cols != x.cols || y.rows != x.rows || y.displayed != x.displayed {
			t.Errorf("session %s: %+v, want %+v", id, y, x)
		}
	}
	// The ids go on where they were.
	_, _, r := got.OpenSession("/third", "")
	_ = r
	if got.next != (persist.Counters{Session: s.next.Session + 1, Tab: s.next.Tab + 1, Pane: s.next.Pane + 1}) {
		t.Errorf("counters after a new session %+v, before %+v", got.next, s.next)
	}
}

func TestAdoptRefusesABrokenState(t *testing.T) {
	s := newModel()
	sid := open(t, s, "/w")
	h := s.Handover()
	h.Tabs[0].Panes = append(h.Tabs[0].Panes, "p99")
	if _, err := Adopt(h); err == nil {
		t.Errorf("a tab that lists a missing pane was adopted")
	}
	h = s.Handover()
	h.Sessions[0].Tabs = []string{"t99"}
	if _, err := Adopt(h); err == nil {
		t.Errorf("a session that lists a missing tab was adopted")
	}
	h = s.Handover()
	h.Agents = append(h.Agents, handover.Agent{Pane: "p99"})
	if _, err := Adopt(h); err == nil {
		t.Errorf("an agent of a missing pane was adopted")
	}
	_ = sid
}
