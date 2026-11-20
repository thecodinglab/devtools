package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/ui"
)

// unsupportedOutsideHerdr is what a command that shows a session prints
// outside herdr without a terminal: herdr cannot open a workspace that no
// window shows (it lacks detached-open), and no client is started without
// a terminal.
var unsupportedOutsideHerdr = ui.Message("unsupported", "herdr", "detached-open")

// Was: the switch started the server and attached ("server", "workspace
// create" and "<attach>" in the record). Without a terminal no client is
// started (3.4): the unsupported words, status 1, and nothing run.
func TestSwitchOutsideHerdrStartsServerAndAttaches(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)
	log, _ := installFakeHerdrOutside(t, `{"label":"other","workspace_id":"w1"}`)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != unsupportedOutsideHerdr {
		t.Fatalf("stderr = %q, want %q", got, unsupportedOutsideHerdr)
	}
	if got := herdrCalls(t, log); strings.Contains(got, "<attach>") || got != "" {
		t.Fatalf("herdr args = %q, want none and no <attach>", got)
	}
}

// Was: "workspace focus w2" then "<attach>". As above.
func TestSwitchOutsideHerdrReusesRunningServer(t *testing.T) {
	root := t.TempDir()
	makeRepo(t, filepath.Join(root, "small"))
	log, marker := installFakeHerdrOutside(t, `{"label":"small-main","workspace_id":"w2"}`)
	clearFile(t, marker)
	t.Setenv("DEVTOOLS_ROOT", root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != unsupportedOutsideHerdr {
		t.Fatalf("stderr = %q, want %q", got, unsupportedOutsideHerdr)
	}
	if got := readLog(t, log); strings.Contains(got, "<attach>") || got != "" {
		t.Fatalf("herdr args = %q, want none and no <attach>", got)
	}
}

func TestMultiplexerOverrides(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)
	t.Setenv("DEVTOOLS_ROOT", root)

	// Was: tmux ran "new-session -A". Inside a multiplexer the environment
	// cannot choose another one (3.4 i).
	t.Run("env picks tmux inside herdr", func(t *testing.T) {
		herdrLog := installFakeHerdr(t, "")
		tmuxLog := installFakeTmux(t)
		t.Setenv("DEVTOOLS_MULTIPLEXER", "tmux")
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 1 {
			t.Fatalf("Run returned %d, want 1; stderr=%s", code, stderr.String())
		}
		if got, want := strings.TrimSpace(stderr.String()), ui.Message("mismatch", "tmux", "herdr"); got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
		if got := readLog(t, herdrLog); got != "" {
			t.Fatalf("herdr args = %q, want none", got)
		}
		if got := readLog(t, tmuxLog); got != "" {
			t.Fatalf("tmux args = %q, want none", got)
		}
	})

	// Was: herdr focused the workspace. Both marks are live and no terminal
	// matches the tmux pane, so the command does nothing.
	t.Run("config picks herdr inside tmux", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(cfgPath, []byte("multiplexer herdr\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DEVTOOLS_CONFIG", cfgPath)
		herdrLog := installFakeHerdr(t, `{"label":"small-main","workspace_id":"w2"}`)
		tmuxLog := installFakeTmux(t)
		insideFakeTmux(t)
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 1 {
			t.Fatalf("Run returned %d, want 1; stderr=%s", code, stderr.String())
		}
		if got, want := strings.TrimSpace(stderr.String()), ui.Message("ambiguous", "tmux", "herdr"); got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
		if got := readLog(t, herdrLog); got != "" {
			t.Fatalf("herdr args = %q, want none", got)
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

// The clone and work steps were: status 0, each showing its session. Outside
// herdr without a terminal they do their git work and then end with the
// unsupported words and status 1; the done step is as before.
func TestRemoveOutsideHerdrClosesWorkspaceOnly(t *testing.T) {
	root := t.TempDir()
	remote := createRemote(t)
	log, marker := installFakeHerdrOutside(t, `{"label":"widgets-main","workspace_id":"w1"},{"label":"widgets-feature-test","workspace_id":"w2"}`)
	clearFile(t, marker)
	t.Setenv("DEVTOOLS_ROOT", root)
	var stdout, stderr bytes.Buffer

	if code := Run([]string{"clone", remote, "widgets"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), unsupportedOutsideHerdr) {
		t.Fatalf("clone returned %d, want 1 with the unsupported words; stderr=%s", code, stderr.String())
	}
	changeCwd(t, filepath.Join(root, "widgets", "main"))
	stderr.Reset()
	if code := Run([]string{"work", "feature/test"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), unsupportedOutsideHerdr) {
		t.Fatalf("work returned %d, want 1 with the unsupported words; stderr=%s", code, stderr.String())
	}
	featurePath := filepath.Join(root, "widgets", "feature-test")
	if _, err := os.Stat(featurePath); err != nil {
		t.Fatalf("work did not do its git work: %v", err)
	}
	if got := readLog(t, log); strings.Contains(got, "<attach>") {
		t.Fatalf("herdr args = %q, want no <attach>", got)
	}
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

// noFZF fails the test when the fake fzf of installFakeFZF ran: the agents
// picker of fzf is gone, devtools agents opens the hub (P2-08b).
func noFZF(t *testing.T, fzfLog string) {
	t.Helper()
	if _, err := os.Stat(fzfLog); err == nil {
		t.Fatalf("fzf ran with input %q", readLog(t, fzfLog))
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// Rewritten by P2-08b (revision 2). It asserted that devtools agents inside
// herdr listed the agents into fzf, the ones needing attention first, and
// focused the pick with agent focus. Since P2-08b agents goes through
// enter on every backend: inside the backend's pane that is Hub of the
// backend in the agents scope, once, and nothing else; fzf does not run.
// The order of the agents is the hub's (internal/hub), the focus of the
// pick is the backend's Jump (internal/backend).
func TestAgentsPicksAttentionFirstAndFocuses(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("HERDR_AGENTS", testAgents)
	fzfLog := installFakeFZF(t, "devtools-main  claude reviewer  done     Review\tw1:p2")
	r := &recordingMux{name: mux.Herdr}
	useRecording(t, r)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	if !slices.Equal(r.opened, []mux.Backend{mux.Herdr}) || r.ctx.Where != mux.InHerdr {
		t.Fatalf("opened %v in %+v, want herdr inside its pane", r.opened, r.ctx)
	}
	wantCalls(t, r, fmt.Sprintf("Hub %d %q", hub.ModeAgents, ""))
	noFZF(t, fzfLog)
	if got := readLog(t, log); got != "" {
		t.Fatalf("herdr args = %q, want none outside the backend", got)
	}
}

// Rewritten by P2-08b (revision 2). It asserted that devtools agents
// outside herdr, its server running, picked with fzf, focused the pick and
// then attached. Since P2-08b, in a plain terminal the guard passes and
// Enter is called once with the hub in the agents scope; the client opens
// with the hub, and herdr's Enter shows the pick before it attaches
// (TestHerdrEnterWithAHubShowsThePickThenAttaches, internal/backend).
// fzf does not run, and nothing of herdr runs before the client.
func TestAgentsOutsideHerdrAttachesAfterFocus(t *testing.T) {
	log, marker := installFakeHerdrOutside(t, testWorkspaces)
	clearFile(t, marker)
	t.Setenv("HERDR_AGENTS", testAgents)
	fzfLog := installFakeFZF(t, "ops            codex            blocked  Approve rm?\tw2:p3")
	scriptedTerminal(t, ordinaryTerminal)
	r := &recordingMux{name: mux.Herdr}
	useRecording(t, r)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	if !slices.Equal(r.opened, []mux.Backend{mux.Herdr}) || r.ctx.Where != mux.InTerminal {
		t.Fatalf("opened %v in %+v, want herdr in a plain terminal", r.opened, r.ctx)
	}
	wantCalls(t, r, fmt.Sprintf("Enter valid=true hub %d %q", hub.ModeAgents, ""))
	noFZF(t, fzfLog)
	if got := readLog(t, log); got != "" {
		t.Fatalf("herdr args = %q, want none before the client", got)
	}
}

// Rewritten by P2-08b onto the recording multiplexer. It asserted that the
// legacy hub, typed outside herdr, started the herdr server before it
// listed the workspaces and then attached. Since P2-08b sessions goes
// through enter on every backend: in a plain terminal the guard passes and
// Enter is called once with the hub in the sessions scope, and nothing of
// herdr runs before the client (HerdrMultiplexer.Enter runs the hub first,
// then EnsureServer: TestHerdrEnterWithAHubShowsThePickThenAttaches).
func TestSessionsOutsideHerdrStartsServerBeforeListing(t *testing.T) {
	log, _ := installFakeHerdrOutside(t, testWorkspaces)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	scriptedTerminal(t, ordinaryTerminal)
	r := &recordingMux{name: mux.Herdr}
	useRecording(t, r)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sessions"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sessions returned %d, stderr=%s", code, stderr.String())
	}
	if !slices.Equal(r.opened, []mux.Backend{mux.Herdr}) || r.ctx.Where != mux.InTerminal {
		t.Fatalf("opened %v in %+v, want herdr in a plain terminal", r.opened, r.ctx)
	}
	wantCalls(t, r, fmt.Sprintf("Enter valid=true hub %d %q", hub.ModeSessions, ""))
	if got := readLog(t, log); got != "" {
		t.Fatalf("herdr args = %q, want none before the client", got)
	}
}

// Rewritten by P2-08b (revision 2). It asserted that devtools agents
// outside herdr, its server not running, started the server before it
// listed the agents into fzf, then focused the pick and attached. Since
// P2-08b, in a plain terminal the guard passes and Enter is called once
// with the hub in the agents scope; the server is started by herdr's Enter
// after the hub (EnsureServer, TestHerdrEnterWithAHubShowsThePickThenAttaches
// in internal/backend), so nothing of herdr runs before the client, the
// server included, and fzf does not run.
func TestAgentsOutsideHerdrStartsServerBeforeListing(t *testing.T) {
	log, marker := installFakeHerdrOutside(t, testWorkspaces)
	t.Setenv("HERDR_AGENTS", testAgents)
	fzfLog := installFakeFZF(t, "ops            codex            blocked  Approve rm?\tw2:p3")
	scriptedTerminal(t, ordinaryTerminal)
	r := &recordingMux{name: mux.Herdr}
	useRecording(t, r)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"agents"}, &stdout, &stderr); code != 0 {
		t.Fatalf("agents returned %d, stderr=%s", code, stderr.String())
	}
	if !slices.Equal(r.opened, []mux.Backend{mux.Herdr}) || r.ctx.Where != mux.InTerminal {
		t.Fatalf("opened %v in %+v, want herdr in a plain terminal", r.opened, r.ctx)
	}
	wantCalls(t, r, fmt.Sprintf("Enter valid=true hub %d %q", hub.ModeAgents, ""))
	noFZF(t, fzfLog)
	if got := readLog(t, log); got != "" {
		t.Fatalf("herdr args = %q, want none before the client", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the herdr server was started before the client (marker: %v)", err)
	}
}
