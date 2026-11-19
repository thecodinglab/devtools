package hub

import (
	"errors"
	"os"
	"strings"
	"testing"

	"devtools/internal/client/chrome"
	"devtools/internal/session"
	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestMain makes the tests hermetic (10.1): nothing of the developer's
// terminal, multiplexer or home reaches them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-hub-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_ACTIVE_PANE_CWD",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// fakeBackend is the hub.Actions of the tests: it records what was asked
// for and answers at once.
type fakeBackend struct {
	created  []CreateRequest
	merged   []Item
	mergeErr error
}

func (f *fakeBackend) Create(req CreateRequest) (Item, error) {
	f.created = append(f.created, req)
	return Item{Kind: KindWorktree, Project: req.Project, Worktree: req.Branch, Label: req.Project + "/" + req.Branch}, nil
}
func (f *fakeBackend) Merge(item Item) (Item, error) {
	f.merged = append(f.merged, item)
	if f.mergeErr != nil {
		return Item{}, f.mergeErr
	}
	return Item{Kind: KindWorktree, Project: item.Project, Worktree: "main", Label: item.Project + "/main"}, nil
}

func worktree(project, name string) Item {
	return Item{
		Kind:     KindWorktree,
		Project:  project,
		Worktree: name,
		Label:    project + "/" + name,
		Path:     "/dev/" + project + "/" + name,
		Root:     "/dev",
		Session:  project + "-" + name,
	}
}

func testItems() []Item {
	return []Item{
		worktree("devtools", "main"),
		worktree("devtools", "popup-redesign"),
		{Kind: KindRepo, Project: "systems", Worktree: "main", Label: "systems", Path: "/dev/systems", Session: "systems-main"},
		worktree("xcloud", "feature-refactoring"),
		worktree("xcloud", "main"),
	}
}

// newTestModel builds the hub with every feature, 100x20, and feeds it
// testItems as one complete scan of this machine.
func newTestModel(t *testing.T, o ModelOptions) (*Model, *fakeBackend) {
	t.Helper()
	return newModelOf(t, o, testItems())
}

// newModelOf is newTestModel with other items. With nil items it feeds
// nothing: the hub as it shows before the catalog of this machine answers.
func newModelOf(t *testing.T, o ModelOptions, items []Item) (*Model, *fakeBackend) {
	t.Helper()
	backend, _ := o.Actions.(*fakeBackend)
	if backend == nil {
		backend = &fakeBackend{}
		o.Actions = backend
	}
	o.Features = Features{Agents: true, Machines: true}
	m := New(o)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	if items != nil {
		feed(m, Event{Kind: Items, Items: items}, Event{Kind: Complete})
	}
	return m, backend
}

// at is the target of a pane in dir on this machine.
func at(dir string) session.Target { return session.Target{Dir: dir} }

func typeText(t *testing.T, m *Model, text string) {
	t.Helper()
	for _, r := range text {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func press(t *testing.T, m *Model, key tea.KeyPressMsg) (tea.Cmd, *chrome.Outcome) {
	t.Helper()
	return m.Update(key)
}

// run follows the commands a key started, feeding their messages back in,
// the way the host's loop would, until the hub closes (the Outcome it
// returns) or there is nothing more to do (nil).
func run(t *testing.T, m *Model, cmd tea.Cmd) *chrome.Outcome {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if out := run(t, m, c); out != nil {
					return out
				}
			}
			return nil
		}
		var out *chrome.Outcome
		cmd, out = m.Update(msg)
		if out != nil {
			return out
		}
	}
	return nil
}

// pickedLabel is the label of the row a closed hub picked, "" when it
// closed without a choice or is still open.
func pickedLabel(m *Model, out *chrome.Outcome) string {
	if out == nil || out.Switch == nil || m.Picked() == nil {
		return ""
	}
	return m.Picked().Item.Label
}

