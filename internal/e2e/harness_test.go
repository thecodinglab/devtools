package e2e

import (
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/testkit"
)

// hexLine is a reply as the helper query prints it.
func hexLine(s string) string {
	h := hex.EncodeToString([]byte(s))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

// startHelper runs a helper program of testkit in a Term of m.
func startHelper(t *testing.T, m *Machine, o TermOptions, name string, args ...string) *Term {
	t.Helper()
	argv, env := testkit.HelperCommand(name, args...)
	return StartIn(t, m, o, argv, env)
}

func TestHarnessFakeTerminalAnswersProbes(t *testing.T) {
	m := newMachine(t, "local")
	da1 := "\x1b[?62;22c" // the engine's DA1 (E6)
	for _, tc := range []struct {
		name     string
		o        TermOptions
		xtv, da1 bool
	}{
		{"a plain terminal answers both", TermOptions{Identity: ghosttyIdentity}, true, true},
		{"a tmux identity is answered as given", TermOptions{Identity: tmuxVersion}, true, true},
		{"an empty identity answers DA1 only", TermOptions{}, false, true},
		{"a silent terminal answers nothing", TermOptions{Identity: ghosttyIdentity, Silent: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The helper prints its replies, then "end", then waits for
			// the end of its input.
			x := startHelper(t, m, tc.o, "query")
			x.WaitFor("end", 10*time.Second)
			out := strings.ReplaceAll(x.Output(), "\r", "")
			lines := strings.Split(out, "\n")
			has := func(line string) bool {
				for _, l := range lines {
					if strings.TrimSpace(l) == line {
						return true
					}
				}
				return false
			}
			if !has("end") {
				t.Fatalf("the helper did not finish; output:\n%s", out)
			}
			wantXTV := hexLine("\x1bP>|" + tc.o.Identity + "\x1b\\")
			if got := has(wantXTV); got != tc.xtv {
				t.Errorf("XTVERSION %q answered: %v, want %v; output:\n%s", tc.o.Identity, got, tc.xtv, out)
			}
			if !tc.xtv && strings.Contains(out, hexLine("\x1bP>|")) {
				t.Errorf("an XTVERSION answer arrived; output:\n%s", out)
			}
			if got := has(hexLine(da1)); got != tc.da1 {
				t.Errorf("DA1 answered: %v, want %v; output:\n%s", got, tc.da1, out)
			}
			if tc.o.Silent && strings.TrimSpace(out) != "end" {
				t.Errorf("a silent terminal answered; output:\n%s", out)
			}
		})
	}
}

func TestHarnessQueryFilterHoldsACutQuery(t *testing.T) {
	stream := "ab\x1b[>qcd\x1b[>0q\x1b[ce\x1b[>1q"
	want := []piece{{data: []byte("ab")}, {query: true}, {data: []byte("cd")}, {query: true}, {data: []byte("\x1b[ce\x1b[>1q")}}
	for cut := 0; cut <= len(stream); cut++ {
		var f queryFilter
		got := append(f.split([]byte(stream[:cut])), f.split([]byte(stream[cut:]))...)
		// Merge adjacent data pieces: a cut may split one run in two.
		var merged []piece
		for _, p := range got {
			if n := len(merged); n > 0 && !p.query && !merged[n-1].query {
				merged[n-1].data = append(append([]byte{}, merged[n-1].data...), p.data...)
				continue
			}
			merged = append(merged, p)
		}
		if !reflect.DeepEqual(merged, want) {
			t.Fatalf("cut at %d: got %+v, want %+v", cut, merged, want)
		}
	}
}

func TestHarnessScreenTracksOutput(t *testing.T) {
	m := newMachine(t, "local")
	t.Run("lines appear on the screen", func(t *testing.T) {
		x := startHelper(t, m, TermOptions{Cols: 40, Rows: 10}, "lines", "30")
		x.WaitFor("line 30", 10*time.Second)
		rows := x.Screen()
		if len(rows) != 10 {
			t.Fatalf("%d rows, want 10", len(rows))
		}
		if rows[8] != "line 30" {
			t.Errorf("row 8 is %q, want %q; screen:\n%s", rows[8], "line 30", strings.Join(rows, "\n"))
		}
	})
	t.Run("a resize reaches the program and the screen", func(t *testing.T) {
		x := startHelper(t, m, TermOptions{Cols: 80, Rows: 24}, "size")
		x.WaitFor("80 x 24", 10*time.Second)
		x.Resize(100, 30)
		x.WaitFor("100 x 30", 10*time.Second)
		if got := len(x.Screen()); got != 30 {
			t.Errorf("%d rows after the resize, want 30", got)
		}
	})
	t.Run("keys arrive as a terminal sends them", func(t *testing.T) {
		x := startHelper(t, m, TermOptions{}, "hex")
		x.WaitFor("ready", 10*time.Second)
		x.Key("prefix+o")
		x.Key("enter")
		x.Key("up")
		x.Key("alt+x")
		x.WaitFor("00 6f 0d 1b 5b 41 1b 78", 10*time.Second)
	})
	t.Run("a bell is counted", func(t *testing.T) {
		x := startHelper(t, m, TermOptions{}, "modes", "\a")
		x.WaitFor("ready", 10*time.Second)
		testkit.Eventually(t, 5*time.Second, "one bell", func() bool { return x.BellCount() == 1 })
	})
}

// sshArgs is the argument vector of 3.5.2 after "ssh".
func sshArgs(alias string) []string {
	return []string{"-T",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
		"--", alias, "devtools", "bridge"}
}

func TestHarnessFakeSSHRecordsItsArguments(t *testing.T) {
	a, b := newMachine(t, "desktop"), newMachine(t, "macmini")
	l := Connect(t, a, b, "macmini", true)
	cfg, err := os.ReadFile(a.ConfigPath())
	if err != nil || !strings.Contains(string(cfg), "machine macmini connect\n") {
		t.Fatalf("config of a: %q, %v", cfg, err)
	}
	a.RunBin(fakeSSH, RunOptions{}, sshArgs("macmini")...)
	if l.Bridges() != 1 {
		t.Errorf("the attempt did not become the bridge of b (%d bridges)", l.Bridges())
	}
	r := a.RunBin(fakeSSH, RunOptions{}, sshArgs("nowhere")...)
	if r.Exit != 255 || !strings.Contains(r.Stderr, "Could not resolve hostname nowhere") {
		t.Errorf("an unknown alias: exit %d, stderr %q", r.Exit, r.Stderr)
	}
	r = a.RunBin(fakeSSH, RunOptions{}, "-T", "macmini", "uname")
	if r.Exit != 255 || l.Bridges() != 1 {
		t.Errorf("a command other than the bridge: exit %d, %d bridges", r.Exit, l.Bridges())
	}
	want := [][]string{sshArgs("macmini"), sshArgs("nowhere"), {"-T", "macmini", "uname"}}
	if got := a.SSHLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("ssh log:\n%q\nwant\n%q", got, want)
	}
	if got := b.SSHLog(); len(got) != 0 {
		t.Errorf("the log of b holds %q", got)
	}
}

