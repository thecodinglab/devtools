package fleet

import (
	"testing"
	"time"

	"devtools/internal/proto"
)

// These tests drive the bell of 3.6.3 through the loop's functions, with a
// clock that the test moves by hand (stillClock), so the end of the rate
// limit is the moment the test chooses (flushBells is what its timer runs).

// put applies a delta to M that puts the agent of pane p (tab t, session s)
// in status.
func put(t *testing.T, f *Fleet, mc *machine, s, tab, p, status string) {
	t.Helper()
	a := proto.Agent{Pane: p, Tab: tab, Session: s, Kind: "claude", Status: status}
	d := proto.Delta{Rev: mc.st.Rev + 1, Changes: []proto.Change{proto.Put(a)}}
	if err := f.applyDelta(mc, d); err != nil {
		t.Fatal(err)
	}
}

// later moves the clock of f by d.
func later(f *Fleet, d time.Duration) {
	f.clock = stillClock{now: f.clock.Now().Add(d)}
}

// NT8, 3.6.3 rule 3
func TestAHeldBellOfAStatusThatEndedIsNotWrittenAndTheNextOneRings(t *testing.T) {
	f, mc := onlineMachine(t)
	put(t, f, mc, "s4", "t4", "p4", "done")
	f.agentEvent(mc, event("s4", "t4", "p4", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p4, want 1", n)
	}
	later(f, 300*time.Millisecond)
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	if n := bells(f); n != 0 {
		t.Fatalf("%d bells inside the rate limit, want the bell held", n)
	}
	// p3 is back at work before the second ends
	put(t, f, mc, "s2", "t3", "p3", "working")
	later(f, 700*time.Millisecond)
	f.flushBells()
	if n := bells(f); n != 0 {
		t.Fatalf("%d bells for a blocked status that had ended, want none", n)
	}
	if s, ok := mc.rung["p3"]; ok {
		t.Fatalf("p3 is marked as rung in %q while it works", s)
	}
	// the next real transition rings
	later(f, 2*time.Second)
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p3 blocked again, want 1", n)
	}
}

func TestAHeldBellOfAnAgentThatIsGoneIsNotWritten(t *testing.T) {
	f, mc := onlineMachine(t)
	put(t, f, mc, "s4", "t4", "p4", "done")
	f.agentEvent(mc, event("s4", "t4", "p4", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p4, want 1", n)
	}
	later(f, 300*time.Millisecond)
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	d := proto.Delta{Rev: mc.st.Rev + 1, Changes: []proto.Change{proto.Del(proto.KindAgent, "p3")}}
	if err := f.applyDelta(mc, d); err != nil {
		t.Fatal(err)
	}
	later(f, 700*time.Millisecond)
	f.flushBells()
	if n := bells(f); n != 0 {
		t.Fatalf("%d bells for an agent that is gone, want none", n)
	}
}

func TestAHeldBellStillRingsWhenItsStatusHolds(t *testing.T) {
	f, mc := onlineMachine(t)
	put(t, f, mc, "s4", "t4", "p4", "done")
	f.agentEvent(mc, event("s4", "t4", "p4", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p4, want 1", n)
	}
	later(f, 300*time.Millisecond)
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	// another agent changes meanwhile; p3 stays blocked
	put(t, f, mc, "s2", "t2", "p2", "idle")
	later(f, 700*time.Millisecond)
	f.flushBells()
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells when the second ends, want the held one", n)
	}
	if mc.rung["p3"] != "blocked" {
		t.Fatalf("p3 rung %q, want blocked", mc.rung["p3"])
	}
}

// 3.6.3 rule 4
func TestAHeldBellOfAServerThatRestartedIsNotWritten(t *testing.T) {
	f, mc := onlineMachine(t)
	put(t, f, mc, "s4", "t4", "p4", "done")
	f.agentEvent(mc, event("s4", "t4", "p4", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p4, want 1", n)
	}
	later(f, 300*time.Millisecond)
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	// another server answers, with p3 blocked in its first snapshot
	f.newServer(mc)
	later(f, 700*time.Millisecond)
	f.flushBells()
	if n := bells(f); n != 0 {
		t.Fatalf("%d bells for what a new server's snapshot holds, want none", n)
	}
}

// event.agent may come before the delta with the same transition
func TestAHeldBellRingsWhenItsDeltaComesAfterTheEvent(t *testing.T) {
	f, mc := onlineMachine(t)
	put(t, f, mc, "s4", "t4", "p4", "done")
	f.agentEvent(mc, event("s4", "t4", "p4", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p4, want 1", n)
	}
	later(f, 300*time.Millisecond)
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	// a delta of another agent, then the one of p3
	put(t, f, mc, "s2", "t2", "p2", "idle")
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	later(f, 700*time.Millisecond)
	f.flushBells()
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells when the second ends, want the held one", n)
	}
}

func TestAHeldBellOfAClosedTabIsNotWritten(t *testing.T) {
	f, mc := onlineMachine(t)
	put(t, f, mc, "s4", "t4", "p4", "done")
	f.agentEvent(mc, event("s4", "t4", "p4", "done", false))
	if n := bells(f); n != 1 {
		t.Fatalf("%d bells for p4, want 1", n)
	}
	later(f, 300*time.Millisecond)
	put(t, f, mc, "s2", "t3", "p3", "blocked")
	f.agentEvent(mc, event("s2", "t3", "p3", "blocked", false))
	d := proto.Delta{Rev: mc.st.Rev + 1, Changes: []proto.Change{proto.Del(proto.KindTab, "t3")}}
	if err := f.applyDelta(mc, d); err != nil {
		t.Fatal(err)
	}
	later(f, 700*time.Millisecond)
	f.flushBells()
	if n := bells(f); n != 0 {
		t.Fatalf("%d bells for the agent of a closed tab, want none", n)
	}
}
