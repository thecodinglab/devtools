package devgit

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const bareDirName = ".bare"

type Result struct {
	Project      string
	Worktree     string
	ProjectPath  string
	BarePath     string
	WorktreePath string
}

type RemoveOptions struct {
	Force        bool
	DeleteBranch bool
	AllowMain    bool
}

type MergeResult struct {
	Result
	MainWorktree     string
	MainWorktreePath string
	Branch           string
}

type PushResult struct {
	Branch   string
	Upstream string
}

type WorktreeStatus struct {
	Branch      string
	Detached    bool
	Dirty       bool
	Changes     int
	Upstream    string
	Ahead       int
	Behind      int
	HasUpstream bool
}

type removePlan struct {
	result Result
	branch string
}

func InitProject(root, projectName string) (Result, error) {
	if err := validateName(projectName, "project"); err != nil {
		return Result{}, err
	}
	projectPath := filepath.Join(root, projectName)
	if exists(projectPath) {
		return Result{}, fmt.Errorf("%s already exists", projectPath)
	}
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		return Result{}, err
	}
	if err := git(projectPath, "init", "-b", "main"); err != nil {
		return Result{}, err
	}
	return Result{Project: projectName, Worktree: "main", ProjectPath: projectPath, WorktreePath: projectPath}, nil
}

func Clone(root, repoURL, projectName string) (Result, error) {
	if projectName == "" {
		projectName = ProjectNameFromURL(repoURL)
	}
	if err := validateName(projectName, "project"); err != nil {
		return Result{}, err
	}
	projectPath := filepath.Join(root, projectName)
	barePath := filepath.Join(projectPath, bareDirName)
	if exists(projectPath) {
		return Result{}, fmt.Errorf("%s already exists", projectPath)
	}
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		return Result{}, err
	}
	if err := git(projectPath, "clone", "--bare", repoURL, bareDirName); err != nil {
		return Result{}, err
	}
	if err := configureBare(barePath); err != nil {
		return Result{}, err
	}
	if err := gitBare(barePath, "fetch", "origin"); err != nil {
		return Result{}, err
	}
	branch, err := defaultBranch(barePath)
	if err != nil {
		return Result{}, err
	}
	worktreePath := filepath.Join(projectPath, branch)
	if err := gitBare(barePath, "worktree", "add", worktreePath, branch); err != nil {
		return Result{}, err
	}
	return Result{Project: projectName, Worktree: branch, ProjectPath: projectPath, BarePath: barePath, WorktreePath: worktreePath}, nil
}

func AddWorktree(root, project, branch, startPoint string) (Result, error) {
	if err := validateName(project, "project"); err != nil {
		return Result{}, err
	}
	if err := validateBranch(branch); err != nil {
		return Result{}, err
	}
	projectPath := filepath.Join(root, project)
	barePath := filepath.Join(projectPath, bareDirName)
	if !exists(barePath) {
		return Result{}, fmt.Errorf("bare repository not found: %s", barePath)
	}
	worktreeName := WorktreeName(branch)
	worktreePath := filepath.Join(projectPath, worktreeName)
	if exists(worktreePath) {
		return Result{}, fmt.Errorf("%s already exists", worktreePath)
	}
	args := []string{"worktree", "add"}
	if startPoint != "" {
		args = append(args, "-b", branch, worktreePath, startPoint)
	} else if localBranchExists(barePath, branch) {
		args = append(args, worktreePath, branch)
	} else {
		args = append(args, "-b", branch, worktreePath, "origin/"+branch)
	}
	if err := gitBare(barePath, args...); err != nil {
		return Result{}, err
	}
	return Result{Project: project, Worktree: worktreeName, ProjectPath: projectPath, BarePath: barePath, WorktreePath: worktreePath}, nil
}