func TestHarnessFakeSSHFailsLikeEachReason(t *testing.T) {
	a, b := newMachine(t, "desktop"), newMachine(t, "macbook")
	l := Connect(t, a, b, "macbook", false)
	// The patterns are those of 3.5.2, by which remote.Classify tells the
	// reasons apart.
	for _, tc := range []struct {
		reason string
		ok     func(r Result) bool
	}{
		{"auth", func(r Result) bool { return r.Exit == 255 && strings.Contains(r.Stderr, "Permission denied") }},
		{"host-key", func(r Result) bool {
			return r.Exit == 255 && strings.Contains(r.Stderr, "Host key verification failed")
		}},
		{"unreachable", func(r Result) bool { return r.Exit == 255 && strings.Contains(r.Stderr, "Connection refused") }},
		{"no-devtools", func(r Result) bool { return r.Exit == 127 && strings.Contains(r.Stderr, "command not found") }},
		{"no-server", func(r Result) bool {
			return r.Exit == 1 && strings.HasPrefix(r.Stdout, "devtools-bridge-error no-server ")
		}},
		{"incompatible", func(r Result) bool { return strings.Contains(r.Stderr, `unknown command "bridge"`) }},
		{"protocol", func(r Result) bool {
			i := strings.Index(r.Stdout, "devtools-bridge-ready 1\n")
			return i > 64<<10
		}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			l.Block(tc.reason)
			r := a.RunBin(fakeSSH, RunOptions{}, sshArgs("macbook")...)
			if !tc.ok(r) {
				t.Errorf("exit %d, stderr %q, stdout %.200q", r.Exit, r.Stderr, r.Stdout)
			}
		})
	}
	if l.Bridges() != 0 {
		t.Errorf("a blocked attempt became the bridge")
	}
	l.Block("")
	a.RunBin(fakeSSH, RunOptions{}, sshArgs("macbook")...)
	if l.Bridges() != 1 {
		t.Errorf("after Block(\"\") the attempt did not become the bridge")
	}

	t.Run("delay and cut", func(t *testing.T) {
		l.Delay(time.Minute)
		done := make(chan Result, 1)
		go func() { done <- a.RunBin(fakeSSH, RunOptions{Timeout: 2 * time.Minute}, sshArgs("macbook")...) }()
		testkit.Eventually(t, 10*time.Second, "the delayed attempt runs", func() bool { return len(l.Pids()) == 1 })
		start := time.Now()
		l.Cut()
		select {
		case r := <-done:
			if r.Exit != -1 {
				t.Errorf("a cut attempt exited %d, want a signal", r.Exit)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Cut did not end the attempt")
		}
		if time.Since(start) > 10*time.Second {
			t.Errorf("Cut took %v", time.Since(start))
		}
		if len(l.Pids()) != 0 {
			t.Errorf("an attempt runs after Cut")
		}
		l.Delay(0)
	})
}

// fakeChildren returns the processes whose program lies in m's directory:
// the fakes and anything they started.
func fakeChildren(t *testing.T, m *Machine) []platform.Proc {
	t.Helper()
	procs, err := platform.Procs()
	if err != nil {
		t.Fatal(err)
	}
	var out []platform.Proc
	for _, p := range procs {
		if len(p.Args) > 0 && strings.HasPrefix(p.Args[0], m.Root()+string(filepath.Separator)) {
			out = append(out, p)
		}
	}
	return out
}