var (
	enter    = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc      = tea.KeyPressMsg{Code: tea.KeyEscape}
	tab      = tea.KeyPressMsg{Code: tea.KeyTab}
	shiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	up       = tea.KeyPressMsg{Code: tea.KeyUp}
	down     = tea.KeyPressMsg{Code: tea.KeyDown}
	ctrlN    = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	ctrlG    = tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
)

func selectedLabel(m *Model) string {
	if m.onCreate() {
		return "<create>"
	}
	item, ok := m.selectedItem()
	if !ok {
		return "<none>"
	}
	return item.Label
}

func TestCursorStartsWhereYouAre(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/xcloud/main/src")})
	if got := selectedLabel(m); got != "xcloud/main" {
		t.Fatalf("selected = %q, want xcloud/main", got)
	}
}

func TestRowsGroupWorktreesUnderTheirProject(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	var got []string
	for _, r := range m.rows {
		if r.kind == rowHeader {
			got = append(got, "# "+r.project)
		} else {
			got = append(got, m.items[r.item].Label)
		}
	}
	want := "devtools/main,devtools/popup-redesign,systems,xcloud/main,xcloud/feature-refactoring"
	if strings.Join(got, ",") != want {
		t.Fatalf("rows = %v, want %s", got, want)
	}
}

func TestQuerySelectsBestMatch(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	typeText(t, m, "xref")
	if got := selectedLabel(m); got != "xcloud/feature-refactoring" {
		t.Fatalf("selected = %q, want xcloud/feature-refactoring", got)
	}
	if m.create == nil || m.create.Branch != "xref" {
		t.Fatalf("create = %+v, want a create row for xref beside the matches", m.create)
	}
}

func TestUnmatchedQueryBecomesFeatureInProjectYouAreIn(t *testing.T) {
	m, backend := newTestModel(t, ModelOptions{Here: at("/dev/devtools/popup-redesign")})
	typeText(t, m, "  my cool feature ")
	if !m.onCreate() {
		t.Fatalf("selected = %q, want the create row", selectedLabel(m))
	}
	cmd, _ := press(t, m, enter)
	out := run(t, m, cmd)
	if out == nil {
		t.Fatalf("expected the switcher to quit after creating, message=%q", m.message)
	}
	want := CreateRequest{Root: "/dev", Project: "devtools", Branch: "my-cool-feature", From: "popup-redesign", FromPath: "/dev/devtools/popup-redesign"}
	if len(backend.created) != 1 || backend.created[0] != want {
		t.Fatalf("created = %+v, want %+v", backend.created, want)
	}
	if got := pickedLabel(m, out); got != "devtools/my-cool-feature" {
		t.Fatalf("picked = %q (%+v), want the new worktree", got, out)
	}
}

func TestProjectPrefixCreatesInThatProjectFromMain(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	typeText(t, m, "xcloud/fix/login")
	want := CreateRequest{Root: "/dev", Project: "xcloud", Branch: "fix/login", From: "main", FromPath: "/dev/xcloud/main"}
	if m.create == nil || *m.create != want {
		t.Fatalf("create = %+v, want %+v", m.create, want)
	}
}

func TestExistingWorktreeNameOffersNoCreateRow(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	typeText(t, m, "popup-redesign")
	if m.create != nil {
		t.Fatalf("create = %+v, want none for an existing worktree", m.create)
	}
	if got := selectedLabel(m); got != "devtools/popup-redesign" {
		t.Fatalf("selected = %q", got)
	}
}

func TestOutsideAProjectExplainsHowToStartAFeature(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/elsewhere")})
	typeText(t, m, "zzz")
	if m.create != nil {
		t.Fatalf("create = %+v, want none outside a project", m.create)
	}
	if !strings.Contains(m.noCreate, "project/name") {
		t.Fatalf("noCreate = %q, want a hint at project/name", m.noCreate)
	}
	press(t, m, ctrlN)
	if m.message != m.noCreate {
		t.Fatalf("message = %q, want the hint", m.message)
	}
}

