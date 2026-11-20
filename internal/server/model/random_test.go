package model

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"devtools/internal/proto"
	"devtools/internal/session"
)

// checkInvariants fails when the model breaks an invariant of 3.3.
func checkInvariants(s *State) error {
	if len(s.byDir) != len(s.sessions) {
		return fmt.Errorf("%d directories for %d sessions", len(s.byDir), len(s.sessions))
	}
	listedTabs := map[string]string{}
	for id, x := range s.sessions {
		if x.ID != id || !validID(id, 's', s.next.Session) {
			return fmt.Errorf("session %s has id %s", id, x.ID)
		}
		if s.byDir[x.Dir] != id {
			return fmt.Errorf("session %s: directory %s maps to %s", id, x.Dir, s.byDir[x.Dir])
		}
		if len(x.Tabs) == 0 {
			return fmt.Errorf("session %s has no tab", id)
		}
		if x.cols < proto.MinCols || x.cols > proto.MaxCols || x.rows < proto.MinRows || x.rows > proto.MaxRows {
			return fmt.Errorf("session %s has size %dx%d", id, x.cols, x.rows)
		}
		current := false
		for i, tid := range x.Tabs {
			t, ok := s.tabs[tid]
			if !ok {
				return fmt.Errorf("session %s lists missing tab %s", id, tid)
			}
			if t.Session != id || t.Index != i+1 {
				return fmt.Errorf("tab %s: session %s index %d, listed by %s at %d", tid, t.Session, t.Index, id, i+1)
			}
			if _, dup := listedTabs[tid]; dup {
				return fmt.Errorf("tab %s listed twice", tid)
			}
			listedTabs[tid] = id
			current = current || tid == x.Current
		}
		if !current {
			return fmt.Errorf("session %s: current %s is not one of %v", id, x.Current, x.Tabs)
		}
	}
	listedPanes := map[string]string{}
	for id, t := range s.tabs {
		if t.ID != id || !validID(id, 't', s.next.Tab) {
			return fmt.Errorf("tab %s has id %s", id, t.ID)
		}
		if listedTabs[id] == "" {
			return fmt.Errorf("tab %s is listed by no session", id)
		}
		if !slices.Equal(t.Layout.Leaves(), t.Panes) || t.Split != t.Layout.Split || !reflect.DeepEqual(t.Tree(), t.Layout) {
			return fmt.Errorf("tab %s: panes %v, split %q, layout %+v", id, t.Panes, t.Split, t.Layout)
		}
		if len(t.Panes) > proto.MaxPanes {
			return fmt.Errorf("tab %s: %d panes", id, len(t.Panes))
		}
		focused := false
		for _, pid := range t.Panes {
			p, ok := s.panes[pid]
			if !ok {
				return fmt.Errorf("tab %s lists missing pane %s", id, pid)
			}
			if p.Tab != id || p.Session != t.Session {
				return fmt.Errorf("pane %s: tab %s session %s, listed by %s of %s", pid, p.Tab, p.Session, id, t.Session)
			}
			if _, dup := listedPanes[pid]; dup {
				return fmt.Errorf("pane %s listed twice", pid)
			}
			listedPanes[pid] = id
			focused = focused || pid == t.Focused
		}
		if !focused {
			return fmt.Errorf("tab %s: focused %s is not one of %v", id, t.Focused, t.Panes)
		}
	}
	for id, p := range s.panes {
		if p.ID != id || !validID(id, 'p', s.next.Pane) {
			return fmt.Errorf("pane %s has id %s", id, p.ID)
		}
		if listedPanes[id] == "" {
			return fmt.Errorf("pane %s is listed by no tab", id)
		}
		switch p.State {
		case proto.PaneDormant, proto.PaneRunning:
			if p.Exit != nil {
				return fmt.Errorf("pane %s is %s with an exit", id, p.State)
			}
		case proto.PaneExited:
		default:
			return fmt.Errorf("pane %s has state %q", id, p.State)
		}
		x, t := s.sessions[p.Session], s.tabs[p.Tab]
		if p.State != proto.PaneDormant && !x.displayed {
			return fmt.Errorf("pane %s is %s in a session never displayed", id, p.State)
		}
		if x.Current != t.ID {
			continue
		}
		if p.State == proto.PaneDormant && x.displayed {
			return fmt.Errorf("pane %s is dormant in the current tab of a displayed session", id)
		}
		if p.State == proto.PaneRunning {
			r := s.rectOf(x, p)
			if p.cols != r.W || p.rows != r.H {
				return fmt.Errorf("pane %s runs at %dx%d, its layout is %dx%d", id, p.cols, p.rows, r.W, r.H)
			}
		}
	}
	for id, a := range s.agents {
		p, ok := s.panes[id]
		if !ok || a.Pane != id || a.Tab != p.Tab || a.Session != p.Session {
			return fmt.Errorf("agent %s = %+v", id, a)
		}
	}
	return nil
}