func TestHarnessFakeTmuxRecordsAndNeverAttaches(t *testing.T) {
	m := newMachine(t, "local")
	m.InstallTmux()
	calls := [][]string{{"new-session", "-A", "-s", "x"}, {"attach-session", "-t", "x"}, {"attach"}}
	for _, args := range calls {
		start := time.Now()
		r := m.RunBin(fakeTmuxName, RunOptions{Timeout: 5 * time.Second}, args...)
		if took := time.Since(start); took > time.Second {
			t.Errorf("tmux %v took %v", args, took)
		}
		if r.Exit != 0 {
			t.Errorf("tmux %v exited %d: %s", args, r.Exit, r.Stderr)
		}
	}
	if got := m.TmuxLog(); !reflect.DeepEqual(got, calls) {
		t.Errorf("tmux log %q, want %q", got, calls)
	}
	if left := fakeChildren(t, m); len(left) != 0 {
		t.Errorf("processes left: %+v", left)
	}
	if got := m.TmuxSessions(); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("sessions %q, want [x]", got)
	}
}

func TestHarnessFakeTmuxAnswersTheBackendsCalls(t *testing.T) {
	m := newMachine(t, "local")
	tmuxVar := m.InstallTmux()
	sock, _, _ := strings.Cut(tmuxVar, ",")
	run := func(args ...string) Result {
		t.Helper()
		return m.RunBin(fakeTmuxName, RunOptions{Timeout: 5 * time.Second}, args...)
	}
	if r := run("new-session", "-d", "-s", "a", "-c", m.Home); r.Exit != 0 {
		t.Fatalf("new-session -d: %d %s", r.Exit, r.Stderr)
	}
	if r := run("has-session", "-t", "a"); r.Exit != 0 {
		t.Errorf("has-session a: %d", r.Exit)
	}
	if r := run("has-session", "-t", "b"); r.Exit != 1 {
		t.Errorf("has-session b: %d", r.Exit)
	}
	r := run("list-sessions", "-F", "#{session_name}\t#{session_windows}\t#{session_attached}")
	if r.Stdout != "a\t1\t0\n" {
		t.Errorf("list-sessions: %q", r.Stdout)
	}
	r = run("-S", sock, "display-message", "-p", "#{pid}")
	_, pid, _ := strings.Cut(tmuxVar, ",")
	pid, _, _ = strings.Cut(pid, ",")
	if strings.TrimSpace(r.Stdout) != pid {
		t.Errorf("#{pid}: %q, want %s", r.Stdout, pid)
	}
	r = run("-S", sock, "display-message", "-p", "-t", "%0", "#{pane_tty}")
	if tty := strings.TrimSpace(r.Stdout); r.Exit != 0 || !strings.HasPrefix(tty, "/dev/") {
		t.Errorf("#{pane_tty}: %d %q", r.Exit, r.Stdout)
	}
	if r := run("-S", sock, "display-message", "-p", "-t", "%7", "#{pane_tty}"); r.Exit != 1 {
		t.Errorf("an unknown pane: %d", r.Exit)
	}
	if r := run("-S", m.DeadTmuxVar()[:strings.IndexByte(m.DeadTmuxVar(), ',')], "display-message", "-p", "#{pid}"); r.Exit != 1 || !strings.Contains(r.Stderr, "no server running") {
		t.Errorf("a dead socket: %d %q", r.Exit, r.Stderr)
	}
	if r := run("switch-client", "-t", "a"); r.Exit != 0 {
		t.Errorf("switch-client: %d", r.Exit)
	}
	if r := run("kill-session", "-t", "a"); r.Exit != 0 || len(m.TmuxSessions()) != 0 {
		t.Errorf("kill-session: %d, sessions %q", r.Exit, m.TmuxSessions())
	}
	if r := run("-V"); strings.TrimSpace(r.Stdout) != tmuxVersion {
		t.Errorf("-V: %q", r.Stdout)
	}
}

