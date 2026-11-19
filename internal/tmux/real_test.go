package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit"
)

// TestMain keeps the tests hermetic: TMUX and TMUX_PANE of the developer
// are removed, so no function here can reach the developer's server
// through them, and TMUX_TMPDIR is a directory of the test, so even a tmux
// started without -L would make its socket there. The locale variables are
// removed too, so every run has the locale of the Nix build sandbox, which
// has none: there tmux prints each tab of a -F format as '_' unless it is
// started with -u, and the tests show that privateTmux passes it.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("/tmp", "dt-tmux-")
	if err != nil {
		panic(err)
	}
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "LANG" || k == "LANGUAGE" || strings.HasPrefix(k, "LC_") {
			os.Unsetenv(k)
		}
	}
	os.Setenv("TMUX_TMPDIR", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// privateTmux puts a tmux first on PATH that is the real tmux of the dev
// shell bound to a private server, tmux -L devtools-test-<pid> -f
// /dev/null, so every function of the package talks to that server and to
// no other. It returns a function that runs the real tmux against the same
// server. The cleanup kills that server, and only that one, and fails the
// test unless it is gone afterwards. Both pass -u: without a UTF-8 locale
// (the Nix build sandbox has none) tmux 3.8 writes each tab of a -F format
// as '_', and ListSessions then reads a line without fields.
func privateTmux(t *testing.T) func(args ...string) (string, error) {
	t.Helper()
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("no tmux on PATH (the dev shell and nativeCheckInputs have one): %v", err)
	}
	name := fmt.Sprintf("devtools-test-%d", os.Getpid())
	bin := t.TempDir()
	wrapper := fmt.Sprintf("#!/bin/sh\nexec '%s' -u -L '%s' -f /dev/null \"$@\"\n", real, name)
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	private := func(args ...string) (string, error) {
		out, err := exec.Command(real, append([]string{"-u", "-L", name, "-f", "/dev/null"}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	t.Cleanup(func() {
		private("kill-server")
		if out, err := private("ls"); err == nil {
			t.Errorf("the private tmux server %s still runs after kill-server: %s", name, out)
		}
	})
	return private
}

func TestRealTmuxDetachedSessionAndPaneTTY(t *testing.T) {
	testkit.RequirePTY(t)
	private := privateTmux(t)

	// Every pane's shell writes the terminal device it has to the file tty
	// in its directory and then waits.
	shell := filepath.Join(t.TempDir(), "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\ntty > tty.tmp && mv tty.tmp tty\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	alpha, beta := t.TempDir(), t.TempDir()

	if Has("alpha") {
		t.Fatal("Has(alpha) before any server")
	}
	if err := NewDetached("alpha", alpha); err != nil {
		t.Fatal(err)
	}
	if err := NewDetached("beta", beta); err != nil {
		t.Fatal(err)
	}
	if !Has("alpha") || !Has("beta") || Has("gamma") {
		t.Fatalf("Has: alpha %v, beta %v, gamma %v; want true, true, false", Has("alpha"), Has("beta"), Has("gamma"))
	}
	sessions, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range sessions {
		names = append(names, s.Name)
		if s.Attached {
			t.Errorf("session %s has a client; NewDetached attaches none", s.Name)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Fatalf("ListSessions = %v, want alpha and beta", names)
	}

	socket, err := private("display-message", "-p", "-t", "alpha", "#{socket_path}")
	if err != nil {
		t.Fatalf("socket path: %v %s", err, socket)
	}
	pid, err := private("display-message", "-p", "#{pid}")
	if err != nil {
		t.Fatalf("server pid: %v %s", err, pid)
	}
	pane, err := private("display-message", "-p", "-t", "alpha", "#{pane_id}")
	if err != nil {
		t.Fatalf("pane id: %v %s", err, pane)
	}
	tmuxVar := socket + "," + pid + ",0"

	// The device PaneTTY reports is the one the pane's shell has.
	var shellTTY string
	testkit.Eventually(t, 10*time.Second, "the pane's shell wrote its tty", func() bool {
		data, err := os.ReadFile(filepath.Join(alpha, "tty"))
		shellTTY = strings.TrimSpace(string(data))
		return err == nil && shellTTY != ""
	})
	tty, known, err := PaneTTY(tmuxVar, pane)
	if err != nil || !known || tty != shellTTY {
		t.Fatalf("PaneTTY(%s) = %q, %v, %v; the shell has %q", pane, tty, known, err, shellTTY)
	}
	if tty, known, err := PaneTTY(tmuxVar, "%999"); err != nil || known || tty != "" {
		t.Errorf("PaneTTY of an unknown pane = %q, %v, %v; want not known, no error", tty, known, err)
	}

	if alive, err := Alive(tmuxVar); err != nil || !alive {
		t.Errorf("Alive with the server's pid = %v, %v; want true", alive, err)
	}
	if alive, err := Alive(socket + ",1,0"); err != nil || alive {
		t.Errorf("Alive with another pid = %v, %v; want false, no error", alive, err)
	}

	// Remove from outside: the session ends, the fallback stays, no client
	// is touched (there is none).
	if err := Remove("beta", "alpha", alpha, false); err != nil {
		t.Fatal(err)
	}
	if Has("beta") || !Has("alpha") {
		t.Fatalf("after Remove: beta %v, alpha %v; want false, true", Has("beta"), Has("alpha"))
	}
	if err := Remove("beta", "alpha", alpha, false); err != nil {
		t.Errorf("Remove of a session that is gone = %v, want nil", err)
	}

	// A server that is gone answers no, not "unknown".
	if out, err := private("kill-server"); err != nil {
		t.Fatalf("kill-server: %v %s", err, out)
	}
	testkit.Eventually(t, 5*time.Second, "the killed server answers no", func() bool {
		alive, err := Alive(tmuxVar)
		_, known, perr := PaneTTY(tmuxVar, pane)
		return err == nil && !alive && perr == nil && !known
	})
}

func TestSplitTmuxVarTakesTheFieldsFromTheEnd(t *testing.T) {
	for v, want := range map[string][2]string{
		"/tmp/tmux-1000/default,1234,0": {"/tmp/tmux-1000/default", "1234"},
		"/tmp/a,b/sock,77,3":            {"/tmp/a,b/sock", "77"},
	} {
		socket, pid, ok := splitTmuxVar(v)
		if !ok || socket != want[0] || pid != want[1] {
			t.Errorf("splitTmuxVar(%q) = %q, %q, %v; want %q, %q", v, socket, pid, ok, want[0], want[1])
		}
	}
	for _, v := range []string{"", "nocomma", ",1"} {
		if _, _, ok := splitTmuxVar(v); ok {
			t.Errorf("splitTmuxVar(%q) is ok", v)
		}
	}
	if _, _, err := PaneTTY("nocomma", "%1"); err == nil {
		t.Error("PaneTTY of a malformed TMUX is no error")
	}
}