func validID(id string, kind byte, counter uint64) bool {
	if !session.ValidID(id) || id[0] != kind {
		return false
	}
	var n uint64
	fmt.Sscanf(id[1:], "%d", &n)
	return n >= 1 && n <= counter
}

// mirror is what a client holds: the state built from the first snapshot
// and every delta since, and the programs the server runs.
type mirror struct {
	state *proto.State
	live  map[string]bool // panes with a program, from the effects
}

// apply feeds one result to the mirror, change by change, and fails when
// the order of the changes or effects is not one the receiver and the
// server can apply.
func (m *mirror) apply(r Result) error {
	for i, c := range r.Changes {
		if err := m.state.Apply(proto.Delta{Rev: m.state.Rev + 1, Changes: []proto.Change{c}}); err != nil {
			return fmt.Errorf("change %d: %v", i, err)
		}
		st := m.state
		switch {
		case c.Op == proto.OpPut && c.Kind == proto.KindTab:
			for _, pid := range st.Tabs[c.ID].Panes {
				if _, ok := st.Panes[pid]; !ok {
					return fmt.Errorf("change %d: tab %s put before its pane %s", i, c.ID, pid)
				}
			}
		case c.Op == proto.OpPut && c.Kind == proto.KindSession:
			for _, tid := range st.Sessions[c.ID].Tabs {
				if _, ok := st.Tabs[tid]; !ok {
					return fmt.Errorf("change %d: session %s put before its tab %s", i, c.ID, tid)
				}
			}
		case c.Op == proto.OpPut && c.Kind == proto.KindAgent:
			if _, ok := st.Panes[c.ID]; !ok {
				return fmt.Errorf("change %d: agent put before its pane %s", i, c.ID)
			}
		case c.Op == proto.OpDel && c.Kind == proto.KindPane:
			for _, t := range st.Tabs {
				for _, pid := range t.Panes {
					if pid == c.ID {
						return fmt.Errorf("change %d: pane %s deleted while tab %s lists it", i, c.ID, t.ID)
					}
				}
			}
			if _, ok := st.Agents[c.ID]; ok {
				return fmt.Errorf("change %d: pane %s deleted before its agent", i, c.ID)
			}
		case c.Op == proto.OpDel && c.Kind == proto.KindTab:
			for _, x := range st.Sessions {
				for _, tid := range x.Tabs {
					if tid == c.ID {
						return fmt.Errorf("change %d: tab %s deleted while session %s lists it", i, c.ID, x.ID)
					}
				}
			}
		}
	}
	last := -1
	for i, e := range r.Effects {
		if rank(e.Kind) < last {
			return fmt.Errorf("effect %d (%s) after a later kind: %s", i, e.Kind, effects(r.Effects))
		}
		last = rank(e.Kind)
		switch e.Kind {
		case Spawn:
			if m.live[e.Pane] {
				return fmt.Errorf("effect %d: spawn of %s, which runs", i, e.Pane)
			}
			if e.Cols < MinPaneCols || e.Rows < MinPaneRows || e.Dir == "" {
				return fmt.Errorf("effect %d: spawn %+v", i, e)
			}
			m.live[e.Pane] = true
		case Resize:
			if !m.live[e.Pane] {
				return fmt.Errorf("effect %d: resize of %s, which does not run", i, e.Pane)
			}
		case Kill:
			delete(m.live, e.Pane)
		}
	}
	return nil
}

