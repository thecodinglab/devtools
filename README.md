# devtools

`devtools` is a small Go CLI for managing development checkouts as Git worktrees and pairing each worktree with a session in a terminal multiplexer.

Sessions live in [herdr](https://herdr.dev) by default: each session is a herdr workspace labeled with the session name. Switching focuses it or creates it in the worktree, removing a worktree closes it, and `sessions` picks between the open workspaces. Inside a herdr pane this goes through the running herdr. From a plain terminal, devtools starts a herdr server in the background if none is running, focuses the workspace, and attaches the terminal to herdr, the way `tmux new-session -A` does.

Inside tmux (`$TMUX` is set, including a tmux nested in herdr), sessions are tmux sessions instead. If herdr is not installed (neither `$HERDR_BIN_PATH` nor `herdr` on `PATH`), devtools falls back to tmux. To pick a multiplexer explicitly, set `DEVTOOLS_MULTIPLEXER=tmux` or `DEVTOOLS_MULTIPLEXER=herdr`, or add a `multiplexer` line to the [config file](#configuration). The environment variable wins over the config file, and both win over the `$TMUX` rule.

By default it uses `~/dev` as the workspace root. Multiple roots can be configured; projects are discovered across all of them. Roots come from, in order of precedence:

1. `--root PATH`, repeatable: `devtools --root ~/dev --root ~/work list`
2. `DEVTOOLS_ROOT`, accepting a colon-separated list of paths
3. `root` lines in the config file (see [Configuration](#configuration))
4. `~/dev`

The first root is the primary root: `new` and `clone` create projects there. All other commands search every root.

## Requirements

- Go 1.26 or newer
- Git
- herdr or tmux: herdr is the default, tmux is used inside tmux or when herdr is not installed
- fzf, for `devtools agents`, `devtools machines` and `devtools sessions --machine`
- Nix, optional, for the provided dev shell and package build

## Install

Run from this repository:

```sh
go install ./cmd/devtools
```

Or build with Nix:

```sh
nix build
```

## Usage

```sh
devtools [--root PATH]... <command> [args]
```

Commands:

```sh
devtools new <project-name>
devtools clone <repo-url> [project-name]
devtools migrate [--allow-dirty] [path]
devtools work [branch] [--from <start-point>]
devtools merge
devtools update [--all]
devtools push
devtools rebase [--onto <base>]
devtools done [worktree] [--force] [--keep-branch] [--allow-main]
devtools list
devtools status [--all]
devtools switch [path-or-query]
devtools pick
devtools sessions [--machine <label-or-id>]
devtools agents [--machine <label-or-id>]
devtools machines
devtools bookmark add <name> [path]
devtools bookmark remove <name>
devtools bookmark list
```

Common flow:

```sh
devtools clone git@github.com:owner/repo.git repo
cd ~/dev/repo/main
devtools work feature/example
devtools merge
```

`clone` creates a bare repository at:

```text
<root>/<project>/.bare
```

and creates the default branch as a worktree at:

```text
<root>/<project>/<branch>
```

Feature branches are converted to filesystem-safe worktree names, so `feature/example` becomes `feature-example`.

## The switcher

`devtools pick`, `devtools switch` without a target, `devtools sessions` and `devtools work` without a branch open the switcher: one list of every worktree, bookmark and open workspace.

```text
  › Switch to a worktree, or name a new feature

  curiensis-website     8 changed
▎ devtools              5 changed
    popup-redesign      3 changed   ↑2   working     Rework the switcher
  kakeibo               1 changed   ↑2   open
  xcloud                8 changed        idle        Slow VM spawn on prod-node-5801
    feature-refactor                     needs you   Approve rm?

  ⏎ switch   ⌃N new feature   ⌃G merge into main   tab open only
```

Each project is one row, standing for its main worktree, with its feature worktrees indented below. The marked row is where you are: the directory of the pane under a herdr popup, or the current directory. The columns are uncommitted changes, how far the branch is ahead of and behind its upstream, and the state of its workspace: `open`, or what its agent is doing (`working`, `idle`, `done`, `needs you`) followed by the agent's terminal title. A clean, pushed worktree without a workspace shows nothing.

Typing filters the list the way fzf does. When the text names no existing worktree, it can become a feature: the last row offers `New feature <name> in <project>, from <worktree>`, branching from the worktree you are in, and enter on it creates the worktree and switches to it. Spaces become dashes. Prefix the name with a project, `xcloud/fix-login`, to start it in another project from its main worktree.

Keys:

- `enter` switches to the selected row, or creates the feature on the create row
- `↑` `↓` or `ctrl+k` `ctrl+j` move
- `ctrl+n` creates the feature named by the text
- `ctrl+g` merges the selected feature into main, after asking, like `devtools merge`
- `tab` shows only open workspaces, or all again
- `esc` clears the text, then closes

`devtools sessions` opens it on the open workspaces only, and `devtools work` without a branch opens it to name a feature in the current project.

## Commands

### `new`

Creates a normal Git repository on the `main` branch and switches to its session.

```sh
devtools new scratch
```

### `clone`

Clones a repository into the bare-worktree layout, creates the remote default branch worktree, and switches to its session.

```sh
devtools clone git@github.com:owner/repo.git
devtools clone git@github.com:owner/repo.git custom-name
```

### `migrate`

Migrates an existing checkout into the managed layout. Checkouts with tracked uncommitted changes are rejected unless `--allow-dirty` is passed. Untracked files are ignored by this check.

```sh
devtools migrate
devtools migrate ~/src/repo --allow-dirty
```

### `work`

Creates a new worktree for a branch in the current managed project and switches to its session. Without a branch it opens [the switcher](#the-switcher) to name one.

```sh
devtools work feature/api
devtools work experiment --from origin/main
```

When `--from` is omitted, the current checkout's `HEAD` is used as the start point.

### `merge`

Lands the current worktree branch on `main` without a merge commit: it rebases the branch onto `main`, then fast-forwards `main` to it, then removes the merged worktree, closes its session, and deletes its local branch. If the rebase stops on a conflict it is undone, leaving both branches as they were; rebase and resolve the conflicts with `devtools rebase`, then merge again. A branch that contains merge commits is refused, since rebasing would drop what those merges changed; merge it with `--squash`. Both worktrees must be clean. Pass `--squash` to squash the branch changes into one new commit on `main`; Git opens your editor for the commit message.

```sh
devtools merge
devtools merge --squash
```

### `update`

Fetches from `origin` and fast-forwards the current project's `main` or `master` worktree. The main worktree must be clean. Pass `--all` to update main worktrees for all managed projects under all workspace roots.

```sh
devtools update
devtools update --all
```

### `push`

Pushes the current branch. If the branch has no upstream, it pushes to `origin` and sets the upstream.

```sh
devtools push
```

### `rebase`

Rebases the current worktree onto the current project's `main` or `master` branch by default. The worktree must be clean. Use `--onto` to choose a different base ref.

```sh
devtools rebase
devtools rebase --onto origin/main
```

### `done`

Removes a worktree, closes its session, and, by default, deletes its local branch. The worktree must be clean and pushed unless `--force` is used.

The worktree is moved into a trash folder inside the project's `.bare` and git forgets it at once; its files, a build's worth of them in a compiled project, are deleted in the background, so `done` returns right away however large the worktree is. `merge` removes the merged worktree the same way.

```sh
devtools done
devtools done feature-api --keep-branch
devtools done feature-api --force
```

Removing `main` or `master` requires `--allow-main`.

### `list`

Lists discovered worktrees under all workspace roots, plus any bookmarks, grouped by project with their paths. Piped into another program it prints one `label<TAB>path` line per worktree instead.

```sh
devtools list
```

### `status`

Shows the worktrees the way the switcher lists them, one row per project with its features below, each with its uncommitted changes, how far it is ahead of (`↑`) and behind (`↓`) its upstream, or `no upstream`. The branch is shown when the worktree's name doesn't already say it. A clean worktree in step with its upstream shows nothing. Untracked files are ignored when counting changes, and `▎` marks the worktree you are in.

When run from inside a managed worktree, only worktrees for that project are shown. From outside a managed project, all workspace roots are shown. Pass `--all` to show all workspace roots from anywhere. Bookmarks are not included.

```sh
devtools status
devtools status --all
```

### `switch`

Switches to the session for a worktree, creating it if needed. The target can be an absolute path or a query matched against discovered labels.

```sh
devtools switch repo/main
devtools switch feature-api
```

If no target is provided, or if a query matches multiple worktrees, [the switcher](#the-switcher) opens, with the query typed in.

### `pick`

Opens [the switcher](#the-switcher).

```sh
devtools pick
```

### `sessions`

Opens [the switcher](#the-switcher) on the open herdr workspaces, or the tmux sessions with the tmux multiplexer, including the ones devtools did not create; `tab` shows every worktree again.

With `--machine`, it picks a workspace on a saved herdr SSH machine (see `herdr machine list`) instead. `--machine` requires the herdr multiplexer.

```sh
devtools sessions
devtools sessions --machine workbox
```

### `agents`

Opens an interactive picker for the coding agents herdr recognizes, showing each one's workspace, kind, status, and terminal title. Agents that need attention come first: blocked, done, idle, working, then unknown. The preview shows the agent's screen. Selecting an agent focuses it; from a plain terminal, devtools then attaches to herdr. With `--machine`, it lists the agents on a saved herdr SSH machine.

```sh
devtools agents
devtools agents --machine workbox
```

### `machines`

Opens an interactive picker for the saved herdr SSH machines, previewing each one's workspaces, then the workspace picker for the selected machine. From a plain terminal, devtools attaches to the machine's herdr over SSH (`herdr --remote`). Inside herdr, it focuses the workspace on the machine, but herdr can't switch the machine the window shows from the CLI, so select the machine in the herdr sidebar to see it.

```sh
devtools machines
```

### `bookmark`

Manages named bookmarks for directories that do not follow the managed bare-worktree layout, like a dotfiles checkout or a notes directory. Bookmarks show up in `list`, `switch`, and `pick` alongside discovered worktrees, and their session is named after the bookmark.

`add` stores the given path, or the current directory when the path is omitted. Bookmarks are stored in the config file.

```sh
devtools bookmark add dotfiles ~/.dotfiles
devtools bookmark add notes
devtools bookmark list
devtools bookmark remove notes
devtools switch dotfiles
```

## Configuration

devtools reads an optional config file from `$DEVTOOLS_CONFIG`, falling back to `$XDG_CONFIG_HOME/devtools/config` and then `~/.config/devtools/config`. The format is line-based; blank lines and lines starting with `#` are ignored:

```text
# workspace roots, searched in order; the first is the primary root
root ~/dev
root ~/work

# name followed by path; paths may contain spaces
bookmark dotfiles ~/.dotfiles
bookmark notes ~/Documents/notes

# always use tmux, or herdr, regardless of $TMUX
multiplexer tmux
```

`root` lines are used only when neither `--root` nor `DEVTOOLS_ROOT` is set. `bookmark` lines are managed by `devtools bookmark add` and `devtools bookmark remove`, but can also be edited by hand. `multiplexer` is overridden by `DEVTOOLS_MULTIPLEXER`.

## Development

Enter the Nix dev shell:

```sh
nix develop
```

Run tests:

```sh
go test ./...
```

Run the formatter for the Nix files:

```sh
nix fmt
```

Build the Nix package:

```sh
nix build
```
