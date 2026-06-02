package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/picker"
	"devtools/internal/tmux"

	"github.com/spf13/cobra"
)

type globals struct {
	root string
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
	root string
}

func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	a := &app{root: defaultRoot()}
	cmd := &cobra.Command{
		Use:           "devtools [--root PATH] <command> [args]",
		Short:         "Manage dev worktrees and tmux sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.PersistentFlags().StringVar(&a.root, "root", a.root, "workspace root")
	cmd.AddCommand(
		newProjectCommand(a, stdout),
		cloneCommand(a, stdout),
		migrateCommand(a, stdout),
		newWorktreeCommand(a, stdout),
		removeWorktreeCommand(a, stdout),
		listCommand(a, stdout),
		switchCommand(a),
		pickCommand(a),
	)
	return cmd
}

func (a *app) globals() (globals, error) {
	root, err := expandPath(a.root)
	if err != nil {
		return globals{}, err
	}
	return globals{root: root}, nil
}

func defaultRoot() string {
	if root := os.Getenv("DEVTOOLS_ROOT"); root != "" {
		return root
	}
	return "~/dev"
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
			result, err := devgit.InitProject(g.root, args[0])
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
			result, err := devgit.Clone(g.root, args[0], project)
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
			project, err := devgit.InferProject(g.root, cwd)
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
			result, err := devgit.AddWorktree(g.root, project, args[0], startPoint)
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
			project, worktree, err := devgit.InferProjectWorktree(g.root, cwd)
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
			plan, err := devgit.PlanRemoveWorktree(g.root, project, worktree, opts)
			if err != nil {
				return err
			}
			fallbackWorktree, fallbackPath := removalFallback(g.root, project, plan.Worktree)
			restoreHangup := ignoreHangup()
			defer restoreHangup()
			if err := tmux.CloseForRemoval(
				tmux.SessionName(plan.Project, plan.Worktree),
				tmux.SessionName(plan.Project, fallbackWorktree),
				fallbackPath,
			); err != nil {
				return err
			}
			result, err := devgit.RemoveWorktree(g.root, project, worktree, opts)
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
			targets, err := discovery.Scan(g.root)
			if err != nil {
				return err
			}
			for _, target := range targets {
				fmt.Fprintf(stdout, "%s\t%s\n", target.Label, target.Path)
			}
			return nil
		},
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
			targets, err := discovery.Scan(g.root)
			if err != nil {
				return err
			}
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
			targets, err := discovery.Scan(g.root)
			if err != nil {
				return err
			}
			target, err := picker.Select(targets)
			if err != nil {
				return err
			}
			return tmux.Switch(target.Path, tmux.SessionName(target.Project, target.Worktree))
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