// TestTabCyclesTheThreeScopes replaces TestTabShowsOnlyOpenWorkspacesAndOthers:
// tab no longer toggles "open only", it goes to the next scope, and
// shift+tab to the previous one instead of one row up (F25). The sessions
// scope lists what the old open-only list did.
func TestTabCyclesTheThreeScopes(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	// the open sessions: one in the directory of xcloud/main, one that
	// belongs to no worktree
	feed(m, Event{Kind: Sessions, Items: []Item{
		{Kind: KindSession, Label: "xcloud-main", Session: "xcloud-main", Path: "/dev/xcloud/main", State: ui.Session{Open: true, Agent: "working"}},
		{Kind: KindSession, Project: "scratch", Label: "scratch", Session: "scratch", SessionID: "w2", State: ui.Session{Open: true}},
	}})
	var scopes []string
	for i := range 4 {
		scopes = append(scopes, m.currentScope())
		if m.currentScope() == ui.ScopeSessions {
			var got []string
			for _, i := range m.selectable() {
				got = append(got, m.items[m.rows[i].item].Label)
			}
			if strings.Join(got, ",") != "xcloud/main,scratch" {
				t.Fatalf("open rows = %v, want xcloud/main and the unknown session scratch once", got)
			}
		}
		if i < 3 {
			press(t, m, tab)
		}
	}
	if got := strings.Join(scopes, ","); got != "all,sessions,agents,all" {
		t.Fatalf("tab went through %s, want all,sessions,agents,all", got)
	}
	scopes = scopes[:0]
	for i := range 4 {
		scopes = append(scopes, m.currentScope())
		if i < 3 {
			press(t, m, shiftTab)
		}
	}
	if got := strings.Join(scopes, ","); got != "all,agents,sessions,all" {
		t.Fatalf("shift+tab went through %s, want all,agents,sessions,all", got)
	}
}

func TestNewModeOffersCreateFirstAndShowsTheProject(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/xcloud/main"), Mode: ModeNew})
	for _, r := range m.rows {
		if r.kind == rowItem && m.items[r.item].Project != "xcloud" {
			t.Fatalf("new mode shows %s from another project", m.items[r.item].Label)
		}
	}
	typeText(t, m, "main")
	// an existing name has no create row, so enter would switch instead
	if m.create != nil {
		t.Fatalf("create = %+v for an existing worktree", m.create)
	}
	press(t, m, esc)
	typeText(t, m, "api")
	if !m.onCreate() {
		t.Fatalf("selected = %q, want the create row", selectedLabel(m))
	}
}

func TestEnterReturnsItemWhenCallerSwitches(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	press(t, m, down)
	cmd, out := press(t, m, enter)
	if out == nil {
		out = run(t, m, cmd)
	}
	if out == nil {
		t.Fatal("expected quit")
	}
	if got := pickedLabel(m, out); got != "devtools/popup-redesign" {
		t.Fatalf("picked = %q (%+v)", got, out)
	}
	if want := (session.Target{Dir: "/dev/devtools/popup-redesign", Name: "devtools-popup-redesign"}); *out.Switch != want {
		t.Fatalf("switch = %+v, want %+v", *out.Switch, want)
	}
}

func TestMergeAsksFirstThenLandsOnMain(t *testing.T) {
	m, backend := newTestModel(t, ModelOptions{Here: at("/dev/devtools/popup-redesign")})
	press(t, m, ctrlG)
	if m.confirm == nil || m.confirm.Label != "devtools/popup-redesign" {
		t.Fatalf("confirm = %+v", m.confirm)
	}
	if !strings.Contains(ansi.Strip(m.footer(m.width)), "Merge popup-redesign into main") {
		t.Fatalf("footer = %q", ansi.Strip(m.footer(m.width)))
	}
	cmd, _ := press(t, m, enter)
	out := run(t, m, cmd)
	if out == nil || len(backend.merged) != 1 {
		t.Fatalf("quit=%v merged=%+v", out != nil, backend.merged)
	}
	if got := pickedLabel(m, out); got != "devtools/main" {
		t.Fatalf("picked = %q (%+v), want main", got, out)
	}
}

