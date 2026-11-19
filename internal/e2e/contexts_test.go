package e2e

import (
	"debug/buildinfo"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"devtools/internal/proto"
)

// Context is a place a command can be typed in (3.5.6, 10.2.1).
type Context int

const (
	Plain               Context = iota // a plain terminal
	DevtoolsPane                       // a pane of the machine's devtools server
	DevtoolsPaneOverSSH                // a devtools pane reached over ssh: no variable survives
	TmuxPane                           // a pane of the fake tmux
	TmuxPopup                          // a popup of the fake tmux
	HerdrPane                          // a pane of the fake herdr
	Screen                             // a window of GNU screen
	DeadTmux                           // a shell that outlived its tmux server
)

// Contexts are the eight contexts in the order of the table of P3-05a.
var Contexts = []Context{Plain, DevtoolsPane, DevtoolsPaneOverSSH, TmuxPane, TmuxPopup, HerdrPane, Screen, DeadTmux}

func (c Context) String() string {
	switch c {
	case Plain:
		return "Plain"
	case DevtoolsPane:
		return "DevtoolsPane"
	case DevtoolsPaneOverSSH:
		return "DevtoolsPaneOverSSH"
	case TmuxPane:
		return "TmuxPane"
	case TmuxPopup:
		return "TmuxPopup"
	case HerdrPane:
		return "HerdrPane"
	case Screen:
		return "Screen"
	case DeadTmux:
		return "DeadTmux"
	}
	return fmt.Sprintf("Context(%d)", int(c))
}

// The identities the contexts answer XTVERSION with across ssh.
const (
	// ghosttyIdentity is a plain terminal. provisional E6: what a real
	// ghostty window answers is the pending [GO] row 1 of E6; until then
	// the plan's form "ghostty <version>" with ghostty's release of the
	// time stands for it. The guard decides by the name before the space.
	ghosttyIdentity = "ghostty 1.2.3"
	// herdrIdentity is what a herdr pane is expected to answer (report 10,
	// Hd8). provisional E6: the herdr row of E6 is pending [GO]; E6 showed
	// that an embedder of libghostty-vt 1.0.1 without the XTVERSION
	// callback answers "libghostty", and that is the value taken here.
	herdrIdentity = "libghostty"
)

// In returns the options of a Term that stands in context c of machine m:
// Env carries the marks of the context and Identity is the answer that
// context gives to XTVERSION across ssh. It sets up what the context needs
// on m: a real pane of m's server for DevtoolsPane (the server must run),
// the fake tmux or the fake herdr for theirs.
func In(m *Machine, c Context) TermOptions {
	s := m.state()
	s.t.Helper()
	if s.real && c != Plain && c != DevtoolsPaneOverSSH {
		// The other contexts open a pane on the server or install a fake;
		// on the real machine that would change something of the user's.
		s.t.Fatalf("e2e: %s=1: the context %v is not available on the real machine; Plain and DevtoolsPaneOverSSH are", EnvReal, c)
	}
	o := TermOptions{Cols: 80, Rows: 24}
	switch c {
	case Plain:
		o.Env = plainEnv()
		o.Identity = ghosttyIdentity
	case DevtoolsPane:
		o.Env = m.openPane()
		o.Identity = devtoolsIdentity(m)
	case DevtoolsPaneOverSSH:
		o.Identity = devtoolsIdentity(m)
	case TmuxPane:
		o.Env = []string{"TMUX=" + m.InstallTmux(), "TMUX_PANE=%0"}
		o.Identity = tmuxVersion
	case TmuxPopup:
		// A popup has TMUX and no TMUX_PANE (E6, report 10 T2), and the
		// pane it was opened from has another terminal device than the
		// popup.
		o.Env = []string{"TMUX=" + m.InstallTmux()}
		o.Identity = tmuxVersion
	case HerdrPane:
		m.InstallHerdr()
		var pane, sock string
		m.ChangeHerdr(func(st *herdrState) {
			st.Running = true
			if len(st.Workspaces) == 0 {
				st.addWorkspace("e2e", m.Home)
			}
			pane, sock = st.Workspaces[0].Pane, st.Socket
		})
		o.Env = []string{"HERDR_ENV=1", "HERDR_SOCKET_PATH=" + sock, "HERDR_PANE_ID=" + pane}
		o.Identity = herdrIdentity
	case Screen:
		o.Env = []string{fmt.Sprintf("STY=%d.e2e", os.Getpid())}
		o.Identity = ghosttyIdentity
	case DeadTmux:
		o.Env = []string{"TMUX=" + m.DeadTmuxVar()}
		o.Identity = ghosttyIdentity
	default:
		s.t.Fatalf("e2e: In: unknown context %v", c)
	}
	return o
}

