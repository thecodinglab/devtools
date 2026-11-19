package hub

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"devtools/internal/client/chrome"
	"devtools/internal/session"
	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// every is the features of the overlay of a devtools window.
var every = Features{Agents: true, Machines: true}

func newHub(t *testing.T, o ModelOptions) *Model {
	t.Helper()
	if o.Actions == nil {
		o.Actions = &fakeBackend{}
	}
	m := New(o)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func feed(m *Model, events ...Event) {
	for _, ev := range events {
		m.Update(eventMsg{ev: ev})
	}
}

func hit(m *Model, keys ...tea.KeyPressMsg) *chrome.Outcome {
	var out *chrome.Outcome
	for _, k := range keys {
		_, out = m.Update(k)
	}
	return out
}

func typeInto(m *Model, text string) {
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func screenText(m *Model) string { return ansi.Strip(m.View(m.width, m.height)) }

// names lists the rows: "@machine" for a heading, "#project" for a project
// heading, "~text" for a note, "machine:label" for an item of another
// machine, a trailing "!" for a stale item.
func names(m *Model) []string {
	var out []string
	for _, r := range m.rows {
		switch r.kind {
		case rowSpace:
			continue
		case rowHost:
			out = append(out, "@"+r.project)
		case rowHeader:
			out = append(out, "#"+r.project)
		case rowNote:
			out = append(out, "~"+r.project)
		default:
			item := m.items[r.item]
			name := item.Label
			if item.Kind == KindAgent {
				name = item.Session + "/" + item.Pane
			}
			if item.Machine != "" {
				name = item.Machine + ":" + name
			}
			if item.Stale {
				name += "!"
			}
			out = append(out, name)
		}
	}
	return out
}

func selectedName(m *Model) string {
	if m.onCreate() {
		return "<create>"
	}
	item, ok := m.selected()
	if !ok {
		return "<none>"
	}
	if item.Kind == KindAgent {
		return item.Machine + ":" + item.Pane
	}
	return item.Machine + ":" + item.Label
}

// selectWhere moves the cursor to the first selectable row whose item
// satisfies ok.
func selectWhere(t *testing.T, m *Model, ok func(Item) bool) {
	t.Helper()
	for i, r := range m.selectable() {
		if ok(m.items[m.rows[r].item]) {
			m.cursor = i
			m.touched = true
			return
		}
	}
	t.Fatalf("no selectable row for the test in %v", names(m))
}

func on(machine string, items ...Item) Event {
	return Event{Machine: machine, Kind: Items, Items: items}
}

func agentItem(session, sessionID, tab, pane, status string, tabIndex int) Item {
	return Item{Kind: KindAgent, Session: session, SessionID: sessionID, Tab: tab, Pane: pane, Status: status, TabIndex: tabIndex, Agent: "claude"}
}

func TestHeadingsShowAtOnceInConfigOrder(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m,
		Event{Kind: Online},
		Event{Machine: "macmini", Kind: Connecting},
		Event{Machine: "macbook", Kind: Connecting},
	)
	if got := strings.Join(names(m), " "); got != "@macmini @macbook" {
		t.Fatalf("rows = %s, want the two headings in config order before any row", got)
	}
	view := screenText(m)
	chipMini, _ := m.theme.Chip("macmini")
	chipBook, _ := m.theme.Chip("macbook")
	mini, book := strings.Index(view, chipMini+"  connecting…"), strings.Index(view, chipBook+"  connecting…")
	if mini < 0 || book < 0 || mini > book {
		t.Fatalf("view lacks the headings in order:\n%s", view)
	}
	if strings.Contains(view, "Loading") {
		t.Fatalf("the first frame says something about loading:\n%s", view)
	}
}

func TestRowsAppearAsEventsArrive(t *testing.T) {
	events := make(chan Event, 8)
	m := newHub(t, ModelOptions{Features: every, Events: events})
	next := func(cmd tea.Cmd) tea.Cmd {
		t.Helper()
		msg := cmd()
		if msg == nil {
			return nil
		}
		cmd, _ = m.Update(msg)
		return cmd
	}
	cmd := m.waitEvent()
	events <- Event{Machine: "macmini", Kind: Connecting}
	cmd = next(cmd)
	events <- on("", worktree("devtools", "main"))
	cmd = next(cmd)
	if got := strings.Join(names(m), " "); got != "devtools/main @macmini" {
		t.Fatalf("rows = %s after this machine's first row", got)
	}
	events <- Event{Machine: "macmini", Kind: Online}
	cmd = next(cmd)
	events <- on("macmini", worktree("blog", "main"))
	cmd = next(cmd)
	events <- on("", worktree("xcloud", "main"))
	cmd = next(cmd)
	if got := strings.Join(names(m), " "); got != "devtools/main xcloud/main @macmini macmini:blog/main" {
		t.Fatalf("rows = %s, want each machine's rows in its own section", got)
	}
	close(events)
	if cmd = next(cmd); cmd != nil {
		t.Fatal("the hub waits on a closed channel")
	}
}

func TestRowsShowBeforeTheirGitStatus(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m, on("", worktree("devtools", "main"), worktree("xcloud", "main")))
	before := names(m)
	if view := screenText(m); !strings.Contains(view, "xcloud") || strings.Contains(view, "changed") {
		t.Fatalf("view = %q, want the rows without their git state", view)
	}
	feed(m, Event{Kind: Git, Path: "/dev/xcloud/main", Git: ui.Git{Changes: 3}})
	if view := screenText(m); !strings.Contains(view, "3 changed") {
		t.Fatalf("view = %q, want the changes of xcloud/main", view)
	}
	if after := names(m); !slices.Equal(before, after) {
		t.Fatalf("rows moved when the git state arrived: %v, then %v", before, after)
	}
}

