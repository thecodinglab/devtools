package e2e

// What the scenarios of the suite (P3-05b) share: projects, the sessions
// they open, reading the screen of a Term, the hub, and three fake programs
// of their own (an agent, a flood, a devtools that predates the bridge).
// Every expected text comes from internal/ui; none is copied here.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"
)

// The fake programs of this file, placed in a machine's bin directory with
// m.link(name, self()).
const (
	fakeAgentName = "claude"    // an agent of the kind claude (3.6.1)
	fakeFloodName = "e2e-flood" // writes a number of MiB of text
	oldDevtools   = devtools    // a devtools built before the bridge existed
)

func init() {
	fakes[fakeAgentName] = runFakeAgent
	fakes[fakeFloodName] = runFakeFlood
	fakes[oldDevtools] = runOldDevtools
}

// theme is the look's theme with the UTF-8 glyphs: every machine of the
// suite runs with LANG=C.UTF-8 (testkit.CleanEnv).
var theme = func() ui.Theme {
	t, err := ui.NewTheme(ui.ThemeOptions{UTF8: true, Dark: true, Known: true, Colors: 1 << 24})
	if err != nil {
		panic(err)
	}
	return t
}()

func glyph(g ui.Glyph) string { return theme.Glyph(g) }

// project makes the git repository ~/dev/<name> of m, a project with the
// one worktree main, and returns its directory. ~/dev is the root the
// catalog and the command line scan when the config names none.
func project(t *testing.T, m *Machine, name string) string {
	t.Helper()
	m.LinkTool("git")
	dir := filepath.Join(m.Home, "dev", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if r := m.RunBin("git", RunOptions{Dir: dir}, "init", "-q"); r.Exit != 0 {
		t.Fatalf("git init %s: %s", dir, r.Stderr)
	}
	return dir
}

// label is the session label of a project's main worktree.
func label(name string) string { return session.Name(name, "main") }

// plain is a plain terminal of m of cols x rows (In(m, Plain)).
func plain(m *Machine, cols, rows int) TermOptions {
	o := In(m, Plain)
	o.Cols, o.Rows = cols, rows
	return o
}

// overSSH is the terminal of a shell reached over ssh that answers
// XTVERSION with identity: no mark survives ssh, so its environment is that
// of a plain terminal.
func overSSH(identity string) TermOptions {
	return TermOptions{Env: plainEnv(), Identity: identity}
}

// openSession starts the client on m in a plain terminal of o on the
// session of project name (`devtools switch <name>`), waits until it shows
// the session with its shell, and prints mark (see markOf) into the pane,
// so that a later wait can tell a full frame of this session.
func openSession(t *testing.T, m *Machine, o TermOptions, name string) *Term {
	t.Helper()
	x := Attach(t, m, o, "switch", name)
	waitShows(t, x, 15*time.Second, "", name)
	printMark(t, x, name)
	return x
}

// markOf is the text a session's first pane prints once (printMark); it
// holds no project name, so it never matches a row of the hub.
func markOf(name string) string {
	h := 0
	for _, r := range name {
		h = h*31 + int(r)
	}
	return fmt.Sprintf("mark-%d-", h%100000)
}

// printMark makes the focused pane print the mark of name: the pane shows
// the command with $((...)) and the result as the number, so the wait sees
// the output, not the echo of the typed line.
func printMark(t *testing.T, x *Term, name string) {
	t.Helper()
	x.Type("echo " + markOf(name) + "$((6*7))\r")
	waitText(t, x, 10*time.Second, markOf(name)+"42")
}

// hasMark reports whether the screen shows the mark of name.
func hasMark(rows []string, name string) bool {
	return strings.Contains(strings.Join(rows, "\n"), markOf(name)+"42")
}

// statusRow is the last row of the screen.
func statusRow(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	return rows[len(rows)-1]
}

// shows reports whether the status row names the session of project name
// on machine ("" for this machine): a session of another machine carries
// the machine's chip, " name ", at the start of the row in front of its
// label.
func shows(rows []string, machine, name string) bool {
	st := statusRow(rows)
	if machine != "" {
		return strings.HasPrefix(st, chipped(machine, label(name)))
	}
	for _, other := range remoteMachines {
		if strings.HasPrefix(st, chipped(other, label(name))) {
			return false
		}
	}
	return strings.Contains(st, " "+label(name)+" ")
}

// remoteMachines are the machines of the suite that are not this one.
var remoteMachines = []string{"macmini", "macbook"}

// chipped is the start of a status row that shows the session lbl of
// another machine: its chip, " machine ", and the label after the
// separator.
func chipped(machine, lbl string) string { return " " + machine + "  " + lbl }

// isHeading reports whether a row of the hub heads the section of a machine
// (machine "" for any): the machine's name, its phrase, and a rule to the
// margin. The input's rule and the closing rule are rules alone.
func isHeading(row, machine string) bool {
	trim := strings.TrimSpace(row)
	rule := glyph(ui.GlyphDividerH)
	if trim == "" || strings.HasPrefix(trim, rule) || !strings.HasSuffix(trim, rule) {
		return false
	}
	return machine == "" || trim == machine || strings.HasPrefix(trim, machine+" ")
}

// showsFull reports whether the screen is a full frame of the session of
// name: its label in the status row and its pane's mark on screen.
func showsFull(rows []string, machine, name string) bool {
	return shows(rows, machine, name) && hasMark(rows, name)
}

// poll is the interval of the waits that measure: short against the
// thresholds, which start at 6 ms.
const poll = 200 * time.Microsecond

// waitUntil polls the screen until ok holds and returns how long that took;
// the test fails after d with the screen.
func waitUntil(t *testing.T, x *Term, d time.Duration, what string, ok func(rows []string) bool) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		if ok(x.Screen()) {
			return time.Since(start)
		}
		if time.Since(start) > d {
			t.Fatalf("%s: not within %v; screen:\n%s", what, d, x.Golden().Plain())
		}
		time.Sleep(poll)
	}
}