func TestRandomOperationsKeepInvariantsAndDeltasEqualSnapshots(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	s := newModel()
	m := &mirror{state: proto.NewState(s.Snapshot()), live: map[string]bool{}}
	dirs := []string{"/a", "/b", "/c", "/d/e", "/gone/f", "/g"}
	seen := map[string]bool{} // every id ever in the state
	present := map[string]bool{}

	pick := func(ids []string) string {
		if len(ids) == 0 || rng.IntN(20) == 0 {
			return []string{"s999", "t999", "p999", ""}[rng.IntN(4)]
		}
		return ids[rng.IntN(len(ids))]
	}
	keys := func(prefix string) []string {
		snap := s.Snapshot()
		var ids []string
		switch prefix {
		case "s":
			for _, x := range snap.Sessions {
				ids = append(ids, x.ID)
			}
		case "t":
			for _, x := range snap.Tabs {
				ids = append(ids, x.ID)
			}
		case "p":
			for _, x := range snap.Panes {
				ids = append(ids, x.ID)
			}
		}
		return ids
	}
	dir := func() string { return dirs[rng.IntN(len(dirs))] }
	cwd := func() string {
		if rng.IntN(2) == 0 {
			return ""
		}
		return dir()
	}
	splitDir := func() string { return []string{"right", "down", "left"}[rng.IntN(3)] }

	const ops = 10000
	counts := map[string]int{}
	for i := 0; i < ops; i++ {
		var r Result
		var err *Error
		var name string
		switch op := rng.IntN(17); op {
		case 0:
			name = "open"
			_, _, r = s.OpenSession(dir(), fmt.Sprintf("label-%d", i))
		case 1:
			name = "close-session"
			if rng.IntN(3) == 0 {
				r = s.CloseSession(pick(keys("s")))
			}
		case 2:
			name = "new-tab"
			_, _, r, err = s.NewTab(pick(keys("s")), cwd())
		case 3:
			name = "select-tab"
			switch rng.IntN(3) {
			case 0:
				_, r, err = s.SelectTab(pick(keys("s")), pick(keys("t")), 0, 0)
			case 1:
				_, r, err = s.SelectTab(pick(keys("s")), "", rng.IntN(5), 0)
			default:
				_, r, err = s.SelectTab(pick(keys("s")), "", 0, rng.IntN(5)-2)
			}
		case 4:
			name = "close-tab"
			if rng.IntN(2) == 0 {
				r, err = s.CloseTab(pick(keys("t")))
			}
		case 5, 6:
			name = "split"
			_, r, err = s.Split(pick(keys("p")), splitDir(), cwd())
		case 7:
			name = "focus"
			r, err = s.Focus(pick(keys("p")))
		case 8:
			name = "focus-dir"
			_, _, r, err = s.FocusDir(pick(keys("p")), []string{"left", "right", "up", "down", "x"}[rng.IntN(5)])
		case 16:
			name = "resize"
			_, r, err = s.Resize(pick(keys("p")), []string{"left", "right", "up", "down", "x"}[rng.IntN(5)], rng.IntN(12)-1)
		case 9:
			name = "close-pane"
			if rng.IntN(2) == 0 {
				r, err = s.ClosePane(pick(keys("p")))
			}
		case 10:
			name = "size"
			r = s.SetSize(pick(keys("s")), rng.IntN(220)-5, rng.IntN(70)-5)
		case 11:
			name = "display"
			r = s.Display(pick(keys("s")))
		case 12, 13:
			name = "set-pane"
			p := pick(keys("p"))
			r = s.SetPane(p, func(x *proto.Pane) {
				switch rng.IntN(6) {
				case 0:
					x.Title = fmt.Sprintf("title %d", rng.IntN(4))
				case 1:
					x.Cwd = dir()
				case 2:
					x.Program, x.Editor, x.Busy = "nvim", rng.IntN(2) == 0, rng.IntN(2) == 0
				case 3:
					x.State = proto.PaneExited
					x.Exit = &proto.PaneExit{Code: rng.IntN(3), Text: "ended"}
				case 4:
					x.State = proto.PaneRunning
				default:
					x.State = []string{proto.PaneDormant, "bogus"}[rng.IntN(2)]
				}
			})
			if p, ok := s.panes[p]; ok && p.State == proto.PaneExited {
				delete(m.live, p.ID) // the server saw the program end
			}
		case 14:
			name = "put-agent"
			r = s.PutAgent(pick(keys("p")), proto.Agent{
				Kind:   []string{"claude", "codex", "other"}[rng.IntN(3)],
				Status: []string{"idle", "working", "blocked", "done"}[rng.IntN(4)],
				Title:  "task", Since: int64(rng.IntN(3)),
			})
		case 15:
			name = "del-agent"
			r = s.DelAgent(pick(keys("p")))
		}
		counts[name]++
		if err != nil && len(r.Changes)+len(r.Effects) != 0 {
			t.Fatalf("op %d %s: an error with a result %+v", i, name, r)
		}
		if e := m.apply(r); e != nil {
			t.Fatalf("op %d %s: %v", i, name, e)
		}
		if e := checkInvariants(s); e != nil {
			t.Fatalf("op %d %s: %v", i, name, e)
		}
		want := proto.NewState(s.Snapshot())
		want.Rev = m.state.Rev
		if !reflect.DeepEqual(m.state, want) {
			t.Fatalf("op %d %s: the deltas do not give the snapshot\n got: %+v\nwant: %+v", i, name, m.state, want)
		}
		running := map[string]bool{}
		for id, p := range s.panes {
			if p.State == proto.PaneRunning {
				running[id] = true
			}
		}
		if !reflect.DeepEqual(running, m.live) && !(len(running) == 0 && len(m.live) == 0) {
			t.Fatalf("op %d %s: running panes %v, the effects started %v", i, name, running, m.live)
		}
		now := map[string]bool{}
		for _, ids := range [][]string{keys("s"), keys("t"), keys("p")} {
			for _, id := range ids {
				now[id] = true
				if seen[id] && !present[id] {
					t.Fatalf("op %d %s: id %s came back", i, name, id)
				}
				seen[id] = true
			}
		}
		present = now

		// Now and then the server restarts: what comes back is the saved
		// structure, every pane dormant, and the counters go on.
		if i%997 == 996 {
			saved := s.Save()
			s2 := Load(saved)
			s2.now, s2.exists = s.now, s.exists
			if got := s2.Save(); !reflect.DeepEqual(got, saved) {
				t.Fatalf("op %d: Load(Save()) saves\n%+v\nwant\n%+v", i, got, saved)
			}
			for id, p := range s2.panes {
				if p.State != proto.PaneDormant {
					t.Fatalf("op %d: restored pane %s is %s", i, id, p.State)
				}
			}
			if len(s2.agents) != 0 {
				t.Fatalf("op %d: agents survived a restart", i)
			}
			s = s2
			m = &mirror{state: proto.NewState(s.Snapshot()), live: map[string]bool{}}
			if e := checkInvariants(s); e != nil {
				t.Fatalf("op %d after the restart: %v", i, e)
			}
		}
	}
	var summary []string
	for name, n := range counts {
		summary = append(summary, fmt.Sprintf("%s %d", name, n))
	}
	t.Logf("operations: %s; %d ids issued", strings.Join(summary, ", "), len(seen))
	if len(seen) < 500 {
		t.Fatalf("only %d ids were issued: the operations do not reach far enough", len(seen))
	}
}
