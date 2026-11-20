package e2e

// The scenarios of two or three machines (R in the table of P3-05b):
// discovery, switching, a cut link, a restarted remote server, the failure
// reasons, an old remote build, commands typed in a remote pane, and an
// agent's bell from another machine.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/ui"
)

// fleetOf is the usual scene of the cross-machine scenarios: the desktop
// with the project alpha, whose window shows alpha in a terminal of 120
// columns (wide enough that no heading is cut); the macmini, a `connect`
// machine, with beta; the macbook, a machine dialled on demand, with gamma.
type fleetOf struct {
	a, b, c *Machine
	lb, lc  *Link
	x       *Term
}

// newFleet makes the scene; extra names more projects of the macmini.
func newFleet(t *testing.T, extra ...string) *fleetOf {
	t.Helper()
	f := &fleetOf{a: StartMachine(t, "desktop"), b: StartMachine(t, "macmini"), c: StartMachine(t, "macbook")}
	project(t, f.a, "alpha")
	for _, name := range append([]string{"beta"}, extra...) {
		project(t, f.b, name)
	}
	project(t, f.c, "gamma")
	f.lb = Connect(t, f.a, f.b, "macmini", true)
	f.lc = Connect(t, f.a, f.c, "macbook", false)
	f.x = openSession(t, f.a, plain(f.a, 120, 24), "alpha")
	f.waitHeld(t)
	return f
}

// linked reports whether the bridge of l reaches the server of m: m counts
// an interactive connection while a bridge holds one (4.4).
func linked(m *Machine) bool { return m.Welcome().Clients > 0 }

// waitHeld waits until the macmini's link is up.
func (f *fleetOf) waitHeld(t *testing.T) {
	t.Helper()
	eventually(t, 15*time.Second, "the link to the macmini is up", func() bool { return f.lb.Bridges() > 0 && linked(f.b) })
}

// dropCold cuts the link to the macbook and waits until the window has
// let it go: its bridge is gone and the status row no longer lists it. The
// next hub open dials it again, cold.
func (f *fleetOf) dropCold(t *testing.T) {
	t.Helper()
	f.lc.Cut()
	eventually(t, 15*time.Second, "the macbook is let go", func() bool {
		return len(f.lc.Pids()) == 0 && !linked(f.c) && !strings.Contains(statusRow(f.x.Screen()), "macbook")
	})
}

// remoteRow reports whether the section of machine has a row: the row
// under its heading is neither empty nor another heading.
func remoteRow(rows []string, machine string) bool {
	for i := 0; i+1 < len(rows)-1; i++ {
		if !isHeading(rows[i], machine) {
			continue
		}
		next := strings.TrimSpace(rows[i+1])
		return next != "" && !isHeading(rows[i+1], "") && !strings.HasPrefix(next, glyph(ui.GlyphDividerH))
	}
	return false
}

// realScene is the scene of the real mode (DEVTOOLS_E2E_REAL=1, 10.1.8):
// the real local server with one scratch session in a temporary
// directory, shown in a terminal of 120 columns, the link to
// DEVTOOLS_E2E_REMOTE, and one scratch session in a temporary directory
// there (far, P3-05b). Both are closed when the test ends, the remote
// directory is removed, and nothing else is opened on either machine.
// The session labelled DEVTOOLS_E2E_QUERY, which the run supplies, is only
// switched to, never typed into.
func realScene(t *testing.T) (a *Machine, x *Term, far scratch) {
	t.Helper()
	cfg, _ := Real()
	a = StartMachine(t, "local")
	Connect(t, a, nil, cfg.Remote, true)
	dir := filepath.Join(t.TempDir(), "e2e-scratch")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { oneshot(a.Socket(), proto.SessionClose{Dir: dir}, 10*time.Second) })
	farDir, removeFar := realScratchDir(t, a, cfg.Remote)
	far = openScratch(t, a, cfg.Remote, farDir, removeFar)
	x = Attach(t, a, plain(a, 120, 24), "switch", dir)
	waitUntil(t, x, 15*time.Second, "the scratch session", func(rows []string) bool {
		return strings.Contains(statusRow(rows), "e2e-scratch")
	})
	return a, x, far
}

// scratchLabel is the label of the scratch session on the other machine.
// No query of these tests finds it but its own: it shares no subsequence
// with "e2e-scratch", the label of the local one, that a query of the
// local one could match.
const scratchLabel = "e2e-far"

