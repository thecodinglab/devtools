package backend

// The devtools backend (5.3.11 of the plan, P2-08b): the commands' view of
// the devtools server and nothing more. It has every capability. Show,
// Leave, Hub, Jump and FocusPane are one-shot socket requests (one
// connection, hello with the caller's pane, 4.2); Sessions, Agents and
// Snapshot are state.get. Enter builds the options of the client and calls
// runClient, the one reference to client.Run in non-test code (rule 10,
// N11): only with a valid pass from the guard (N20).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/guard"
	"devtools/internal/client/keys"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// runClient is the client program. The tests of this package replace it
// with a recorder; it is unexported so that nothing outside Enter can reach
// it (rule 10).
var runClient = client.Run // tests replace it

// newFleet makes the client's fleet; the tests replace it to see the
// options Enter built. It dials nothing (fleet.New).
var newFleet = fleet.New

// requestLimit bounds one one-shot request: connect, hello, the request and
// its reply. The liveness and availability checks use checkLimit.
const requestLimit = 5 * time.Second

// version is what the one-shot hello says; Deps.Version when the command
// knows it.
const unknownVersion = "devtools"

// oneshotCaps are the wire capabilities a command implements (4.1): the
// routed requests, state.get and the agent records it reads.
var oneshotCaps = []string{proto.CapAgents, proto.CapRoute, proto.CapStateGet}

// DevtoolsMultiplexer is the commands' view of the devtools server.
type DevtoolsMultiplexer struct {
	deps   Deps
	env    map[string]string
	socket string
	// pane is DEVTOOLS_PANE when the command runs inside a devtools pane
	// (the context is InDevtools); "" everywhere else, where no window
	// shows the caller and a variable that is set decides nothing.
	pane string
}

func newDevtools(c mux.Context, d Deps) (*DevtoolsMultiplexer, error) {
	env := envMap(d.Env)
	socket, err := socketOf(env)
	if err != nil {
		return nil, err
	}
	m := &DevtoolsMultiplexer{deps: d, env: env, socket: socket}
	if c.Where == mux.InDevtools {
		m.pane = env[proto.EnvPane]
	}
	return m, nil
}

// envMap is env (os.Environ form; nil: the process environment) as a map;
// the last entry of a name wins.
func envMap(env []string) map[string]string {
	if env == nil {
		env = os.Environ()
	}
	vars := make(map[string]string, len(env))
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok {
			vars[name] = value
		}
	}
	return vars
}

// socketOf is the server's socket for the environment: DEVTOOLS_SOCKET when
// it is set (it must be absolute), else the default path of the state
// directory (proto.SocketPath).
func socketOf(env map[string]string) (string, error) {
	if s := env[proto.EnvSocket]; s != "" {
		if !filepath.IsAbs(s) {
			return "", fmt.Errorf("devtools: %s %q is not an absolute path", proto.EnvSocket, s)
		}
		return filepath.Clean(s), nil
	}
	return proto.SocketPath()
}

// devtoolsAvailable: the server socket answers hello.
func devtoolsAvailable() error {
	socket, err := proto.SocketPath()
	if err != nil {
		return err
	}
	conn, _, err := dialOneshot(socket, unknownVersion, "", time.Now().Add(checkLimit))
	if err != nil {
		return fmt.Errorf("no devtools server answers at %s: %w", socket, err)
	}
	conn.Close()
	return nil
}

// devtoolsKnows is Facts.Devtools: connect to socket, hello, state.get.
// Live when the pane id is in the snapshot. Dead when the socket does not
// exist, the connection is refused, or the snapshot arrives without the
// pane. Unknown when the limit of 500 ms ends first or anything else fails.
// An empty socket is the default path of the state directory.
func devtoolsKnows(socket, pane string) mux.Liveness {
	if socket == "" {
		s, err := proto.SocketPath()
		if err != nil {
			return mux.Unknown
		}
		socket = s
	}
	if _, err := os.Lstat(socket); errors.Is(err, fs.ErrNotExist) {
		return mux.Dead
	}
	conn, _, err := dialOneshot(socket, unknownVersion, "", time.Now().Add(checkLimit))
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist) {
			return mux.Dead
		}
		// a timeout, a refused hello, anything else
		return mux.Unknown
	}
	defer conn.Close()
	reply, err := conn.request(proto.StateGet{})
	if err != nil || !reply.OK || reply.Msg.T != proto.TypeStateSnapshot {
		return mux.Unknown
	}
	var snap proto.Snapshot
	if err := reply.Msg.Decode(&snap); err != nil {
		return mux.Unknown
	}
	for _, p := range snap.Panes {
		if p.ID == pane {
			return mux.Live
		}
	}
	return mux.Dead
}

