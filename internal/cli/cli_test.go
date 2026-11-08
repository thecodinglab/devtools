package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"devtools/internal/hub"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "devtools-cli-test")
	if err != nil {
		panic(err)
	}
	// Keep tests hermetic: never read the developer's real config file.
	os.Setenv("DEVTOOLS_CONFIG", filepath.Join(tmp, "config"))
	// Nor drive the herdr session the tests may run in: without a fake herdr
	// the missing binary makes devtools fall back to the fake tmux.
	for _, name := range []string{"HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_SESSION", "DEVTOOLS_MULTIPLEXER", "TMUX"} {
		os.Unsetenv(name)
	}
	os.Setenv("HERDR_BIN_PATH", filepath.Join(tmp, "no-herdr"))
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func TestSwitchUsesFakeTmuxForNormalRepo(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)

	log := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(out)), " ")
	want := "new-session -A -s small-main -c " + project
	if got != want {
		t.Fatalf("tmux args = %q, want %q", got, want)
	}
}

func TestSwitchCreatesHerdrWorkspace(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)

	log := installFakeHerdr(t, `{"label":"other","workspace_id":"w1"}`)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	got := readLog(t, log)
	want := "workspace list\nworkspace create --cwd " + project + " --label small-main --focus"
	if got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestSwitchFocusesExistingHerdrWorkspace(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)

	log := installFakeHerdr(t, `{"label":"other","workspace_id":"w1"},{"label":"small-main","workspace_id":"w2"}`)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	got := readLog(t, log)
	want := "workspace list\nworkspace focus w2"
	if got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestSwitchPrefersTmuxNestedInHerdr(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)

	herdrLog := installFakeHerdr(t, "")
	tmuxLog := installFakeTmux(t)
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	if got := readLog(t, herdrLog); got != "" {
		t.Fatalf("herdr args = %q, want none", got)
	}
	if got := readLog(t, tmuxLog); !strings.Contains(got, "switch-client -t small-main") {
		t.Fatalf("tmux args = %q, want a switch-client", got)
	}
}

func TestListAcceptsRootPersistentFlag(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"list", "--root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	want := "small\t" + project
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}

func TestListMergesMultipleRootsFromEnv(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	alphaPath := filepath.Join(rootA, "alpha")
	betaPath := filepath.Join(rootB, "beta")
	makeRepo(t, alphaPath)
	makeRepo(t, betaPath)
	t.Setenv("DEVTOOLS_ROOT", rootA+string(os.PathListSeparator)+rootB)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	want := "alpha\t" + alphaPath + "\nbeta\t" + betaPath
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}

func TestConfigFileDefinesRootsAndBookmarks(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	alphaPath := filepath.Join(rootA, "alpha")
	betaPath := filepath.Join(rootB, "beta")
	makeRepo(t, alphaPath)
	makeRepo(t, betaPath)
	notesPath := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config")
	cfg := "# devtools config\nroot " + rootA + "\nroot " + rootB + "\nbookmark notes " + notesPath + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVTOOLS_CONFIG", cfgPath)
	t.Setenv("DEVTOOLS_ROOT", "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("list returned %d, stderr=%s", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	for _, want := range []string{"alpha\t" + alphaPath, "beta\t" + betaPath, "notes\t" + notesPath} {
		if !strings.Contains(got, want) {
			t.Fatalf("list output = %q, want it to contain %q", got, want)
		}
	}

	changeCwd(t, t.TempDir())
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status returned %d, stderr=%s", code, stderr.String())
	}
	got = strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Fatalf("status output = %q, want repos from both roots", got)
	}
	if strings.Contains(got, "notes") {
		t.Fatalf("status output = %q, want bookmarks excluded", got)
	}

	stdout.Reset()
	stderr.Reset()
	overrideRoot := t.TempDir()
	gammaPath := filepath.Join(overrideRoot, "gamma")
	makeRepo(t, gammaPath)
	if code := Run([]string{"list", "--root", overrideRoot}, &stdout, &stderr); code != 0 {
		t.Fatalf("list --root returned %d, stderr=%s", code, stderr.String())
	}
	got = strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "gamma\t"+gammaPath) || strings.Contains(got, "alpha") {
		t.Fatalf("list --root output = %q, want only gamma plus bookmarks", got)
	}
}