// scratch is a session a test opened on another machine, in a directory
// of its own, and closes when it ends.
type scratch struct {
	machine string // the alias of the machine
	dir     string // its directory there
	session string // its id there
}

// farRequest sends req to the server of machine through a one-shot
// connection over "ssh -T <machine> devtools bridge", the way the fleet
// reaches it (4.4), with the ssh of a's PATH: the fake one, or in the
// real mode the real one. The ssh process gets
// the environment of the test process, which in the real mode is the
// environment the run was started with, without the marks of a context.
func farRequest(a *Machine, machine string, req proto.Typed) (proto.Reply, error) {
	ssh, err := lookPath(a.Env(), "ssh")
	if err != nil {
		return proto.Reply{}, err
	}
	d := remote.NewDialer(remote.DialOptions{SSH: ssh, Hello: proto.Hello{Gen: proto.Gen, Version: "e2e", Kind: proto.KindOneshot}})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, fail := d.Dial(ctx, machine)
	if fail != nil {
		return proto.Reply{}, fmt.Errorf("the bridge to %s: %s: %s", machine, fail.Reason, fail.Error())
	}
	defer l.Close()
	if err := l.Send(proto.Request(1, req)); err != nil {
		return proto.Reply{}, fmt.Errorf("the bridge to %s: %w", machine, err)
	}
	for {
		select {
		case m, ok := <-l.Recv():
			if !ok {
				return proto.Reply{}, fmt.Errorf("the bridge to %s ended before the reply to %s", machine, req.Type())
			}
			if m.ID != 1 {
				continue
			}
			if r, ok := proto.AsReply(m); ok {
				return r, nil
			}
		case <-ctx.Done():
			return proto.Reply{}, fmt.Errorf("the bridge to %s: no reply to %s within 30 s", machine, req.Type())
		}
	}
}

// openScratch opens the scratch session of the test on machine, in dir,
// which the test made there and nothing else uses; remove deletes dir. It
// registers the close of the session and the removal before it returns or
// fails, so both happen however the test ends. A session that already
// existed for dir is not the test's: the test fails and leaves it open.
func openScratch(t *testing.T, a *Machine, machine string, dir string, remove func()) scratch {
	t.Helper()
	t.Cleanup(remove)
	r, err := farRequest(a, machine, proto.SessionOpen{Dir: dir, Name: scratchLabel})
	if err != nil {
		t.Fatalf("session.open on %s: %v", machine, err)
	}
	if !r.OK {
		t.Fatalf("session.open on %s: %s %s", machine, r.Code, r.Text)
	}
	var res proto.SessionOpenResult
	if err := r.Msg.Decode(&res); err != nil {
		t.Fatal(err)
	}
	if !res.Created {
		t.Fatalf("session.open on %s found the session %s of %s: it is not the test's, and it stays", machine, res.Session, dir)
	}
	s := scratch{machine: machine, dir: dir, session: res.Session}
	t.Cleanup(func() {
		r, err := farRequest(a, machine, proto.SessionClose{Session: s.session})
		var res proto.SessionCloseResult
		switch {
		case err != nil:
			t.Errorf("session.close of the scratch session %s on %s: %v", s.session, machine, err)
		case !r.OK:
			t.Errorf("session.close of the scratch session %s on %s: %s %s", s.session, machine, r.Code, r.Text)
		case r.Msg.Decode(&res) != nil || !res.Closed:
			t.Errorf("session.close: the scratch session %s on %s was not closed", s.session, machine)
		}
	})
	return s
}

// tempPath is what mktemp -d prints, and the only kind of path the real
// mode hands to the remote shell.
var tempPath = regexp.MustCompile(`^/[A-Za-z0-9._/+-]+$`)

