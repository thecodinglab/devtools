package fleet_test

import (
	"slices"
	"testing"
	"time"

	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/ui"
)

// MC1
func TestOnlyConnectMachinesAreDialledAtStart(t *testing.T) {
	r := start(t, "M connect", "K")
	waitDials(t, r, "M", 1)
	if n := r.M("K").Dials(); n != 0 {
		t.Fatalf("K was dialled %d times at start, want 0", n)
	}
	for _, name := range r.Dialer.Names() {
		if name != "" && name != "M" {
			t.Fatalf("dialled %q at start; only the local machine and M may be", name)
		}
	}
}

// MC2
func TestAClientWithoutConnectMachinesHoldsNoConnections(t *testing.T) {
	r := start(t, "desktop", "macbook")
	for range 6 {
		r.Advance(10 * time.Minute)
	}
	time.Sleep(fleettest.Quiet)
	for _, name := range []string{"desktop", "macbook"} {
		if n := r.M(name).Dials(); n != 0 {
			t.Fatalf("%s was dialled %d times without the hub or a switch, want 0", name, n)
		}
	}
}

// MC3
func TestAnOnDemandMachineIsClosedTenMinutesAfterItsLastUse(t *testing.T) {
	r := start(t, "K")
	r.M("K").Online(fleettest.Snapshot())
	feed := r.OpenHub()
	feed.Next("K", hub.Online)
	// K is off before the hub opens too: what follows must be later
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "online" })
	r.F.HubClosed()
	feed.WaitClosed()
	r.Clock.WaitTimer(t, 10*time.Minute)
	r.Advance(10*time.Minute - time.Second)
	time.Sleep(fleettest.Quiet)
	if !r.M("K").Live() {
		t.Fatal("K's link was closed before 10 minutes had passed")
	}
	r.Advance(time.Second)
	r.Eventually("K's link closed", func() bool { return !r.M("K").Live() })
	n := r.Next(fleet.Notice)
	if want := ui.Notice("closed-idle", "K"); n.Notice != want {
		t.Fatalf("notice %q, want %q", n.Notice, want)
	}
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "off" })
	if got := fleettest.MachineOf(t, s, "K"); got.Problem != (hub.Problem{}) {
		t.Fatalf("K closed by its idle timer has the problem %+v, want none", got.Problem)
	}
}

func TestReopeningTheHubStopsTheIdleTimer(t *testing.T) {
	r := start(t, "K")
	r.M("K").Online(fleettest.Snapshot())
	feed := r.OpenHub()
	feed.Next("K", hub.Online)
	r.F.HubClosed()
	r.Clock.WaitTimer(t, 10*time.Minute)
	r.Advance(9 * time.Minute)
	r.OpenHub().Next("K", hub.Online)
	r.Advance(2 * time.Minute)
	time.Sleep(fleettest.Quiet)
	if !r.M("K").Live() {
		t.Fatal("K was closed while the hub was open")
	}
	if r.M("K").Dials() != 1 {
		t.Fatalf("K was dialled %d times, want 1: an online machine is not dialled again", r.M("K").Dials())
	}
}

// MC4
func TestMachinesComeOnlyFromConfig(t *testing.T) {
	r := start(t, "M connect")
	w := r.M("M").Welcome()
	w.Host = "macbook"
	r.M("M").SetWelcome(w)
	snap := fleettest.Snapshot(fleettest.Sess{N: 3, Dir: "/home/florian/dev/x", Label: "macbook"})
	online(t, r, "M", snap)
	feed := r.OpenHub()
	feed.Next("M", hub.Sessions)
	r.M("M").Push(proto.Encode(proto.UISwitch{Session: "s3"}))
	r.M("M").Push(proto.Encode(proto.UIHub{Mode: proto.HubAll, Here: "/macbook"}))
	r.Advance(time.Minute)
	time.Sleep(fleettest.Quiet)
	s, _ := r.Latest()
	var names []string
	for _, m := range s.Machines {
		names = append(names, m.Name)
	}
	if !slices.Equal(names, []string{"", "M"}) {
		t.Fatalf("machines %q, want exactly the config's", names)
	}
	for _, ev := range feed.All() {
		if ev.Machine != "" && ev.Machine != "M" {
			t.Fatalf("a hub event of the machine %q, which is not configured", ev.Machine)
		}
	}
	for _, name := range r.Dialer.Names() {
		if name != "" && name != "M" {
			t.Fatalf("dialled %q, which is not configured", name)
		}
	}
}