// waitText waits until text is on the screen.
func waitText(t *testing.T, x *Term, d time.Duration, text string) time.Duration {
	t.Helper()
	return waitUntil(t, x, d, fmt.Sprintf("%q on screen", text), func(rows []string) bool {
		return strings.Contains(strings.Join(rows, "\n"), text)
	})
}

// waitShows waits until the status row names the session of name on
// machine.
func waitShows(t *testing.T, x *Term, d time.Duration, machine, name string) time.Duration {
	t.Helper()
	return waitUntil(t, x, d, fmt.Sprintf("the session %s of %q on screen", label(name), machine), func(rows []string) bool {
		return shows(rows, machine, name)
	})
}

// hubOpen reports whether the hub is on screen: its input row starts with
// the prompt glyph.
func hubOpen(rows []string) bool {
	p := glyph(ui.GlyphPrompt) + " "
	for _, r := range rows {
		if strings.HasPrefix(strings.TrimLeft(r, " "), p) {
			return true
		}
	}
	return false
}

// hubRow reports whether a row of the hub (not its input row and not the
// status row) shows text.
func hubRow(rows []string, text string) bool {
	if len(rows) < 2 {
		return false
	}
	for _, r := range rows[:len(rows)-1] {
		if strings.HasPrefix(strings.TrimLeft(r, " "), glyph(ui.GlyphPrompt)+" ") {
			continue
		}
		if strings.Contains(r, text) {
			return true
		}
	}
	return false
}

// selected reports whether the selected row of the hub is the one of name.
func selected(rows []string, name string) bool {
	return hubRow(rows, glyph(ui.GlyphSelected)+" "+name)
}

// heading returns the row of the hub that heads the section of machine
// ("macmini   listing… ───"), "" when there is none.
func heading(rows []string, machine string) string {
	for _, r := range rows[:max(0, len(rows)-1)] {
		if isHeading(r, machine) {
			return strings.TrimSpace(r)
		}
	}
	return ""
}

