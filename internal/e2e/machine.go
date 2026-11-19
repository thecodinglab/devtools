package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/testkit"
)

// Machine is one devtools server with its own HOME and XDG_STATE_HOME.
//
// Its directory, a short temporary directory, holds h (the HOME), b (the
// bin directory that comes first on every PATH of the machine: devtools,
// the pane shell and whichever fakes a test placed there) and f (the files
// of the fakes: their state and their logs). StateDir is the devtools state
// directory, where the server's socket and state.json are.
type Machine struct{ Name, Home, StateDir string }

// machines holds what a Machine needs beyond its three public fields; it
// is keyed by the Machine so that the struct stays the one of 10.1.8.
var machines sync.Map // *Machine -> *machineState

type machineState struct {
	t    *testing.T
	root string
	real bool // the real local server of the real mode (startReal)

	mu     sync.Mutex
	server *proc
	terms  []int    // pids of the programs started in a Term of this machine
	env    []string // AddEnv
}

func (m *Machine) state() *machineState {
	s, ok := machines.Load(m)
	if !ok {
		panic("e2e: a Machine that StartMachine did not make")
	}
	return s.(*machineState)
}

// proc is one process the harness started and waits for.
type proc struct {
	p     *os.Process
	done  chan struct{}
	state *os.ProcessState
}

func startProc(argv0 string, argv, env []string, files []*os.File, dir string, sys *syscall.SysProcAttr) (*proc, error) {
	p, err := os.StartProcess(argv0, argv, &os.ProcAttr{Dir: dir, Env: env, Files: files, Sys: sys})
	if err != nil {
		return nil, err
	}
	pr := &proc{p: p, done: make(chan struct{})}
	go func() {
		st, _ := p.Wait()
		pr.state = st
		close(pr.done)
	}()
	return pr, nil
}

