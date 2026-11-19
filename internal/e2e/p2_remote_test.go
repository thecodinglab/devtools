package e2e

// The scenarios of 10.2 with two or three machines that P2-11 adds: N14
// (a UI command typed in a remote pane), SW18 (the bridge killed under a
// live client), SW19 (no bridge outlives its client), AG2 (an agent of
// another machine picked in the hub) and, last, N7 (every recorded ssh is
// the bridge). The scene is the one of the suite (newFleet): the desktop's
// window, the macmini held by `connect`, the macbook dialled on demand.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/ui"
)

// sshRecord is one argument list a fake ssh received, with the test and
// the machine it ran for.
type sshRecord struct {
	test, machine string
	args          []string
}

// sshRecords collects the ssh logs of every machine a Term was attached to,
// when its test ends: the machine's directory is removed after that, and
// TestP2EveryRecordedSSHIsTheBridge reads those of the tests before it.
// The audit of N7 does not depend on that test's place in the run: each
// machine's complete log is checked in its own test's cleanup (watchSSH),
// so a later test, a paused parallel one and the last repetition of
// -count are checked as well.
var (
	sshMu      sync.Mutex
	sshRecords []sshRecord
	sshWatched sync.Map // *Machine -> true
)

func init() {
	attachHooks = append(attachHooks, collectSSH)
}

// collectSSH audits the ssh log of every machine a Term is attached to.
// Only a client dials (internal/backend, remote.NewDialer) and a client
// runs only in a Term, so these are the machines whose devtools runs the
// fake ssh. The harness's own tests, which feed the fake by hand (RunBin)
// and attach no Term, are not audited.
func collectSSH(m *Machine, _ TermOptions, _ string) {
	watchSSH(m.state().t, m)
}

// sshAuditor is the part of a test that watchSSH uses; *testing.T has it,
// and TestP2AnSSHOfALaterTestFailsThatTest passes a recorder.
type sshAuditor interface {
	Name() string
	Cleanup(func())
	Errorf(format string, args ...any)
}

// watchSSH registers, once per machine, a cleanup of the machine's test tb
// that waits until the processes of the machine's Terms are gone, reads
// the machine's complete ssh log, fails tb for every argument list that is
// not the bridge of 4.4, and copies the lists into sshRecords. Attach runs
// the hook before it registers the Term's own cleanup, so this cleanup runs
// after the Terms of tb are closed (and after the subtests of tb), and
// before the machine's directory goes.
func watchSSH(tb sshAuditor, m *Machine) {
	if _, loaded := sshWatched.LoadOrStore(m, true); loaded {
		return
	}
	s := m.state()
	path := m.fakeDir("ssh.log")
	test, name := tb.Name(), m.Name
	tb.Cleanup(func() {
		defer sshWatched.Delete(m)
		s.mu.Lock()
		leaders := slices.Clone(s.terms)
		s.mu.Unlock()
		waitGroupsGone(leaders, 10*time.Second)
		lists, err := readLog(path)
		if err != nil {
			tb.Errorf("N7: the ssh log of the %s: %v", name, err)
		}
		for _, args := range lists {
			if problem := notTheBridge(args); problem != "" {
				tb.Errorf("N7: %s on the %s: %s", test, name, problem)
			}
		}
		sshMu.Lock()
		defer sshMu.Unlock()
		if err != nil {
			sshRecords = append(sshRecords, sshRecord{test: test, machine: name, args: []string{"unreadable log: " + err.Error()}})
			return
		}
		for _, args := range lists {
			sshRecords = append(sshRecords, sshRecord{test: test, machine: name, args: args})
		}
	})
}

