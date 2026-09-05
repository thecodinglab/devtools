package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"devtools/internal/config"
	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/picker"
	"devtools/internal/tmux"

	"github.com/spf13/cobra"
)

type globals struct {
	roots     []string
	bookmarks []config.Bookmark
}

func (g globals) primaryRoot() string {
	return g.roots[0]
}

func Run(args []string, stdout, stderr io.Writer) int {
	cmd := newRootCommand(stdout, stderr)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type app struct {
	roots []string
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	a := &app{}
	cmd := &cobra.Command{
		Use:           "devtools [--root PATH]... <command> [args]",
		Short:         "Manage dev worktrees and tmux sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().StringArrayVar(&a.roots, "root", nil, "workspace root (repeatable; overrides DEVTOOLS_ROOT and config roots)")
	cmd.AddCommand(
		newProjectCommand(a, stdout),
		cloneCommand(a, stdout),
		migrateCommand(a, stdout),
		newWorktreeCommand(a, stdout),
		mergeCommand(a, stdout),
		updateCommand(a, stdout),
		pushCommand(stdout),
		rebaseCommand(a, stdout),
		nukeCommand(stdout),
		removeWorktreeCommand(a, stdout),
		listCommand(a, stdout),
		statusCommand(a, stdout),
		switchCommand(a),
		pickCommand(a),
		bookmarkCommand(a, stdout),
		sessionsCommand(),
		sessionPreviewCommand(stdout),
	)
	return cmd
}

func (a *app) globals() (globals, error) {
	cfgPath, err := config.Path()
	if err != nil {
		return globals{}, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return globals{}, err
	}
	roots := a.roots
	if len(roots) == 0 {
		if env := os.Getenv("DEVTOOLS_ROOT"); env != "" {
			roots = filepath.SplitList(env)
		}
	}
	if len(roots) == 0 {
		roots = cfg.Roots
	}
	if len(roots) == 0 {
		roots = []string{"~/dev"}
	}
	var g globals
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		abs, err := expandPath(root)
		if err != nil {
			return globals{}, err
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		g.roots = append(g.roots, abs)
	}
	if len(g.roots) == 0 {
		return globals{}, errors.New("no workspace roots configured")
	}
	for _, bookmark := range cfg.Bookmarks {
		abs, err := expandPath(bookmark.Path)
		if err != nil {
			return globals{}, err
		}
		g.bookmarks = append(g.bookmarks, config.Bookmark{Name: bookmark.Name, Path: abs})
	}
	return g, nil
}

func scanTargets(g globals) ([]discovery.Target, error) {
	var targets []discovery.Target
	seen := map[string]bool{}
	for _, root := range g.roots {
		scanned, err := discovery.Scan(root)
		if err != nil {
			return nil, err
		}
		for _, target := range scanned {
			if seen[target.Path] {
				continue
			}
			seen[target.Path] = true
			targets = append(targets, target)
		}
	}
	sortTargets(targets)
	return targets, nil
}

func withBookmarks(targets []discovery.Target, bookmarks []config.Bookmark) []discovery.Target {
	for _, bookmark := range bookmarks {
		if stat, err := os.Stat(bookmark.Path); err != nil || !stat.IsDir() {
			continue
		}
		targets = append(targets, discovery.Target{
			Label:   bookmark.Name,
			Project: bookmark.Name,
			Path:    bookmark.Path,
			Kind:    "bookmark",
		})
	}
	sortTargets(targets)
	return targets
}

func sortTargets(targets []discovery.Target) {
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Label < targets[j].Label
	})
}

func inferProject(g globals, cwd string) (string, string, error) {
	for _, root := range g.roots {
		project, err := devgit.InferProject(root, cwd)
		if err == nil {
			return root, project, nil
		}
	}
	return "", "", fmt.Errorf("%s is not inside a managed project under any workspace root (%s)", cwd, strings.Join(g.roots, ", "))
}

func inferProjectWorktree(g globals, cwd string) (string, string, string, error) {
	root, _, err := inferProject(g, cwd)
	if err != nil {
		return "", "", "", err
	}
	project, worktree, err := devgit.InferProjectWorktree(root, cwd)
	if err != nil {
		return "", "", "", err
	}
	return root, project, worktree, nil
}

func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Abs(path)
}

