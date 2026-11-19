package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"devtools/internal/backend"
	"devtools/internal/config"
	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/session"
	"devtools/internal/ui"

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
		var e *exit
		if errors.As(err, &e) {
			if e.text != "" {
				fmt.Fprintln(stderr, e.text)
			}
			return e.code
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type app struct {
	roots  []string
	stdout io.Writer
	stderr io.Writer
	picked *picked // the multiplexer of this command, once multiplexer() found it
}

// picked is what multiplexer() found: the backend and where the command runs.
type picked struct {
	m mux.Multiplexer
	c mux.Context
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	a := &app{stdout: stdout, stderr: stderr}
	cmd := &cobra.Command{
		Use:           "devtools [--root PATH]... <command> [args]",
		Short:         "Manage dev worktrees and their sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		// the feature gate (gate.go) runs before every command
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return a.gate(cmd)
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().StringArrayVar(&a.roots, "root", nil, "workspace root (repeatable; overrides DEVTOOLS_ROOT and config roots)")
	cmd.AddCommand(
		feature(featWork, newProjectCommand(a, stdout)),
		feature(featWork, cloneCommand(a, stdout)),
		feature(featWork, migrateCommand(a, stdout)),
		feature(featWork, newWorktreeCommand(a, stdout)),
		feature(featMerge, mergeCommand(a, stdout)),
		updateCommand(a, stdout),
		pushCommand(stdout),
		rebaseCommand(a, stdout),
		nukeCommand(stdout),
		feature(featMerge, removeWorktreeCommand(a, stdout)),
		listCommand(a, stdout),
		statusCommand(a, stdout),
		feature(featWork, switchCommand(a)),
		feature(featPick, pickCommand(a)),
		feature(featBookmark, bookmarkCommand(a, stdout)),
		feature(featSessions, sessionsCommand(a)),
		attachCommand(a, stdout),
		agentsCommand(a, stdout),
		keysCommand(a, stdout),
		sessionCommand(a, stdout),
		paneCommand(a, stdout),
		serverCommand(a, stdout),
		bridgeCommand(),
	)
	return cmd
}

func loadConfig() (config.Config, error) {
	cfgPath, err := config.Path()
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(cfgPath)
}

func (a *app) globals() (globals, error) {
	cfg, err := loadConfig()
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
			if _, _, err := a.multiplexer(); err != nil {
				return err
			}
			g, err := a.globals()
			if err != nil {
				return err
			}
			result, err := devgit.InitProject(g.primaryRoot(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.ProjectPath)
			_, err = a.show(result.ProjectPath, session.Name(result.Project, result.Worktree))
			return err
		},
	}
}

func cloneCommand(a *app, stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "clone <repo-url> [project-name]",
		Short: "Clone a repository into the workspace",
		Args:  usageArgRange(1, 2, "usage: devtools clone <repo-url> [project-name]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := a.multiplexer(); err != nil {
				return err
			}
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
			_, err = a.show(result.WorktreePath, session.Name(result.Project, result.Worktree))
			return err
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
			if _, _, err := a.multiplexer(); err != nil {
				return err
			}
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
			_, err = a.show(result.WorktreePath, session.Name(result.Project, result.Worktree))
			return err
		},
	}
	cmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "allow migrating a dirty checkout")
	return cmd
}

func newWorktreeCommand(a *app, stdout io.Writer) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "work [branch]",
		Short: "Create a new worktree, or name it in the switcher",
		Args:  usageMaximumArgs(1, "usage: devtools work [branch] [--from <start-point>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if from != "" {
					return errors.New("usage: devtools work <branch> --from <start-point>")
				}
				return a.enter(mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeNew}})
			}
			if _, _, err := a.multiplexer(); err != nil {
				return err
			}
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
			_, err = a.show(result.WorktreePath, session.Name(result.Project, result.Worktree))
			return err
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
			if _, _, err := a.multiplexer(); err != nil {
				return err
			}
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
			result, closeSession, err := a.mergeWorktree(root, project, worktree, squash)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.MainWorktreePath)
			return closeSession()
		},
	}
	cmd.Flags().BoolVar(&squash, "squash", false, "squash the worktree branch into a single commit")
	return cmd
}

