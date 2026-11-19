package e2e

// The fake tmux and the fake herdr. Each is the test binary started under
// that name from a machine's bin directory (the fake herdr also through
// HERDR_BIN_PATH). Each appends its argument list to a log of the machine
// and answers the calls a backend makes, from a state file of the machine.
// Neither starts a process: a fake tmux asked to attach and a fake herdr
// asked to run a server or a client record the call and return at once.
//
// The calls answered are the ones internal/tmux/tmux.go and
// internal/herdr/{herdr,agents,server}.go make in the tree, and the ones
// 5.3.11 adds (PaneTTY, Alive and the halves of Switch for tmux; Remove
// for herdr) and 7.1.3 keeps.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/creack/pty"
)

const (
	fakeTmuxName  = "tmux"
	fakeHerdrName = "herdr"
	tmuxVersion   = "tmux 3.8" // E6: tmux 3.8 answers XTVERSION with "tmux 3.8"
	herdrVersion  = "herdr 0.9.5"
)

func init() {
	fakes[fakeTmuxName] = runFakeTmux
	fakes[fakeHerdrName] = runFakeHerdr
	attachHooks = append(attachHooks, tmuxAttachHook)
}

// TmuxLog returns every argument list the fake tmux of m received, in
// order, without the program name.
func (m *Machine) TmuxLog() [][]string {
	m.state().t.Helper()
	return m.fakeLog(fakeTmuxName)
}

// HerdrLog returns every argument list the fake herdr of m received, in
// order, without the program name.
func (m *Machine) HerdrLog() [][]string {
	m.state().t.Helper()
	return m.fakeLog(fakeHerdrName)
}

func (m *Machine) fakeLog(name string) [][]string {
	s := m.state()
	s.t.Helper()
	lists, err := readLog(m.fakeDir(name + ".log"))
	if err != nil {
		s.t.Fatalf("e2e: machine %s: the %s log: %v", m.Name, name, err)
	}
	return lists
}

// fakeSockets holds the sockets the fakes stand behind: listeners of the
// test process, which accept and close, so that a check that connects
// finds a live server. Keyed by path.
var fakeSockets sync.Map

// listenFake makes the socket at path live until the test ends.
func listenFake(t *testing.T, path string) {
	t.Helper()
	if _, ok := fakeSockets.Load(path); ok {
		return
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("e2e: listen on %s: %v", path, err)
	}
	fakeSockets.Store(path, l)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		fakeSockets.Delete(path)
	})
}