// UnavailableWords returns what a person reads for e: the unavailable
// words for tmux and herdr, the noserver words for devtools, with the
// argument "tmux" when tmux is installed. It runs nothing beyond the PATH
// lookup of tmuxAvailable (N26: the fallback is words).
func UnavailableWords(e *mux.ErrUnavailable) string {
	if e.Backend == mux.Devtools {
		if tmuxAvailable() == nil {
			return ui.Message("noserver", "tmux")
		}
		return ui.Message("noserver")
	}
	return ui.Message("unavailable", string(e.Backend))
}

func (m *DevtoolsMultiplexer) Name() mux.Backend { return mux.Devtools }
func (m *DevtoolsMultiplexer) Caps() mux.Caps    { return CapsOf(m.Name()) }

// Show inside a devtools pane asks the window that shows the pane to
// switch to the session (ui.switch, ShownInWindow). When no window shows
// the pane (no-client), when the server does not accept the pane as the
// caller's (refuse no-pane), and outside a pane the session is opened on
// the server and nothing is switched (session.open, OpenedOnly).
func (m *DevtoolsMultiplexer) Show(dir, name string) (mux.Shown, error) {
	if m.pane != "" {
		_, err := m.ask(m.pane, proto.UISwitch{Dir: dir, Name: name})
		if err == nil {
			return mux.ShownInWindow, nil
		}
		if !isCode(err, proto.CodeNoClient) && !isNoPane(err) {
			return 0, err
		}
	}
	if _, err := m.ask("", proto.SessionOpen{Dir: dir, Name: name}); err != nil {
		return 0, err
	}
	return mux.OpenedOnly, nil
}

// Leave moves the window that shows the caller's pane off the session of
// dir, to the fallback, when the pane is in that session (ui.switch), and
// returns the function that ends the session (session.close{dir}). That
// function opens a connection of its own, so it works when the first one
// is gone, and it is the last thing the command does (3.5.6).
func (m *DevtoolsMultiplexer) Leave(dir, fallbackDir, fallbackName string) (func() error, error) {
	if m.pane != "" {
		snap, err := m.snapshot(context.Background())
		if err != nil {
			return nil, err
		}
		if s, ok := sessionOfPane(snap, m.pane); ok && sameDir(s.Dir, dir) {
			_, err := m.ask(m.pane, proto.UISwitch{Dir: fallbackDir, Name: fallbackName})
			if err != nil && !isCode(err, proto.CodeNoClient) && !isNoPane(err) {
				return nil, err
			}
		}
	}
	return func() error {
		_, err := m.ask("", proto.SessionClose{Dir: dir})
		return err
	}, nil
}

// Hub asks the window that shows the caller's pane to open its hub
// (ui.hub): the overlay opens there. Only valid inside a devtools pane.
func (m *DevtoolsMultiplexer) Hub(mode hub.Mode, query string) error {
	if m.pane == "" {
		return errors.New("devtools: the hub of a window opens only from a devtools pane")
	}
	here, _ := os.Getwd()
	_, err := m.ask(m.pane, proto.UIHub{Mode: wireMode(mode), Query: query, Here: here})
	if isCode(err, proto.CodeNoClient) {
		return errors.New(ui.Message("no-client"))
	}
	return err
}

// Sessions lists the sessions of the server (state.get).
func (m *DevtoolsMultiplexer) Sessions(ctx context.Context) ([]hub.Item, error) {
	snap, err := m.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	agents := map[string]proto.Agent{}
	for _, a := range sortedAgents(snap.Agents) {
		if _, ok := agents[a.Session]; !ok {
			agents[a.Session] = a
		}
	}
	items := make([]hub.Item, 0, len(snap.Sessions))
	for _, s := range snap.Sessions {
		a := agents[s.ID]
		items = append(items, hub.Item{
			Kind:      hub.KindSession,
			Project:   s.Label,
			Label:     s.Label,
			Path:      s.Dir,
			Session:   s.Label,
			SessionID: s.ID,
			State:     uiSession(true, false, a.Status, a.Title),
		})
	}
	return items, nil
}

