package herdr

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseWorkspaceList(t *testing.T) {
	got, err := parseWorkspaceList([]byte(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[` +
		`{"active_tab_id":"w1:t1","focused":true,"label":"project-main","number":1,"pane_count":1,"tab_count":1,"workspace_id":"w1"},` +
		`{"active_tab_id":"w2:t1","focused":false,"label":"ops","number":2,"pane_count":3,"tab_count":2,"workspace_id":"w2"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Workspace{
		{ID: "w1", Label: "project-main", Tabs: 1, Focused: true},
		{ID: "w2", Label: "ops", Tabs: 2, Focused: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaces = %#v, want %#v", got, want)
	}
}

func TestErrorMessage(t *testing.T) {
	fallback := errors.New("exit status 1")
	tests := map[string]string{
		`{"error":{"code":"workspace_not_found","message":"workspace nope not found"},"id":"cli:workspace:focus"}`: "workspace nope not found",
		"connection refused\n": "connection refused",
		"":                     "exit status 1",
	}
	for stderr, want := range tests {
		if got := errorMessage([]byte(stderr), fallback); got != want {
			t.Fatalf("errorMessage(%q) = %q, want %q", stderr, got, want)
		}
	}
}

func TestParseAgentListSortsAttentionFirst(t *testing.T) {
	got, err := parseAgentList([]byte(`{"id":"cli:agent:list","result":{"type":"agent_list","agents":[` +
		`{"agent":"claude","agent_status":"working","pane_id":"w1:p1","workspace_id":"w1","terminal_title_stripped":"refactor","cwd":"/src","focused":true,"revision":4},` +
		`{"agent":"codex","agent_status":"idle","pane_id":"w1:p2","workspace_id":"w1"},` +
		`{"agent":"pi","agent_status":"mystery","pane_id":"w2:p1","workspace_id":"w2"},` +
		`{"agent":"claude","agent_status":"blocked","pane_id":"w2:p2","workspace_id":"w2","name":"reviewer"},` +
		`{"agent":"amp","agent_status":"unknown","pane_id":"w3:p1","workspace_id":"w3"},` +
		`{"agent":"claude","agent_status":"done","pane_id":"w3:p2","workspace_id":"w3"},` +
		`{"agent":"codex","agent_status":"idle","pane_id":"w3:p3","workspace_id":"w3"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != (Agent{Pane: "w1:p1", Workspace: "w1", Kind: "claude", Status: "working", Title: "refactor", Cwd: "/src", Focused: true}) {
		t.Fatalf("agent = %#v", got[0])
	}
	SortAgents(got)
	var panes []string
	for _, agent := range got {
		panes = append(panes, agent.Pane)
	}
	want := []string{"w2:p2", "w3:p2", "w1:p2", "w3:p3", "w1:p1", "w3:p1", "w2:p1"}
	if !reflect.DeepEqual(panes, want) {
		t.Fatalf("order = %v, want %v", panes, want)
	}
}

func TestParseServerStatus(t *testing.T) {
	for out, want := range map[string]bool{
		`{"status":"running","running":true,"version":"0.9.5","socket":"/tmp/herdr.sock"}`: true,
		`{"status":"not_running","running":false,"socket":"/tmp/herdr.sock"}`:              false,
	} {
		got, err := parseServerStatus([]byte(out))
		if err != nil || got != want {
			t.Fatalf("parseServerStatus(%s) = %v, %v, want %v", out, got, err, want)
		}
	}
	if _, err := parseServerStatus([]byte("status: running\n")); err == nil {
		t.Fatal("want an error for non-json output")
	}
}

// fakeHerdr logs its arguments and reports a server running once "herdr
// server" has run. workspace list answers $HERDR_WORKSPACES.
func fakeHerdr(t *testing.T) (log, marker string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	log = filepath.Join(dir, "herdr.log")
	marker = filepath.Join(dir, "running")
	script := "#!/bin/sh\nif [ $# -eq 0 ]; then echo '<attach>' >> \"$HERDR_LOG\"; else printf '%s\\n' \"$*\" >> \"$HERDR_LOG\"; fi\n" +
		"case \"$*\" in\n" +
		"'status server --json') if [ -e \"$HERDR_RUNNING\" ]; then echo '{\"running\":true}'; else echo '{\"running\":false}'; fi;;\n" +
		"server) touch \"$HERDR_RUNNING\";;\n" +
		"'workspace list') printf '{\"result\":{\"workspaces\":[%s]}}\\n' \"$HERDR_WORKSPACES\";;\n" +
		"'workspace create '*) echo '{\"result\":{}}';;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("HERDR_LOG", log)
	t.Setenv("HERDR_RUNNING", marker)
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_WORKSPACES", "")
	return log, marker
}

// Switch took over the body of SwitchWorkspace without the returned id
// (P2-09): it focuses the workspace labelled with the session, or creates it
// in dir, and needs nothing of what create prints. The arguments reach the
// program as they are, without a --machine prefix.
func TestSwitchFocusesTheWorkspaceOrCreatesIt(t *testing.T) {
	log, _ := fakeHerdr(t)
	t.Setenv("HERDR_WORKSPACES", `{"label":"other","workspace_id":"w1"},{"label":"small-main","workspace_id":"w2"}`)
	if err := (Client{}).Switch("/dev/small", "small-main"); err != nil {
		t.Fatal(err)
	}
	if err := (Client{}).Switch("/dev/new", "new-main"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"workspace list", "workspace focus w2",
		"workspace list", "workspace create --cwd /dev/new --label new-main --focus",
	}
	if got := readLines(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("herdr calls = %q, want %q", got, want)
	}
}

// EnsureServer and Attach are the steps HerdrMultiplexer.Enter runs in a
// plain terminal (Visit, which ran them itself, is gone): a server is
// started only when none runs, and the client is herdr without arguments.
func TestEnsureServerStartsAServerOnlyWhenNoneRuns(t *testing.T) {
	log, marker := fakeHerdr(t)
	if err := EnsureServer(); err != nil {
		t.Fatal(err)
	}
	if err := EnsureServer(); err != nil {
		t.Fatal(err)
	}
	if err := Attach(); err != nil {
		t.Fatal(err)
	}
	want := []string{"status server --json", "server", "status server --json", "<attach>"}
	if got := readLines(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("herdr calls = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no server was started: %v", err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// polling the server status repeats it a timing dependent number of times
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(lines) == 0 || lines[len(lines)-1] != line {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestEnsureServerTimesOut(t *testing.T) {
	_, marker := fakeHerdr(t)
	// a server that never comes up
	t.Setenv("HERDR_RUNNING", marker+".never")
	bin := os.Getenv("HERDR_BIN_PATH")
	script, _ := os.ReadFile(bin)
	script = []byte(strings.Replace(string(script), "server) touch", "server) true", 1))
	if err := os.WriteFile(bin, script, 0o755); err != nil {
		t.Fatal(err)
	}
	old := serverStartTimeout
	serverStartTimeout = 200 * time.Millisecond
	t.Cleanup(func() { serverStartTimeout = old })
	if err := EnsureServer(); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err = %v, want a start timeout", err)
	}
}

func TestServerStatusProbesAreBounded(t *testing.T) {
	fakeHerdr(t)
	// a server that accepts but never answers
	bin := os.Getenv("HERDR_BIN_PATH")
	script, _ := os.ReadFile(bin)
	script = []byte(strings.Replace(string(script), "'status server --json') ", "'status server --json') sleep 30; ", 1))
	if err := os.WriteFile(bin, script, 0o755); err != nil {
		t.Fatal(err)
	}
	oldStatus, oldStart := serverStatusTimeout, serverStartTimeout
	serverStatusTimeout, serverStartTimeout = 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { serverStatusTimeout, serverStartTimeout = oldStatus, oldStart })

	start := time.Now()
	if err := EnsureServer(); err == nil || !strings.Contains(err.Error(), "no answer in time") {
		t.Fatalf("err = %v, want a status timeout", err)
	}
	if _, err := ServerRunning(); err == nil {
		t.Fatal("ServerRunning should time out")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("status probes took %s", elapsed)
	}
}

func TestEnsureServerPollsWithinStartDeadline(t *testing.T) {
	_, marker := fakeHerdr(t)
	// the started server comes up but never answers
	bin := os.Getenv("HERDR_BIN_PATH")
	script, _ := os.ReadFile(bin)
	script = []byte(strings.Replace(string(script), "'status server --json') ", "'status server --json') [ -e \"$HERDR_RUNNING\" ] && sleep 30; ", 1))
	if err := os.WriteFile(bin, script, 0o755); err != nil {
		t.Fatal(err)
	}
	old := serverStartTimeout
	serverStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { serverStartTimeout = old })

	start := time.Now()
	if err := EnsureServer(); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err = %v, want a start timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("EnsureServer took %s", elapsed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("server was not started: %v", err)
	}
}
