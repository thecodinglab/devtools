# devtools

`devtools` is a small Go CLI for managing development checkouts as Git worktrees and pairing each worktree with a tmux session.

By default it uses `~/dev` as the workspace root. Set `DEVTOOLS_ROOT` or pass `--root PATH` to use a different workspace.

## Requirements

- Go 1.26 or newer
- Git
- tmux
- fzf, for interactive picking with `devtools pick`, `devtools sessions`, or ambiguous `devtools switch` queries
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
devtools [--root PATH] <command> [args]
```

Commands:

```sh
devtools new <project-name>
devtools clone <repo-url> [project-name]
devtools migrate [--allow-dirty] [path]
devtools work <branch> [--from <start-point>]
devtools merge
devtools update [--all]
devtools push
devtools rebase [--onto <base>]
devtools done [worktree] [--force] [--keep-branch] [--allow-main]
devtools list
devtools status [--all]
devtools switch [path-or-query]
devtools pick
devtools sessions
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

## Commands

### `new`

Creates a normal Git repository on the `main` branch and switches to its tmux session.

```sh
devtools new scratch
```

### `clone`

Clones a repository into the bare-worktree layout, creates the remote default branch worktree, and switches to its tmux session.

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

Creates a new worktree for a branch in the current managed project and switches to its tmux session.

```sh
devtools work feature/api
devtools work experiment --from origin/main
```

When `--from` is omitted, the current checkout's `HEAD` is used as the start point.

### `merge`

Merges the current worktree branch into the `main` worktree, fast-forwarding when Git can, then removes the merged worktree and deletes its local branch. Both worktrees must be clean. Pass `--squash` to squash the branch changes into one new commit on `main`; Git opens your editor for the commit message.

```sh
devtools merge
devtools merge --squash
```

### `update`

Fetches from `origin` and fast-forwards the current project's `main` or `master` worktree. The main worktree must be clean. Pass `--all` to update main worktrees for all managed projects under the workspace root.

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

Removes a worktree and, by default, deletes its local branch. The worktree must be clean and pushed unless `--force` is used.

```sh
devtools done
devtools done feature-api --keep-branch
devtools done feature-api --force
```

Removing `main` or `master` requires `--allow-main`.

### `list`

Lists discovered worktrees under the workspace root.

```sh
devtools list
```

### `status`

Shows a compact dashboard with branch, clean or dirty state, ahead/behind counts, and upstream. Untracked files are ignored when computing the dirty state.

When run from inside a managed worktree, only worktrees for that project are shown. From outside a managed project, the whole workspace root is shown. Pass `--all` to show the whole workspace root from anywhere.

```sh
devtools status
devtools status --all
```

### `switch`

Switches to a tmux session for a worktree. The target can be an absolute path or a query matched against discovered labels.

```sh
devtools switch repo/main
devtools switch feature-api
```

If no target is provided, or if a query matches multiple worktrees, `fzf` is used to pick one.

### `pick`

Always opens the interactive picker and switches to the selected worktree.

```sh
devtools pick
```

### `sessions`

Opens an interactive picker for active tmux sessions and switches to the selected session.

```sh
devtools sessions
```

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
