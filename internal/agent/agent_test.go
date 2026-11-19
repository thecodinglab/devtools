package agent

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain keeps the tests hermetic (10.1): the package is pure and reads no
// environment, but nothing of the user's session may reach it either way.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-agent-test")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "DEVTOOLS_TEST_NO_PTY" {
			continue
		}
		if strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "XDG_") ||
			name == "TMUX" || name == "TMUX_PANE" || name == "CLAUDECODE" {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// t0 is the start of every scripted timeline.
var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

// The process shapes of E4 part D (Linux, read with ps and /proc).
var (
	nixClaude = Proc{Name: ".claude-wrapped", Args: []string{
		"/nix/store/4ikygy36qrkaxixgjpnwh5y84diqwai2-claude-code-2.1.319/bin/claude",
		"--settings", "/nix/store/16gi670xv2hrqpbi65s38g0y356367w9-claude-code-proxy-settings.json"}}
	// provisional (E4): inferred from the Nix wrapper of Codex 0.167.0, not observed.
	nixCodex = Proc{Name: ".codex-wrapped", Args: []string{
		"/nix/store/m40y4d072lw6556sl06w2q6k43fbp8kx-codex-0.167.0/bin/codex"}}
	shell = Proc{Name: "zsh", Args: []string{"-zsh"}}
)

// claudeTracker is a tracker that identified Claude at t0.
func claudeTracker(t *testing.T) *Tracker {
	t.Helper()
	tr := New()
	if !tr.Foreground(at(0), []Proc{nixClaude}) {
		t.Fatal("identifying Claude changed nothing")
	}
	return tr
}

func codexTracker(t *testing.T) *Tracker {
	t.Helper()
	tr := New()
	if !tr.Foreground(at(0), []Proc{nixCodex}) {
		t.Fatal("identifying Codex changed nothing")
	}
	return tr
}

func wantStatus(t *testing.T, tr *Tracker, want Status, when string) {
	t.Helper()
	if got := tr.State().Status; got != want {
		t.Fatalf("%s: status %q, want %q", when, got, want)
	}
}

func TestClaudeAndCodexAreRecognisedFromTheirProcesses(t *testing.T) {
	cases := []struct {
		what string
		proc Proc
		want Kind
	}{
		// E4 part D, Linux: what ran on this machine.
		{"Claude Code 2.1.319 under its Nix wrapper (E4, Linux)", nixClaude, Claude},
		{"the same with argv[0] rewritten by the program", Proc{Name: ".claude-wrapped", Args: []string{"claude"}}, Claude},
		// The planning rule of 3.6.1.
		{"a process named claude", Proc{Name: "claude", Args: []string{"claude"}}, Claude},
		{"node running the npm package", Proc{Name: "node", Args: []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}}, Claude},
		{"node with a script named claude", Proc{Name: "node", Args: []string{"node", "--no-warnings", "/home/u/.npm/bin/claude"}}, Claude},
		{"bun running claude", Proc{Name: "bun", Args: []string{"bun", "run", "claude"}}, Claude},
		// provisional (E4): macOS shapes; no ps of a Mac was recorded. The
		// native installer names the executable after its version and starts
		// it as claude; darwin's process table gives a path as the name.
		{"a native install named after its version (macOS, provisional E4)", Proc{Name: "2.1.319", Args: []string{"claude"}}, Claude},
		{"a name given as a path (macOS, provisional E4)", Proc{Name: "/Users/u/.local/bin/claude", Args: []string{"/Users/u/.local/bin/claude"}}, Claude},
		// Codex, provisional (E4): inferred from its wrapper, never observed.
		{"Codex 0.167.0 under its Nix wrapper (provisional E4)", nixCodex, Codex},
		{"a process named codex", Proc{Name: "codex", Args: []string{"codex"}}, Codex},
		{"a codex- helper", Proc{Name: "codex-code-mode-host", Args: []string{"codex-code-mode-host"}}, Codex},
		{"node running the npm package of codex", Proc{Name: "node", Args: []string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}}, Codex},
		{"node with a script named codex", Proc{Name: "node", Args: []string{"node", "/home/u/.npm/bin/codex", "exec"}}, Codex},
		// Not agents.
		{"vim editing a file named claude", Proc{Name: "vim", Args: []string{"vim", "claude"}}, ""},
		{"node running a server", Proc{Name: "node", Args: []string{"node", "server.js"}}, ""},
		{"node with claude only as a later argument", Proc{Name: "node", Args: []string{"node", "a.js", "b", "c", "claude"}}, ""},
		{"a name that only starts with claude", Proc{Name: "claudette", Args: []string{"claudette"}}, ""},
		{"a name that only starts with codex", Proc{Name: "codexy", Args: []string{"codexy"}}, ""},
		{"python with claude as an argument", Proc{Name: "python3", Args: []string{"python3", "claude"}}, ""},
		{"no arguments at all", Proc{Name: "less"}, ""},
	}
	for _, c := range cases {
		tr := New()
		tr.Foreground(at(0), []Proc{shell, c.proc})
		if got := tr.State().Kind; got != c.want {
			t.Errorf("%s: kind %q, want %q", c.what, got, c.want)
		}
	}
}

