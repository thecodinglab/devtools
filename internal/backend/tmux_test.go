package backend

import (
	"errors"
	"os"
	"testing"

	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/session"
)

var (
	inTmux     = mux.Context{Where: mux.InTmux}
	inHerdr    = mux.Context{Where: mux.InHerdr}
	inTerminal = mux.Context{Where: mux.InTerminal}
)

func openTmux(t *testing.T, c mux.Context) *TmuxMultiplexer {
	t.Helper()
	m, err := Open(mux.Tmux, c, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	return m.(*TmuxMultiplexer)
}

// pickWith replaces the standalone hub for the test by a function that
// records the options it was given and returns pick (nil: the hub was
// closed without a choice). It reads no event of the feed.
func pickWith(t *testing.T, pick *hub.Item) *[]hub.ModelOptions {
	t.Helper()
	var seen []hub.ModelOptions
	old := runStandalone
	runStandalone = func(o hub.ModelOptions, in, out *os.File) (*hub.Item, error) {
		seen = append(seen, o)
		if pick == nil {
			return nil, nil
		}
		p := *pick
		return &p, nil
	}
	t.Cleanup(func() { runStandalone = old })
	return &seen
}

// noHub makes a call of the standalone hub fail the test.
func noHub(t *testing.T) {
	t.Helper()
	old := runStandalone
	runStandalone = func(o hub.ModelOptions, in, out *os.File) (*hub.Item, error) {
		t.Errorf("the standalone hub ran")
		return nil, nil
	}
	t.Cleanup(func() { runStandalone = old })
}

func TestTmuxShowInsideSwitchesTheClient(t *testing.T) {
	t.Run("a missing session is created detached, then the client switches", func(t *testing.T) {
		log := installFakeTmux(t)
		shown, err := openTmux(t, inTmux).Show("/work/proj/feat", "proj-feat")
		if err != nil {
			t.Fatal(err)
		}
		if shown != mux.ShownInWindow {
			t.Errorf("Show returned %v, want ShownInWindow", shown)
		}
		wantCalls(t, log,
			"has-session -t proj-feat",
			"new-session -d -s proj-feat -c /work/proj/feat",
			"switch-client -t proj-feat",
		)
	})
	t.Run("an existing session is only switched to", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-feat")
		shown, err := openTmux(t, inTmux).Show("/work/proj/feat", "proj-feat")
		if err != nil || shown != mux.ShownInWindow {
			t.Fatalf("Show = %v, %v; want ShownInWindow", shown, err)
		}
		wantCalls(t, log, "has-session -t proj-feat", "switch-client -t proj-feat")
	})
}

func TestTmuxShowOutsideCreatesDetached(t *testing.T) {
	for _, c := range []mux.Context{inTerminal, {Where: mux.NoTerminal}, {Where: mux.InForeign, Foreign: "screen"}} {
		t.Run("where="+whereName(c.Where), func(t *testing.T) {
			log := installFakeTmux(t)
			shown, err := openTmux(t, c).Show("/work/proj/feat", "proj-feat")
			if err != nil {
				t.Fatal(err)
			}
			if shown != mux.OpenedOnly {
				t.Errorf("Show returned %v, want OpenedOnly", shown)
			}
			wantCalls(t, log, "has-session -t proj-feat", "new-session -d -s proj-feat -c /work/proj/feat")
			wantNoCall(t, log, "new-session -A", "attach-session", "attach", "switch-client")
		})
	}
	t.Run("an existing session is left as it is", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-feat")
		shown, err := openTmux(t, inTerminal).Show("/work/proj/feat", "proj-feat")
		if err != nil || shown != mux.OpenedOnly {
			t.Fatalf("Show = %v, %v; want OpenedOnly", shown, err)
		}
		wantCalls(t, log, "has-session -t proj-feat")
	})
}

func whereName(w mux.Where) string {
	return [...]string{"devtools", "tmux", "herdr", "foreign", "ambiguous", "terminal", "no-terminal"}[w]
}