// realScratchDir makes a temporary directory on machine with mktemp -d
// through the real ssh, and returns it with the function that removes it
// again (rmdir: only the empty directory, never anything in it). It runs
// only in the real mode, which the user starts.
func realScratchDir(t *testing.T, a *Machine, machine string) (string, func()) {
	t.Helper()
	ssh, err := lookPath(a.Env(), "ssh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(ssh, append([]string{"-T", "-o", "BatchMode=yes", "--", machine}, args...)...)
		cmd.Env = a.Env()
		var out, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		if err != nil {
			err = fmt.Errorf("ssh %s %s: %v: %s", machine, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(out.String()), err
	}
	dir, err := run("mktemp", "-d")
	if err != nil {
		t.Fatal(err)
	}
	if !tempPath.MatchString(dir) || strings.Contains(dir, "..") {
		t.Fatalf("mktemp -d on %s printed %q, not a temporary directory", machine, dir)
	}
	return dir, func() {
		if _, err := run("rmdir", "--", dir); err != nil {
			t.Errorf("the scratch directory %s on %s stays: %v", dir, machine, err)
		}
	}
}

// fakeScratchDir is realScratchDir for a machine of the harness: a
// directory under its home.
func fakeScratchDir(t *testing.T, m *Machine) (string, func()) {
	t.Helper()
	dir, err := os.MkdirTemp(m.Home, "e2e-far-")
	if err != nil {
		t.Fatal(err)
	}
	return dir, func() {
		if err := os.Remove(dir); err != nil {
			t.Errorf("the scratch directory %s stays: %v", dir, err)
		}
	}
}

// pickScratch switches the window to the scratch session s through the
// hub. It returns once the status row names s on its machine and the hub
// is closed: from then on what is typed reaches s and nothing else.
func pickScratch(t *testing.T, x *Term, s scratch) {
	t.Helper()
	choose(t, x, scratchLabel, scratchLabel)
	x.Key("enter")
	waitUntil(t, x, 15*time.Second, "the scratch session of "+s.machine, func(rows []string) bool {
		return !hubOpen(rows) && strings.HasPrefix(statusRow(rows), chipped(s.machine, scratchLabel))
	})
}

// othersOf is the state of every session of s but the one of skip, as far
// as a test can change it: tabs, current tab, splits, focus, and per pane
// its state, directory and program.
func othersOf(s proto.Snapshot, skip string) string {
	var b strings.Builder
	for _, ss := range s.Sessions {
		if ss.ID == skip {
			continue
		}
		fmt.Fprintf(&b, "%s %q in %s, current %s:\n", ss.ID, ss.Label, ss.Dir, ss.Current)
		for _, tb := range s.Tabs {
			if tb.Session == ss.ID {
				fmt.Fprintf(&b, "  tab %s %d %s %v focused %s\n", tb.ID, tb.Index, tb.Split, tb.Panes, tb.Focused)
			}
		}
		for _, p := range s.Panes {
			if p.Session == ss.ID {
				fmt.Fprintf(&b, "  pane %s %s %s %s\n", p.ID, p.State, p.Cwd, p.Program)
			}
		}
	}
	return b.String()
}

// pickReal switches the window of the real mode to the session of the
// query on the remote machine and returns the time from enter to a frame
// that names it.
func pickReal(t *testing.T, x *Term, cfg RealConfig) time.Duration {
	t.Helper()
	if !hubOpen(x.Screen()) {
		openHub(t, x)
	}
	x.Type(cfg.Query)
	waitUntil(t, x, 15*time.Second, "a row of "+cfg.Remote+" for "+cfg.Query, func(rows []string) bool { return remoteRow(rows, cfg.Remote) })
	start := time.Now()
	x.Key("enter")
	waitUntil(t, x, 15*time.Second, "the session of "+cfg.Remote, func(rows []string) bool {
		return !hubOpen(rows) && strings.HasPrefix(statusRow(rows), " "+cfg.Remote+"  ")
	})
	return time.Since(start)
}

func TestRemoteDiscoveryHeldAndCold(t *testing.T) {
	if cfg, ok := Real(); ok {
		_, x, _ := realScene(t)
		open := func() time.Duration {
			start := time.Now()
			x.Key("prefix+o")
			waitUntil(t, x, 30*time.Second, "a row of "+cfg.Remote, func(rows []string) bool { return remoteRow(rows, cfg.Remote) })
			took := time.Since(start)
			closeHub(t, x)
			return took
		}
		// The first open after the client started may have to dial: it is
		// held against the cold bound, the others against the held one.
		measureAgainst(t, "T2", lanBound("T2", "cold"), 0, open())
		var samples []time.Duration
		for range SamplesP95 {
			samples = append(samples, open())
		}
		measureAgainst(t, "T2", lanBound("T2", "held"), 0, percentile(samples, 95))
		return
	}
	f := newFleet(t)
	t.Run("held", func(t *testing.T) {
		measureP95(t, "T2", "held", SamplesP95, func(int) time.Duration {
			start := time.Now()
			f.x.Key("prefix+o")
			waitUntil(t, f.x, 5*time.Second, "the first row of the macmini", func(rows []string) bool { return hubRow(rows, "beta") })
			took := time.Since(start)
			closeHub(t, f.x)
			return took
		})
	})
	t.Run("cold", func(t *testing.T) {
		measureP95(t, "T2", "cold", SamplesP95, func(int) time.Duration {
			f.dropCold(t)
			start := time.Now()
			f.x.Key("prefix+o")
			waitUntil(t, f.x, 15*time.Second, "the first row of the macbook", func(rows []string) bool { return hubRow(rows, "gamma") })
			took := time.Since(start)
			closeHub(t, f.x)
			return took
		})
	})
	// One window throughout, and no client on the other machines.
	if n := attachProcs(t, f.a, f.x); n != 1 {
		t.Errorf("%d client processes on the desktop, want 1", n)
	}
	for _, m := range []*Machine{f.b, f.c} {
		if n := attachProcs(t, m); n != 0 {
			t.Errorf("%d client processes on the %s", n, m.Name)
		}
	}
}

func TestHubUsableWithDeadMachine(t *testing.T) {
	t.Parallel()
	a, c := StartMachine(t, "desktop"), StartMachine(t, "macbook")
	project(t, a, "alpha")
	project(t, a, "omega")
	project(t, c, "gamma")
	lc := Connect(t, a, c, "macbook", false)
	x := openSession(t, a, plain(a, 120, 24), "alpha")
	// Asleep: ssh waits out its ConnectTimeout (10 s in the arguments of
	// 3.5.2) and then fails as unreachable.
	lc.Delay(10 * time.Second)
	lc.Block("unreachable")
	want, _ := ui.MachinePhrase("macbook", "off", ui.Problem{Reason: string(remote.Unreachable), Retry: "next-open"}, false)

	start := time.Now()
	x.Key("prefix+o")
	first := waitUntil(t, x, 5*time.Second, "a local row", func(rows []string) bool { return hubRow(rows, "alpha") })
	waitUntil(t, x, 30*time.Second, "the macbook's heading says it is not reachable", func(rows []string) bool {
		return strings.Contains(heading(rows, "macbook"), want)
	})
	took := time.Since(start)
	t.Logf("DS12: the heading of the sleeping machine said %q after %v", want, took)
	if took > 15*time.Second {
		t.Errorf("DS12: the heading took %v, want about 10 s", took)
	}
	closeHub(t, x)
	samples := []time.Duration{first}
	for range SamplesP95 - 1 {
		start := time.Now()
		x.Key("prefix+o")
		waitUntil(t, x, 5*time.Second, "a local row", func(rows []string) bool { return hubRow(rows, "alpha") })
		samples = append(samples, time.Since(start))
		closeHub(t, x)
	}
	t.Logf("T2b local rows: p50 %v, p95 %v over %d runs", percentile(samples, 50), percentile(samples, 95), len(samples))
	MeasureDuration(t, "T2b", "local rows", percentile(samples, 95))
	// The window still works: a local pick needs nothing of the macbook.
	choose(t, x, "omega", "omega")
	x.Key("enter")
	waitShows(t, x, 15*time.Second, "", "omega")
}

func TestSwitchLocalHeldCold(t *testing.T) {
	if cfg, ok := Real(); ok {
		_, x, _ := realScene(t)
		measureAgainst(t, "T3", lanBound("T3", "cold"), 0, pickReal(t, x, cfg))
		var held, local []time.Duration
		for range SamplesP95 {
			start := time.Now()
			openHub(t, x)
			x.Type("e2e-scratch")
			waitUntil(t, x, 15*time.Second, "the scratch row", func(rows []string) bool { return selected(rows, "e2e-scratch") || hubRow(rows, "e2e-scratch") })
			start = time.Now()
			x.Key("enter")
			waitUntil(t, x, 15*time.Second, "the scratch session", func(rows []string) bool {
				return !hubOpen(rows) && strings.Contains(statusRow(rows), "e2e-scratch")
			})
			local = append(local, time.Since(start))
			held = append(held, pickReal(t, x, cfg))
		}
		measureAgainst(t, "T3", lanBound("T3", "held"), 0, percentile(held, 95))
		measureAgainst(t, "T3", lanBound("T3", "local"), 0, percentile(local, 95))
		return
	}
	f := newFleet(t)
	project(t, f.a, "omega")
	// every session once, so that each has its mark on screen
	for _, s := range []struct{ machine, name string }{{"", "omega"}, {"macmini", "beta"}, {"macbook", "gamma"}} {
		visit(t, f.x, s.machine, s.name)
	}
	pick(t, f.x, "", "alpha")

	t.Run("local", func(t *testing.T) {
		measureP95(t, "T3", "local", SamplesP95, func(i int) time.Duration {
			if i%2 == 0 {
				return pick(t, f.x, "", "omega")
			}
			return pick(t, f.x, "", "alpha")
		})
	})
	pick(t, f.x, "", "alpha")
	t.Run("held", func(t *testing.T) {
		measureP95(t, "T3", "held", SamplesP95, func(int) time.Duration {
			took := pick(t, f.x, "macmini", "beta")
			pick(t, f.x, "", "alpha")
			return took
		})
	})
	// Cold: the macbook is not connected when the hub opens. Its rows come
	// only from its link (a lost machine's rows cannot be picked, DS5), so
	// the hub key dials it. The time is taken from the hub key, not from
	// enter: it holds the dial, the first rows, the selection and the full
	// frame, which is more than T3 cold asks and bounds it from above.
	t.Run("cold", func(t *testing.T) {
		measureP95(t, "T3", "cold", SamplesP95, func(int) time.Duration {
			f.dropCold(t)
			start := time.Now()
			pick(t, f.x, "macbook", "gamma")
			took := time.Since(start)
			pick(t, f.x, "", "alpha")
			return took
		})
	})
	if n := attachProcs(t, f.a, f.x); n != 1 {
		t.Errorf("SW1: %d client processes on the desktop, want one throughout", n)
	}
	for _, m := range []*Machine{f.b, f.c} {
		if n := attachProcs(t, m); n != 0 {
			t.Errorf("SW1: %d client processes on the %s", n, m.Name)
		}
	}
}

// machineNotices are the notices that name machine and why a switch to it
// failed (5a.14.4).
func machineNotices(machine string) []string {
	out := []string{ui.Notice("no-answer", machine), ui.Notice("still-connecting", machine), ui.Notice("refused", machine, "")}
	for _, r := range []remote.Reason{remote.Auth, remote.HostKey, remote.Unreachable, remote.NoDevtools, remote.NoServer, remote.Incompatible, remote.Protocol, "lost"} {
		out = append(out, ui.Notice("machine", machine, string(r), ""))
	}
	for i, n := range out {
		out[i] = strings.TrimSpace(n)
	}
	return out
}

// noticeOf returns the notice of machine in the status row, "" when there
// is none.
func noticeOf(rows []string, machine string) string {
	st := statusRow(rows)
	for _, n := range machineNotices(machine) {
		if strings.Contains(st, n) {
			return n
		}
	}
	return ""
}

func TestSwitchFailureKeepsView(t *testing.T) {
	t.Parallel()
	f := newFleet(t, "delta")
	keeps := func(t *testing.T, what string, enter func()) {
		t.Helper()
		// a notice of an earlier failure leaves first (ui.NoticeLife)
		waitUntil(t, f.x, ui.NoticeLife+5*time.Second, "no notice of the macmini", func(rows []string) bool { return noticeOf(rows, "macmini") == "" })
		start := time.Now()
		enter()
		var notice string
		waitUntil(t, f.x, 20*time.Second, what+": the hub closed and a notice names the macmini", func(rows []string) bool {
			notice = noticeOf(rows, "macmini")
			return notice != "" && !hubOpen(rows)
		})
		took := time.Since(start)
		t.Logf("%s: %q", what, notice)
		MeasureDuration(t, "T3b", "report", took)
		rows := f.x.Screen()
		if !showsFull(rows, "", "alpha") {
			t.Errorf("%s: the previous view is gone; screen:\n%s", what, strings.Join(rows, "\n"))
		}
	}
	t.Run("refused", func(t *testing.T) {
		// The project's directory goes away after the hub listed it: the
		// macmini refuses to open it.
		choose(t, f.x, "delta", "delta")
		if err := os.RemoveAll(filepath.Join(f.b.Home, "dev", "delta")); err != nil {
			t.Fatal(err)
		}
		keeps(t, "a refused switch", func() { f.x.Key("enter") })
		if !strings.Contains(statusRow(f.x.Screen()), strings.TrimSpace(ui.Notice("refused", "macmini", ""))) {
			t.Errorf("the notice is not the refusal of the macmini: %q", statusRow(f.x.Screen()))
		}
	})
	t.Run("timed out", func(t *testing.T) {
		// The link is up and the bridge stops answering: the switch waits
		// for view.ok, then gives up with the link.
		eventually(t, 20*time.Second, "the macmini is back", func() bool { return linked(f.b) && len(f.lb.Pids()) > 0 })
		closeHub(t, f.x)
		choose(t, f.x, "beta", "beta")
		pids := f.lb.Pids()
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGSTOP)
		}
		defer func() {
			for _, pid := range pids {
				syscall.Kill(pid, syscall.SIGCONT)
			}
		}()
		keeps(t, "a switch that gets no answer", func() { f.x.Key("enter") })
	})
}

