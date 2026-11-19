package backend

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"devtools/internal/client"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

var inDevtools = mux.Context{Where: mux.InDevtools}

// paneSnapshot is the state of the fake server: two sessions of one
// project, one pane each, and an agent in the main session.
var paneSnapshot = proto.Snapshot{
	Rev: 3,
	Sessions: []proto.Session{
		{ID: "s1", Dir: "/work/proj/feat", Label: "proj-feat", Tabs: []string{"t1"}, Current: "t1"},
		{ID: "s2", Dir: "/work/proj/main", Label: "proj-main", Tabs: []string{"t2"}, Current: "t2"},
	},
	Tabs: []proto.Tab{
		{ID: "t1", Session: "s1", Index: 1, Panes: []string{"p1"}, Focused: "p1"},
		{ID: "t2", Session: "s2", Index: 1, Panes: []string{"p2"}, Focused: "p2"},
	},
	Panes: []proto.Pane{
		{ID: "p1", Tab: "t1", Session: "s1", State: proto.PaneRunning, Cwd: "/work/proj/feat"},
		{ID: "p2", Tab: "t2", Session: "s2", State: proto.PaneRunning, Cwd: "/work/proj/main"},
	},
	Agents: []proto.Agent{
		{Pane: "p2", Tab: "t2", Session: "s2", Kind: proto.AgentClaude, Status: proto.StatusBlocked, Title: "fix it", Cwd: "/work/proj/main"},
	},
}

// openDevtools opens the devtools backend in context c against the fake
// server, with DEVTOOLS_PANE set to pane.
func openDevtools(t *testing.T, f *fakeServer, c mux.Context, pane string, d Deps) *DevtoolsMultiplexer {
	t.Helper()
	d.Env = append([]string{proto.EnvSocket + "=" + f.socket, proto.EnvPane + "=" + pane}, d.Env...)
	m, err := Open(mux.Devtools, c, d)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*DevtoolsMultiplexer)
}

// recordClient replaces runClient for the test and returns the options of
// every call.
func recordClient(t *testing.T, result error) *[]client.Options {
	t.Helper()
	var seen []client.Options
	old := runClient
	runClient = func(o client.Options) error {
		seen = append(seen, o)
		return result
	}
	t.Cleanup(func() { runClient = old })
	return &seen
}

func wantRequests(t *testing.T, f *fakeServer, want ...string) {
	t.Helper()
	if got := f.Requests(); !slices.Equal(got, want) {
		t.Fatalf("requests of the fake server =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestDevtoolsShowSwitchesTheWindowOfThePane(t *testing.T) {
	t.Run("a window shows the pane", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		m := openDevtools(t, f, inDevtools, "p1", Deps{})
		shown, err := m.Show("/work/proj/main", "proj-main")
		if err != nil || shown != mux.ShownInWindow {
			t.Fatalf("Show = %v, %v; want ShownInWindow", shown, err)
		}
		wantRequests(t, f, `ui.switch {"dir":"/work/proj/main","name":"proj-main"}`)
		h := f.Hellos()
		if len(h) != 1 || h[0].Kind != proto.KindOneshot || h[0].Pane != "p1" || len(h[0].Ancestors) == 0 || h[0].Ancestors[0] != os.Getpid() {
			t.Fatalf("hellos = %+v, want one one-shot with the pane and this pid first", h)
		}
	})
	t.Run("no window shows the pane", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		f.set(func(f *fakeServer) { f.noClient = true })
		m := openDevtools(t, f, inDevtools, "p1", Deps{})
		shown, err := m.Show("/work/proj/main", "proj-main")
		if err != nil || shown != mux.OpenedOnly {
			t.Fatalf("Show = %v, %v; want OpenedOnly", shown, err)
		}
		wantRequests(t, f,
			`ui.switch {"dir":"/work/proj/main","name":"proj-main"}`,
			`session.open {"dir":"/work/proj/main","name":"proj-main"}`)
	})
	t.Run("the server does not take the pane for the caller's", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		f.set(func(f *fakeServer) { f.refuse = proto.CodeNoPane })
		m := openDevtools(t, f, inDevtools, "p1", Deps{})
		shown, err := m.Show("/work/proj/main", "proj-main")
		if err != nil || shown != mux.OpenedOnly {
			t.Fatalf("Show = %v, %v; want OpenedOnly", shown, err)
		}
		wantRequests(t, f, `session.open {"dir":"/work/proj/main","name":"proj-main"}`)
	})
}