func TestCompleteRemovesRowsTheScanNoLongerReports(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m,
		on("", worktree("devtools", "main"), worktree("devtools", "old-feature")),
		Event{Kind: Complete},
		on("", worktree("devtools", "main")),
	)
	if got := strings.Join(names(m), " "); got != "devtools/main devtools/old-feature" {
		t.Fatalf("rows = %s, a row went before the scan ended", got)
	}
	feed(m, Event{Kind: Complete})
	if got := strings.Join(names(m), " "); got != "devtools/main" {
		t.Fatalf("rows = %s, want the row the scan no longer reported gone", got)
	}
}

func TestCursorStaysOnItsRowWhenRowsArriveAbove(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every, Here: session.Target{Dir: "/dev/xcloud/main"}})
	feed(m, on("", worktree("xcloud", "main")), on("macmini", worktree("blog", "main"), worktree("blog", "draft")))
	m.Update(down)
	m.Update(down)
	if got := selectedName(m); got != "macmini:blog/draft" {
		t.Fatalf("selected = %s", got)
	}
	feed(m, on("", worktree("devtools", "main"), worktree("devtools", "fix")), on("macmini", worktree("aaa", "main")))
	if got := selectedName(m); got != "macmini:blog/draft" {
		t.Fatalf("selected = %s after rows arrived above, want macmini:blog/draft", got)
	}
}

func TestAFailedMachineShowsReasonAndRetryState(t *testing.T) {
	for _, c := range []struct {
		problem Problem
		state   string
		heading string
	}{
		{Problem{Reason: "unreachable", Retry: "retrying"}, "retrying", "not reachable. Retrying."},
		{Problem{Reason: "unreachable", Retry: "next-open"}, "off", "not reachable. Opening the hub again retries."},
		{Problem{Reason: "lost", Retry: "retrying"}, "retrying", "not reachable. Retrying."},
		{Problem{Reason: "auth", Retry: "in-30s"}, "attention", "✗ ssh could not log in. Run ssh macmini once in a shell."},
		{Problem{Reason: "host-key", Retry: "in-30s"}, "attention", "✗ host key not accepted. Run ssh macmini once and check it."},
		{Problem{Reason: "no-devtools", Retry: "in-30s"}, "attention", "✗ devtools is not installed there."},
		{Problem{Reason: "no-server", Retry: "in-30s"}, "attention", "✗ the devtools server is not running there."},
		{Problem{Reason: "incompatible", Retry: "in-30s"}, "attention", "✗ incompatible. Update devtools on macmini."},
		{Problem{Reason: "protocol", Text: "bash: devtools: Permission denied", Retry: "retrying"}, "retrying", "✗ unexpected answer: bash: devtools: Permission denied"},
	} {
		t.Run(c.problem.Reason+"-"+c.problem.Retry, func(t *testing.T) {
			m := newHub(t, ModelOptions{Features: every})
			feed(m, on("macmini", worktree("blog", "main")), Event{Machine: "macmini", Kind: Failed, Err: c.problem})
			// the chip of the theme, then the phrase (5a.14.2)
			chip, _ := m.theme.Chip("macmini")
			if heading := " " + chip + "  " + c.heading; !strings.Contains(screenText(m), heading) {
				t.Fatalf("view lacks %q:\n%s", heading, screenText(m))
			}
			views := m.machineViews()
			if len(views) != 2 || views[1].Name != "macmini" || views[1].State != c.state || views[1].Problem != c.problem {
				t.Fatalf("machines = %+v, want macmini %s with %+v", views, c.state, c.problem)
			}
			if got := strings.Join(names(m), " "); got != "@macmini macmini:blog/main!" {
				t.Fatalf("rows = %s, want the machine's row stale under its heading", got)
			}
		})
	}
}

