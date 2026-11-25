package cli

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/testkit"
	"devtools/internal/ui"
)

// liveWorld is a world whose fake server can upgrade itself live; the
// restarts of the service are counted, and the service runs the fake
// server (pid 1) unless servicePidOf says otherwise.
func liveWorld(t *testing.T, servicePidOf int) (*world, *int) {
	t.Helper()
	restarts := 0
	saved, savedPid, savedWait, savedPoll := restartService, servicePid, upgradeWait, upgradePoll
	restartService = func() error { restarts++; return nil }
	servicePid = func() (int, error) { return servicePidOf, nil }
	upgradeWait, upgradePoll = 500*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { restartService, servicePid, upgradeWait, upgradePoll = saved, savedPid, savedWait, savedPoll })
	w := newWorld(t)
	w.server.set(func(f *fakeServer) { f.caps = []string{proto.CapStateGet, proto.CapUpgrade} })
	return w, &restarts
}

// accept makes the fake server accept the upgrade as attempt id and,
// when done is set, show its outcome in the welcome right away.
func accept(w *world, id string, done *proto.Upgraded) {
	w.server.set(func(f *fakeServer) {
		f.upgrade = func(rid uint64) proto.Msg {
			f.upgraded = done
			return proto.OKReply(rid, proto.ServerUpgradeResult{ID: id, From: "fake", To: "0.1.0+new", Panes: 2})
		}
	})
}

func refuse(w *world, code, text string) {
	w.server.set(func(f *fakeServer) {
		f.upgrade = func(rid uint64) proto.Msg { return proto.ErrReply(rid, code, text) }
	})
}

func TestServerRestartUpgradesLiveAndReportsTheOutcome(t *testing.T) {
	w, restarts := liveWorld(t, 1)
	accept(w, "00aa11bb22cc33dd", &proto.Upgraded{ID: "00aa11bb22cc33dd", From: "fake", To: "0.1.0+new", Kept: 2})
	w.server.set(func(f *fakeServer) { f.version = "fake" })
	r := executeWith("", "server", "restart")
	if r.code != 0 || *restarts != 0 {
		t.Fatalf("live restart: %+v, %d service restarts", r, *restarts)
	}
	if !strings.Contains(r.stdout, ui.Upgrade("upgrading", "fake", "0.1.0+new", "2")) || !strings.Contains(r.stdout, "kept running") {
		t.Fatalf("output %q", r.stdout)
	}
	var acks []string
	w.server.set(func(f *fakeServer) { acks = append(acks, f.acks...) })
	if len(acks) != 1 || acks[0] != "00aa11bb22cc33dd" {
		t.Fatalf("acknowledgements %q", acks)
	}
}

func TestServerRestartReportsEveryOutcomeAndNeverRestartsColdAfterAcceptance(t *testing.T) {
	for name, c := range map[string]struct {
		u    *proto.Upgraded
		want string
	}{
		"failed":   {&proto.Upgraded{ID: "a1a1a1a1a1a1a1a1", Error: "exec: no such file"}, ui.Upgrade("failed", "exec: no such file")},
		"fallback": {&proto.Upgraded{ID: "a1a1a1a1a1a1a1a1", Error: "the payload", Back: true}, ui.Upgrade("fallback", "the payload")},
		"cold":     {&proto.Upgraded{ID: "a1a1a1a1a1a1a1a1", Error: "both failed", Cold: true}, ui.Upgrade("cold", "both failed")},
		"pending":  {nil, ui.Upgrade("pending")},
		// a retry must not take the outcome of an earlier attempt for its own
		"other attempt": {&proto.Upgraded{ID: "ffffffffffffffff"}, ui.Upgrade("pending")},
	} {
		t.Run(name, func(t *testing.T) {
			w, restarts := liveWorld(t, 1)
			accept(w, "a1a1a1a1a1a1a1a1", c.u)
			r := executeWith("y\n", "server", "restart")
			if r.code != 1 || *restarts != 0 || !strings.Contains(r.stdout, c.want) {
				t.Fatalf("%+v, %d service restarts; want status 1, no restart and %q", r, *restarts, c.want)
			}
		})
	}
}

func TestServerRestartRefusals(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		w, restarts := liveWorld(t, 1)
		refuse(w, proto.CodeCurrent, "the server runs the installed build already")
		r := executeWith("", "server", "restart")
		if r.code != 0 || *restarts != 0 || !strings.Contains(r.stdout, "already runs the installed build") {
			t.Fatalf("%+v, %d restarts", r, *restarts)
		}
	})
	t.Run("busy", func(t *testing.T) {
		w, restarts := liveWorld(t, 1)
		refuse(w, proto.CodeBusy, "a paste is in progress")
		r := executeWith("y\n", "server", "restart")
		if r.code != 1 || *restarts != 0 || !strings.Contains(r.stdout, "a paste is in progress") {
			t.Fatalf("%+v, %d restarts", r, *restarts)
		}
	})
	t.Run("incompatible, then no", func(t *testing.T) {
		w, restarts := liveWorld(t, 1)
		refuse(w, proto.CodeIncompatible, "it reads handover versions [99]")
		r := executeWith("n\n", "server", "restart")
		if r.code != 1 || *restarts != 0 || !strings.Contains(r.stdout, ui.Upgrade("incompatible", "it reads handover versions [99]")) ||
			!strings.Contains(r.stdout, ui.Message("restart", "2", "1")) {
			t.Fatalf("%+v, %d restarts", r, *restarts)
		}
	})
	t.Run("incompatible, then yes", func(t *testing.T) {
		w, restarts := liveWorld(t, 1)
		refuse(w, proto.CodeIncompatible, "it does not answer upgrade-check")
		r := executeWith("y\n", "server", "restart")
		if r.code != 0 || *restarts != 1 {
			t.Fatalf("%+v, %d restarts", r, *restarts)
		}
	})
	t.Run("ambiguous disconnect", func(t *testing.T) {
		w, restarts := liveWorld(t, 1)
		w.server.set(func(f *fakeServer) { f.hangup[proto.TypeServerUpgrade] = true })
		r := executeWith("y\n", "server", "restart")
		if r.code != 1 || *restarts != 0 || !strings.Contains(r.stdout, ui.Upgrade("unknown")) {
			t.Fatalf("%+v, %d restarts", r, *restarts)
		}
	})
}

