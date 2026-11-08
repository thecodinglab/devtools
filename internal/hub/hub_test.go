package hub

import (
	"errors"
	"strings"
	"testing"

	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeBackend struct {
	sessions map[string]ui.Session
	others   []Item
	created  []CreateRequest
	merged   []Item
	switched []Item
	mergeErr error
}

func (f *fakeBackend) Status(path string) (ui.Git, error) { return ui.Git{}, nil }
func (f *fakeBackend) Sessions() (map[string]ui.Session, []Item, error) {
	return f.sessions, f.others, nil
}
func (f *fakeBackend) Switch(item Item) error { f.switched = append(f.switched, item); return nil }
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

func newTestModel(t *testing.T, opts Options) (model, *fakeBackend) {
	t.Helper()
	backend, _ := opts.Backend.(*fakeBackend)
	if backend == nil {
		backend = &fakeBackend{}
		opts.Backend = backend
	}
	if opts.Items == nil {
		opts.Items = testItems()
	}
	m := newModel(opts)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	return next.(model), backend
}

func typeText(t *testing.T, m model, text string) model {
	t.Helper()
	for _, r := range text {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = next.(model)
	}
	return m
}

func press(t *testing.T, m model, key tea.KeyPressMsg) (model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(key)
	return next.(model), cmd
}

// run follows the commands a key started, feeding their messages back in,
// the way the program loop would, until a quit or nothing more to do.
func run(t *testing.T, m model, cmd tea.Cmd) (model, bool) {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			return m, true
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				var quit bool
				if m, quit = run(t, m, c); quit {
					return m, true
				}
			}
			return m, false
		}
		next, nextCmd := m.Update(msg)
		m, cmd = next.(model), nextCmd
	}
	return m, false
}

var (
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	tab   = tea.KeyPressMsg{Code: tea.KeyTab}
	down  = tea.KeyPressMsg{Code: tea.KeyDown}
	ctrlN = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}
	ctrlG = tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
)

func selectedLabel(m model) string {
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
	m, _ := newTestModel(t, Options{Here: "/dev/xcloud/main/src"})
	if got := selectedLabel(m); got != "xcloud/main" {
		t.Fatalf("selected = %q, want xcloud/main", got)
	}
}

func TestRowsGroupWorktreesUnderTheirProject(t *testing.T) {
	m, _ := newTestModel(t, Options{})
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
	m, _ := newTestModel(t, Options{Here: "/dev/devtools/main"})
	m = typeText(t, m, "xref")
	if got := selectedLabel(m); got != "xcloud/feature-refactoring" {
		t.Fatalf("selected = %q, want xcloud/feature-refactoring", got)
	}
	if m.create == nil || m.create.Branch != "xref" {
		t.Fatalf("create = %+v, want a create row for xref beside the matches", m.create)
	}
}

func TestUnmatchedQueryBecomesFeatureInProjectYouAreIn(t *testing.T) {
	m, backend := newTestModel(t, Options{Here: "/dev/devtools/popup-redesign"})
	m = typeText(t, m, "  my cool feature ")
	if !m.onCreate() {
		t.Fatalf("selected = %q, want the create row", selectedLabel(m))
	}
	m, cmd := press(t, m, enter)
	m, quit := run(t, m, cmd)
	if !quit {
		t.Fatalf("expected the switcher to quit after creating, message=%q", m.message)
	}
	want := CreateRequest{Root: "/dev", Project: "devtools", Branch: "my-cool-feature", From: "popup-redesign", FromPath: "/dev/devtools/popup-redesign"}
	if len(backend.created) != 1 || backend.created[0] != want {
		t.Fatalf("created = %+v, want %+v", backend.created, want)
	}
	if m.result == nil || m.result.Label != "devtools/my-cool-feature" {
		t.Fatalf("result = %+v, want the new worktree", m.result)
	}
}

func TestProjectPrefixCreatesInThatProjectFromMain(t *testing.T) {
	m, _ := newTestModel(t, Options{Here: "/dev/devtools/main"})
	m = typeText(t, m, "xcloud/fix/login")
	want := CreateRequest{Root: "/dev", Project: "xcloud", Branch: "fix/login", From: "main", FromPath: "/dev/xcloud/main"}
	if m.create == nil || *m.create != want {
		t.Fatalf("create = %+v, want %+v", m.create, want)
	}
}