// counter is what the counting loop of a pane prints.
var counter = regexp.MustCompile(`count (\d+)`)

// count returns the last count on screen, -1 when there is none.
func count(rows []string) int {
	m := counter.FindAllStringSubmatch(strings.Join(rows, "\n"), -1)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[len(m)-1][1])
	return n
}

// linkOutage is how long TestLinkCutShowsStaleThenResumes keeps the link
// cut: the counter of the macmini advances by about ten meanwhile, and the
// fleet's next redial comes 1.5 s after the link is back (the backoff of
// NT6), inside T7's 3 s.
const linkOutage = 2 * time.Second

func TestLinkCutShowsStaleThenResumes(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	f.b.LinkTool("sleep")
	visit(t, f.x, "macmini", "beta")
	f.x.Type("i=0; while :; do i=$((i+1)); printf 'count %d \\r' $i; sleep 0.2; done\r")
	waitUntil(t, f.x, 10*time.Second, "the counter runs", func(rows []string) bool { return count(rows) > 2 })

	f.lb.Block(string(remote.Unreachable))
	before := count(f.x.Screen())
	bridges := f.lb.Bridges()
	cut := time.Now()
	f.lb.Cut()
	stale := waitText(t, f.x, 20*time.Second, ui.BannerPaused)
	MeasureDuration(t, "T7", "stale", stale)
	// the last picture is kept
	if rows := f.x.Screen(); count(rows) < before || !shows(rows, "macmini", "beta") {
		t.Errorf("the last picture is gone; screen:\n%s", strings.Join(rows, "\n"))
	}
	// The link stays cut for linkOutage, then is let through again. T7's
	// "back" runs from that moment, the link back, to the first live frame:
	// it holds the wait for the fleet's next redial, the dial and the
	// frame. The fleet redials a connect machine with a backoff of 0.5 s
	// doubling (3.5.2, NT6), at 0.5, 1.5, 3.5, 7.5 s after the loss, so a
	// link that is back after an outage of L is redialled at the latest
	// about L later: T7's bound holds for an outage shorter than it, and
	// linkOutage is one. A longer outage is NT6's case, not T7's.
	time.Sleep(time.Until(cut.Add(linkOutage)))
	back := time.Now()
	f.lb.Block("")
	waitUntil(t, f.x, 20*time.Second, "the live view", func(rows []string) bool {
		return !strings.Contains(strings.Join(rows, "\n"), ui.BannerPaused) && shows(rows, "macmini", "beta") && count(rows) > before+5
	})
	MeasureDuration(t, "T7", "back", time.Since(back))
	if f.lb.Bridges() <= bridges {
		t.Errorf("the live view is back without a new bridge to the macmini")
	}
	if after := count(f.x.Screen()); after <= before+5 {
		t.Errorf("the counter of the macmini did not advance while the link was cut: %d, then %d", before, after)
	}
	// Cut again: while the view is stale the hub opens and a local pick
	// works (SW11).
	f.lb.Block(string(remote.Unreachable))
	f.lb.Cut()
	waitText(t, f.x, 20*time.Second, ui.BannerPaused)
	pick(t, f.x, "", "alpha")
	f.lb.Block("")
}

