package e2e

// The shared ssh connection of 3.5.2 seen from the window: a link lost to
// silence comes back over it without a login, two windows log in once, a
// window without one waits for the user instead of logging in, and a
// login that failed is not tried again by itself. Every login of the fake
// ssh stands in for a Touch ID prompt of the user's agent.

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/remote"
	"devtools/internal/ui"
)

// sshTo returns the argument lists of the fake ssh of a for alias, in
// order.
func sshTo(a *Machine, alias string) [][]string {
	var out [][]string
	for _, args := range a.SSHLog() {
		if dest, _, ok := parseSSH(args); ok && dest == alias {
			out = append(out, args)
		}
	}
	return out
}

// isCheck reports whether args is the "-O check" of a shared connection.
func isCheck(args []string) bool {
	op, ok := sshFlag(args, 'O')
	return ok && op == "check"
}

// mayLogIn reports whether args is a bridge that may start a new ssh
// login: it shares nothing or may become the master (ControlMaster=auto).
// A bridge with ControlMaster=no and the check may not.
func mayLogIn(args []string) bool {
	if isCheck(args) {
		return false
	}
	master, _ := sshOption(args, "ControlMaster")
	return master != "no"
}

// countSSH counts the lists of lists that ok accepts.
func countSSH(lists [][]string, ok func([]string) bool) int {
	n := 0
	for _, args := range lists {
		if ok(args) {
			n++
		}
	}
	return n
}

// loginBanner is the banner of a window that shows a session of machine
// while the machine needs a new ssh login (3.5.2).
func loginBanner(machine string) string {
	phrase, _ := ui.MachinePhrase(machine, "attention", ui.Problem{Reason: string(remote.NeedsLogin), Retry: "login"}, false)
	return machine + "  " + phrase
}

// live reports whether the window shows the session of name on machine
// with no banner over it.
func live(rows []string, machine, name string) bool {
	return !hubOpen(rows) && shows(rows, machine, name) && !strings.Contains(strings.Join(rows, "\n"), ui.BannerPaused)
}

// stopBridges stops the processes of l with SIGSTOP, as a network that went
// silent: the bridge answers nothing and its pipes stay open. They are
// continued when the test ends.
func stopBridges(t *testing.T, l *Link) {
	t.Helper()
	pids := l.Pids()
	if len(pids) == 0 {
		t.Fatalf("no fake ssh of the %s runs", l.Alias())
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGSTOP)
	}
	t.Cleanup(func() {
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGCONT)
		}
	})
}

// A link that goes silent, as on a train whose network dropped, is ended
// by the client's watchdog (10 s, 4.4) and redialled over the shared
// connection, which outlives the outage: the redial waits out the outage
// inside its attempt and logs in nowhere.
func TestASilentLinkComesBackOverTheSharedConnectionWithoutALogin(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	if n := f.lb.Logins(); n != 1 || !f.lb.Shared() {
		t.Fatalf("the first link to the macmini: %d logins, shared %v; want one login and the shared connection up", n, f.lb.Shared())
	}
	bridges := f.lb.Bridges()
	// the network is gone for a while: the bridge is silent, and every
	// attempt waits before it gets through
	f.lb.Delay(4 * time.Second)
	stopBridges(t, f.lb)
	waitText(t, f.x, 30*time.Second, ui.BannerPaused)
	waitUntil(t, f.x, 40*time.Second, "the live view of beta", func(rows []string) bool { return live(rows, "macmini", "beta") })
	f.lb.Delay(0)
	if f.lb.Bridges() <= bridges {
		t.Errorf("the live view is back without a new bridge to the macmini")
	}
	if n := f.lb.Logins(); n != 1 {
		t.Errorf("%d logins to the macmini, want still 1", n)
	}
	lists := sshTo(f.a, "macmini")
	if n := countSSH(lists, mayLogIn); n != 1 {
		t.Errorf("%d ssh to the macmini could log in, want only the first; ssh %q", n, lists)
	}
	if countSSH(lists, isCheck) == 0 {
		t.Errorf("no redial asked the shared connection first; ssh %q", lists)
	}
}

// Two windows of this machine share one connection to the macmini: one
// login for both. When the shared connection is gone, opening the hub in
// one window logs in once, and the other window comes back over that
// connection by itself, without a login and without its hub.
func TestTwoWindowsLogInOnce(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	y := openSession(t, f.a, plain(f.a, 120, 24), "alpha")
	eventually(t, 15*time.Second, "both windows reach the macmini", func() bool { return f.b.Welcome().Clients == 2 })
	if n := f.lb.Logins(); n != 1 {
		t.Fatalf("two windows: %d logins to the macmini, want 1", n)
	}
	visit(t, y, "macmini", "beta")

	// both links are lost while the connection stays: no login
	f.lb.Cut()
	eventually(t, 30*time.Second, "both windows reach the macmini again", func() bool { return f.b.Welcome().Clients == 2 })
	if n := f.lb.Logins(); n != 1 {
		t.Errorf("after a cut: %d logins to the macmini, want still 1", n)
	}

	// the shared connection is gone: both windows wait for the user
	f.lb.DropShared()
	for _, x := range []*Term{f.x, y} {
		waitText(t, x, 30*time.Second, loginBanner("macmini"))
	}
	if n := f.lb.Logins(); n != 1 {
		t.Fatalf("without the shared connection: %d logins to the macmini, want still 1", n)
	}
	openHub(t, f.x)
	eventually(t, 30*time.Second, "the hub's login brings the shared connection back", func() bool { return f.lb.Shared() })
	closeHub(t, f.x)
	for _, x := range []*Term{f.x, y} {
		waitUntil(t, x, 30*time.Second, "the live view of beta", func(rows []string) bool { return live(rows, "macmini", "beta") })
	}
	eventually(t, 15*time.Second, "both windows reach the macmini again", func() bool { return f.b.Welcome().Clients == 2 })
	if n := f.lb.Logins(); n != 2 {
		t.Errorf("after the hub's login: %d logins to the macmini, want 2", n)
	}
}

