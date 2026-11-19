package fleet_test

import (
	"testing"
	"time"

	"devtools/internal/agent"
	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
)

func sess(n int, status string) fleettest.Sess {
	return fleettest.Sess{N: n, Dir: "/home/florian/dev/s" + itoa(n), Label: "s" + itoa(n), Agent: status}
}

// NT1
func TestRollupCountsAgentsOfEveryConnectedMachine(t *testing.T) {
	r := fleettest.New(t, machines("M connect")...)
	r.M("").Online(fleettest.Snapshot(sess(1, proto.StatusWorking)))
	r.M("M").Online(fleettest.Snapshot(sess(2, proto.StatusBlocked), sess(3, proto.StatusIdle)))
	s := r.Settle(func(s fleet.SummaryData) bool {
		return fleettest.StateOf(s, "") == "online" && fleettest.StateOf(s, "M") == "online"
	})
	if s.Top != agent.Blocked {
		t.Fatalf("top %q, want blocked", s.Top)
	}
	want := map[agent.Status]int{agent.Working: 1, agent.Blocked: 1, agent.Idle: 1}
	if len(s.Counts) != 3 || s.Counts[agent.Working] != 1 || s.Counts[agent.Blocked] != 1 || s.Counts[agent.Idle] != 1 {
		t.Fatalf("counts %v, want %v", s.Counts, want)
	}
	l, m := fleettest.MachineOf(t, s, ""), fleettest.MachineOf(t, s, "M")
	if l.Agents[agent.Working] != 1 || len(l.Agents) != 1 || m.Agents[agent.Blocked] != 1 || m.Agents[agent.Idle] != 1 || len(m.Agents) != 2 {
		t.Fatalf("per machine %v and %v", l.Agents, m.Agents)
	}
	if l.Busy != 1 || m.Busy != 1 {
		t.Fatalf("busy %d and %d, want 1 and 1", l.Busy, m.Busy)
	}
	if s.Urgent != (session.Target{Machine: "M", Session: "s2", Tab: "t2", Pane: "p2"}) {
		t.Fatalf("urgent %+v, want the blocked agent of M", s.Urgent)
	}
	if s.Mode != "none" {
		t.Fatalf("mode %q without a view, want none", s.Mode)
	}
}

// NT2
func TestABlockedAgentOnAnotherMachineRingsOnce(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking)))
	r.None(fleet.Bell)
	r.M("M").Push(fleettest.AgentEvent(2, proto.StatusBlocked, false))
	r.M("M").Push(fleettest.AgentDelta(2, 2, proto.StatusBlocked))
	r.Next(fleet.Bell)
	s := r.Settle(func(s fleet.SummaryData) bool { return s.Top == agent.Blocked })
	if s.Urgent.Machine != "M" || s.Urgent.Pane != "p2" {
		t.Fatalf("urgent %+v", s.Urgent)
	}
	r.Advance(5 * time.Second)
	r.None(fleet.Bell)
	if n := r.Count(fleet.Bell); n != 1 {
		t.Fatalf("%d bells, want 1", n)
	}
}

// NT4
func TestAReconnectDoesNotRingAgain(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking)))
	r.M("M").Push(fleettest.AgentEvent(2, proto.StatusBlocked, false))
	r.M("M").Push(fleettest.AgentDelta(2, 2, proto.StatusBlocked))
	r.Next(fleet.Bell)
	r.M("M").Drop()
	r.M("M").Online(fleettest.Snapshot(sess(2, proto.StatusBlocked)))
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "online" && s.Top == agent.Blocked })
	r.Advance(2 * time.Second)
	r.None(fleet.Bell)
}

// NT11
func TestAnAgentThatBlockedDuringAnOutageRingsOnReconnect(t *testing.T) {
	t.Run("same boot", func(t *testing.T) {
		r := start(t, "M connect")
		online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking), sess(3, proto.StatusWorking)))
		r.M("M").Push(fleettest.AgentEvent(2, proto.StatusBlocked, false))
		r.M("M").Push(fleettest.AgentDelta(2, 2, proto.StatusBlocked))
		r.Next(fleet.Bell)
		r.Settle(func(s fleet.SummaryData) bool { return s.Top == agent.Blocked })
		r.M("M").Drop()
		r.M("M").Online(fleettest.Snapshot(sess(2, proto.StatusBlocked), sess(3, proto.StatusBlocked)))
		r.Clock.WaitTimer(t, 500*time.Millisecond)
		r.Advance(500 * time.Millisecond)
		r.Settle(func(s fleet.SummaryData) bool { return s.Counts[agent.Blocked] == 2 })
		r.Advance(time.Second)
		r.Next(fleet.Bell)
		r.Advance(5 * time.Second)
		r.None(fleet.Bell)
		if n := r.Count(fleet.Bell); n != 2 {
			t.Fatalf("%d bells, want 2: the first block, and one for the agent that blocked during the outage", n)
		}
	})
	t.Run("another boot", func(t *testing.T) {
		r := start(t, "M connect")
		online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking)))
		r.M("M").Drop()
		w := r.M("M").Welcome()
		w.Boot = "boot-M-2"
		r.M("M").SetWelcome(w)
		r.M("M").Online(fleettest.Snapshot(sess(2, proto.StatusBlocked)))
		r.Clock.WaitTimer(t, 500*time.Millisecond)
		r.Advance(500 * time.Millisecond)
		r.Settle(func(s fleet.SummaryData) bool { return s.Top == agent.Blocked })
		r.Advance(2 * time.Second)
		r.None(fleet.Bell)
	})
}