// Enter turns this terminal into a devtools window. Without a valid pass
// it returns ErrNoPass and does nothing else. Then it builds the options of
// the client from Deps: the fleet of this machine and the configured
// machines, the key table of the bind and unbind lines, the look with its
// hint strip, the hub overlay over Deps.Actions, the color lines (checked
// by ui.NewTheme), the entry and the pass; and runs the client. An entry
// without a target and without a hub (`devtools attach`) shows the session
// most recently shown, else opens the hub (4.5). A server
// that is gone by then is *mux.ErrUnavailable, whose words are noserver.
func (m *DevtoolsMultiplexer) Enter(p *guard.Pass, e mux.Entry) error {
	if !p.Valid() {
		return mux.ErrNoPass
	}
	o, err := m.clientOptions(p, e)
	if err != nil {
		return err
	}
	err = runClient(o)
	if errors.Is(err, client.ErrNoServer) {
		return &mux.ErrUnavailable{Backend: mux.Devtools, Reason: err.Error()}
	}
	return err
}

// clientOptions builds what Enter hands the client.
func (m *DevtoolsMultiplexer) clientOptions(p *guard.Pass, e mux.Entry) (client.Options, error) {
	var cfg config.Config
	if m.deps.Config != nil {
		cfg = *m.deps.Config
	}
	table, err := keys.New(cfg.Binds, cfg.Unbinds)
	if err != nil {
		return client.Options{}, err
	}
	tokens, err := themeTokens(cfg.Colors)
	if err != nil {
		return client.Options{}, err
	}
	if _, err := ui.NewTheme(ui.ThemeOptions{Tokens: tokens}); err != nil {
		return client.Options{}, err
	}
	version := m.deps.Version
	if version == "" {
		version = unknownVersion
	}
	cols, rows := 0, 0
	if m.deps.Out != nil {
		cols, rows, _ = platform.Winsize(m.deps.Out)
	}
	f := newFleet(fleet.Options{
		Machines: cfg.Machines,
		Dialer: remote.NewDialer(remote.DialOptions{
			SSH:    "ssh",
			Socket: m.socket,
			Hello:  m.interactiveHello(version),
		}),
		Cols: cols,
		Rows: rows,
	})
	here, _ := os.Getwd()
	o := client.Options{
		Pass:    p,
		Fleet:   f,
		OpenHub: m.openHub(),
		Keys:    table,
		Chrome:  chrome.NewLook(chrome.LookOptions{Chords: table.Chords}),
		Colors:  cfg.Colors,
		Version: version,
		In:      m.deps.In,
		Out:     m.deps.Out,
		Env:     m.deps.Env,
	}
	if e.Target != nil {
		t := *e.Target
		o.Initial = &t
	}
	if e.Hub != nil {
		o.Hub = &fleet.HubRequest{Mode: e.Hub.Mode, Query: e.Hub.Query, Here: session.Target{Dir: here}}
	}
	if e.Target == nil && e.Hub == nil {
		// Entry{} (`devtools attach`): the session most recently shown,
		// else the hub in mode all (4.5, the attach row).
		if t, ok := m.mostRecentlyShown(); ok {
			o.Initial = &t
		} else {
			o.Hub = &fleet.HubRequest{Mode: hub.ModeAll, Here: session.Target{Dir: here}}
		}
	}
	return o, nil
}

// mostRecentlyShown is the local session a view was set on last: the
// greatest `shown` of the server's snapshot (4.2.2). It is false when no
// session was ever shown and when the snapshot cannot be read; the client
// then opens with the hub, and a server that is not there is the client's
// ErrNoServer, as for every other entry.
func (m *DevtoolsMultiplexer) mostRecentlyShown() (session.Target, bool) {
	snap, err := m.snapshot(context.Background())
	if err != nil {
		return session.Target{}, false
	}
	var best *proto.Session
	for i := range snap.Sessions {
		s := &snap.Sessions[i]
		if s.Shown <= 0 || !validSessionID(s.ID) {
			continue
		}
		if best == nil || s.Shown > best.Shown || (s.Shown == best.Shown && s.ID < best.ID) {
			best = s
		}
	}
	if best == nil {
		return session.Target{}, false
	}
	return session.Target{Session: best.ID}, true
}