// wait waits up to d for the process to end and reports whether it did.
func (p *proc) wait(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// killGroup sends sig to the process group of a process the harness
// started as the leader of a new session.
func (p *proc) killGroup(sig syscall.Signal) {
	syscall.Kill(-p.p.Pid, sig)
}

// StartMachine builds nothing; it runs the binary named by DEVTOOLS_E2E_BIN
// (default: built once per test run into a temp dir) as `devtools server`,
// and returns when the server's socket accepts a connection. The server is
// stopped when the test ends.
//
// In the real mode (Real) it starts nothing: it returns the machine of the
// real local server (startReal), and the other machine is reached under
// DEVTOOLS_E2E_REMOTE with Connect(t, a, nil, remote, connect).
func StartMachine(t *testing.T, name string) *Machine {
	t.Helper()
	if realOn {
		return startReal(t, name)
	}
	m := newMachine(t, name)
	m.Start()
	return m
}

// newMachine makes the directories of a machine and its bin directory,
// without a server.
func newMachine(t *testing.T, name string) *Machine {
	t.Helper()
	if binary == "" {
		t.Fatal("e2e: no binary under test; TestMain did not run")
	}
	if realOn {
		t.Fatalf("e2e: %s=1: an isolated machine is not made in the real mode; only %v run in it", EnvReal, RealTests)
	}
	root := testkit.ShortTempDir(t)
	home := filepath.Join(root, "h")
	m := &Machine{Name: name, Home: home, StateDir: filepath.Join(home, ".local", "state", "devtools")}
	for _, dir := range []string{
		filepath.Join(home, ".config", "devtools"), m.StateDir,
		filepath.Join(root, "b"), filepath.Join(root, "f", "panes"), filepath.Join(root, "f", "ssh"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("e2e: machine %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, markerFile), []byte(name+"\n"), 0o600); err != nil {
		t.Fatalf("e2e: machine %s: %v", name, err)
	}
	s := &machineState{t: t, root: root}
	machines.Store(m, s)
	t.Cleanup(func() {
		m.shutdown()
		machines.Delete(m)
	})
	m.link(devtools, binary)
	m.link(paneShell, self())
	return m
}

// startReal is StartMachine in the real mode: the machine of the real local
// server, the installed or the rehearsal one, which must already run. HOME
// and the state directory are those of the environment the user started
// the run with (realEnv); the machine's directory is a short temporary
// directory that holds only the harness's own files and the link to the
// binary under test. The harness never starts, stops, kills or configures
// this server, places no fake on its PATH and writes nothing under HOME.
// Only the tests of RealTests get it, and each gets one: the other machine
// is the remote one, reached through the real ssh.
func startReal(t *testing.T, name string) *Machine {
	t.Helper()
	top, _, _ := strings.Cut(t.Name(), "/")
	if !isRealTest(top) {
		t.Fatalf("e2e: %s=1: %s does not run in the real mode; only %v do", EnvReal, top, RealTests)
	}
	var count int
	machines.Range(func(_, v any) bool {
		if s := v.(*machineState); s.real && s.t == t {
			count++
		}
		return true
	})
	if count > 0 {
		t.Fatalf("e2e: %s=1: one local machine per test; reach %s with Connect(t, a, nil, %q, connect)", EnvReal, realMode.Remote, realMode.Remote)
	}
	home, _ := testkit.LookupEnv(realEnv, "HOME")
	if !filepath.IsAbs(home) {
		t.Fatalf("e2e: %s=1: HOME %q is not an absolute path", EnvReal, home)
	}
	stateDir := filepath.Join(home, ".local", "state", "devtools")
	if x, _ := testkit.LookupEnv(realEnv, "XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		stateDir = filepath.Join(x, "devtools")
	}
	root := testkit.ShortTempDir(t)
	for _, dir := range []string{filepath.Join(root, "b"), filepath.Join(root, "f")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("e2e: machine %s: %v", name, err)
		}
	}
	m := &Machine{Name: name, Home: home, StateDir: stateDir}
	s := &machineState{t: t, root: root, real: true}
	machines.Store(m, s)
	t.Cleanup(func() { machines.Delete(m) })
	m.link(devtools, binary)
	c, err := net.DialTimeout("unix", m.Socket(), 5*time.Second)
	if err != nil {
		t.Fatalf("e2e: %s=1: the local server does not answer on %s (start it first: rc-server start in the rehearsal, 9.2): %v", EnvReal, m.Socket(), err)
	}
	c.Close()
	return m
}

// IsReal reports whether m is the real local server of the real mode.
func (m *Machine) IsReal() bool { return m.state().real }

// refuseReal fails the test when m is the real machine, where doing would
// start, stop or change something of the user's.
func (m *Machine) refuseReal(doing string) {
	s := m.state()
	s.t.Helper()
	if s.real {
		s.t.Fatalf("e2e: %s=1: the harness does not %s on the real machine", EnvReal, doing)
	}
}

// self is the test binary.
func self() string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return exe
}

// link places target in the machine's bin directory under name.
func (m *Machine) link(name, target string) {
	s := m.state()
	s.t.Helper()
	p := m.Bin(name)
	if _, err := os.Lstat(p); err == nil {
		return
	}
	if err := os.Symlink(target, p); err != nil {
		s.t.Fatalf("e2e: machine %s: link %s: %v", m.Name, name, err)
	}
}

// Bin returns the path of name in the machine's bin directory.
func (m *Machine) Bin(name string) string { return filepath.Join(m.state().root, "b", name) }

// fakeDir returns the path of name among the files of the fakes.
func (m *Machine) fakeDir(name ...string) string {
	return filepath.Join(append([]string{m.state().root, "f"}, name...)...)
}

// Root returns the machine's directory.
func (m *Machine) Root() string { return m.state().root }

// Socket returns the path of the server's socket.
func (m *Machine) Socket() string { return filepath.Join(m.StateDir, proto.SocketName) }

// ConfigPath returns the path of the machine's config file
// (~/.config/devtools/config).
func (m *Machine) ConfigPath() string { return filepath.Join(m.Home, ".config", "devtools", "config") }

// Config appends lines to the machine's config file.
func (m *Machine) Config(lines ...string) {
	s := m.state()
	s.t.Helper()
	m.refuseReal("write the config")
	if err := appendLine(m.ConfigPath(), []byte(strings.Join(lines, "\n"))); err != nil {
		s.t.Fatalf("e2e: machine %s: config: %v", m.Name, err)
	}
}

// LinkTool places a program found on the test's own PATH into the
// machine's bin directory, for a scenario that needs it (git). ssh and the
// multiplexers are never linked this way: the fakes stand in for them, and
// a name that is already in the bin directory is left as it is.
func (m *Machine) LinkTool(name string) {
	s := m.state()
	s.t.Helper()
	m.refuseReal("link a tool (the real mode uses the PATH it was started with)")
	if name == fakeSSH {
		s.t.Fatalf("e2e: LinkTool(%q): the harness never links the real ssh; use Connect", name)
	}
	p, err := findSelf(name)
	if err != nil {
		s.t.Fatalf("e2e: LinkTool(%q): %v", name, err)
	}
	m.link(name, p)
}

// AddEnv adds variables to every environment of this machine from now on
// (Env), for a fake that a test placed in the bin directory and that is
// named by a variable.
func (m *Machine) AddEnv(kv ...string) {
	s := m.state()
	s.mu.Lock()
	s.env = append(s.env, kv...)
	s.mu.Unlock()
}

// Env returns the complete environment of a process of this machine:
// HOME, XDG_STATE_HOME, LANG, SHELL (the pane shell), a PATH whose first
// entry is the machine's bin directory, the variables of AddEnv, then
// extra. No variable of the test process is inherited. On the real machine
// the base is the environment the user started the run with, without the
// marks of a context (realEnv).
func (m *Machine) Env(extra ...string) []string {
	s := m.state()
	s.mu.Lock()
	added := append([]string{}, s.env...)
	s.mu.Unlock()
	if s.real {
		return mergeEnv(realEnv, append(added, extra...)...)
	}
	return mergeEnv(machineEnv(s.root), append(added, extra...)...)
}

// machineEnv is the base environment of the machine in root; the fake ssh
// builds the environment of the bridge with it, in another process.
func machineEnv(root string) []string {
	return testkit.CleanEnv(filepath.Join(root, "h"),
		"PATH="+filepath.Join(root, "b")+":/usr/bin:/bin",
		"SHELL="+filepath.Join(root, "b", paneShell),
		// The fakes are the test binary, which under -race sleeps a second
		// at its exit (the race runtime's atexit_sleep_ms); a fake answers
		// at once, as the program it stands for does.
		"GORACE=atexit_sleep_ms=0",
	)
}

// Start starts the server, for StartMachine and for a test that starts it
// again after Stop or Kill. It returns when the socket accepts a
// connection and fails the test when the server ends first or after 15 s.
func (m *Machine) Start() {
	s := m.state()
	s.t.Helper()
	m.refuseReal("start a server")
	s.mu.Lock()
	running := s.server != nil && !s.server.exited()
	s.mu.Unlock()
	if running {
		s.t.Fatalf("e2e: machine %s: the server runs already", m.Name)
	}
	logf, err := os.OpenFile(m.fakeDir("server.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		s.t.Fatalf("e2e: machine %s: %v", m.Name, err)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		logf.Close()
		s.t.Fatalf("e2e: machine %s: %v", m.Name, err)
	}
	p, err := startProc(m.Bin(devtools), []string{m.Bin(devtools), "server"}, m.Env(),
		[]*os.File{null, logf, logf}, m.Home, &syscall.SysProcAttr{Setsid: true})
	null.Close()
	logf.Close()
	if err != nil {
		s.t.Fatalf("e2e: machine %s: start the server: %v", m.Name, err)
	}
	s.mu.Lock()
	s.server = p
	s.mu.Unlock()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if c, err := net.DialTimeout("unix", m.Socket(), time.Second); err == nil {
			c.Close()
			return
		}
		if p.exited() {
			s.t.Fatalf("e2e: machine %s: the server ended before its socket answered (%v); log:\n%s", m.Name, p.state, m.ServerLog())
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("e2e: machine %s: the server's socket did not answer within 15 s; log:\n%s", m.Name, m.ServerLog())
		}
		time.Sleep(testkit.PollInterval)
	}
}

// ServerLog returns what the server wrote to its standard output and
// error.
func (m *Machine) ServerLog() string {
	b, _ := os.ReadFile(m.fakeDir("server.log"))
	return string(b)
}

// ServerPid returns the pid of the running server, 0 when none runs.
func (m *Machine) ServerPid() int {
	s := m.state()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server == nil || s.server.exited() {
		return 0
	}
	return s.server.p.Pid
}

// Stop sends SIGTERM to the server and waits for it to end; after 15 s it
// is killed and the test fails.
func (m *Machine) Stop() {
	s := m.state()
	s.t.Helper()
	m.refuseReal("stop the server")
	p := m.takeServer()
	if p == nil {
		return
	}
	p.p.Signal(syscall.SIGTERM)
	if !p.wait(15 * time.Second) {
		p.killGroup(syscall.SIGKILL)
		p.wait(5 * time.Second)
		s.t.Errorf("e2e: machine %s: the server did not end within 15 s of SIGTERM; killed", m.Name)
	}
}

// Kill sends SIGKILL to the server, for crash tests, and waits for it.
func (m *Machine) Kill() {
	s := m.state()
	s.t.Helper()
	m.refuseReal("kill the server")
	p := m.takeServer()
	if p == nil {
		return
	}
	p.p.Signal(syscall.SIGKILL)
	if !p.wait(10 * time.Second) {
		s.t.Errorf("e2e: machine %s: the server did not end after SIGKILL", m.Name)
	}
}

func (m *Machine) takeServer() *proc {
	s := m.state()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.server
	s.server = nil
	return p
}

// shutdown runs when the test ends: the server is stopped and whatever is
// left in its process group is killed.
func (m *Machine) shutdown() {
	p := m.takeServer()
	if p == nil {
		return
	}
	p.p.Signal(syscall.SIGTERM)
	if !p.wait(10 * time.Second) {
		p.killGroup(syscall.SIGKILL)
		p.wait(5 * time.Second)
	}
}

// RunOptions configure one one-shot command.
type RunOptions struct {
	Env     []string      // added to Env(); the marks of a context, In(m, c).Env
	Stdin   string        // the command's standard input; empty: /dev/null
	Dir     string        // working directory; empty: HOME
	Timeout time.Duration // the test fails when the command runs longer; 0: 30 s
}

// Result is how a one-shot command ended.
type Result struct {
	Stdout, Stderr string
	Exit           int // -1 when a signal ended it
}

// Run runs one one-shot command as `devtools <args>` without a terminal, in
// a plain context (the devtools backend is named while namesDevtools holds,
// as In(m, Plain) does), and returns its standard output and exit status.
func (m *Machine) Run(args ...string) (stdout string, exit int) {
	m.state().t.Helper()
	r := m.RunWith(RunOptions{Env: plainEnv()}, args...)
	return r.Stdout, r.Exit
}

// RunWith runs one one-shot command as `devtools <args>` without a
// terminal: a new session with no controlling terminal, so nothing can
// reach the terminal of the test. The environment is Env() with o.Env
// added; nothing else is added, DEVTOOLS_MULTIPLEXER included.
func (m *Machine) RunWith(o RunOptions, args ...string) Result {
	m.state().t.Helper()
	return m.RunBin(devtools, o, args...)
}

// RunBin runs the program name of the machine's bin directory (devtools, or
// a fake a test placed there) with args, as RunWith runs devtools.
func (m *Machine) RunBin(name string, o RunOptions, args ...string) Result {
	s := m.state()
	s.t.Helper()
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	dir := o.Dir
	if dir == "" {
		dir = m.Home
	}
	var stdin *os.File
	var feed *os.File
	if o.Stdin == "" {
		null, err := os.Open(os.DevNull)
		if err != nil {
			s.t.Fatalf("e2e: run: %v", err)
		}
		stdin = null
	} else {
		r, w, err := os.Pipe()
		if err != nil {
			s.t.Fatalf("e2e: run: %v", err)
		}
		stdin, feed = r, w
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		s.t.Fatalf("e2e: run: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		s.t.Fatalf("e2e: run: %v", err)
	}
	argv := append([]string{m.Bin(name)}, args...)
	p, err := startProc(m.Bin(name), argv, m.Env(o.Env...), []*os.File{stdin, outW, errW}, dir, &syscall.SysProcAttr{Setsid: true})
	stdin.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		if feed != nil {
			feed.Close()
		}
		s.t.Fatalf("e2e: run devtools %s: %v", strings.Join(args, " "), err)
	}
	if feed != nil {
		go func() {
			io.WriteString(feed, o.Stdin)
			feed.Close()
		}()
	}
	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(&stdout, outR); outR.Close() }()
	go func() { defer wg.Done(); io.Copy(&stderr, errR); errR.Close() }()
	if !p.wait(timeout) {
		p.killGroup(syscall.SIGKILL)
		p.wait(5 * time.Second)
		wg.Wait()
		s.t.Fatalf("e2e: devtools %s ran longer than %v; stdout:\n%s\nstderr:\n%s", strings.Join(args, " "), timeout, stdout.String(), stderr.String())
	}
	wg.Wait()
	code, _ := testkit.ExitStatus(p.state)
	return Result{Stdout: stdout.String(), Stderr: stderr.String(), Exit: code}
}