// NT12
func TestNoBellInASecondWindowForAnAgentShownInTheFirst(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking)))
	r.M("M").Push(fleettest.AgentEvent(2, proto.StatusBlocked, true))
	r.M("M").Push(fleettest.AgentDelta(2, 2, proto.StatusBlocked))
	r.Settle(func(s fleet.SummaryData) bool { return s.Top == agent.Blocked })
	r.Advance(2 * time.Second)
	r.None(fleet.Bell)
}

func TestALocalAgentRingsLikeARemoteOne(t *testing.T) {
	r := fleettest.New(t)
	r.M("").Online(fleettest.Snapshot(sess(1, proto.StatusWorking)))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "") == "online" })
	r.M("").Push(fleettest.AgentEvent(1, proto.StatusDone, false))
	r.Next(fleet.Bell)
	// the same status again does not ring; leaving it and coming back does
	r.M("").Push(fleettest.AgentEvent(1, proto.StatusDone, false))
	r.Advance(2 * time.Second)
	r.None(fleet.Bell)
	r.M("").Push(fleettest.AgentDelta(2, 1, proto.StatusWorking))
	r.M("").Push(fleettest.AgentEvent(1, proto.StatusDone, false))
	r.Next(fleet.Bell)
}

// NT8
func TestBellsAreRateLimited(t *testing.T) {
	r := start(t, "M connect")
	var ss []fleettest.Sess
	for i := 2; i <= 12; i++ {
		ss = append(ss, sess(i, proto.StatusWorking))
	}
	online(t, r, "M", fleettest.Snapshot(ss...))
	for i := 2; i <= 11; i++ {
		r.M("M").Push(fleettest.AgentEvent(i, proto.StatusDone, false))
	}
	r.Next(fleet.Bell)
	r.None(fleet.Bell)
	r.Advance(300 * time.Millisecond)
	r.M("M").Push(fleettest.AgentEvent(12, proto.StatusBlocked, false))
	r.Clock.WaitTimer(t, 700*time.Millisecond)
	r.None(fleet.Bell)
	r.Advance(700 * time.Millisecond)
	r.Next(fleet.Bell)
	r.Advance(5 * time.Second)
	r.None(fleet.Bell)
	if n := r.Count(fleet.Bell); n != 2 {
		t.Fatalf("%d bells, want 2", n)
	}
}

// NT8
func TestABellInsideTheRateLimitIsRungWhenItEnds(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking), sess(3, proto.StatusWorking)))
	r.M("M").Push(fleettest.AgentEvent(2, proto.StatusDone, false))
	r.Next(fleet.Bell)
	r.Advance(900 * time.Millisecond)
	r.M("M").Push(fleettest.AgentEvent(3, proto.StatusBlocked, false))
	r.Clock.WaitTimer(t, 100*time.Millisecond)
	r.Advance(99 * time.Millisecond)
	r.None(fleet.Bell)
	r.Advance(time.Millisecond)
	r.Next(fleet.Bell)
	// it was written: the same status does not ring again
	r.M("M").Push(fleettest.AgentEvent(3, proto.StatusBlocked, false))
	r.Advance(5 * time.Second)
	r.None(fleet.Bell)
}

func TestNoBellFromAMachineThatIsNotOnline(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking)))
	// an invalid pane id is no agent
	r.M("M").Push(proto.Encode(proto.AgentEvent{Session: "s2", Tab: "t2", Pane: "x2", Kind: "claude", Status: proto.StatusBlocked}))
	r.None(fleet.Bell)
}

func TestSummaryUpdatesAreCoalesced(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusIdle)))
	r.Take(fleet.Summary)
	before := r.Count(fleet.Summary)
	statuses := []string{proto.StatusWorking, proto.StatusIdle}
	for i := range 100 {
		r.M("M").Push(fleettest.AgentDelta(uint64(i+2), 2, statuses[i%2]))
	}
	r.M("M").Push(fleettest.AgentDelta(102, 2, proto.StatusDone))
	// every delta is read within this, in one instant of the fake clock
	time.Sleep(200 * time.Millisecond)
	r.Advance(50 * time.Millisecond)
	s := r.Summary(func(s fleet.SummaryData) bool { return s.Top == agent.Done })
	if s.Counts[agent.Done] != 1 || len(s.Counts) != 1 {
		t.Fatalf("counts %v, want the newest state", s.Counts)
	}
	if n := r.Count(fleet.Summary) - before; n > 2 {
		t.Fatalf("%d summaries for 101 changes in one instant, want at most 2", n)
	}
	r.Advance(time.Second)
	r.None(fleet.Summary)
}