func TestRemoteServerRestartReopensByDirectory(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	boot := f.b.Welcome().Boot
	pid := shellPid(t, f.x, "before")
	f.b.Stop()
	f.b.Start()
	if f.b.Welcome().Boot == boot {
		t.Fatalf("the macmini kept its boot id %s", boot)
	}
	waitText(t, f.x, 30*time.Second, ui.Notice("restarted", "macmini"))
	waitShows(t, f.x, 10*time.Second, "macmini", "beta")
	if got := shellPid(t, f.x, "after"); got == pid {
		t.Errorf("the shell is still %d: its shells must be new", pid)
	}
}

func TestFailureReasons(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	reasons := make([]string, 0, len(blockFailures))
	for r := range blockFailures {
		reasons = append(reasons, r)
	}
	slices.Sort(reasons)
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			closeHub(t, f.x)
			f.dropCold(t)
			f.lc.Block(reason)
			phrase, failed := ui.MachinePhrase("macbook", "off", ui.Problem{Reason: reason, Retry: "next-open"}, false)
			want := strings.TrimSpace(phrase)
			if failed {
				want = glyph(ui.GlyphFailed) + " " + want
			}
			openHub(t, f.x)
			waitUntil(t, f.x, 30*time.Second, "the macbook's heading says "+reason, func(rows []string) bool {
				return strings.Contains(heading(rows, "macbook"), want)
			})
			rows := f.x.Screen()
			if !hubRow(rows, "alpha") || !hubRow(rows, "beta") {
				t.Errorf("the other machines are affected; screen:\n%s", strings.Join(rows, "\n"))
			}
			t.Logf("%s: %s", reason, heading(rows, "macbook"))
		})
	}
	f.lc.Block("")
}