func TestMergeFailureStaysOpenWithTheError(t *testing.T) {
	backend := &fakeBackend{mergeErr: errors.New("popup-redesign does not rebase cleanly onto main")}
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/popup-redesign"), Actions: backend})
	press(t, m, ctrlG)
	cmd, _ := press(t, m, enter)
	if out := run(t, m, cmd); out != nil {
		t.Fatal("quit after a failed merge")
	}
	if !m.failed || !strings.Contains(ansi.Strip(m.footer(m.width)), "does not rebase cleanly") {
		t.Fatalf("footer = %q", ansi.Strip(m.footer(m.width)))
	}
}

func TestMergeRefusesMainAndEscCancels(t *testing.T) {
	m, backend := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	press(t, m, ctrlG)
	if m.confirm != nil || m.message == "" {
		t.Fatalf("confirm=%+v message=%q, want a refusal for main", m.confirm, m.message)
	}
	press(t, m, down)
	press(t, m, ctrlG)
	press(t, m, esc)
	if m.confirm != nil || len(backend.merged) != 0 {
		t.Fatalf("esc did not cancel: confirm=%+v merged=%+v", m.confirm, backend.merged)
	}
}

func TestEscClearsQueryThenCloses(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	typeText(t, m, "dev")
	cmd, out := press(t, m, esc)
	if m.input.Value() != "" || cmd != nil || out != nil {
		t.Fatalf("first esc: value=%q cmd=%v outcome=%+v", m.input.Value(), cmd, out)
	}
	cmd, out = press(t, m, esc)
	if out == nil {
		out = run(t, m, cmd)
	}
	if out == nil || out.Switch != nil {
		t.Fatalf("second esc should close without a choice, outcome=%+v", out)
	}
}

func TestRowsShowHeadingWhenMainDoesNotMatch(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	typeText(t, m, "refac")
	if len(m.rows) != 2 || m.rows[0].kind != rowHeader || m.rows[0].project != "xcloud" || !m.rows[1].child {
		t.Fatalf("rows = %+v, want the xcloud heading over its matching feature", m.rows)
	}
}

func TestSelectedRowFillsTheWidth(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/popup-redesign")})
	for _, line := range strings.Split(m.View(m.width, m.height), "\n") {
		if strings.Contains(line, "popup-redesign") {
			if w := ansi.StringWidth(line); w != m.width {
				t.Fatalf("selected row is %d wide, want %d", w, m.width)
			}
			return
		}
	}
	t.Fatal("no row for popup-redesign")
}

func TestViewFitsSmallWindows(t *testing.T) {
	for _, size := range [][2]int{{20, 5}, {40, 8}, {120, 40}} {
		m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		typeText(t, m, "feature")
		lines := strings.Split(m.View(m.width, m.height), "\n")
		if len(lines) > size[1] {
			t.Fatalf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > size[0] {
				t.Fatalf("%dx%d: line %q is %d wide", size[0], size[1], ansi.Strip(line), w)
			}
		}
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		query, s string
		ok       bool
	}{
		{"xref", "xcloud/feature-refactoring", true},
		{"dev main", "devtools/main", true},
		{"zz", "devtools/main", false},
		{"", "anything", true},
	} {
		if _, _, ok := match(c.query, c.s); ok != c.ok {
			t.Errorf("match(%q, %q) = %v, want %v", c.query, c.s, ok, c.ok)
		}
	}
	// a word start beats a scattered match
	start, _, _ := match("main", "devtools/main")
	scattered, _, _ := match("main", "my-animation")
	if start <= scattered {
		t.Errorf("score(main in devtools/main)=%d <= score in my-animation=%d", start, scattered)
	}
}