// validSessionID: a session id of the server's form, the form the fleet
// accepts in a Target (3.5.4 step 1).
func validSessionID(id string) bool {
	return session.ValidID(id) && id[0] == 's'
}

// openHub is the constructor of the hub overlay: a closure over
// Deps.Actions, so the client never sees them, with the parts of the hub
// this backend has.
func (m *DevtoolsMultiplexer) openHub() func(req fleet.HubRequest, events <-chan hub.Event, theme ui.Theme) chrome.Overlay {
	actions := m.deps.Actions
	features := hub.Features{
		Agents:   m.Caps().Has(mux.CapAgents),
		Machines: m.Caps().Has(mux.CapMachines),
	}
	return func(req fleet.HubRequest, events <-chan hub.Event, theme ui.Theme) chrome.Overlay {
		return hub.New(hub.ModelOptions{
			Mode:     req.Mode,
			Query:    req.Query,
			Here:     req.Here,
			Events:   events,
			Actions:  actions,
			Theme:    theme,
			Features: features,
		})
	}
}

// interactiveHello is the hello of the client's local link (4.2.1): the
// terminal and the parent chain for the nesting check of the server, and
// the display variables of the allow-list (3.2.1).
func (m *DevtoolsMultiplexer) interactiveHello(version string) proto.Hello {
	h := proto.Hello{
		Gen:       proto.Gen,
		Version:   version,
		Caps:      []string{proto.CapCatalog, proto.CapAgents, proto.CapRoute, proto.CapStateGet, proto.CapTree},
		Kind:      proto.KindInteractive,
		Ancestors: ancestors(),
	}
	if m.deps.In != nil {
		if tty, err := platform.TTYName(m.deps.In); err == nil {
			h.TTY = tty
		}
	}
	for _, name := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"} {
		if v, ok := m.env[name]; ok && v != "" {
			if h.Env == nil {
				h.Env = map[string]string{}
			}
			h.Env[name] = v
		}
	}
	return h
}

// themeTokens reads the color lines of the config (#rrggbb, checked by
// config) as the tokens of ui.NewTheme.
func themeTokens(colors []config.Color) ([]ui.Token, error) {
	tokens := make([]ui.Token, 0, len(colors))
	for _, c := range colors {
		light, err := parseRGB(c.Light)
		if err != nil {
			return nil, fmt.Errorf("line %d: color %s: %v", c.Line, c.Token, err)
		}
		dark, err := parseRGB(c.Dark)
		if err != nil {
			return nil, fmt.Errorf("line %d: color %s: %v", c.Line, c.Token, err)
		}
		tokens = append(tokens, ui.Token{Name: c.Token, Light: light, Dark: dark})
	}
	return tokens, nil
}

func parseRGB(s string) (uint32, error) {
	if len(s) != 7 || s[0] != '#' {
		return 0, fmt.Errorf("%q is not of the form #rrggbb", s)
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not of the form #rrggbb", s)
	}
	return uint32(v), nil
}

// Agents lists the agents of the server, most urgent first (state.get).
func (m *DevtoolsMultiplexer) Agents(ctx context.Context) ([]hub.Item, error) {
	if err := mux.Need(m, mux.CapAgents); err != nil {
		return nil, err
	}
	snap, err := m.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	tabIndex := map[string]int{}
	for _, t := range snap.Tabs {
		tabIndex[t.ID] = t.Index
	}
	items := make([]hub.Item, 0, len(snap.Agents))
	for _, a := range sortedAgents(snap.Agents) {
		label := a.Title
		if label == "" {
			label = a.Kind
		}
		items = append(items, hub.Item{
			Kind:      hub.KindAgent,
			Label:     label,
			Path:      a.Cwd,
			SessionID: a.Session,
			Tab:       a.Tab,
			TabIndex:  tabIndex[a.Tab],
			Pane:      a.Pane,
			Agent:     a.Kind,
			Status:    a.Status,
			State:     uiSession(true, false, a.Status, a.Title),
		})
	}
	return items, nil
}