func RemoveWorktree(root, project, worktree string, opts RemoveOptions) (Result, error) {
	plan, err := planRemoveWorktree(root, project, worktree, opts)
	if err != nil {
		return Result{}, err
	}
	barePath := plan.result.BarePath
	worktreePath := plan.result.WorktreePath

	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, worktreePath)
	if err := gitBare(barePath, args...); err != nil {
		return Result{}, err
	}
	if opts.DeleteBranch && plan.branch != "" {
		deleteArgs := []string{"branch", "-D", plan.branch}
		if err := gitBare(barePath, deleteArgs...); err != nil {
			return Result{}, err
		}
	}
	if err := gitBare(barePath, "worktree", "prune"); err != nil {
		return Result{}, err
	}
	return plan.result, nil
}

func MergeWorktreeToMain(root, project, worktree string) (MergeResult, error) {
	plan, err := planRemoveWorktree(root, project, worktree, RemoveOptions{
		Force:        true,
		DeleteBranch: true,
	})
	if err != nil {
		return MergeResult{}, err
	}
	if plan.branch == "main" || plan.branch == "master" {
		return MergeResult{}, fmt.Errorf("refusing to merge %s worktree into itself", plan.branch)
	}
	clean, err := isClean(plan.result.WorktreePath)
	if err != nil {
		return MergeResult{}, err
	}
	if !clean {
		return MergeResult{}, errors.New("worktree has uncommitted changes; commit or stash them before merging")
	}

	mainWorktree, mainWorktreePath, err := mainWorktree(root, project)
	if err != nil {
		return MergeResult{}, err
	}
	clean, err = isClean(mainWorktreePath)
	if err != nil {
		return MergeResult{}, err
	}
	if !clean {
		return MergeResult{}, errors.New("main worktree has uncommitted changes; commit or stash them before merging")
	}

	if err := git(mainWorktreePath, "merge", plan.branch); err != nil {
		return MergeResult{}, err
	}

	return MergeResult{
		Result:           plan.result,
		MainWorktree:     mainWorktree,
		MainWorktreePath: mainWorktreePath,
		Branch:           plan.branch,
	}, nil
}

func MainBranch(root, project string) (string, error) {
	worktree, _, err := mainWorktree(root, project)
	return worktree, err
}

func UpdateMainWorktree(root, project string) (Result, error) {
	projectPath := filepath.Join(root, project)
	barePath := filepath.Join(projectPath, bareDirName)
	if !exists(barePath) {
		return Result{}, fmt.Errorf("bare repository not found: %s", barePath)
	}
	if err := gitBare(barePath, "fetch", "--prune", "origin"); err != nil {
		return Result{}, err
	}
	worktree, worktreePath, err := mainWorktree(root, project)
	if err != nil {
		return Result{}, err
	}
	clean, err := isClean(worktreePath)
	if err != nil {
		return Result{}, err
	}
	if !clean {
		return Result{}, errors.New("main worktree has uncommitted changes; commit or stash them before updating")
	}
	status, err := Status(worktreePath)
	if err != nil {
		return Result{}, err
	}
	if !status.HasUpstream {
		return Result{}, fmt.Errorf("%s has no upstream", status.Branch)
	}
	if err := git(worktreePath, "merge", "--ff-only", status.Upstream); err != nil {
		return Result{}, err
	}
	return Result{Project: project, Worktree: worktree, ProjectPath: projectPath, BarePath: barePath, WorktreePath: worktreePath}, nil
}

func PushCurrent(path string) (PushResult, error) {
	branch, err := currentBranch(path)
	if err != nil {
		return PushResult{}, err
	}
	if branch == "" {
		return PushResult{}, errors.New("cannot push detached HEAD")
	}
	upstream, err := currentUpstream(path)
	if err != nil {
		if err := git(path, "push", "-u", "origin", branch); err != nil {
			return PushResult{}, err
		}
		return PushResult{Branch: branch, Upstream: "origin/" + branch}, nil
	}
	if err := git(path, "push"); err != nil {
		return PushResult{}, err
	}
	return PushResult{Branch: branch, Upstream: upstream}, nil
}

func RebaseCurrent(path, onto string) error {
	if onto == "" {
		onto = "main"
	}
	clean, err := isClean(path)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("worktree has uncommitted changes; commit or stash them before rebasing")
	}
	return git(path, "rebase", onto)
}

func PlanRemoveWorktree(root, project, worktree string, opts RemoveOptions) (Result, error) {
	plan, err := planRemoveWorktree(root, project, worktree, opts)
	if err != nil {
		return Result{}, err
	}
	return plan.result, nil
}