// Status is the object of `devtools server status --json` (4.5).
type Status struct {
	Pid      int    `json:"pid"`
	Version  string `json:"version"`
	Gen      int    `json:"gen"`
	Boot     string `json:"boot"`
	Sessions int    `json:"sessions"`
	Panes    int    `json:"panes"`
	Clients  int    `json:"clients"`
	Socket   string `json:"socket"`
}

// ServerStatus runs `devtools server status --json` and returns its
// object; ok is false when the command did not exit 0 or printed no
// object.
func (m *Machine) ServerStatus() (st Status, ok bool) {
	m.state().t.Helper()
	r := m.RunWith(RunOptions{}, "server", "status", "--json")
	if r.Exit != 0 {
		return Status{}, false
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &st); err != nil {
		return Status{}, false
	}
	return st, true
}

// Oneshot opens a one-shot connection to the machine's server, sends req
// with id 1 and returns the server's welcome and the reply. The test fails
// when the server refuses, closes or does not answer within 10 s.
func (m *Machine) Oneshot(req proto.Typed) (proto.Welcome, proto.Reply) {
	s := m.state()
	s.t.Helper()
	w, reply, err := oneshot(m.Socket(), req, 10*time.Second)
	if err != nil {
		s.t.Fatalf("e2e: machine %s: %s: %v", m.Name, req.Type(), err)
	}
	return w, reply
}