// openHub presses the hub key and waits until the hub is on screen.
func openHub(t *testing.T, x *Term) time.Duration {
	t.Helper()
	start := time.Now()
	x.Key("prefix+o")
	waitUntil(t, x, 10*time.Second, "the hub opens", hubOpen)
	return time.Since(start)
}

// closeHub closes the hub (escape, twice when a query is typed) and waits
// until it is gone.
func closeHub(t *testing.T, x *Term) {
	t.Helper()
	for i := 0; i < 3 && hubOpen(x.Screen()); i++ {
		x.Key("escape")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && hubOpen(x.Screen()) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitUntil(t, x, 5*time.Second, "the hub closes", func(rows []string) bool { return !hubOpen(rows) })
}

// choose opens the hub, types query and waits until the row of name is
// selected; the next enter picks it.
func choose(t *testing.T, x *Term, query, name string) {
	t.Helper()
	if !hubOpen(x.Screen()) {
		openHub(t, x)
	}
	x.Type(query)
	waitUntil(t, x, 15*time.Second, "the row "+name+" is selected", func(rows []string) bool { return selected(rows, name) })
}

// pick switches the window to the session of project name on machine
// through the hub and returns the time from enter to the full frame.
func pick(t *testing.T, x *Term, machine, name string) time.Duration {
	t.Helper()
	choose(t, x, name, name)
	start := time.Now()
	x.Key("enter")
	waitUntil(t, x, 15*time.Second, fmt.Sprintf("a full frame of %s on %q", label(name), machine), func(rows []string) bool {
		return !hubOpen(rows) && showsFull(rows, machine, name)
	})
	return time.Since(start)
}

// rawOutput is everything the program of x wrote, escape sequences kept.
func rawOutput(x *Term) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.raw.String()
}

// altScreen is the sequence that enters the alternate screen; a client that
// starts writes it, words printed before a client writes none.
const altScreen = "\x1b[?1049h"

// lines is output with the carriage returns of the terminal removed.
func lines(out string) string { return strings.ReplaceAll(out, "\r", "") }

// attachProcs counts the processes of m's binary whose command line ends in
// attach, or that are a client started by `devtools switch` in a Term of
// m, the client processes the harness can see from the process table.
func attachProcs(t *testing.T, m *Machine, terms ...*Term) int {
	t.Helper()
	procs, err := platform.Procs()
	if err != nil {
		t.Fatal(err)
	}
	pids := map[int]bool{}
	for _, x := range terms {
		if !x.Exited() {
			pids[x.Pid()] = true
		}
	}
	n := 0
	for _, p := range procs {
		if len(p.Args) < 2 || p.Args[0] != m.Bin(devtools) {
			continue
		}
		if p.Args[len(p.Args)-1] == "attach" || pids[p.Pid] {
			n++
		}
	}
	return n
}

// serverProcs counts the processes of m's binary that run `devtools
// server`.
func serverProcs(t *testing.T, m *Machine) int {
	t.Helper()
	procs, err := platform.Procs()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range procs {
		if len(p.Args) >= 2 && p.Args[0] == m.Bin(devtools) && p.Args[1] == "server" {
			n++
		}
	}
	return n
}