func planRemoveWorktree(root, project, worktree string, opts RemoveOptions) (removePlan, error) {
	if err := validateName(project, "project"); err != nil {
		return removePlan{}, err
	}
	if worktree == "" {
		return removePlan{}, errors.New("worktree name must not be empty")
	}
	projectPath := filepath.Join(root, project)
	barePath := filepath.Join(projectPath, bareDirName)
	if !exists(barePath) {
		return removePlan{}, fmt.Errorf("bare repository not found: %s", barePath)
	}
	worktreeName := WorktreeName(worktree)
	if !opts.AllowMain && (worktreeName == "main" || worktreeName == "master") {
		return removePlan{}, fmt.Errorf("refusing to remove %s worktree without --allow-main", worktreeName)
	}
	worktreePath := filepath.Join(projectPath, worktreeName)
	if !exists(worktreePath) {
		return removePlan{}, fmt.Errorf("worktree not found: %s", worktreePath)
	}
	branch, err := branchForWorktree(worktreePath)
	if err != nil {
		return removePlan{}, err
	}
	if branch == "" {
		return removePlan{}, fmt.Errorf("could not detect branch for %s", worktreePath)
	}
	if !opts.AllowMain && (branch == "main" || branch == "master") {
		return removePlan{}, fmt.Errorf("refusing to remove %s branch without --allow-main", branch)
	}
	if !opts.Force {
		clean, err := isClean(worktreePath)
		if err != nil {
			return removePlan{}, err
		}
		if !clean {
			return removePlan{}, errors.New("worktree has uncommitted changes; pass --force to remove anyway")
		}
		if err := isPushed(worktreePath); err != nil {
			return removePlan{}, err
		}
	}
	result := Result{Project: project, Worktree: worktreeName, ProjectPath: projectPath, BarePath: barePath, WorktreePath: worktreePath}
	return removePlan{result: result, branch: branch}, nil
}

func mainWorktree(root, project string) (string, string, error) {
	projectPath := filepath.Join(root, project)
	for _, worktree := range []string{"main", "master"} {
		path := filepath.Join(projectPath, worktree)
		if stat, err := os.Stat(path); err == nil && stat.IsDir() {
			return worktree, path, nil
		}
	}
	return "", "", fmt.Errorf("main worktree not found in %s", projectPath)
}

func InferProject(root, cwd string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	if canonicalRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = canonicalRoot
	}
	if canonicalCwd, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = canonicalCwd
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("%s is not inside %s", cwd, root)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return "", fmt.Errorf("could not infer project from %s", cwd)
	}
	project := parts[0]
	if !exists(filepath.Join(root, project, bareDirName)) {
		return "", fmt.Errorf("could not infer bare-layout project from %s", cwd)
	}
	return project, nil
}

func InferProjectWorktree(root, cwd string) (string, string, error) {
	project, err := InferProject(root, cwd)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return "", "", err
	}
	if canonicalRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = canonicalRoot
	}
	if canonicalCwd, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = canonicalCwd
	}
	rel, err := filepath.Rel(filepath.Join(root, project), cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", "", fmt.Errorf("could not infer worktree from %s", cwd)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" || parts[0] == bareDirName {
		return "", "", fmt.Errorf("could not infer worktree from %s", cwd)
	}
	return project, parts[0], nil
}