func TestDevtoolsShowWithoutAPaneOpensOnly(t *testing.T) {
	for _, c := range []mux.Context{inTerminal, {Where: mux.NoTerminal}, {Where: mux.InForeign, Foreign: "screen"}} {
		t.Run(whereName(c.Where), func(t *testing.T) {
			f := newFakeServer(t, paneSnapshot)
			// a pane variable that is set decides nothing outside a pane
			m := openDevtools(t, f, c, "p1", Deps{})
			shown, err := m.Show("/work/proj/main", "proj-main")
			if err != nil || shown != mux.OpenedOnly {
				t.Fatalf("Show = %v, %v; want OpenedOnly", shown, err)
			}
			wantRequests(t, f, `session.open {"dir":"/work/proj/main","name":"proj-main"}`)
			for _, h := range f.Hellos() {
				if h.Pane != "" {
					t.Fatalf("a hello named the pane %s outside a pane", h.Pane)
				}
			}
		})
	}
}

func TestLeaveSwitchesAwayOnlyWhenThePaneIsInTheSessionBeingRemoved(t *testing.T) {
	t.Run("the pane is in the session", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		m := openDevtools(t, f, inDevtools, "p1", Deps{})
		closeLast, err := m.Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		wantRequests(t, f, `state.get {}`, `ui.switch {"dir":"/work/proj/main","name":"proj-main"}`)
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		wantRequests(t, f, `state.get {}`, `ui.switch {"dir":"/work/proj/main","name":"proj-main"}`, `session.close {"dir":"/work/proj/feat"}`)
	})
	t.Run("the pane is in another session", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		m := openDevtools(t, f, inDevtools, "p2", Deps{})
		closeLast, err := m.Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		wantRequests(t, f, `state.get {}`, `session.close {"dir":"/work/proj/feat"}`)
	})
	t.Run("outside a pane", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		m := openDevtools(t, f, inTerminal, "p1", Deps{})
		closeLast, err := m.Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		if got := f.Requests(); len(got) != 0 {
			t.Fatalf("Leave outside a pane sent %q before the close", got)
		}
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		wantRequests(t, f, `session.close {"dir":"/work/proj/feat"}`)
	})
}

// The close is the last request, on a connection of its own: when every
// connection of the command was hung up after Leave (the window moved, the
// terminal went away), the close still reaches the server.
func TestCloseIsSentLastAndSurvivesHangup(t *testing.T) {
	f := newFakeServer(t, paneSnapshot)
	m := openDevtools(t, f, inDevtools, "p1", Deps{})
	closeLast, err := m.Leave("/work/proj/feat", "/work/proj/main", "proj-main")
	if err != nil {
		t.Fatal(err)
	}
	f.set(func(f *fakeServer) {
		for _, c := range f.conns {
			c.Close()
		}
	})
	before := len(f.Hellos())
	if err := closeLast(); err != nil {
		t.Fatalf("the close after a hangup: %v", err)
	}
	got := f.Requests()
	if len(got) == 0 || got[len(got)-1] != `session.close {"dir":"/work/proj/feat"}` {
		t.Fatalf("requests = %q, want the close last", got)
	}
	if after := len(f.Hellos()); after != before+1 {
		t.Fatalf("the close said %d hellos, want one connection of its own", after-before)
	}
}