func TestAFailedMachineStaysVisibleUnderAnyFilter(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m,
		on("", worktree("devtools", "main")),
		on("macbook", worktree("blog", "main")),
		Event{Machine: "macmini", Kind: Failed, Err: Problem{Reason: "unreachable", Retry: "retrying"}},
	)
	for _, query := range []string{"devtools", "zzzz", "blog"} {
		m.Update(esc)
		typeInto(m, query)
		got := names(m)
		if !slices.Contains(got, "@macmini") {
			t.Fatalf("%s: rows = %v, want the failed machine's heading", query, got)
		}
		if query != "blog" && slices.Contains(got, "@macbook") {
			t.Fatalf("%s: rows = %v, the heading of an online machine without a match stayed", query, got)
		}
	}
}

func TestStaleRowsCannotBePicked(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m,
		on("macmini", worktree("blog", "main"), worktree("blog", "draft")),
		Event{Machine: "macmini", Kind: Agents, Items: []Item{agentItem("blog-main", "s1", "t1", "p1", "blocked", 1)}},
	)
	if len(m.selectable()) != 2 {
		t.Fatalf("selectable = %v while macmini is online", names(m))
	}
	feed(m, Event{Machine: "macmini", Kind: Failed, Err: Problem{Reason: "lost", Retry: "retrying"}})
	if n := len(m.selectable()); n != 0 {
		t.Fatalf("%d stale rows can be selected: %v", n, names(m))
	}
	for _, item := range m.visible() {
		if !item.Stale {
			t.Fatalf("row %s of a lost machine is not stale", item.Label)
		}
	}
	m.Update(down)
	if _, out := m.Update(enter); out != nil || m.Picked() != nil {
		t.Fatalf("enter on stale rows picked %+v", m.Picked())
	}
	m.Update(tab)
	m.Update(tab)
	if m.currentScope() != ui.ScopeAgents || len(m.visible()) != 1 || len(m.selectable()) != 0 {
		t.Fatalf("agents scope: rows %v, %d selectable, want the stale agent unselectable", names(m), len(m.selectable()))
	}
	feed(m, Event{Machine: "macmini", Kind: Online})
	if len(m.selectable()) != 1 {
		t.Fatalf("the rows stayed stale after the machine came back: %v", names(m))
	}
}

func TestSessionsJoinRowsByDirectory(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	main := worktree("devtools", "main")
	feed(m,
		on("", main),
		Event{Kind: Sessions, Items: []Item{
			// the same directory under another label joins the row
			{Label: "renamed", Session: "renamed", SessionID: "s1", Path: "/dev/devtools/main/", State: ui.Session{Agent: "done"}},
			// the label of the row in another directory does not
			{Label: "devtools-main", Session: "devtools-main", SessionID: "s2", Path: "/elsewhere"},
			{Label: "scratch", Session: "scratch", SessionID: "s3", Path: "/tmp/scratch"},
		}},
	)
	if got := strings.Join(names(m), " "); got != "devtools/main devtools-main scratch" {
		t.Fatalf("rows = %s, want the catalog row joined with s1 and the other two on their own", got)
	}
	item, _ := m.selected()
	if item.SessionID != "s1" || !item.State.Open || item.State.Agent != "done" {
		t.Fatalf("joined row = %+v, want session s1, open, done", item)
	}
	if _, out := m.Update(enter); out == nil || *out.Switch != (session.Target{Session: "s1"}) {
		t.Fatalf("pick = %+v, want the session s1", out)
	}
}