// waitGroupsGone waits up to d until no process is left in the process
// groups the programs of the Terms lead (Attach starts each in a new
// session). A client's ssh runs in the client's group, so afterwards no
// fake ssh of these clients is still about to write its log line; one that
// is still there after d has had the time to write it.
func waitGroupsGone(leaders []int, d time.Duration) {
	if len(leaders) == 0 {
		return
	}
	groups := map[int]bool{}
	for _, pid := range leaders {
		groups[pid] = true
	}
	deadline := time.Now().Add(d)
	for {
		procs, err := platform.Procs()
		if err != nil {
			return
		}
		left := slices.ContainsFunc(procs, func(p platform.Proc) bool { return groups[p.Pgid] })
		if !left || time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// notTheBridge says what is wrong with one argument list of the fake ssh,
// "" when it is the vector of 4.4 followed by `devtools bridge`: -T
// present, -t absent.
func notTheBridge(args []string) string {
	var problems []string
	if len(args) != len(bridgeArgs("")) || !validAlias(args[14]) || !slices.Equal(args, bridgeArgs(args[14])) {
		problems = append(problems, fmt.Sprintf("ssh %q, want ssh %q", args, bridgeArgs("<alias>")))
	}
	if !slices.Contains(args, "-T") || slices.Contains(args, "-t") || slices.Contains(args, "-tt") {
		problems = append(problems, fmt.Sprintf("ssh %q: -T must be there and -t not", args))
	}
	return strings.Join(problems, "; ")
}

// bridgeArgs is the argument vector of 4.4 after "ssh" for alias, written
// out from the plan, not taken from internal/remote.
func bridgeArgs(alias string) []string {
	return []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ConnectionAttempts=1",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"--", alias, "devtools", "bridge",
	}
}

// bridgeProcs returns the pids of the processes of m's binary that run
// `devtools bridge`.
func bridgeProcs(t *testing.T, m *Machine) []int {
	t.Helper()
	procs, err := platform.Procs()
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, p := range procs {
		if len(p.Args) >= 2 && p.Args[0] == m.Bin(devtools) && p.Args[1] == "bridge" {
			out = append(out, p.Pid)
		}
	}
	return out
}

// hereRow reports whether the hub marks the row of name in the section of
// machine as the one the hub was opened from.
func hereRow(rows []string, machine, name string) bool {
	in := false
	for _, r := range rows[:max(0, len(rows)-1)] {
		if isHeading(r, "") {
			in = isHeading(r, machine)
			continue
		}
		// the row you are in has the here mark, or, while the cursor is on
		// it, the selection bar in the accent: the hub opens on it
		trim := strings.TrimLeft(r, " ")
		mark := strings.HasPrefix(trim, glyph(ui.GlyphHere)) || strings.HasPrefix(trim, glyph(ui.GlyphSelected))
		if in && mark && strings.Contains(trim, name) {
			return true
		}
	}
	return false
}

// N14: in a pane of the macmini that the desktop's window shows over the
// bridge, `devtools attach` and `devtools pick` open the hub of the
// desktop's window with the macmini's session marked as "here"; no client
// starts on the macmini.
func TestP2UICommandInARemotePaneOpensTheHubOfTheDisplayingWindow(t *testing.T) {
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	for _, cmd := range []string{"attach", "pick"} {
		t.Run(cmd, func(t *testing.T) {
			status := cmd + "-status-"
			f.x.Type(fmt.Sprintf("devtools %s; echo %s$?\r", cmd, status))
			waitUntil(t, f.x, 15*time.Second, "the desktop's hub opens with the macmini's beta as here", func(rows []string) bool {
				return hubOpen(rows) && hereRow(rows, "macmini", "beta")
			})
			if n := attachProcs(t, f.b); n != 0 {
				t.Errorf("%d client processes on the macmini", n)
			}
			if n := attachProcs(t, f.a, f.x); n != 1 {
				t.Errorf("%d client processes on the desktop, want its one window", n)
			}
			closeHub(t, f.x)
			waitText(t, f.x, 10*time.Second, status+"0")
			if !shows(f.x.Screen(), "macmini", "beta") {
				t.Errorf("the window left the macmini's beta; screen:\n%s", f.x.Golden().Plain())
			}
		})
	}
	if n := attachProcs(t, f.b); n != 0 {
		t.Errorf("%d client processes on the macmini", n)
	}
	if n := attachProcs(t, f.c); n != 0 {
		t.Errorf("%d client processes on the macbook", n)
	}
}

// SW18: the ssh process of the displayed machine is killed with SIGKILL.
// The view goes stale with the reason and keeps its last picture, typing
// reaches no shell, the hub opens and a local pick works; the client stays
// alive, and prefix+d detaches into a terminal in a sane mode.
func TestP2KillingTheBridgeLeavesAUsableClient(t *testing.T) {
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	pids := f.lb.Pids()
	if len(pids) == 0 {
		t.Fatal("no ssh process of the macmini runs")
	}
	// The macmini stays unreachable after the kill, so the view stays stale
	// while the test looks at it.
	f.lb.Block("unreachable")
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatalf("kill the ssh process %d: %v", pid, err)
		}
	}
	waitText(t, f.x, 20*time.Second, ui.BannerPaused)
	rows := f.x.Screen()
	if !showsFull(rows, "macmini", "beta") {
		t.Errorf("the last picture of beta is gone; screen:\n%s", strings.Join(rows, "\n"))
	}
	if f.x.Exited() {
		t.Fatalf("the client ended with the bridge")
	}
	// Input is not sent while the view is stale: a command typed now must
	// not run on any machine (the machines share this host's file system).
	marker := filepath.Join(f.b.Root(), "f", "typed-while-stale")
	f.x.Type("touch " + marker + "\r")
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a command typed into the stale view ran")
	}
	// the hub opens and a local pick works (SW11)
	pick(t, f.x, "", "alpha")
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a command typed into the stale view ran after the pick")
	}
	if n := attachProcs(t, f.a, f.x); n != 1 {
		t.Errorf("%d client processes on the desktop, want the one window", n)
	}
	f.lb.Block("")
	f.x.Key("prefix+d")
	if code, out := f.x.Exit(); code != 0 {
		t.Errorf("detach: exit %d; output:\n%s", code, out)
	}
	sane(t, f.x)
}

