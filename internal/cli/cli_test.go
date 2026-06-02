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

func TestShortWorktreeCommandInfersProjectFromCurrentDirectory(t *testing.T) {
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
	if code := Run([]string{"w", "feature/cli"}, &stdout, &stderr); code != 0 {
		t.Fatalf("w returned %d, stderr=%s", code, stderr.String())
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

func installFakeTmux(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmux.log")
	fakeTmux := filepath.Join(bin, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TMUX_LOG\"\n"
	if err := os.WriteFile(fakeTmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", log)
	t.Setenv("TMUX", "")
	return log
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
