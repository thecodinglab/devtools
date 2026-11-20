package e2e

// The guard of R3 with the real binary (10.2.1): N1, N3, N5, N16 and the
// plain terminal that passes.

import (
	"strings"
	"testing"
	"time"

	"devtools/internal/ui"
)

// refused runs `devtools <cmd>` in a terminal of o on m and asserts that it
// ends with status 1 and the words want, and that no client started: no
// alternate screen in its output, no client process and no interactive
// connection on m. It returns how long the command ran.
func refused(t *testing.T, m *Machine, o TermOptions, want string, cmd ...string) time.Duration {
	t.Helper()
	start := time.Now()
	x := Attach(t, m, o, cmd...)
	code, out := x.Exit()
	took := time.Since(start)
	if code != 1 {
		t.Errorf("devtools %s: exit %d, want 1; output:\n%s", strings.Join(cmd, " "), code, out)
	}
	if got := strings.TrimSpace(lines(out)); got != want {
		t.Errorf("devtools %s: output %q, want %q", strings.Join(cmd, " "), got, want)
	}
	if strings.Contains(rawOutput(x), altScreen) {
		t.Errorf("devtools %s entered the alternate screen: a client started", strings.Join(cmd, " "))
	}
	if n := m.Clients(); n != 0 {
		t.Errorf("devtools %s: %d clients", strings.Join(cmd, " "), n)
	}
	return took
}

func TestGuardOpensHubInsidePane(t *testing.T) {
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	// typed in the pane: the pane's own environment and terminal
	x.Type("devtools attach; echo attach-status-$?\r")
	waitUntil(t, x, 10*time.Second, "the window's hub opens", hubOpen)
	if st, ok := m.ServerStatus(); !ok || st.Clients != 1 {
		t.Errorf("server status %+v (%v) while the hub is open: want the one interactive connection of the window", st, ok)
	}
	closeHub(t, x)
	waitText(t, x, 10*time.Second, "attach-status-0")
	if n := attachProcs(t, m, x); n != 1 {
		t.Errorf("%d client processes, want the one of the window", n)
	}
	if st, ok := m.ServerStatus(); !ok || st.Clients != 1 {
		t.Errorf("server status %+v (%v): want one interactive connection, the window's", st, ok)
	}
	// The pane said that it is already inside devtools: the line before the
	// status (the text is the command line's own, not one of internal/ui,
	// so it is not compared word for word here).
	rows := x.Screen()
	for i, r := range rows {
		if strings.Contains(r, "attach-status-0") && (i == 0 || strings.TrimSpace(rows[i-1]) == "" || strings.Contains(rows[i-1], "devtools attach;")) {
			t.Errorf("devtools attach printed nothing before its status; screen:\n%s", strings.Join(rows, "\n"))
		}
	}
}

func TestGuardRefusesUnderScreenEnv(t *testing.T) {
	m := StartMachine(t, "desktop")
	screen := In(m, Screen).Env
	for _, mark := range []struct{ kv, where string }{
		{screen[0], "screen"},
		{"ZELLIJ=0", "zellij"},
		{"ZELLIJ_SESSION_NAME=e2e", "zellij"},
	} {
		for _, cmd := range []string{"attach", "pick", "sessions", "agents"} {
			name, _, _ := strings.Cut(mark.kv, "=")
			t.Run(name+"/"+cmd, func(t *testing.T) {
				// The terminal itself is plain (it answers as ghostty); the
				// mark says screen or zellij runs around it.
				o := TermOptions{Env: []string{mark.kv}, Identity: ghosttyIdentity}
				refused(t, m, o, ui.Message("othermux", devtools, mark.where), cmd)
			})
		}
	}
	if st, ok := m.ServerStatus(); !ok || st.Clients != 0 {
		t.Errorf("server status %+v (%v): an interactive connection was made", st, ok)
	}
}

func TestGuardRefusesByProbeName(t *testing.T) {
	m := StartMachine(t, "desktop")
	t.Run("a devtools pane reached over ssh", func(t *testing.T) {
		o := In(m, DevtoolsPaneOverSSH)
		host := o.Identity[strings.LastIndexByte(o.Identity, ' ')+1:]
		refused(t, m, overSSH(o.Identity), ui.Message("nested", devtools, host), "attach")
	})
	t.Run("a tmux pane reached over ssh", func(t *testing.T) {
		refused(t, m, overSSH(In(m, TmuxPane).Identity), ui.Message("othermux", devtools, "tmux"), "attach")
	})
	if got := m.TmuxLog(); len(got) != 0 {
		t.Errorf("tmux ran: %q", got)
	}
}

func TestGuardRefusesSilentTerminal(t *testing.T) {
	m := StartMachine(t, "desktop")
	o := overSSH("")
	o.Silent = true
	took := refused(t, m, o, ui.Message("unknown"), "attach")
	// The probe waits 2 s for DA1 (3.4); a refusal long after that would be
	// a hang, one before it did not wait.
	if took < 1500*time.Millisecond || took > 6*time.Second {
		t.Errorf("the refusal came after %v, want about 2 s", took)
	}
	t.Logf("a silent terminal was refused after %v", took)
}

func TestGuardAcceptsPlainTerminal(t *testing.T) {
	m := StartMachine(t, "desktop")
	attached := func(t *testing.T, o TermOptions) {
		t.Helper()
		x := Attach(t, m, o)
		eventually(t, 10*time.Second, "the client starts", func() bool {
			return strings.Contains(rawOutput(x), altScreen) && m.Clients() == 1
		})
		x.Key("prefix+d")
		if code, out := x.Exit(); code != 0 {
			t.Errorf("detach: exit %d; output:\n%s", code, out)
		}
	}
	t.Run("ghostty", func(t *testing.T) {
		if id := In(m, Plain).Identity; !strings.HasPrefix(id, "ghostty ") {
			t.Fatalf("the plain identity is %q", id)
		}
		attached(t, In(m, Plain))
	})
	t.Run("DA1 without XTVERSION is refused", func(t *testing.T) {
		refused(t, m, overSSH(""), ui.Message("unknown"), "attach")
	})
	t.Run("DA1 without XTVERSION with DEVTOOLS_TRUST_TERMINAL=1", func(t *testing.T) {
		o := overSSH("")
		o.Env = append(o.Env, "DEVTOOLS_TRUST_TERMINAL=1")
		attached(t, o)
	})
	t.Run("DEVTOOLS_TRUST_TERMINAL=1 lets nothing else in", func(t *testing.T) {
		o := overSSH(In(m, DevtoolsPaneOverSSH).Identity)
		o.Env = append(o.Env, "DEVTOOLS_TRUST_TERMINAL=1")
		host := o.Identity[strings.LastIndexByte(o.Identity, ' ')+1:]
		refused(t, m, o, ui.Message("nested", devtools, host), "attach")
	})
}
