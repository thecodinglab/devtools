package backend

// The tmux backend (5.3.11 of the plan, P2-14). Its capability set is
// detached-open only (F39). Inside tmux it runs only switch-client,
// new-session -d, kill-session and detach-client, none of which starts a
// client; Enter is the one method that starts one, through tmux.Attach and
// only with a valid guard pass. Agents, Jump, Snapshot and FocusPane are
// refused before anything runs (R14).

import (
	"context"
	"errors"
	"path/filepath"

	"devtools/internal/client/guard"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/tmux"
)

// TmuxMultiplexer is the commands' view of tmux.
type TmuxMultiplexer struct {
	inside bool // the context the backend was opened with is InTmux
	deps   Deps
}

func newTmux(c mux.Context, d Deps) (*TmuxMultiplexer, error) {
	return &TmuxMultiplexer{inside: c.Where == mux.InTmux, deps: d}, nil
}

// tmuxAvailable: the program is found on PATH.
func tmuxAvailable() error {
	if !tmux.Installed() {
		return errors.New("tmux is not installed")
	}
	return nil
}

func (m *TmuxMultiplexer) Name() mux.Backend { return mux.Tmux }
func (m *TmuxMultiplexer) Caps() mux.Caps    { return CapsOf(m.Name()) }

// Show makes the session exist, detached when it is missing. Inside tmux
// it then switches the caller's client to it; outside no window shows the
// caller, the session is only opened. It never attaches.
func (m *TmuxMultiplexer) Show(dir, name string) (mux.Shown, error) {
	if !tmux.Has(name) {
		if err := tmux.NewDetached(name, dir); err != nil {
			return 0, err
		}
	}
	if !m.inside {
		return mux.OpenedOnly, nil
	}
	if err := tmux.SwitchClient(name); err != nil {
		return 0, err
	}
	return mux.ShownInWindow, nil
}

// Leave is tmux.Remove: inside tmux the client moves to the fallback and
// the session of dir ends; outside it only ends. Whether the caller is
// inside comes from the context, never from the environment. The session
// ends here, as today; the returned function has nothing left to do.
func (m *TmuxMultiplexer) Leave(dir, fallbackDir, fallbackName string) (func() error, error) {
	if err := tmux.Remove(sessionOfDir(dir), fallbackName, fallbackDir, m.inside); err != nil {
		return nil, err
	}
	return func() error { return nil }, nil
}

// Hub runs the standalone hub in the caller's pane or popup and shows the
// pick. Only valid inside tmux.
func (m *TmuxMultiplexer) Hub(mode hub.Mode, query string) error {
	if !m.inside {
		return errors.New("the tmux hub runs only inside tmux")
	}
	pick, err := standaloneHub(m, m.deps, mode, query, hub.Features{})
	if err != nil || pick == nil {
		return err
	}
	_, err = m.Show(pick.Path, pick.Session)
	return err
}

// Sessions lists the tmux sessions of this machine.
func (m *TmuxMultiplexer) Sessions(ctx context.Context) ([]hub.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	list, err := tmux.ListSessions()
	if err != nil {
		return nil, err
	}
	items := make([]hub.Item, 0, len(list))
	for _, s := range list {
		items = append(items, hub.Item{
			Kind:      hub.KindSession,
			Project:   s.Name,
			Label:     s.Name,
			Session:   s.Name,
			SessionID: s.Name,
			State:     uiSession(true, s.Attached, "", ""),
		})
	}
	return items, nil
}

// Enter turns this terminal into a tmux client. Without a valid pass it
// returns ErrNoPass and runs nothing. With Entry.Hub the standalone hub
// runs first, in the plain terminal, and a cancelled pick starts no
// client. Then tmux.Attach of the pick, of Entry.Target, or of the session
// tmux used last.
func (m *TmuxMultiplexer) Enter(p *guard.Pass, e mux.Entry) error {
	if !p.Valid() {
		return mux.ErrNoPass
	}
	var name, dir string
	switch {
	case e.Hub != nil:
		pick, err := standaloneHub(m, m.deps, e.Hub.Mode, e.Hub.Query, hub.Features{})
		if err != nil || pick == nil {
			return err
		}
		name, dir = pick.Session, pick.Path
	case e.Target != nil:
		name, dir = targetName(e.Target), e.Target.Dir
	}
	return tmux.Attach(name, dir)
}

func (m *TmuxMultiplexer) Agents(ctx context.Context) ([]hub.Item, error) {
	return nil, m.refuse(mux.CapAgents)
}

func (m *TmuxMultiplexer) Jump(agent hub.Item) error { return m.refuse(mux.CapAgents) }

func (m *TmuxMultiplexer) Snapshot(ctx context.Context) (proto.Snapshot, error) {
	return proto.Snapshot{}, m.refuse(mux.CapLayout)
}

func (m *TmuxMultiplexer) FocusPane(direction string) (bool, error) {
	return false, m.refuse(mux.CapPaneFocus)
}

// refuse is what every gated method of tmux is: tmux has none of the four
// capabilities they need, so each returns *mux.ErrUnsupported and does
// nothing else.
func (m *TmuxMultiplexer) refuse(c mux.Capability) error {
	return &mux.ErrUnsupported{Backend: m.Name(), Capability: c}
}

// sessionOfDir is the name of the session devtools gives the worktree at
// dir: session.Name of its project and worktree, which in the bare layout
// are the names of dir's parent and of dir (internal/devgit). merge and
// done, the callers of Leave, work on such worktrees only.
func sessionOfDir(dir string) string {
	dir = filepath.Clean(dir)
	return session.Name(filepath.Base(filepath.Dir(dir)), filepath.Base(dir))
}

// targetName is the label of the session a Target names: its Name, else
// its Session, else the session of its Dir.
func targetName(t *session.Target) string {
	switch {
	case t.Name != "":
		return t.Name
	case t.Session != "":
		return t.Session
	case t.Dir != "":
		return sessionOfDir(t.Dir)
	}
	return ""
}