func newProjectCommand(a *app, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "new <project-name>",
		Short: "Create a new project",
		Args:  usageExactArgs(1, "usage: devtools new <project-name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			result, err := devgit.InitProject(g.primaryRoot(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.ProjectPath)
			return tmux.Switch(result.ProjectPath, tmux.SessionName(result.Project, result.Worktree))
		},
	}
}

func cloneCommand(a *app, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "clone <repo-url> [project-name]",
		Short: "Clone a repository into the workspace",
		Args:  usageArgRange(1, 2, "usage: devtools clone <repo-url> [project-name]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			project := ""
			if len(args) == 2 {
				project = args[1]
			}
			result, err := devgit.Clone(g.primaryRoot(), args[0], project)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.WorktreePath)
			return tmux.Switch(result.WorktreePath, tmux.SessionName(result.Project, result.Worktree))
		},
	}
}

func migrateCommand(a *app, stdout io.Writer) *cobra.Command {
	var allowDirty bool
	cmd := &cobra.Command{
		Use:   "migrate [path]",
		Short: "Migrate an existing checkout",
		Args:  usageMaximumArgs(1, "usage: devtools migrate [--allow-dirty] [path]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			abs, err := expandPath(path)
			if err != nil {
				return err
			}
			result, err := devgit.Migrate(abs, allowDirty)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.WorktreePath)
			return tmux.Switch(result.WorktreePath, tmux.SessionName(result.Project, result.Worktree))
		},
	}
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "allow migrating a dirty checkout")
	return cmd
}

func newWorktreeCommand(a *app, stdout io.Writer) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "work <branch>",
		Short: "Create a new worktree",
		Args:  usageExactArgs(1, "usage: devtools work <branch> [--from <start-point>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, project, err := inferProject(g, cwd)
			if err != nil {
				return err
			}
			startPoint := from
			if startPoint == "" {
				startPoint, err = devgit.CurrentHEAD(cwd)
				if err != nil {
					return err
				}
			}
			result, err := devgit.AddWorktree(root, project, args[0], startPoint)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.WorktreePath)
			return tmux.Switch(result.WorktreePath, tmux.SessionName(result.Project, result.Worktree))
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "start point")
	return cmd
}

func mergeCommand(a *app, stdout io.Writer) *cobra.Command {
	var squash bool
	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Merge the current worktree into main and remove it",
		Args:  usageNoArgs("usage: devtools merge [--squash]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, project, worktree, err := inferProjectWorktree(g, cwd)
			if err != nil {
				return err
			}
			result, err := devgit.MergeWorktreeToMain(root, project, worktree, devgit.MergeOptions{
				Squash: squash,
			})
			if err != nil {
				return err
			}
			restoreHangup := ignoreHangup()
			defer restoreHangup()
			if err := tmux.CloseForRemoval(
				tmux.SessionName(result.Project, result.Worktree),
				tmux.SessionName(result.Project, result.MainWorktree),
				result.MainWorktreePath,
			); err != nil {
				return err
			}
			_, err = devgit.RemoveWorktree(root, project, worktree, devgit.RemoveOptions{
				Force:        true,
				DeleteBranch: true,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.MainWorktreePath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&squash, "squash", false, "squash the worktree branch into a single commit")
	return cmd
}

func updateCommand(a *app, stdout io.Writer) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Fast-forward main worktrees",
		Args:  usageNoArgs("usage: devtools update [--all]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			if all {
				for _, root := range g.roots {
					if err := updateAllMainWorktrees(root, stdout); err != nil {
						return err
					}
				}
				return nil
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, project, err := inferProject(g, cwd)
			if err != nil {
				return err
			}
			result, err := devgit.UpdateMainWorktree(root, project)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.WorktreePath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "update main worktrees for all managed projects")
	return cmd
}

func updateAllMainWorktrees(root string, stdout io.Writer) error {
	targets, err := discovery.Scan(root)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, target := range targets {
		if target.Kind != "worktree" || seen[target.Project] || (target.Worktree != "main" && target.Worktree != "master") {
			continue
		}
		seen[target.Project] = true
		result, err := devgit.UpdateMainWorktree(root, target.Project)
		if err != nil {
			return fmt.Errorf("%s: %w", target.Project, err)
		}
		fmt.Fprintln(stdout, result.WorktreePath)
	}
	return nil
}

func pushCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Push the current branch",
		Args:  usageNoArgs("usage: devtools push"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			result, err := devgit.PushCurrent(cwd)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s\t%s\n", result.Branch, result.Upstream)
			return nil
		},
	}
}

