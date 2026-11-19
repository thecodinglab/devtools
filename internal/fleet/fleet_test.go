package fleet_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/proto"
)

// TestMain is hermetic: the fleet reads no environment and no file, and no
// variable of the developer's session reaches a test.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "HERDR_") || (strings.HasPrefix(name, "DEVTOOLS_") && name != "DEVTOOLS_TEST_NO_PTY") ||
			name == "TMUX" || name == "TMUX_PANE" {
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}

// machines builds config lines: "M connect" is a permanent machine, "K" an
// on-demand one.
func machines(lines ...string) []config.Machine {
	var out []config.Machine
	for _, l := range lines {
		name, rest, _ := strings.Cut(l, " ")
		out = append(out, config.Machine{Name: name, Connect: rest == "connect"})
	}
	return out
}

// here is the session of the local machine in most tests.
var here = fleettest.Sess{N: 1, Dir: "/home/florian/dev/devtools/main", Label: "devtools-main"}

// start is a rig whose local machine is online with one session.
func start(t *testing.T, lines ...string) *fleettest.Rig {
	t.Helper()
	r := fleettest.New(t, machines(lines...)...)
	r.M("").Online(fleettest.Snapshot(here))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "") == "online" })
	return r
}

// online scripts machine name online with snap and waits until the
// summary says so.
func online(t *testing.T, r *fleettest.Rig, name string, snap proto.Snapshot) fleet.SummaryData {
	t.Helper()
	r.M(name).Online(snap)
	return r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, name) == "online" })
}

// watchID is the id of the newest catalog.watch sent to machine name.
func watchID(t *testing.T, r *fleettest.Rig, name string, n int) uint64 {
	t.Helper()
	r.Eventually("catalog.watch number "+itoa(n)+" to "+name, func() bool {
		return len(r.M(name).SentOf(proto.TypeCatalogWatch)) >= n
	})
	return r.M(name).SentOf(proto.TypeCatalogWatch)[n-1].ID
}

func itoa(n int) string { return strconv.Itoa(n) }

// entry is a catalog row of a worktree.
func entry(path, label string) proto.CatalogEntry {
	return proto.CatalogEntry{Kind: proto.CatalogWorktree, Project: "p", Worktree: label, Label: label, Path: path, Root: "/home/florian/dev", Session: "p-" + label}
}

// waitDials waits until machine name was dialled n times, and checks that
// it stays at n for a moment.
func waitDials(t *testing.T, r *fleettest.Rig, name string, n int) {
	t.Helper()
	r.Eventually("dial number "+itoa(n)+" of "+name, func() bool { return r.M(name).Dials() >= n })
	time.Sleep(fleettest.Quiet)
	if got := r.M(name).Dials(); got != n {
		t.Fatalf("%q was dialled %d times, want %d", name, got, n)
	}
}

func TestTheRigStopsTheFleetWhenTheTestEnds(t *testing.T) {
	r := start(t)
	if r.M("").Dials() != 1 {
		t.Fatalf("the local machine was dialled %d times, want 1", r.M("").Dials())
	}
}