// Jump asks the window that shows the caller's pane to show the agent's
// pane (ui.switch with the session, tab and pane of the agent).
func (m *DevtoolsMultiplexer) Jump(agent hub.Item) error {
	if err := mux.Need(m, mux.CapAgents); err != nil {
		return err
	}
	if agent.Pane == "" {
		return errors.New("devtools: the agent has no pane")
	}
	_, err := m.ask(m.pane, proto.UISwitch{Session: agent.SessionID, Tab: agent.Tab, Pane: agent.Pane})
	if isCode(err, proto.CodeNoClient) {
		return errors.New(ui.Message("no-client"))
	}
	return err
}

// Snapshot returns the state of the server (state.get).
func (m *DevtoolsMultiplexer) Snapshot(ctx context.Context) (proto.Snapshot, error) {
	if err := mux.Need(m, mux.CapLayout); err != nil {
		return proto.Snapshot{}, err
	}
	return m.snapshot(ctx)
}

// errNoPane is the error of FocusPane outside a devtools pane: there is no
// pane to move the focus from.
var errNoPane = errors.New("devtools: not inside a devtools pane")

// FocusPane moves the focus of the caller's tab from the caller's pane in
// direction (pane.focus) and reports whether it moved.
func (m *DevtoolsMultiplexer) FocusPane(direction string) (bool, error) {
	if err := mux.Need(m, mux.CapPaneFocus); err != nil {
		return false, err
	}
	if m.pane == "" {
		return false, errNoPane
	}
	reply, err := m.ask(m.pane, proto.PaneFocus{Pane: m.pane, Direction: direction})
	if err != nil {
		return false, err
	}
	var r proto.PaneFocusResult
	if err := reply.Msg.Decode(&r); err != nil {
		return false, err
	}
	return r.Moved, nil
}

// snapshot is state.get on a one-shot connection without a pane.
func (m *DevtoolsMultiplexer) snapshot(ctx context.Context) (proto.Snapshot, error) {
	deadline := time.Now().Add(requestLimit)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := ctx.Err(); err != nil {
		return proto.Snapshot{}, err
	}
	conn, _, err := dialOneshot(m.socket, m.version(), "", deadline)
	if err != nil {
		return proto.Snapshot{}, err
	}
	defer conn.Close()
	reply, err := conn.request(proto.StateGet{})
	if err != nil {
		return proto.Snapshot{}, err
	}
	if !reply.OK {
		return proto.Snapshot{}, &errReply{Code: reply.Code, Text: reply.Text}
	}
	var snap proto.Snapshot
	if err := reply.Msg.Decode(&snap); err != nil {
		return proto.Snapshot{}, err
	}
	return snap, nil
}

// ask sends one request on a new one-shot connection whose hello names
// pane ("" for none) and returns its ok reply; an err reply is *errReply.
func (m *DevtoolsMultiplexer) ask(pane string, req proto.Typed) (proto.Reply, error) {
	conn, _, err := dialOneshot(m.socket, m.version(), pane, time.Now().Add(requestLimit))
	if err != nil {
		return proto.Reply{}, err
	}
	defer conn.Close()
	reply, err := conn.request(req)
	if err != nil {
		return proto.Reply{}, err
	}
	if !reply.OK {
		return reply, &errReply{Code: reply.Code, Text: reply.Text}
	}
	return reply, nil
}

func (m *DevtoolsMultiplexer) version() string {
	if m.deps.Version != "" {
		return m.deps.Version
	}
	return unknownVersion
}

// errReply is an err reply of the server: the code decides, the text is
// for a person (4.1).
type errReply struct{ Code, Text string }

func (e *errReply) Error() string {
	if e.Text != "" {
		return session.Sanitize(e.Text, 200)
	}
	return "devtools: the server answered " + e.Code
}

// errRefused is a refuse of the hello (4.2.1).
type errRefused struct{ Code, Text string }

func (e *errRefused) Error() string {
	if e.Text != "" {
		return session.Sanitize(e.Text, 200)
	}
	return "devtools: the server refused the connection: " + e.Code
}

func isCode(err error, code string) bool {
	var r *errReply
	return errors.As(err, &r) && r.Code == code
}

func isNoPane(err error) bool {
	var r *errRefused
	return errors.As(err, &r) && r.Code == proto.CodeNoPane
}

// oneshot is one connection of a command to the server.
type oneshot struct {
	conn net.Conn
	next uint64
}

