package cli

import (
	"bytes"
	"devtools/internal/hub"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSwitchOutsideHerdrStartsServerAndAttaches(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)
	log, _ := installFakeHerdrOutside(t, `{"label":"other","workspace_id":"w1"}`)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	got := herdrCalls(t, log)
	want := "status server --json\nserver\nstatus server --json\n" +
		"workspace list\nworkspace create --cwd " + project + " --label small-main --focus\n<attach>"
	if got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestSwitchOutsideHerdrReusesRunningServer(t *testing.T) {
	root := t.TempDir()
	makeRepo(t, filepath.Join(root, "small"))
	log, marker := installFakeHerdrOutside(t, `{"label":"small-main","workspace_id":"w2"}`)
	clearFile(t, marker)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
	}
	want := "status server --json\nworkspace list\nworkspace focus w2\n<attach>"
	if got := readLog(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestMultiplexerOverrides(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)
	t.Setenv("DEVTOOLS_ROOT", root)

	t.Run("env picks tmux inside herdr", func(t *testing.T) {
		herdrLog := installFakeHerdr(t, "")
		tmuxLog := installFakeTmux(t)
		t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
			t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
		}
		if got := readLog(t, herdrLog); got != "" {
			t.Fatalf("herdr args = %q, want none", got)
		}
		if got := readLog(t, tmuxLog); got != "new-session -A -s small-main -c "+project {
			t.Fatalf("tmux args = %q", got)
		}
	})

	t.Run("config picks herdr inside tmux", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(cfgPath, []byte("multiplexer herdr\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DEVTOOLS_CONFIG", cfgPath)
		herdrLog := installFakeHerdr(t, `{"label":"small-main","workspace_id":"w2"}`)
		tmuxLog := installFakeTmux(t)
		t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
			t.Fatalf("Run returned %d, stderr=%s", code, stderr.String())
		}
		if got := readLog(t, herdrLog); got != "workspace list\nworkspace focus w2" {
			t.Fatalf("herdr args = %q", got)
		}
		if out, _ := os.ReadFile(tmuxLog); len(out) != 0 {
			t.Fatalf("tmux args = %q, want none", out)
		}
	})

	t.Run("invalid env", func(t *testing.T) {
		t.Setenv("DEVTOOLS_MULTIPLEXER", "screen")
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"switch", "small"}, &stdout, &stderr); code == 0 {
			t.Fatal("switch should fail")
		}
		if !strings.Contains(stderr.String(), "unknown multiplexer") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})
}