func TestAgentsModeListsEveryMachineMostUrgentFirst(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every, Mode: ModeAgents})
	feed(m,
		Event{Kind: Agents, Items: []Item{
			agentItem("b-main", "s1", "t1", "p1", "idle", 1),
			agentItem("a-main", "s2", "t1", "p2", "working", 1),
			agentItem("z-main", "s3", "t2", "p5", "blocked", 2),
		}},
		Event{Machine: "macmini", Kind: Agents, Items: []Item{
			agentItem("a-main", "s1", "t1", "p3", "blocked", 1),
			agentItem("blog-main", "s2", "t1", "p4", "done", 1),
			agentItem("a-main", "s1", "t1", "p0", "blocked", 1),
		}},
		Event{Machine: "macbook", Kind: Connecting},
	)
	want := "z-main/p5 macmini:a-main/p0 macmini:a-main/p3 macmini:blog-main/p4 a-main/p2 b-main/p1 @macbook"
	if got := strings.Join(names(m), " "); got != want {
		t.Fatalf("rows =\n  %s\nwant\n  %s", got, want)
	}
	if got := selectedName(m); got != ":p5" {
		t.Fatalf("selected = %s, want the most urgent agent", got)
	}
	view := screenText(m)
	for _, text := range []string{"Jump to an agent", "◆ needs you", "2 claude", "macmini", "⏎ jump to pane", "tab all"} {
		if !strings.Contains(view, text) {
			t.Fatalf("view lacks %q:\n%s", text, view)
		}
	}
}

func TestPickingAnAgentReturnsItsPane(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every, Mode: ModeAgents})
	feed(m, Event{Machine: "macmini", Kind: Agents, Items: []Item{agentItem("blog-main", "s3", "t2", "p5", "blocked", 2)}})
	_, out := m.Update(enter)
	want := session.Target{Machine: "macmini", Session: "s3", Tab: "t2", Pane: "p5"}
	if out == nil || out.Switch == nil || *out.Switch != want {
		t.Fatalf("outcome = %+v, want %+v", out, want)
	}
	if p := m.Picked(); p == nil || p.Target != want || p.Item.Pane != "p5" || p.Item.Kind != KindAgent {
		t.Fatalf("picked = %+v", p)
	}
}

func TestPickReturnsTheTargetOfTheRow(t *testing.T) {
	setup := func(t *testing.T) *Model {
		m := newHub(t, ModelOptions{Features: every})
		feed(m,
			on("macmini", worktree("blog", "main"), worktree("blog", "draft")),
			Event{Machine: "macmini", Kind: Sessions, Items: []Item{
				{Label: "blog-main", Session: "blog-main", SessionID: "s1", Path: "/dev/blog/main"},
				{Label: "scratch", Session: "scratch", SessionID: "s2", Path: "/tmp/scratch"},
			}},
			Event{Machine: "macmini", Kind: Agents, Items: []Item{agentItem("scratch", "s2", "t4", "p9", "working", 3)}},
		)
		return m
	}
	for _, c := range []struct {
		name  string
		agent bool
		which func(Item) bool
		want  session.Target
	}{
		{"a catalog row with an open session", false, func(i Item) bool { return i.Path == "/dev/blog/main" }, session.Target{Machine: "macmini", Session: "s1"}},
		{"a catalog row without a session", false, func(i Item) bool { return i.Path == "/dev/blog/draft" }, session.Target{Machine: "macmini", Dir: "/dev/blog/draft", Name: "blog-draft"}},
		{"a session row", false, func(i Item) bool { return i.Kind == KindSession }, session.Target{Machine: "macmini", Session: "s2"}},
		{"an agent row", true, func(i Item) bool { return i.Kind == KindAgent }, session.Target{Machine: "macmini", Session: "s2", Tab: "t4", Pane: "p9"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := setup(t)
			if c.agent {
				m.Update(shiftTab)
			}
			selectWhere(t, m, c.which)
			_, out := m.Update(enter)
			if out == nil || out.Switch == nil || *out.Switch != c.want {
				t.Fatalf("outcome = %+v, want %+v", out, c.want)
			}
		})
	}
}

