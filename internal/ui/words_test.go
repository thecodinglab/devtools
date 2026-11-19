package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMain is hermetic by construction: internal/ui reads no environment,
// no clock and no file outside testdata.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// assertGolden compares got with testdata/golden/<name>.txt, which holds
// the text and one newline after it. The messages of 5a.9.3 have the text
// layer only, typed from the mockup; it is never written by a test, so this
// helper has no -update. It stands in for golden.Assert of internal/testkit
// (P1-01), which is not merged when this package is written.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", name+".txt"))
	if err != nil {
		t.Fatalf("golden %s: %v", name, err)
	}
	want := string(raw)
	got += "\n"
	if got == want {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < max(len(gl), len(wl)); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g == w {
			continue
		}
		col := 0
		for col < len(g) && col < len(w) && g[col] == w[col] {
			col++
		}
		t.Errorf("golden %s differs in line %d:\n got  %q\n want %q\n       %s^", name, i+1, g, w, strings.Repeat(" ", col))
		return
	}
}

// The machine of the examples of 5a.14 and the backends of 5a.14.6.
const machine = "macmini"

var (
	backends     = []string{"devtools", "tmux", "herdr"}
	capabilities = []string{"machines", "agent-status", "window", "pane-focus", "layout", "detached-open"}
	reasons      = []string{"auth", "host-key", "unreachable", "no-devtools", "no-server", "incompatible", "protocol", "lost"}
)

func TestMachinePhraseForEveryStateAndReason(t *testing.T) {
	long := strings.Repeat("abcdefghij", 10) // 100 cells, the phrase keeps 60
	cases := []struct {
		row     string
		state   string
		p       Problem
		listing bool
		phrase  string
		failed  bool
	}{
		{"online, listing complete", "online", Problem{}, false, "", false},
		{"online, still listing", "online", Problem{}, true, "listing…", false},
		{"connecting", "connecting", Problem{}, false, "connecting…", false},
		{"off", "off", Problem{}, false, "not connected.", false},
		{"unreachable, retrying", "retrying", Problem{Reason: "unreachable", Retry: "retrying"}, false, "not reachable. Retrying.", false},
		{"unreachable, retried when the hub opens", "off", Problem{Reason: "unreachable", Retry: "next-open"}, false, "not reachable. Opening the hub again retries.", false},
		{"auth", "attention", Problem{Reason: "auth", Retry: "in-30s"}, false, "ssh could not log in. Run ssh macmini once in a shell.", true},
		{"host-key", "attention", Problem{Reason: "host-key", Retry: "in-30s"}, false, "host key not accepted. Run ssh macmini once and check it.", true},
		{"no-devtools", "attention", Problem{Reason: "no-devtools"}, false, "devtools is not installed there.", true},
		{"no-server", "attention", Problem{Reason: "no-server"}, false, "the devtools server is not running there.", true},
		{"incompatible", "attention", Problem{Reason: "incompatible"}, false, "incompatible. Update devtools on macmini.", true},
		{"protocol", "retrying", Problem{Reason: "protocol", Text: long, Retry: "retrying"}, false, "unexpected answer: " + long[:60], true},
		{"a lost link", "retrying", Problem{Reason: "lost", Retry: "retrying"}, false, "not reachable. Retrying.", false},
	}
	for _, c := range cases {
		phrase, failed := MachinePhrase(machine, c.state, c.p, c.listing)
		if phrase != c.phrase || failed != c.failed {
			t.Errorf("%s: MachinePhrase = %q, %v; want %q, %v", c.row, phrase, failed, c.phrase, c.failed)
		}
	}
	if phrase, _ := MachinePhrase(machine, "retrying", Problem{Reason: "protocol", Text: "a\nb\x1b[2J"}, false); strings.ContainsAny(phrase, "\n\x1b") {
		t.Errorf("a protocol text keeps its control characters: %q", phrase)
	}

	if got := ListPhrase("cut"); got != "list cut at 2,000 rows. Type to narrow it." {
		t.Errorf("list cut: %q", got)
	}
	if got := ListPhrase("incomplete"); got != "scan did not finish. Some worktrees may be missing." {
		t.Errorf("scan incomplete: %q", got)
	}
	if got := LocalPhrase("older"); got != "server is older than this client. Run devtools server restart." {
		t.Errorf("this machine, server older: %q", got)
	}
	if got := LocalPhrase("stopped"); got != "the devtools server stopped. Reconnecting." {
		t.Errorf("this machine, server stopped: %q", got)
	}
	if BannerPaused != "Typing is paused." {
		t.Errorf("the banner's sentence is %q", BannerPaused)
	}
}