func TestDevtoolsKnowsOnlyAPaneOfTheSnapshot(t *testing.T) {
	f := newFakeServer(t, paneSnapshot)
	if got := devtoolsKnows(f.socket, "p1"); got != mux.Live {
		t.Errorf("a pane of the snapshot: %v, want Live", got)
	}
	if got := devtoolsKnows(f.socket, "p9"); got != mux.Dead {
		t.Errorf("a pane the snapshot lacks: %v, want Dead", got)
	}
	if got := devtoolsKnows(f.socket+".missing", "p1"); got != mux.Dead {
		t.Errorf("a socket that does not exist: %v, want Dead", got)
	}
	if got := devtoolsKnows(refusedSocket(t), "p1"); got != mux.Dead {
		t.Errorf("a socket nobody listens on: %v, want Dead", got)
	}

	silent := newFakeServer(t, paneSnapshot)
	silent.set(func(f *fakeServer) { f.silent = true })
	start := time.Now()
	if got := devtoolsKnows(silent.socket, "p1"); got != mux.Unknown {
		t.Errorf("a server that does not answer: %v, want Unknown", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the check took %v, want the limit of 500 ms", d)
	}

	garbage := newFakeServer(t, paneSnapshot)
	garbage.set(func(f *fakeServer) { f.garbage = true })
	if got := devtoolsKnows(garbage.socket, "p1"); got != mux.Unknown {
		t.Errorf("a server that answers garbage: %v, want Unknown", got)
	}
}

func TestUnavailableWordsNameTmuxOnlyWhenItIsInstalled(t *testing.T) {
	devtools := &mux.ErrUnavailable{Backend: mux.Devtools, Reason: "no server"}
	t.Run("without tmux", func(t *testing.T) {
		if got, want := UnavailableWords(devtools), ui.Message("noserver"); got != want {
			t.Fatalf("UnavailableWords = %q, want %q", got, want)
		}
	})
	t.Run("with tmux", func(t *testing.T) {
		log := installFakeTmux(t)
		if got, want := UnavailableWords(devtools), ui.Message("noserver", "tmux"); got != want {
			t.Fatalf("UnavailableWords = %q, want %q", got, want)
		}
		for _, b := range []mux.Backend{mux.Tmux, mux.Herdr} {
			if got, want := UnavailableWords(&mux.ErrUnavailable{Backend: b}), ui.Message("unavailable", string(b)); got != want {
				t.Fatalf("UnavailableWords(%s) = %q, want %q", b, got, want)
			}
		}
		// the PATH lookup is all: tmux itself never ran
		wantEmpty(t, log)
	})
}

// actionsProbe is a hub.Actions that records its calls.
type actionsProbe struct{ created []hub.CreateRequest }

func (a *actionsProbe) Create(req hub.CreateRequest) (hub.Item, error) {
	a.created = append(a.created, req)
	return hub.Item{}, nil
}
func (a *actionsProbe) Merge(item hub.Item) (hub.Item, error) { return item, nil }

func TestDevtoolsEnterBuildsTheClientOptions(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{{Name: "macmini", Connect: true}, {Name: "server"}},
		Binds:    []config.Bind{{Chord: "prefix+y", Action: "tab-new", Line: 3}},
		Unbinds:  []config.Unbind{{Chord: "prefix+c", Line: 4}},
		Colors:   []config.Color{{Token: "warning", Light: "#aa0000", Dark: "#ff5555", Line: 5}},
	}
	actions := &actionsProbe{}
	var fleets []fleet.Options
	oldFleet := newFleet
	newFleet = func(o fleet.Options) *fleet.Fleet {
		fleets = append(fleets, o)
		return oldFleet(o)
	}
	t.Cleanup(func() { newFleet = oldFleet })

	f := newFakeServer(t, paneSnapshot)
	target := &session.Target{Dir: "/work/proj/main", Name: "proj-main"}
	for _, e := range []struct {
		name  string
		entry mux.Entry
	}{
		{"a target", mux.Entry{Target: target}},
		{"a hub", mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAgents, Query: "fix"}}},
		{"nothing", mux.Entry{}},
	} {
		t.Run(e.name, func(t *testing.T) {
			seen := recordClient(t, nil)
			fleets = nil
			m := openDevtools(t, f, inTerminal, "", Deps{Config: cfg, Actions: actions, Version: "1.2.3"})
			pass := realPass(t)
			if err := m.Enter(pass, e.entry); err != nil {
				t.Fatal(err)
			}
			if len(*seen) != 1 {
				t.Fatalf("runClient was called %d times, want once", len(*seen))
			}
			o := (*seen)[0]
			if o.Pass != pass || !o.Pass.Valid() {
				t.Fatal("the client was not given the pass of the guard")
			}
			if got := o.Keys.Chords("tab-new", ""); !slices.Contains(got, "prefix+y") || slices.Contains(got, "prefix+c") {
				t.Fatalf("chords of tab-new = %q, want the bind line and not the unbound default", got)
			}
			if len(fleets) != 1 || !slices.Equal(fleets[0].Machines, cfg.Machines) || fleets[0].Dialer == nil {
				t.Fatalf("fleet options = %+v, want the machines of the config and a dialer", fleets)
			}
			if o.Fleet == nil || o.Chrome == nil || o.OpenHub == nil {
				t.Fatalf("options lack the fleet, the chrome or the hub: %+v", o)
			}
			if overlay := o.OpenHub(fleet.HubRequest{Mode: hub.ModeAll}, make(chan hub.Event), ui.Theme{}); overlay == nil {
				t.Fatal("OpenHub returned no overlay")
			}
			if !slices.Equal(o.Colors, cfg.Colors) || o.Version != "1.2.3" {
				t.Fatalf("colors %+v, version %q", o.Colors, o.Version)
			}
			switch {
			case e.entry.Target != nil:
				if o.Initial == nil || *o.Initial != *target || o.Hub != nil {
					t.Fatalf("initial %+v, hub %+v; want the target", o.Initial, o.Hub)
				}
			case e.entry.Hub != nil:
				if o.Hub == nil || o.Hub.Mode != hub.ModeAgents || o.Hub.Query != "fix" || o.Initial != nil {
					t.Fatalf("initial %+v, hub %+v; want the hub in agents mode", o.Initial, o.Hub)
				}
			default:
				// no session of the fixture was ever shown: the hub
				if o.Initial != nil || o.Hub == nil || o.Hub.Mode != hub.ModeAll || o.Hub.Query != "" {
					t.Fatalf("initial %+v, hub %+v; want the hub in mode all", o.Initial, o.Hub)
				}
			}
			// building the options asks the server nothing, except what
			// was shown last when the entry names nothing
			want := []string{}
			if e.entry.Target == nil && e.entry.Hub == nil {
				want = []string{`state.get {}`}
			}
			wantRequests(t, f, want...)
			f.set(func(f *fakeServer) { f.requests = nil })
		})
	}

	t.Run("a bind line the key table refuses starts nothing", func(t *testing.T) {
		seen := recordClient(t, nil)
		bad := &config.Config{Binds: []config.Bind{{Chord: "prefix+y", Action: "zoom", Line: 7}}}
		m := openDevtools(t, f, inTerminal, "", Deps{Config: bad})
		err := m.Enter(realPass(t), mux.Entry{})
		if err == nil || !strings.Contains(err.Error(), "line 7") || !strings.Contains(err.Error(), "prefix+y") {
			t.Fatalf("Enter = %v, want a start error naming the line and the chord", err)
		}
		if len(*seen) != 0 {
			t.Fatal("the client started")
		}
	})

	t.Run("the server went away before the client", func(t *testing.T) {
		recordClient(t, client.ErrNoServer)
		m := openDevtools(t, f, inTerminal, "", Deps{})
		err := m.Enter(realPass(t), mux.Entry{})
		if u, ok := errAs[*mux.ErrUnavailable](err); !ok || u.Backend != mux.Devtools {
			t.Fatalf("Enter = %v, want *ErrUnavailable for devtools", err)
		}
	})
}