func TestExistingWorktreeNameOffersNoCreateRow(t *testing.T) {
	m, _ := newTestModel(t, Options{Here: "/dev/devtools/main"})
	m = typeText(t, m, "popup-redesign")
	if m.create != nil {
		t.Fatalf("create = %+v, want none for an existing worktree", m.create)
	}
	if got := selectedLabel(m); got != "devtools/popup-redesign" {
		t.Fatalf("selected = %q", got)
	}
}

func TestOutsideAProjectExplainsHowToStartAFeature(t *testing.T) {
	m, _ := newTestModel(t, Options{Here: "/elsewhere"})
	m = typeText(t, m, "zzz")
	if m.create != nil {
		t.Fatalf("create = %+v, want none outside a project", m.create)
	}
	if !strings.Contains(m.noCreate, "project/name") {
		t.Fatalf("noCreate = %q, want a hint at project/name", m.noCreate)
	}
	m, _ = press(t, m, ctrlN)
	if m.message != m.noCreate {
		t.Fatalf("message = %q, want the hint", m.message)
	}
}

func TestTabShowsOnlyOpenWorkspacesAndOthers(t *testing.T) {
	backend := &fakeBackend{
		sessions: map[string]ui.Session{
			"xcloud-main": {Open: true, Agent: "working"},
			"scratch":     {Open: true},
		},
		others: []Item{
			{Kind: KindWorkspace, Project: "xcloud-main", Label: "xcloud-main", Session: "xcloud-main", WorkspaceID: "w1", State: ui.Session{Open: true, Agent: "working"}},
			{Kind: KindWorkspace, Project: "scratch", Label: "scratch", Session: "scratch", WorkspaceID: "w2", State: ui.Session{Open: true}},
		},
	}
	m, _ := newTestModel(t, Options{Backend: backend})
	m, _ = run(t, m, m.loadSessions())
	m, _ = press(t, m, tab)
	var got []string
	for _, i := range m.selectable() {
		got = append(got, m.items[m.rows[i].item].Label)
	}
	if strings.Join(got, ",") != "xcloud/main,scratch" {
		t.Fatalf("open rows = %v, want xcloud/main and the unknown workspace scratch once", got)
	}
}

func TestNewModeOffersCreateFirstAndShowsTheProject(t *testing.T) {
	m, _ := newTestModel(t, Options{Here: "/dev/xcloud/main", Mode: ModeNew})
	for _, r := range m.rows {
		if r.kind == rowItem && m.items[r.item].Project != "xcloud" {
			t.Fatalf("new mode shows %s from another project", m.items[r.item].Label)
		}
	}
	m = typeText(t, m, "main")
	// an existing name has no create row, so enter would switch instead
	if m.create != nil {
		t.Fatalf("create = %+v for an existing worktree", m.create)
	}
	m, _ = press(t, m, esc)
	m = typeText(t, m, "api")
	if !m.onCreate() {
		t.Fatalf("selected = %q, want the create row", selectedLabel(m))
	}
}

func TestEnterReturnsItemWhenCallerSwitches(t *testing.T) {
	m, backend := newTestModel(t, Options{Here: "/dev/devtools/main"})
	m, _ = press(t, m, down)
	m, cmd := press(t, m, enter)
	if _, quit := run(t, m, cmd); !quit {
		t.Fatal("expected quit")
	}
	if m.result == nil || m.result.Label != "devtools/popup-redesign" {
		t.Fatalf("result = %+v", m.result)
	}
	if len(backend.switched) != 0 {
		t.Fatalf("switched in place: %+v", backend.switched)
	}
}

func TestEnterSwitchesInPlace(t *testing.T) {
	m, backend := newTestModel(t, Options{Here: "/dev/devtools/main", SwitchInPlace: true})
	m, cmd := press(t, m, enter)
	if _, quit := run(t, m, cmd); !quit {
		t.Fatal("expected quit")
	}
	if len(backend.switched) != 1 || backend.switched[0].Label != "devtools/main" {
		t.Fatalf("switched = %+v", backend.switched)
	}
}