func TestReasonWordForEveryReason(t *testing.T) {
	want := map[string]string{
		"auth":         "auth",
		"host-key":     "host key",
		"unreachable":  "unreachable",
		"no-devtools":  "no devtools",
		"no-server":    "no server",
		"incompatible": "incompatible",
		"protocol":     "protocol",
		"lost":         "lost",
	}
	for _, r := range reasons {
		if got := ReasonWord(r); got != want[r] {
			t.Errorf("ReasonWord(%q) = %q, want %q", r, got, want[r])
		}
	}
}

func TestRollupWordSingularAndPlural(t *testing.T) {
	cases := []struct {
		status string
		n      int
		want   string
	}{
		{"blocked", 1, "needs you"},
		{"blocked", 2, "need you"},
		{"blocked", 12, "need you"},
		{"done", 1, "done"},
		{"done", 3, "done"},
		{"working", 1, ""},
		{"working", 4, ""},
		{"idle", 2, ""},
	}
	for _, c := range cases {
		if got := RollupWord(c.status, c.n); got != c.want {
			t.Errorf("RollupWord(%q, %d) = %q, want %q", c.status, c.n, got, c.want)
		}
	}
	if got := UrgentPlace("macmini api", 1); got != "in macmini api" {
		t.Errorf("one urgent agent: %q", got)
	}
	if got := UrgentPlace("api", 2); got != "first in api" {
		t.Errorf("several urgent agents: %q", got)
	}
	for status, want := range map[string]string{"blocked": "needs you", "done": "done", "working": "working", "idle": "idle", "": "open", "unknown": "open"} {
		if got := AgentWord(status); got != want {
			t.Errorf("AgentWord(%q) = %q, want %q", status, got, want)
		}
	}
}

// notices is every kind of Notice with the arguments of the examples of
// 5a.14.4, the machine macmini.
func notices() map[string][]string {
	n := map[string][]string{
		"copied":           {"1204"},
		"unbound":          {"z"},
		"no-answer":        {machine},
		"still-connecting": {machine},
		"refused":          {machine, "no such session"},
		"keys-dropped":     nil,
		"restarted":        {machine},
		"closed-idle":      {machine},
		"self":             {machine},
		"older-server":     nil,
		"bad-frame":        {machine},
	}
	for _, r := range reasons {
		args := []string{machine, r}
		if r == "protocol" {
			args = append(args, "HTTP/1.1 400")
		}
		n["machine "+r] = args
	}
	return n
}

func notice(kind string, args []string) string {
	return Notice(strings.Fields(kind)[0], args...)
}