func TestHarnessFakeHerdrAnswersTheKeptCalls(t *testing.T) {
	m := newMachine(t, "local")
	m.InstallHerdr()
	if got := valueOf(m.Env(), "HERDR_BIN_PATH"); got != m.Bin(fakeHerdrName) {
		t.Fatalf("HERDR_BIN_PATH is %q", got)
	}
	run := func(args ...string) Result {
		t.Helper()
		start := time.Now()
		r := m.RunBin(fakeHerdrName, RunOptions{Timeout: 5 * time.Second}, args...)
		if took := time.Since(start); took > time.Second {
			t.Errorf("herdr %v took %v", args, took)
		}
		return r
	}
	status := func() bool {
		t.Helper()
		var s struct{ Running *bool }
		r := run("status", "server", "--json")
		if err := json.Unmarshal([]byte(r.Stdout), &s); err != nil || s.Running == nil {
			t.Fatalf("status server --json: %q", r.Stdout)
		}
		return *s.Running
	}
	if status() {
		t.Fatal("the fake server runs before it was started")
	}
	if r := run("workspace", "list"); r.Exit != 1 {
		t.Errorf("workspace list without a server: %d", r.Exit)
	}
	if r := run("server"); r.Exit != 0 {
		t.Errorf("server: %d", r.Exit)
	}
	if !status() {
		t.Fatal("the fake server does not run after `server`")
	}
	// workspace create, as Switch does (herdr.go:136), read as parseCreated
	// reads it (herdr.go:250-267).
	var created struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	r := run("workspace", "create", "--cwd", m.Home, "--label", "proj-main", "--focus")
	if err := json.Unmarshal([]byte(r.Stdout), &created); err != nil || created.Result.Workspace.ID == "" || created.Result.RootPane.ID == "" {
		t.Fatalf("workspace create: %q %v", r.Stdout, err)
	}
	ws := created.Result.Workspace.ID
	var list struct {
		Result struct {
			Workspaces []struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	r = run("workspace", "list")
	if err := json.Unmarshal([]byte(r.Stdout), &list); err != nil || len(list.Result.Workspaces) != 1 || list.Result.Workspaces[0].Label != "proj-main" {
		t.Errorf("workspace list: %q %v", r.Stdout, err)
	}
	if r := run("workspace", "focus", ws); r.Exit != 0 {
		t.Errorf("workspace focus: %d %s", r.Exit, r.Stderr)
	}
	for _, args := range [][]string{{"tab", "list", "--workspace", ws}, {"pane", "list", "--workspace", ws}} {
		if r := run(args...); r.Exit != 0 || !strings.HasPrefix(r.Stdout, `{"result":`) {
			t.Errorf("herdr %v: %d %q", args, r.Exit, r.Stdout)
		}
	}
	m.ChangeHerdr(func(st *herdrState) {
		st.Agents = []herdrAgent{{Pane: created.Result.RootPane.ID, Workspace: ws, Name: "claude", Kind: "claude", Status: "blocked"}}
	})
	var agents struct {
		Result struct {
			Agents []struct {
				Pane   string `json:"pane_id"`
				Status string `json:"agent_status"`
			} `json:"agents"`
		} `json:"result"`
	}
	r = run("agent", "list")
	if err := json.Unmarshal([]byte(r.Stdout), &agents); err != nil || len(agents.Result.Agents) != 1 || agents.Result.Agents[0].Status != "blocked" {
		t.Errorf("agent list: %q %v", r.Stdout, err)
	}
	if r := run("agent", "focus", created.Result.RootPane.ID); r.Exit != 0 {
		t.Errorf("agent focus: %d %s", r.Exit, r.Stderr)
	}
	if r := run("workspace", "close", ws); r.Exit != 0 {
		t.Errorf("workspace close: %d %s", r.Exit, r.Stderr)
	}
	// The client (Attach, server.go:97) records and returns.
	if r := run(); r.Exit != 0 {
		t.Errorf("the client: %d", r.Exit)
	}
	if got := len(m.HerdrLog()); got != 13 {
		t.Errorf("%d calls in the log, want 13: %q", got, m.HerdrLog())
	}
	if left := fakeChildren(t, m); len(left) != 0 {
		t.Errorf("processes left: %+v", left)
	}
}

// markNames are the variables TestHarnessContextsCarryTheirMarks looks at.
var markNames = []string{
	"DEVTOOLS_PANE", "DEVTOOLS_SOCKET", "DEVTOOLS_TTY", envMultiplexer,
	"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_PANE_ID", "STY",
}

// checkMarks fails unless exactly the variables in set are among the mark
// variables of env.
func checkMarks(t *testing.T, env []string, set ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, n := range set {
		want[n] = true
	}
	for _, n := range markNames {
		_, has := testkit.LookupEnv(env, n)
		if has != want[n] {
			t.Errorf("%s set: %v, want %v (env %q)", n, has, want[n], env)
		}
	}
}

func TestHarnessContextsCarryTheirMarks(t *testing.T) {
	plain := []string{}
	if namesDevtools {
		plain = []string{envMultiplexer}
	}
	for _, tc := range []struct {
		c        Context
		set      []string
		identity func(id string) bool
		check    func(t *testing.T, m *Machine, o TermOptions)
	}{
		{Plain, plain, func(id string) bool { return id == ghosttyIdentity }, func(t *testing.T, m *Machine, o TermOptions) {
			if namesDevtools && valueOf(o.Env, envMultiplexer) != "devtools" {
				t.Errorf("a plain terminal does not name devtools: %q", o.Env)
			}
		}},
		{DevtoolsPaneOverSSH, nil, func(id string) bool {
			f := strings.Fields(id)
			return len(f) == 3 && f[0] == "devtools"
		}, nil},
		{TmuxPane, []string{"TMUX", "TMUX_PANE"}, func(id string) bool { return id == "tmux 3.8" }, func(t *testing.T, m *Machine, o TermOptions) {
			// The pane is live and answers for TMUX_PANE; its pid is the
			// one in $TMUX.
			sock, rest, _ := strings.Cut(valueOf(o.Env, "TMUX"), ",")
			pid, _, _ := strings.Cut(rest, ",")
			r := m.RunBin(fakeTmuxName, RunOptions{}, "-S", sock, "display-message", "-p", "#{pid}")
			if strings.TrimSpace(r.Stdout) != pid {
				t.Errorf("#{pid} %q, want %s", r.Stdout, pid)
			}
			// A Term attached in the pane is the pane's terminal device.
			_, paneTTY := testkit.OpenPTY(t, 80, 24)
			for _, hook := range attachHooks {
				hook(m, o, paneTTY.Name())
			}
			r = m.RunBin(fakeTmuxName, RunOptions{}, "-S", sock, "display-message", "-p", "-t", valueOf(o.Env, "TMUX_PANE"), "#{pane_tty}")
			if r.Exit != 0 || strings.TrimSpace(r.Stdout) != paneTTY.Name() {
				t.Errorf("the pane reports %q (%s), want %s", r.Stdout, r.Stderr, paneTTY.Name())
			}
			if c, err := net.Dial("unix", sock); err != nil {
				t.Errorf("the socket of the fake tmux: %v", err)
			} else {
				c.Close()
			}
		}},
		{TmuxPopup, []string{"TMUX"}, func(id string) bool { return id == "tmux 3.8" }, func(t *testing.T, m *Machine, o TermOptions) {
			// The pane the popup came from has another device than the
			// terminal of a Term in the popup: attaching one there does not
			// make the pane report it.
			_, popupTTY := testkit.OpenPTY(t, 80, 24)
			for _, hook := range attachHooks {
				hook(m, o, popupTTY.Name())
			}
			sock, _, _ := strings.Cut(valueOf(o.Env, "TMUX"), ",")
			r := m.RunBin(fakeTmuxName, RunOptions{}, "-S", sock, "display-message", "-p", "-t", "%0", "#{pane_tty}")
			if tty := strings.TrimSpace(r.Stdout); !strings.HasPrefix(tty, "/dev/") || tty == popupTTY.Name() {
				t.Errorf("the pane of the popup reports %q, the popup's own terminal is %s", tty, popupTTY.Name())
			}
		}},
		{HerdrPane, []string{"HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_PANE_ID"}, func(id string) bool { return id == "libghostty" }, func(t *testing.T, m *Machine, o TermOptions) {
			if c, err := net.Dial("unix", valueOf(o.Env, "HERDR_SOCKET_PATH")); err != nil {
				t.Errorf("the socket of the fake herdr: %v", err)
			} else {
				c.Close()
			}
			if valueOf(m.Env(), "HERDR_BIN_PATH") != m.Bin(fakeHerdrName) {
				t.Errorf("HERDR_BIN_PATH does not name the fake")
			}
			r := m.RunBin(fakeHerdrName, RunOptions{}, "status", "server", "--json")
			if !strings.Contains(r.Stdout, `"running":true`) {
				t.Errorf("the fake herdr server does not run: %q", r.Stdout)
			}
		}},
		{Screen, []string{"STY"}, func(id string) bool { return id == ghosttyIdentity }, nil},
		{DeadTmux, []string{"TMUX"}, func(id string) bool { return id == ghosttyIdentity }, func(t *testing.T, m *Machine, o TermOptions) {
			sock, _, _ := strings.Cut(valueOf(o.Env, "TMUX"), ",")
			if _, err := os.Stat(sock); err != nil {
				t.Errorf("the dead socket does not exist: %v", err)
			}
			if c, err := net.Dial("unix", sock); err == nil {
				c.Close()
				t.Errorf("somebody listens on the dead socket")
			}
			r := m.RunBin(fakeTmuxName, RunOptions{}, "-S", sock, "display-message", "-p", "#{pid}")
			if r.Exit == 0 || !strings.Contains(r.Stderr, "no server running") {
				t.Errorf("the fake tmux answers on the dead socket: %d %q", r.Exit, r.Stderr)
			}
		}},
	} {
		t.Run(tc.c.String(), func(t *testing.T) {
			m := newMachine(t, "local")
			o := In(m, tc.c)
			checkMarks(t, o.Env, tc.set...)
			if !tc.identity(o.Identity) {
				t.Errorf("identity %q", o.Identity)
			}
			if o.Silent {
				t.Errorf("the context is silent")
			}
			if tc.check != nil {
				tc.check(t, m, o)
			}
			if tc.c != Plain && valueOf(o.Env, envMultiplexer) != "" {
				t.Errorf("%v names a backend", tc.c)
			}
		})
	}
}

// binaryHasServer reports whether the binary under test knows the command
// `devtools server` (4.5). It asks for the command's help in a machine of
// its own, which starts nothing. A binary that answers anything but its
// help or cobra's "unknown command" fails the test.
func binaryHasServer(t *testing.T) bool {
	t.Helper()
	m := newMachine(t, "probe")
	r := m.RunWith(RunOptions{Timeout: 10 * time.Second}, "server", "--help")
	switch {
	case r.Exit == 0:
		return true
	case strings.Contains(r.Stdout+r.Stderr, `unknown command "server"`):
		return false
	}
	t.Fatalf("devtools server --help: exit %d\nstdout:\n%s\nstderr:\n%s", r.Exit, r.Stdout, r.Stderr)
	return false
}

// TestHarnessDevtoolsPaneContext is the DevtoolsPane row of
// TestHarnessContextsCarryTheirMarks. It needs a running server and a pane,
// so it first passes at K3, and is left out of the run of the harness
// before that by its name.
//
// Provisional until K3: CHECK ./internal/e2e/ runs every test of the
// package, and before the command `devtools server` of 4.5 exists no server
// can start. The test then returns at once with
// a log line, as TestGallery does without its variable; it does not call
// t.Skip, because a skipped test fails a checkpoint (6.7). From the first
// binary that knows `server` on, the whole test runs, and any failure of
// that command fails it. K3 runs it with -v and checks that the line
// "returns at once" is absent.
func TestHarnessDevtoolsPaneContext(t *testing.T) {
	if !binaryHasServer(t) {
		t.Log("provisional K3: the binary under test has no command `server`; the test returns at once until it has")
		return
	}
	m := StartMachine(t, "local")
	o := In(m, DevtoolsPane)
	checkMarks(t, o.Env, "DEVTOOLS_PANE", "DEVTOOLS_SOCKET", "DEVTOOLS_TTY")
	if got := valueOf(o.Env, "DEVTOOLS_SOCKET"); got != m.Socket() {
		t.Errorf("DEVTOOLS_SOCKET %q, want %q", got, m.Socket())
	}
	if tty := valueOf(o.Env, "DEVTOOLS_TTY"); !strings.HasPrefix(tty, "/dev/") {
		t.Errorf("DEVTOOLS_TTY %q", tty)
	}
	pane := valueOf(o.Env, "DEVTOOLS_PANE")
	_, r := m.Oneshot(proto.StateGet{})
	var snap proto.Snapshot
	if err := r.Msg.Decode(&snap); err != nil {
		t.Fatal(err)
	}
	// The variables came from inside the pane, so its program runs: the
	// helper displayed the session (a pane that session.open made is
	// dormant until then), and the pane keeps running after the helper's
	// view ended.
	found := false
	for _, p := range snap.Panes {
		if p.ID != pane {
			continue
		}
		found = true
		if p.State != proto.PaneRunning {
			t.Errorf("pane %s is %s, want %s", pane, p.State, proto.PaneRunning)
		}
	}
	if !found {
		t.Errorf("DEVTOOLS_PANE %q is not a pane of the server", pane)
	}
	// The helper's view is gone: the scenario starts without a client. The
	// server sees the close of the connection on its own time.
	w := m.Welcome()
	for deadline := time.Now().Add(5 * time.Second); w.Clients != 0 && time.Now().Before(deadline); w = m.Welcome() {
		time.Sleep(testkit.PollInterval)
	}
	if w.Clients != 0 {
		t.Errorf("%d interactive connections remain after In(m, DevtoolsPane), want 0", w.Clients)
	}
	if want := "devtools " + identWord(w.Version, "unknown") + " " + identWord(w.Host, "unknown"); o.Identity != want {
		t.Errorf("identity %q, want %q", o.Identity, want)
	}
}

func TestThresholdsHoldEveryRowOf10Point4(t *testing.T) {
	ids := []string{"T1", "T2", "T2b", "T3", "T3b", "T4", "T5", "T6", "T7", "T8", "T9", "T10"}
	if len(Thresholds) != len(ids) {
		t.Fatalf("%d rows, want %d", len(Thresholds), len(ids))
	}
	for i, id := range ids {
		th := Thresholds[i]
		if th.ID != id || len(th.Loopback) == 0 || len(th.LAN) == 0 || len(th.Tests) == 0 {
			t.Errorf("row %d: %+v", i, th)
		}
		for _, h := range th.Hard {
			Limit(id, h)
		}
	}
	if Limit("T10", "full").Bytes() != 520<<20 || Limit("T10", "idle").Bytes() != 60<<20 {
		t.Errorf("T10: %+v", Row("T10").Loopback)
	}
	if Limit("T1", "first local row").Duration() != 100*time.Millisecond {
		t.Errorf("T1: %+v", Row("T1"))
	}
	if !Limit("T5", "throughput").Met(31) || Limit("T5", "throughput").Met(29) {
		t.Errorf("T5 throughput is a floor")
	}
}

// The real mode (10.1.8) is never entered by an automated run: these tests
// drive its dispatch with fixtures. The environments, the PATH, the binary
// and the "local server" below are made in temporary directories, and the
// server is a listener of the test; no real server, ssh or HOME is reached.

// fixtureEnv returns getenv over the variables of env.
func fixtureEnv(env ...string) func(string) string {
	return func(name string) string { return valueOf(env, name) }
}

func TestHarnessRealModeReadsItsThreeVariables(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want RealConfig
		on   bool
		err  string
	}{
		{"unset is isolated", nil, RealConfig{}, false, ""},
		{"empty is isolated", []string{EnvReal + "=", EnvRemote + "=macmini"}, RealConfig{}, false, ""},
		{"all three", []string{EnvReal + "=1", EnvRemote + "=macmini", EnvQuery + "=devtools"}, RealConfig{Remote: "macmini", Query: "devtools"}, true, ""},
		{"a label with a space", []string{EnvReal + "=1", EnvRemote + "=mac-mini.lan", EnvQuery + "=my project"}, RealConfig{Remote: "mac-mini.lan", Query: "my project"}, true, ""},
		{"another value", []string{EnvReal + "=yes", EnvRemote + "=macmini", EnvQuery + "=devtools"}, RealConfig{}, false, EnvReal + "=1"},
		{"no remote", []string{EnvReal + "=1", EnvQuery + "=devtools"}, RealConfig{}, false, EnvRemote},
		{"a remote that is no alias", []string{EnvReal + "=1", EnvRemote + "=-oProxyCommand=x", EnvQuery + "=devtools"}, RealConfig{}, false, EnvRemote},
		{"no query", []string{EnvReal + "=1", EnvRemote + "=macmini"}, RealConfig{}, false, EnvQuery},
		{"a blank query", []string{EnvReal + "=1", EnvRemote + "=macmini", EnvQuery + "=  "}, RealConfig{}, false, EnvQuery},
		{"a query with a control character", []string{EnvReal + "=1", EnvRemote + "=macmini", EnvQuery + "=a\x1bb"}, RealConfig{}, false, EnvQuery},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, on, err := parseReal(fixtureEnv(tc.env...))
			if tc.err == "" && err != nil {
				t.Fatalf("error %v", err)
			}
			if tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("error %v, want one naming %s", err, tc.err)
			}
			if got != tc.want || on != tc.on {
				t.Errorf("got %+v %v, want %+v %v", got, on, tc.want, tc.on)
			}
		})
	}
}