func TestTheLoopNeverBlocksOnASlowConsumer(t *testing.T) {
	r := fleettest.NewUnread(t, machines("M connect", "K")...)
	r.M("").Online(fleettest.Snapshot(here))
	r.M("M").Online(fleettest.Snapshot(sess(2, proto.StatusIdle)))
	r.Eventually("M's link", func() bool { return r.M("M").Live() })
	_ = r.Hub() // opened, never read
	for range 1000 {
		r.F.Switch(session.Target{Machine: "M", Session: "s2"})
	}
	statuses := []string{proto.StatusWorking, proto.StatusIdle}
	for i := range 600 {
		r.M("M").Push(fleettest.AgentDelta(uint64(i+2), 2, statuses[i%2]))
	}
	for i := range 300 {
		r.M("M").Push(fleettest.AgentEvent(2, []string{proto.StatusDone, proto.StatusBlocked}[i%2], false))
		r.Advance(time.Second)
	}
	r.F.HubClosed()
	r.M("K").Online(fleettest.Snapshot())
	_ = r.Hub()
	r.Eventually("K's catalog.watch", func() bool { return len(r.M("K").SentOf(proto.TypeCatalogWatch)) == 1 })
	r.F.HubClosed()
	r.Eventually("catalog.stop to K", func() bool { return len(r.M("K").SentOf(proto.TypeCatalogStop)) == 1 })
}

func TestColorsReachEveryLinkAndLinksThatComeLater(t *testing.T) {
	r := start(t, "M")
	colors := proto.Encode(proto.ViewColors{Fg: "#c0caf5", Bg: "#1a1b26", Dark: true})
	r.F.Colors(colors)
	r.Eventually("view.colors to the local server", func() bool { return len(r.M("").SentOf(proto.TypeViewColors)) == 1 })
	r.M("M").Online(fleettest.Snapshot())
	r.OpenHub().Next("M", hub.Online)
	r.Eventually("view.colors to M", func() bool { return len(r.M("M").SentOf(proto.TypeViewColors)) == 1 })
	var c proto.ViewColors
	if err := r.M("M").SentOf(proto.TypeViewColors)[0].Decode(&c); err != nil || c.Fg != "#c0caf5" || !c.Dark {
		t.Fatalf("view.colors %+v, %v", c, err)
	}
}

// agentStatus waits for an Agents event of machine on feed in which the
// agent of pane has status: the loop has read every message pushed before
// the delta that made it so.
func agentStatus(t *testing.T, feed *fleettest.Feed, machine, pane, status string) {
	t.Helper()
	deadline := time.Now().Add(fleettest.Wait)
	for time.Now().Before(deadline) {
		ev := feed.Next(machine, hub.Agents)
		for _, it := range ev.Items {
			if it.Pane == pane && it.Status == status {
				return
			}
		}
	}
	t.Fatalf("no Agents event of %q with %s %s", machine, pane, status)
}

// NT8, 3.6.3 rule 3
func TestABellHeldForAStatusThatEndedDoesNotSilenceTheNextOne(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(sess(2, proto.StatusWorking), sess(3, proto.StatusWorking)))
	feed := r.OpenHub() // its Agents events say when a delta was read
	r.M("M").Push(fleettest.AgentDelta(2, 2, proto.StatusDone))
	r.M("M").Push(fleettest.AgentEvent(2, proto.StatusDone, false))
	r.Next(fleet.Bell)
	r.Advance(300 * time.Millisecond)
	r.M("M").Push(fleettest.AgentDelta(3, 3, proto.StatusBlocked))
	r.M("M").Push(fleettest.AgentEvent(3, proto.StatusBlocked, false))
	r.Clock.WaitTimer(t, 700*time.Millisecond)
	agentStatus(t, feed, "M", "p3", proto.StatusBlocked)
	// p3 is back at work before the second ends
	r.M("M").Push(fleettest.AgentDelta(4, 3, proto.StatusWorking))
	agentStatus(t, feed, "M", "p3", proto.StatusWorking)
	r.Advance(700 * time.Millisecond)
	r.None(fleet.Bell)
	// the next real transition rings
	r.Advance(2 * time.Second)
	r.M("M").Push(fleettest.AgentDelta(5, 3, proto.StatusBlocked))
	r.M("M").Push(fleettest.AgentEvent(3, proto.StatusBlocked, false))
	r.Next(fleet.Bell)
}
