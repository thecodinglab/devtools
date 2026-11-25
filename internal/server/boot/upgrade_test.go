package boot

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/handover"
	"devtools/internal/platform"
	"devtools/internal/server"
	"devtools/internal/testkit"
)

// stubExec replaces the exec of both boundaries; it records the path and
// fails, as an exec that did not happen.
func stubExec(t *testing.T) *[]string {
	t.Helper()
	var paths []string
	saved := execKeeping
	execKeeping = func(path string, argv, env []string, keep []int) error {
		paths = append(paths, path)
		return errors.New("stub exec")
	}
	t.Cleanup(func() { execKeeping = saved })
	return &paths
}

func signals(t *testing.T, queued ...os.Signal) chan os.Signal {
	t.Helper()
	sigs := make(chan os.Signal, 8)
	for _, s := range queued {
		sigs <- s
	}
	t.Cleanup(func() { signal.Stop(sigs) })
	return sigs
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// copyExecutable is an executable file of its own in dir.
func copyExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// rollbackEnv is the inventory of an attempt whose old build is path,
// with the rollback handle open on it.
func rollbackEnv(t *testing.T, path string) handover.Env {
	t.Helper()
	id, err := platform.FileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := keepExecutable(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	return handover.Env{V: 1, ID: "0123456789abcdef", Attempt: 1, Payload: 4000, Lock: 4001, Listener: 4002, Lo: 4004, Hi: 4003, Exe: fd, From: id.Path}
}

func TestTheStopFilterIgnoresSIGHUP(t *testing.T) {
	if got := takeStop(signals(t, syscall.SIGHUP, syscall.SIGHUP)); got != nil {
		t.Fatalf("SIGHUP alone is a stop: %v", got)
	}
	if got := takeStop(signals(t, syscall.SIGHUP, syscall.SIGTERM)); got != syscall.SIGTERM {
		t.Fatalf("SIGHUP then SIGTERM = %v, want SIGTERM", got)
	}
	if got := takeStop(signals(t)); got != nil {
		t.Fatalf("nothing queued = %v", got)
	}
}

// A SIGHUP queued while the new image failed to adopt does not keep the
// old build from running again; a SIGTERM is a stop, not a cold start.
func TestTheExecBackIgnoresSIGHUPAndStopsOnSIGTERM(t *testing.T) {
	dir := t.TempDir()
	old := copyExecutable(t, dir, "devtools-old")
	r := &run{log: quietLog(), argv0: "/profile/bin/devtools"}

	paths := stubExec(t)
	in := &inherited{env: rollbackEnv(t, old), files: map[int]*os.File{}}
	err, sig := r.execBack(in, signals(t, syscall.SIGHUP))
	if sig != nil || len(*paths) != 1 || (*paths)[0] != in.env.From {
		t.Fatalf("with SIGHUP queued: %v, %v, exec'd %q; want the exec back", err, sig, *paths)
	}

	*paths = nil
	err, sig = r.execBack(in, signals(t, syscall.SIGHUP, syscall.SIGTERM))
	if sig != syscall.SIGTERM || len(*paths) != 0 || !errors.Is(err, errStopping) {
		t.Fatalf("with SIGTERM queued: %v, %v, exec'd %q; want a stop without exec", err, sig, *paths)
	}
}

// The exec back runs only the file the old image ran from: a path removed
// or replaced since is refused, not exec'd.
func TestTheExecBackRefusesAReplacedOrRemovedBuild(t *testing.T) {
	r := &run{log: quietLog(), argv0: "/profile/bin/devtools"}
	paths := stubExec(t)
	for name, change := range map[string]func(p string){
		"removed": func(p string) { os.Remove(p) },
		"replaced": func(p string) {
			os.WriteFile(p+".new", []byte("#!/bin/sh\nexit 1\n"), 0o755)
			os.Rename(p+".new", p)
		},
	} {
		t.Run(name, func(t *testing.T) {
			old := copyExecutable(t, t.TempDir(), "devtools-old")
			in := &inherited{env: rollbackEnv(t, old), files: map[int]*os.File{}}
			change(old)
			err, sig := r.execBack(in, signals(t))
			if err == nil || sig != nil || len(*paths) != 0 {
				t.Fatalf("%v, %v, exec'd %q; want a refusal", err, sig, *paths)
			}
		})
	}
	// And the handle itself refuses a file that is not the running image.
	p := copyExecutable(t, t.TempDir(), "devtools-old")
	id, _ := platform.FileIdentity(p)
	os.WriteFile(p+".new", []byte("x"), 0o755)
	os.Rename(p+".new", p)
	if fd, err := keepExecutable(id); err == nil {
		syscall.Close(fd)
		t.Fatal("a replaced running build was kept as the rollback")
	}
}

// execRun is a run that can hand over: a listener, a lock, a state
// directory and the identity of a build.
func execRun(t *testing.T) *run {
	t.Helper()
	dir := testkit.ShortTempDir(t)
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	lock, err := os.Create(filepath.Join(dir, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	self, err := platform.FileIdentity(copyExecutable(t, dir, "devtools-self"))
	if err != nil {
		t.Fatal(err)
	}
	return &run{log: quietLog(), stateDir: dir, ul: ln.(*net.UnixListener), lock: lock, self: self, argv0: "/profile/bin/devtools", version: "a"}
}

func execHandover(t *testing.T, target string, accepted *bool) *server.Handover {
	t.Helper()
	id, err := platform.FileIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	return &server.Handover{
		Attempt: "0123456789abcdef", Target: id, Auto: true,
		Header: handover.Header{Attempt: "0123456789abcdef", Upgraded: &handover.Upgraded{To: "b"}},
		Data:   &handover.Writer{},
		Accept: func() { *accepted = true },
	}
}

// No exec without the journal's record of the attempt: the guard of the
// automatic upgrade against a crash loop.
func TestNoExecWithoutTheJournal(t *testing.T) {
	r := execRun(t)
	paths := stubExec(t)
	// upgrade.json is a directory: the journal cannot be written.
	if err := os.Mkdir(filepath.Join(r.stateDir, handover.JournalName), 0o700); err != nil {
		t.Fatal(err)
	}
	accepted := false
	h := execHandover(t, copyExecutable(t, r.stateDir, "devtools-new"), &accepted)
	err, sig := r.execStep(h, signals(t))
	if err == nil || !strings.Contains(err.Error(), "journal") || sig != nil || len(*paths) != 0 || accepted {
		t.Fatalf("%v, %v, exec'd %q, accepted %v; want a refusal before the requester hears ok", err, sig, *paths, accepted)
	}
}

// With a journal the exec happens, after the record of the attempt, the
// automatic one with its time for the interval of the next image.
func TestTheExecIsJournaledFirst(t *testing.T) {
	r := execRun(t)
	paths := stubExec(t)
	accepted := false
	target := copyExecutable(t, r.stateDir, "devtools-new")
	h := execHandover(t, target, &accepted)
	err, _ := r.execStep(h, signals(t, syscall.SIGHUP))
	if err == nil || len(*paths) != 1 || !accepted {
		t.Fatalf("%v, exec'd %q, accepted %v", err, *paths, accepted)
	}
	a, ok := handover.LoadJournal(r.stateDir).Get(h.Attempt)
	if !ok || a.Result != handover.Failed || !a.Auto || a.Started == 0 || handover.LoadJournal(r.stateDir).LastAuto() != a.Started {
		t.Fatalf("journal %+v", a)
	}

	// A target replaced after its check is not exec'd.
	*paths = nil
	h = execHandover(t, target, &accepted)
	h.Attempt, h.Header.Attempt = "1123456789abcdef", "1123456789abcdef"
	h.Verify = func() error { return errors.New("the build changed after it was checked") }
	if err, _ := r.execStep(h, signals(t)); err == nil || len(*paths) != 0 {
		t.Fatalf("%v, exec'd %q", err, *paths)
	}
}

// The SIGKILL of the cold recovery cannot reach a recycled pid: a kill
// that began when the child ended runs to its end before the child is
// reaped, and until then the pid is the child's.
func TestTheDelayedKillEndsBeforeTheReap(t *testing.T) {
	kills := make(chan func(), 1)
	saved := killLater
	killLater = func(d time.Duration, f func()) func() bool {
		kills <- f
		return func() bool { return false } // as if the timer fired already
	}
	t.Cleanup(func() { killLater = saved })
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		reapChild(cmd.Process.Pid, time.Second)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond) // the child ended long ago
	select {
	case <-done:
		t.Fatal("the child was reaped while its kill was still running")
	default:
	}
	(<-kills)()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child was never reaped")
	}
}