// Welcome returns the welcome of the machine's server (version, host,
// boot), from a one-shot connection that asks nothing.
func (m *Machine) Welcome() proto.Welcome {
	s := m.state()
	s.t.Helper()
	w, _, err := oneshot(m.Socket(), nil, 10*time.Second)
	if err != nil {
		s.t.Fatalf("e2e: machine %s: hello: %v", m.Name, err)
	}
	return w
}

// oneshot is Oneshot without a test: hello, welcome, then req (when not
// nil) and its reply.
func oneshot(socket string, req proto.Typed, limit time.Duration) (proto.Welcome, proto.Reply, error) {
	c, err := net.DialTimeout("unix", socket, limit)
	if err != nil {
		return proto.Welcome{}, proto.Reply{}, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(limit))
	hello := proto.Hello{Gen: proto.Gen, Version: "e2e", Kind: proto.KindOneshot}
	if err := proto.WriteMsg(c, proto.Encode(hello)); err != nil {
		return proto.Welcome{}, proto.Reply{}, err
	}
	first, err := proto.ReadMsg(c)
	if err != nil {
		return proto.Welcome{}, proto.Reply{}, err
	}
	if first.T != proto.TypeWelcome {
		return proto.Welcome{}, proto.Reply{}, fmt.Errorf("the server answered the hello with %s", first.Body)
	}
	var w proto.Welcome
	if err := first.Decode(&w); err != nil {
		return proto.Welcome{}, proto.Reply{}, err
	}
	if req == nil {
		return w, proto.Reply{}, nil
	}
	if err := proto.WriteMsg(c, proto.Request(1, req)); err != nil {
		return w, proto.Reply{}, err
	}
	for {
		msg, err := proto.ReadMsg(c)
		if err != nil {
			return w, proto.Reply{}, err
		}
		if msg.ID != 1 {
			continue
		}
		if r, ok := proto.AsReply(msg); ok {
			return w, r, nil
		}
	}
}