func TestRemoteRowsOfferNoLocalActions(t *testing.T) {
	actions := &fakeBackend{}
	m := newHub(t, ModelOptions{Features: every, Actions: actions, Here: session.Target{Dir: "/dev/devtools/main"}})
	feed(m, on("", worktree("devtools", "main")), on("macmini", worktree("kakeibo", "main"), worktree("kakeibo", "budget")))
	selectWhere(t, m, func(i Item) bool { return i.Machine == "macmini" && i.Worktree == "budget" })
	if footer := ansi.Strip(m.footer(m.width)); strings.Contains(footer, "merge into main") {
		t.Fatalf("footer = %q, offers a merge over there", footer)
	}
	m.Update(ctrlG)
	if m.confirm != nil || m.message != ui.MergeNotHere {
		t.Fatalf("ctrl+g: confirm = %+v, message = %q", m.confirm, m.message)
	}
	m.Update(ctrlN)
	if m.busy != "" || m.message != ui.CreateNotHere {
		t.Fatalf("ctrl+n: busy = %q, message = %q", m.busy, m.message)
	}
	if !strings.Contains(screenText(m), ui.CreateNotHere) {
		t.Fatalf("the footer does not say why:\n%s", screenText(m))
	}
	typeInto(m, "kakeibo/fresh")
	if m.create != nil || !strings.Contains(m.noCreate, "on macmini") {
		t.Fatalf("create = %+v, noCreate = %q", m.create, m.noCreate)
	}
	if len(actions.created) != 0 || len(actions.merged) != 0 {
		t.Fatalf("actions ran: %+v %+v", actions.created, actions.merged)
	}
}

func TestTypingAMachineNameFiltersToIt(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m,
		on("", worktree("devtools", "main")),
		on("macmini", worktree("kakeibo", "main"), worktree("kakeibo", "budget")),
		on("macbook", worktree("blog", "main")),
	)
	typeInto(m, "macmini")
	if got := strings.Join(names(m), " "); got != "@macmini macmini:kakeibo/main macmini:kakeibo/budget" {
		t.Fatalf("rows = %s, want what is on macmini only", got)
	}
}

func TestViewFitsTheGivenSize(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every, Here: session.Target{Dir: "/dev/devtools/main"}})
	feed(m,
		on("", worktree("devtools", "main"), worktree("devtools", "a-rather-long-feature-name-that-will-not-fit"), worktree("xcloud", "main")),
		Event{Kind: Sessions, Items: []Item{{Label: "devtools-main", Session: "devtools-main", SessionID: "s1", Path: "/dev/devtools/main", State: ui.Session{Agent: "blocked", Title: "Fix the hub footer with a title long enough to be cut"}}}},
		Event{Kind: Agents, Items: []Item{agentItem("devtools-main", "s1", "t1", "p1", "blocked", 2)}},
		on("macmini", worktree("blog", "main")),
		Event{Machine: "macbook", Kind: Failed, Err: Problem{Reason: "auth", Retry: "in-30s"}},
	)
	for _, scope := range []string{"all", "sessions", "agents"} {
		for _, size := range [][2]int{{20, 5}, {40, 10}, {80, 24}, {200, 50}} {
			w, h := size[0], size[1]
			lines := strings.Split(m.View(w, h), "\n")
			if len(lines) != h {
				t.Fatalf("%s %dx%d: %d lines", scope, w, h, len(lines))
			}
			for _, line := range lines {
				if got := ansi.StringWidth(line); got > w {
					t.Fatalf("%s %dx%d: line %q is %d wide", scope, w, h, ansi.Strip(line), got)
				}
			}
		}
		m.Update(tab)
	}
	// at 200 the column is 100 wide and centred
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	first := strings.Split(ansi.Strip(m.View(200, 50)), "\n")[0]
	if !strings.HasPrefix(first, strings.Repeat(" ", 50)+"  "+"›") {
		t.Fatalf("row 0 at 200 = %q, want the column at 50", first)
	}
	if x, y, ok := m.Cursor(); !ok || y != 0 || x != 54 {
		t.Fatalf("cursor = %d,%d,%v, want 54,0 in the input", x, y, ok)
	}
}