func TestAShellIsNotAnAgent(t *testing.T) {
	for _, group := range [][]Proc{
		{shell},
		{{Name: "bash", Args: []string{"bash"}}},
		{{Name: "fish", Args: []string{"/run/current-system/sw/bin/fish", "-l"}}},
		{{Name: "zsh", Args: []string{"zsh"}}, {Name: "git", Args: []string{"git", "commit"}}},
		nil,
	} {
		tr := New()
		if tr.Foreground(at(0), group) {
			t.Errorf("%v: a poll changed the state", group)
		}
		if st := tr.State(); st.Kind != "" || st.Status != "" {
			t.Errorf("%v: state %+v, want no agent", group, st)
		}
	}
	s := NewSet(func() time.Time { return t0 })
	if ch := s.Procs("p1", []Proc{shell}); len(ch) != 0 {
		t.Errorf("a shell gave changes %+v", ch)
	}
	if ch := s.Title("p1", "~/dev"); len(ch) != 0 {
		t.Errorf("the title of a shell gave changes %+v", ch)
	}
	if len(s.All()) != 0 {
		t.Errorf("All lists a shell: %+v", s.All())
	}
}

func TestAToolInTheForegroundDoesNotEndTheAgent(t *testing.T) {
	tr := claudeTracker(t)
	tr.Report(at(10), Report{State: "working"})
	if tr.Foreground(at(1000), []Proc{{Name: "git", Args: []string{"git", "status"}}}) {
		t.Error("one poll without a match changed the state")
	}
	if st := tr.State(); st.Kind != Claude || st.Status != Working {
		t.Fatalf("after one poll without a match: %+v", st)
	}
	tr.Foreground(at(2000), []Proc{nixClaude})
	tr.Foreground(at(3000), []Proc{shell})
	if st := tr.State(); st.Kind != Claude {
		t.Fatalf("a match between two misses did not reset the count: %+v", st)
	}
}

func TestTwoPollsWithoutTheAgentEndIt(t *testing.T) {
	tr := claudeTracker(t)
	tr.Report(at(10), Report{State: "working"})
	tr.Foreground(at(1000), []Proc{shell})
	if !tr.Foreground(at(2000), []Proc{shell}) {
		t.Fatal("the second poll without a match changed nothing")
	}
	if st := tr.State(); st.Kind != "" || st.Status != "" {
		t.Fatalf("after two polls without a match: %+v", st)
	}
	// A new agent in the pane starts from nothing: the old reports are gone.
	tr.Foreground(at(3000), []Proc{nixClaude})
	wantStatus(t, tr, Unknown, "a new agent")
}