// SW19: the client is killed while it displays the macmini; within 60 s no
// `devtools bridge` is left on the macmini's side, nor on the macbook's,
// whose link the hub had opened.
func TestP2NoBridgeSurvivesItsClient(t *testing.T) {
	f := newFleet(t)
	// the macbook is dialled by opening the hub
	openHub(t, f.x)
	waitUntil(t, f.x, 15*time.Second, "the first row of the macbook", func(rows []string) bool { return hubRow(rows, "gamma") })
	closeHub(t, f.x)
	visit(t, f.x, "macmini", "beta")
	if len(bridgeProcs(t, f.b)) == 0 || len(bridgeProcs(t, f.c)) == 0 {
		t.Fatalf("bridges before the kill: macmini %v, macbook %v; want one each", bridgeProcs(t, f.b), bridgeProcs(t, f.c))
	}
	if err := syscall.Kill(f.x.Pid(), syscall.SIGKILL); err != nil {
		t.Fatalf("kill the client: %v", err)
	}
	start := time.Now()
	eventually(t, 60*time.Second, "no bridge left", func() bool {
		return len(bridgeProcs(t, f.b)) == 0 && len(bridgeProcs(t, f.c)) == 0 &&
			len(f.lb.Pids()) == 0 && len(f.lc.Pids()) == 0
	})
	t.Logf("SW19: the last bridge ended %v after the client was killed", time.Since(start))
	eventually(t, 10*time.Second, "the servers lost the bridges' connections", func() bool {
		return f.b.Welcome().Clients == 0 && f.c.Welcome().Clients == 0
	})
	if code, _ := f.x.Exit(); code != -1 {
		t.Errorf("the client ended with %d, want the signal", code)
	}
}

