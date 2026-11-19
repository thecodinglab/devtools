package backend

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devtools/internal/testkit"
)

// The fake herdr (10.2.8): a script named by HERDR_BIN_PATH that records
// its arguments, one call per line of $HERDR_LOG ("<attach>" for a call
// without arguments, which is the herdr client), and answers from the
// environment of the test. It is a copy of today's installFakeHerdr
// (internal/cli/cli_test.go:1194) without the machine calls the reduced
// backend does not make. The server runs when the file $HERDR_RUNNING
// exists; "herdr server" creates it. No test runs a real herdr (6.1 rule 6,
// RK27).
const fakeHerdrScript = `#!/bin/sh
if [ $# -eq 0 ]; then echo '<attach>' >> "$HERDR_LOG"; else printf '%s\n' "$*" >> "$HERDR_LOG"; fi
case "$1 $2" in
'workspace list') printf '{"result":{"workspaces":[%s]}}\n' "$HERDR_WORKSPACES";;
'workspace create') echo '{"result":{"workspace":{"workspace_id":"w9"},"root_pane":{"pane_id":"w9:p1"}}}';;
'agent list') printf '{"result":{"agents":[%s]}}\n' "$HERDR_AGENTS";;
'status server') if [ -e "$HERDR_RUNNING" ]; then echo '{"running":true}'; else echo '{"running":false}'; fi;;
'server ') touch "$HERDR_RUNNING";;
*) echo '{"result":{}}';;
esac
exit 0
`

// installFakeHerdr names the fake in HERDR_BIN_PATH, with the given
// comma-separated workspace objects as the open workspaces and agents as
// the agent objects, and its server running or not. It returns the log.
func installFakeHerdr(t *testing.T, workspaces, agents string, running bool) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	log := filepath.Join(dir, "herdr.log")
	marker := filepath.Join(dir, "running")
	if err := os.WriteFile(bin, []byte(fakeHerdrScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if running {
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("HERDR_LOG", log)
	t.Setenv("HERDR_WORKSPACES", workspaces)
	t.Setenv("HERDR_AGENTS", agents)
	t.Setenv("HERDR_RUNNING", marker)
	return log
}

// herdrCalls reads the log of the fake herdr, with the repeated server
// status polls of EnsureServer, whose count depends on timing, collapsed
// into one.
func herdrCalls(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, c := range calls(t, log) {
		if len(out) > 0 && out[len(out)-1] == c {
			continue
		}
		out = append(out, c)
	}
	return out
}

// herdrSocket listens on a unix socket in a short temporary directory, the
// path a HERDR_SOCKET_PATH of a live herdr names. It accepts every
// connection and answers nothing. The listener is closed when the test
// ends.
func herdrSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testkit.ShortTempDir(t), "herdr.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var held []net.Conn
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	t.Cleanup(func() {
		l.Close()
		<-done
		for _, c := range held {
			c.Close()
		}
	})
	return path
}

// staleSocket is a unix socket path whose listener is gone: the file is
// there and a connection is refused, as after a crash of the multiplexer.
func staleSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testkit.ShortTempDir(t), "stale.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the stale socket file is gone: %v", err)
	}
	return path
}

// workspace is one workspace object of herdr's workspace list.
func workspace(id, label string, focused bool, status string) string {
	f := "false"
	if focused {
		f = "true"
	}
	return `{"workspace_id":"` + id + `","label":"` + label + `","tab_count":1,"focused":` + f + `,"agent_status":"` + status + `"}`
}

// agentObject is one agent object of herdr's agent list.
func agentObject(pane, ws, kind, status string) string {
	return `{"pane_id":"` + pane + `","workspace_id":"` + ws + `","agent":"` + kind + `","agent_status":"` + status + `","terminal_title_stripped":"` + kind + ` at work","cwd":"/tmp/` + ws + `","focused":false}`
}

func joinObjects(objects ...string) string { return strings.Join(objects, ",") }