func TestRemoveOutsideHerdrClosesWorkspaceOnly(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log, marker := installFakeHerdrOutside(t, `{"label":"widgets-main","workspace_id":"w1"},{"label":"widgets-feature-test","workspace_id":"w2"}`)
	clearFile(t, marker)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	changeCwd(t, filepath.Join(root, "widgets", "main"))
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	changeCwd(t, featurePath)
	clearFile(t, log)

	if code := Run([]string{"done", "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("done returned %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(featurePath); !os.IsNotExist(err) {
		t.Fatalf("expected feature worktree to be removed, err=%v", err)
	}
	want := "status server --json\nworkspace list\nworkspace close w2"
	if got := readLog(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestRemoveOutsideHerdrWithoutServerTouchesNothing(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 0 {
		t.Fatalf("clone returned %d, stderr=%s", code, stderr.String())
	}
	changeCwd(t, filepath.Join(root, "widgets", "main"))
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("work returned %d, stderr=%s", code, stderr.String())
	}
	changeCwd(t, filepath.Join(root, "widgets", "feature-test"))
	log, _ := installFakeHerdrOutside(t, "")

	if code := Run([]string{"done", "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("done returned %d, stderr=%s", code, stderr.String())
	}
	if got := readLog(t, log); got != "status server --json" {
		t.Fatalf("herdr args = %q, want only the status check", got)
	}
}

const testAgents = `{"agent":"claude","agent_status":"working","pane_id":"w1:p1","workspace_id":"w1","terminal_title_stripped":"Refactor config"},` +
	`{"agent":"codex","agent_status":"blocked","pane_id":"w2:p3","workspace_id":"w2","terminal_title_stripped":"Approve rm?"},` +
	`{"agent":"claude","agent_status":"done","pane_id":"w1:p2","workspace_id":"w1","name":"reviewer","terminal_title_stripped":"Review"}`

const testWorkspaces = `{"label":"devtools-main","workspace_id":"w1","tab_count":2},{"label":"ops","workspace_id":"w2","tab_count":1}`

func TestAgentsPicksAttentionFirstAndFocuses(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("HERDR_AGENTS", testAgents)
	fzfLog := installFakeFZF(t, "devtools-main  claude reviewer  done     Review\tw1:p2")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	wantInput := "ops            codex            blocked  Approve rm?\tw2:p3\n" +
		"devtools-main  claude reviewer  done     Review\tw1:p2\n" +
		"devtools-main  claude           working  Refactor config\tw1:p1"
	if got := readLog(t, fzfLog); got != wantInput {
		t.Fatalf("fzf input = %q, want %q", got, wantInput)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := fmt.Sprintf("--with-nth=1 --delimiter=\t --preview %q agent-preview {2}", exe)
	if got := readLog(t, fzfLog+".args"); got != wantArgs {
		t.Fatalf("fzf args = %q, want %q", got, wantArgs)
	}
	if got := readLog(t, log); got != "agent list\nworkspace list\nagent focus w1:p2" {
		t.Fatalf("herdr args = %q", got)
	}
}

func TestAgentsOutsideHerdrAttachesAfterFocus(t *testing.T) {
	log, marker := installFakeHerdrOutside(t, testWorkspaces)
	clearFile(t, marker)
	t.Setenv("HERDR_AGENTS", testAgents)
	installFakeFZF(t, "ops            codex            blocked  Approve rm?\tw2:p3")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	want := "status server --json\nagent list\nworkspace list\nstatus server --json\nagent focus w2:p3\n<attach>"
	if got := readLog(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

const testMachines = `{"id":"m1","label":"Build","target":"dev@build","session":"agents","enabled":true,"selected":false},` +
	`{"id":"m2","label":"workbox","target":"workbox","session":"default","enabled":false,"selected":true}`

func TestAgentsOnMachineInsideHerdrPointsAtSidebar(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("HERDR_AGENTS", testAgents)
	t.Setenv("HERDR_MACHINES", testMachines)
	fzfLog := installFakeFZF(t, "devtools-main  claude reviewer  done     Review\tw1:p2")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents", "--machine", "Build"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	want := "machine list --json\n--machine m1 agent list\n--machine m1 workspace list\n--machine m1 agent focus w1:p2"
	if got := readLog(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := fmt.Sprintf("--with-nth=1 --delimiter=\t --preview %q agent-preview --machine m1 {2}", exe)
	if got := readLog(t, fzfLog+".args"); got != wantArgs {
		t.Fatalf("fzf args = %q, want %q", got, wantArgs)
	}
	if got := strings.TrimSpace(stdout.String()); got != "select machine Build in the herdr sidebar to see it" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestAgentPreviewTailsAgentScreen(t *testing.T) {
	log := installFakeHerdr(t, "")
	t.Setenv("HERDR_PANE_LINES", "10")
	t.Setenv("FZF_PREVIEW_LINES", "3")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agent-preview", "--machine", "Build box", "w1:p2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agent-preview returned %d, stderr=%s", code, stderr.String())
	}
	if want := "w1:p2 line 8\nw1:p2 line 9\nw1:p2 line 10\n"; stdout.String() != want {
		t.Fatalf("preview = %q, want %q", stdout.String(), want)
	}
	if got := readLog(t, log); got != "--machine Build box agent read w1:p2 --source visible --format ansi" {
		t.Fatalf("herdr args = %q", got)
	}
}

func TestMachinesPicksMachineThenWorkspaceAndAttachesRemote(t *testing.T) {
	log, _ := installFakeHerdrOutside(t, testWorkspaces)
	t.Setenv("HERDR_MACHINES", testMachines)
	fzfLog := installFakeFZFSequence(t, "Build    dev@build  enabled\tm1", "ops\t1 tabs\tw2")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"machines"}, &stdout, &stderr); code != 0 {
		t.Fatalf("machines returned %d, stderr=%s", code, stderr.String())
	}
	wantInput := "Build    dev@build  enabled\tm1\nworkbox  workbox    disabled, selected\tm2"
	if got := readLog(t, fzfLog+".1"); got != wantInput {
		t.Fatalf("machine fzf input = %q, want %q", got, wantInput)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readLog(t, fzfLog+".1.args"), fmt.Sprintf("--with-nth=1 --delimiter=\t --preview %q machine-preview {2}", exe); got != want {
		t.Fatalf("machine fzf args = %q, want %q", got, want)
	}
	if got, want := readLog(t, fzfLog+".2.args"), fmt.Sprintf("--with-nth=1 --delimiter=\t --preview %q session-preview --machine m1 {3}", exe); got != want {
		t.Fatalf("workspace fzf args = %q, want %q", got, want)
	}
	want := "machine list --json\nmachine list --json\n--machine m1 workspace list\n--machine m1 workspace focus w2\n--remote dev@build --session agents"
	if got := readLog(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestMachinesWithoutSavedMachines(t *testing.T) {
	installFakeHerdr(t, "")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"machines"}, &stdout, &stderr); code == 0 {
		t.Fatal("machines should fail without saved machines")
	}
	if !strings.Contains(stderr.String(), "no saved herdr machines") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestMachinePreviewListsRemoteWorkspaces(t *testing.T) {
	log := installFakeHerdr(t, `{"label":"api","workspace_id":"w1","tab_count":2,"agent_status":"working"},{"label":"ops","workspace_id":"w2","tab_count":1,"agent_status":"idle"}`)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"machine-preview", "m1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("machine-preview returned %d, stderr=%s", code, stderr.String())
	}
	if want := "api  2 tabs  working\nops  1 tabs  idle\n"; stdout.String() != want {
		t.Fatalf("preview = %q, want %q", stdout.String(), want)
	}
	if got := readLog(t, log); got != "--machine m1 workspace list" {
		t.Fatalf("herdr args = %q", got)
	}
}

func TestSessionsOnMachine(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("HERDR_MACHINES", testMachines)
	installFakeFZF(t, "ops\t1 tabs\tw2")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sessions", "--machine", "m1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sessions returned %d, stderr=%s", code, stderr.String())
	}
	want := "machine list --json\n--machine m1 workspace list\n--machine m1 workspace focus w2"
	if got := readLog(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestSessionsMachineNeedsHerdr(t *testing.T) {
	installFakeHerdr(t, "")
	installFakeTmux(t)
	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sessions", "--machine", "m1"}, &stdout, &stderr); code == 0 {
		t.Fatal("sessions --machine should fail with tmux")
	}
	if !strings.Contains(stderr.String(), "--machine needs the herdr multiplexer") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSessionPreviewOnMachine(t *testing.T) {
	log := installFakeHerdr(t, "")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"session-preview", "--machine", "m1", "w2"}, &stdout, &stderr); code != 0 {
		t.Fatalf("session-preview returned %d, stderr=%s", code, stderr.String())
	}
	if got := readLog(t, log); got != "--machine m1 tab list --workspace w2\n--machine m1 pane list --workspace w2" {
		t.Fatalf("herdr args = %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"session-preview": "session-preview",
		"{3}":             "{3}",
		"Build box":       "'Build box'",
		"it's":            `'it'\''s'`,
	}
	for in, want := range tests {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// installFakeFZF that answers its nth run with the nth selection, logging
// each run's input to <log>.n and its arguments to <log>.n.args.
func installFakeFZFSequence(t *testing.T, selections ...string) string {
	t.Helper()
	bin := t.TempDir()
	dir := t.TempDir()
	log := filepath.Join(dir, "fzf.log")
	for i, selection := range selections {
		if err := os.WriteFile(fmt.Sprintf("%s.sel.%d", log, i+1), []byte(selection+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nn=$(($(cat \"$FZF_LOG.count\" 2>/dev/null || echo 0)+1))\necho $n > \"$FZF_LOG.count\"\n" +
		"printf '%s\\n' \"$*\" > \"$FZF_LOG.$n.args\"\ncat > \"$FZF_LOG.$n\"\ncat \"$FZF_LOG.sel.$n\"\n"
	if err := os.WriteFile(filepath.Join(bin, "fzf"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FZF_LOG", log)
	return log
}

func TestSessionsOutsideHerdrStartsServerBeforeListing(t *testing.T) {
	log, _ := installFakeHerdrOutside(t, testWorkspaces)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		if opts.SwitchInPlace {
			t.Fatal("outside herdr the switcher can't switch in place, attaching takes the terminal")
		}
		_, others, err := opts.Backend.Sessions()
		if err != nil || len(others) != 2 {
			t.Fatalf("others = %+v, err = %v", others, err)
		}
		return &others[1], nil
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sessions"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sessions returned %d, stderr=%s", code, stderr.String())
	}
	want := "status server --json\nserver\nstatus server --json\nworkspace list\nagent list\nstatus server --json\nworkspace focus w2\n<attach>"
	if got := herdrCalls(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}

func TestSwitcherShowsEachWorkspacesMostUrgentAgent(t *testing.T) {
	installFakeHerdr(t, testWorkspaces)
	t.Setenv("HERDR_AGENTS", testAgents)
	sessions, _, err := hubBackend{backend: "herdr"}.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions["ops"]; got.Agent != "blocked" || got.Title != "Approve rm?" {
		t.Fatalf("ops = %+v, want its blocked agent", got)
	}
	// done outranks working
	if got := sessions["devtools-main"]; got.Agent != "done" || got.Title != "Review" {
		t.Fatalf("devtools-main = %+v, want the done reviewer", got)
	}
}

func TestAgentsOutsideHerdrStartsServerBeforeListing(t *testing.T) {
	log, _ := installFakeHerdrOutside(t, testWorkspaces)
	t.Setenv("HERDR_AGENTS", testAgents)
	installFakeFZF(t, "ops            codex            blocked  Approve rm?\tw2:p3")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	want := "status server --json\nserver\nstatus server --json\nagent list\nworkspace list\nstatus server --json\nagent focus w2:p3\n<attach>"
	if got := herdrCalls(t, log); got != want {
		t.Fatalf("herdr args = %q, want %q", got, want)
	}
}