// `devtools attach` in a plain terminal is Enter with Entry{}: the client
// shows the session most recently shown, else it opens with the hub
// (4.5, the attach row). Found by the tests of P2-11.
func TestDevtoolsAttachShowsTheSessionMostRecentlyShownElseTheHub(t *testing.T) {
	enter := func(t *testing.T, socket string) client.Options {
		t.Helper()
		seen := recordClient(t, nil)
		m, err := Open(mux.Devtools, inTerminal, Deps{Env: []string{proto.EnvSocket + "=" + socket}})
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Enter(realPass(t), mux.Entry{}); err != nil {
			t.Fatal(err)
		}
		if len(*seen) != 1 {
			t.Fatalf("runClient was called %d times, want once", len(*seen))
		}
		return (*seen)[0]
	}
	wantHub := func(t *testing.T, o client.Options) {
		t.Helper()
		if o.Initial != nil || o.Hub == nil || o.Hub.Mode != hub.ModeAll || o.Hub.Query != "" {
			t.Fatalf("initial %+v, hub %+v; want the hub in mode all", o.Initial, o.Hub)
		}
	}

	t.Run("a session was shown", func(t *testing.T) {
		snap := paneSnapshot
		snap.Sessions = []proto.Session{
			{ID: "s1", Dir: "/work/proj/feat", Label: "proj-feat", Tabs: []string{"t1"}, Current: "t1", Shown: 1795000000000},
			{ID: "s2", Dir: "/work/proj/main", Label: "proj-main", Tabs: []string{"t2"}, Current: "t2", Shown: 1795000009000},
			{ID: "s3", Dir: "/work/other", Label: "other", Tabs: []string{"t3"}, Current: "t3"},
		}
		f := newFakeServer(t, snap)
		o := enter(t, f.socket)
		if o.Initial == nil || *o.Initial != (session.Target{Session: "s2"}) || o.Hub != nil {
			t.Fatalf("initial %+v, hub %+v; want the session s2, shown last, and no hub", o.Initial, o.Hub)
		}
		wantRequests(t, f, `state.get {}`)
	})
	t.Run("no session was ever shown", func(t *testing.T) {
		f := newFakeServer(t, paneSnapshot)
		wantHub(t, enter(t, f.socket))
	})
	t.Run("the server has no session", func(t *testing.T) {
		f := newFakeServer(t, proto.Snapshot{Rev: 1})
		wantHub(t, enter(t, f.socket))
	})
	t.Run("the snapshot cannot be read", func(t *testing.T) {
		// the client then says that the server is not there (ErrNoServer)
		wantHub(t, enter(t, refusedSocket(t)))
	})
}

