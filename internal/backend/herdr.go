package backend

// The herdr backend (5.3.11 of the plan, P2-14), reduced to what an
// unpatched herdr 0.9.5 does on one machine (F37; step 0 of P2-14 found
// every call it makes in the release,
// docs/replace-herdr/experiments/p2-14-herdr-release.md). Its capability
// set is agent-status only: it cannot open a session without showing it,
// so Show outside herdr is refused, and Snapshot and FocusPane are refused
// before anything runs (R14). Enter is the one method that starts a herdr
// client, through herdr.Attach and only with a valid guard pass.
// herdr.Inside, herdr.CloseForRemoval and herdr.Visit are not called: they
// read the environment, and P2-09 deletes them.

import (
	"context"
	"errors"

	"devtools/internal/client/guard"
	"devtools/internal/herdr"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
)

// HerdrMultiplexer is the commands' view of herdr.
type HerdrMultiplexer struct {
	inside bool // the context the backend was opened with is InHerdr
	deps   Deps
	client herdr.Client
}

func newHerdr(c mux.Context, d Deps) (*HerdrMultiplexer, error) {
	return &HerdrMultiplexer{inside: c.Where == mux.InHerdr, deps: d}, nil
}

// herdrAvailable: the program is found on PATH or through HERDR_BIN_PATH.
func herdrAvailable() error {
	if !herdr.Installed() {
		return errors.New("herdr is not installed")
	}
	return nil
}

func (m *HerdrMultiplexer) Name() mux.Backend { return mux.Herdr }
func (m *HerdrMultiplexer) Caps() mux.Caps    { return CapsOf(m.Name()) }

// Show inside herdr focuses the workspace of the session, creating it in
// dir first. Outside herdr no window shows the caller, and herdr cannot
// open a workspace without showing it: *mux.ErrUnsupported, nothing run.
func (m *HerdrMultiplexer) Show(dir, name string) (mux.Shown, error) {
	if !m.inside {
		if err := mux.Need(m, mux.CapDetached); err != nil {
			return 0, err
		}
	}
	if err := m.client.Switch(dir, name); err != nil {
		return 0, err
	}
	return mux.ShownInWindow, nil
}

// Leave is Client.Remove, with inside taken from the context.
func (m *HerdrMultiplexer) Leave(dir, fallbackDir, fallbackName string) (func() error, error) {
	return m.client.Remove(sessionOfDir(dir), fallbackName, fallbackDir, m.inside)
}

// Hub runs the standalone hub with agents in the caller's pane or popup
// and shows or jumps to the pick. Only valid inside herdr.
func (m *HerdrMultiplexer) Hub(mode hub.Mode, query string) error {
	if !m.inside {
		return errors.New("the herdr hub runs only inside herdr")
	}
	pick, err := standaloneHub(m, m.deps, mode, query, m.features())
	if err != nil || pick == nil {
		return err
	}
	return m.focus(pick)()
}

// Sessions lists the workspaces of the herdr server. Outside herdr it
// starts no server: without a running one there is nothing to list.
func (m *HerdrMultiplexer) Sessions(ctx context.Context) ([]hub.Item, error) {
	if ok, err := m.serverUp(ctx); !ok {
		return nil, err
	}
	workspaces, err := m.client.ListWorkspaces()
	if err != nil {
		return nil, err
	}
	items := make([]hub.Item, 0, len(workspaces))
	for _, w := range workspaces {
		items = append(items, hub.Item{
			Kind:      hub.KindSession,
			Project:   w.Label,
			Label:     w.Label,
			Session:   w.Label,
			SessionID: w.ID,
			State:     uiSession(true, w.Focused, w.AgentStatus, ""),
		})
	}
	return items, nil
}

