package devgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectNameFromURL(t *testing.T) {
	tests := map[string]string{
		"https://github.com/acme/widgets.git": "widgets",
		"git@github.com:acme/widgets.git":     "widgets",
		"ssh://git@example.com/acme/tool":     "tool",
		"/tmp/local/repo.git":                 "repo",
	}
	for input, want := range tests {
		if got := ProjectNameFromURL(input); got != want {
			t.Fatalf("ProjectNameFromURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWorktreeName(t *testing.T) {
	tests := map[string]string{
		"main":                    "main",
		"origin/main":             "main",
		"refs/heads/feature/test": "feature-test",
		"feature/nested/branch":   "feature-nested-branch",
	}
	for input, want := range tests {
		if got := WorktreeName(input); got != want {
			t.Fatalf("WorktreeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCloneCreatesBareRepoAndMainWorktree(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")

	result, err := Clone(root, remote, "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if result.Worktree != "main" {
		t.Fatalf("worktree = %q, want main", result.Worktree)
	}
	assertExists(t, filepath.Join(root, "widgets", ".bare", "HEAD"))
	assertExists(t, filepath.Join(root, "widgets", "main", "README.md"))

	fetch := gitOut(t, filepath.Join(root, "widgets"), "--git-dir", filepath.Join(root, "widgets", ".bare"), "config", "--get-all", "remote.origin.fetch")
	if strings.TrimSpace(fetch) != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("unexpected fetch refspec: %q", fetch)
	}
}

func TestMigratePreservesCheckoutAsDefaultWorktree(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	repo := filepath.Join(root, "widgets")
	gitCmd(t, root, "clone", remote, repo)
	gitCmd(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Migrate(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	wantWorktree, err := filepath.EvalSymlinks(filepath.Join(repo, "main"))
	if err != nil {
		t.Fatal(err)
	}
	if result.WorktreePath != wantWorktree {
		t.Fatalf("worktree path = %q", result.WorktreePath)
	}
	assertExists(t, filepath.Join(repo, ".bare", "HEAD"))
	assertExists(t, filepath.Join(repo, "main", "README.md"))
	if _, err := os.Stat(filepath.Join(repo, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected original .git path to be gone, err=%v", err)
	}
	status := gitOut(t, filepath.Join(repo, "main"), "status", "--porcelain", "--untracked-files=no")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("migrated worktree is dirty:\n%s", status)
	}
	assertExists(t, filepath.Join(repo, "main", "scratch.txt"))
	list := gitOut(t, repo, "--git-dir", filepath.Join(repo, ".bare"), "worktree", "list", "--porcelain")
	if !strings.Contains(list, "worktree "+result.WorktreePath) {
		t.Fatalf("worktree list missing preserved checkout:\n%s", list)
	}
}

func TestAddWorktreeCreatesBranchDirectory(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	if _, err := Clone(root, remote, "widgets"); err != nil {
		t.Fatal(err)
	}
	result, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	if result.Worktree != "feature-test" {
		t.Fatalf("worktree = %q, want feature-test", result.Worktree)
	}
	assertExists(t, filepath.Join(root, "widgets", "feature-test", "README.md"))
}

func TestAddWorktreeChecksOutExistingLocalBranch(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	if _, err := Clone(root, remote, "widgets"); err != nil {
		t.Fatal(err)
	}
	added, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	gitCmd(t, added.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "work")
	want := strings.TrimSpace(gitOut(t, added.WorktreePath, "rev-parse", "HEAD"))
	if _, err := RemoveWorktree(root, "widgets", "feature/test", RemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}

	// The branch still exists locally; a fresh `work` should check it out at its tip
	// rather than failing or branching from the start point.
	readded, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatalf("expected existing local branch to be checked out: %v", err)
	}
	got := strings.TrimSpace(gitOut(t, readded.WorktreePath, "rev-parse", "HEAD"))
	if got != want {
		t.Fatalf("HEAD = %q, want existing branch tip %q", got, want)
	}
}

func TestAddWorktreeChecksOutExistingRemoteBranch(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	if _, err := Clone(root, remote, "widgets"); err != nil {
		t.Fatal(err)
	}
	added, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	gitCmd(t, added.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "work")
	gitCmd(t, added.WorktreePath, "push", "-u", "origin", "feature/test")
	want := strings.TrimSpace(gitOut(t, added.WorktreePath, "rev-parse", "HEAD"))
	if _, err := RemoveWorktree(root, "widgets", "feature/test", RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatal(err)
	}

	// Only the remote branch remains; `work` should create a tracking branch from
	// origin/feature/test instead of branching from the start point.
	readded, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatalf("expected existing remote branch to be checked out: %v", err)
	}
	got := strings.TrimSpace(gitOut(t, readded.WorktreePath, "rev-parse", "HEAD"))
	if got != want {
		t.Fatalf("HEAD = %q, want remote branch tip %q", got, want)
	}
	upstream := strings.TrimSpace(gitOut(t, readded.WorktreePath, "rev-parse", "--abbrev-ref", "feature/test@{upstream}"))
	if upstream != "origin/feature/test" {
		t.Fatalf("upstream = %q, want origin/feature/test", upstream)
	}
}

func TestRemoveWorktreeRequiresPushedBranchAndDeletesLocalBranch(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	if _, err := Clone(root, remote, "widgets"); err != nil {
		t.Fatal(err)
	}
	added, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	gitCmd(t, added.WorktreePath, "push", "-u", "origin", "feature/test")

	removed, err := RemoveWorktree(root, "widgets", "feature/test", RemoveOptions{DeleteBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	if removed.WorktreePath != added.WorktreePath {
		t.Fatalf("removed path = %q, want %q", removed.WorktreePath, added.WorktreePath)
	}
	if _, err := os.Stat(added.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree to be removed, err=%v", err)
	}
	if _, err := gitOutputBare(filepath.Join(root, "widgets", ".bare"), "show-ref", "--verify", "--quiet", "refs/heads/feature/test"); err == nil {
		t.Fatal("expected local branch to be deleted")
	}
}

func TestRemoveWorktreeRejectsUnpushedBranch(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	if _, err := Clone(root, remote, "widgets"); err != nil {
		t.Fatal(err)
	}
	if _, err := AddWorktree(root, "widgets", "feature/test", "main"); err != nil {
		t.Fatal(err)
	}
	_, err := RemoveWorktree(root, "widgets", "feature/test", RemoveOptions{DeleteBranch: true})
	if err == nil || !strings.Contains(err.Error(), "no upstream") {
		t.Fatalf("expected no upstream error, got %v", err)
	}
}

func TestStatusReportsDirtyAheadBehindAndUpstream(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	result, err := Clone(root, remote, "widgets")
	if err != nil {
		t.Fatal(err)
	}

	status, err := Status(result.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" || status.Upstream != "origin/main" || status.Ahead != 0 || status.Behind != 0 || status.Dirty {
		t.Fatalf("clean status = %#v", status)
	}

	if err := os.WriteFile(filepath.Join(result.WorktreePath, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err = Status(result.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty || status.Changes != 0 {
		t.Fatalf("untracked status = %#v, want clean", status)
	}
	if err := os.WriteFile(filepath.Join(result.WorktreePath, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err = Status(result.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty || status.Changes != 1 {
		t.Fatalf("dirty status = %#v, want one change", status)
	}
	gitCmd(t, result.WorktreePath, "checkout", "--", "README.md")
	gitCmd(t, result.WorktreePath, "add", "local.txt")
	gitCmd(t, result.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "local")

	clone := filepath.Join(t.TempDir(), "clone")
	gitCmd(t, filepath.Dir(clone), "clone", remote, clone)
	if err := os.WriteFile(filepath.Join(clone, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, clone, "add", "remote.txt")
	gitCmd(t, clone, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "remote")
	gitCmd(t, clone, "push", "origin", "main")
	gitCmd(t, result.WorktreePath, "fetch", "origin")

	status, err = Status(result.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if status.Dirty || status.Ahead != 1 || status.Behind != 1 || !status.HasUpstream {
		t.Fatalf("diverged status = %#v, want clean ahead=1 behind=1", status)
	}
}

func TestUpdateMainWorktreeFetchesAndFastForwards(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	result, err := Clone(root, remote, "widgets")
	if err != nil {
		t.Fatal(err)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	gitCmd(t, filepath.Dir(clone), "clone", remote, clone)
	if err := os.WriteFile(filepath.Join(clone, "remote.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, clone, "add", "remote.txt")
	gitCmd(t, clone, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "remote")
	gitCmd(t, clone, "push", "origin", "main")

	updated, err := UpdateMainWorktree(root, "widgets")
	if err != nil {
		t.Fatal(err)
	}
	if updated.WorktreePath != result.WorktreePath {
		t.Fatalf("updated path = %q, want %q", updated.WorktreePath, result.WorktreePath)
	}
	assertExists(t, filepath.Join(result.WorktreePath, "remote.txt"))
}

func TestPushCurrentSetsUpstreamWhenMissing(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	if _, err := Clone(root, remote, "widgets"); err != nil {
		t.Fatal(err)
	}
	added, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(added.WorktreePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, added.WorktreePath, "add", "feature.txt")
	gitCmd(t, added.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "feature")

	result, err := PushCurrent(added.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch != "feature/test" || result.Upstream != "origin/feature/test" {
		t.Fatalf("push result = %#v", result)
	}
	upstream := gitOut(t, added.WorktreePath, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if strings.TrimSpace(upstream) != "origin/feature/test" {
		t.Fatalf("upstream = %q, want origin/feature/test", strings.TrimSpace(upstream))
	}
}

func TestRebaseCurrentRebasesOntoTarget(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t, "main")
	cloned, err := Clone(root, remote, "widgets")
	if err != nil {
		t.Fatal(err)
	}
	added, err := AddWorktree(root, "widgets", "feature/test", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloned.WorktreePath, "main.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, cloned.WorktreePath, "add", "main.txt")
	gitCmd(t, cloned.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "main")
	if err := os.WriteFile(filepath.Join(added.WorktreePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, added.WorktreePath, "add", "feature.txt")
	gitCmd(t, added.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "feature")

	if err := RebaseCurrent(added.WorktreePath, "main"); err != nil {
		t.Fatal(err)
	}
	mergeBase := strings.TrimSpace(gitOut(t, added.WorktreePath, "merge-base", "HEAD", "main"))
	mainHead := strings.TrimSpace(gitOut(t, cloned.WorktreePath, "rev-parse", "HEAD"))
	if mergeBase != mainHead {
		t.Fatalf("merge-base = %s, want main HEAD %s", mergeBase, mainHead)
	}
}

func createRemote(t *testing.T, branch string) string {
	t.Helper()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	remote := filepath.Join(base, "remote.git")
	gitCmd(t, base, "init", "-b", branch, src)
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, src, "add", "README.md")
	gitCmd(t, src, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "init")
	gitCmd(t, base, "clone", "--bare", src, remote)
	return remote
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}
