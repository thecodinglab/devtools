package cli

import (
	"os"
	"path/filepath"
	"strings"

	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/herdr"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/tmux"
	"devtools/internal/ui"
)

// runHub shows the switcher; tests replace it.
var runHub = hub.Run

// openHub shows the switcher over every worktree and bookmark, opened from
// the directory of the pane below it when herdr runs it as a popup. It
// shows before the roots are scanned, which it leaves to the switcher.
func (a *app) openHub(mode hub.Mode, query string) error {
	return a.runHub(mode, query, func() ([]hub.Item, error) {
		g, err := a.globals()
		if err != nil {
			return nil, err
		}
		targets, err := scanTargets(g)
		if err != nil {
			return nil, err
		}
		return hubItems(g, withBookmarks(targets, g.bookmarks)), nil
	})
}

// openHubWith shows the switcher over targets already found.
func (a *app) openHubWith(g globals, targets []discovery.Target, query string) error {
	items := hubItems(g, targets)
	return a.runHub(hub.ModeAll, query, func() ([]hub.Item, error) { return items, nil })
}

func (a *app) runHub(mode hub.Mode, query string, load func() ([]hub.Item, error)) error {
	backend, err := a.multiplexer()
	if err != nil {
		return err
	}
	if mode == hub.ModeOpen && backend == mux.Herdr {
		if err := ensureLocalServer(""); err != nil {
			return err
		}
	}
	b := hubBackend{app: a, backend: backend}
	item, err := runHub(hub.Options{
		Load:          load,
		Here:          hubHere(),
		Mode:          mode,
		Query:         query,
		Backend:       b,
		SwitchInPlace: switchesInPlace(backend),
	})
	if err != nil || item == nil {
		return err
	}
	return b.Switch(*item)
}

// switchesInPlace reports whether switching leaves the terminal alone, so
// the switcher can do it and show a failure itself. Outside a multiplexer
// switching attaches the terminal, which needs the switcher gone first.
func switchesInPlace(backend mux.Backend) bool {
	if backend == mux.Herdr {
		return herdr.Inside()
	}
	return os.Getenv("TMUX") != ""
}

// hubHere is the directory the switcher counts as where you are: the pane
// under a herdr popup, else the current directory.
func hubHere() string {
	if dir := os.Getenv("HERDR_ACTIVE_PANE_CWD"); dir != "" {
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() {
			return dir
		}
	}
	dir, _ := os.Getwd()
	return dir
}

func hubItems(g globals, targets []discovery.Target) []hub.Item {
	items := make([]hub.Item, 0, len(targets))
	for _, target := range targets {
		item := hub.Item{
			Project:  target.Project,
			Worktree: target.Worktree,
			Label:    target.Label,
			Path:     target.Path,
			Session:  tmux.SessionName(target.Project, target.Worktree),
		}
		switch target.Kind {
		case "worktree":
			item.Kind = hub.KindWorktree
			item.Root = rootOf(g, target.Path)
		case "bookmark":
			item.Kind = hub.KindBookmark
		default:
			item.Kind = hub.KindRepo
		}
		items = append(items, item)
	}
	return items
}

func rootOf(g globals, path string) string {
	for _, root := range g.roots {
		if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
			return root
		}
	}
	return g.primaryRoot()
}

// hubBackend does the switcher's work with the multiplexer devtools uses.
type hubBackend struct {
	app     *app
	backend mux.Backend
}

func (b hubBackend) Status(path string) (ui.Git, error) {
	status, err := devgit.Status(path)
	if err != nil {
		return ui.Git{}, err
	}
	return ui.Git{
		Changes:     status.Changes,
		Ahead:       status.Ahead,
		Behind:      status.Behind,
		HasUpstream: status.HasUpstream,
		Detached:    status.Detached,
		Branch:      status.Branch,
	}, nil
}

func (b hubBackend) Sessions() (map[string]ui.Session, []hub.Item, error) {
	if b.backend == mux.Herdr {
		return herdrSessions()
	}
	return tmuxSessions()
}

// herdrSessions reads the open workspaces, each with its most urgent agent,
// and returns every workspace as an item of its own too. It never starts a
// server.
func herdrSessions() (map[string]ui.Session, []hub.Item, error) {
	if !herdr.Inside() {
		running, err := herdr.ServerRunning()
		if err != nil || !running {
			return nil, nil, err
		}
	}
	client := herdr.Client{}
	// both at once, each is a round trip to the server
	var agents []herdr.Agent
	agentsDone := make(chan struct{})
	go func() {
		defer close(agentsDone)
		agents, _ = client.ListAgents()
	}()
	workspaces, err := client.ListWorkspaces()
	<-agentsDone
	if err != nil {
		return nil, nil, err
	}
	// sorted most urgent first, so the first agent of a workspace speaks
	// for it
	agentOf := map[string]herdr.Agent{}
	for _, agent := range agents {
		if _, ok := agentOf[agent.Workspace]; !ok {
			agentOf[agent.Workspace] = agent
		}
	}
	sessions := make(map[string]ui.Session, len(workspaces))
	var others []hub.Item
	for _, workspace := range workspaces {
		session := ui.Session{Open: true, Focused: workspace.Focused}
		if agent, ok := agentOf[workspace.ID]; ok {
			session.Agent = agent.Status
			session.Title = agent.Title
		}
		// devtools finds a session's workspace by label, the first one
		if _, ok := sessions[workspace.Label]; !ok {
			sessions[workspace.Label] = session
		}
		others = append(others, hub.Item{
			Kind:        hub.KindWorkspace,
			Project:     workspace.Label,
			Label:       workspace.Label,
			Session:     workspace.Label,
			WorkspaceID: workspace.ID,
			State:       session,
		})
	}
	return sessions, others, nil
}

func tmuxSessions() (map[string]ui.Session, []hub.Item, error) {
	list, err := tmux.ListSessions()
	if err != nil {
		return nil, nil, err
	}
	sessions := make(map[string]ui.Session, len(list))
	var others []hub.Item
	for _, session := range list {
		state := ui.Session{Open: true, Focused: session.Attached}
		sessions[session.Name] = state
		others = append(others, hub.Item{
			Kind:    hub.KindWorkspace,
			Project: session.Name,
			Label:   session.Name,
			Session: session.Name,
			State:   state,
		})
	}
	return sessions, others, nil
}

func (b hubBackend) Switch(item hub.Item) error {
	if item.Kind != hub.KindWorkspace {
		return b.app.switchSession(item.Path, item.Session)
	}
	if b.backend == mux.Herdr {
		return herdr.Visit(func() error {
			return herdr.Client{}.FocusWorkspace(item.WorkspaceID)
		})
	}
	return tmux.SwitchSession(item.Session)
}

func (b hubBackend) Create(req hub.CreateRequest) (hub.Item, error) {
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
		Session:  tmux.SessionName(result.Project, result.Worktree),
	}, nil
}

func (b hubBackend) Merge(item hub.Item) (hub.Item, error) {
	result, closeSession, err := b.app.mergeWorktree(item.Root, item.Project, item.Worktree, false)
	if err != nil {
		return hub.Item{}, err
	}
	if err := closeSession(); err != nil {
		return hub.Item{}, err
	}
	return hub.Item{
		Kind:     hub.KindWorktree,
		Project:  result.Project,
		Worktree: result.MainWorktree,
		Label:    result.Project + "/" + result.MainWorktree,
		Path:     result.MainWorktreePath,
		Root:     item.Root,
		Session:  tmux.SessionName(result.Project, result.MainWorktree),
	}, nil
}
