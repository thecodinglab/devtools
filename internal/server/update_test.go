package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
)

// installRig is a profile as the service sees it: two builds in a store,
// and the program path, a symlink to one of them, that the server was
// started through. Nothing runs: InstalledVersion answers by path.
type installRig struct {
	t        *testing.T
	program  string
	running  string
	mu       sync.Mutex
	versions map[string]string // path → what "<path> version" prints; missing fails
	asked    []string
}

func newInstallRig(t *testing.T) *installRig {
	dir := testkit.ShortTempDir(t)
	r := &installRig{t: t, program: filepath.Join(dir, "profile-devtools"), running: filepath.Join(dir, "a-devtools"), versions: map[string]string{}}
	r.build("a-devtools")
	r.point("a-devtools")
	return r
}

func (r *installRig) path(name string) string { return filepath.Join(filepath.Dir(r.program), name) }

// build puts an executable file into the store.
func (r *installRig) build(name string) string {
	p := r.path(name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return p
}

// point switches the profile to the build name, as an activation does.
func (r *installRig) point(name string) {
	tmp := r.program + ".new"
	os.Remove(tmp)
	if err := os.Symlink(r.path(name), tmp); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(tmp, r.program); err != nil {
		r.t.Fatal(err)
	}
}

func (r *installRig) setVersion(name, v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, _ := filepath.EvalSymlinks(r.path(name))
	r.versions[p] = v
}

func (r *installRig) askedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.asked)
}

func (r *installRig) options(o *Options) {
	o.Program, o.Executable = r.program, r.running
	o.UpdateInterval = 20 * time.Millisecond
	o.InstalledVersion = func(ctx context.Context, path string) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.asked = append(r.asked, path)
		v, ok := r.versions[path]
		if !ok {
			return "", errors.New("unknown command \"version\"")
		}
		return v + "\n", nil
	}
}

// installedOf reads what the core holds.
func installedOf(ts *testServer) string {
	var v string
	ts.do(func(c *core) { v = c.installed })
	return v
}

func waitInstalled(t *testing.T, ts *testServer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for installedOf(ts) != want {
		if time.Now().After(deadline) {
			t.Fatalf("installed = %q, want %q", installedOf(ts), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An update installed while the server runs is in the welcome of every new
// connection and in event.update to the connected windows that claimed
// CapUpdate; one that did not claim it gets nothing. Switching back to the
// running build clears it.
func TestTheServerNoticesAnInstalledUpdate(t *testing.T) {
	r := newInstallRig(t)
	ts := newTestServer(t, r.options)

	h := ts.interactive()
	if w := h.Welcome(); w.Installed != "" || !slices.Contains(w.Caps, proto.CapUpdate) {
		t.Fatalf("welcome before an update: %+v", w)
	}
	withCap := ts.interactiveWith(proto.CapUpdate)
	from := len(withCap.Received())

	time.Sleep(100 * time.Millisecond) // a few rounds of the watch see the same build
	if n := r.askedCount(); n != 0 {
		t.Fatalf("the running build was asked for its version %d times", n)
	}

	r.build("b-devtools")
	r.setVersion("b-devtools", "0.1.0+04c5fef")
	r.point("b-devtools")
	waitInstalled(t, ts, "0.1.0+04c5fef")

	var ev proto.UpdateEvent
	if err := withCap.WaitMessage(proto.TypeEventUpdate, from).Decode(&ev); err != nil || ev.Installed != "0.1.0+04c5fef" {
		t.Fatalf("event.update = %+v, %v", ev, err)
	}
	for _, m := range h.Received() {
		if m.T == proto.TypeEventUpdate {
			t.Fatalf("a window without the capability got event.update")
		}
	}
	if w := ts.interactive().Welcome(); w.Installed != "0.1.0+04c5fef" {
		t.Fatalf("welcome after an update: installed %q", w.Installed)
	}
	asked := r.askedCount()
	time.Sleep(100 * time.Millisecond)
	if n := r.askedCount(); n != asked {
		t.Fatalf("the same installed build was asked again: %d, then %d times", asked, n)
	}

	// a rollback to the running build: nothing waits any more
	from = len(withCap.Received())
	r.point("a-devtools")
	waitInstalled(t, ts, "")
	if err := withCap.WaitMessage(proto.TypeEventUpdate, from).Decode(&ev); err != nil || ev.Installed != "" {
		t.Fatalf("event.update after the rollback = %+v, %v", ev, err)
	}
	if w := ts.interactive().Welcome(); w.Installed != "" {
		t.Fatalf("welcome after the rollback: installed %q", w.Installed)
	}
}

// A build that does not say its version (an older devtools without the
// version command) is an update of unknown version, and so is another file
// that prints the running build's version: two dirty builds of one commit
// have the same stamp.
func TestTheInstalledVersionIsAskedOfTheBuild(t *testing.T) {
	r := newInstallRig(t)
	ts := newTestServer(t, r.options)

	r.build("old-devtools")
	r.point("old-devtools")
	waitInstalled(t, ts, installedUnknown)

	r.build("b-devtools")
	r.setVersion("b-devtools", "0.1.0+04c5fef")
	r.point("b-devtools")
	waitInstalled(t, ts, "0.1.0+04c5fef")

	r.build("same-devtools")
	r.setVersion("same-devtools", "0.0.0-test") // the version of the test server
	r.point("same-devtools")
	waitInstalled(t, ts, installedUnknown)
}

// The running build is told by its file, not by the spelling of its path:
// another path to the same file (a symlink, /private/var for /var) is no
// update, and the build the profile points at when the server starts is
// one when it is not the running image (switched between exec and boot).
func TestTheRunningBuildIsItsFile(t *testing.T) {
	r := newInstallRig(t)
	alias := r.path("alias-devtools")
	if err := os.Symlink(r.running, alias); err != nil {
		t.Fatal(err)
	}
	r.running = alias
	ts := newTestServer(t, r.options)
	time.Sleep(100 * time.Millisecond)
	if n := r.askedCount(); n != 0 || installedOf(ts) != "" {
		t.Fatalf("the running build under another path was taken for an update: asked %d, installed %q", n, installedOf(ts))
	}

	r2 := newInstallRig(t)
	r2.build("b-devtools")
	r2.setVersion("b-devtools", "0.1.0+04c5fef")
	r2.point("b-devtools") // before the server's watch starts
	ts2 := newTestServer(t, r2.options)
	waitInstalled(t, ts2, "0.1.0+04c5fef")
}

// Without a program path the server watches nothing.
func TestNoProgramWatchesNothing(t *testing.T) {
	asked := false
	ts := newTestServer(t, func(o *Options) {
		o.UpdateInterval = 10 * time.Millisecond
		o.InstalledVersion = func(context.Context, string) (string, error) { asked = true; return "x", nil }
	})
	time.Sleep(50 * time.Millisecond)
	if installedOf(ts) != "" || asked {
		t.Fatal("a server without a program path watched one")
	}
}

// interactiveWith dials an interactive connection that claims caps.
func (ts *testServer) interactiveWith(caps ...string) *prototest.Client {
	ts.t.Helper()
	h := prototest.Interactive()
	h.Caps = caps
	return prototest.Dial(ts.t, ts.socket, h)
}