func TestTmuxLeaveClosesForRemoval(t *testing.T) {
	t.Run("inside, the client moves to the fallback, then the session ends", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-main", "proj-feat")
		closeLast, err := openTmux(t, inTmux).Leave("/work/proj/feat", "/work/proj/main", "proj-main")
		if err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log,
			"has-session -t proj-main",
			"switch-client -t proj-main",
			"has-session -t proj-feat",
			"kill-session -t proj-feat",
		)
		if err := closeLast(); err != nil {
			t.Fatal(err)
		}
		if n := len(calls(t, log)); n != 4 {
			t.Errorf("the returned function ran tmux; %d calls", n)
		}
	})
	t.Run("inside, a missing fallback is created detached first", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-feat")
		if _, err := openTmux(t, inTmux).Leave("/work/proj/feat", "/work/proj/main", "proj-main"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log,
			"has-session -t proj-main",
			"new-session -d -s proj-main -c /work/proj/main",
			"switch-client -t proj-main",
			"has-session -t proj-feat",
			"kill-session -t proj-feat",
		)
	})
	t.Run("inside, leaving the fallback itself detaches the client", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-main")
		if _, err := openTmux(t, inTmux).Leave("/work/proj/main", "/work/proj/main", "proj-main"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log, "detach-client", "has-session -t proj-main", "kill-session -t proj-main")
	})
	t.Run("outside, the session only ends", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-main", "proj-feat")
		if _, err := openTmux(t, inTerminal).Leave("/work/proj/feat", "/work/proj/main", "proj-main"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log, "has-session -t proj-feat", "kill-session -t proj-feat")
	})
	t.Run("the context decides, not the variable TMUX", func(t *testing.T) {
		log := installFakeTmux(t)
		tmuxHas(t, "proj-main", "proj-feat")
		t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
		t.Setenv("TMUX_PANE", "%3")
		if _, err := openTmux(t, inTerminal).Leave("/work/proj/feat", "/work/proj/main", "proj-main"); err != nil {
			t.Fatal(err)
		}
		wantNoCall(t, log, "switch-client", "detach-client")
		wantCalls(t, log, "has-session -t proj-feat", "kill-session -t proj-feat")
	})
	t.Run("outside, a session that does not exist is left alone", func(t *testing.T) {
		log := installFakeTmux(t)
		if _, err := openTmux(t, inTerminal).Leave("/work/proj/feat", "/work/proj/main", "proj-main"); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log, "has-session -t proj-feat")
	})
}

func TestTmuxEnterAttachesOnlyWithAPass(t *testing.T) {
	t.Run("without a pass nothing runs", func(t *testing.T) {
		log := installFakeTmux(t)
		noHub(t)
		target := &session.Target{Dir: "/work/proj/feat", Name: "proj-feat"}
		err := openTmux(t, inTerminal).Enter(nil, mux.Entry{Target: target})
		if !errors.Is(err, mux.ErrNoPass) {
			t.Fatalf("Enter without a pass = %v, want ErrNoPass", err)
		}
		wantEmpty(t, log)
	})
	t.Run("a target is attached to, created in its directory when missing", func(t *testing.T) {
		log := installFakeTmux(t)
		noHub(t)
		target := &session.Target{Dir: "/work/proj/feat", Name: "proj-feat"}
		if err := openTmux(t, inTerminal).Enter(realPass(t), mux.Entry{Target: target}); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log, "new-session -A -s proj-feat -c /work/proj/feat")
	})
	t.Run("without a target the last session is attached to", func(t *testing.T) {
		log := installFakeTmux(t)
		noHub(t)
		if err := openTmux(t, inTerminal).Enter(realPass(t), mux.Entry{}); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log, "attach-session")
	})
	t.Run("with a hub the pick is attached to after the hub ended", func(t *testing.T) {
		log := installFakeTmux(t)
		seen := pickWith(t, &hub.Item{Kind: hub.KindWorktree, Path: "/work/proj/feat", Session: "proj-feat"})
		err := openTmux(t, inTerminal).Enter(realPass(t), mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAll, Query: "feat"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(*seen) != 1 {
			t.Fatalf("the standalone hub ran %d times, want once", len(*seen))
		}
		if o := (*seen)[0]; o.Query != "feat" || o.Mode != hub.ModeAll || o.Features != (hub.Features{}) || o.Events == nil {
			t.Errorf("hub options = mode %v, query %q, features %+v, events %v", o.Mode, o.Query, o.Features, o.Events != nil)
		}
		wantCalls(t, log, "new-session -A -s proj-feat -c /work/proj/feat")
	})
	t.Run("a picked open session is attached to by its name", func(t *testing.T) {
		log := installFakeTmux(t)
		pickWith(t, &hub.Item{Kind: hub.KindSession, Session: "notes"})
		if err := openTmux(t, inTerminal).Enter(realPass(t), mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeSessions}}); err != nil {
			t.Fatal(err)
		}
		wantCalls(t, log, "attach-session -t notes")
	})
	t.Run("a cancelled hub starts no client", func(t *testing.T) {
		log := installFakeTmux(t)
		pickWith(t, nil)
		if err := openTmux(t, inTerminal).Enter(realPass(t), mux.Entry{Hub: &mux.HubRequest{}}); err != nil {
			t.Fatal(err)
		}
		wantEmpty(t, log)
	})
}