func TestHarnessRealModeRunsOnlyTheReadAndSwitchTests(t *testing.T) {
	// The pattern rc-measure passes, read from the script, is accepted.
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root, "scripts", "verify", "rc-measure"))
	if err != nil {
		t.Fatal(err)
	}
	var measured string
	for _, line := range strings.Split(string(script), "\n") {
		if v, ok := strings.CutPrefix(line, "TESTS="); ok {
			measured = strings.Trim(v, `'"`)
		}
	}
	if measured == "" {
		t.Fatal("rc-measure has no line TESTS=")
	}
	for _, p := range []string{measured, "^(" + strings.Join(RealTests, "|") + ")$", "TestSwitchLocalHeldCold", "^TestInputLatencyRemote$"} {
		if !realFilterOK(p) {
			t.Errorf("%q is refused", p)
		}
	}
	for _, p := range []string{"", ".", "^$", "Test", "TestSwitch", "TestSwitch.*", "^(TestSwitchLocalHeldCold|TestHarnessDevtoolsPaneContext)$",
		"TestSwitchLocalHeldCold/sub", "^()$", "TestHubFirstLocalRow"} {
		if realFilterOK(p) {
			t.Errorf("%q is accepted", p)
		}
	}
}

// realFixture puts the run into the real mode with the environment env for
// the duration of the test, and restores the isolated mode after it.
func realFixture(t *testing.T, env []string, run string) error {
	t.Helper()
	oldBinary, oldMode, oldOn, oldEnv := binary, realMode, realOn, realEnv
	t.Cleanup(func() { binary, realMode, realOn, realEnv = oldBinary, oldMode, oldOn, oldEnv })
	cfg, on, err := parseReal(fixtureEnv(env...))
	if err != nil {
		return err
	}
	if !on {
		t.Fatal("the fixture is not the real mode")
	}
	return enterReal(cfg, env, run)
}

