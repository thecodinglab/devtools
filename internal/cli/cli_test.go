package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	if len(lines) != 3 {
		t.Fatalf("stdout = %q, want header plus two current-project rows", got)
	}
	if strings.Contains(got, "gadgets/main") {
		t.Fatalf("default status should only show current project, got %q", got)
	}
	if !strings.Contains(lines[0], "WORKTREE") || !strings.Contains(lines[0], "UPSTREAM") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "widgets/feature-test") || !strings.Contains(lines[1], "feature/test") || !strings.Contains(lines[1], "dirty(1)") || !strings.Contains(lines[1], "-") {
		t.Fatalf("feature row = %q", lines[1])
	}
	if !strings.Contains(lines[2], "widgets/main") || !strings.Contains(lines[2], "main") || !strings.Contains(lines[2], "clean") || !strings.Contains(lines[2], "origin/main") {
		t.Fatalf("main row = %q", lines[2])
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status", "--all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status --all returned %d, stderr=%s", code, stderr.String())
	}
	got = strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "gadgets/main") || !strings.Contains(got, "widgets/feature-test") || !strings.Contains(got, "widgets/main") {
		t.Fatalf("status --all output = %q, want all worktrees", got)
	}

	changeCwd(t, t.TempDir())
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status outside project returned %d, stderr=%s", code, stderr.String())
	}
	got = strings.TrimSpace(stdout.String())
	if !strings.Contains(got, "gadgets/main") || !strings.Contains(got, "widgets/feature-test") || !strings.Contains(got, "widgets/main") {
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

func TestSwitchAmbiguousQueryUsesPickerWithMatches(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "widgets")
	mainPath := filepath.Join(project, "main")
	featurePath := filepath.Join(project, "feature-cli")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, project, "init", "--bare", ".bare")
	makeRepo(t, mainPath)
	makeRepo(t, featurePath)

	tmuxLog := installFakeTmux(t)
	fzfLog := installFakeFZF(t, "widgets/feature-cli\t"+featurePath)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"switch", "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
	fzfInput, err := os.ReadFile(fzfLog)
	if err != nil {
		t.Fatal(err)
	}
	gotInput := strings.TrimSpace(string(fzfInput))
	wantInput := "widgets/feature-cli\t" + featurePath + "\nwidgets/main\t" + mainPath
	if gotInput != wantInput {
		t.Fatalf("fzf input = %q, want %q", gotInput, wantInput)
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

func TestSessionsCommandUsesPickerWithActiveTmuxSessions(t *testing.T) {
	log := installFakeTmuxWithSessions(t, "work\t1\t0\nops\t2\t1")
	fzfLog := installFakeFZF(t, "ops\t2 windows\tattached")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"sessions"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sessions returned %d, stderr=%s", code, stderr.String())
	}
	fzfInput, err := os.ReadFile(fzfLog)
	if err != nil {
		t.Fatal(err)
	}
	gotInput := strings.TrimSpace(string(fzfInput))
	wantInput := "work\t1 windows\nops\t2 windows\tattached"
	if gotInput != wantInput {
		t.Fatalf("fzf input = %q, want %q", gotInput, wantInput)
	}
	fzfArgs, err := os.ReadFile(fzfLog + ".args")
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.TrimSpace(string(fzfArgs))
	wantArgs := "--with-nth=1 --delimiter=\t --preview tmux capture-pane -ep -t {1}"
	if gotArgs != wantArgs {
		t.Fatalf("fzf args = %q, want %q", gotArgs, wantArgs)
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