// AG2: an agent runs on the macmini in a tab the window does not show, in
// the left pane of a split. Picked in the desktop's hub, the window shows
// the macmini's session with that tab current and that pane focused.
func TestP2AgentsLandsOnThePaneOfAnotherMachine(t *testing.T) {
	f := newFleet(t)
	f.b.link(fakeAgentName, self())
	visit(t, f.x, "macmini", "beta")
	// a second tab with the agent, split: the keys go to the new right pane
	f.x.Key("prefix+c")
	eventually(t, 10*time.Second, "a second tab of beta", func() bool {
		ss, _ := sessionOf(snapshot(t, f.b), "beta")
		return len(ss.Tabs) == 2 && currentTab(t, f.b, "beta").ID == ss.Tabs[1]
	})
	f.x.Type(fakeAgentName + "\r")
	waitText(t, f.x, 10*time.Second, "agent working")
	var agentPane, agentTab string
	eventually(t, 15*time.Second, "the macmini sees the working agent", func() bool {
		for _, ag := range snapshot(t, f.b).Agents {
			if ag.Kind == proto.AgentClaude {
				agentPane, agentTab = ag.Pane, ag.Tab
				return true
			}
		}
		return false
	})
	f.x.Key("prefix+%")
	eventually(t, 10*time.Second, "the agent's tab is split, the keys in the right pane", func() bool {
		tb := currentTab(t, f.b, "beta")
		return tb.ID == agentTab && len(tb.Panes) == 2 && tb.Focused != agentPane
	})
	// then a third tab, so the agent's tab is not the current one
	f.x.Key("prefix+c")
	eventually(t, 10*time.Second, "a third tab is current", func() bool {
		ss, _ := sessionOf(snapshot(t, f.b), "beta")
		return len(ss.Tabs) == 3 && currentTab(t, f.b, "beta").ID != agentTab
	})
	pick(t, f.x, "", "alpha")

	f.x.Key("prefix+a")
	waitUntil(t, f.x, 15*time.Second, "the agent of the macmini is selected", func(rows []string) bool {
		for _, r := range rows {
			if strings.HasPrefix(strings.TrimLeft(r, " "), glyph(ui.GlyphSelected)) && strings.Contains(r, "macmini") && strings.Contains(r, label("beta")) {
				return true
			}
		}
		return false
	})
	f.x.Key("enter")
	waitUntil(t, f.x, 15*time.Second, "the window shows the macmini's beta", func(rows []string) bool {
		return !hubOpen(rows) && shows(rows, "macmini", "beta")
	})
	eventually(t, 10*time.Second, "the agent's tab is current and its pane focused", func() bool {
		tb := currentTab(t, f.b, "beta")
		return tb.ID == agentTab && tb.Focused == agentPane
	})
	waitText(t, f.x, 10*time.Second, "agent working")
	if n := attachProcs(t, f.a, f.x); n != 1 {
		t.Errorf("%d client processes on the desktop, want the one window", n)
	}
	if n := attachProcs(t, f.b); n != 0 {
		t.Errorf("%d client processes on the macmini", n)
	}
}

// N7: every argument list a fake ssh received is the vector of 4.4
// followed by `devtools bridge`: -T present, -t absent. This test dials
// itself in every way the fleet does: a held link, a dial on demand, a
// redial after a cut, an attempt that fails, and checks its own lists and
// those of the tests that ended before it. The lists of every other test
// whose machines had a terminal, before or after this one, parallel or
// not, are checked in that test's own cleanup (watchSSH), which fails it.
func TestP2EveryRecordedSSHIsTheBridge(t *testing.T) {
	f := newFleet(t)
	openHub(t, f.x)
	waitUntil(t, f.x, 15*time.Second, "the first row of the macbook", func(rows []string) bool { return hubRow(rows, "gamma") })
	closeHub(t, f.x)
	// a redial of the held link after a cut
	bridges := f.lb.Bridges()
	f.lb.Cut()
	eventually(t, 60*time.Second, "a new bridge to the macmini", func() bool { return f.lb.Bridges() > bridges })
	// a failed attempt on demand
	f.dropCold(t)
	f.lc.Block("auth")
	openHub(t, f.x)
	phrase, _ := ui.MachinePhrase("macbook", "off", ui.Problem{Reason: "auth", Retry: "next-open"}, false)
	waitUntil(t, f.x, 30*time.Second, "the macbook's heading says auth", func(rows []string) bool {
		return strings.Contains(heading(rows, "macbook"), strings.TrimSpace(phrase))
	})
	closeHub(t, f.x)
	f.lc.Block("")

	own := f.a.SSHLog()
	aliases := map[string]int{}
	for _, args := range own {
		if len(args) > 14 {
			aliases[args[14]]++
		}
	}
	if len(own) < 4 || aliases["macmini"] < 2 || aliases["macbook"] < 2 {
		t.Fatalf("the scenario dialled too little to prove anything: %q", own)
	}
	check := func(where string, args []string) {
		t.Helper()
		if problem := notTheBridge(args); problem != "" {
			t.Errorf("%s: %s", where, problem)
		}
	}
	for _, args := range own {
		check(t.Name()+" on the desktop", args)
	}
	sshMu.Lock()
	earlier := slices.Clone(sshRecords)
	sshMu.Unlock()
	for _, r := range earlier {
		check(r.test+" on the "+r.machine, r.args)
	}
	t.Logf("N7: %d argument lists of this test and %d of earlier tests, each the bridge of 4.4", len(own), len(earlier))
}