func TestTheColdRestartRestartsOnlyTheServiceServer(t *testing.T) {
	w, restarts := liveWorld(t, 4242)
	r := executeWith("", "server", "restart", "--cold", "--yes")
	if r.code != 1 || *restarts != 0 {
		t.Fatalf("--cold of a server the service does not run: %+v, %d restarts", r, *restarts)
	}
	for _, typ := range w.server.Types() {
		if typ == proto.TypeServerUpgrade {
			t.Fatalf("--cold asked for a live upgrade")
		}
	}
	w2, restarts2 := liveWorld(t, 1)
	_ = w2
	if r := executeWith("", "server", "restart", "--cold", "--yes"); r.code != 0 || *restarts2 != 1 {
		t.Fatalf("--cold of the service's server: %+v, %d restarts", r, *restarts2)
	}
}

func TestUpgradeCheckSaysWhatThisBuildTakesOver(t *testing.T) {
	newWorld(t)
	r := executeWith("", "server", "upgrade-check", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"handover":[1]`) || !strings.Contains(r.stdout, `"gens":[1,2]`) {
		t.Fatalf("upgrade-check: %+v", r)
	}
}

// serviceCalls replaces the service manager: the restarts and starts are
// counted, and the service runs pid servicePidOf.
func serviceCalls(t *testing.T, servicePidOf int) (restarts, starts *int) {
	t.Helper()
	restarts, starts = new(int), new(int)
	saved, savedStart, savedPid, savedLimit := restartService, startService, servicePid, statusLimit
	restartService = func() error { *restarts++; return nil }
	startService = func() error { *starts++; return nil }
	servicePid = func() (int, error) { return servicePidOf, nil }
	statusLimit = 200 * time.Millisecond
	t.Cleanup(func() {
		restartService, startService, servicePid, statusLimit = saved, savedStart, savedPid, savedLimit
	})
	return restarts, starts
}

// A server that accepts and does not answer (a live upgrade pauses its
// accepting and holds its loop) is never restarted: not by restart, not by
// --cold, not with --yes.
func TestAServerThatDoesNotAnswerIsNeverRestarted(t *testing.T) {
	for _, args := range [][]string{{"server", "restart"}, {"server", "restart", "--yes"}, {"server", "restart", "--cold", "--yes"}} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			restarts, starts := serviceCalls(t, 1)
			w := newWorld(t)
			w.server.set(func(f *fakeServer) { f.silent = true })
			r := executeWith("y\n", args...)
			if r.code != 1 || *restarts != 0 || *starts != 0 || !strings.Contains(r.stdout, "nothing was restarted") {
				t.Fatalf("%+v, %d restarts, %d starts", r, *restarts, *starts)
			}
		})
	}
}

// No server at the socket: the service is started (which ends nothing)
// only when the service manager runs no server; a socket that is not the
// service's leaves a running service alone.
func TestNoServerStartsTheServiceOnlyWhenItRunsNone(t *testing.T) {
	stale := func(t *testing.T) string {
		p := filepath.Join(testkit.ShortTempDir(t), "stale.sock")
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatal(err)
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		ln.Close()
		return p
	}
	for name, socket := range map[string]func(t *testing.T) string{
		"missing": func(t *testing.T) string { return filepath.Join(testkit.ShortTempDir(t), "no.sock") },
		"refused": stale,
	} {
		t.Run(name+", service stopped", func(t *testing.T) {
			restarts, starts := serviceCalls(t, 0)
			newWorld(t)
			t.Setenv("DEVTOOLS_SOCKET", socket(t))
			if r := executeWith("", "server", "restart"); r.code != 0 || *restarts != 0 || *starts != 1 {
				t.Fatalf("%+v, %d restarts, %d starts", r, *restarts, *starts)
			}
		})
		t.Run(name+", service runs another server", func(t *testing.T) {
			restarts, starts := serviceCalls(t, 4242)
			newWorld(t)
			t.Setenv("DEVTOOLS_SOCKET", socket(t))
			r := executeWith("", "server", "restart", "--yes")
			if r.code != 1 || *restarts != 0 || *starts != 0 || !strings.Contains(r.stdout, ui.Upgrade("other-server", "4242")) {
				t.Fatalf("%+v, %d restarts, %d starts", r, *restarts, *starts)
			}
		})
	}
}