// addTerm records the pid of a process started in a Term of this machine.
func (m *Machine) addTerm(pid int) {
	s := m.state()
	s.mu.Lock()
	s.terms = append(s.terms, pid)
	s.mu.Unlock()
}

// Clients returns the number of client processes of the binary on this
// machine: the processes of the binary whose command line ends in "attach"
// that run in a Term of this machine, in one of its panes or under its bin
// directory, or the number of interactive connections that
// `server status --json` reports, whichever is larger (a client that
// connected is counted once). It is 0 exactly when no client runs.
func (m *Machine) Clients() int {
	s := m.state()
	s.t.Helper()
	procs, err := platform.Procs()
	if err != nil {
		s.t.Fatalf("e2e: the process table: %v", err)
	}
	s.mu.Lock()
	roots := map[int]bool{}
	for _, pid := range s.terms {
		roots[pid] = true
	}
	if s.server != nil && !s.server.exited() {
		roots[s.server.p.Pid] = true
	}
	s.mu.Unlock()
	parent := make(map[int]int, len(procs))
	for _, p := range procs {
		parent[p.Pid] = p.PPid
	}
	under := func(pid int) bool {
		for i := 0; i < 64 && pid > 1; i++ {
			if roots[pid] {
				return true
			}
			pid = parent[pid]
		}
		return false
	}
	n := 0
	for _, p := range procs {
		if len(p.Args) < 2 || filepath.Base(p.Args[0]) != devtools || p.Args[len(p.Args)-1] != "attach" {
			continue
		}
		if p.Args[0] == m.Bin(devtools) || under(p.Pid) {
			n++
		}
	}
	if m.ServerPid() != 0 {
		if st, ok := m.ServerStatus(); ok {
			n = max(n, st.Clients)
		}
	}
	return n
}