func TestOldRemoteWithoutBridgeIsIncompatible(t *testing.T) {
	t.Parallel()
	a, b := StartMachine(t, "desktop"), StartMachine(t, "macmini")
	project(t, a, "alpha")
	project(t, b, "beta")
	Connect(t, a, b, "macmini", true)
	// the macbook runs a devtools from before the bridge, and no server
	old := newMachine(t, "macbook")
	if err := os.Remove(old.Bin(devtools)); err != nil {
		t.Fatal(err)
	}
	old.link(oldDevtools, self())
	if r := old.RunWith(RunOptions{}, "bridge"); r.Exit != 1 || !strings.Contains(r.Stderr, `unknown command "bridge"`) {
		t.Fatalf("the old devtools: exit %d, stderr %q", r.Exit, r.Stderr)
	}
	Connect(t, a, old, "macbook", false)
	x := openSession(t, a, plain(a, 120, 24), "alpha")
	phrase, _ := ui.MachinePhrase("macbook", "off", ui.Problem{Reason: string(remote.Incompatible), Retry: "next-open"}, false)
	want := glyph(ui.GlyphFailed) + " " + strings.TrimSpace(phrase)
	openHub(t, x)
	waitUntil(t, x, 30*time.Second, "the macbook's heading says incompatible", func(rows []string) bool {
		return strings.Contains(heading(rows, "macbook"), want)
	})
	// nothing else is affected
	waitUntil(t, x, 10*time.Second, "the rows of the desktop and the macmini", func(rows []string) bool {
		return hubRow(rows, "alpha") && hubRow(rows, "beta")
	})
	// one attempt per hub open, never a tight loop (DS7)
	time.Sleep(3 * time.Second)
	attempts := 0
	for _, args := range a.SSHLog() {
		if slices.Contains(args, "macbook") {
			attempts++
		}
	}
	if attempts != 1 {
		t.Errorf("%d attempts to reach the macbook with the hub open once, want 1", attempts)
	}
	closeHub(t, x)
	if !showsFull(x.Screen(), "", "alpha") {
		t.Errorf("the view of alpha is gone; screen:\n%s", x.Golden().Plain())
	}
}