func TestClaudeStatusFollowsItsReports(t *testing.T) {
	// The states and kinds the engine delivered in E4 part A (A04 to A14),
	// and what 3.6.2 makes of them, from working (so that a change into
	// working shows) and from idle.
	cases := []struct {
		r          Report
		fromIdle   Status // at once
		fromWork   Status // at once
		afterDelay Status // from working, after Debounce without being seen
	}{
		{Report{State: "idle"}, Idle, Working, Done},
		{Report{State: "working"}, Working, Working, Working},
		{Report{State: "done"}, Idle, Working, Done},
		{Report{State: "blocked"}, Blocked, Blocked, Blocked},
		{Report{State: "blocked", Kind: "permission"}, Blocked, Blocked, Blocked},
		{Report{State: "blocked", Kind: "question"}, Blocked, Blocked, Blocked},
		{Report{State: "blocked", Kind: "auth"}, Blocked, Blocked, Blocked},
		{Report{State: "error"}, Idle, Working, Done},
		{Report{State: "clear"}, Idle, Working, Done},
		{Report{State: "sleeping"}, Idle, Working, Done},
	}
	for _, c := range cases {
		tr := claudeTracker(t)
		tr.Report(at(1), Report{State: "idle"})
		tr.Report(at(100), c.r)
		if got := tr.State().Status; got != c.fromIdle {
			t.Errorf("%+v from idle: %q, want %q", c.r, got, c.fromIdle)
		}

		tr = claudeTracker(t)
		tr.Report(at(1), Report{State: "working"})
		tr.Report(at(100), c.r)
		if got := tr.State().Status; got != c.fromWork {
			t.Errorf("%+v from working: %q, want %q", c.r, got, c.fromWork)
		}
		tr.Tick(at(100).Add(Debounce))
		if got := tr.State().Status; got != c.afterDelay {
			t.Errorf("%+v from working, after %v: %q, want %q", c.r, Debounce, got, c.afterDelay)
		}
	}

	// Once the pane has sent a report, titles no longer move the status.
	tr := claudeTracker(t)
	tr.Report(at(1), Report{State: "idle"})
	tr.Title(at(2), "◐ Claude Code")
	wantStatus(t, tr, Idle, "a spinner title after a report")
	if tr.State().Title != "◐ Claude Code" {
		t.Errorf("title %q not kept", tr.State().Title)
	}

	// A report that came before the first matching poll counts.
	tr = New()
	tr.Report(at(1), Report{State: "blocked", Kind: "permission"})
	tr.Foreground(at(500), []Proc{nixClaude})
	wantStatus(t, tr, Blocked, "a report before identification")

	// Without a report, the title of 3.6.2 (E4 part C: "◐ "/"◑ " busy, "✳ " at rest).
	tr = claudeTracker(t)
	for _, step := range []struct {
		title string
		want  Status
	}{
		{"✳ Claude Code", Idle},
		{"◐ Claude Code", Working},
		{"◑ Claude Code", Working},
		{"⠂ Fix the parser", Working},
		{"Claude Code", Working}, // anything else changes nothing
	} {
		tr.Title(at(10), step.title)
		wantStatus(t, tr, step.want, "title "+step.title)
	}
}

// provisional (E4): the strings are those of the Codex 0.167.0 binary; F32
// removes this rule and this test if no Codex recording ever comes.
func TestCodexStatusFollowsItsTitle(t *testing.T) {
	tr := codexTracker(t)
	wantStatus(t, tr, Unknown, "before any title")
	steps := []struct {
		ms    int
		title string
		want  Status
	}{
		{1, "codex", Idle},
		{800, "⠋ codex", Working},
		{900, "⠙ codex", Working},
		{1301, "[ ! ] Action Required", Blocked},
		{1350, "[ . ] Action Required", Blocked},
		{2000, "⠹ codex", Working},
		{2500, "codex", Working}, // leaving working waits
	}
	for _, s := range steps {
		tr.Title(at(s.ms), s.title)
		wantStatus(t, tr, s.want, s.title)
	}
	tr.Tick(at(3000))
	wantStatus(t, tr, Done, "500 ms after the last spinner")

	// Codex reports are not its source while no recording confirms them.
	tr = codexTracker(t)
	tr.Title(at(1), "codex")
	tr.Report(at(2), Report{State: "working"})
	wantStatus(t, tr, Idle, "a report of Codex")
}

func TestLeavingWorkingIsDebounced(t *testing.T) {
	tr := claudeTracker(t)
	tr.Report(at(1000), Report{State: "working"})
	tr.Report(at(2000), Report{State: "idle"})
	wantStatus(t, tr, Working, "at once")
	changed, next := tr.Tick(at(2399))
	if changed {
		t.Error("a tick before the time changed the state")
	}
	if !next.Equal(at(2500)) {
		t.Errorf("next %v, want %v", next, at(2500))
	}
	// A second input in the same direction does not move the deadline.
	tr.Report(at(2400), Report{State: "done"})
	if _, next := tr.Tick(at(2450)); !next.Equal(at(2500)) {
		t.Errorf("next after a second idle %v, want %v", next, at(2500))
	}
	changed, next = tr.Tick(at(2500))
	if !changed || !next.IsZero() {
		t.Errorf("tick at the deadline: changed %v, next %v", changed, next)
	}
	wantStatus(t, tr, Done, "after the debounce")
	if !tr.State().Since.Equal(at(2500)) {
		t.Errorf("since %v, want %v", tr.State().Since, at(2500))
	}

	// A contrary input inside the 500 ms cancels the change.
	tr = claudeTracker(t)
	tr.Report(at(1000), Report{State: "working"})
	tr.Report(at(2000), Report{State: "idle"})
	tr.Report(at(2300), Report{State: "working"})
	if changed, next := tr.Tick(at(2600)); changed || !next.IsZero() {
		t.Errorf("after a contrary input: changed %v, next %v", changed, next)
	}
	wantStatus(t, tr, Working, "after a contrary input")
	if !tr.State().Since.Equal(at(1000)) {
		t.Errorf("since moved to %v", tr.State().Since)
	}
}