// dialOneshot connects to socket and says hello as a one-shot with pane
// (4.2.1): with a pane the hello carries the caller's pid and its parent
// chain, which the server checks against the pane's child. Everything until
// the connection is closed must end by deadline.
func dialOneshot(socket, version, pane string, deadline time.Time) (*oneshot, proto.Welcome, error) {
	d := net.Dialer{Deadline: deadline}
	conn, err := d.Dial("unix", socket)
	if err != nil {
		return nil, proto.Welcome{}, err
	}
	conn.SetDeadline(deadline)
	hello := proto.Hello{Gen: proto.Gen, Version: version, Caps: oneshotCaps, Kind: proto.KindOneshot, Pane: pane}
	if pane != "" {
		hello.Ancestors = ancestors()
	}
	if err := proto.WriteMsg(conn, proto.Encode(hello)); err != nil {
		conn.Close()
		return nil, proto.Welcome{}, err
	}
	for {
		msg, err := proto.ReadMsg(conn)
		if err != nil {
			conn.Close()
			return nil, proto.Welcome{}, err
		}
		switch msg.T {
		case proto.TypeWelcome:
			var w proto.Welcome
			if err := msg.Decode(&w); err != nil {
				conn.Close()
				return nil, proto.Welcome{}, err
			}
			return &oneshot{conn: conn}, w, nil
		case proto.TypeRefuse:
			var r proto.Refuse
			msg.Decode(&r)
			conn.Close()
			return nil, proto.Welcome{}, &errRefused{Code: r.Code, Text: r.Text}
		case proto.TypePing:
			if err := proto.WriteMsg(conn, proto.Encode(proto.Pong{})); err != nil {
				conn.Close()
				return nil, proto.Welcome{}, err
			}
		}
	}
}

// request sends req with the next id and reads until its reply; pings are
// answered, everything else is skipped.
func (o *oneshot) request(req proto.Typed) (proto.Reply, error) {
	o.next++
	id := o.next
	if err := proto.WriteMsg(o.conn, proto.Request(id, req)); err != nil {
		return proto.Reply{}, err
	}
	for {
		msg, err := proto.ReadMsg(o.conn)
		if err != nil {
			return proto.Reply{}, err
		}
		if msg.T == proto.TypePing {
			if err := proto.WriteMsg(o.conn, proto.Encode(proto.Pong{})); err != nil {
				return proto.Reply{}, err
			}
			continue
		}
		if r, ok := proto.AsReply(msg); ok && msg.ID == id {
			return r, nil
		}
	}
}

func (o *oneshot) Close() error { return o.conn.Close() }

// ancestors is this process's pid followed by its parent chain (4.2.1).
func ancestors() []int {
	pid := os.Getpid()
	return append([]int{pid}, platform.Ancestors(pid)...)
}

// wireMode is the ui.hub mode of a hub mode (4.2.10).
func wireMode(mode hub.Mode) string {
	switch mode {
	case hub.ModeOpen:
		return proto.HubOpen
	case hub.ModeNew:
		return proto.HubNew
	case hub.ModeAgents:
		return proto.HubAgents
	}
	return proto.HubAll
}

// sessionOfPane is the session of the snapshot that holds pane.
func sessionOfPane(snap proto.Snapshot, pane string) (proto.Session, bool) {
	for _, p := range snap.Panes {
		if p.ID != pane {
			continue
		}
		for _, s := range snap.Sessions {
			if s.ID == p.Session {
				return s, true
			}
		}
	}
	return proto.Session{}, false
}

// sameDir reports whether a and b name one directory: equal after
// cleaning, or after resolving symbolic links.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// sortedAgents returns the agents most urgent first: blocked, done,
// working, idle, then anything else; by pane id within a status.
func sortedAgents(agents []proto.Agent) []proto.Agent {
	out := append([]proto.Agent(nil), agents...)
	rank := func(s string) int {
		switch s {
		case proto.StatusBlocked:
			return 0
		case proto.StatusDone:
			return 1
		case proto.StatusWorking:
			return 2
		case proto.StatusIdle:
			return 3
		}
		return 4
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i].Status), rank(out[j].Status); ri != rj {
			return ri < rj
		}
		return out[i].Pane < out[j].Pane
	})
	return out
}
