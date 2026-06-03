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
	script := "#!/bin/sh\ncat > \"$FZF_LOG\"\nprintf '%s\\n' \"$FZF_SELECTION\"\n"
	if err := os.WriteFile(fakeFZF, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FZF_LOG", log)
	t.Setenv("FZF_SELECTION", selection)
	return log
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