func TestHarnessRealModeEntersOnlyFromAPlainTerminal(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self(), filepath.Join(bin, devtools)); err != nil {
		t.Fatal(err)
	}
	base := []string{"HOME=" + dir, "PATH=" + bin + ":/usr/bin:/bin", EnvReal + "=1", EnvRemote + "=macmini", EnvQuery + "=devtools",
		platform.EnvService + "=devtools-rc", "XDG_CONFIG_HOME=" + filepath.Join(dir, "config")}
	run := "^(" + strings.Join(RealTests, "|") + ")$"

	t.Run("enters", func(t *testing.T) {
		env := append(append([]string{}, base...), "DEVTOOLS_MULTIPLEXER=devtools", "TMUX_PANE=%3")
		if err := realFixture(t, env, run); err != nil {
			t.Fatal(err)
		}
		cfg, on := Real()
		if !on || cfg != (RealConfig{Remote: "macmini", Query: "devtools"}) {
			t.Errorf("Real() = %+v %v", cfg, on)
		}
		// Without DEVTOOLS_E2E_BIN the binary is the devtools on PATH, not a
		// build.
		if Binary() != filepath.Join(bin, devtools) {
			t.Errorf("binary %q", Binary())
		}
		checkMarks(t, realEnv, platform.EnvService, "XDG_CONFIG_HOME", "HOME", "PATH")
		for _, n := range []string{envMultiplexer, "TMUX_PANE", EnvReal, EnvRemote, EnvQuery} {
			if _, has := testkit.LookupEnv(realEnv, n); has {
				t.Errorf("%s reaches the real processes", n)
			}
		}
	})
	t.Run("names its binary", func(t *testing.T) {
		env := append(append([]string{}, base...), EnvBin+"="+self())
		if err := realFixture(t, env, run); err != nil {
			t.Fatal(err)
		}
		if Binary() != self() {
			t.Errorf("binary %q, want %s", Binary(), self())
		}
	})
	for _, tc := range []struct {
		name string
		env  []string
		run  string
		err  string
	}{
		{"an isolated test", base, "^(TestSwitchLocalHeldCold|TestHarnessDevtoolsPaneContext)$", "runs only"},
		{"every test", base, "", "runs only"},
		{"inside tmux", append(append([]string{}, base...), "TMUX=/tmp/x,1,0"), run, "TMUX"},
		{"inside a devtools pane", append(append([]string{}, base...), "DEVTOOLS_PANE=p1"), run, "DEVTOOLS_PANE"},
		{"inside a herdr pane", append(append([]string{}, base...), "HERDR_ENV=1"), run, "HERDR_ENV"},
		{"without a binary", []string{"HOME=" + dir, "PATH=" + dir, EnvReal + "=1", EnvRemote + "=macmini", EnvQuery + "=devtools"}, run, "not on PATH"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			err := realFixture(t, tc.env, tc.run)
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("error %v, want one with %q", err, tc.err)
			}
			if _, on := Real(); on {
				t.Errorf("a refused run is in the real mode")
			}
		})
	}
}