func TestKeysWaitWhileAMergeRuns(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	m.busy = "Merging popup-redesign into main…"
	for _, key := range []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModCtrl}, esc, enter} {
		cmd, out := m.Update(key)
		if cmd != nil || out != nil {
			t.Fatalf("%s while busy started %v (outcome %+v), want it ignored", key, cmd, out)
		}
	}
}

func TestWorkspacesSharingALabelStayApart(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	feed(m, Event{Kind: Sessions, Items: []Item{
		{Kind: KindSession, Project: "scratch", Label: "scratch", Session: "scratch", SessionID: "w1", State: ui.Session{Open: true, Agent: "idle"}},
		{Kind: KindSession, Project: "scratch", Label: "scratch", Session: "scratch", SessionID: "w2", State: ui.Session{Open: true, Agent: "blocked"}},
	}})
	typeText(t, m, "scratch")
	var got []string
	for _, r := range m.selectable() {
		item := m.items[m.rows[r].item]
		got = append(got, item.SessionID+":"+m.session(item).Agent)
	}
	if strings.Join(got, ",") != "w1:idle,w2:blocked" {
		t.Fatalf("rows = %v, want both workspaces with their own state", got)
	}
}

func TestBestMatchWinsOverItsProjectsMain(t *testing.T) {
	m, _ := newModelOf(t, ModelOptions{}, []Item{worktree("application", "main"), worktree("application", "api")})
	typeText(t, m, "api")
	if got := selectedLabel(m); got != "application/api" {
		t.Fatalf("selected = %q, want application/api", got)
	}
}

func TestSameProjectUnderAnotherRootIsAnotherProject(t *testing.T) {
	one := worktree("widgets", "main")
	api := worktree("widgets", "api")
	two := worktree("widgets", "main")
	one.Root, api.Root, two.Root = "/one", "/one", "/two"
	one.Path, api.Path, two.Path = "/one/widgets/main", "/one/widgets/api", "/two/widgets/main"
	m, _ := newModelOf(t, ModelOptions{Here: at("/two/widgets/main"), Mode: ModeNew}, []Item{one, api, two})
	for _, r := range m.rows {
		if r.kind == rowItem && m.items[r.item].Root != "/two" {
			t.Fatalf("new mode shows %s under %s", m.items[r.item].Label, m.items[r.item].Root)
		}
	}
	typeText(t, m, "api")
	if m.create == nil || m.create.Root != "/two" || !m.onCreate() {
		t.Fatalf("create = %+v, want api in /two/widgets", m.create)
	}
}

func TestBookmarkOfAWorktreeDoesNotHideIt(t *testing.T) {
	items := append([]Item{{Kind: KindBookmark, Project: "aaa", Label: "aaa", Path: "/dev/devtools/main", Session: "aaa-main"}}, testItems()...)
	m, _ := newModelOf(t, ModelOptions{Here: at("/dev/devtools/main")}, items)
	typeText(t, m, "fresh-feature")
	if m.create == nil || m.create.Project != "devtools" {
		t.Fatalf("create = %+v noCreate = %q, want a feature in devtools", m.create, m.noCreate)
	}
}

func TestCreateRowStaysInSightInATinyWindow(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main"), Mode: ModeNew})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 5})
	typeText(t, m, "popup")
	view := ansi.Strip(m.View(m.width, m.height))
	if !strings.Contains(view, "New feature popup") || !strings.Contains(view, "create") {
		t.Fatalf("view = %q, want the create row and its footer", view)
	}
}