func TestEveryNoticeFitsFiftyCells(t *testing.T) {
	for kind, args := range notices() {
		got := notice(kind, args)
		if got == "" {
			t.Errorf("notice %s is empty", kind)
		}
		if w := Width(got); w > 50 {
			t.Errorf("notice %s is %d cells, at most 50: %q", kind, w, got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("notice %s has two lines: %q", kind, got)
		}
	}
	for kind, want := range map[string]string{
		"copied":           "Copied 1,204 characters",
		"unbound":          "No binding for z",
		"no-answer":        "macmini did not answer",
		"still-connecting": "still connecting to macmini",
		"machine auth":     "macmini ssh could not log in.",
		"machine lost":     "macmini not reachable.",
		"refused":          "macmini: no such session",
		"keys-dropped":     "Some keys typed during the switch were dropped",
		"restarted":        "macmini restarted. Its shells are new.",
		"closed-idle":      "macmini closed after 10 min idle",
		"self":             "macmini is this machine and is left out",
		"older-server":     "Server is older. Run devtools server restart.",
		"bad-frame":        "macmini sent an unreadable frame. Reconnecting.",
	} {
		if got := notice(kind, notices()[kind]); got != want {
			t.Errorf("notice %s = %q, want %q", kind, got, want)
		}
	}
	if got := Notice("copied", "1"); got != "Copied 1 character" {
		t.Errorf("one character copied: %q", got)
	}
}

// messages is every kind of Message with the arguments of a mockup.
func messages() map[string][]string {
	return map[string][]string{
		"noserver":    nil,
		"nested":      {"devtools", "desktop"},
		"othermux":    {"devtools", "tmux"},
		"unknown":     nil,
		"notty":       {"attach"},
		"generation":  {"7"},
		"restart":     {"7", "3"},
		"no-client":   nil,
		"opened-only": {"devtools-main"},
		"unsupported": {"tmux", "agent-status"},
		"mismatch":    {"tmux", "devtools"},
		"unavailable": {"herdr"},
		"ambiguous":   {"tmux", "herdr"},
	}
}

var (
	emptyKinds = map[string]string{
		"projects": "", "sessions": "", "agents": "", "machine": machine, "query": "api",
		"name": "", "loading": "", "loading-sessions": "",
	}
	busyKinds = map[string][]string{
		"open":        {"api"},
		"open-remote": {"api", machine},
		"create":      {"login", "api"},
		"merge":       {"login"},
	}
)

// panics reports whether f panicked.
func panics(f func()) (did bool) {
	defer func() { did = recover() != nil }()
	f()
	return false
}

func TestEveryKindHasWords(t *testing.T) {
	for kind, args := range messages() {
		if got := Message(kind, args...); got == "" {
			t.Errorf("Message(%q) is empty", kind)
		}
	}
	for kind, args := range notices() {
		if got := notice(kind, args); got == "" {
			t.Errorf("Notice(%q) is empty", kind)
		}
	}
	for kind, arg := range emptyKinds {
		if got := Empty(kind, arg); got == "" {
			t.Errorf("Empty(%q) is empty", kind)
		}
	}
	for kind, args := range busyKinds {
		if got := Busy(kind, args...); got == "" {
			t.Errorf("Busy(%q) is empty", kind)
		}
	}

	// unsupported with each of the six capabilities and each backend; the
	// names are written out (internal/ui imports no devtools package) and
	// TestCapsTableHasSixCapabilities pins the same six in internal/mux.
	for _, b := range backends {
		for _, c := range capabilities {
			got := Message("unsupported", b, c)
			if !strings.HasPrefix(got, b+" cannot ") {
				t.Errorf("unsupported %s %s does not name the backend first: %q", b, c, got)
			}
			if strings.Contains(got, c) && c != "layout" && c != "window" && c != "machines" {
				t.Errorf("unsupported %s %s names the capability's identifier: %q", b, c, got)
			}
			hasSecond := strings.HasSuffix(got, ". A devtools window can.")
			if hasSecond != (c != "detached-open") {
				t.Errorf("unsupported %s %s: the sentence about a devtools window is %v: %q", b, c, hasSecond, got)
			}
		}
	}
	if !panics(func() { Message("unsupported", "tmux", "teleport") }) {
		t.Error("an unknown capability does not panic")
	}
	if !panics(func() { Message("unsupported", "tmux") }) {
		t.Error("unsupported without a capability does not panic")
	}
	if !panics(func() { Message("noserver", "herdr") }) {
		t.Error("noserver with another word than tmux does not panic")
	}
	for name, f := range map[string]func(){
		"Message": func() { Message("nonsense") },
		"Notice":  func() { Notice("nonsense") },
		"Empty":   func() { Empty("nonsense", "") },
		"Busy":    func() { Busy("nonsense") },
	} {
		if !panics(f) {
			t.Errorf("%s with an unknown kind does not panic", name)
		}
	}
}

func TestMessagesBeforeStart(t *testing.T) {
	cases := []struct {
		id   string
		kind string
		args []string
	}{
		{"message-noserver", "noserver", nil},
		{"message-nested", "nested", []string{"devtools", "desktop"}},
		{"message-othermux", "othermux", []string{"devtools", "tmux"}},
		{"message-unknown", "unknown", nil},
		{"message-notty", "notty", []string{"attach"}},
		{"message-generation", "generation", []string{"7"}},
		{"message-restart", "restart", []string{"7", "3"}},
		{"message-noclient", "no-client", nil},
		{"message-openedonly", "opened-only", []string{"devtools-main"}},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			assertGolden(t, c.id, Message(c.kind, c.args...))
		})
	}
}

