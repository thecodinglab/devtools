package fleet_test

import (
	"slices"
	"testing"
	"time"

	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/remote"
)

// Logging in (3.5.2): a remote dial may start a new ssh login, and so a
// prompt of the ssh agent, only on a grant of the user's presence: the
// start of the client, the hub opening, a switch there, the terminal
// gaining focus. Every other dial runs over the shared connection or
// fails as login.

// logins waits until machine name was dialled len(want) times and checks
// the login flag of each dial.
func logins(t *testing.T, r *fleettest.Rig, name string, want ...bool) {
	t.Helper()
	waitDials(t, r, name, len(want))
	if got := r.M(name).DialLogins(); !slices.Equal(got, want) {
		t.Fatalf("the dials of %q could log in %v, want %v", name, got, want)
	}
}

// needsLogin is a permanent machine M that was online, lost its link and
// whose retry found no shared connection: it waits for the user, and its
// next retry is due in d. It does not wait for a summary, which would move
// the clock.
func needsLogin(t *testing.T, r *fleettest.Rig, d time.Duration) {
	t.Helper()
	online(t, r, "M", fleettest.Snapshot())
	r.M("M").Drop()
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	logins(t, r, "M", true, false)
	r.M("M").Fail(remote.NeedsLogin)
	r.Clock.WaitTimer(t, d)
}

func TestTheFirstDialsLogIn(t *testing.T) {
	r := start(t, "M connect", "K")
	logins(t, r, "M", true)
	logins(t, r, "", true)
}

func TestARetryAfterALostLinkDoesNotLogIn(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot())
	for i := range 3 {
		r.M("M").Drop()
		r.Clock.WaitTimer(t, 500*time.Millisecond)
		r.Advance(500 * time.Millisecond)
		waitDials(t, r, "M", i+2)
		online(t, r, "M", fleettest.Snapshot())
		r.Advance(time.Minute)
	}
	logins(t, r, "M", true, false, false, false)
}

func TestNeedsLoginWaitsForTheUserAndIsRetriedWithoutLoggingIn(t *testing.T) {
	r := start(t, "M connect")
	feed := r.OpenHub()
	needsLogin(t, r, time.Second)
	for {
		ev := feed.Next("M", hub.Failed)
		if ev.Err.Reason != string(remote.NeedsLogin) {
			continue
		}
		if ev.Err.Retry != "login" {
			t.Fatalf("heading %+v, want the retry login", ev.Err)
		}
		break
	}
	r.F.HubClosed()
	// the backoff, at most focusedRetry while focused
	want := []bool{true, false}
	for _, d := range []time.Duration{time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second} {
		r.Clock.WaitTimer(t, d)
		r.Advance(d - time.Millisecond)
		logins(t, r, "M", want...)
		r.Advance(time.Millisecond)
		want = append(want, false)
		logins(t, r, "M", want...)
		r.M("M").Fail(remote.NeedsLogin)
	}
	// unfocused the backoff goes on
	r.Clock.WaitTimer(t, 2*time.Second)
	r.F.Focus(false)
	r.Advance(2 * time.Second)
	want = append(want, false)
	logins(t, r, "M", want...)
	r.M("M").Fail(remote.NeedsLogin)
	r.Clock.WaitTimer(t, 32*time.Second)
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "attention" })
	if p := fleettest.MachineOf(t, s, "M").Problem; p.Reason != string(remote.NeedsLogin) || p.Retry != "login" {
		t.Fatalf("problem %+v, want login, retried as login", p)
	}
}

func TestOpeningTheHubGrantsOneLogin(t *testing.T) {
	r := start(t, "M connect")
	needsLogin(t, r, time.Second)
	feed := r.OpenHub()
	// at once, though M failed just now
	logins(t, r, "M", true, false, true)
	feed.Next("M", hub.Connecting)
	r.M("M").Fail(remote.Unreachable)
	r.Clock.WaitTimer(t, 2*time.Second)
	r.Advance(2 * time.Second)
	logins(t, r, "M", true, false, true, false)
}

func TestOpeningTheHubOnAnOnDemandMachineLogsIn(t *testing.T) {
	r := start(t, "K")
	r.M("K").Fail(remote.NeedsLogin)
	feed := r.OpenHub()
	if ev := feed.Next("K", hub.Failed); ev.Err.Retry != "next-open" {
		t.Fatalf("heading %+v, want the retry next-open", ev.Err)
	}
	logins(t, r, "K", true)
	r.F.HubClosed()
	r.OpenHub()
	logins(t, r, "K", true, true)
}

func TestASwitchGrantsOneLogin(t *testing.T) {
	r := startShowing(t, "M connect")
	needsLogin(t, r, time.Second)
	r.F.Switch(target("M", 2))
	logins(t, r, "M", true, false, true)
	r.M("M").Fail(remote.Unreachable)
	// the switch ends with its notice after the retry is scheduled; the
	// keys it held also wait 2 s
	r.Next(fleet.Notice)
	r.Clock.WaitTimer(t, 2*time.Second)
	r.Advance(2 * time.Second)
	logins(t, r, "M", true, false, true, false)
}

func TestASwitchReplacesADialThatCannotLogIn(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot())
	r.M("M").Drop()
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	// the retry runs over the shared connection, and waits
	logins(t, r, "M", true, false)
	r.F.Switch(target("M", 2))
	logins(t, r, "M", true, false, true)
}