func TestEnteringBlockedIsImmediate(t *testing.T) {
	tr := claudeTracker(t)
	tr.Report(at(1000), Report{State: "working"})
	if !tr.Report(at(2000), Report{State: "blocked", Kind: "permission"}) {
		t.Fatal("blocked changed nothing")
	}
	wantStatus(t, tr, Blocked, "from working")
	if _, next := tr.Tick(at(2000)); !next.IsZero() {
		t.Errorf("blocked asked for a timer at %v", next)
	}
	// From blocked back to idle is not a change out of working: at once.
	tr.Report(at(3000), Report{State: "idle"})
	wantStatus(t, tr, Idle, "from blocked")
	// Into working is immediate too, also while a change out of working waits.
	tr.Report(at(4000), Report{State: "working"})
	wantStatus(t, tr, Working, "into working")
	tr.Report(at(5000), Report{State: "idle"})
	tr.Report(at(5100), Report{State: "blocked"})
	wantStatus(t, tr, Blocked, "while idle waited")
	tr.Tick(at(6000))
	wantStatus(t, tr, Blocked, "after the waiting idle's time")
}

func TestIdleAfterWorkingIsDoneUntilSeen(t *testing.T) {
	tr := claudeTracker(t)
	tr.Report(at(1), Report{State: "idle"})
	wantStatus(t, tr, Idle, "idle before any work")
	tr.Report(at(1000), Report{State: "working"})
	tr.Report(at(2000), Report{State: "done"})
	tr.Tick(at(2500))
	wantStatus(t, tr, Done, "after working, unseen")
	tr.Report(at(3000), Report{State: "idle"})
	tr.Report(at(4000), Report{State: "clear"})
	wantStatus(t, tr, Done, "further idle reports")
	tr.Report(at(5000), Report{State: "working"})
	wantStatus(t, tr, Working, "working clears done")
	tr.Report(at(6000), Report{State: "idle"})
	tr.Tick(at(6500))
	tr.Report(at(7000), Report{State: "blocked"})
	wantStatus(t, tr, Blocked, "blocked clears done")
	tr.Report(at(8000), Report{State: "idle"})
	wantStatus(t, tr, Idle, "idle after blocked is not done")

	// Seen while the change out of working waits: the user watched it end.
	tr = claudeTracker(t)
	tr.Report(at(1000), Report{State: "working"})
	tr.Report(at(2000), Report{State: "idle"})
	tr.Seen(at(2100))
	tr.Tick(at(2500))
	wantStatus(t, tr, Idle, "seen while the change waited")
}

func TestSeenTurnsDoneIntoIdle(t *testing.T) {
	tr := claudeTracker(t)
	tr.Report(at(1000), Report{State: "working"})
	tr.Report(at(2000), Report{State: "done"})
	tr.Tick(at(2500))
	wantStatus(t, tr, Done, "unseen")
	if !tr.Seen(at(3000)) {
		t.Fatal("seen changed nothing")
	}
	wantStatus(t, tr, Idle, "seen")
	if !tr.State().Since.Equal(at(3000)) {
		t.Errorf("since %v, want %v", tr.State().Since, at(3000))
	}
	if tr.Seen(at(4000)) {
		t.Error("seen again changed the state")
	}
	// Seen does not touch working or blocked.
	tr.Report(at(5000), Report{State: "working"})
	tr.Seen(at(5100))
	wantStatus(t, tr, Working, "seen while working")
	tr.Report(at(6000), Report{State: "blocked"})
	tr.Seen(at(6100))
	wantStatus(t, tr, Blocked, "seen while blocked")
}