func TestDevtoolsHubAsksTheWindow(t *testing.T) {
	f := newFakeServer(t, paneSnapshot)
	m := openDevtools(t, f, inDevtools, "p1", Deps{})
	if err := m.Hub(hub.ModeAgents, "fix"); err != nil {
		t.Fatal(err)
	}
	got := f.Requests()
	if len(got) != 1 || !strings.HasPrefix(got[0], `ui.hub {"here":`) || !strings.Contains(got[0], `"mode":"agents"`) || !strings.Contains(got[0], `"query":"fix"`) {
		t.Fatalf("requests = %q, want one ui.hub in agents mode", got)
	}
	f.set(func(f *fakeServer) { f.noClient = true })
	if err := m.Hub(hub.ModeAll, ""); err == nil || err.Error() != ui.Message("no-client") {
		t.Fatalf("Hub without a window = %v, want the no-client words", err)
	}
}

func TestDevtoolsListsSessionsAgentsAndTheSnapshot(t *testing.T) {
	f := newFakeServer(t, paneSnapshot)
	m := openDevtools(t, f, inTerminal, "", Deps{})
	sessions, err := m.Sessions(t.Context())
	if err != nil || len(sessions) != 2 || sessions[0].SessionID != "s1" || sessions[1].State.Agent != proto.StatusBlocked {
		t.Fatalf("Sessions = %+v, %v", sessions, err)
	}
	agents, err := m.Agents(t.Context())
	if err != nil || len(agents) != 1 || agents[0].Pane != "p2" || agents[0].Kind != hub.KindAgent || agents[0].Status != proto.StatusBlocked {
		t.Fatalf("Agents = %+v, %v", agents, err)
	}
	snap, err := m.Snapshot(t.Context())
	if err != nil || len(snap.Panes) != 2 || snap.Rev != 3 {
		t.Fatalf("Snapshot = %+v, %v", snap, err)
	}
	wantRequests(t, f, `state.get {}`, `state.get {}`, `state.get {}`)
}

func TestDevtoolsFocusPaneMovesFromThePane(t *testing.T) {
	f := newFakeServer(t, paneSnapshot)
	f.set(func(f *fakeServer) { f.moved = true })
	m := openDevtools(t, f, inDevtools, "p1", Deps{})
	moved, err := m.FocusPane("right")
	if err != nil || !moved {
		t.Fatalf("FocusPane = %v, %v", moved, err)
	}
	wantRequests(t, f, `pane.focus {"direction":"right","pane":"p1"}`)

	outside := openDevtools(t, f, inTerminal, "p1", Deps{})
	if _, err := outside.FocusPane("left"); err == nil {
		t.Fatal("FocusPane outside a pane did not fail")
	}
	if got := f.Requests(); len(got) != 1 {
		t.Fatalf("FocusPane outside a pane sent %q", got[1:])
	}
}

func TestDevtoolsJumpShowsThePaneOfTheAgent(t *testing.T) {
	f := newFakeServer(t, paneSnapshot)
	m := openDevtools(t, f, inDevtools, "p1", Deps{})
	if err := m.Jump(hub.Item{Kind: hub.KindAgent, SessionID: "s2", Tab: "t2", Pane: "p2"}); err != nil {
		t.Fatal(err)
	}
	wantRequests(t, f, `ui.switch {"pane":"p2","session":"s2","tab":"t2"}`)
}