// devtoolsIdentity is the XTVERSION answer of a pane of m:
// "devtools <version> <host>". With a running server it is the version and
// host of its welcome; without one, the version written into the binary
// under test and this host's name.
func devtoolsIdentity(m *Machine) string {
	m.state().t.Helper()
	if m.ServerPid() != 0 || m.IsReal() {
		if w, _, err := oneshot(m.Socket(), nil, 2*time.Second); err == nil {
			return "devtools " + identWord(w.Version, "unknown") + " " + identWord(w.Host, "unknown")
		}
	}
	version := "unknown"
	if bi, err := buildinfo.ReadFile(binary); err == nil {
		version = bi.Main.Version
	}
	host, _ := os.Hostname()
	return "devtools " + identWord(version, "unknown") + " " + identWord(host, "unknown")
}

// identWord keeps the characters an identity word may have (letters,
// digits, ".", "_", "-", "+"), as the engine's identity does.
func identWord(s, empty string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-+", r) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return empty
	}
	return b.String()
}

// openPane opens a session on m's server, has it displayed so that its
// pane runs, and returns the variables of that pane: DEVTOOLS_PANE,
// DEVTOOLS_SOCKET and DEVTOOLS_TTY, as the pane shell of the machine wrote
// them from inside the pane.
//
// The session is opened with the one-shot request session.open (4.2.7),
// the request every command that opens a session sends; no command of the
// command line opens one without git work or a client. session.open leaves
// the new pane dormant: nothing is spawned until a view is set on the
// session (3.2.3, model.OpenSession and model.Display). The helper
// therefore holds a private interactive view of the session (view.set)
// until the pane has written its variables, and then closes it. The pane
// keeps running after the view ends, and no connection of kind interactive
// is left behind, so a scenario in this context starts with no client
// (Clients() is 0), as it does in a real pane.
func (m *Machine) openPane() []string {
	s := m.state()
	s.t.Helper()
	dir := filepath.Join(m.Home, "e2e-pane")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatalf("e2e: %v", err)
	}
	_, reply := m.Oneshot(proto.SessionOpen{Dir: dir, Name: "e2e-pane"})
	if !reply.OK {
		s.t.Fatalf("e2e: session.open: %s %s", reply.Code, reply.Text)
	}
	var opened proto.SessionOpenResult
	if err := reply.Msg.Decode(&opened); err != nil {
		s.t.Fatalf("e2e: session.open: %v", err)
	}
	view, err := displaySession(m.Socket(), opened.Session, 80, 24, 10*time.Second)
	if err != nil {
		s.t.Fatalf("e2e: display session %s: %v", opened.Session, err)
	}
	defer view.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, p := range view.Panes {
			if b, err := os.ReadFile(paneFile(s.root, p)); err == nil {
				return strings.Fields(string(b))
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("e2e: no pane of session %s wrote its variables within 10 s; server log:\n%s", opened.Session, m.ServerLog())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sessionView is a private interactive connection that displays one
// session. Panes are the panes of the session's current tab, from view.ok.
type sessionView struct {
	Panes []string
	nc    net.Conn
	done  chan struct{}
}

// Close ends the view: the connection is closed and the reader that drains
// its frames ends with it.
func (v *sessionView) Close() {
	v.nc.Close()
	<-v.done
}

// displaySession connects to the server on socket as a local interactive
// client of cols x rows, sets the view on session and returns once view.ok
// came. Until Close, a goroutine reads and drops what the server sends (the
// snapshot, deltas and frames), so the frame lane of the connection never
// stalls.
func displaySession(socket, session string, cols, rows int, limit time.Duration) (*sessionView, error) {
	nc, err := net.DialTimeout("unix", socket, limit)
	if err != nil {
		return nil, err
	}
	nc.SetDeadline(time.Now().Add(limit))
	hello := proto.Hello{Gen: proto.Gen, Version: "e2e", Kind: proto.KindInteractive}
	if err := proto.WriteMsg(nc, proto.Encode(hello)); err != nil {
		nc.Close()
		return nil, err
	}
	first, err := proto.ReadMsg(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if first.T != proto.TypeWelcome {
		nc.Close()
		return nil, fmt.Errorf("the server answered the hello with %s", first.Body)
	}
	if err := proto.WriteMsg(nc, proto.Request(1, proto.ViewSet{Session: session, Cols: cols, Rows: rows})); err != nil {
		nc.Close()
		return nil, err
	}
	for {
		msg, err := proto.ReadMsg(nc)
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("view.set: %w", err)
		}
		if msg.ID != 1 {
			continue
		}
		r, ok := proto.AsReply(msg)
		if !ok {
			continue
		}
		if !r.OK {
			nc.Close()
			return nil, fmt.Errorf("view.set: %s %s", r.Code, r.Text)
		}
		var vok proto.ViewOK
		if err := msg.Decode(&vok); err != nil {
			nc.Close()
			return nil, fmt.Errorf("view.ok: %w", err)
		}
		nc.SetDeadline(time.Time{})
		v := &sessionView{Panes: vok.Panes, nc: nc, done: make(chan struct{})}
		go func() {
			defer close(v.done)
			io.Copy(io.Discard, nc)
		}()
		return v, nil
	}
}
