# devtools

`devtools` is a small Go CLI for managing development checkouts as Git worktrees, and the terminal multiplexer that pairs each worktree with a session.

devtools is its own multiplexer: a server per machine keeps the sessions, their tabs and panes, and every terminal window is a client of it (`devtools attach`). The client draws a status row, has a key table and copy mode, and opens the hub, the switcher over every worktree, bookmark, session and agent, on this machine and on the other machines named in the [config](#configuration). A session on another machine is shown in the same window over `ssh <machine> devtools bridge`; nothing listens on the network.

Two other multiplexers can be used instead, through the same commands:

- tmux, the fallback for when the devtools server is down: `DEVTOOLS_MULTIPLEXER=tmux devtools attach`.
- herdr, for a machine that still has it installed.

Each backend does what its multiplexer does natively and nothing more. A command or a part of the hub that needs a capability the backend lacks says so and exits with status 1, or is not shown; nothing is emulated:

| Capability | devtools | tmux | herdr |
| --- | --- | --- | --- |
| `machines`: sessions of other machines, shown in this window | yes | no | no |
| `agent-status`: the coding agents and their state, `devtools agents` | yes | no | yes |
| `window`: the status row, the key table, copy mode, `devtools keys` | yes | no | no |
| `pane-focus`: `devtools pane focus` | yes | no | no |
| `layout`: `devtools session list --json`, `devtools pane list --json` | yes | no | no |
| `detached-open`: open a session that no window shows | yes | yes | no |

Which backend a command uses: inside a devtools, tmux or herdr pane, that multiplexer; a `DEVTOOLS_MULTIPLEXER` that names another one is refused. Elsewhere `DEVTOOLS_MULTIPLEXER` (`devtools`, `tmux` or `herdr`), then the `multiplexer` line of the config, then devtools. A backend that is not available is an error that says so; devtools never falls back to another one on its own. No multiplexer is ever started inside another.

By default it uses `~/dev` as the workspace root. Multiple roots can be configured; projects are discovered across all of them. Roots come from, in order of precedence:

1. `--root PATH`, repeatable: `devtools --root ~/dev --root ~/work list`
2. `DEVTOOLS_ROOT`, accepting a colon-separated list of paths
3. `root` lines in the config file (see [Configuration](#configuration))
4. `~/dev`

The first root is the primary root: `new` and `clone` create projects there. All other commands search every root.

## Requirements

- Go 1.26 or newer, with cgo: the terminal engine is libghostty-vt, which the Nix dev shell provides
- Git
- ssh, for other machines
- tmux or herdr, only to use them as the backend
- Nix, optional, for the provided dev shell and package build

## Install

Build with Nix:

```sh
nix build
```

The flake also has a home-manager module, `programs.devtools`, which writes the config and runs `devtools server` as a user service (`devtools-server` under systemd, launchd on macOS).

## Usage

```sh
devtools [--root PATH]... <command> [args]
```

Commands:

```sh
devtools attach
devtools new <project-name>
devtools clone <repo-url> [project-name]
devtools migrate [--allow-dirty] [path]
devtools work [branch] [--from <start-point>]
devtools merge [--squash]
devtools update [--all]
devtools push
devtools rebase [--onto <base>]
devtools done [worktree] [--force] [--keep-branch] [--allow-main]
devtools list
devtools status [--all]
devtools switch [path-or-query]
devtools pick
devtools sessions
devtools agents [--json]
devtools keys
devtools session list --json
devtools pane list --json
devtools pane focus --direction left|right|up|down
devtools server [status [--json] | restart [--yes]]
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

`devtools pick`, `devtools switch` without a target, `devtools sessions` and `devtools work` without a branch open the switcher, the hub: one list of every worktree, bookmark and open session. In a devtools window it opens over the window, the prefix key (`ctrl+space`) and `o` open it too; in tmux and herdr it runs in the pane or popup the command was typed in; from a plain terminal the window starts with it open.

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

Each project is one row, standing for its main worktree, with its feature worktrees indented below. The marked row is where you are: the current directory. The columns are uncommitted changes, how far the branch is ahead of and behind its upstream, and the state of its session: `open`, or what its agent is doing (`working`, `idle`, `done`, `needs you`) followed by the agent's terminal title. A clean, pushed worktree without a session shows nothing.

Typing filters the list the way fzf does. When the text names no existing worktree, it can become a feature: the last row offers `New feature <name> in <project>, from <worktree>`, branching from the worktree you are in, and enter on it creates the worktree and switches to it. Spaces become dashes. Prefix the name with a project, `xcloud/fix-login`, to start it in another project from its main worktree.

Keys:

- `enter` switches to the selected row, or creates the feature on the create row
- `↑` `↓` or `ctrl+k` `ctrl+j` move
- `ctrl+n` creates the feature named by the text
- `ctrl+g` merges the selected feature into main, after asking, like `devtools merge`
- `tab` cycles all, open sessions only and, with `agent-status`, the agents
- `esc` clears the text, then closes

`devtools sessions` opens it on the open sessions only, `devtools agents` on the agents, and `devtools work` without a branch opens it to name a feature in the current project.

On the devtools backend the projects and sessions of [your other machines](#other-machines) show below this machine's, each machine under its name, as soon as it answers. Typing a machine's name lists what is on it, `macmini xcl` say. On tmux and herdr the hub lists this machine only.

## Other machines

Other machines exist only as `machine` lines of the [config](#configuration), by ssh host alias:

```text
machine macmini connect
machine macbook
```

The devtools client reaches a machine by running `ssh -T <alias> devtools bridge`, which connects to the devtools server over there; ssh allocates no terminal and nothing listens on the network. A machine with `connect` is connected while the client runs; one without it is connected when the hub opens or a switch goes there, and let go of 10 minutes after it is no longer shown. Picking a session of another machine in the hub shows it in this window, and the status row names the machine. A machine that does not answer stays in the hub under its name, with the reason.

Nothing received from another machine adds, renames or removes a machine, and everything it sends is drawn without its control characters. Only the devtools backend has other machines; tmux and herdr show this machine only.

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

If no target is provided, or if a query matches multiple worktrees, [the switcher](#the-switcher) opens, with the query typed in. A query that matches nothing here is an error.

### `pick`

Opens [the switcher](#the-switcher).

```sh
devtools pick
```

### `attach`

Turns this terminal into a window of the multiplexer. From a plain terminal it shows the session shown last, or the switcher; inside a window it opens the switcher instead and says that you are already inside. A terminal that is itself inside a multiplexer, also over ssh, is refused rather than nesting one multiplexer in another.

```sh
devtools attach
DEVTOOLS_MULTIPLEXER=tmux devtools attach
```

### `sessions`

Opens [the switcher](#the-switcher) on the open sessions, including the ones devtools did not create; `tab` shows every worktree again.

```sh
devtools sessions
```

### `agents`

Opens [the switcher](#the-switcher) on the coding agents of every machine, the ones that need attention first: blocked, done, idle, working. Picking one shows its pane. `--json` prints one object per line for each agent of this machine. Needs `agent-status`: devtools or herdr.

```sh
devtools agents
devtools agents --json
```

### `keys`

Prints every key binding of the devtools window, of the switcher and of copy mode after the `bind` and `unbind` lines of the config were applied. The prefix key is `ctrl+space`; `prefix ?` shows the bindings in the window.

### `session list`, `pane list`, `pane focus`

`devtools session list --json` and `devtools pane list --json` print one object per line for each session or pane of the devtools server. `devtools pane focus --direction left|right|up|down` moves the focus to the neighbouring pane of the pane it runs in: exit 0 moved, 1 no pane that way, 2 an error; editors use it for navigation across panes.

### `server`

`devtools server` is the server; the service manager runs it. `devtools server status` says whether it runs (exit 0) or not (exit 1, with the command that starts it); `devtools server restart` restarts the service, after asking when programs are running in it (`--yes` does not ask). A session survives a restart as its directory and layout; the programs in it start again.

### `bookmark`

Manages named bookmarks for directories that do not follow the managed bare-worktree layout, like a dotfiles checkout or a notes directory. Bookmarks show up in `list`, `switch`, and `pick` alongside discovered worktrees, and their session is named after the bookmark.

`add` stores the given path, or the current directory when the path is omitted. Bookmarks are stored in the local config file.

```sh
devtools bookmark add dotfiles ~/.dotfiles
devtools bookmark add notes
devtools bookmark list
devtools bookmark remove notes
devtools switch dotfiles
```

## Configuration

devtools reads two config files with one grammar: `$DEVTOOLS_CONFIG`, falling back to `$XDG_CONFIG_HOME/devtools/config` and then `~/.config/devtools/config` (home-manager writes this one), and then the same path with `.local` appended, which `devtools bookmark` writes. The format is line-based; blank lines and lines starting with `#` are ignored, and an unknown directive or a malformed line is an error that names the file and the line:

```text
# workspace roots, searched in order; the first is the primary root
root ~/dev
root ~/work

# name followed by path; paths may contain spaces
bookmark dotfiles ~/.dotfiles
bookmark notes ~/Documents/notes

# the backend outside a multiplexer: devtools (the default), tmux or herdr
multiplexer devtools

# other machines, by ssh host alias; connect keeps the connection
machine macmini connect
machine macbook

# key bindings of the devtools window
bind prefix+f hub new
unbind prefix+q

# one of the colour tokens strong, muted, raised, selection, warning: light, dark
color selection #d0e0ff #304060
```

`root` lines are used only when neither `--root` nor `DEVTOOLS_ROOT` is set. `bookmark` lines are managed by `devtools bookmark add` and `devtools bookmark remove`, but can also be edited by hand. `multiplexer` is overridden by `DEVTOOLS_MULTIPLEXER` and is not read inside a multiplexer. A line a backend has no use for (`machine` on tmux, `bind` on herdr) is read and ignored, so one config serves every backend.

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
