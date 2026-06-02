package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/picker"
	"devtools/internal/tmux"
)

type globals struct {
	root string
}

func Run(args []string, stdout, stderr io.Writer) int {
	if err := run(args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) error {
	g, rest, err := parseGlobals(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		printUsage(stdout)
		return nil
	}

	switch rest[0] {
	case "clone":
		return cloneCmd(g, rest[1:], stdout)
	case "new":
		return newProjectCmd(g, rest[1:], stdout)
	case "migrate":
		return migrateCmd(g, rest[1:], stdout)
	case "w":
		return newWorktreeCmd(g, rest[1:], stdout)
	case "rm", "done":
		return removeWorktreeCmd(g, rest[1:], stdout)
	case "list":
		return listCmd(g, rest[1:], stdout)
	case "switch":
		return switchCmd(g, rest[1:])
	case "pick":
		return pickCmd(g, rest[1:])
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func parseGlobals(args []string) (globals, []string, error) {
	g := globals{root: defaultRoot()}
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--root":
			i++
			if i >= len(args) {
				return g, nil, errors.New("--root requires a path")
			}
			g.root = args[i]
		case "-h", "--help":
			rest = append(rest, arg)
		default:
			rest = append(rest, args[i:]...)
			i = len(args)
		}
	}
	root, err := expandPath(g.root)
	if err != nil {
		return g, nil, err
	}
	g.root = root
	return g, rest, nil
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

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: devtools [--root PATH] <command> [args]

Commands:
  new <project-name>
  clone <repo-url> [project-name]
  migrate [--allow-dirty] [path]
  w <branch> [--from <start-point>]
  rm [worktree] [--force] [--keep-branch] [--allow-main]
  list
  switch [path-or-query]
  pick

Aliases:
  done  same as rm`)
}

func newProjectCmd(g globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: devtools new <project-name>")
	}
	result, err := devgit.InitProject(g.root, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, result.ProjectPath)
	return tmux.Switch(result.ProjectPath, tmux.SessionName(result.Project, result.Worktree))
}

func cloneCmd(g globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("usage: devtools clone <repo-url> [project-name]")
	}
	project := ""
	if fs.NArg() == 2 {
		project = fs.Arg(1)
	}
	result, err := devgit.Clone(g.root, fs.Arg(0), project)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, result.WorktreePath)
	return tmux.Switch(result.WorktreePath, tmux.SessionName(result.Project, result.Worktree))
}

func migrateCmd(g globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	allowDirty := fs.Bool("allow-dirty", false, "allow migrating a dirty checkout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: devtools migrate [--allow-dirty] [path]")
	}
	path := "."
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	abs, err := expandPath(path)
	if err != nil {
		return err
	}
	result, err := devgit.Migrate(abs, *allowDirty)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, result.WorktreePath)
	return tmux.Switch(result.WorktreePath, tmux.SessionName(result.Project, result.Worktree))
}

func newWorktreeCmd(g globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("w", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "start point")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: devtools w <branch> [--from <start-point>]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	project, err := devgit.InferProject(g.root, cwd)
	if err != nil {
		return err
	}
	startPoint := *from
	if startPoint == "" {
		startPoint, err = devgit.CurrentHEAD(cwd)
		if err != nil {
			return err
		}
	}
	result, err := devgit.AddWorktree(g.root, project, fs.Arg(0), startPoint)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, result.WorktreePath)
	return tmux.Switch(result.WorktreePath, tmux.SessionName(result.Project, result.Worktree))
}

func removeWorktreeCmd(g globals, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	force := fs.Bool("force", false, "remove without clean/pushed checks")
	keepBranch := fs.Bool("keep-branch", false, "keep the local branch")
	allowMain := fs.Bool("allow-main", false, "allow removing main or master")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: devtools rm [worktree] [--force] [--keep-branch] [--allow-main]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	project, worktree, err := devgit.InferProjectWorktree(g.root, cwd)
	if err != nil {
		return err
	}
	if fs.NArg() == 1 {
		worktree = fs.Arg(0)
	}
	result, err := devgit.RemoveWorktree(g.root, project, worktree, devgit.RemoveOptions{
		Force:        *force,
		DeleteBranch: !*keepBranch,
		AllowMain:    *allowMain,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, result.WorktreePath)
	return nil
}

func listCmd(g globals, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: devtools list")
	}
	targets, err := discovery.Scan(g.root)
	if err != nil {
		return err
	}
	for _, target := range targets {
		fmt.Fprintf(stdout, "%s\t%s\n", target.Label, target.Path)
	}
	return nil
}

func switchCmd(g globals, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: devtools switch [path-or-query]")
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
}

func pickCmd(g globals, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: devtools pick")
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
}