// MC6
func TestAMachineThatIsThisMachineIsDropped(t *testing.T) {
	t.Run("the local link is up first", func(t *testing.T) {
		r := start(t, "M connect", "K")
		w := r.M("M").Welcome()
		w.Boot = r.M("").Welcome().Boot
		r.M("M").SetWelcome(w)
		r.M("M").Online(fleettest.Snapshot())
		n := r.Next(fleet.Notice)
		if want := ui.Notice("self", "M"); n.Notice != want {
			t.Fatalf("notice %q, want %q", n.Notice, want)
		}
		r.Eventually("M's link closed", func() bool { return !r.M("M").Live() })
		s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "" })
		if len(s.Machines) != 2 || s.Machines[1].Name != "K" {
			t.Fatalf("machines %+v, want the local machine and K", s.Machines)
		}
		r.Advance(time.Hour)
		r.F.HubClosed()
		r.OpenHub()
		time.Sleep(fleettest.Quiet)
		if n := r.M("M").Dials(); n != 1 {
			t.Fatalf("M was dialled %d times, want 1: it is left out for this run", n)
		}
		r.None(fleet.Notice)
	})
	t.Run("the local link is up later", func(t *testing.T) {
		r := fleettest.New(t, machines("M connect")...)
		w := r.M("M").Welcome()
		w.Boot = r.M("").Welcome().Boot
		r.M("M").SetWelcome(w)
		online(t, r, "M", fleettest.Snapshot())
		r.M("").Online(fleettest.Snapshot(here))
		n := r.Next(fleet.Notice)
		if want := ui.Notice("self", "M"); n.Notice != want {
			t.Fatalf("notice %q, want %q", n.Notice, want)
		}
		r.Eventually("M's link closed", func() bool { return !r.M("M").Live() })
	})
}

// DS2
func TestAHangingSSHNeverBlocksTheHub(t *testing.T) {
	r := start(t, "M")
	r.M("M").Hang()
	feed := r.OpenHub()
	feed.Next("M", hub.Connecting)
	feed.Next("", hub.Sessions)
	id := watchID(t, r, "", 1)
	r.M("").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/devtools/main", "main")}}))
	r.M("").Push(proto.Request(id, proto.CatalogDone{}))
	feed.Next("", hub.Items)
	feed.Next("", hub.Complete)
	feed.None("M", hub.Failed)
	r.Clock.WaitTimer(t, 30*time.Second)
	r.Advance(30 * time.Second)
	ev := feed.Next("M", hub.Failed)
	if ev.Err.Reason != string(remote.Unreachable) {
		t.Fatalf("a hanging dial ended with %+v, want unreachable", ev.Err)
	}
}