func TestShowsBeforeItemsLoadThenPlacesCursor(t *testing.T) {
	// nothing is fed until after the first view: the catalog answers late
	m, _ := newModelOf(t, ModelOptions{Here: at("/dev/xcloud/main")}, nil)
	view := ansi.Strip(m.View(m.width, m.height))
	if strings.Contains(view, "Loading") || !strings.Contains(view, "Switch to a worktree") {
		t.Fatalf("view=%q, want the switcher drawn before loading, quietly", view)
	}
	m.Update(slowMsg{})
	if view := ansi.Strip(m.View(m.width, m.height)); !strings.Contains(view, "Loading worktrees") {
		t.Fatalf("view = %q, want a loading note once it takes a while", view)
	}
	feed(m, Event{Kind: Items, Items: testItems()}, Event{Kind: Complete})
	if got := selectedLabel(m); got != "xcloud/main" {
		t.Fatalf("selected = %q after loading, want xcloud/main", got)
	}
}

func TestOpenModeWaitsForSessionsBeforeSayingNoneAreOpen(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Mode: ModeOpen})
	m.Update(slowMsg{})
	if view := ansi.Strip(m.View(m.width, m.height)); strings.Contains(view, "No open sessions") || !strings.Contains(view, "Loading open sessions") {
		t.Fatalf("view = %q, want a loading note until the sessions are known", view)
	}
	feed(m, Event{Kind: Sessions})
	if view := ansi.Strip(m.View(m.width, m.height)); !strings.Contains(view, "No open sessions") {
		t.Fatalf("view = %q, want no open workspaces once known", view)
	}
}

func TestGitStateShowsPerWorktree(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	feed(m, Event{Kind: Git, Path: "/dev/xcloud/main", Git: ui.Git{Changes: 3}})
	if m.git[gitKey{"", "/dev/xcloud/main"}].Changes != 3 || len(m.git) != 1 {
		t.Fatalf("git = %+v, want just xcloud/main", m.git)
	}
	if !strings.Contains(ansi.Strip(m.View(m.width, m.height)), "3 changed") {
		t.Fatal("the row did not show its changes")
	}
}

func TestProjectQuerySelectsMainOverItsFeatures(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	for _, query := range []string{"xcloud", "xcl"} {
		press(t, m, esc)
		typeText(t, m, query)
		if got := selectedLabel(m); got != "xcloud/main" {
			t.Fatalf("%s: selected = %q, want xcloud/main", query, got)
		}
	}
}

func remoteWorktree(machine, project, name string) Item {
	item := worktree(project, name)
	item.Machine = machine
	return item
}

// connecting names the other machines in the order of the config, before
// any of them answers, the way the fleet does when the hub opens.
func connecting(m *Model, machines ...string) {
	for _, name := range machines {
		feed(m, Event{Machine: name, Kind: Connecting})
	}
}

// arrive feeds the items of other machines in the way they come in: each
// batch is one machine answering with its whole catalog.
func arrive(t *testing.T, m *Model, items ...[]Item) {
	t.Helper()
	for _, batch := range items {
		machine := batch[0].Machine
		feed(m,
			Event{Machine: machine, Kind: Online},
			Event{Machine: machine, Kind: Items, Items: batch},
			Event{Machine: machine, Kind: Complete},
		)
	}
}

func rowNames(m *Model) []string {
	var names []string
	for _, r := range m.rows {
		switch r.kind {
		case rowSpace:
			continue
		case rowHost:
			names = append(names, "@"+r.project)
		case rowHeader:
			names = append(names, "#"+r.project)
		default:
			item := m.items[r.item]
			name := item.Label
			if item.Machine != "" {
				name = item.Machine + ":" + name
			}
			names = append(names, name)
		}
	}
	return names
}

func TestOtherMachinesListAfterThisOneEachUnderItsName(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/popup-redesign")})
	connecting(m, "macbook", "macmini")
	arrive(t, m,
		[]Item{remoteWorktree("macmini", "xcloud", "main"), remoteWorktree("macmini", "xcloud", "feature-net")},
		[]Item{remoteWorktree("macbook", "kakeibo", "main")},
	)
	want := "devtools/main devtools/popup-redesign systems xcloud/main xcloud/feature-refactoring " +
		"@macbook macbook:kakeibo/main @macmini macmini:xcloud/main macmini:xcloud/feature-net"
	if got := strings.Join(rowNames(m), " "); got != want {
		t.Fatalf("rows =\n  %s\nwant\n  %s", got, want)
	}
	if got := selectedLabel(m); got != "devtools/popup-redesign" {
		t.Fatalf("selected = %q, the cursor moved as machines answered", got)
	}
}

