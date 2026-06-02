package discovery

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanFindsNormalReposAndBareWorktrees(t *testing.T) {
	root := t.TempDir()
	normal := filepath.Join(root, "small")
	makeRepo(t, normal)

	bareProject := filepath.Join(root, "widgets")
	main := filepath.Join(bareProject, "main")
	if err := os.MkdirAll(bareProject, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, bareProject, "init", "--bare", ".bare")
	makeRepo(t, main)

	targets, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	got := labels(targets)
	want := "small,widgets/main"
	if got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
}

func TestResolveRejectsAmbiguousQueries(t *testing.T) {
	targets := []Target{
		{Label: "alpha/main", Path: "/tmp/alpha/main"},
		{Label: "alpha/feature", Path: "/tmp/alpha/feature"},
	}
	if _, err := Resolve(targets, "alpha"); err == nil {
		t.Fatal("expected ambiguous query error")
	}
}

func TestResolveMatchesReturnsAmbiguousMatches(t *testing.T) {
	targets := []Target{
		{Label: "alpha/main", Path: "/tmp/alpha/main"},
		{Label: "alpha/feature", Path: "/tmp/alpha/feature"},
		{Label: "beta/main", Path: "/tmp/beta/main"},
	}
	matches, err := ResolveMatches(targets, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	got := labels(matches)
	want := "alpha/main,alpha/feature"
	if got != want {
		t.Fatalf("matches = %q, want %q", got, want)
	}
}

func labels(targets []Target) string {
	parts := make([]string, len(targets))
	for i, target := range targets {
		parts[i] = target.Label
	}
	return strings.Join(parts, ",")
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

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