// snapshot returns the state of m's server.
func snapshot(t *testing.T, m *Machine) proto.Snapshot {
	t.Helper()
	_, r := m.Oneshot(proto.StateGet{})
	if !r.OK {
		t.Fatalf("state.get on %s: %s %s", m.Name, r.Code, r.Text)
	}
	var s proto.Snapshot
	if err := r.Msg.Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// sessionOf returns the session of m whose label is label(name).
func sessionOf(s proto.Snapshot, name string) (proto.Session, bool) {
	for _, ss := range s.Sessions {
		if ss.Label == label(name) {
			return ss, true
		}
	}
	return proto.Session{}, false
}

// layout is the shape of a session: per tab, in index order, the split and
// the working directory of each pane.
func layout(s proto.Snapshot, name string) string {
	ss, ok := sessionOf(s, name)
	if !ok {
		return "no session " + label(name)
	}
	tabs := map[string]proto.Tab{}
	for _, tb := range s.Tabs {
		tabs[tb.ID] = tb
	}
	panes := map[string]proto.Pane{}
	for _, p := range s.Panes {
		panes[p.ID] = p
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s in %s:", ss.Label, ss.Dir)
	for _, id := range ss.Tabs {
		tb := tabs[id]
		fmt.Fprintf(&b, " tab %d %s [", tb.Index, tb.Split)
		for i, p := range tb.Panes {
			if i > 0 {
				b.WriteString(" ")
			}
			b.WriteString(panes[p].Cwd)
		}
		b.WriteString("]")
	}
	return b.String()
}

// shellPid makes the focused pane print the pid of its shell and returns
// it; tag tells this reading from an earlier one.
func shellPid(t *testing.T, x *Term, tag string) int {
	t.Helper()
	x.Type("echo " + tag + "-pid-$$-end\r")
	re := regexp.MustCompile(regexp.QuoteMeta(tag) + `-pid-(\d+)-end`)
	var pid int
	waitUntil(t, x, 10*time.Second, "the shell prints its pid", func(rows []string) bool {
		m := re.FindStringSubmatch(strings.Join(rows, "\n"))
		if m == nil {
			return false
		}
		pid, _ = strconv.Atoi(m[1])
		return true
	})
	return pid
}

// percentile returns the p-th percentile (0 to 100) of samples, nearest
// rank.
func percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	s := slices.Clone(samples)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	rank := int(p/100*float64(len(s))+0.999999) - 1
	return s[min(max(rank, 0), len(s)-1)]
}

// measureP95 runs one measurement n times and reports the p95 against
// threshold id's bound what.
func measureP95(t *testing.T, id, what string, n int, once func(i int) time.Duration) []time.Duration {
	t.Helper()
	samples := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, once(i))
	}
	t.Logf("%s %s: p50 %v, p95 %v, max %v over %d runs", id, what, percentile(samples, 50), percentile(samples, 95), percentile(samples, 100), n)
	MeasureDuration(t, id, what, percentile(samples, 95))
	return samples
}

// lanBound returns the bound what of threshold id on real machines.
func lanBound(id, what string) Bound {
	for _, b := range Row(id).LAN {
		if b.What == what {
			return b
		}
	}
	panic(fmt.Sprintf("e2e: threshold %s has no LAN bound %q", id, what))
}

// measureAgainst logs a measured time beside bound b of threshold id and
// fails the test when it is missed, for the bounds that are not in the
// loopback column (Measure reads that column).
func measureAgainst(t *testing.T, id string, b Bound, extra, got time.Duration) {
	t.Helper()
	limit := b.Duration() + extra
	t.Logf("%s %s: measured %.3g ms, at most %.3g ms", id, b.What, float64(got)/float64(time.Millisecond), float64(limit)/float64(time.Millisecond))
	if got > limit {
		t.Errorf("%s %s: %v misses the threshold (at most %v)", id, b.What, got, limit)
	}
}

// rssBytes is the resident set size of process pid: VmRSS of
// /proc/<pid>/status on Linux, ps -o rss= on macOS (10.4). The engine's
// memory lies outside the Go heap, so the heap is never read.
func rssBytes(t *testing.T, pid int) int64 {
	t.Helper()
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				kib, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				return kib << 10
			}
		}
		t.Fatalf("no VmRSS in /proc/%d/status", pid)
	}
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o rss= -p %d: %v", pid, err)
	}
	kib, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return kib << 10
}

// cpuTime is the user and system time process pid has used: fields 14 and
// 15 of /proc/<pid>/stat on Linux, ps -o time= on macOS.
func cpuTime(t *testing.T, pid int) time.Duration {
	t.Helper()
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		// fields[0] is field 3 (state); utime and stime are fields 14 and 15
		ut, err1 := strconv.ParseInt(fields[11], 10, 64)
		st, err2 := strconv.ParseInt(fields[12], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("/proc/%d/stat: %q", pid, s)
		}
		// USER_HZ is 100 on every Linux devtools runs on
		return time.Duration(ut+st) * 10 * time.Millisecond
	}
	out, err := exec.Command("/bin/ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o time= -p %d: %v", pid, err)
	}
	return parsePSTime(t, strings.TrimSpace(string(out)))
}