func TestHarnessRealModeMachineIsTheRunningServer(t *testing.T) {
	home := testkit.ShortTempDir(t)
	state := filepath.Join(home, ".local", "state", "devtools")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	// The stand-in of the user's running server: a listener on the default
	// socket of the fixture's HOME.
	ln, err := net.Listen("unix", filepath.Join(state, proto.SocketName))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin", EnvReal + "=1", EnvRemote + "=macmini", EnvQuery + "=devtools", EnvBin + "=" + self()}
	if err := realFixture(t, env, "TestSwitchLocalHeldCold"); err != nil {
		t.Fatal(err)
	}
	// Only the tests of RealTests get the real machine; this one stands for
	// them for its duration.
	old := RealTests
	RealTests = append(append([]string{}, old...), t.Name())
	defer func() { RealTests = old }()

	m := StartMachine(t, "local")
	if !m.IsReal() || m.Home != home || m.StateDir != state || m.Socket() != filepath.Join(state, proto.SocketName) {
		t.Errorf("machine %+v, socket %s", *m, m.Socket())
	}
	if m.ServerPid() != 0 {
		t.Errorf("the harness started a server: %d", m.ServerPid())
	}
	if got, _ := os.Readlink(m.Bin(devtools)); got != self() {
		t.Errorf("devtools is %q, want the binary under test", got)
	}
	if strings.HasPrefix(m.Root(), home) {
		t.Errorf("the harness's files %s are under HOME", m.Root())
	}
	e := m.Env("X=1")
	if valueOf(e, "HOME") != home || valueOf(e, "PATH") != "/usr/bin:/bin" || valueOf(e, "X") != "1" || valueOf(e, "SHELL") != "" {
		t.Errorf("env %q", e)
	}
	if o := In(m, Plain); namesDevtools && valueOf(o.Env, envMultiplexer) != "devtools" {
		t.Errorf("a plain terminal does not name devtools: %q", o.Env)
	}
	l := Connect(t, m, nil, "macmini", true)
	if l.Alias() != "macmini" || l.Pids() != nil || l.Bridges() != 0 {
		t.Errorf("link %+v", l)
	}
	// Nothing was written under HOME: no config line, no fake ssh.
	var written []string
	filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Mode()&os.ModeSocket == 0 {
			written = append(written, p)
		}
		return nil
	})
	if len(written) != 0 {
		t.Errorf("written under HOME: %q", written)
	}
	if _, err := os.Lstat(m.Bin(fakeSSH)); err == nil {
		t.Errorf("a fake ssh was placed")
	}
}