func TestTheModelStartsNoProcess(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == "os/exec" || path == "syscall" {
				t.Errorf("%s imports %s: the hub starts no process", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source file checked")
	}
}

func TestSelectionFollowsTheItemNotTheIndex(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m, on("", worktree("aaa", "main"), worktree("devtools", "main"), worktree("xcloud", "main")))
	m.Update(down)
	if got := selectedName(m); got != ":devtools/main" {
		t.Fatalf("selected = %s", got)
	}
	// the scan no longer has aaa: the row above goes, the index of
	// devtools/main changes, the selection does not
	feed(m, Event{Kind: Complete}, on("", worktree("devtools", "main"), worktree("xcloud", "main")), Event{Kind: Complete})
	if got := selectedName(m); got != ":devtools/main" {
		t.Fatalf("selected = %s after the row above went, want devtools/main", got)
	}
	// sessions and agents arriving do not move it either
	feed(m,
		Event{Kind: Sessions, Items: []Item{{Label: "x", Session: "x", SessionID: "s1", Path: "/dev/xcloud/main"}}},
		Event{Kind: Agents, Items: []Item{agentItem("x", "s1", "t1", "p1", "working", 1)}},
	)
	if got := selectedName(m); got != ":devtools/main" {
		t.Fatalf("selected = %s after sessions arrived", got)
	}
}

func TestAThemeMessageRedrawsWithTheNewTheme(t *testing.T) {
	utf8, _ := ui.NewTheme(ui.ThemeOptions{UTF8: true})
	ascii, _ := ui.NewTheme(ui.ThemeOptions{UTF8: false})
	m := newHub(t, ModelOptions{Features: every, Theme: utf8, Here: session.Target{Dir: "/dev/devtools/main"}})
	feed(m, on("", worktree("devtools", "main")))
	before := screenText(m)
	if !strings.Contains(before, "› ") || !strings.Contains(before, "▎") || !strings.Contains(before, "⏎ switch") {
		t.Fatalf("view in the UTF-8 set:\n%s", before)
	}
	m.Update(ui.ThemeMsg{Theme: ascii})
	after := screenText(m)
	if strings.Contains(after, "›") || !strings.Contains(after, "  > ") || !strings.Contains(after, "enter switch") || !strings.Contains(after, "|") {
		t.Fatalf("view after the theme changed to the ASCII set:\n%s", after)
	}
}

func TestScopeAgentsIsAbsentWithoutTheFeature(t *testing.T) {
	m := newHub(t, ModelOptions{Features: Features{}, Mode: ModeAgents})
	if m.currentScope() != ui.ScopeAll {
		t.Fatalf("ModeAgents opened the scope %s without the feature, want all", m.currentScope())
	}
	if got := strings.Join(m.scopeNames(), " "); got != "all sessions" {
		t.Fatalf("scopes = %s, want all and sessions", got)
	}
	feed(m,
		on("", worktree("devtools", "main")),
		Event{Kind: Agents, Items: []Item{agentItem("devtools-main", "s1", "t1", "p1", "blocked", 1)}},
		Event{Kind: Sessions, Items: []Item{{Label: "devtools-main", Session: "devtools-main", SessionID: "s1", Path: "/dev/devtools/main", State: ui.Session{Agent: "blocked", Title: "Approve rm?"}}}},
	)
	for _, item := range m.items {
		if item.Kind == KindAgent {
			t.Fatalf("an agent row exists without the feature: %+v", item)
		}
	}
	view := screenText(m)
	first := strings.Split(view, "\n")[0]
	if !strings.HasSuffix(strings.TrimRight(first, " "), "all  sessions") || strings.Contains(view, "agents") {
		t.Fatalf("view shows the agents scope:\n%s", view)
	}
	if strings.Contains(view, "needs you") || strings.Contains(view, "Approve rm?") || !strings.Contains(view, "open") {
		t.Fatalf("view shows an agent state without the feature:\n%s", view)
	}
}