func TestOtherMachineBringsItsOwnState(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	item := remoteWorktree("macmini", "kakeibo", "main")
	item.Git = ui.Git{Changes: 7}
	item.State = ui.Session{Open: true, Agent: "blocked", Title: "Approve rm?"}
	arrive(t, m, []Item{item})
	view := ansi.Strip(m.View(m.width, m.height))
	for _, want := range []string{"macmini", "7 changed", "needs you", "Approve rm?"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	typeText(t, m, "kakeibo")
	m.Update(tab)
	if selectedLabel(m) != "kakeibo/main" {
		t.Fatalf("an open workspace over there is not open only: %v", rowNames(m))
	}
}

func TestTypingAMachineListsWhatIsOnIt(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	arrive(t, m, []Item{remoteWorktree("macmini", "kakeibo", "main"), remoteWorktree("macmini", "kakeibo", "budget")})
	typeText(t, m, "macmini bud")
	if got := strings.Join(rowNames(m), " "); got != "@macmini #kakeibo macmini:kakeibo/budget" {
		t.Fatalf("rows = %s", got)
	}
	// the hits on the machine are not drawn on the worktree name
	for _, r := range m.rows {
		if r.kind == rowItem && (len(r.nameHits) != 3 || r.nameHits[0] != 0) {
			t.Fatalf("hits = %v, want the three of bud", r.nameHits)
		}
	}
}

func TestSamePathOverThereIsNotWhereYouAre(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/xcloud/main")})
	arrive(t, m, []Item{remoteWorktree("macmini", "xcloud", "main")})
	if here := m.items[m.here]; here.Machine != "" {
		t.Fatalf("here = %+v", here)
	}
	// a feature typed here starts here, not over there
	typeText(t, m, "fresh")
	if m.create == nil || m.create.Root != "/dev" || m.create.FromPath != "/dev/xcloud/main" {
		t.Fatalf("create = %+v", m.create)
	}
}

func TestProjectOnlyOverThereStartsNoFeatureHere(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{Here: at("/dev/devtools/main")})
	arrive(t, m, []Item{remoteWorktree("macmini", "kakeibo", "main")})
	typeText(t, m, "kakeibo/budget")
	if m.create != nil || !strings.Contains(m.noCreate, "on macmini") {
		t.Fatalf("create = %+v, noCreate = %q", m.create, m.noCreate)
	}
}

func TestFeatureOverThereCannotBeMergedFromHere(t *testing.T) {
	m, backend := newTestModel(t, ModelOptions{})
	arrive(t, m, []Item{remoteWorktree("macmini", "kakeibo", "budget")})
	typeText(t, m, "budget")
	press(t, m, ctrlG)
	if m.confirm != nil || len(backend.merged) != 0 || !strings.Contains(m.message, "this machine") {
		t.Fatalf("confirm = %+v, message = %q", m.confirm, m.message)
	}
}

func TestCursorStaysOnItsMachineWhenAnEarlierOneAnswers(t *testing.T) {
	m, _ := newTestModel(t, ModelOptions{})
	connecting(m, "abox", "zbox")
	arrive(t, m, []Item{remoteWorktree("zbox", "kakeibo", "main")})
	typeText(t, m, "kakeibo")
	if item, _ := m.selectedItem(); item.Machine != "zbox" {
		t.Fatalf("selected = %+v", item)
	}
	arrive(t, m, []Item{remoteWorktree("abox", "kakeibo", "main")})
	if item, _ := m.selectedItem(); item.Machine != "zbox" {
		t.Fatalf("selected = %+v after abox answered, want still zbox", item)
	}
}