func TestUnknownWhenNothingIsHeardWithinThreeSeconds(t *testing.T) {
	// provisional (E4): the status at identification is unknown and stays so
	// while nothing is heard; E4 recorded no real agent start.
	tr := claudeTracker(t)
	wantStatus(t, tr, Unknown, "at identification")
	if changed, next := tr.Tick(at(3000)); changed || !next.IsZero() {
		t.Errorf("tick: changed %v, next %v", changed, next)
	}
	tr.Foreground(at(1000), []Proc{nixClaude})
	tr.Foreground(at(2000), []Proc{nixClaude})
	tr.Foreground(at(3000), []Proc{nixClaude})
	tr.Title(at(3100), "Claude Code") // a title without a glyph says nothing
	wantStatus(t, tr, Unknown, "after 3 s with nothing heard")

	tr = claudeTracker(t)
	tr.Report(at(2000), Report{State: "idle"})
	tr.Tick(at(3000))
	wantStatus(t, tr, Idle, "a report within 3 s")
}

func TestNotifyOnlyOnEnteringBlockedOrDone(t *testing.T) {
	clock := t0
	s := NewSet(func() time.Time { return clock })
	step := func(ms int, f func() []Change) []Change {
		clock = at(ms)
		return f()
	}
	notified := func(ch []Change) bool {
		for _, c := range ch {
			if c.Notify {
				return true
			}
		}
		return false
	}
	tick := func() []Change { ch, _ := s.Tick(); return ch }
	steps := []struct {
		ms     int
		f      func() []Change
		notify bool
		what   string
	}{
		{0, func() []Change { return s.Procs("p1", []Proc{nixClaude}) }, false, "identification"},
		{1, func() []Change { return s.Report("p1", Report{State: "idle"}) }, false, "idle"},
		{1000, func() []Change { return s.Report("p1", Report{State: "working"}) }, false, "working"},
		{2000, func() []Change { return s.Report("p1", Report{State: "blocked", Kind: "permission"}) }, true, "blocked"},
		{2100, func() []Change { return s.Title("p1", "Claude Code") }, false, "a title while blocked"},
		{2200, func() []Change { return s.Report("p1", Report{State: "blocked", Kind: "question"}) }, false, "blocked again"},
		{3000, func() []Change { return s.Report("p1", Report{State: "working"}) }, false, "working again"},
		{4000, func() []Change { return s.Report("p1", Report{State: "done"}) }, false, "done waits"},
		{4500, tick, true, "done after the debounce"},
		{4600, func() []Change { return s.Report("p1", Report{State: "idle"}) }, false, "idle while done"},
		{5000, func() []Change { return s.Seen("p1") }, false, "seen"},
		{6000, func() []Change { return s.Report("p1", Report{State: "blocked"}) }, true, "blocked from idle"},
		{7000, func() []Change { return s.Report("p1", Report{State: "idle"}) }, false, "idle from blocked"},
		{8000, func() []Change { return s.Gone("p1") }, false, "gone"},
	}
	for _, st := range steps {
		ch := step(st.ms, st.f)
		if got := notified(ch); got != st.notify {
			t.Errorf("%s: notify %v, want %v (changes %+v)", st.what, got, st.notify, ch)
		}
	}
}

func TestGoneRemovesThePane(t *testing.T) {
	clock := t0
	s := NewSet(func() time.Time { return clock })
	s.Procs("p1", []Proc{nixClaude})
	s.Report("p1", Report{State: "working"})
	s.Procs("p2", []Proc{nixCodex})
	s.Procs("p3", []Proc{shell})
	clock = at(100)
	s.Report("p1", Report{State: "idle"}) // a change that waits
	if all := s.All(); len(all) != 2 || all["p1"].Kind != Claude || all["p2"].Kind != Codex {
		t.Fatalf("All: %+v", all)
	}
	ch := s.Gone("p1")
	if len(ch) != 1 || ch[0].Pane != "p1" || ch[0].State.Kind != "" || ch[0].Notify {
		t.Fatalf("Gone of an agent: %+v", ch)
	}
	if _, ok := s.All()["p1"]; ok {
		t.Fatal("p1 is still in All")
	}
	clock = at(1000)
	if ch, next := s.Tick(); len(ch) != 0 || !next.IsZero() {
		t.Errorf("tick after Gone: %+v, next %v", ch, next)
	}
	if ch := s.Gone("p3"); len(ch) != 0 {
		t.Errorf("Gone of a pane that was no agent: %+v", ch)
	}
	if ch := s.Gone("p9"); len(ch) != 0 {
		t.Errorf("Gone of an unknown pane: %+v", ch)
	}
	if ch := s.Seen("p9"); len(ch) != 0 || len(s.panes) != 1 {
		t.Errorf("Seen of an unknown pane: %+v, %d trackers", ch, len(s.panes))
	}
	// A pane that is used again starts afresh.
	s.Procs("p1", []Proc{shell})
	if _, ok := s.All()["p1"]; ok {
		t.Error("a new pane under an old id inherited its agent")
	}
}