func TestTabCyclesTwoScopesWithoutAgents(t *testing.T) {
	m := newHub(t, ModelOptions{Features: Features{}})
	var got []string
	for range 3 {
		got = append(got, m.currentScope())
		m.Update(tab)
	}
	for range 3 {
		got = append(got, m.currentScope())
		m.Update(shiftTab)
	}
	if s := strings.Join(got, ","); s != "all,sessions,all,sessions,all,sessions" {
		t.Fatalf("scopes = %s, want all and sessions only", s)
	}
}

func TestNoHeadingsWithoutMachines(t *testing.T) {
	m := newHub(t, ModelOptions{Features: Features{Agents: true}})
	feed(m,
		Event{Kind: Connecting},
		Event{Kind: Online},
		on("", worktree("devtools", "main")),
		Event{Machine: "macmini", Kind: Connecting},
		on("macmini", worktree("blog", "main")),
		Event{Machine: "macbook", Kind: Failed, Err: Problem{Reason: "auth", Retry: "in-30s"}},
	)
	for _, r := range m.rows {
		if r.kind == rowHost {
			t.Fatalf("a machine heading without the feature: %v", names(m))
		}
	}
	if got := strings.Join(names(m), " "); got != "devtools/main" {
		t.Fatalf("rows = %s, want this machine's rows only", got)
	}
	if m.machineViews() != nil {
		t.Fatalf("machine states without the feature: %+v", m.machineViews())
	}
	view := screenText(m)
	for _, phrase := range []string{"macmini", "macbook", "connecting…", "listing…", "not connected.", "ssh could not log in"} {
		if strings.Contains(view, phrase) {
			t.Fatalf("view has the machine phrase %q:\n%s", phrase, view)
		}
	}
}

func TestTheHubCoversThePaneArea(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	if r := m.Bounds(120, 40); r.X != 0 || r.Y != 0 || r.W != 120 || r.H != 40 {
		t.Fatalf("bounds = %+v, want the whole area", r)
	}
	m.busy = ui.Busy("merge", "x")
	if _, _, ok := m.Cursor(); ok {
		t.Fatal("the cursor shows while the hub waits")
	}
}

func TestEscAndCtrlCCloseWithoutAChoice(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	typeInto(m, "dev")
	if out := hit(m, esc); out != nil || m.input.Value() != "" {
		t.Fatalf("first esc closed (%+v) or left %q", out, m.input.Value())
	}
	if out := hit(m, esc); out == nil || out.Switch != nil {
		t.Fatalf("second esc = %+v, want closed without a choice", out)
	}
	if out := hit(newHub(t, ModelOptions{}), tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); out == nil || out.Switch != nil {
		t.Fatalf("ctrl+c = %+v, want closed without a choice", out)
	}
}

func TestCreatingAFeaturePicksIt(t *testing.T) {
	actions := &fakeBackend{}
	m := newHub(t, ModelOptions{Features: every, Actions: actions, Here: session.Target{Dir: "/dev/devtools/main"}})
	feed(m, on("", worktree("devtools", "main")))
	typeInto(m, "fresh")
	cmd, out := m.Update(enter)
	if out != nil || cmd == nil || !strings.Contains(m.busy, "Creating fresh in devtools") {
		t.Fatalf("enter on the create row: out = %+v, busy = %q", out, m.busy)
	}
	_, out = m.Update(cmd())
	if out == nil || out.Switch == nil {
		t.Fatalf("outcome = %+v, want the new worktree picked", out)
	}
	if len(actions.created) != 1 || m.Picked() == nil || m.Picked().Item.Label != "devtools/fresh" {
		t.Fatalf("created = %+v, picked = %+v", actions.created, m.Picked())
	}
}