func TestMessagesOfTheBackends(t *testing.T) {
	cases := []struct {
		id   string
		kind string
		args []string
	}{
		{"message-noserver-tmux", "noserver", []string{"tmux"}},
		{"message-nested-tmux", "nested", []string{"tmux", "desktop"}},
		{"message-othermux-tmux", "othermux", []string{"tmux", "screen"}},
		{"message-unsupported-machines", "unsupported", []string{"tmux", "machines"}},
		{"message-unsupported-agent-status", "unsupported", []string{"tmux", "agent-status"}},
		{"message-unsupported-window", "unsupported", []string{"tmux", "window"}},
		{"message-unsupported-pane-focus", "unsupported", []string{"herdr", "pane-focus"}},
		{"message-unsupported-layout", "unsupported", []string{"tmux", "layout"}},
		{"message-unsupported-detached-open", "unsupported", []string{"herdr", "detached-open"}},
		{"message-mismatch", "mismatch", []string{"tmux", "devtools"}},
		{"message-unavailable", "unavailable", []string{"herdr"}},
		{"message-ambiguous", "ambiguous", []string{"tmux", "herdr"}},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			assertGolden(t, c.id, Message(c.kind, c.args...))
		})
	}
}

func TestBackendMessagesFitEightyCells(t *testing.T) {
	const host = "macbookpro"
	var all []string
	add := func(kind string, args ...string) { all = append(all, Message(kind, args...)) }
	add("noserver")
	add("noserver", "tmux")
	for _, b := range backends {
		add("unavailable", b)
		add("nested", b, host)
		for _, c := range capabilities {
			add("unsupported", b, c)
		}
		for _, where := range append([]string{"screen", "zellij"}, backends...) {
			if where != b {
				add("othermux", b, where)
			}
		}
		for _, other := range backends {
			if other != b {
				add("mismatch", b, other)
				add("ambiguous", b, other)
			}
		}
	}
	for _, m := range all {
		for _, line := range strings.Split(m, "\n") {
			if w := Width(line); w > 80 {
				t.Errorf("a line is %d cells, at most 80: %q", w, line)
			}
		}
	}
}

func TestNoServerWithoutTmuxHasTwoLines(t *testing.T) {
	got := Message("noserver")
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", "message-noserver.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got+"\n" != string(raw) {
		t.Errorf("Message(\"noserver\") is %q, the golden of revision 1 is %q", got, raw)
	}
	if n := strings.Count(got, "\n") + 1; n != 2 {
		t.Errorf("Message(\"noserver\") has %d lines, want 2", n)
	}
	if strings.Contains(got, "DEVTOOLS_MULTIPLEXER") {
		t.Error("without tmux the words name the tmux fallback")
	}
}

