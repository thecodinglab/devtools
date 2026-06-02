# devtools

`devtools` is a small Go CLI for managing development checkouts as Git worktrees and pairing each worktree with a tmux session.

By default it uses `~/dev` as the workspace root. Set `DEVTOOLS_ROOT` or pass `--root PATH` to use a different workspace.

## Requirements

- Go 1.26 or newer
- Git
- tmux
- fzf, for interactive picking with `devtools pick` or ambiguous `devtools switch` queries
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
devtools done [worktree] [--force] [--keep-branch] [--allow-main]
devtools list
devtools switch [path-or-query]
devtools pick
```

Common flow:

```sh
devtools clone git@github.com:owner/repo.git repo
cd ~/dev/repo/main
devtools work feature/example
devtools switch repo/main
devtools done feature-example
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

Migrates an existing checkout into the managed layout. Dirty checkouts are rejected unless `--allow-dirty` is passed.

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