// TestHarnessRealModeReachesTheTests runs this test binary as rc-measure
// runs the package, with DEVTOOLS_E2E_REAL=1 and the filter of the real
// tests, in a fixture environment under a temporary HOME. -test.list makes
// it list the selected tests instead of running them, so TestMain's
// dispatch is exercised up to m.Run and nothing reaches a server. A filter
// that selects other tests is refused before m.Run.
func TestHarnessRealModeReachesTheTests(t *testing.T) {
	home := testkit.ShortTempDir(t)
	filter := "^(" + strings.Join(RealTests, "|") + ")$"
	env := testkit.CleanEnv(home, "GORACE=atexit_sleep_ms=0", EnvReal+"=1", EnvRemote+"=macmini", EnvQuery+"=devtools", EnvBin+"="+self())
	for _, tc := range []struct {
		name, run string
		exit      int
		out       string
	}{
		{"the real tests", filter, 0, ""},
		{"every test", ".", 1, "runs only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(self(), "-test.run="+tc.run, "-test.list="+tc.run)
			cmd.Env = env
			cmd.Dir = home
			out, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				ee, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = ee.ExitCode()
			}
			if code != tc.exit || !strings.Contains(string(out), tc.out) {
				t.Errorf("exit %d, want %d; output:\n%s", code, tc.exit, out)
			}
		})
	}
}