// everyText is every text words.go can produce, with the arguments of the
// tests above.
func everyText() []string {
	var all []string
	for kind, args := range messages() {
		all = append(all, Message(kind, args...))
	}
	for _, b := range backends {
		for _, c := range capabilities {
			all = append(all, Message("unsupported", b, c))
		}
		all = append(all, Message("nested", b, "desktop"), Message("unavailable", b))
	}
	all = append(all, Message("noserver", "tmux"), Message("notty"))
	for kind, args := range notices() {
		all = append(all, notice(kind, args))
	}
	for kind, arg := range emptyKinds {
		all = append(all, Empty(kind, arg))
	}
	for kind, args := range busyKinds {
		all = append(all, Busy(kind, args...))
	}
	for _, state := range []string{"off", "connecting", "online", "retrying", "attention"} {
		for _, r := range append([]string{""}, reasons...) {
			for _, retry := range []string{"", "retrying", "next-open", "in-30s"} {
				for _, listing := range []bool{false, true} {
					p, _ := MachinePhrase(machine, state, Problem{Reason: r, Text: "x", Retry: retry}, listing)
					all = append(all, p)
				}
			}
		}
	}
	for _, r := range reasons {
		all = append(all, ReasonWord(r))
	}
	for _, status := range []string{"blocked", "done", "working", "idle", ""} {
		all = append(all, AgentWord(status), RollupWord(status, 1), RollupWord(status, 2))
	}
	all = append(all, LocalPhrase("older"), LocalPhrase("stopped"), ListPhrase("cut"), ListPhrase("incomplete"))
	for _, scope := range []string{ScopeAll, ScopeSessions, ScopeAgents} {
		all = append(all, Placeholder(scope, false), scope)
	}
	all = append(all, Placeholder(ScopeAll, true))
	for _, h := range HintOrder() {
		all = append(all, HintLabel(h[0], h[1]))
	}
	row := CreateRow("login", "api", "main")
	all = append(all, row[0]+row[1]+row[2], MergeQuestion("login"), UrgentPlace("api", 2))
	for short := 0; short <= 2; short++ {
		all = append(all, ConfirmClose("vim", short), CopyPosition(12, 7412, short))
	}
	all = append(all, PaneKilled("vim", 9), PaneNotStarted("vim", "no such file"),
		ModePrefix, ModeSwitching, ModeCopy, ModeScrolled, SwitchingTo,
		Selecting, SelectingLines, SelectingBlock, AnyKeyReturns, FleetRetrying, FleetOff,
		HintCancel, HintMore, HintSelect, HintCopy, HintLeave,
		HubSwitch, HubCreate, HubJump, HubMerge, HubNewFeature, HubMergeMain, HubClose, HubClear, HubCancel,
		BannerPaused, CreateNotHere, MergeNotHere, ConfirmClosePane, ConfirmCancel, PressEnter,
		TooSmall, TooSmallNeed)
	return all
}

func TestWordsSayNeitherWorkspaceNorHostNorPeer(t *testing.T) {
	// "host key" is ssh's name for the thing and is allowed (5a.14.2).
	host := regexp.MustCompile(`\bhost\b(?: key\b)?`)
	for _, text := range everyText() {
		low := strings.ToLower(text)
		for _, word := range []string{"workspace", "peer", "please", "sorry", "error:"} {
			if strings.Contains(low, word) {
				t.Errorf("%q says %q", text, word)
			}
		}
		for _, m := range host.FindAllString(low, -1) {
			if m != "host key" {
				t.Errorf("%q says host", text)
			}
		}
	}
}

func TestHintLabelsFollowTheOrderOfTheBindings(t *testing.T) {
	want := []string{"hub", "sessions", "agents", "new feature", "new tab", "next tab", "previous tab",
		"tab", "split right", "split down", "close pane", "copy", "session", "detach"}
	order := HintOrder()
	if len(order) != len(want) {
		t.Fatalf("HintOrder has %d hints, 5a.11.2 has %d", len(order), len(want))
	}
	for i, h := range order {
		if got := HintLabel(h[0], h[1]); got != want[i] {
			t.Errorf("hint %d (%s %s) is %q, want %q", i, h[0], h[1], got, want[i])
		}
	}
	if HintLabel("tab-select", "4") != "tab" || HintLabel("session-next", "") != "session" {
		t.Error("tab-select 4 and session-next do not share the label of their group")
	}
	if HintLabel("focus-left", "vim") != "" {
		t.Error("an action outside the strip has a label")
	}
	if TooSmallNeed != Number(MinCols)+" by "+Number(MinRows)+" needed" {
		t.Errorf("the too-small text %q does not say the minimum size", TooSmallNeed)
	}
}