func TestSetReportsTheEndOfAnAgentAndTheNextTimer(t *testing.T) {
	clock := t0
	s := NewSet(func() time.Time { return clock })
	s.Procs("p2", []Proc{nixClaude})
	s.Procs("p1", []Proc{nixClaude})
	s.Report("p1", Report{State: "working"})
	s.Report("p2", Report{State: "working"})
	clock = at(100)
	s.Report("p2", Report{State: "idle"})
	clock = at(200)
	s.Report("p1", Report{State: "idle"})
	ch, next := s.Tick()
	if len(ch) != 0 || !next.Equal(at(600)) {
		t.Fatalf("tick at 200: %+v, next %v", ch, next)
	}
	clock = at(700)
	ch, next = s.Tick()
	if len(ch) != 2 || ch[0].Pane != "p1" || ch[1].Pane != "p2" || !next.IsZero() {
		t.Fatalf("tick at 700: %+v, next %v", ch, next)
	}
	for _, c := range ch {
		if c.State.Status != Done || !c.Notify {
			t.Errorf("%+v, want a notified done", c)
		}
	}
	s.Procs("p1", []Proc{shell})
	ch = s.Procs("p1", []Proc{shell})
	if len(ch) != 1 || ch[0].State != (State{}) || ch[0].Notify {
		t.Fatalf("end of an agent: %+v", ch)
	}
	if ch := s.Title("p1", "~"); len(ch) != 0 {
		t.Errorf("a title of a pane that is no agent: %+v", ch)
	}
}

func TestRollupOrder(t *testing.T) {
	order := []Status{Blocked, Done, Working, Idle, Unknown}
	for i, s := range order {
		if want := len(order) - 1 - i; Rank(s) != want {
			t.Errorf("Rank(%q) = %d, want %d", s, Rank(s), want)
		}
	}
	if Rank("bogus") != 0 {
		t.Errorf("Rank of an unknown status: %d", Rank("bogus"))
	}
	cases := []struct {
		in   []Status
		want Status
	}{
		{nil, ""},
		{[]Status{Unknown}, Unknown},
		{[]Status{"bogus"}, Unknown},
		{[]Status{Idle, Unknown}, Idle},
		{[]Status{Unknown, Working, Idle}, Working},
		{[]Status{Working, Done, Idle}, Done},
		{[]Status{Idle, Done, Blocked, Working, Unknown}, Blocked},
		{[]Status{Blocked, Done}, Blocked},
	}
	for _, c := range cases {
		if got := Rollup(c.in); got != c.want {
			t.Errorf("Rollup(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTitlesAreSanitised(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b]52;c;aGVsbG8=\a✳ Claude Code", "]52;c;aGVsbG8=✳ Claude Code"},
		{"Fix\r\nthe\tparser", "Fixtheparser"},
		{"rtl\u202eoverride", "rtloverride"},
		{"\u2066isolated\u2069", "isolated"},
		{"c1\u009bcontrol", "c1control"},
		{"bad\xffbyte", "bad\ufffdbyte"},
		{"zero\u200bwidth", "zero\u200bwidth"}, // not a control: kept, as session.Sanitize does
		{strings.Repeat("x", 300), strings.Repeat("x", 255) + "…"},
	}
	for _, c := range cases {
		tr := claudeTracker(t)
		tr.Title(at(1), c.in)
		if got := tr.State().Title; got != c.want {
			t.Errorf("Title(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The glyph rule reads the sanitised title: a control character in front
	// of the spinner does not hide it.
	tr := claudeTracker(t)
	tr.Title(at(1), "\x1b◐ Claude Code")
	wantStatus(t, tr, Working, "a spinner behind ESC")
	// A repeated title changes nothing (E4: the engine reports it each time).
	if tr.Title(at(2), "◐ Claude Code") {
		t.Error("a repeated title changed the state")
	}
	// The title of a pane survives the end of its agent; a Set reports titles
	// of agents only.
	s := NewSet(func() time.Time { return t0 })
	s.Procs("p1", []Proc{nixClaude})
	ch := s.Title("p1", "hello\x07world")
	if len(ch) != 1 || ch[0].State.Title != "helloworld" {
		t.Fatalf("Set.Title: %+v", ch)
	}
}
