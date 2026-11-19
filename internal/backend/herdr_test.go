package backend

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/session"
)

func openHerdr(t *testing.T, c mux.Context) *HerdrMultiplexer {
	t.Helper()
	m, err := Open(mux.Herdr, c, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	return m.(*HerdrMultiplexer)
}

func wantHerdrCalls(t *testing.T, log string, want ...string) {
	t.Helper()
	if got := herdrCalls(t, log); !slices.Equal(got, want) {
		t.Fatalf("calls of the fake herdr =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestHerdrShowOutsideIsUnsupported(t *testing.T) {
	for _, c := range []mux.Context{inTerminal, {Where: mux.NoTerminal}, {Where: mux.InForeign, Foreign: "zellij"}} {
		t.Run("where="+whereName(c.Where), func(t *testing.T) {
			log := installFakeHerdr(t, "", "", true)
			_, err := openHerdr(t, c).Show("/work/proj/feat", "proj-feat")
			var u *mux.ErrUnsupported
			if !errors.As(err, &u) || u.Backend != mux.Herdr || u.Capability != mux.CapDetached {
				t.Fatalf("Show outside herdr = %v, want *ErrUnsupported{herdr, detached-open}", err)
			}
			wantEmpty(t, log)
		})
	}
}

func TestHerdrShowInsideFocusesTheWorkspace(t *testing.T) {
	t.Run("an open workspace is focused", func(t *testing.T) {
		log := installFakeHerdr(t, workspace("w1", "proj-feat", false, "idle"), "", true)
		shown, err := openHerdr(t, inHerdr).Show("/work/proj/feat", "proj-feat")
		if err != nil || shown != mux.ShownInWindow {
			t.Fatalf("Show = %v, %v; want ShownInWindow", shown, err)
		}
		wantHerdrCalls(t, log, "workspace list", "workspace focus w1")
	})
	t.Run("a missing workspace is created in dir and focused", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		if _, err := openHerdr(t, inHerdr).Show("/work/proj/feat", "proj-feat"); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "workspace list", "workspace create --cwd /work/proj/feat --label proj-feat --focus")
	})
}

func TestHerdrAgentsAreSorted(t *testing.T) {
	log := installFakeHerdr(t, "", joinObjects(
		agentObject("w1:p1", "w1", "codex", "working"),
		agentObject("w2:p1", "w2", "claude", "idle"),
		agentObject("w1:p2", "w1", "claude", "blocked"),
		agentObject("w3:p1", "w3", "claude", "done"),
		agentObject("w3:p2", "w3", "codex", "working"),
	), true)
	items, err := openHerdr(t, inHerdr).Agents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Pane+" "+it.Status)
		if it.Kind != hub.KindAgent || it.Machine != "" || it.Agent == "" || it.SessionID == "" {
			t.Errorf("agent item = %+v, want an agent of this machine with its kind and session", it)
		}
	}
	want := []string{"w1:p2 blocked", "w3:p1 done", "w2:p1 idle", "w1:p1 working", "w3:p2 working"}
	if !slices.Equal(got, want) {
		t.Fatalf("Agents order = %v, want %v (blocked, done, idle, working; herdr's order within a status)", got, want)
	}
	wantHerdrCalls(t, log, "agent list")
}

func TestHerdrAgentsOutsideHerdrStartNoServer(t *testing.T) {
	log := installFakeHerdr(t, "", agentObject("w1:p1", "w1", "claude", "idle"), false)
	m := openHerdr(t, inTerminal)
	items, err := m.Agents(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatalf("Agents without a server = %v, %v; want nothing", items, err)
	}
	sessions, err := m.Sessions(t.Context())
	if err != nil || len(sessions) != 0 {
		t.Fatalf("Sessions without a server = %v, %v; want nothing", sessions, err)
	}
	wantHerdrCalls(t, log, "status server --json")
}

func TestHerdrSessionsAreTheWorkspaces(t *testing.T) {
	log := installFakeHerdr(t, joinObjects(workspace("w1", "proj-main", true, "blocked"), workspace("w2", "notes", false, "unknown")), "", true)
	items, err := openHerdr(t, inHerdr).Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SessionID != "w1" || items[0].Session != "proj-main" || !items[0].State.Focused ||
		items[0].State.Agent != "blocked" || items[1].SessionID != "w2" || items[1].Kind != hub.KindSession {
		t.Fatalf("Sessions = %+v", items)
	}
	wantHerdrCalls(t, log, "workspace list")
}