// paneFile is where the pane shell of a machine writes the variables of
// its pane: f/panes/<pane id>.
func paneFile(root, pane string) string { return filepath.Join(root, "f", "panes", pane) }

// runPaneShell is the program of every pane of a machine (its SHELL): it
// writes DEVTOOLS_PANE, DEVTOOLS_SOCKET and DEVTOOLS_TTY of its pane to the
// machine's pane file, so a test can stand in that pane, then becomes
// /bin/sh. It is not started as a login shell, so nothing of the host's
// profile (and no PATH of the developer) reaches the pane.
func runPaneShell(args []string) int {
	home := os.Getenv("HOME")
	root := filepath.Dir(home)
	pane := os.Getenv(proto.EnvPane)
	if _, err := os.Stat(filepath.Join(root, markerFile)); err == nil && pane != "" && !strings.ContainsAny(pane, "/\x00") && pane != "." && pane != ".." {
		body := fmt.Sprintf("%s=%s\n%s=%s\n%s=%s\n",
			proto.EnvPane, pane, proto.EnvSocket, os.Getenv(proto.EnvSocket), proto.EnvTTY, os.Getenv(proto.EnvTTY))
		dst := paneFile(root, pane)
		tmp := dst + ".tmp"
		if os.WriteFile(tmp, []byte(body), 0o600) == nil {
			os.Rename(tmp, dst)
		}
	}
	err := syscall.Exec("/bin/sh", append([]string{"sh"}, args...), os.Environ())
	fmt.Fprintf(os.Stderr, "e2e-shell: /bin/sh: %v\n", err)
	return 127
}