func TestMergeAsksFirstThenLandsOnMain(t *testing.T) {
	m, backend := newTestModel(t, Options{Here: "/dev/devtools/popup-redesign"})
	m, _ = press(t, m, ctrlG)
	if m.confirm == nil || m.confirm.Label != "devtools/popup-redesign" {
		t.Fatalf("confirm = %+v", m.confirm)
	}
	if !strings.Contains(ansi.Strip(m.footer()), "Merge popup-redesign into main") {
		t.Fatalf("footer = %q", ansi.Strip(m.footer()))
	}
	m, cmd := press(t, m, enter)
	m, quit := run(t, m, cmd)
	if !quit || len(backend.merged) != 1 {
		t.Fatalf("quit=%v merged=%+v", quit, backend.merged)
	}
	if m.result == nil || m.result.Label != "devtools/main" {
		t.Fatalf("result = %+v, want main", m.result)
	}
}

func TestMergeFailureStaysOpenWithTheError(t *testing.T) {
	backend := &fakeBackend{mergeErr: errors.New("popup-redesign does not rebase cleanly onto main")}
	m, _ := newTestModel(t, Options{Here: "/dev/devtools/popup-redesign", Backend: backend})
	m, _ = press(t, m, ctrlG)
	m, cmd := press(t, m, enter)
	m, quit := run(t, m, cmd)
	if quit {
		t.Fatal("quit after a failed merge")
	}
	if !m.failed || !strings.Contains(ansi.Strip(m.footer()), "does not rebase cleanly") {
		t.Fatalf("footer = %q", ansi.Strip(m.footer()))
	}
}

func TestMergeRefusesMainAndEscCancels(t *testing.T) {
	m, backend := newTestModel(t, Options{Here: "/dev/devtools/main"})
	m, _ = press(t, m, ctrlG)
	if m.confirm != nil || m.message == "" {
		t.Fatalf("confirm=%+v message=%q, want a refusal for main", m.confirm, m.message)
	}
	m, _ = press(t, m, down)
	m, _ = press(t, m, ctrlG)
	m, _ = press(t, m, esc)
	if m.confirm != nil || len(backend.merged) != 0 {
		t.Fatalf("esc did not cancel: confirm=%+v merged=%+v", m.confirm, backend.merged)
	}
}

func TestEscClearsQueryThenCloses(t *testing.T) {
	m, _ := newTestModel(t, Options{})
	m = typeText(t, m, "dev")
	m, cmd := press(t, m, esc)
	if m.input.Value() != "" || cmd != nil {
		t.Fatalf("first esc: value=%q cmd=%v", m.input.Value(), cmd)
	}
	_, cmd = press(t, m, esc)
	if _, quit := run(t, m, cmd); !quit {
		t.Fatal("second esc should close")
	}
}

func TestRowsShowHeadingWhenMainDoesNotMatch(t *testing.T) {
	m, _ := newTestModel(t, Options{})
	m = typeText(t, m, "refac")
	if len(m.rows) != 2 || m.rows[0].kind != rowHeader || m.rows[0].project != "xcloud" || !m.rows[1].child {
		t.Fatalf("rows = %+v, want the xcloud heading over its matching feature", m.rows)
	}
}

func TestSelectedRowFillsTheWidth(t *testing.T) {
	m, _ := newTestModel(t, Options{Here: "/dev/devtools/popup-redesign"})
	for _, line := range strings.Split(m.View().Content, "\n") {
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
		m, _ := newTestModel(t, Options{Here: "/dev/devtools/main"})
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		m = typeText(t, m, "feature")
		lines := strings.Split(m.View().Content, "\n")
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
	m, _ := newTestModel(t, Options{})
	m.busy = "Merging popup-redesign into main…"
	for _, key := range []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModCtrl}, esc, enter} {
		next, cmd := m.Update(key)
		m = next.(model)
		if cmd != nil {
			t.Fatalf("%s while busy started %v, want it ignored", key, cmd)
		}
	}
}

func TestWorkspacesSharingALabelStayApart(t *testing.T) {
	backend := &fakeBackend{
		sessions: map[string]ui.Session{"scratch": {Open: true, Agent: "idle"}},
		others: []Item{
			{Kind: KindWorkspace, Project: "scratch", Label: "scratch", Session: "scratch", WorkspaceID: "w1", State: ui.Session{Open: true, Agent: "idle"}},
			{Kind: KindWorkspace, Project: "scratch", Label: "scratch", Session: "scratch", WorkspaceID: "w2", State: ui.Session{Open: true, Agent: "blocked"}},
		},
	}
	m, _ := newTestModel(t, Options{Backend: backend})
	m, _ = run(t, m, m.loadSessions())
	m = typeText(t, m, "scratch")
	var got []string
	for _, r := range m.selectable() {
		item := m.items[m.rows[r].item]
		got = append(got, item.WorkspaceID+":"+m.session(item).Agent)
	}
	if strings.Join(got, ",") != "w1:idle,w2:blocked" {
		t.Fatalf("rows = %v, want both workspaces with their own state", got)
	}
}

