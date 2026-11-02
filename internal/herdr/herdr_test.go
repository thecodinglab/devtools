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

func TestInside(t *testing.T) {
	tests := []struct {
		name        string
		env, socket string
		want        bool
	}{
		{name: "outside", want: false},
		{name: "pane", env: "1", want: true},
		{name: "custom command", socket: "/tmp/herdr.sock", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_ENV", tt.env)
			t.Setenv("HERDR_SOCKET_PATH", tt.socket)
			if got := Inside(); got != tt.want {
				t.Fatalf("Inside() = %v, want %v", got, tt.want)
			}
		})
	}
}

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

func TestParsePaneList(t *testing.T) {
	got, err := parsePaneList([]byte(`{"id":"cli:pane:list","result":{"type":"pane_list","panes":[` +
		`{"pane_id":"w1:p1","tab_id":"w1:t1"},{"pane_id":"w1:p3","tab_id":"w1:t2"},{"pane_id":"w1:p2","tab_id":"w1:t1"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"w1:t1": {"w1:p1", "w1:p2"},
		"w1:t2": {"w1:p3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %#v, want %#v", got, want)
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

func TestParseMachineList(t *testing.T) {
	// the shape of herdr 0.9.5's MachineListRow, plus a field it may grow
	got, err := parseMachineList([]byte(`[
  {"id":"0123456789abcdef0123456789abcdef","label":"Build","target":"dev@build","session":"agents","enabled":true,"selected":false,"status":"reachable"},
  {"id":"fedcba","label":"workbox","target":"ssh://you@workbox:2222","session":"default","enabled":false,"selected":true}
]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Machine{
		{ID: "0123456789abcdef0123456789abcdef", Label: "Build", Target: "dev@build", Session: "agents", Enabled: true},
		{ID: "fedcba", Label: "workbox", Target: "ssh://you@workbox:2222", Session: "default", Selected: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("machines = %#v, want %#v", got, want)
	}
	if got, err := parseMachineList([]byte("[]\n")); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %#v, %v", got, err)
	}
	if !reflect.DeepEqual(want[0].RemoteArgs(), []string{"--remote", "dev@build", "--session", "agents"}) {
		t.Fatalf("remote args = %v", want[0].RemoteArgs())
	}
	// explicit, so an inherited HERDR_SESSION can't redirect the client
	if !reflect.DeepEqual(want[1].RemoteArgs(), []string{"--remote", "ssh://you@workbox:2222", "--session", "default"}) {
		t.Fatalf("remote args = %v", want[1].RemoteArgs())
	}
	if got := (Machine{Target: "box"}).RemoteArgs(); !reflect.DeepEqual(got, []string{"--remote", "box"}) {
		t.Fatalf("remote args without session = %v", got)
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

func TestClientPrefixesMachine(t *testing.T) {
	if got := (Client{}).args("agent", "list"); !reflect.DeepEqual(got, []string{"agent", "list"}) {
		t.Fatalf("local args = %v", got)
	}
	got := Client{Machine: "Build box"}.args("workspace", "focus", "w1")
	if want := []string{"--machine", "Build box", "workspace", "focus", "w1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("machine args = %v, want %v", got, want)
	}
}

// fakeHerdr logs its arguments and reports a server running once "herdr
// server" has run.
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
		"'machine list --json') echo '[{\"id\":\"m1\",\"label\":\"Build\",\"target\":\"dev@build\",\"session\":\"default\",\"enabled\":true,\"selected\":false},{\"id\":\"m2\",\"label\":\"Build\",\"target\":\"dev@other\",\"session\":\"default\",\"enabled\":true,\"selected\":false}]';;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("HERDR_LOG", log)
	t.Setenv("HERDR_RUNNING", marker)
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_SOCKET_PATH", "")
	return log, marker
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

func TestVisitOutsideHerdrStartsServerAndAttaches(t *testing.T) {
	log, _ := fakeHerdr(t)
	focused := false
	if err := Visit(func() error { focused = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !focused {
		t.Fatal("focus did not run")
	}
	want := []string{"status server --json", "server", "status server --json", "<attach>"}
	if got := readLines(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("herdr calls = %q, want %q", got, want)
	}
}

func TestVisitOutsideHerdrReusesRunningServer(t *testing.T) {
	log, marker := fakeHerdr(t)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Visit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"status server --json", "<attach>"}
	if got := readLines(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("herdr calls = %q, want %q", got, want)
	}
}

func TestVisitInsideHerdrOnlyFocuses(t *testing.T) {
	log, _ := fakeHerdr(t)
	t.Setenv("HERDR_ENV", "1")
	if err := Visit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if out, _ := os.ReadFile(log); len(out) != 0 {
		t.Fatalf("herdr calls = %q, want none", out)
	}
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

func TestFindMachine(t *testing.T) {
	fakeHerdr(t)
	if machine, err := FindMachine("m2"); err != nil || machine.Target != "dev@other" {
		t.Fatalf("FindMachine(m2) = %#v, %v", machine, err)
	}
	if _, err := FindMachine("Build"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err = %v, want ambiguous", err)
	}
	if _, err := FindMachine("nope"); err == nil {
		t.Fatal("want an error for an unknown machine")
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
