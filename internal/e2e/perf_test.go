package e2e

// The thresholds of the whole path (10.4): key to echo (T4), a flood (T5)
// and the idle cost (T6).

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit/golden"
)

// echoLine is the characters typed one at a time, one line of cat each:
// letters only, so no key is an editing key of the terminal's line
// discipline.
const echoLine = "abcdefghijklmnopqrstuvwxyz"

// cursorRow is the plain row the cursor of s is on.
func cursorRow(s golden.Screen) string {
	if s.CursorY < 0 || s.CursorY >= len(s.Lines) {
		return ""
	}
	return golden.PlainRow(s.Lines[s.CursorY])
}

// echoes measures n keys, one at a time, typed into a cat that runs in x
// (on a bare PTY or in a pane): the time from writing a key's byte to the
// moment the row of the cursor ends in everything typed on that line. The
// kernel's line discipline echoes, so the measurement is the path of the
// bytes and not of a program.
func echoes(t *testing.T, x *Term, n int) []time.Duration {
	t.Helper()
	samples := make([]time.Duration, 0, n)
	typed := ""
	for i := 0; i < n; i++ {
		if len(typed) == len(echoLine) {
			// a new line: cat prints the line back, the cursor goes down
			x.Type("\r")
			want := echoLine
			waitUntil(t, x, 5*time.Second, "cat prints the line back", func([]string) bool {
				g := x.Golden()
				return cursorRow(g) == "" && g.CursorY > 0 && strings.HasSuffix(golden.PlainRow(g.Lines[g.CursorY-1]), want)
			})
			typed = ""
		}
		c := echoLine[len(typed)]
		typed += string(c)
		want := typed
		start := time.Now()
		x.Type(string(c))
		for {
			if strings.HasSuffix(cursorRow(x.Golden()), want) {
				break
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("the echo of %q did not come within 5 s; screen:\n%s", c, x.Golden().Plain())
			}
			time.Sleep(poll)
		}
		samples = append(samples, time.Since(start))
		// one key at a time on an idle pane: the frame interval of the
		// client passes before the next key
		time.Sleep(20 * time.Millisecond)
	}
	return samples
}

// bareEchoes is the measurement on a bare PTY: cat started directly in a
// Term of m.
func bareEchoes(t *testing.T, m *Machine, n int) []time.Duration {
	t.Helper()
	if !m.IsReal() {
		m.LinkTool("cat") // also the cat of the panes: the bin directory is first on their PATH
	}
	cat, err := lookPath(m.Env(), "cat")
	if err != nil {
		t.Fatal(err)
	}
	x := StartIn(t, m, TermOptions{Cols: 80, Rows: 24}, []string{cat}, nil)
	time.Sleep(100 * time.Millisecond)
	return echoes(t, x, n)
}

// startCat runs cat in the focused pane of x and waits until it is the
// pane's program (the shell's prompt is gone from the cursor's row).
func startCat(t *testing.T, x *Term) {
	t.Helper()
	x.Type("printf '\\033[H\\033[2J'; cat\r")
	waitUntil(t, x, 10*time.Second, "cat runs", func([]string) bool {
		g := x.Golden()
		return cursorRow(g) == "" && !strings.Contains(strings.Join(plainRows(g), "\n"), "$ printf")
	})
	time.Sleep(200 * time.Millisecond)
}

// added reports the measurement through devtools over the bare one, at the
// percentile p, against the bound what of T4.
func added(t *testing.T, what string, p float64, through, bare []time.Duration) time.Duration {
	t.Helper()
	d := percentile(through, p) - percentile(bare, p)
	t.Logf("T4 %s: through devtools %v, bare PTY %v, added %v (percentile %.0f over %d keys)", what, percentile(through, p), percentile(bare, p), d, p, len(through))
	return max(d, 0)
}

func TestInputLatencyLocal(t *testing.T) {
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	bare := bareEchoes(t, m, SamplesP95)
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	startCat(t, x)
	through := echoes(t, x, SamplesP95)
	MeasureDuration(t, "T4", "local p50", added(t, "local p50", 50, through, bare))
	MeasureDuration(t, "T4", "local p95", added(t, "local p95", 95, through, bare))
}

// sshRoundTrip is the median round trip of a line through `ssh -T <alias>
// cat` in env: what T4 adds for a remote pane on real machines (10.4).
// It runs only in the real mode, which the user starts.
func sshRoundTrip(t *testing.T, env []string, alias string) time.Duration {
	t.Helper()
	cmd := exec.Command("ssh", "-T", "-o", "BatchMode=yes", "--", alias, "cat")
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("ssh -T %s cat: %v", alias, err)
	}
	defer func() {
		in.Close()
		cmd.Wait()
	}()
	r := bufio.NewReader(out)
	var samples []time.Duration
	for i := 0; i < 11; i++ {
		start := time.Now()
		fmt.Fprintf(in, "line %d\n", i)
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatalf("ssh -T %s cat: %v", alias, err)
		}
		samples = append(samples, time.Since(start))
	}
	rt := percentile(samples, 50)
	t.Logf("T4 remote: the round trip of ssh -T %s cat is %v", alias, rt)
	return rt
}