// deadSocket makes a socket file at path that nobody listens on: a
// connection to it is refused, as after its server died.
func deadSocket(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("e2e: socket %s: %v", path, err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
}

// --- the fake tmux ---

// tmuxState is the state of the fake tmux server of a machine.
type tmuxState struct {
	Socket   string            `json:"socket"` // the one live socket
	Pid      int               `json:"pid"`    // the server pid, the second field of $TMUX
	Alive    bool              `json:"alive"`
	Sessions []tmuxSession     `json:"sessions"`
	Panes    map[string]string `json:"panes"` // pane id -> terminal device
}

type tmuxSession struct {
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Attached bool   `json:"attached"`
}

// InstallTmux places the fake tmux in m's bin directory with a live
// server: its socket accepts connections, and its one pane %0 reports a
// terminal device that is not the scenario's until a Term is attached in
// the TmuxPane context. It returns the value of $TMUX for that server.
func (m *Machine) InstallTmux() string {
	s := m.state()
	s.t.Helper()
	dir := m.fakeDir(fakeTmuxName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatalf("e2e: fake tmux: %v", err)
	}
	m.link(fakeTmuxName, self())
	sock := filepath.Join(dir, "s")
	listenFake(s.t, sock)
	var tmuxVar string
	m.changeTmux(func(st *tmuxState) {
		if st.Socket == "" {
			st.Socket = sock
			st.Pid = os.Getpid()
			st.Alive = true
			st.Panes = map[string]string{"%0": otherTTY(s.t)}
		}
		tmuxVar = fmt.Sprintf("%s,%d,0", st.Socket, st.Pid)
	})
	return tmuxVar
}

// DeadTmuxVar returns a $TMUX that names a socket of m nobody listens on.
func (m *Machine) DeadTmuxVar() string {
	s := m.state()
	s.t.Helper()
	m.InstallTmux()
	sock := m.fakeDir(fakeTmuxName, "dead")
	deadSocket(s.t, sock)
	return fmt.Sprintf("%s,%d,0", sock, os.Getpid()+1)
}

// otherTTY returns the device of a PTY that the test holds open and that
// no scenario runs on: the device a tmux pane reports when it is not the
// caller's terminal.
var otherTTYs sync.Map // *testing.T -> string

func otherTTY(t *testing.T) string {
	if v, ok := otherTTYs.Load(t); ok {
		return v.(string)
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("e2e: open a PTY: %v", err)
	}
	name := slave.Name()
	otherTTYs.Store(t, name)
	t.Cleanup(func() {
		slave.Close()
		master.Close()
		otherTTYs.Delete(t)
	})
	return name
}

// changeTmux changes the fake tmux state of m under its lock.
func (m *Machine) changeTmux(change func(*tmuxState)) {
	s := m.state()
	s.t.Helper()
	if err := changeState(m.fakeDir(fakeTmuxName, "state.json"), change); err != nil {
		s.t.Fatalf("e2e: fake tmux: %v", err)
	}
}

// TmuxSessions returns the names of the sessions of the fake tmux.
func (m *Machine) TmuxSessions() []string {
	var out []string
	m.changeTmux(func(st *tmuxState) {
		for _, s := range st.Sessions {
			out = append(out, s.Name)
		}
	})
	return out
}

// AddTmuxSession adds a session to the fake tmux.
func (m *Machine) AddTmuxSession(name, dir string) {
	m.changeTmux(func(st *tmuxState) {
		if st.find(name) < 0 {
			st.Sessions = append(st.Sessions, tmuxSession{Name: name, Dir: dir})
		}
	})
}

// tmuxAttachHook makes pane %N of the fake tmux report the Term's device
// when the Term stands in the TmuxPane context of m: TMUX names m's live
// socket and TMUX_PANE is set. A popup has no TMUX_PANE (E6), so its pane
// keeps reporting another device.
func tmuxAttachHook(m *Machine, o TermOptions, tty string) {
	pane := valueOf(o.Env, "TMUX_PANE")
	sock, _, _ := strings.Cut(valueOf(o.Env, "TMUX"), ",")
	if pane == "" || sock == "" || sock != m.fakeDir(fakeTmuxName, "s") {
		return
	}
	m.changeTmux(func(st *tmuxState) { st.Panes[pane] = tty })
}

// changeState reads the JSON state at path, lets change edit it and
// writes it back, under a lock beside the file.
func changeState[S any](path string, change func(*S)) error {
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	var st S
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	change(&st)
	b, err := json.Marshal(&st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (st *tmuxState) find(name string) int {
	for i, s := range st.Sessions {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// tmuxFlagsWithArg are the options of tmux and of its commands that take
// an argument, as far as the backend uses them.
var tmuxGlobalWithArg = "LSfcT"

// tmuxCommand is one parsed command line: its flags and the words after
// them.
type tmuxCommand struct {
	name  string
	flags map[string]string // "-d" -> "", "-s" -> "x"
	words []string
}

// tmuxCommandArgs are the flags with an argument per command.
var tmuxCommandArgs = map[string]string{
	"new-session":     "sctnxyFeE",
	"attach-session":  "tcfE",
	"has-session":     "t",
	"switch-client":   "tcTOK",
	"kill-session":    "t",
	"display-message": "tcFd",
	"list-sessions":   "Ff",
	"list-windows":    "tFf",
	"list-panes":      "tFf",
	"capture-pane":    "tSEb",
	"detach-client":   "tsE",
}

var tmuxAliases = map[string]string{
	"new": "new-session", "attach": "attach-session", "a": "attach-session", "at": "attach-session",
	"has": "has-session", "switchc": "switch-client", "display": "display-message",
	"ls": "list-sessions", "lsw": "list-windows", "lsp": "list-panes", "capturep": "capture-pane",
	"detach": "detach-client",
}

func parseTmuxCommand(args []string) tmuxCommand {
	c := tmuxCommand{flags: map[string]string{}}
	if len(args) == 0 {
		return c
	}
	c.name = args[0]
	if full, ok := tmuxAliases[c.name]; ok {
		c.name = full
	}
	withArg := tmuxCommandArgs[c.name]
	i := 1
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || len(a) < 2 {
			break
		}
		for j := 1; j < len(a); j++ {
			f := string(a[j])
			if strings.Contains(withArg, f) {
				if j+1 < len(a) {
					c.flags["-"+f] = a[j+1:]
				} else if i+1 < len(args) {
					i++
					c.flags["-"+f] = args[i]
				}
				break
			}
			c.flags["-"+f] = ""
		}
	}
	c.words = args[i:]
	return c
}

// runFakeTmux is the fake tmux.
func runFakeTmux(args []string) int {
	root, err := selfRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake tmux: %v\n", err)
		return 1
	}
	if err := logArgs(filepath.Join(root, "f", fakeTmuxName+".log"), args); err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake tmux: the log: %v\n", err)
		return 1
	}
	// Global options before the command.
	socket := ""
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && len(args[i]) > 1 {
		a := args[i]
		if a == "-V" {
			fmt.Println(tmuxVersion)
			return 0
		}
		f := a[1:2]
		if strings.Contains(tmuxGlobalWithArg, f) {
			v := a[2:]
			if v == "" && i+1 < len(args) {
				i++
				v = args[i]
			}
			switch f {
			case "S":
				socket = v
			case "L":
				socket = "label:" + v
			}
		}
		i++
	}
	if socket == "" {
		if tv := os.Getenv("TMUX"); tv != "" {
			socket, _, _ = strings.Cut(tv, ",")
		}
	}
	c := parseTmuxCommand(args[i:])
	code := 0
	err = changeState(filepath.Join(root, "f", fakeTmuxName, "state.json"), func(st *tmuxState) {
		code = tmuxAnswer(st, socket, c)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake tmux: %v\n", err)
		return 1
	}
	return code
}

// tmuxAnswer answers one command against the state.
func tmuxAnswer(st *tmuxState, socket string, c tmuxCommand) int {
	if c.name == "start-server" {
		st.Alive = true
		return 0
	}
	if st.Socket == "" || !st.Alive || (socket != "" && socket != st.Socket) {
		where := socket
		if where == "" {
			where = st.Socket
		}
		fmt.Fprintf(os.Stderr, "no server running on %s\n", where)
		return 1
	}
	target := c.flags["-t"]
	session := strings.TrimSuffix(strings.SplitN(target, ":", 2)[0], ":")
	missing := func() int {
		fmt.Fprintf(os.Stderr, "can't find session: %s\n", session)
		return 1
	}
	switch c.name {
	case "has-session":
		if st.find(session) < 0 {
			return missing()
		}
		return 0
	case "list-sessions":
		format := c.flags["-F"]
		if format == "" {
			format = "#{session_name}: #{session_windows} windows"
		}
		for _, s := range st.Sessions {
			fmt.Println(tmuxFormat(st, format, s.Name, "%0"))
		}
		return 0
	case "new-session":
		name := c.flags["-s"]
		if name == "" {
			name = strconv.Itoa(len(st.Sessions))
		}
		_, detached := c.flags["-d"]
		_, reuse := c.flags["-A"]
		if st.find(name) >= 0 {
			if !reuse {
				fmt.Fprintf(os.Stderr, "duplicate session: %s\n", name)
				return 1
			}
		} else {
			st.Sessions = append(st.Sessions, tmuxSession{Name: name, Dir: c.flags["-c"]})
		}
		if !detached {
			// A client would attach here; the fake records the call (the
			// log) and returns, so no test holds a tmux client.
			st.Sessions[st.find(name)].Attached = true
		}
		return 0
	case "attach-session":
		idx := 0
		if session != "" {
			idx = st.find(session)
		}
		if idx < 0 || len(st.Sessions) == 0 {
			if len(st.Sessions) == 0 {
				fmt.Fprintln(os.Stderr, "no sessions")
				return 1
			}
			return missing()
		}
		st.Sessions[idx].Attached = true
		return 0
	case "switch-client":
		if st.find(session) < 0 {
			return missing()
		}
		return 0
	case "kill-session":
		idx := st.find(session)
		if idx < 0 {
			return missing()
		}
		st.Sessions = append(st.Sessions[:idx], st.Sessions[idx+1:]...)
		return 0
	case "detach-client":
		return 0
	case "kill-server":
		st.Alive = false
		return 0
	case "display-message":
		pane := "%0"
		if strings.HasPrefix(target, "%") {
			pane = target
			if _, ok := st.Panes[pane]; !ok {
				fmt.Fprintf(os.Stderr, "can't find pane: %s\n", pane)
				return 1
			}
		}
		name := ""
		if len(st.Sessions) > 0 {
			name = st.Sessions[0].Name
		}
		if session != "" && !strings.HasPrefix(target, "%") {
			if st.find(session) < 0 {
				return missing()
			}
			name = session
		}
		if _, print := c.flags["-p"]; print {
			fmt.Println(tmuxFormat(st, strings.Join(c.words, " "), name, pane))
		}
		return 0
	case "list-windows":
		if st.find(session) < 0 {
			return missing()
		}
		format := c.flags["-F"]
		if format == "" {
			format = "#{window_index}: #{window_name}"
		}
		fmt.Println(tmuxFormat(st, format, session, "%0"))
		return 0
	case "list-panes":
		format := c.flags["-F"]
		if format == "" {
			format = "#{pane_id}"
		}
		ids := make([]string, 0, len(st.Panes))
		for id := range st.Panes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Println(tmuxFormat(st, format, session, id))
		}
		return 0
	case "capture-pane":
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command: %s\n", c.name)
	return 1
}

// tmuxFormat expands the #{...} variables the backend asks for.
func tmuxFormat(st *tmuxState, format, session, pane string) string {
	windows, attached := "1", "0"
	if i := st.find(session); i >= 0 && st.Sessions[i].Attached {
		attached = "1"
	}
	vars := map[string]string{
		"session_name":     session,
		"session_windows":  windows,
		"session_attached": attached,
		"window_id":        "@1",
		"window_index":     "1",
		"window_name":      "e2e",
		"pane_id":          pane,
		"pane_tty":         st.Panes[pane],
		"client_tty":       st.Panes[pane],
		"pid":              strconv.Itoa(st.Pid),
		"socket_path":      st.Socket,
		"version":          strings.TrimPrefix(tmuxVersion, "tmux "),
	}
	var b strings.Builder
	for {
		i := strings.Index(format, "#{")
		if i < 0 {
			b.WriteString(format)
			return b.String()
		}
		j := strings.IndexByte(format[i:], '}')
		if j < 0 {
			b.WriteString(format)
			return b.String()
		}
		b.WriteString(format[:i])
		b.WriteString(vars[format[i+2:i+j]])
		format = format[i+j+1:]
	}
}

// --- the fake herdr ---

// herdrState is the state of the fake herdr server of a machine.
type herdrState struct {
	Socket     string           `json:"socket"`
	Running    bool             `json:"running"`
	Next       int              `json:"next"`
	Workspaces []herdrWorkspace `json:"workspaces"`
	Agents     []herdrAgent     `json:"agents"`
}

// herdrWorkspace has the fields of herdr's "workspace list" that
// internal/herdr reads (herdr.go:37-43), and its one pane.
type herdrWorkspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	Tabs        int    `json:"tab_count"`
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
	Cwd         string `json:"cwd"`
	Pane        string `json:"root_pane"`
}

// herdrAgent has the fields of herdr's "agent list" (agents.go:9-18).
type herdrAgent struct {
	Pane      string `json:"pane_id"`
	Workspace string `json:"workspace_id"`
	Name      string `json:"name"`
	Kind      string `json:"agent"`
	Status    string `json:"agent_status"`
	Title     string `json:"terminal_title_stripped"`
	Cwd       string `json:"cwd"`
	Focused   bool   `json:"focused"`
}

// InstallHerdr places the fake herdr in m's bin directory and names it by
// HERDR_BIN_PATH in every environment of m. Its server is not running
// until something runs `herdr server` or a test says so; its socket is
// live from now on.
func (m *Machine) InstallHerdr() {
	s := m.state()
	s.t.Helper()
	dir := m.fakeDir(fakeHerdrName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.t.Fatalf("e2e: fake herdr: %v", err)
	}
	if _, err := os.Lstat(m.Bin(fakeHerdrName)); err != nil {
		m.link(fakeHerdrName, self())
		m.AddEnv("HERDR_BIN_PATH=" + m.Bin(fakeHerdrName))
	}
	sock := filepath.Join(dir, "s")
	listenFake(s.t, sock)
	m.ChangeHerdr(func(st *herdrState) { st.Socket = sock })
}

// ChangeHerdr changes the state of the fake herdr of m under its lock.
func (m *Machine) ChangeHerdr(change func(*herdrState)) {
	s := m.state()
	s.t.Helper()
	if err := changeState(m.fakeDir(fakeHerdrName, "state.json"), change); err != nil {
		s.t.Fatalf("e2e: fake herdr: %v", err)
	}
}

// addWorkspace adds a workspace and returns it.
func (st *herdrState) addWorkspace(label, cwd string) herdrWorkspace {
	st.Next++
	w := herdrWorkspace{
		ID: fmt.Sprintf("w%d", st.Next), Label: label, Tabs: 1, Cwd: cwd,
		Pane: fmt.Sprintf("w%d-p1", st.Next), AgentStatus: "unknown",
	}
	for i := range st.Workspaces {
		st.Workspaces[i].Focused = false
	}
	w.Focused = true
	st.Workspaces = append(st.Workspaces, w)
	return w
}

func (st *herdrState) workspace(id string) int {
	for i, w := range st.Workspaces {
		if w.ID == id {
			return i
		}
	}
	return -1
}

// herdrOut prints v as herdr prints a result: {"result": v}.
func herdrOut(v any) {
	b, _ := json.Marshal(map[string]any{"result": v})
	fmt.Println(string(b))
}

// herdrErr fails as herdr does: a JSON error on stderr, status 1.
func herdrErr(format string, args ...any) int {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": fmt.Sprintf(format, args...)}})
	fmt.Fprintln(os.Stderr, string(b))
	return 1
}

// flagValue returns the value after name in args.
func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

// runFakeHerdr is the fake herdr.
func runFakeHerdr(args []string) int {
	root, err := selfRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake herdr: %v\n", err)
		return 1
	}
	if err := logArgs(filepath.Join(root, "f", fakeHerdrName+".log"), args); err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake herdr: the log: %v\n", err)
		return 1
	}
	code := 0
	err = changeState(filepath.Join(root, "f", fakeHerdrName, "state.json"), func(st *herdrState) {
		code = herdrAnswer(st, args)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake herdr: %v\n", err)
		return 1
	}
	return code
}

// herdrAnswer answers one call against the state.
func herdrAnswer(st *herdrState, args []string) int {
	// The client: no arguments, or only options. It would attach the
	// terminal; the fake records the call (the log) and returns.
	if len(args) == 0 || strings.HasPrefix(args[0], "-") && args[0] != "--version" && args[0] != "-V" {
		return 0
	}
	switch args[0] {
	case "--version", "-V":
		fmt.Println(herdrVersion)
		return 0
	case "server":
		// It would start the server; the fake marks it running.
		st.Running = true
		return 0
	case "status":
		if len(args) >= 2 && args[1] == "server" {
			b, _ := json.Marshal(map[string]bool{"running": st.Running})
			fmt.Println(string(b))
			return 0
		}
	}
	if !st.Running {
		return herdrErr("herdr server is not running")
	}
	sub := ""
	if len(args) > 1 {
		sub = args[1]
	}
	rest := args[min(2, len(args)):]
	switch args[0] + " " + sub {
	case "workspace list":
		ws := st.Workspaces
		if ws == nil {
			ws = []herdrWorkspace{}
		}
		herdrOut(map[string]any{"workspaces": ws})
		return 0
	case "workspace create":
		w := st.addWorkspace(flagValue(rest, "--label"), flagValue(rest, "--cwd"))
		herdrOut(map[string]any{
			"workspace": map[string]string{"workspace_id": w.ID},
			"root_pane": map[string]string{"pane_id": w.Pane},
		})
		return 0
	case "workspace focus":
		if len(rest) == 0 || st.workspace(rest[0]) < 0 {
			return herdrErr("no workspace %s", strings.Join(rest, " "))
		}
		for i := range st.Workspaces {
			st.Workspaces[i].Focused = st.Workspaces[i].ID == rest[0]
		}
		herdrOut(map[string]any{})
		return 0
	case "workspace close":
		if len(rest) == 0 || st.workspace(rest[0]) < 0 {
			return herdrErr("no workspace %s", strings.Join(rest, " "))
		}
		i := st.workspace(rest[0])
		st.Workspaces = append(st.Workspaces[:i], st.Workspaces[i+1:]...)
		herdrOut(map[string]any{})
		return 0
	case "tab list":
		w := st.workspace(flagValue(rest, "--workspace"))
		if w < 0 {
			return herdrErr("no workspace %s", flagValue(rest, "--workspace"))
		}
		herdrOut(map[string]any{"tabs": []map[string]any{{"tab_id": st.Workspaces[w].ID + "-t1", "number": 1, "label": "1"}}})
		return 0
	case "pane list":
		w := st.workspace(flagValue(rest, "--workspace"))
		if w < 0 {
			return herdrErr("no workspace %s", flagValue(rest, "--workspace"))
		}
		herdrOut(map[string]any{"panes": []map[string]string{{"pane_id": st.Workspaces[w].Pane, "tab_id": st.Workspaces[w].ID + "-t1"}}})
		return 0
	case "agent list":
		agents := st.Agents
		if agents == nil {
			agents = []herdrAgent{}
		}
		herdrOut(map[string]any{"agents": agents})
		return 0
	case "agent focus":
		for _, a := range st.Agents {
			if len(rest) > 0 && (a.Pane == rest[0] || a.Name == rest[0]) {
				herdrOut(map[string]any{})
				return 0
			}
		}
		return herdrErr("no agent %s", strings.Join(rest, " "))
	}
	return herdrErr("unknown command: %s", strings.Join(args, " "))
}