func TestSwitchTypedInRemotePane(t *testing.T) {
	t.Parallel()
	f := newFleet(t, "delta")
	visit(t, f.x, "macmini", "beta")
	// typed in the macmini's pane, which the desktop's window shows
	f.x.Type("devtools switch delta; echo switch-status-$?\r")
	waitShows(t, f.x, 15*time.Second, "macmini", "delta")
	if n := attachProcs(t, f.a, f.x); n != 1 {
		t.Errorf("%d client processes on the desktop, want the one window", n)
	}
	if n := attachProcs(t, f.b); n != 0 {
		t.Errorf("%d client processes on the macmini", n)
	}
	// back on beta, the command had ended with status 0
	pick(t, f.x, "macmini", "beta")
	waitText(t, f.x, 10*time.Second, "switch-status-0")
}

func TestPaneFocusFromRemotePane(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	visit(t, f.x, "macmini", "beta")
	f.x.Key("prefix+%")
	var tab proto.Tab
	eventually(t, 10*time.Second, "the tab of beta is split, the keys in the right pane", func() bool {
		tab = currentTab(t, f.b, "beta")
		return len(tab.Panes) == 2 && tab.Focused == tab.Panes[1]
	})
	waitText(t, f.x, 10*time.Second, glyph(ui.GlyphRight))
	f.x.Type("devtools pane focus --direction left; echo focus-status-$?\r")
	waitText(t, f.x, 10*time.Second, "focus-status-0")
	eventually(t, 10*time.Second, "the macmini moved the focus to the left pane", func() bool {
		return currentTab(t, f.b, "beta").Focused == tab.Panes[0]
	})
	// The window follows when its cursor is in the left pane, left of the
	// divider.
	divider := strings.Index(f.x.Screen()[0], glyph(ui.GlyphDividerV))
	if divider < 0 {
		t.Fatalf("no divider; screen:\n%s", f.x.Golden().Plain())
	}
	divider = len([]rune(f.x.Screen()[0][:divider]))
	follows := func(d time.Duration) bool {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(poll) {
			if g := f.x.Golden(); g.CursorX < divider {
				return true
			}
		}
		return false
	}
	if !follows(3 * time.Second) {
		// provisional (open finding 1 of K3, contract change 14 part 1): the
		// fleet does not forward the displayed state yet, so the client
		// reads it with state.get only after frames, at most once a second
		// (internal/client/update.go, refreshEvery). A change of focus
		// that prints nothing is then seen with the next output; an empty
		// line gives it one. When the fleet forwards the state the window
		// follows at once and this branch is not taken.
		t.Log("provisional K3 finding 1: the window did not follow the focus within 3 s without output; an empty line is typed")
		time.Sleep(time.Second)
		f.x.Type("\r")
		if !follows(10 * time.Second) {
			t.Fatalf("the window does not show the focus in the left pane; screen:\n%s", f.x.Golden().Plain())
		}
	}
	// the window follows: what is typed now reaches the left pane
	f.x.Type("echo left-$((4+4))-here\r")
	waitText(t, f.x, 10*time.Second, "left-8-here")
	for _, r := range f.x.Screen() {
		if i := strings.Index(r, "left-8-here"); i >= 0 && strings.Index(r, glyph(ui.GlyphDividerV)) >= 0 && strings.Index(r, glyph(ui.GlyphDividerV)) < i {
			t.Errorf("the line went to the right pane: %q", r)
		}
	}
}