// DS3
func TestEveryFailureReasonReachesTheHeading(t *testing.T) {
	reasons := []struct {
		reason    remote.Reason
		permanent string // the retry state of a permanent machine
	}{
		{remote.Auth, "in-30s"},
		{remote.HostKey, "in-30s"},
		{remote.Unreachable, "retrying"},
		{remote.NoDevtools, "in-30s"},
		{remote.NoServer, "in-30s"},
		{remote.Incompatible, "in-30s"},
		{remote.Protocol, "retrying"},
		{remote.NeedsLogin, "login"},
	}
	for _, c := range reasons {
		t.Run(string(c.reason)+" on demand", func(t *testing.T) {
			r := start(t, "M", "K")
			r.M("M").Fail(c.reason)
			r.M("K").Online(fleettest.Snapshot())
			feed := r.OpenHub()
			ev := feed.Next("M", hub.Failed)
			want := hub.Problem{Reason: string(c.reason), Text: string(c.reason), Retry: "next-open"}
			if ev.Err != want {
				t.Fatalf("heading %+v, want %+v", ev.Err, want)
			}
			feed.Next("K", hub.Online)
			// M is off before the hub opens too: wait for the off with the reason
			s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.MachineOf(t, s, "M").Problem.Reason != "" })
			if got := fleettest.MachineOf(t, s, "M").Problem; got != want {
				t.Fatalf("summary problem %+v, want %+v", got, want)
			}
		})
		t.Run(string(c.reason)+" permanent", func(t *testing.T) {
			r := start(t, "M connect")
			feed := r.OpenHub()
			feed.Next("M", hub.Connecting)
			r.M("M").Fail(c.reason)
			ev := feed.Next("M", hub.Failed)
			if ev.Err.Reason != string(c.reason) || ev.Err.Retry != c.permanent {
				t.Fatalf("heading %+v, want reason %s and retry %s", ev.Err, c.reason, c.permanent)
			}
			state := map[string]string{"in-30s": "attention", "login": "attention", "retrying": "retrying"}[c.permanent]
			r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == state })
		})
	}
}

func TestTheLocalLinkRetriesWhenNoServerRuns(t *testing.T) {
	r := fleettest.New(t)
	r.Eventually("the first local dial", func() bool { return r.M("").Dials() == 1 })
	r.M("").Fail(remote.NoServer)
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "") == "retrying" })
	if p := fleettest.MachineOf(t, s, "").Problem; p.Reason != string(remote.NoServer) || p.Retry != "retrying" {
		t.Fatalf("local problem %+v, want no-server, retrying", p)
	}
	r.Advance(500 * time.Millisecond)
	waitDials(t, r, "", 2)
	r.M("").Online(fleettest.Snapshot(here))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "") == "online" })
}

// DS7
func TestAnIncompatibleMachineIsShownAndNotRetriedInALoop(t *testing.T) {
	r := start(t, "M connect")
	feed := r.OpenHub()
	feed.Next("M", hub.Connecting)
	r.M("M").Fail(remote.Incompatible)
	if ev := feed.Next("M", hub.Failed); ev.Err.Reason != string(remote.Incompatible) || ev.Err.Retry != "in-30s" {
		t.Fatalf("heading %+v, want incompatible, in-30s", ev.Err)
	}
	r.Clock.WaitTimer(t, 30*time.Second)
	r.Advance(10 * time.Second)
	// opening the hub again within 30 s shows the reason and dials nothing
	r.F.HubClosed()
	again := r.OpenHub()
	if ev := again.Next("M", hub.Failed); ev.Err.Reason != string(remote.Incompatible) {
		t.Fatalf("heading on reopening %+v, want incompatible", ev.Err)
	}
	r.Advance(19 * time.Second)
	waitDials(t, r, "M", 1)
	r.M("M").Fail(remote.Incompatible)
	r.Advance(time.Second)
	waitDials(t, r, "M", 2)
	again.Next("M", hub.Failed)
	// and again 30 s later, not before
	r.Clock.WaitTimer(t, 30*time.Second)
	r.Advance(30*time.Second - time.Millisecond)
	waitDials(t, r, "M", 2)
	r.Advance(time.Millisecond)
	waitDials(t, r, "M", 3)
}

// NT6
func TestAConnectMachineIsRedialledWithBackoff(t *testing.T) {
	r := start(t, "M connect")
	r.F.Focus(false)
	feed := r.OpenHub()
	feed.Next("M", hub.Connecting)
	waits := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second, 120 * time.Second, 120 * time.Second,
	}
	for i, d := range waits {
		r.M("M").Fail(remote.Unreachable)
		feed.Next("M", hub.Failed)
		r.Clock.WaitTimer(t, d)
		r.Advance(d - time.Millisecond)
		waitDials(t, r, "M", i+1)
		r.Advance(time.Millisecond)
		waitDials(t, r, "M", i+2)
		feed.Next("M", hub.Connecting)
	}
	r.M("M").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/x", Label: "x", Agent: proto.StatusWorking}))
	feed.Next("M", hub.Online)
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "online" })
	if s.Counts["working"] != 1 {
		t.Fatalf("counts %v after the return, want its working agent", s.Counts)
	}
}