// Enter turns this terminal into a herdr client. Without a valid pass it
// returns ErrNoPass and runs nothing. With Entry.Hub the standalone hub
// runs first, in the plain terminal, and a cancelled pick starts no server
// and no client. Then herdr.EnsureServer, the focus (the picked session or
// Entry.Target, or the picked agent; nothing without either), and
// herdr.Attach. The hub runs before the client because a herdr client
// takes the terminal until it ends.
func (m *HerdrMultiplexer) Enter(p *guard.Pass, e mux.Entry) error {
	if !p.Valid() {
		return mux.ErrNoPass
	}
	focus := func() error { return nil }
	switch {
	case e.Hub != nil:
		pick, err := standaloneHub(m, m.deps, e.Hub.Mode, e.Hub.Query, m.features())
		if err != nil || pick == nil {
			return err
		}
		focus = m.focus(pick)
	case e.Target != nil:
		dir, name := e.Target.Dir, targetName(e.Target)
		focus = func() error { return m.client.Switch(dir, name) }
	}
	if err := herdr.EnsureServer(); err != nil {
		return err
	}
	if err := focus(); err != nil {
		return err
	}
	return herdr.Attach()
}

// Agents lists the agents herdr reports, most urgent first. Outside herdr
// it starts no server.
func (m *HerdrMultiplexer) Agents(ctx context.Context) ([]hub.Item, error) {
	if err := mux.Need(m, mux.CapAgents); err != nil {
		return nil, err
	}
	if ok, err := m.serverUp(ctx); !ok {
		return nil, err
	}
	agents, err := m.client.ListAgents()
	if err != nil {
		return nil, err
	}
	herdr.SortAgents(agents)
	items := make([]hub.Item, 0, len(agents))
	for _, a := range agents {
		label := a.Name
		if label == "" {
			label = a.Title
		}
		if label == "" {
			label = a.Kind
		}
		items = append(items, hub.Item{
			Kind:      hub.KindAgent,
			Label:     label,
			Path:      a.Cwd,
			SessionID: a.Workspace,
			Pane:      a.Pane,
			Agent:     a.Kind,
			Status:    a.Status,
			State:     uiSession(true, a.Focused, a.Status, a.Title),
		})
	}
	return items, nil
}

// Jump focuses the pane of an agent.
func (m *HerdrMultiplexer) Jump(agent hub.Item) error {
	if err := mux.Need(m, mux.CapAgents); err != nil {
		return err
	}
	if agent.Pane == "" {
		return errors.New("herdr: the agent has no pane")
	}
	return m.client.FocusAgent(agent.Pane)
}

func (m *HerdrMultiplexer) Snapshot(ctx context.Context) (proto.Snapshot, error) {
	return proto.Snapshot{}, m.refuse(mux.CapLayout)
}

func (m *HerdrMultiplexer) FocusPane(direction string) (bool, error) {
	return false, m.refuse(mux.CapPaneFocus)
}

// refuse is what the gated methods of herdr outside its set are: each
// returns *mux.ErrUnsupported and does nothing else.
func (m *HerdrMultiplexer) refuse(c mux.Capability) error {
	return &mux.ErrUnsupported{Backend: m.Name(), Capability: c}
}

// features is what the standalone hub of herdr shows: agents when the
// backend has agent-status, never other machines.
func (m *HerdrMultiplexer) features() hub.Features {
	return hub.Features{Agents: m.Caps().Has(mux.CapAgents)}
}

// focus returns what showing a pick of the hub is: Jump for an agent, the
// workspace by its id for an open session (labels need not be unique), and
// Switch for everything else.
func (m *HerdrMultiplexer) focus(pick *hub.Item) func() error {
	item := *pick
	switch {
	case item.Kind == hub.KindAgent:
		return func() error { return m.Jump(item) }
	case item.Kind == hub.KindSession && item.SessionID != "":
		return func() error { return m.client.FocusWorkspace(item.SessionID) }
	}
	return func() error { return m.client.Switch(item.Path, item.Session) }
}

// serverUp reports whether there is a herdr server to ask. Inside herdr
// there is; outside, only a running one counts, and none is started.
func (m *HerdrMultiplexer) serverUp(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if m.inside {
		return true, nil
	}
	return herdr.ServerRunning()
}