// mergeWorktree lands a feature worktree on main, rebased so main only
// fast-forwards, then removes the worktree and its branch, moving over to
// main's session first if you are in the feature's. The returned function
// closes the feature's session, which may end devtools when it runs in
// there, so call it last.
func (a *app) mergeWorktree(root, project, worktree string, squash bool) (devgit.MergeResult, func() error, error) {
	result, err := devgit.MergeWorktreeToMain(root, project, worktree, devgit.MergeOptions{
		Squash: squash,
	})
	if err != nil {
		return devgit.MergeResult{}, nil, err
	}
	m, _, err := a.multiplexer()
	if err != nil {
		return devgit.MergeResult{}, nil, err
	}
	restoreHangup := ignoreHangup()
	closeSession, err := m.Leave(
		result.WorktreePath,
		result.MainWorktreePath,
		session.Name(result.Project, result.MainWorktree),
	)
	if err != nil {
		restoreHangup()
		return devgit.MergeResult{}, nil, err
	}
	_, err = devgit.RemoveWorktree(root, project, worktree, devgit.RemoveOptions{
		Force:        true,
		DeleteBranch: true,
	})
	if err != nil {
		restoreHangup()
		return devgit.MergeResult{}, nil, err
	}
	return result, func() error {
		defer restoreHangup()
		return closeSession()
	}, nil
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
			m, _, err := a.multiplexer()
			if err != nil {
				return err
			}
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
			closeSession, err := m.Leave(
				plan.WorktreePath,
				fallbackPath,
				session.Name(plan.Project, fallbackWorktree),
			)
			if err != nil {
				return err
			}
			result, err := devgit.RemoveWorktree(root, project, worktree, opts)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, result.WorktreePath)
			return closeSession()
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
			if !isTerminal(stdout) {
				for _, target := range targets {
					fmt.Fprintf(stdout, "%s\t%s\n", target.Label, target.Path)
				}
				return nil
			}
			home, _ := os.UserHomeDir()
			return printTree(stdout, treeRows(targets), "", func(row treeRow) []cell {
				path := row.target.Path
				if home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
					path = "~" + strings.TrimPrefix(path, home)
				}
				return []cell{{path, ui.CommentText}}
			})
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
			states := make(map[string]devgit.WorktreeStatus, len(targets))
			for _, target := range targets {
				status, err := devgit.Status(target.Path)
				if err != nil {
					return fmt.Errorf("%s: %w", target.Label, err)
				}
				states[target.Path] = status
			}
			here := ""
			if current, ok := currentTarget(targets, cwd); ok {
				here = current.Path
			}
			return printTree(stdout, treeRows(targets), here, func(row treeRow) []cell {
				return statusCells(row.target, states[row.target.Path])
			})
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

// statusCells are the columns of a status row: the branch when the
// worktree's name doesn't say it, uncommitted changes, how far the branch is
// from its upstream, or that it has none.
func statusCells(target discovery.Target, status devgit.WorktreeStatus) []cell {
	git := ui.Git{
		Changes:     status.Changes,
		Ahead:       status.Ahead,
		Behind:      status.Behind,
		HasUpstream: status.HasUpstream,
		Detached:    status.Detached,
		Branch:      status.Branch,
	}
	branch := ""
	if devgit.WorktreeName(status.Branch) != target.Worktree {
		branch = status.Branch
	}
	upstream := ""
	if !status.HasUpstream && !status.Detached {
		upstream = "no upstream"
	}
	return []cell{
		{branch, ui.CommentText},
		{ui.Changes(git), ui.MutedText},
		{ui.Sync(git), ui.MutedText},
		{upstream, ui.CommentText},
	}
}

func switchCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "switch [path-or-query]",
		Short: "Switch to a worktree session",
		Args:  usageMaximumArgs(1, "usage: devtools switch [path-or-query]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := a.multiplexer(); err != nil {
				return err
			}
			g, err := a.globals()
			if err != nil {
				return err
			}
			targets, err := scanTargets(g)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return a.enter(mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAll}})
			}
			targets = withBookmarks(targets, g.bookmarks)
			matches, err := discovery.ResolveMatches(targets, args[0])
			if err != nil {
				return err
			}
			if len(matches) > 1 {
				return a.enter(mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAll, Query: args[0]}})
			}
			target := matches[0]
			_, err = a.show(target.Path, session.Name(target.Project, target.Worktree))
			return err
		},
	}
}

func pickCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "pick",
		Short: "Open the switcher",
		Args:  usageNoArgs("usage: devtools pick"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.enter(mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAll}})
		},
	}
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

func sessionsCommand(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "Open the switcher on the open sessions",
		Args:  usageNoArgs("usage: devtools sessions"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.enter(mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeOpen}})
		},
	}
}

// openBackend is backend.Open; tests replace it to hand the commands a
// recording mux.Multiplexer.
var openBackend = backend.Open

// multiplexer finds the multiplexer of this command, once, at its start
// (5.3.8 steps 1 to 3): where the command runs, mux.Detect over the facts
// of this process; which backend, mux.Select with DEVTOOLS_MULTIPLEXER and
// the multiplexer directive; and the implementation, backend.Open. It
// returns the multiplexer with the context it was opened in. A selection
// error is the words of its kind (ambiguous, mismatch, unavailable) or, for
// a name mux.Parse rejects, its own text; each ends the command with status
// 1 before it does anything else.
func (a *app) multiplexer() (mux.Multiplexer, mux.Context, error) {
	if a.picked != nil {
		return a.picked.m, a.picked.c, nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, mux.Context{}, err
	}
	env := os.Environ()
	c := mux.Detect(backend.Facts(env, terminalIn, terminalOut))
	b, err := mux.Select(c, mux.Choice{Env: os.Getenv("DEVTOOLS_MULTIPLEXER"), Config: cfg.Multiplexer}, backend.Available)
	if err != nil {
		return nil, c, selectionWords(err)
	}
	// the roots the hub of a backend scans are the roots of this command:
	// --root, then DEVTOOLS_ROOT, then the config
	if roots := a.commandRoots(); len(roots) > 0 {
		cfg.Roots = roots
	}
	m, err := openBackend(b, c, backend.Deps{
		Config:  &cfg,
		Actions: hubActions{},
		Version: version,
		In:      terminalIn,
		Out:     terminalOut,
		Env:     env,
	})
	if err != nil {
		return nil, c, err
	}
	a.picked = &picked{m: m, c: c}
	return m, c, nil
}

// commandRoots are the roots named by --root or DEVTOOLS_ROOT, nil when
// neither names one (then the config's roots stand).
func (a *app) commandRoots() []string {
	roots := a.roots
	if len(roots) == 0 {
		if env := os.Getenv("DEVTOOLS_ROOT"); env != "" {
			roots = filepath.SplitList(env)
		}
	}
	var out []string
	for _, r := range roots {
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}

// selectionWords is what a person reads for an error of mux.Select: the
// words of 5.3.9 for the three typed errors (for an unavailable backend
// backend.UnavailableWords, the noserver words for devtools, so this
// package names no backend), and the error's own text for a name mux.Parse
// rejects.
func selectionWords(err error) error {
	switch e := err.(type) {
	case *mux.ErrMismatch:
		return words(ui.Message("mismatch", string(e.Asked), string(e.Inside)))
	case *mux.ErrUnavailable:
		return words(backend.UnavailableWords(e))
	case *mux.ErrAmbiguous:
		names := ambiguousNames(e.Marks)
		return words(ui.Message("ambiguous", names[0], names[1]))
	}
	return err
}

// ambiguousNames are the two names the ambiguous words give: the live
// backends of the context, then screen or zellij when their mark is set
// (they are live whenever set, and Context.Marks lists backends only).
func ambiguousNames(marks []mux.Backend) []string {
	var names []string
	for _, b := range marks {
		names = append(names, string(b))
	}
	if os.Getenv("STY") != "" {
		names = append(names, "screen")
	}
	if os.Getenv("ZELLIJ") != "" || os.Getenv("ZELLIJ_SESSION_NAME") != "" {
		names = append(names, "zellij")
	}
	// Detect makes a context ambiguous only with two live marks, so this
	// only keeps the words from failing on a context built by hand.
	for len(names) < 2 {
		names = append(names, "")
	}
	return names
}

// wordsError is an error whose text is the words a person reads. Run
// prints it on standard error and the command ends with status 1.
type wordsError struct{ text string }

func (e *wordsError) Error() string { return e.text }

func words(text string) error { return &wordsError{text: text} }

// say prints a line for the person on standard error, where Run prints the
// errors too.
func (a *app) say(text string) {
	if a.stderr != nil {
		fmt.Fprintln(a.stderr, text)
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