func TestWithFocusTheBackoffStopsAtTwoSeconds(t *testing.T) {
	r := start(t, "M connect")
	feed := r.OpenHub()
	feed.Next("M", hub.Connecting)
	waits := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second,
	}
	for i, d := range waits {
		r.M("M").Fail(remote.Unreachable)
		feed.Next("M", hub.Failed)
		r.Clock.WaitTimer(t, d)
		r.Advance(d - time.Millisecond)
		waitDials(t, r, "M", i+1)
		r.Advance(time.Millisecond)
		waitDials(t, r, "M", i+2)
		feed.Next("M", hub.Connecting)
	}
}

func TestGainingFocusBringsALongRetryForward(t *testing.T) {
	r := start(t, "M connect")
	r.F.Focus(false)
	feed := r.OpenHub()
	feed.Next("M", hub.Connecting)
	dials := 1
	for _, d := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second} {
		r.M("M").Fail(remote.Unreachable)
		feed.Next("M", hub.Failed)
		r.Clock.WaitTimer(t, d)
		r.Advance(d)
		dials++
		waitDials(t, r, "M", dials)
		feed.Next("M", hub.Connecting)
	}
	// unfocused the next wait is 8 s; focus a second in, and it dials a
	// second later, 2 s after the failure
	r.M("M").Fail(remote.Unreachable)
	feed.Next("M", hub.Failed)
	r.Clock.WaitTimer(t, 8*time.Second)
	r.Advance(time.Second)
	r.F.Focus(true)
	r.Clock.WaitTimer(t, time.Second)
	waitDials(t, r, "M", dials)
	r.Advance(time.Second)
	dials++
	waitDials(t, r, "M", dials)
	feed.Next("M", hub.Connecting)
	// a retry overdue by then dials at once
	r.F.Focus(false)
	r.M("M").Fail(remote.Unreachable)
	feed.Next("M", hub.Failed)
	r.Clock.WaitTimer(t, 16*time.Second)
	r.Advance(5 * time.Second)
	waitDials(t, r, "M", dials)
	r.F.Focus(true)
	r.Clock.WaitTimer(t, 0)
	r.Advance(0)
	dials++
	waitDials(t, r, "M", dials)
}

func TestTheBackoffStartsAgainAfterAMinuteOnline(t *testing.T) {
	r := start(t, "M connect")
	feed := r.OpenHub()
	r.M("M").Fail(remote.Unreachable)
	feed.Next("M", hub.Failed)
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	r.M("M").Fail(remote.Unreachable)
	r.Clock.WaitTimer(t, time.Second)
	r.Advance(time.Second)
	r.M("M").Online(fleettest.Snapshot())
	feed.Next("M", hub.Online)
	r.Advance(time.Minute)
	r.M("M").Drop()
	feed.Next("M", hub.Failed)
	r.Clock.WaitTimer(t, 500*time.Millisecond)
}

// NT5
func TestAgentsOfALostMachineLeaveTheRollup(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/x", Label: "x", Agent: proto.StatusBlocked}))
	r.Settle(func(s fleet.SummaryData) bool { return s.Counts["blocked"] == 1 })
	r.M("M").Drop()
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "retrying" })
	if len(s.Counts) != 0 || s.Top != "" {
		t.Fatalf("counts %v, top %q with M lost, want nothing counted", s.Counts, s.Top)
	}
	if m := fleettest.MachineOf(t, s, "M"); len(m.Agents) != 0 || m.Busy != 1 || m.Problem.Reason != "lost" {
		t.Fatalf("M %+v, want no agents counted, busy 1, the reason lost", m)
	}
	r.None(fleet.Bell)
}