func TestHerdrHubJumpsToAnAgentPick(t *testing.T) {
	t.Run("an agent pick focuses its pane", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		seen := pickWith(t, &hub.Item{Kind: hub.KindAgent, Pane: "w1:p2", SessionID: "w1"})
		if err := openHerdr(t, inHerdr).Hub(hub.ModeAgents, ""); err != nil {
			t.Fatal(err)
		}
		if len(*seen) != 1 || (*seen)[0].Mode != hub.ModeAgents {
			t.Fatalf("hub options = %+v", *seen)
		}
		if f := (*seen)[0].Features; !f.Agents || f.Machines {
			t.Errorf("the herdr hub has features %+v, want agents and no machines", f)
		}
		wantHerdrCalls(t, log, "agent focus w1:p2")
	})
	t.Run("an open session pick focuses its workspace by id", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		pickWith(t, &hub.Item{Kind: hub.KindSession, Session: "notes", SessionID: "w7"})
		if err := openHerdr(t, inHerdr).Hub(hub.ModeSessions, ""); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "workspace focus w7")
	})
	t.Run("a worktree pick is shown", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		pickWith(t, &hub.Item{Kind: hub.KindWorktree, Path: "/work/proj/feat", Session: "proj-feat"})
		if err := openHerdr(t, inHerdr).Hub(hub.ModeAll, ""); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "workspace list", "workspace create --cwd /work/proj/feat --label proj-feat --focus")
	})
	t.Run("a cancelled hub does nothing", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		pickWith(t, nil)
		if err := openHerdr(t, inHerdr).Hub(hub.ModeAll, ""); err != nil {
			t.Fatal(err)
		}
		wantEmpty(t, log)
	})
}

func TestHerdrEnterWithAHubShowsThePickThenAttaches(t *testing.T) {
	hubEntry := mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAll}}
	t.Run("a picked worktree: the hub first, then the server, the focus and the client", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", false)
		seen := pickWith(t, &hub.Item{Kind: hub.KindWorktree, Path: "/work/proj/feat", Session: "proj-feat"})
		if err := openHerdr(t, inTerminal).Enter(realPass(t), hubEntry); err != nil {
			t.Fatal(err)
		}
		if len(*seen) != 1 || !(*seen)[0].Features.Agents || (*seen)[0].Features.Machines {
			t.Fatalf("hub options = %+v, want one hub with agents", *seen)
		}
		wantHerdrCalls(t, log,
			"status server --json",
			"server",
			"status server --json",
			"workspace list",
			"workspace create --cwd /work/proj/feat --label proj-feat --focus",
			"<attach>",
		)
	})
	t.Run("a picked agent is focused before the client starts", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		pickWith(t, &hub.Item{Kind: hub.KindAgent, Pane: "w2:p1"})
		if err := openHerdr(t, inTerminal).Enter(realPass(t), hubEntry); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "status server --json", "agent focus w2:p1", "<attach>")
	})
	t.Run("a cancelled pick starts no server and no client", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", false)
		pickWith(t, nil)
		if err := openHerdr(t, inTerminal).Enter(realPass(t), hubEntry); err != nil {
			t.Fatal(err)
		}
		wantEmpty(t, log)
	})
	t.Run("a target: the server, workspace create, the client", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		noHub(t)
		target := &session.Target{Dir: "/work/proj/feat", Name: "proj-feat"}
		if err := openHerdr(t, inTerminal).Enter(realPass(t), mux.Entry{Target: target}); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log,
			"status server --json",
			"workspace list",
			"workspace create --cwd /work/proj/feat --label proj-feat --focus",
			"<attach>",
		)
	})
	t.Run("a target with an open workspace: the server, workspace focus, the client", func(t *testing.T) {
		log := installFakeHerdr(t, workspace("w3", "proj-feat", false, "idle"), "", true)
		noHub(t)
		target := &session.Target{Dir: "/work/proj/feat", Name: "proj-feat"}
		if err := openHerdr(t, inTerminal).Enter(realPass(t), mux.Entry{Target: target}); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "status server --json", "workspace list", "workspace focus w3", "<attach>")
	})
	t.Run("neither: the server and the client, no focus", func(t *testing.T) {
		log := installFakeHerdr(t, "", "", true)
		noHub(t)
		if err := openHerdr(t, inTerminal).Enter(realPass(t), mux.Entry{}); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "status server --json", "<attach>")
	})
}

func TestHerdrLeaveRemovesTheWorkspace(t *testing.T) {
	ws := joinObjects(workspace("w1", "proj-main", false, "idle"), workspace("w2", "proj-feat", true, "idle"))
	t.Run("inside, the fallback is focused and the close comes last", func(t *testing.T) {
		log := installFakeHerdr(t, ws, "", true)
		closeLast, err := openHerdr(t, inHerdr).Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "workspace list", "workspace focus w1")
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "workspace list", "workspace focus w1", "workspace close w2")
	})
	t.Run("outside, nothing is focused; the context decides, not HERDR_ENV", func(t *testing.T) {
		log := installFakeHerdr(t, ws, "", true)
		t.Setenv("HERDR_ENV", "1")
		closeLast, err := openHerdr(t, inTerminal).Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "status server --json", "workspace list", "workspace close w2")
	})
	t.Run("outside without a server nothing is asked or started", func(t *testing.T) {
		log := installFakeHerdr(t, ws, "", false)
		closeLast, err := openHerdr(t, inTerminal).Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		wantHerdrCalls(t, log, "status server --json")
	})
}

func TestHerdrAvailableIsTheProgramOfHerdrBinPath(t *testing.T) {
	if err := Available(mux.Herdr); err == nil {
		t.Fatal("herdr is available without a herdr")
	}
	log := installFakeHerdr(t, "", "", true)
	if err := Available(mux.Herdr); err != nil {
		t.Fatalf("herdr is not available with HERDR_BIN_PATH set: %v", err)
	}
	wantEmpty(t, log)
}