// currentTab returns the current tab of the session of name on m.
func currentTab(t *testing.T, m *Machine, name string) proto.Tab {
	t.Helper()
	s := snapshot(t, m)
	ss, ok := sessionOf(s, name)
	if !ok {
		return proto.Tab{}
	}
	for _, tb := range s.Tabs {
		if tb.ID == ss.Current {
			return tb
		}
	}
	return proto.Tab{}
}

func TestAgentBlockedRingsBellOffScreen(t *testing.T) {
	t.Parallel()
	f := newFleet(t)
	f.b.link(fakeAgentName, self())
	visit(t, f.x, "macmini", "beta")
	f.x.Type(fakeAgentName + "\r")
	waitText(t, f.x, 10*time.Second, "agent working")
	eventually(t, 15*time.Second, "the macmini sees a working claude", func() bool {
		for _, ag := range snapshot(t, f.b).Agents {
			if ag.Kind == proto.AgentClaude && ag.Status == proto.StatusWorking {
				return true
			}
		}
		return false
	})
	// the agent's tab is not on screen: the window shows a local session
	pick(t, f.x, "", "alpha")
	waitText(t, f.x, 10*time.Second, glyph(ui.GlyphWorking))
	bells := f.x.BellCount()
	start := time.Now()
	writeTrigger(t, f.b)
	for f.x.BellCount() == bells {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("no bell within 10 s; screen:\n%s", f.x.Golden().Plain())
		}
		time.Sleep(poll)
	}
	MeasureDuration(t, "T8", "bell", time.Since(start))
	waitText(t, f.x, 10*time.Second, ui.RollupWord(proto.StatusBlocked, 1))
	time.Sleep(3 * time.Second)
	if got := f.x.BellCount() - bells; got != 1 {
		t.Errorf("%d bells, want one", got)
	}
	if !showsFull(f.x.Screen(), "", "alpha") {
		t.Errorf("the view changed; screen:\n%s", f.x.Golden().Plain())
	}
}
