package discovery

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Target struct {
	Label    string
	Project  string
	Worktree string
	Path     string
	Kind     string
}

func Scan(root string) ([]Target, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var targets []Target
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		project := entry.Name()
		projectPath := filepath.Join(root, project)
		barePath := filepath.Join(projectPath, ".bare")
		if isGitDir(barePath) {
			worktrees, err := os.ReadDir(projectPath)
			if err != nil {
				return nil, err
			}
			for _, wt := range worktrees {
				if !wt.IsDir() || wt.Name() == ".bare" {
					continue
				}
				wtPath := filepath.Join(projectPath, wt.Name())
				if isWorktree(wtPath) {
					targets = append(targets, Target{
						Label:    project + "/" + wt.Name(),
						Project:  project,
						Worktree: wt.Name(),
						Path:     wtPath,
						Kind:     "worktree",
					})
				}
			}
			continue
		}
		if isWorktree(projectPath) {
			targets = append(targets, Target{
				Label:    project,
				Project:  project,
				Worktree: "main",
				Path:     projectPath,
				Kind:     "repo",
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Label < targets[j].Label
	})
	return targets, nil
}

func Resolve(targets []Target, query string) (Target, error) {
	matches, err := ResolveMatches(targets, query)
	if err != nil {
		return Target{}, err
	}
	if len(matches) > 1 {
		labels := make([]string, len(matches))
		for i, match := range matches {
			labels[i] = match.Label
		}
		return Target{}, fmt.Errorf("ambiguous target %q: %s", query, strings.Join(labels, ", "))
	}
	return matches[0], nil
}

func ResolveMatches(targets []Target, query string) ([]Target, error) {
	if query == "" {
		return nil, errors.New("empty target")
	}
	if abs, err := filepath.Abs(query); err == nil && isWorktree(abs) {
		base := filepath.Base(abs)
		parent := filepath.Base(filepath.Dir(abs))
		return []Target{{Label: parent + "/" + base, Project: parent, Worktree: base, Path: abs, Kind: "path"}}, nil
	}
	var matches []Target
	for _, target := range targets {
		if target.Label == query || target.Path == query || strings.Contains(target.Label, query) {
			matches = append(matches, target)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no target matches %q", query)
	}
	return matches, nil
}

func isGitDir(path string) bool {
	stat, err := os.Stat(path)
	if err != nil || !stat.IsDir() {
		return false
	}
	return exists(filepath.Join(path, "HEAD")) && exists(filepath.Join(path, "objects"))
}

func isWorktree(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