// parsePSTime reads the time column of ps: [[dd-]hh:]mm:ss.cc.
func parsePSTime(t *testing.T, s string) time.Duration {
	t.Helper()
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		days, _ = strconv.ParseInt(d, 10, 64)
		s = rest
	}
	parts := strings.Split(s, ":")
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			t.Fatalf("ps time %q", s)
		}
		total = total*60 + v
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total*float64(time.Second))
}

// agentTrigger is the file whose appearance turns the fake agent of a
// machine blocked.
func agentTrigger(root string) string { return filepath.Join(root, "f", "agent-blocked") }

// The OSC 7501 reports of the fake agent (3.6.2).
const (
	oscWorking = "\x1b]7501;state=working\x1b\\"
	oscBlocked = "\x1b]7501;state=blocked:kind=permission:app=claude-code\x07"
)

// runFakeAgent is a fake Claude Code: started as "claude" in a pane, it
// reports working, prints a line, and reports blocked when the trigger file
// of its machine appears; then it waits for the end of its input.
func runFakeAgent(args []string) int {
	root, err := selfRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Stdout.WriteString(oscWorking + "agent working\r\n")
	for {
		if _, err := os.Stat(agentTrigger(root)); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	os.Stdout.WriteString(oscBlocked + "agent blocked\r\n")
	buf := make([]byte, 256)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			return 0
		}
	}
}

// floodLine is one line of the flood: 79 printable characters and a line
// end, so no line wraps at 80 columns.
var floodLine = []byte(strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz", 3)[:79] + "\n")

// floodDone is what the flood prints after its last byte.
const floodDone = "flood-done"

// runFakeFlood writes args[0] MiB of text lines to its standard output,
// then floodDone.
func runFakeFlood(args []string) int {
	mib := 100
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
			mib = v
		}
	}
	chunk := make([]byte, 0, 64<<10)
	for len(chunk)+len(floodLine) <= cap(chunk) {
		chunk = append(chunk, floodLine...)
	}
	for written := 0; written < mib<<20; written += len(chunk) {
		if _, err := os.Stdout.Write(chunk); err != nil {
			return 1
		}
	}
	os.Stdout.WriteString(floodDone + "\n")
	return 0
}

// runOldDevtools is a devtools of a build before the bridge: its command
// line knows no `bridge` and says so as cobra did in the build at 87d8dfb
// (cmd/devtools/main.go printed the error and exited 1).
func runOldDevtools(args []string) int {
	if len(args) == 0 {
		fmt.Println("Manage dev worktrees and their herdr or tmux sessions")
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q for %q\n", args[0], devtools)
	return 1
}

// writeTrigger creates the trigger file of the fake agent of m.
func writeTrigger(t *testing.T, m *Machine) {
	t.Helper()
	if err := os.WriteFile(agentTrigger(m.Root()), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// eventually is testkit.Eventually.
func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	testkit.Eventually(t, d, what, ok)
}

// plainRows is the screen of a golden screen as plain rows.
func plainRows(s golden.Screen) []string {
	out := make([]string, len(s.Lines))
	for i, l := range s.Lines {
		out[i] = golden.PlainRow(l)
	}
	return out
}

// visit switches the window to the session of project name on machine
// through the hub, the first time: it waits until the status row names the
// session, then has its pane print the mark that later picks wait for.
func visit(t *testing.T, x *Term, machine, name string) {
	t.Helper()
	choose(t, x, name, name)
	x.Key("enter")
	waitUntil(t, x, 15*time.Second, fmt.Sprintf("the session %s on %q", label(name), machine), func(rows []string) bool {
		return !hubOpen(rows) && shows(rows, machine, name)
	})
	if !hasMark(x.Screen(), name) {
		printMark(t, x, name)
	}
}
