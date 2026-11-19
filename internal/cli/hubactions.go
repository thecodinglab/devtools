package cli

// The actions of the hub (hub.Actions, 5.3.8 of the plan; moved from
// internal/cli/hub.go:259-296 by P2-08b). They are handed to the backend
// as backend.Deps.Actions: the devtools backend closes the hub overlay of
// the client over them, the other two backends give them to the
// standalone hub. Both run git only: what the hub shows after them is a
// switch of the hub's own host (fleet.Switch inside the client, Show after
// a standalone pick), never a multiplexer call from here (N15).

import (
	"devtools/internal/devgit"
	"devtools/internal/hub"
	"devtools/internal/session"
)

// hubActions creates and merges feature worktrees for the hub.
type hubActions struct{}

var _ hub.Actions = hubActions{}

// Create adds the feature worktree req names, from the HEAD of the worktree
// the hub was opened from.
func (hubActions) Create(req hub.CreateRequest) (hub.Item, error) {
	startPoint, err := devgit.CurrentHEAD(req.FromPath)
	if err != nil {
		return hub.Item{}, err
	}
	result, err := devgit.AddWorktree(req.Root, req.Project, req.Branch, startPoint)
	if err != nil {
		return hub.Item{}, err
	}
	return hub.Item{
		Kind:     hub.KindWorktree,
		Project:  result.Project,
		Worktree: result.Worktree,
		Label:    result.Project + "/" + result.Worktree,
		Path:     result.WorktreePath,
		Root:     req.Root,
		Session:  session.Name(result.Project, result.Worktree),
	}, nil
}

// Merge lands the feature worktree of item on main, rebased so main only
// fast-forwards, removes the worktree and its branch, and returns main's
// worktree, which the hub then shows.
func (hubActions) Merge(item hub.Item) (hub.Item, error) {
	result, err := devgit.MergeWorktreeToMain(item.Root, item.Project, item.Worktree, devgit.MergeOptions{})
	if err != nil {
		return hub.Item{}, err
	}
	if _, err := devgit.RemoveWorktree(item.Root, item.Project, item.Worktree, devgit.RemoveOptions{
		Force:        true,
		DeleteBranch: true,
	}); err != nil {
		return hub.Item{}, err
	}
	return mainItem(result, item.Root), nil
}

func mainItem(result devgit.MergeResult, root string) hub.Item {
	return hub.Item{
		Kind:     hub.KindWorktree,
		Project:  result.Project,
		Worktree: result.MainWorktree,
		Label:    result.Project + "/" + result.MainWorktree,
		Path:     result.MainWorktreePath,
		Root:     root,
		Session:  session.Name(result.Project, result.MainWorktree),
	}
}