func CurrentHEAD(path string) (string, error) {
	out, err := gitOutput(path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func Status(path string) (WorktreeStatus, error) {
	branch, err := currentBranch(path)
	if err != nil {
		return WorktreeStatus{}, err
	}
	status := WorktreeStatus{Branch: branch}
	if status.Branch == "" {
		head, err := shortHEAD(path)
		if err != nil {
			return WorktreeStatus{}, err
		}
		status.Branch = "HEAD@" + head
		status.Detached = true
	}

	porcelain, err := trackedStatusPorcelain(path)
	if err != nil {
		return WorktreeStatus{}, err
	}
	lines := strings.Split(strings.TrimSpace(porcelain), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	status.Changes = len(lines)
	status.Dirty = status.Changes > 0

	upstream, err := currentUpstream(path)
	if err == nil {
		status.Upstream = upstream
	} else if !status.Detached {
		status.Upstream = inferredOriginBranch(path, status.Branch)
	}
	status.HasUpstream = status.Upstream != ""
	if !status.HasUpstream {
		return status, nil
	}
	ahead, behind, err := aheadBehind(path, status.Upstream)
	if err != nil {
		return WorktreeStatus{}, err
	}
	status.Ahead = ahead
	status.Behind = behind
	return status, nil
}

func Migrate(path string, allowDirty bool) (Result, error) {
	projectPath, err := filepath.Abs(path)
	if err != nil {
		return Result{}, err
	}
	if canonical, err := filepath.EvalSymlinks(projectPath); err == nil {
		projectPath = canonical
	}
	if !isNormalRepo(projectPath) {
		return Result{}, fmt.Errorf("%s is not a normal git repository", projectPath)
	}
	if !allowDirty {
		clean, err := isClean(projectPath)
		if err != nil {
			return Result{}, err
		}
		if !clean {
			return Result{}, errors.New("repository has uncommitted changes; pass --allow-dirty to migrate anyway")
		}
	}
	if err := git(projectPath, "remote", "get-url", "origin"); err != nil {
		return Result{}, errors.New("repository must have an origin remote")
	}
	branch, err := currentBranch(projectPath)
	if err != nil || branch == "" {
		branch, err = normalRepoDefaultBranch(projectPath)
		if err != nil {
			return Result{}, err
		}
	}
	worktreeName := WorktreeName(branch)
	barePath := filepath.Join(projectPath, bareDirName)
	worktreePath := filepath.Join(projectPath, worktreeName)
	if exists(barePath) {
		return Result{}, fmt.Errorf("%s already exists", barePath)
	}
	if exists(worktreePath) {
		return Result{}, fmt.Errorf("%s already exists", worktreePath)
	}

	gitPath := filepath.Join(projectPath, ".git")
	if err := os.Rename(gitPath, barePath); err != nil {
		return Result{}, err
	}
	rollback := true
	defer func() {
		if rollback {
			_ = os.Rename(barePath, gitPath)
		}
	}()

	if err := configureBare(barePath); err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		return Result{}, err
	}
	if err := gitBare(barePath, "worktree", "add", "--no-checkout", worktreePath, branch); err != nil {
		return Result{}, err
	}
	if err := moveCheckoutEntries(projectPath, worktreePath); err != nil {
		return Result{}, err
	}
	if err := git(worktreePath, "reset", "--mixed", "HEAD"); err != nil {
		return Result{}, err
	}
	project := filepath.Base(projectPath)
	rollback = false
	return Result{Project: project, Worktree: worktreeName, ProjectPath: projectPath, BarePath: barePath, WorktreePath: worktreePath}, nil
}

func ProjectNameFromURL(raw string) string {
	trimmed := strings.TrimSuffix(raw, "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
		trimmed = u.Path
	}
	if idx := strings.LastIndex(trimmed, ":"); idx >= 0 && !strings.Contains(trimmed[idx+1:], "/") {
		trimmed = trimmed[idx+1:]
	}
	base := filepath.Base(trimmed)
	if base == "." || base == string(filepath.Separator) {
		return "repo"
	}
	return base
}

func WorktreeName(branch string) string {
	name := strings.TrimSpace(branch)
	name = strings.TrimPrefix(name, "refs/heads/")
	name = strings.TrimPrefix(name, "origin/")
	name = strings.ReplaceAll(name, string(filepath.Separator), "-")
	name = strings.ReplaceAll(name, "/", "-")
	return name
}

func validateName(name, kind string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	if strings.Contains(name, "/") || strings.Contains(name, string(filepath.Separator)) {
		return fmt.Errorf("%s name must not contain path separators: %q", kind, name)
	}
	return nil
}

func validateBranch(branch string) error {
	if strings.TrimSpace(branch) == "" {
		return errors.New("branch name must not be empty")
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", branch)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("invalid branch name %q: %s", branch, msg)
	}
	return nil
}

func configureBare(barePath string) error {
	if err := gitBare(barePath, "config", "core.bare", "true"); err != nil {
		return err
	}
	_ = gitBare(barePath, "config", "--unset-all", "remote.origin.fetch")
	return gitBare(barePath, "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
}

func defaultBranch(barePath string) (string, error) {
	if out, err := gitOutputBare(barePath, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch := strings.TrimSpace(out)
		if strings.HasPrefix(branch, "origin/") {
			return strings.TrimPrefix(branch, "origin/"), nil
		}
	}
	for _, branch := range []string{"main", "master"} {
		if remoteBranchExists(barePath, branch) {
			return branch, nil
		}
		if localBranchExists(barePath, branch) {
			return branch, nil
		}
	}
	return "", errors.New("could not detect default branch; expected origin/HEAD, main, or master")
}

func normalRepoDefaultBranch(path string) (string, error) {
	if out, err := gitOutput(path, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch := strings.TrimSpace(out)
		if strings.HasPrefix(branch, "origin/") {
			return strings.TrimPrefix(branch, "origin/"), nil
		}
	}
	for _, branch := range []string{"main", "master"} {
		if _, err := gitOutput(path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			return branch, nil
		}
	}
	return "", errors.New("could not detect default branch; expected main or master")
}

func currentBranch(path string) (string, error) {
	out, err := gitOutput(path, "branch", "--show-current")
	return strings.TrimSpace(out), err
}

func shortHEAD(path string) (string, error) {
	out, err := gitOutput(path, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out), err
}

func inferredOriginBranch(path, branch string) string {
	remote := "origin/" + branch
	if _, err := gitOutput(path, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote); err != nil {
		return ""
	}
	return remote
}

func branchForWorktree(path string) (string, error) {
	out, err := gitOutput(path, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func currentUpstream(path string) (string, error) {
	upstream, err := gitOutput(path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	return strings.TrimSpace(upstream), err
}

func aheadBehind(path, upstream string) (int, int, error) {
	out, err := gitOutput(path, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("unexpected ahead/behind output: %q", strings.TrimSpace(out))
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, err
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

func remoteBranchExists(barePath, branch string) bool {
	_, err := gitOutputBare(barePath, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

func localBranchExists(barePath, branch string) bool {
	_, err := gitOutputBare(barePath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func isClean(path string) (bool, error) {
	out, err := trackedStatusPorcelain(path)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

func trackedStatusPorcelain(path string) (string, error) {
	return gitOutput(path, "status", "--porcelain", "--untracked-files=no")
}

func isPushed(path string) error {
	upstream, err := gitOutput(path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return errors.New("branch has no upstream; push it or pass --force")
	}
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return errors.New("branch has no upstream; push it or pass --force")
	}
	if err := git(path, "merge-base", "--is-ancestor", "HEAD", upstream); err != nil {
		return fmt.Errorf("branch is not fully pushed to %s; push it or pass --force", upstream)
	}
	return nil
}

func isNormalRepo(path string) bool {
	if !exists(filepath.Join(path, ".git")) {
		return false
	}
	out, err := gitOutput(path, "rev-parse", "--is-bare-repository")
	return err == nil && strings.TrimSpace(out) == "false"
}

func moveCheckoutEntries(projectPath, worktreePath string) error {
	entries, err := os.ReadDir(projectPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == bareDirName || filepath.Join(projectPath, name) == worktreePath {
			continue
		}
		if err := os.Rename(filepath.Join(projectPath, name), filepath.Join(worktreePath, name)); err != nil {
			return err
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func git(dir string, args ...string) error {
	_, err := gitOutput(dir, args...)
	return err
}

func gitBare(barePath string, args ...string) error {
	_, err := gitOutputBare(barePath, args...)
	return err
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return run(cmd)
}

func gitOutputBare(barePath string, args ...string) (string, error) {
	fullArgs := append([]string{"--git-dir", barePath}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = filepath.Dir(barePath)
	return run(cmd)
}

func run(cmd *exec.Cmd) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(cmd.Args[1:], " "), msg)
	}
	return stdout.String(), nil
}