// NT7
func TestAnOffMachineStaysInTheSummary(t *testing.T) {
	r := start(t, "K")
	r.M("K").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/x", Label: "x", Agent: proto.StatusIdle}))
	feed := r.OpenHub()
	feed.Next("K", hub.Online)
	// K is off before the hub opens too: what follows must be later
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "online" })
	r.F.HubClosed()
	r.Clock.WaitTimer(t, 10*time.Minute)
	r.Advance(10 * time.Minute)
	r.Eventually("K closed", func() bool { return !r.M("K").Live() })
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "off" })
	if m := fleettest.MachineOf(t, s, "K"); m.Busy != 0 {
		t.Fatalf("K %+v, want busy 0: its agent was idle", m)
	}
}

// NT7
func TestAnOffMachineWithAgentsStaysInTheStatusRow(t *testing.T) {
	r := start(t, "K")
	r.M("K").Online(fleettest.Snapshot(
		fleettest.Sess{N: 2, Dir: "/home/florian/dev/x", Label: "x", Agent: proto.StatusWorking},
		fleettest.Sess{N: 3, Dir: "/home/florian/dev/y", Label: "y", Agent: proto.StatusIdle},
	))
	feed := r.OpenHub()
	feed.Next("K", hub.Online)
	// K is off before the hub opens too: what follows must be later
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "online" })
	r.F.HubClosed()
	r.Clock.WaitTimer(t, 10*time.Minute)
	r.Advance(10 * time.Minute)
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "off" })
	m := fleettest.MachineOf(t, s, "K")
	if m.Busy != 1 {
		t.Fatalf("K %+v, want busy 1: an agent worked when the link closed", m)
	}
	if len(m.Agents) != 0 || s.Counts["working"] != 0 {
		t.Fatalf("an off machine's agents are counted: %+v, %v", m.Agents, s.Counts)
	}
	// it stays so until K is dialled again
	r.Advance(time.Hour)
	if s, _ := r.Latest(); fleettest.MachineOf(t, s, "K").Busy != 1 {
		t.Fatalf("K lost its busy count while off")
	}
}

// MC4, SEC8
func TestAMachineCannotRenameItself(t *testing.T) {
	r := start(t, "macmini connect")
	w := r.M("macmini").Welcome()
	w.Host = "macbook"
	r.M("macmini").SetWelcome(w)
	s := online(t, r, "macmini", fleettest.Snapshot())
	m := s.Machines[1]
	if m.Name != "macmini" || m.Host != "macbook" {
		t.Fatalf("machine %+v, want the alias macmini with the host macbook as secondary text", m)
	}
	feed := r.OpenHub()
	feed.Next("macmini", hub.Online)
	feed.None("macbook", hub.Online)
}

func TestReconnectReplacesTheStateOfTheMachine(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(
		fleettest.Sess{N: 2, Dir: "/home/florian/dev/a", Label: "a", Agent: proto.StatusWorking},
		fleettest.Sess{N: 3, Dir: "/home/florian/dev/b", Label: "b"},
	))
	feed := r.OpenHub()
	if ev := feed.Next("M", hub.Sessions); len(ev.Items) != 2 {
		t.Fatalf("sessions %+v, want two", ev.Items)
	}
	feed.Next("M", hub.Agents)
	r.M("M").Drop()
	feed.Next("M", hub.Failed)
	r.M("M").Online(fleettest.Snapshot(fleettest.Sess{N: 4, Dir: "/home/florian/dev/c", Label: "c"}))
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	feed.Next("M", hub.Online)
	ev := feed.Next("M", hub.Sessions)
	if len(ev.Items) != 1 || ev.Items[0].SessionID != "s4" {
		t.Fatalf("sessions after the reconnect %+v, want only s4", ev.Items)
	}
	if ev := feed.Next("M", hub.Agents); len(ev.Items) != 0 {
		t.Fatalf("agents after the reconnect %+v, want none", ev.Items)
	}
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "online" })
	if len(s.Counts) != 0 {
		t.Fatalf("counts %v after the reconnect, want none", s.Counts)
	}
}

