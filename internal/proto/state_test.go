package proto

import (
	"errors"
	"reflect"
	"testing"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		Rev: 41,
		Sessions: []Session{
			{ID: "s4", Dir: "/d/main", Label: "d-main", Tabs: []string{"t18", "t17"}, Current: "t17"},
			{ID: "s5", Dir: "/d/other", Label: "d-other", Tabs: []string{"t20"}, Current: "t20"},
		},
		Tabs: []Tab{
			{ID: "t17", Session: "s4", Index: 1, Panes: []string{"p53", "p52"}, Split: SplitRight, Focused: "p52"},
			{ID: "t18", Session: "s4", Index: 2, Panes: []string{"p60"}, Focused: "p60"},
			{ID: "t20", Session: "s5", Index: 1, Panes: []string{"p70"}, Focused: "p70"},
		},
		Panes: []Pane{
			{ID: "p52", Tab: "t17", Session: "s4", State: PaneRunning},
			{ID: "p53", Tab: "t17", Session: "s4", State: PaneRunning},
			{ID: "p60", Tab: "t18", Session: "s4", State: PaneDormant},
			{ID: "p70", Tab: "t20", Session: "s5", State: PaneRunning},
		},
		Agents: []Agent{
			{Pane: "p60", Tab: "t18", Session: "s4", Kind: AgentCodex, Status: StatusDone},
			{Pane: "p52", Tab: "t17", Session: "s4", Kind: AgentClaude, Status: StatusWorking},
			{Pane: "p70", Tab: "t20", Session: "s5", Kind: AgentClaude, Status: StatusIdle},
		},
	}
}

func TestStateApplyRejectsARevisionGap(t *testing.T) {
	s := NewState(sampleSnapshot())
	for _, rev := range []uint64{41, 43, 0, 40} {
		err := s.Apply(Delta{Rev: rev, Changes: []Change{Del(KindPane, "p52")}})
		if !errors.Is(err, ErrGap) {
			t.Fatalf("rev %d after 41: err = %v, want ErrGap", rev, err)
		}
	}
	if s.Rev != 41 || len(s.Panes) != 4 {
		t.Fatalf("a refused delta changed the state: rev %d, %d panes", s.Rev, len(s.Panes))
	}
	if err := s.Apply(Delta{Rev: 42}); err != nil || s.Rev != 42 {
		t.Fatalf("the next revision: rev %d, err %v", s.Rev, err)
	}
}

func TestStateApplyPutAndDel(t *testing.T) {
	s := NewState(sampleSnapshot())
	newPane := Pane{ID: "p54", Tab: "t18", Session: "s4", State: PaneRunning, Title: "vim"}
	tab := Tab{ID: "t18", Session: "s4", Index: 2, Panes: []string{"p60", "p54"}, Split: SplitDown, Focused: "p54"}
	d := Delta{Rev: 42, Changes: []Change{
		Put(newPane),
		Put(tab),
		Put(Agent{Pane: "p54", Tab: "t18", Session: "s4", Kind: "aider", Status: "pondering"}),
		Del(KindAgent, "p60"),
		Del(KindPane, "p99"), // never existed: not an error
	}}
	if err := s.Apply(d); err != nil {
		t.Fatal(err)
	}
	if s.Rev != 42 {
		t.Fatalf("rev %d", s.Rev)
	}
	if got := s.Panes["p54"]; !reflect.DeepEqual(got, newPane) {
		t.Fatalf("pane p54 = %+v", got)
	}
	if got := s.Tabs["t18"]; !reflect.DeepEqual(got, tab) {
		t.Fatalf("tab t18 = %+v", got)
	}
	if a := s.Agents["p54"]; a.Kind != Unknown || a.Status != Unknown {
		t.Fatalf("unknown agent values not normalised: %+v", a)
	}
	if _, ok := s.Agents["p60"]; ok {
		t.Fatal("agent p60 not deleted")
	}

	// A malformed change refuses the whole delta.
	bad := []Change{
		{Op: "move", Kind: KindPane, ID: "p52"},
		{Op: OpPut, Kind: "window", ID: "w1", Value: []byte(`{}`)},
		{Op: OpPut, Kind: KindPane, ID: "p52"},
		{Op: OpPut, Kind: KindPane, ID: "p52", Value: []byte(`{"id":"p53"}`)},
		{Op: OpPut, Kind: KindTab, ID: "t1", Value: []byte(`[1]`)},
		{Op: OpDel, Kind: KindPane, ID: ""},
	}
	for _, c := range bad {
		err := s.Apply(Delta{Rev: 43, Changes: []Change{Del(KindPane, "p52"), c}})
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("change %+v: err = %v, want ErrMalformed", c, err)
		}
		if s.Rev != 42 {
			t.Fatalf("change %+v: revision moved", c)
		}
		if _, ok := s.Panes["p52"]; !ok {
			t.Fatalf("change %+v: the good change before it was applied", c)
		}
	}

	// A put whose value omits its id takes the change's id.
	if err := s.Apply(Delta{Rev: 43, Changes: []Change{{Op: OpPut, Kind: KindSession, ID: "s9", Value: []byte(`{"dir":"/x"}`)}}}); err != nil {
		t.Fatal(err)
	}
	if s.Sessions["s9"].ID != "s9" {
		t.Fatalf("session s9 = %+v", s.Sessions["s9"])
	}
}

func TestApplyOnAZeroState(t *testing.T) {
	var s State
	if err := s.Apply(Delta{Rev: 1, Changes: []Change{Put(Session{ID: "s1"})}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Sessions["s1"]; !ok {
		t.Fatal("session not added to a zero State")
	}
}

func TestSessionViewHoldsTheCurrentTabAndItsPanes(t *testing.T) {
	s := NewState(sampleSnapshot())
	v, ok := s.View("s4")
	if !ok {
		t.Fatal("no view of s4")
	}
	if v.Session.ID != "s4" {
		t.Fatalf("session %+v", v.Session)
	}
	var tabs []string
	for _, tb := range v.Tabs {
		tabs = append(tabs, tb.ID)
	}
	if !reflect.DeepEqual(tabs, []string{"t17", "t18"}) {
		t.Fatalf("tabs %v, want index order t17 t18", tabs)
	}
	var panes []string
	for _, p := range v.Panes {
		panes = append(panes, p.ID)
	}
	if !reflect.DeepEqual(panes, []string{"p53", "p52"}) {
		t.Fatalf("panes %v, want the current tab's in tab order", panes)
	}
	var agents []string
	for _, a := range v.Agents {
		agents = append(agents, a.Pane)
	}
	if !reflect.DeepEqual(agents, []string{"p52", "p60"}) {
		t.Fatalf("agents %v, want the session's by tab", agents)
	}

	// The current tab changes with a delta.
	sess := s.Sessions["s4"]
	sess.Current = "t18"
	if err := s.Apply(Delta{Rev: 42, Changes: []Change{Put(sess)}}); err != nil {
		t.Fatal(err)
	}
	v, _ = s.View("s4")
	if len(v.Panes) != 1 || v.Panes[0].ID != "p60" {
		t.Fatalf("after the tab change: panes %+v", v.Panes)
	}
	if _, ok := s.View("s404"); ok {
		t.Fatal("a view of a missing session")
	}
}