// Without the shared connection the window's redials do not log in: the
// macmini needs a new ssh login and says so, and the redials only ask
// whether a shared connection is back. Opening the hub, the user being
// there, logs in exactly once and the macmini comes back.
func TestBackgroundRedialsNeverLogIn(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	before := len(sshTo(f.a, "macmini"))
	f.lb.DropShared()
	waitText(t, f.x, 30*time.Second, loginBanner("macmini"))
	// several redials while the terminal has focus (at most 2 s apart)
	eventually(t, 30*time.Second, "three redials of the macmini", func() bool {
		return countSSH(sshTo(f.a, "macmini")[before:], isCheck) >= 3
	})
	// and it still says so
	waitText(t, f.x, 10*time.Second, loginBanner("macmini"))
	after := sshTo(f.a, "macmini")[before:]
	if n := countSSH(after, mayLogIn); n != 0 {
		t.Errorf("%d redials of the macmini could log in, want none; ssh %q", n, after)
	}
	if n := f.lb.Logins(); n != 1 {
		t.Fatalf("%d logins to the macmini without the user, want still 1", n)
	}

	openHub(t, f.x)
	eventually(t, 30*time.Second, "the hub's login brings the shared connection back", func() bool { return f.lb.Shared() })
	closeHub(t, f.x)
	waitUntil(t, f.x, 30*time.Second, "the live view of beta", func(rows []string) bool { return live(rows, "macmini", "beta") })
	time.Sleep(3 * time.Second)
	if n := f.lb.Logins(); n != 2 {
		t.Errorf("%d logins to the macmini after one hub open, want 2", n)
	}
	if !f.lb.Shared() {
		t.Errorf("the hub's login did not bring the shared connection back")
	}
}

// A login that failed as auth, a dismissed or locked agent, is not tried
// again by itself: not by the retry 30 s later, and not when the terminal
// gains focus. Opening the hub tries again.
func TestAFailedLoginIsNotRetriedByItself(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	f.lb.Block(string(remote.Auth))
	f.lb.DropShared()
	waitText(t, f.x, 30*time.Second, loginBanner("macmini"))

	// the user opens the hub and dismisses the prompt
	phrase, _ := ui.MachinePhrase("macmini", "attention", ui.Problem{Reason: string(remote.Auth), Retry: "in-30s"}, false)
	authHeading := glyph(ui.GlyphFailed) + " " + phrase
	openHub(t, f.x)
	waitUntil(t, f.x, 30*time.Second, "the macmini's heading says the login failed", func(rows []string) bool {
		return strings.Contains(heading(rows, "macmini"), authHeading)
	})
	closeHub(t, f.x)
	if n := f.lb.Logins(); n != 2 {
		t.Fatalf("%d logins to the macmini after the hub's try, want 2", n)
	}
	before := len(sshTo(f.a, "macmini"))

	// the terminal loses and gains focus; the gain is heard (the client
	// asks for the colours again) but lets nothing log in
	focus := func() {
		t.Helper()
		asked := strings.Count(rawOutput(f.x), "]11;?")
		f.x.Type("\x1b[O")
		f.x.Type("\x1b[I")
		eventually(t, 10*time.Second, "the client hears the focus gain", func() bool {
			return strings.Count(rawOutput(f.x), "]11;?") > asked
		})
	}
	focus()
	// the retry of attentionGap (30 s) comes and does not log in
	eventually(t, 45*time.Second, "the retry of the macmini", func() bool {
		return countSSH(sshTo(f.a, "macmini")[before:], isCheck) > 0
	})
	f.lb.Block("")
	focus()
	time.Sleep(5 * time.Second)
	after := sshTo(f.a, "macmini")[before:]
	if n := countSSH(after, mayLogIn); n != 0 {
		t.Errorf("%d redials of the macmini could log in after the failed login, want none; ssh %q", n, after)
	}
	if n := f.lb.Logins(); n != 2 {
		t.Fatalf("%d logins to the macmini after the failed login, want still 2", n)
	}
	if rows := f.x.Screen(); live(rows, "macmini", "beta") {
		t.Fatalf("the macmini came back without a login; screen:\n%s", strings.Join(rows, "\n"))
	}

	// the hub tries again, and this time the login succeeds
	openHub(t, f.x)
	eventually(t, 30*time.Second, "the hub's login brings the shared connection back", func() bool { return f.lb.Shared() })
	closeHub(t, f.x)
	waitUntil(t, f.x, 30*time.Second, "the live view of beta", func(rows []string) bool { return live(rows, "macmini", "beta") })
	if n := f.lb.Logins(); n != 3 {
		t.Errorf("%d logins to the macmini after the second hub open, want 3", n)
	}
}
