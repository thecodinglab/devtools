package fleet

import (
	"testing"
	"time"

	"devtools/internal/config"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// These tests look inside the loop: what is on screen is decided by the
// viewer of P2-06, which does not exist yet, so a fake one stands in. They
// call the loop's functions from the test goroutine, without Run, which is
// the only goroutine that would otherwise touch the state.

// showing is a viewer that displays one target.
type showing struct {
	noView
	t session.Target
}

func (v showing) state() (string, session.Target, bool) { return "showing", v.t, false }
func (v showing) holds(m string) bool                   { return m == v.t.Machine }

// stillClock is a clock whose timers never fire.
type stillClock struct{ now time.Time }

func (c stillClock) Now() time.Time                        { return c.now }
func (c stillClock) AfterFunc(time.Duration, func()) Timer { return stillTimer{} }

type stillTimer struct{}

func (stillTimer) Stop() bool { return true }

// onlineMachine puts machine M online with sessions s2 (tabs t2, t3; t2
// current) and s4, each tab with one pane and an agent.
func onlineMachine(t *testing.T) (*Fleet, *machine) {
	t.Helper()
	f := New(Options{Machines: []config.Machine{{Name: "M", Connect: true}}, Clock: stillClock{now: time.Unix(1795000000, 0)}})
	t.Cleanup(func() { f.updates.close() })
	mc := f.byName["M"]
	mc.status = "online"
	mc.st = proto.NewState(proto.Snapshot{
		Sessions: []proto.Session{
			{ID: "s2", Dir: "/a", Label: "a", Tabs: []string{"t2", "t3"}, Current: "t2"},
			{ID: "s4", Dir: "/b", Label: "b", Tabs: []string{"t4"}, Current: "t4"},
		},
		Tabs: []proto.Tab{
			{ID: "t2", Session: "s2", Index: 1, Panes: []string{"p2"}, Focused: "p2"},
			{ID: "t3", Session: "s2", Index: 2, Panes: []string{"p3"}, Focused: "p3"},
			{ID: "t4", Session: "s4", Index: 1, Panes: []string{"p4"}, Focused: "p4"},
		},
		Agents: []proto.Agent{
			{Pane: "p2", Tab: "t2", Session: "s2", Kind: "claude", Status: "working"},
			{Pane: "p3", Tab: "t3", Session: "s2", Kind: "claude", Status: "working"},
			{Pane: "p4", Tab: "t4", Session: "s4", Kind: "claude", Status: "working"},
		},
	})
	f.view = showing{t: session.Target{Machine: "M", Session: "s2"}}
	return f, mc
}

// bells counts the bells that reach Updates within a moment.
func bells(f *Fleet) int {
	n := 0
	deadline := time.After(50 * time.Millisecond)
	for {
		select {
		case u := <-f.Updates():
			if u.Kind == Bell {
				n++
			}
		case <-deadline:
			return n
		}
	}
}

func event(sess, tab, pane, status string, shown bool) proto.AgentEvent {
	return proto.AgentEvent{Session: sess, Tab: tab, Pane: pane, Kind: "claude", Status: status, Shown: shown}
}

// NT3
func TestNoBellForWhatIsOnScreen(t *testing.T) {
	f, mc := onlineMachine(t)
	f.agentEvent(mc, event("s2", "t2", "p2", "blocked", false))
	if n := bells(f); n != 0 {
		t.Fatalf("%d bells for the agent of the tab on screen, want none", n)
	}
	if s := f.computeSummary(); s.Urgent != (session.Target{}) {
		t.Fatalf("urgent %+v: the agent on screen is not where to look", s.Urgent)
	}
}

func TestABellRingsForAnotherTabOfTheDisplayedSession(t *testing.T) {
	f, mc := onlineMachine(t)
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for a tab that is not the current one, want 1", n)
	}
}

func TestABellRingsWhenTheTerminalHasNoFocus(t *testing.T) {
	f, mc := onlineMachine(t)
	f.focused = false
	f.agentEvent(mc, event("s2", "t2", "p2", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for the tab on screen without focus, want 1", n)
	}
}

func TestTheSummaryCarriesWhatTheViewerShows(t *testing.T) {
	f, mc := onlineMachine(t)
	mc.st.Agents["p4"] = proto.Agent{Pane: "p4", Tab: "t4", Session: "s4", Kind: "claude", Status: "done"}
	s := f.computeSummary()
	if s.Mode != "showing" || s.Target.Session != "s2" || s.Target.Machine != "M" {
		t.Fatalf("summary mode %q target %+v", s.Mode, s.Target)
	}
	if s.Urgent != (session.Target{Machine: "M", Session: "s4", Tab: "t4", Pane: "p4"}) {
		t.Fatalf("urgent %+v", s.Urgent)
	}
}

func TestAMachineTheWindowHoldsIsSupervisedAndNotClosedWhenIdle(t *testing.T) {
	f := New(Options{Machines: []config.Machine{{Name: "K"}}, Clock: stillClock{}})
	t.Cleanup(func() { f.updates.close() })
	k := f.byName["K"]
	if f.supervised(k) {
		t.Fatal("an on-demand machine nobody shows is supervised")
	}
	f.view = showing{t: session.Target{Machine: "K", Session: "s2"}}
	if !f.supervised(k) {
		t.Fatal("the machine of the displayed session is not supervised")
	}
	k.link = nil
	f.startIdle(k)
	if k.idle != nil {
		t.Fatal("the machine of the displayed session got an idle timer")
	}
}

func TestPipeCoalescesWaitingSummaries(t *testing.T) {
	p := newPipe[Update](1, func(u Update) bool { return u.Kind == Summary })
	defer p.close()
	p.put(Update{Kind: Notice, Notice: "a"})
	p.put(Update{Kind: Notice, Notice: "x"})
	// out holds the first, the pipe the second; the rest wait in the queue
	time.Sleep(10 * time.Millisecond)
	p.put(Update{Kind: Summary, Summary: SummaryData{Mode: "one"}})
	p.put(Update{Kind: Notice, Notice: "b"})
	p.put(Update{Kind: Summary, Summary: SummaryData{Mode: "two"}})
	var got []string
	for range 4 {
		u := <-p.out
		if u.Kind == Summary {
			got = append(got, u.Summary.Mode)
		} else {
			got = append(got, u.Notice)
		}
	}
	want := []string{"a", "x", "b", "two"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	select {
	case u := <-p.out:
		t.Fatalf("an extra update %+v", u)
	case <-time.After(20 * time.Millisecond):
	}
}