func TestNotesShowUnderTheirMachine(t *testing.T) {
	catalog := func(machine string) Event {
		return Event{Machine: machine, Kind: Note, Path: "/home/florian/work", Err: Problem{Reason: "catalog", Text: "permission denied"}}
	}
	for _, c := range []struct {
		name     string
		features Features
		machine  string
		notes    []Event
		want     []string // the lines, in order
	}{
		{"a catalog error on another machine", every, "macmini", []Event{catalog("macmini")},
			[]string{"/home/florian/work: permission denied"}},
		{"a catalog error on this machine", every, "", []Event{catalog("")},
			[]string{"/home/florian/work: permission denied"}},
		{"a catalog error on this machine without machine headings", Features{}, "", []Event{catalog("")},
			[]string{"/home/florian/work: permission denied"}},
		{"a cut catalog", every, "macmini", []Event{{Machine: "macmini", Kind: Note, Err: Problem{Reason: "cut", Text: "2000"}}},
			[]string{"list cut at 2,000 rows. Type to narrow it."}},
		{"a cut list of sessions", every, "macmini", []Event{{Machine: "macmini", Kind: Note, Err: Problem{Reason: "cut", Text: "500"}}},
			[]string{"list cut at 500 rows. Type to narrow it."}},
		{"an incomplete scan on this machine", Features{Agents: true}, "", []Event{{Kind: Note, Err: Problem{Reason: "incomplete"}}},
			[]string{"scan did not finish. Some worktrees may be missing."}},
		{"two notes, one of them twice", every, "macmini", []Event{
			catalog("macmini"),
			{Machine: "macmini", Kind: Note, Err: Problem{Reason: "incomplete"}},
			catalog("macmini"),
		}, []string{"/home/florian/work: permission denied", "scan did not finish. Some worktrees may be missing."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newHub(t, ModelOptions{Features: c.features})
			feed(m, Event{Machine: c.machine, Kind: Online}, on(c.machine, worktree("blog", "main")), Event{Machine: c.machine, Kind: Complete})
			before := m.machineViews()
			feed(m, c.notes...)

			// a note changes neither the state nor the rows
			heading := []string{}
			row := "blog/main"
			if c.machine != "" {
				heading, row = []string{"@" + c.machine}, c.machine+":blog/main"
			}
			var lines []string
			for _, l := range c.want {
				lines = append(lines, "~"+l)
			}
			want := strings.Join(slices.Concat(heading, lines, []string{row}), " ")
			if got := strings.Join(names(m), " "); got != want {
				t.Fatalf("rows = %s, want %s", got, want)
			}
			if after := m.machineViews(); c.features.Machines {
				i := len(after) - 1
				if after[i].State != before[i].State || after[i].Problem != before[i].Problem || after[i].Listing != before[i].Listing {
					t.Fatalf("the note changed the machine: %+v, was %+v", after[i], before[i])
				}
				if !slices.Equal(after[i].Notes, c.want) {
					t.Fatalf("notes = %q, want %q", after[i].Notes, c.want)
				}
			}
			if got := selectedName(m); got != c.machine+":blog/main" {
				t.Fatalf("selected = %s, a note took the cursor", got)
			}

			view := screenText(m)
			at := -1
			for _, l := range c.want {
				i := strings.Index(view, l)
				if i < 0 || i < at || strings.Count(view, l) != 1 {
					t.Fatalf("view lacks %q once and in order:\n%s", l, view)
				}
				at = i
			}
			if i := strings.Index(view, "main"); i < at {
				t.Fatalf("the notes are not above the machine's rows:\n%s", view)
			}
		})
	}
}

func TestNotesGoWhenTheMachineComesBack(t *testing.T) {
	m := newHub(t, ModelOptions{Features: every})
	feed(m,
		Event{Machine: "macmini", Kind: Online},
		Event{Machine: "macmini", Kind: Note, Err: Problem{Reason: "incomplete"}},
		Event{Machine: "macmini", Kind: Failed, Err: Problem{Reason: "lost", Retry: "retrying"}},
	)
	if got := strings.Join(names(m), " "); !strings.Contains(got, "~scan did not finish") {
		t.Fatalf("rows = %s, the note went with the link", got)
	}
	feed(m, Event{Machine: "macmini", Kind: Online})
	if got := strings.Join(names(m), " "); strings.Contains(got, "~") {
		t.Fatalf("rows = %s, the note of the last scan outlived a new link", got)
	}
}