func TestGainingFocusGrantsOneLogin(t *testing.T) {
	r := start(t, "M connect")
	r.F.Focus(false)
	needsLogin(t, r, time.Second)
	r.F.Focus(true)
	r.Advance(time.Second)
	logins(t, r, "M", true, false, true)
	r.M("M").Fail(remote.Unreachable)
	r.Clock.WaitTimer(t, 2*time.Second)
	r.Advance(2 * time.Second)
	logins(t, r, "M", true, false, true, false)
}

func TestAfterAFailedLoginNothingLogsInByItself(t *testing.T) {
	r := start(t, "M connect")
	logins(t, r, "M", true)
	r.M("M").Fail(remote.Auth)
	r.Clock.WaitTimer(t, 30*time.Second)
	// focus grants nothing now
	r.F.Focus(false)
	r.F.Focus(true)
	r.Advance(30 * time.Second)
	want := []bool{true, false}
	logins(t, r, "M", want...)
	r.M("M").Fail(remote.NeedsLogin)
	for _, d := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second} {
		r.Clock.WaitTimer(t, d)
		r.F.Focus(false)
		r.F.Focus(true)
		r.Advance(d)
		want = append(want, false)
		logins(t, r, "M", want...)
		r.M("M").Fail(remote.NeedsLogin)
	}
	r.Clock.WaitTimer(t, 2*time.Second)
	n := r.M("M").Dials()

	// the hub may: its login fails as auth too, and focus still grants
	// nothing; a switch may then, and after it focus grants again
	feed := r.OpenHub()
	r.Eventually("the hub's dial", func() bool { return r.M("M").Dials() == n+1 })
	if l := r.M("M").DialLogins(); !l[n] {
		t.Fatalf("the hub's dial could not log in: %v", l)
	}
	r.M("M").Fail(remote.Auth)
	// the catalog of this machine waits 30 s too: the heading says when
	// the retry is scheduled
	if ev := feed.Next("M", hub.Failed); ev.Err.Reason != string(remote.Auth) {
		t.Fatalf("heading %+v, want auth", ev.Err)
	}
	r.Clock.WaitTimer(t, 30*time.Second)
	r.F.Focus(false)
	r.F.Focus(true)
	r.Advance(30 * time.Second)
	r.Eventually("the retry after the hub's dial", func() bool { return r.M("M").Dials() == n+2 })
	if l := r.M("M").DialLogins(); l[n+1] {
		t.Fatalf("a dial after the second failed login could log in: %v", l)
	}
	r.M("M").Fail(remote.NeedsLogin)
	r.Clock.WaitTimer(t, 2*time.Second)
	r.F.Switch(target("M", 2))
	r.Eventually("the switch's dial", func() bool { return r.M("M").Dials() == n+3 })
	r.M("M").Fail(remote.Unreachable)
	r.Next(fleet.Notice)
	r.Clock.WaitTimer(t, 2*time.Second)
	r.F.Focus(false)
	r.F.Focus(true)
	r.Advance(2 * time.Second)
	r.Eventually("the retry after the switch", func() bool { return r.M("M").Dials() == n+4 })
	time.Sleep(fleettest.Quiet)
	if l := r.M("M").DialLogins(); len(l) != n+4 || !l[n+2] || !l[n+3] {
		t.Fatalf("the switch's dial and the one after focus could log in %v, want both", l)
	}
}

func TestAGrantIsClearedWhenTheMachineComesOnline(t *testing.T) {
	r := start(t, "M connect")
	online(t, r, "M", fleettest.Snapshot())
	r.M("M").Drop()
	answer := r.M("M").Later()
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	logins(t, r, "M", true, false)
	// focus comes while the retry runs: the grant waits for the next dial
	r.F.Focus(false)
	r.F.Focus(true)
	answer(fleettest.Snapshot())
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "M") == "online" })
	r.M("M").Drop()
	r.Clock.WaitTimer(t, time.Second)
	r.Advance(time.Second)
	logins(t, r, "M", true, false, false)
}

func TestTheLocalMachineIsNotAskedToLogIn(t *testing.T) {
	r := fleettest.New(t)
	r.Eventually("the first local dial", func() bool { return r.M("").Dials() == 1 })
	r.M("").Fail(remote.NoServer)
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	waitDials(t, r, "", 2)
	r.M("").Online(fleettest.Snapshot(here))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "") == "online" })
	r.M("").Drop()
	r.Clock.WaitTimer(t, time.Second)
	r.Advance(time.Second)
	// the local socket ignores the flag; nothing withholds it
	logins(t, r, "", true, true, true)
	s, _ := r.Latest()
	if p := fleettest.MachineOf(t, s, "").Problem; p.Retry == "login" {
		t.Fatalf("the local machine waits for a login: %+v", p)
	}
}

func TestOpeningTheHubRightAfterAFailedLoginTriesAgain(t *testing.T) {
	r := start(t, "M connect")
	logins(t, r, "M", true)
	r.M("M").Fail(remote.Auth)
	r.Clock.WaitTimer(t, 30*time.Second)
	// what the words of auth ask for; a reason a login cannot fix waits
	// out its 30 s (TestAnIncompatibleMachineIsShownAndNotRetriedInALoop)
	feed := r.OpenHub()
	feed.Next("M", hub.Connecting)
	logins(t, r, "M", true, true)
}