func TestBestMatchWinsOverItsProjectsMain(t *testing.T) {
	m, _ := newTestModel(t, Options{Items: []Item{worktree("application", "main"), worktree("application", "api")}})
	m = typeText(t, m, "api")
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
	m, _ := newTestModel(t, Options{Items: []Item{one, api, two}, Here: "/two/widgets/main", Mode: ModeNew})
	for _, r := range m.rows {
		if r.kind == rowItem && m.items[r.item].Root != "/two" {
			t.Fatalf("new mode shows %s under %s", m.items[r.item].Label, m.items[r.item].Root)
		}
	}
	m = typeText(t, m, "api")
	if m.create == nil || m.create.Root != "/two" || !m.onCreate() {
		t.Fatalf("create = %+v, want api in /two/widgets", m.create)
	}
}

func TestBookmarkOfAWorktreeDoesNotHideIt(t *testing.T) {
	items := append([]Item{{Kind: KindBookmark, Project: "aaa", Label: "aaa", Path: "/dev/devtools/main", Session: "aaa-main"}}, testItems()...)
	m, _ := newTestModel(t, Options{Items: items, Here: "/dev/devtools/main"})
	m = typeText(t, m, "fresh-feature")
	if m.create == nil || m.create.Project != "devtools" {
		t.Fatalf("create = %+v noCreate = %q, want a feature in devtools", m.create, m.noCreate)
	}
}

func TestCreateRowStaysInSightInATinyWindow(t *testing.T) {
	m, _ := newTestModel(t, Options{Here: "/dev/devtools/main", Mode: ModeNew})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 5})
	m = typeText(t, next.(model), "popup")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "New feature popup") || !strings.Contains(view, "create") {
		t.Fatalf("view = %q, want the create row and its footer", view)
	}
}

func TestShowsBeforeItemsLoadThenPlacesCursor(t *testing.T) {
	loaded := false
	m, _ := newTestModel(t, Options{
		Items: []Item{},
		Load: func() ([]Item, error) {
			loaded = true
			return testItems(), nil
		},
		Here: "/dev/xcloud/main",
	})
	view := ansi.Strip(m.View().Content)
	if loaded || strings.Contains(view, "Loading") || !strings.Contains(view, "Switch to a worktree") {
		t.Fatalf("loaded=%v view=%q, want the switcher drawn before loading, quietly", loaded, view)
	}
	next, _ := m.Update(slowMsg{})
	if view := ansi.Strip(next.(model).View().Content); !strings.Contains(view, "Loading worktrees") {
		t.Fatalf("view = %q, want a loading note once it takes a while", view)
	}
	m, _ = run(t, m, m.loadItems())
	if got := selectedLabel(m); got != "xcloud/main" {
		t.Fatalf("selected = %q after loading, want xcloud/main", got)
	}
}

func TestOpenModeWaitsForSessionsBeforeSayingNoneAreOpen(t *testing.T) {
	m, _ := newTestModel(t, Options{Mode: ModeOpen})
	next, _ := m.Update(slowMsg{})
	m = next.(model)
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "No open workspaces") || !strings.Contains(view, "Loading open workspaces") {
		t.Fatalf("view = %q, want a loading note until the sessions are known", view)
	}
	m, _ = run(t, m, m.loadSessions())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "No open workspaces") {
		t.Fatalf("view = %q, want no open workspaces once known", view)
	}
}

func TestGitStateShowsPerWorktree(t *testing.T) {
	m, _ := newTestModel(t, Options{})
	next, _ := m.Update(gitMsg{path: "/dev/xcloud/main", git: ui.Git{Changes: 3}})
	m = next.(model)
	if m.git["/dev/xcloud/main"].Changes != 3 || len(m.git) != 1 {
		t.Fatalf("git = %+v, want just xcloud/main", m.git)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "3 changed") {
		t.Fatal("the row did not show its changes")
	}
}