// sshRecorder stands for the *testing.T of another scenario in
// TestP2AnSSHOfALaterTestFailsThatTest: it keeps the cleanups and the
// errors, and end runs the cleanups as the end of a test does.
type sshRecorder struct {
	name     string
	mu       sync.Mutex
	cleanups []func()
	errors   []string
}

func (r *sshRecorder) Name() string { return r.name }

func (r *sshRecorder) Cleanup(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanups = append(r.cleanups, f)
}

func (r *sshRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *sshRecorder) failures() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errors)
}

func (r *sshRecorder) end() {
	r.mu.Lock()
	fs := slices.Clone(r.cleanups)
	r.cleanups = nil
	r.mu.Unlock()
	for i := len(fs) - 1; i >= 0; i-- {
		fs[i]()
	}
}

// sshScenarios numbers the scenarios TestP2AnSSHOfALaterTestFailsThatTest
// makes, so that each run of -count has its own names.
var sshScenarios atomic.Int32

// N7 for the scenarios that end after TestP2EveryRecordedSSHIsTheBridge or
// run in parallel with it: the audit of a machine's ssh log is part of the
// end of the machine's own test, not of the snapshot that test takes. A
// scenario whose fake ssh received `-t` in place of `-T` fails when it
// ends, with the argument list; one that received only the bridge does
// not. The records of the scenarios are taken out again, so that they do
// not reach the next run of TestP2EveryRecordedSSHIsTheBridge.
func TestP2AnSSHOfALaterTestFailsThatTest(t *testing.T) {
	n := sshScenarios.Add(1)
	scenario := func(name string, args ...[]string) *sshRecorder {
		t.Helper()
		a, b := newMachine(t, "desktop"), newMachine(t, "macmini")
		l := Connect(t, a, b, "macmini", false)
		l.Block("auth") // each attempt ends at once, and no bridge runs
		rec := &sshRecorder{name: fmt.Sprintf("%s/%s-%d", t.Name(), name, n)}
		watchSSH(rec, a)
		for _, v := range args {
			if r := a.RunBin(fakeSSH, RunOptions{}, v...); r.Exit != 255 {
				t.Fatalf("the blocked fake ssh %q: exit %d, stderr %q", v, r.Exit, r.Stderr)
			}
		}
		if got := rec.failures(); len(got) != 0 {
			t.Errorf("%s failed before it ended: %q", name, got)
		}
		rec.end()
		t.Cleanup(func() {
			sshMu.Lock()
			defer sshMu.Unlock()
			sshRecords = slices.DeleteFunc(sshRecords, func(r sshRecord) bool { return r.test == rec.name })
		})
		return rec
	}

	good := scenario("bridge-only", bridgeArgs("macmini"), bridgeArgs("macmini"))
	if got := good.failures(); len(got) != 0 {
		t.Errorf("a scenario whose ssh was only the bridge failed: %q", got)
	}

	tty := slices.Clone(bridgeArgs("macmini"))
	tty[0] = "-t"
	bad := scenario("with-a-tty", bridgeArgs("macmini"), tty)
	got := bad.failures()
	if len(got) != 1 {
		t.Fatalf("a scenario whose ssh got -t: %d failures %q, want one", len(got), got)
	}
	if !strings.Contains(got[0], fmt.Sprintf("%q", tty)) || !strings.Contains(got[0], "-T must be there and -t not") {
		t.Errorf("the failure %q does not name the argument list %q and the rule", got[0], tty)
	}
}