// The keys of TestInputLatencyRemote go to a scratch session the test opens
// on the other machine and closes at its end (P3-05b): in the real mode a
// session of the user is never typed into. The isolated run takes the same
// path through the fake ssh, and checks that the macmini's other sessions
// are as they were.
func TestInputLatencyRemote(t *testing.T) {
	if cfg, ok := Real(); ok {
		a, x, far := realScene(t)
		bare := bareEchoes(t, a, SamplesP95)
		pickScratch(t, x, far)
		startCat(t, x)
		through := echoes(t, x, SamplesP95)
		x.Type("\x04")
		rt := sshRoundTrip(t, a.Env(), cfg.Remote)
		measureAgainst(t, "T4", lanBound("T4", "remote p95"), rt, added(t, "remote p95", 95, through, bare))
		return
	}
	f := newFleet(t)
	bare := bareEchoes(t, f.a, SamplesP95)
	f.b.LinkTool("cat")
	dir, remove := fakeScratchDir(t, f.b)
	far := openScratch(t, f.a, "macmini", dir, remove)
	others := othersOf(snapshot(t, f.b), far.session)
	pickScratch(t, f.x, far)
	startCat(t, f.x)
	through := echoes(t, f.x, SamplesP95)
	f.x.Type("\x04")
	// provisional E5: on one host the fake ssh adds no network, so the
	// round trip of ssh -T <B> cat that the LAN bound adds is 0 here.
	measureAgainst(t, "T4", lanBound("T4", "remote p95"), 0, added(t, "remote p95", 95, through, bare))
	if got := othersOf(snapshot(t, f.b), far.session); got != others {
		t.Errorf("the other sessions of the macmini changed:\nbefore:\n%safter:\n%s", others, got)
	}
	// the window shows a local session again before the scratch one closes
	pick(t, f.x, "", "alpha")
}

// floodMiB is the size of the flood of T5.
const floodMiB = 100

func TestFloodThroughputAndResponsiveness(t *testing.T) {
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	m.link(fakeFloodName, self())
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	server := m.ServerPid()
	rss0 := rssBytes(t, server)
	peak := rss0

	start := time.Now()
	x.Type(fakeFloodName + " " + fmt.Sprint(floodMiB) + "\r")
	// the flood runs: the screen fills with its lines
	waitText(t, x, 10*time.Second, string(floodLine[:40]))
	time.Sleep(200 * time.Millisecond)

	// a key pressed during the flood acts: the hub opens
	var keys []time.Duration
	for i := 0; i < 5 && !strings.Contains(strings.Join(x.Screen(), "\n"), floodDone); i++ {
		keys = append(keys, openHub(t, x))
		closeHub(t, x)
		peak = max(peak, rssBytes(t, server))
		time.Sleep(100 * time.Millisecond)
	}
	waitUntil(t, x, 120*time.Second, "the end of the flood", func(rows []string) bool {
		peak = max(peak, rssBytes(t, server))
		return strings.Contains(strings.Join(rows, "\n"), floodDone)
	})
	took := time.Since(start)
	peak = max(peak, rssBytes(t, server))
	if len(keys) == 0 {
		t.Errorf("the flood ended before a key could be pressed during it (%v)", took)
	}
	Measure(t, "T5", "throughput", float64(floodMiB)/took.Seconds())
	MeasureDuration(t, "T5", "key during the flood", percentile(keys, 100))
	Measure(t, "T5", "server growth", float64(peak-rss0)/(1<<20))
	// the pane still answers
	printMark(t, x, "after the flood")
}

// idleFor is the time T6 is measured over.
const idleFor = 60 * time.Second

func TestIdleCPU(t *testing.T) {
	t.Parallel()
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	for i := 2; i <= 10; i++ {
		x.Key("prefix+c")
		want := i
		eventually(t, 10*time.Second, fmt.Sprintf("tab %d", want), func() bool {
			ss, _ := sessionOf(snapshot(t, m), "alpha")
			return len(ss.Tabs) == want
		})
	}
	s := snapshot(t, m)
	running := 0
	for _, p := range s.Panes {
		if p.State == "running" {
			running++
		}
	}
	if len(s.Panes) != 10 {
		t.Fatalf("%d panes, want 10", len(s.Panes))
	}
	// settle: the shells have printed their prompts
	time.Sleep(2 * time.Second)
	server, client := m.ServerPid(), x.Pid()
	cpu0 := cpuTime(t, server) + cpuTime(t, client)
	start := time.Now()
	time.Sleep(idleFor)
	used := cpuTime(t, server) + cpuTime(t, client) - cpu0
	wall := time.Since(start)
	t.Logf("T6: server and client used %v of CPU in %v with %d panes (%d running)", used, wall.Round(time.Millisecond), len(s.Panes), running)
	Measure(t, "T6", "cpu", 100*used.Seconds()/wall.Seconds())
	if x.Exited() {
		t.Errorf("the client ended")
	}
}