func rebaseCommand(a *app, stdout io.Writer) *cobra.Command {
	var onto string
	cmd := &cobra.Command{
		Use:   "rebase",
		Short: "Rebase the current worktree",
		Args:  usageNoArgs("usage: devtools rebase [--onto <base>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if onto == "" {
				onto = defaultBaseRef(a, cwd)
			}
			if err := devgit.RebaseCurrent(cwd, onto); err != nil {
				return err
			}
			fmt.Fprintln(stdout, onto)
			return nil
		},
	}
	cmd.Flags().StringVar(&onto, "onto", "", "base ref")
	return cmd
}

func defaultBaseRef(a *app, cwd string) string {
	g, err := a.globals()
	if err != nil {
		return "main"
	}
	root, project, err := inferProject(g, cwd)
	if err != nil {
		return "main"
	}
	branch, err := devgit.MainBranch(root, project)
	if err != nil {
		return "main"
	}
	return branch
}

func nukeCommand(stdout io.Writer) *cobra.Command {
	var includeIgnored bool
	cmd := &cobra.Command{
		Use:   "nuke",
		Short: "Discard all changes in the current worktree",
		Args:  usageNoArgs("usage: devtools nuke [--include-ignored]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := devgit.NukeWorktree(cwd, includeIgnored); err != nil {
				return err
			}
			fmt.Fprintln(stdout, cwd)
			return nil
		},
	}
	cmd.Flags().BoolVar(&includeIgnored, "include-ignored", false, "also remove git-ignored files")
	return cmd
}

func removeWorktreeCommand(a *app, stdout io.Writer) *cobra.Command {
	var force, keepBranch, allowMain bool
	cmd := &cobra.Command{
		Use:   "done [worktree]",
		Short: "Remove a worktree",
		Args:  usageMaximumArgs(1, "usage: devtools done [worktree] [--force] [--keep-branch] [--allow-main]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, project, worktree, err := inferProjectWorktree(g, cwd)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				worktree = args[0]
			}
			opts := devgit.RemoveOptions{
				Force:        force,
				DeleteBranch: !keepBranch,
				AllowMain:    allowMain,
			}
			plan, err := devgit.PlanRemoveWorktree(root, project, worktree, opts)
			if err != nil {
				return err
			}
			fallbackWorktree, fallbackPath := removalFallback(root, project, plan.Worktree)
			restoreHangup := ignoreHangup()
			defer restoreHangup()
			if err := tmux.CloseForRemoval(
				tmux.SessionName(plan.Project, plan.Worktree),
				tmux.SessionName(plan.Project, fallbackWorktree),
				fallbackPath,
			); err != nil {
				return err
			}
			result, err := devgit.RemoveWorktree(root, project, worktree, opts)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.WorktreePath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove without clean/pushed checks")
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "keep the local branch")
	cmd.Flags().BoolVar(&allowMain, "allow-main", false, "allow removing main or master")
	return cmd
}

func ignoreHangup() func() {
	wasIgnored := signal.Ignored(syscall.SIGHUP)
	signal.Ignore(syscall.SIGHUP)
	return func() {
		if !wasIgnored {
			signal.Reset(syscall.SIGHUP)
		}
	}
}

func removalFallback(root, project, removingWorktree string) (string, string) {
	projectPath := filepath.Join(root, project)
	if removingWorktree == "main" || removingWorktree == "master" {
		return removingWorktree, filepath.Join(projectPath, removingWorktree)
	}
	for _, worktree := range []string{"main", "master"} {
		path := filepath.Join(projectPath, worktree)
		if stat, err := os.Stat(path); err == nil && stat.IsDir() {
			return worktree, path
		}
	}
	return removingWorktree, filepath.Join(projectPath, removingWorktree)
}

func listCommand(a *app, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List known worktrees",
		Args:  usageNoArgs("usage: devtools list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			targets, err := scanTargets(g)
			if err != nil {
				return err
			}
			targets = withBookmarks(targets, g.bookmarks)
			for _, target := range targets {
				fmt.Fprintf(stdout, "%s\t%s\n", target.Label, target.Path)
			}
			return nil
		},
	}
}