// DS10, SEC12
func TestOversizedInputFromAMachineEndsThatLinkOnly(t *testing.T) {
	r := start(t, "M", "K")
	r.M("M").Online(fleettest.Snapshot())
	r.M("K").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/k", Label: "k"}))
	feed := r.OpenHub()
	feed.Next("M", hub.Online)
	feed.Next("K", hub.Online)
	id := watchID(t, r, "M", 1)

	rows := make([]proto.CatalogEntry, 5000)
	for i := range rows {
		rows[i] = entry("/home/florian/dev/p/w"+itoa(i), "w"+itoa(i))
	}
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: rows}))
	if ev := feed.Next("M", hub.Items); len(ev.Items) != 2000 {
		t.Fatalf("%d rows, want the list cut at 2000", len(ev.Items))
	}
	note := feed.Next("M", hub.Closed+1)
	if note.Err.Reason != "cut" || note.Err.Text != "2000" {
		t.Fatalf("note %+v, want the list cut at 2000", note.Err)
	}

	big := make([]byte, 0, 5<<20+64)
	big = append(big, `{"t":"frame","pad":"`...)
	for len(big) < 5<<20 {
		big = append(big, 'a')
	}
	big = append(big, `"}`...)
	r.M("M").Push(proto.Msg{T: proto.TypeFrame, Body: big})
	if ev := feed.Next("M", hub.Failed); ev.Err.Reason != string(remote.Protocol) {
		t.Fatalf("an oversized message ended the link with %+v, want protocol", ev.Err)
	}
	r.Eventually("M's link closed", func() bool { return !r.M("M").Live() })
	if !r.M("K").Live() {
		t.Fatal("K's link ended with M's")
	}
	put := proto.Put(proto.Session{ID: "s5", Dir: "/home/florian/dev/k2", Label: "k2"})
	r.M("K").Push(proto.Encode(proto.Delta{Rev: 2, Changes: []proto.Change{put}}))
	for {
		ev := feed.Next("K", hub.Sessions)
		if len(ev.Items) == 2 {
			break
		}
	}
	feed.None("K", hub.Failed)
}

func TestAStateWithMoreThanFiveHundredSessionsIsCut(t *testing.T) {
	r := start(t, "M connect")
	var ss []fleettest.Sess
	for i := range 600 {
		ss = append(ss, fleettest.Sess{N: i + 1, Dir: "/home/florian/dev/s" + itoa(i), Label: "s" + itoa(i)})
	}
	online(t, r, "M", fleettest.Snapshot(ss...))
	feed := r.OpenHub()
	if ev := feed.Next("M", hub.Sessions); len(ev.Items) != 500 {
		t.Fatalf("%d sessions, want 500", len(ev.Items))
	}
	put := proto.Put(proto.Session{ID: "s999", Dir: "/home/florian/dev/new", Label: "new"})
	r.M("M").Push(proto.Encode(proto.Delta{Rev: 2, Changes: []proto.Change{put}}))
	if note := feed.Next("M", hub.Closed+1); note.Err.Reason != "cut" {
		t.Fatalf("note %+v, want cut", note.Err)
	}
	if ev := feed.Next("M", hub.Sessions); len(ev.Items) != 500 {
		t.Fatalf("%d sessions after a delta, want still 500", len(ev.Items))
	}
}

func TestADeltaOutOfOrderEndsTheLinkAndReconnects(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot())
	r.M("M").Push(fleettest.AgentDelta(5, 2, proto.StatusWorking))
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "retrying" })
	if p := fleettest.MachineOf(t, s, "M").Problem; p.Reason != string(remote.Protocol) {
		t.Fatalf("problem %+v, want protocol", p)
	}
}