func TestSwitchToBookmarkUsesBookmarkNameAsSession(t *testing.T) {
	root := t.TempDir()
	notesPath := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfgPath, []byte("bookmark notes "+notesPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVTOOLS_CONFIG", cfgPath)
	t.Setenv("DEVTOOLS_ROOT", root)
	log := installFakeTmux(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "notes"}, &stdout, &stderr); code != 0 {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(out)), " ")
	want := "new-session -A -s notes -c " + notesPath
	if got != want {
		t.Fatalf("tmux args = %q, want %q", got, want)
	}
}

func TestBookmarkAddListRemoveManageConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "devtools", "config")
	t.Setenv("DEVTOOLS_CONFIG", cfgPath)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	notesPath := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"bookmark", "add", "notes", notesPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("bookmark add returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "notes\t"+notesPath {
		t.Fatalf("bookmark add stdout = %q", strings.TrimSpace(stdout.String()))
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bookmark", "add", "notes", notesPath}, &stdout, &stderr); code == 0 {
		t.Fatal("duplicate bookmark add should fail")
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr = %q, want already exists", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bookmark", "add", "missing", filepath.Join(notesPath, "nope")}, &stdout, &stderr); code == 0 {
		t.Fatal("bookmark add for missing directory should fail")
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bookmark", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("bookmark list returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "notes\t"+notesPath {
		t.Fatalf("bookmark list stdout = %q", strings.TrimSpace(stdout.String()))
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bookmark", "remove", "notes"}, &stdout, &stderr); code != 0 {
		t.Fatalf("bookmark remove returned %d, stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bookmark", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("bookmark list returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("bookmark list after remove = %q, want empty", strings.TrimSpace(stdout.String()))
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"bookmark", "remove", "notes"}, &stdout, &stderr); code == 0 {
		t.Fatal("removing a missing bookmark should fail")
	}
}

func TestWorkInfersProjectInSecondaryRoot(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	var stdout, stderr bytes.Buffer

	t.Setenv("DEVTOOLS_ROOT", rootB)
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	t.Setenv("DEVTOOLS_ROOT", rootA+string(os.PathListSeparator)+rootB)
	changeCwd(t, filepath.Join(rootB, "widgets", "main"))

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"work", "feature/multi"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	want := filepath.Join(rootB, "widgets", "feature-multi")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}

func TestStatusShowsCompactDashboardForDiscoveredWorktrees(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"clone", remote, "gadgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	changeCwd(t, mainPath)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	if err := os.WriteFile(filepath.Join(featurePath, "scratch.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(featurePath, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status returned %d, stderr=%s", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want the project row and its feature", got)
	}
	if strings.Contains(got, "gadgets") {
		t.Fatalf("default status should only show current project, got %q", got)
	}
	// the project row stands for main, marked as where you are; clean and
	// in step with origin, it says nothing more
	if strings.TrimSpace(lines[0]) != "▎ widgets" {
		t.Fatalf("main row = %q", lines[0])
	}
	// README.md changed, scratch.txt is untracked and doesn't count
	if !strings.HasPrefix(lines[1], "    feature-test") || !strings.Contains(lines[1], "1 changed") || !strings.Contains(lines[1], "no upstream") {
		t.Fatalf("feature row = %q", lines[1])
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status --all returned %d, stderr=%s", code, stderr.String())
	}
	got = strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "gadgets") || !strings.Contains(got, "feature-test") || !strings.Contains(got, "widgets") {
		t.Fatalf("status --all output = %q, want all worktrees", got)
	}

	changeCwd(t, t.TempDir())
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status outside project returned %d, stderr=%s", code, stderr.String())
	}
	got = strings.TrimSpace(stdout.String())
	if got != "gadgets\n  widgets\n    feature-test   1 changed   no upstream" {
		t.Fatalf("status outside project output = %q, want all worktrees", got)
	}
}

func TestShortWorkAndRemoveAliasesAreNotRegistered(t *testing.T) {
	for _, name := range []string{"w", "rm"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{name}, &stdout, &stderr); code == 0 {
			t.Fatalf("%s returned success, stdout=%s", name, stdout.String())
		}
		if !strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("%s stderr = %q, want unknown command", name, stderr.String())
		}
	}
}

func TestWorkCommandInfersProjectFromCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log := installFakeTmux(t)
	var stdout, stderr bytes.Buffer
	t.Setenv("DEVTOOLS_ROOT", root)
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(mainPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalCwd)
	})

	stdout.Reset()
	stderr.Reset()
	clearFile(t, log)
	if code := Run([]string{"work", "feature/cli"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	want := filepath.Join(root, "widgets", "feature-cli")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
	if _, err := os.Stat(filepath.Join(want, "README.md")); err != nil {
		t.Fatalf("expected created worktree: %v", err)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(out)), " ")
	wantTmux := "new-session -A -s widgets-feature-cli -c " + want
	if got != wantTmux {
		t.Fatalf("tmux args = %q, want %q", got, wantTmux)
	}
}

func TestNewProjectInitializesNormalRepo(t *testing.T) {
	root := t.TempDir()
	log := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"new", "fresh"}, &stdout, &stderr); code != 0 {
		t.Fatalf("new returned %d, stderr=%s", code, stderr.String())
	}
	project := filepath.Join(root, "fresh")
	if strings.TrimSpace(stdout.String()) != project {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), project)
	}
	if _, err := os.Stat(filepath.Join(project, ".git")); err != nil {
		t.Fatalf("expected normal git repo: %v", err)
	}
	branch := gitOut(t, project, "branch", "--show-current")
	if strings.TrimSpace(branch) != "main" {
		t.Fatalf("branch = %q, want main", strings.TrimSpace(branch))
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(out)), " ")
	want := "new-session -A -s fresh-main -c " + project
	if got != want {
		t.Fatalf("tmux args = %q, want %q", got, want)
	}
}

func TestCloneStartsTmuxSessionForMainWorktree(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	if strings.TrimSpace(stdout.String()) != mainPath {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), mainPath)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(strings.Fields(string(out)), " ")
	want := "new-session -A -s widgets-main -c " + mainPath
	if got != want {
		t.Fatalf("tmux args = %q, want %q", got, want)
	}
}

func TestSwitchAmbiguousQueryOpensSwitcherWithTheQuery(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "widgets")
	featurePath := filepath.Join(project, "feature-cli")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, project, "init", "--bare", ".bare")
	makeRepo(t, filepath.Join(project, "main"))
	makeRepo(t, featurePath)

	tmuxLog := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		if opts.Mode != hub.ModeAll || opts.Query != "widgets" {
			t.Fatalf("mode=%v query=%q, want all and the query", opts.Mode, opts.Query)
		}
		items, err := opts.Load()
		if err != nil {
			t.Fatal(err)
		}
		var labels []string
		for _, item := range items {
			labels = append(labels, item.Label)
		}
		if strings.Join(labels, ",") != "widgets/feature-cli,widgets/main" {
			t.Fatalf("items = %v", labels)
		}
		return &items[0], nil
	})
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"switch", "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
	tmuxOut, err := os.ReadFile(tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	gotTmux := strings.Join(strings.Fields(string(tmuxOut)), " ")
	wantTmux := "new-session -A -s widgets-feature-cli -c " + featurePath
	if gotTmux != wantTmux {
		t.Fatalf("tmux args = %q, want %q", gotTmux, wantTmux)
	}
}

func TestSessionsOpensSwitcherOnOpenTmuxSessions(t *testing.T) {
	log := installFakeTmuxWithSessions(t, "work\t1\t0\nops\t2\t1")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		if opts.Mode != hub.ModeOpen || !opts.SwitchInPlace {
			t.Fatalf("mode=%v in place=%v, want open sessions switched in place", opts.Mode, opts.SwitchInPlace)
		}
		sessions, others, err := opts.Backend.Sessions()
		if err != nil {
			t.Fatal(err)
		}
		if !sessions["ops"].Open || !sessions["ops"].Focused || !sessions["work"].Open || len(others) != 2 {
			t.Fatalf("sessions = %+v, others = %+v", sessions, others)
		}
		return nil, opts.Backend.Switch(others[1])
	})
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"sessions"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sessions returned %d, stderr=%s", code, stderr.String())
	}
	got := tmuxCommands(t, log)
	want := []string{
		"list-sessions -F #{session_name}\t#{session_windows}\t#{session_attached}",
		"switch-client -t ops",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tmux commands = %#v, want %#v", got, want)
	}
}