func TestTmuxHubShowsThePick(t *testing.T) {
	t.Run("the client switches to the pick", func(t *testing.T) {
		log := installFakeTmux(t)
		seen := pickWith(t, &hub.Item{Kind: hub.KindWorktree, Path: "/work/proj/feat", Session: "proj-feat"})
		if err := openTmux(t, inTmux).Hub(hub.ModeNew, "fix"); err != nil {
			t.Fatal(err)
		}
		if len(*seen) != 1 || (*seen)[0].Mode != hub.ModeNew || (*seen)[0].Query != "fix" {
			t.Fatalf("hub options = %+v", *seen)
		}
		if f := (*seen)[0].Features; f.Agents || f.Machines {
			t.Errorf("the tmux hub has features %+v, want none", f)
		}
		wantCalls(t, log,
			"has-session -t proj-feat",
			"new-session -d -s proj-feat -c /work/proj/feat",
			"switch-client -t proj-feat",
		)
		wantNoCall(t, log, "attach-session", "new-session -A")
	})
	t.Run("a cancelled hub switches nothing", func(t *testing.T) {
		log := installFakeTmux(t)
		pickWith(t, nil)
		if err := openTmux(t, inTmux).Hub(hub.ModeAll, ""); err != nil {
			t.Fatal(err)
		}
		wantEmpty(t, log)
	})
	t.Run("outside tmux the hub does not run", func(t *testing.T) {
		log := installFakeTmux(t)
		noHub(t)
		if err := openTmux(t, inTerminal).Hub(hub.ModeAll, ""); err == nil {
			t.Fatal("Hub outside tmux succeeded")
		}
		wantEmpty(t, log)
	})
}

func TestTmuxSessionsListsTheSessionsOfThisMachine(t *testing.T) {
	log := installFakeTmuxWithSessions(t, "work\t1\t0\nops\t2\t1")
	items, err := openTmux(t, inTmux).Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("Sessions = %+v, want two", items)
	}
	for i, want := range []struct {
		name    string
		focused bool
	}{{"work", false}, {"ops", true}} {
		it := items[i]
		if it.Kind != hub.KindSession || it.Session != want.name || it.Label != want.name ||
			!it.State.Open || it.State.Focused != want.focused || it.Machine != "" {
			t.Errorf("item %d = %+v, want the open session %s", i, it, want.name)
		}
	}
	wantCalls(t, log, "list-sessions -F #{session_name}\t#{session_windows}\t#{session_attached}")
}

func TestTmuxAvailableIsTheProgramOnPath(t *testing.T) {
	if err := Available(mux.Tmux); err == nil {
		t.Fatal("tmux is available without a tmux on PATH")
	}
	log := installFakeTmux(t)
	if err := Available(mux.Tmux); err != nil {
		t.Fatalf("tmux is not available with a tmux on PATH: %v", err)
	}
	wantEmpty(t, log)
}

func TestLeaveNamesTheSessionOfTheWorktree(t *testing.T) {
	for dir, want := range map[string]string{
		"/work/proj/feat":             "proj-feat",
		"/work/My Project/feature.x/": "my-project-feature-x",
		"/home/u/dev/devtools/main":   "devtools-main",
	} {
		if got := sessionOfDir(dir); got != want {
			t.Errorf("sessionOfDir(%q) = %q, want %q", dir, got, want)
		}
	}
}