func statusCommand(a *app, stdout io.Writer) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show worktree status dashboard",
		Args:  usageNoArgs("usage: devtools status [--all]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			targets, err := scanTargets(g)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			targets = statusTargets(targets, cwd, all)
			rows := make([]statusRow, 0, len(targets))
			for _, target := range targets {
				status, err := devgit.Status(target.Path)
				if err != nil {
					return fmt.Errorf("%s: %w", target.Label, err)
				}
				rows = append(rows, newStatusRow(target, status))
			}
			printStatusRows(stdout, rows)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show all worktrees under the workspace root")
	return cmd
}

func statusTargets(targets []discovery.Target, cwd string, all bool) []discovery.Target {
	if all {
		return targets
	}
	current, ok := currentTarget(targets, cwd)
	if !ok {
		return targets
	}
	filtered := make([]discovery.Target, 0, len(targets))
	for _, target := range targets {
		if target.Project == current.Project {
			filtered = append(filtered, target)
		}
	}
	return filtered
}

func currentTarget(targets []discovery.Target, cwd string) (discovery.Target, bool) {
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return discovery.Target{}, false
	}
	if canonical, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = canonical
	}
	for _, target := range targets {
		path, err := filepath.Abs(target.Path)
		if err != nil {
			continue
		}
		if canonical, err := filepath.EvalSymlinks(path); err == nil {
			path = canonical
		}
		rel, err := filepath.Rel(path, cwd)
		if err == nil && (rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..") {
			return target, true
		}
	}
	return discovery.Target{}, false
}

type statusRow struct {
	Worktree string
	Branch   string
	State    string
	Ahead    string
	Behind   string
	Upstream string
}

func newStatusRow(target discovery.Target, status devgit.WorktreeStatus) statusRow {
	state := "clean"
	if status.Dirty {
		state = fmt.Sprintf("dirty(%d)", status.Changes)
	}
	ahead := "-"
	behind := "-"
	upstream := "-"
	if status.HasUpstream {
		ahead = fmt.Sprint(status.Ahead)
		behind = fmt.Sprint(status.Behind)
		upstream = status.Upstream
	}
	return statusRow{
		Worktree: target.Label,
		Branch:   status.Branch,
		State:    state,
		Ahead:    ahead,
		Behind:   behind,
		Upstream: upstream,
	}
}

func printStatusRows(stdout io.Writer, rows []statusRow) {
	widths := []int{len("WORKTREE"), len("BRANCH"), len("STATE"), len("AHEAD"), len("BEHIND"), len("UPSTREAM")}
	for _, row := range rows {
		widths[0] = max(widths[0], len(row.Worktree))
		widths[1] = max(widths[1], len(row.Branch))
		widths[2] = max(widths[2], len(row.State))
		widths[3] = max(widths[3], len(row.Ahead))
		widths[4] = max(widths[4], len(row.Behind))
		widths[5] = max(widths[5], len(row.Upstream))
	}
	format := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%%ds  %%%ds  %%-%ds\n", widths[0], widths[1], widths[2], widths[3], widths[4], widths[5])
	fmt.Fprintf(stdout, format, "WORKTREE", "BRANCH", "STATE", "AHEAD", "BEHIND", "UPSTREAM")
	for _, row := range rows {
		fmt.Fprintf(stdout, format, row.Worktree, row.Branch, row.State, row.Ahead, row.Behind, row.Upstream)
	}
}

func switchCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "switch [path-or-query]",
		Short: "Switch to a worktree session",
		Args:  usageMaximumArgs(1, "usage: devtools switch [path-or-query]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			targets, err := scanTargets(g)
			if err != nil {
				return err
			}
			targets = withBookmarks(targets, g.bookmarks)
			var target discovery.Target
			if len(args) == 0 {
				target, err = picker.Select(targets)
			} else {
				var matches []discovery.Target
				matches, err = discovery.ResolveMatches(targets, args[0])
				if err == nil {
					if len(matches) == 1 {
						target = matches[0]
					} else {
						target, err = picker.Select(matches)
					}
				}
			}
			if err != nil {
				return err
			}
			return tmux.Switch(target.Path, tmux.SessionName(target.Project, target.Worktree))
		},
	}
}

func pickCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "pick",
		Short: "Pick a worktree session",
		Args:  usageNoArgs("usage: devtools pick"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			targets, err := scanTargets(g)
			if err != nil {
				return err
			}
			targets = withBookmarks(targets, g.bookmarks)
			target, err := picker.Select(targets)
			if err != nil {
				return err
			}
			return tmux.Switch(target.Path, tmux.SessionName(target.Project, target.Worktree))
		},
	}
}

func sessionPreviewCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "session-preview <session>",
		Short:  "Render the fzf preview for a tmux session",
		Hidden: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("usage: devtools session-preview <session>")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			windows, err := tmux.ListWindows(args[0])
			if err != nil {
				return err
			}
			height := previewHeight()
			for i, window := range windows {
				lines := height / len(windows)
				if i < height%len(windows) {
					lines++
				}
				if lines < 1 {
					break
				}
				fmt.Fprintf(stdout, "\x1b[1m── %d: %s ──\x1b[0m\n", window.Index, window.Name)
				content, err := tmux.CapturePane(window.ID)
				if err != nil {
					return err
				}
				for _, line := range tailLines(content, lines-1) {
					fmt.Fprintln(stdout, line)
				}
			}
			return nil
		},
	}
}

func previewHeight() int {
	if lines, err := strconv.Atoi(os.Getenv("FZF_PREVIEW_LINES")); err == nil && lines > 0 {
		return lines
	}
	return 40
}

func tailLines(content string, max int) []string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

func bookmarkCommand(a *app, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bookmark <command>",
		Short: "Manage bookmarked directories",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		bookmarkAddCommand(stdout),
		bookmarkRemoveCommand(stdout),
		bookmarkListCommand(a, stdout),
	)
	return cmd
}

func bookmarkAddCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> [path]",
		Short: "Bookmark a directory",
		Args:  usageArgRange(1, 2, "usage: devtools bookmark add <name> [path]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if strings.ContainsAny(name, " \t") {
				return fmt.Errorf("bookmark name %q must not contain whitespace", name)
			}
			path := "."
			if len(args) == 2 {
				path = args[1]
			}
			abs, err := expandPath(path)
			if err != nil {
				return err
			}
			if stat, err := os.Stat(abs); err != nil || !stat.IsDir() {
				return fmt.Errorf("%s is not a directory", abs)
			}
			cfgPath, err := config.Path()
			if err != nil {
				return err
			}
			if err := config.AddBookmark(cfgPath, name, abs); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s\t%s\n", name, abs)
			return nil
		},
	}
}

func bookmarkRemoveCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a bookmark",
		Args:  usageExactArgs(1, "usage: devtools bookmark remove <name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, err := config.Path()
			if err != nil {
				return err
			}
			if err := config.RemoveBookmark(cfgPath, args[0]); err != nil {
				return err
			}
			fmt.Fprintln(stdout, args[0])
			return nil
		},
	}
}

func bookmarkListCommand(a *app, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List bookmarks",
		Args:  usageNoArgs("usage: devtools bookmark list"),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := a.globals()
			if err != nil {
				return err
			}
			for _, bookmark := range g.bookmarks {
				fmt.Fprintf(stdout, "%s\t%s\n", bookmark.Name, bookmark.Path)
			}
			return nil
		},
	}
}

func sessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "Pick an active tmux session",
		Args:  usageNoArgs("usage: devtools sessions"),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			options := make([]picker.Option, 0, len(sessions))
			for _, session := range sessions {
				label := fmt.Sprintf("%s\t%d windows", session.Name, session.Windows)
				if session.Attached {
					label += "\tattached"
				}
				options = append(options, picker.Option{
					Label: label,
					Value: session.Name,
				})
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			preview := fmt.Sprintf("%q session-preview {1}", exe)
			option, err := picker.SelectOption(options, "no tmux sessions found", preview)
			if err != nil {
				return err
			}
			return tmux.SwitchSession(option.Value)
		},
	}
}

func usageNoArgs(usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return errors.New(usage)
		}
		return nil
	}
}

func usageExactArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return errors.New(usage)
		}
		return nil
	}
}

func usageMaximumArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return errors.New(usage)
		}
		return nil
	}
}

func usageArgRange(min, max int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return errors.New(usage)
		}
		return nil
	}
}