func TestWorkWithoutBranchOpensSwitcherToNameAFeature(t *testing.T) {
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	installFakeTmux(t)
	called := false
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		called = true
		if opts.Mode != hub.ModeNew {
			t.Fatalf("mode = %v, want new", opts.Mode)
		}
		return nil, nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"work"}, &stdout, &stderr); code != 0 || !called {
		t.Fatalf("work returned %d called=%v, stderr=%s", code, called, stderr.String())
	}
}

func TestSwitcherCreatesFeatureFromTheWorktreeYouAreIn(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	t.Setenv("HERDR_ACTIVE_PANE_CWD", mainPath)
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		if opts.Here != mainPath {
			t.Fatalf("here = %q, want the pane's directory %q", opts.Here, mainPath)
		}
		item, err := opts.Backend.Create(hub.CreateRequest{
			Root: root, Project: "widgets", Branch: "feature/api", From: "main", FromPath: mainPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		return &item, nil
	})
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-api")
	if branch := strings.TrimSpace(gitOut(t, featurePath, "branch", "--show-current")); branch != "feature/api" {
		t.Fatalf("branch = %q, want feature/api", branch)
	}
}

func TestMergeRebasesSoMainOnlyFastForwards(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	identity(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	commitFile(t, featurePath, "feature.txt", "feature\n", "feature")
	// main moves on after the branch started
	commitFile(t, mainPath, "main.txt", "main\n", "main moved")

	changeCwd(t, featurePath)
	if code := Run([]string{"merge"}, &stdout, &stderr); code != 0 {
		t.Fatalf("merge returned %d, stderr=%s", code, stderr.String())
	}
	if merges := strings.TrimSpace(gitOut(t, mainPath, "rev-list", "--merges", "HEAD")); merges != "" {
		t.Fatalf("main has merge commits %q, want a linear history", merges)
	}
	subjects := strings.Fields(gitOut(t, mainPath, "log", "--format=%s", "-3"))
	if strings.Join(subjects, " ") != "feature main moved init" {
		t.Fatalf("main log = %v, want the feature replayed on top of main", subjects)
	}
}

func TestMergeRefusesBranchWithMergeCommits(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	identity(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	gitCmd(t, featurePath, "checkout", "-q", "-b", "side")
	commitFile(t, featurePath, "side.txt", "side\n", "side")
	gitCmd(t, featurePath, "checkout", "-q", "feature/test")
	commitFile(t, featurePath, "feature.txt", "feature\n", "feature")
	gitCmd(t, featurePath, "merge", "--no-ff", "--no-commit", "side")
	commitFile(t, featurePath, "merge-only.txt", "only in the merge\n", "merge side")
	before := strings.TrimSpace(gitOut(t, featurePath, "rev-parse", "HEAD"))

	changeCwd(t, featurePath)
	stderr.Reset()
	if code := Run([]string{"merge"}, &stdout, &stderr); code == 0 {
		t.Fatal("merge succeeded and would have dropped the merge commit")
	}
	if !strings.Contains(stderr.String(), "--squash") {
		t.Fatalf("stderr = %q, want a pointer to --squash", stderr.String())
	}
	if after := strings.TrimSpace(gitOut(t, featurePath, "rev-parse", "HEAD")); after != before {
		t.Fatalf("feature moved from %s to %s", before, after)
	}
}

func TestMergeThatConflictsLeavesEverythingAsItWas(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	identity(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	commitFile(t, featurePath, "README.md", "feature\n", "feature edit")
	commitFile(t, mainPath, "README.md", "main\n", "main edit")
	before := strings.TrimSpace(gitOut(t, featurePath, "rev-parse", "HEAD"))
	mainBefore := strings.TrimSpace(gitOut(t, mainPath, "rev-parse", "HEAD"))

	changeCwd(t, featurePath)
	stderr.Reset()
	if code := Run([]string{"merge"}, &stdout, &stderr); code == 0 {
		t.Fatal("merge succeeded despite the conflict")
	}
	if !strings.Contains(stderr.String(), "does not rebase cleanly onto main") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if after := strings.TrimSpace(gitOut(t, featurePath, "rev-parse", "HEAD")); after != before {
		t.Fatalf("feature moved from %s to %s", before, after)
	}
	if after := strings.TrimSpace(gitOut(t, mainPath, "rev-parse", "HEAD")); after != mainBefore {
		t.Fatalf("main moved from %s to %s", mainBefore, after)
	}
	if status := strings.TrimSpace(gitOut(t, featurePath, "status", "--porcelain")); status != "" {
		t.Fatalf("feature worktree left dirty: %q", status)
	}
}

func TestSessionPreviewFitsAllWindowsIntoPreviewHeight(t *testing.T) {
	log := installFakeTmuxWithWindows(t, "@1\t1\tvim\n@2\t2\tserver logs", 10)
	t.Setenv("FZF_PREVIEW_LINES", "8")
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"session-preview", "work"}, &stdout, &stderr); code != 0 {
		t.Fatalf("session-preview returned %d, stderr=%s", code, stderr.String())
	}
	want := "\x1b[1m── 1: vim ──\x1b[0m\n" +
		"@1 line 8\n@1 line 9\n@1 line 10\n" +
		"\x1b[1m── 2: server logs ──\x1b[0m\n" +
		"@2 line 8\n@2 line 9\n@2 line 10\n"
	if stdout.String() != want {
		t.Fatalf("preview output = %q, want %q", stdout.String(), want)
	}
	got := tmuxCommands(t, log)
	wantCommands := []string{
		"list-windows -t work -F #{window_id}\t#{window_index}\t#{window_name}",
		"capture-pane -ep -t @1",
		"capture-pane -ep -t @2",
	}
	if strings.Join(got, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("tmux commands = %#v, want %#v", got, wantCommands)
	}
}

func TestNukeDiscardsWorktreeChanges(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	if err := os.WriteFile(filepath.Join(mainPath, "README.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainPath, "untracked.txt"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()

	if code := Run([]string{"nuke"}, &stdout, &stderr); code != 0 {
		t.Fatalf("nuke returned %d, stderr=%s", code, stderr.String())
	}
	wantPath, err := filepath.EvalSymlinks(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != wantPath {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), wantPath)
	}
	if _, err := os.Stat(filepath.Join(mainPath, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked.txt should have been removed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(mainPath, "README.md")); err != nil || strings.TrimSpace(string(got)) != "hello" {
		t.Fatalf("README.md = %q (err=%v), want restored to \"hello\"", got, err)
	}
}

func TestRemoveSwitchesToMainSessionBeforeRemovingFeature(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	changeCwd(t, featurePath)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	clearFile(t, log)
	stdout.Reset()
	stderr.Reset()

	if code := Run([]string{"done", "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("done returned %d, stderr=%s", code, stderr.String())
	}
	wantRemoved := featurePath
	if strings.TrimSpace(stdout.String()) != wantRemoved {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), wantRemoved)
	}
	if _, err := os.Stat(featurePath); !os.IsNotExist(err) {
		t.Fatalf("expected feature worktree to be removed, err=%v", err)
	}
	got := tmuxCommands(t, log)
	want := []string{
		"has-session -t widgets-main",
		"switch-client -t widgets-main",
		"has-session -t widgets-feature-test",
		"kill-session -t widgets-feature-test",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tmux commands = %#v, want %#v", got, want)
	}
}

func TestRemoveMainDetachesBeforeRemovingSession(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	clearFile(t, log)
	stdout.Reset()
	stderr.Reset()

	if code := Run([]string{"done", "--allow-main", "--force", "--keep-branch"}, &stdout, &stderr); code != 0 {
		t.Fatalf("done returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != mainPath {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), mainPath)
	}
	if _, err := os.Stat(mainPath); !os.IsNotExist(err) {
		t.Fatalf("expected main worktree to be removed, err=%v", err)
	}
	got := tmuxCommands(t, log)
	want := []string{
		"detach-client",
		"has-session -t widgets-main",
		"kill-session -t widgets-main",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tmux commands = %#v, want %#v", got, want)
	}
}

func TestMergeMergesCurrentWorktreeIntoMainAndRemovesIt(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log := installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	if err := os.WriteFile(filepath.Join(featurePath, "feature.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, featurePath, "add", "feature.txt")
	gitCmd(t, featurePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "feature")

	changeCwd(t, featurePath)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	clearFile(t, log)
	stdout.Reset()
	stderr.Reset()

	if code := Run([]string{"merge"}, &stdout, &stderr); code != 0 {
		t.Fatalf("merge returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != mainPath {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), mainPath)
	}
	if _, err := os.Stat(filepath.Join(mainPath, "feature.txt")); err != nil {
		t.Fatalf("expected merged file in main: %v", err)
	}
	if _, err := os.Stat(featurePath); !os.IsNotExist(err) {
		t.Fatalf("expected feature worktree to be removed, err=%v", err)
	}
	barePath := filepath.Join(root, "widgets", ".bare")
	cmd := exec.Command("git", "--git-dir", barePath, "show-ref", "--verify", "--quiet", "refs/heads/feature/test")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected feature branch to be deleted")
	}
	got := tmuxCommands(t, log)
	want := []string{
		"has-session -t widgets-main",
		"switch-client -t widgets-main",
		"has-session -t widgets-feature-test",
		"kill-session -t widgets-feature-test",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tmux commands = %#v, want %#v", got, want)
	}
}

func TestMergeMovesToMainHerdrWorkspaceAndClosesWorktreeWorkspace(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log := installFakeHerdr(t, `{"label":"widgets-main","workspace_id":"w1"},{"label":"widgets-feature-test","workspace_id":"w2"}`)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	if err := os.WriteFile(filepath.Join(featurePath, "feature.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, featurePath, "add", "feature.txt")
	gitCmd(t, featurePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "feature")

	changeCwd(t, featurePath)
	t.Setenv("HERDR_KILLS_IN", featurePath)
	clearFile(t, log)
	stdout.Reset()
	stderr.Reset()

	if code := Run([]string{"merge"}, &stdout, &stderr); code != 0 {
		t.Fatalf("merge returned %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(featurePath); !os.IsNotExist(err) {
		t.Fatalf("expected feature worktree to be removed, err=%v", err)
	}
	got := readLog(t, log)
	want := "workspace list\nworkspace list\nworkspace focus w1\nworkspace close w2"
	if got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestMergeSquashCreatesSingleCommitWithEditorMessage(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	installFakeGitEditor(t, "squashed feature\n\ncombined changes\n")
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	gitCmd(t, mainPath, "config", "user.name", "Test")
	gitCmd(t, mainPath, "config", "user.email", "test@example.com")
	gitCmd(t, mainPath, "config", "commit.gpgsign", "false")
	beforeCount := strings.TrimSpace(gitOut(t, mainPath, "rev-list", "--count", "HEAD"))

	changeCwd(t, mainPath)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	if err := os.WriteFile(filepath.Join(featurePath, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, featurePath, "add", "one.txt")
	gitCmd(t, featurePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "one")
	if err := os.WriteFile(filepath.Join(featurePath, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, featurePath, "add", "two.txt")
	gitCmd(t, featurePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "two")

	changeCwd(t, featurePath)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"merge", "--squash"}, &stdout, &stderr); code != 0 {
		t.Fatalf("merge --squash returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != mainPath {
		t.Fatalf("stdout = %q, want %q", strings.TrimSpace(stdout.String()), mainPath)
	}
	if _, err := os.Stat(filepath.Join(mainPath, "one.txt")); err != nil {
		t.Fatalf("expected first squashed file in main: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainPath, "two.txt")); err != nil {
		t.Fatalf("expected second squashed file in main: %v", err)
	}
	if _, err := os.Stat(featurePath); !os.IsNotExist(err) {
		t.Fatalf("expected feature worktree to be removed, err=%v", err)
	}
	afterCount := strings.TrimSpace(gitOut(t, mainPath, "rev-list", "--count", "HEAD"))
	if afterCount != "2" || beforeCount != "1" {
		t.Fatalf("commit count before=%s after=%s, want 1 then 2", beforeCount, afterCount)
	}
	subject := strings.TrimSpace(gitOut(t, mainPath, "log", "-1", "--format=%s"))
	if subject != "squashed feature" {
		t.Fatalf("squash commit subject = %q, want squashed feature", subject)
	}
	barePath := filepath.Join(root, "widgets", ".bare")
	cmd := exec.Command("git", "--git-dir", barePath, "show-ref", "--verify", "--quiet", "refs/heads/feature/test")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected feature branch to be deleted")
	}
}

func TestGitWorkflowCommandsOperateThroughCLI(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	mainPath := filepath.Join(root, "widgets", "main")
	changeCwd(t, mainPath)
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")

	if err := os.WriteFile(filepath.Join(mainPath, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, mainPath, "add", "main.txt")
	gitCmd(t, mainPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "main")
	gitCmd(t, mainPath, "push", "origin", "main")

	changeCwd(t, featurePath)
	if err := os.WriteFile(filepath.Join(featurePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, featurePath, "add", "feature.txt")
	gitCmd(t, featurePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "feature")

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"rebase"}, &stdout, &stderr); code != 0 {
		t.Fatalf("rebase returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "main" {
		t.Fatalf("rebase stdout = %q, want main", strings.TrimSpace(stdout.String()))
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"push"}, &stdout, &stderr); code != 0 {
		t.Fatalf("push returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "feature/test\torigin/feature/test" {
		t.Fatalf("push stdout = %q", strings.TrimSpace(stdout.String()))
	}

	clone := filepath.Join(t.TempDir(), "clone")
	gitCmd(t, filepath.Dir(clone), "clone", remote, clone)
	if err := os.WriteFile(filepath.Join(clone, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, clone, "add", "remote.txt")
	gitCmd(t, clone, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "remote")
	gitCmd(t, clone, "push", "origin", "main")

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"update"}, &stdout, &stderr); code != 0 {
		t.Fatalf("update returned %d, stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != mainPath {
		t.Fatalf("update stdout = %q, want %q", strings.TrimSpace(stdout.String()), mainPath)
	}
	if _, err := os.Stat(filepath.Join(mainPath, "remote.txt")); err != nil {
		t.Fatalf("expected updated main worktree: %v", err)
	}
}

// stubHub replaces the switcher, which needs a terminal, with fn.
func stubHub(t *testing.T, fn func(hub.Options) (*hub.Item, error)) {
	t.Helper()
	saved := runHub
	runHub = fn
	t.Cleanup(func() { runHub = saved })
}

// identity lets git commit and rebase in tests.
func identity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
}

func commitFile(t *testing.T, dir, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", name)
	gitCmd(t, dir, "commit", "-m", message)
}

func installFakeTmux(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmux.log")
	fakeTmux := filepath.Join(bin, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_LOG\"\n"
	if err := os.WriteFile(fakeTmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", log)
	t.Setenv("TMUX", "")
	return log
}

// installFakeHerdr runs devtools as if inside herdr, with the given
// comma-separated workspace objects as open workspaces. Agents, machines and
// pane output come from HERDR_AGENTS, HERDR_MACHINES and HERDR_PANE_LINES.
func installFakeHerdr(t *testing.T, workspaces string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herdr")
	log := filepath.Join(t.TempDir(), "herdr.log")
	script := "#!/bin/sh\nif [ $# -eq 0 ]; then echo '<attach>' >> \"$HERDR_LOG\"; else printf '%s\\n' \"$*\" >> \"$HERDR_LOG\"; fi\n" +
		"if [ \"$1\" = --machine ]; then shift 2; fi\n" +
		// herdr kills the processes of a closed workspace, devtools included
		"if [ \"$1 $2\" = \"workspace close\" ] && [ -e \"$HERDR_KILLS_IN\" ]; then\necho 'killed devtools' >&2\nexit 1\nfi\n" +
		"case \"$1 $2\" in\n" +
		"'workspace list') printf '{\"result\":{\"workspaces\":[%s]}}\\n' \"$HERDR_WORKSPACES\";;\n" +
		"'tab list') echo '{\"result\":{\"tabs\":[]}}';;\n" +
		"'pane list') echo '{\"result\":{\"panes\":[]}}';;\n" +
		"'agent list') printf '{\"result\":{\"agents\":[%s]}}\\n' \"$HERDR_AGENTS\";;\n" +
		"'machine list') printf '[%s]\\n' \"$HERDR_MACHINES\";;\n" +
		"'agent read') i=1; while [ \"$i\" -le \"$HERDR_PANE_LINES\" ]; do printf '%s line %s\\n' \"$3\" \"$i\"; i=$((i+1)); done;;\n" +
		"'status server') if [ -z \"$HERDR_RUNNING\" ] || [ -e \"$HERDR_RUNNING\" ]; then echo '{\"running\":true}'; else echo '{\"running\":false}'; fi;;\n" +
		"'server ') touch \"$HERDR_RUNNING\";;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("HERDR_LOG", log)
	t.Setenv("HERDR_WORKSPACES", workspaces)
	t.Setenv("HERDR_RUNNING", "")
	t.Setenv("TMUX", "")
	return log
}

// installFakeHerdrOutside runs devtools in a plain terminal with herdr
// installed, its server running once the returned marker file exists.
func installFakeHerdrOutside(t *testing.T, workspaces string) (string, string) {
	t.Helper()
	log := installFakeHerdr(t, workspaces)
	marker := filepath.Join(t.TempDir(), "running")
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_RUNNING", marker)
	return log, marker
}

// herdrCalls reads the fake herdr log, collapsing the repeated server status
// polls whose count depends on timing.
func herdrCalls(t *testing.T, log string) string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(readLog(t, log), "\n") {
		if len(lines) == 0 || lines[len(lines)-1] != line {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func installFakeTmuxWithSessions(t *testing.T, sessions string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmux.log")
	fakeTmux := filepath.Join(bin, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_LOG\"\nif [ \"$1\" = \"list-sessions\" ]; then\nprintf '%s\\n' \"$TMUX_SESSIONS\"\nfi\n"
	if err := os.WriteFile(fakeTmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", log)
	t.Setenv("TMUX_SESSIONS", sessions)
	t.Setenv("TMUX", "")
	return log
}

func installFakeTmuxWithWindows(t *testing.T, windows string, paneLines int) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmux.log")
	fakeTmux := filepath.Join(bin, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_LOG\"\n" +
		"if [ \"$1\" = \"list-windows\" ]; then\nprintf '%s\\n' \"$TMUX_WINDOWS\"\nfi\n" +
		"if [ \"$1\" = \"capture-pane\" ]; then\ni=1\nwhile [ \"$i\" -le \"$TMUX_PANE_LINES\" ]; do\nprintf '%s line %s\\n' \"$4\" \"$i\"\ni=$((i+1))\ndone\nfi\n"
	if err := os.WriteFile(fakeTmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", log)
	t.Setenv("TMUX_WINDOWS", windows)
	t.Setenv("TMUX_PANE_LINES", strconv.Itoa(paneLines))
	return log
}

func changeCwd(t *testing.T, dir string) {
	t.Helper()
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(originalCwd)
	})
}

func clearFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func tmuxCommands(t *testing.T, log string) []string {
	t.Helper()
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func installFakeFZF(t *testing.T, selection string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "fzf.log")
	fakeFZF := filepath.Join(bin, "fzf")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$FZF_LOG.args\"\ncat > \"$FZF_LOG\"\nprintf '%s\\n' \"$FZF_SELECTION\"\n"
	if err := os.WriteFile(fakeFZF, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FZF_LOG", log)
	t.Setenv("FZF_SELECTION", selection)
	return log
}

func installFakeGitEditor(t *testing.T, message string) {
	t.Helper()
	bin := t.TempDir()
	editor := filepath.Join(bin, "editor")
	script := "#!/bin/sh\ncat > \"$1\" <<'EOF'\n" + message + "EOF\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_EDITOR", editor)
}

func makeRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

func createRemote(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	remote := filepath.Join(base, "remote.git")
	gitCmd(t, base, "init", "-b", "main", src)
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", "README.md")
	gitCmd(t, src, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	gitCmd(t, base, "clone", "--bare", src, remote)
	return remote
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
