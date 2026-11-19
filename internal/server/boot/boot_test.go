package boot

import (
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/testkit"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode()
}

func TestRefusesToStartWithoutSHELL(t *testing.T) {
	w := newWorld(t)
	line := refused(t, w.env("SHELL"))
	if !strings.Contains(line, "SHELL is not set") {
		t.Errorf("stderr %q does not say that SHELL is not set", line)
	}
	if _, err := os.Stat(w.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("step 1 failed but the state directory was touched: %v", err)
	}
	for _, shell := range []string{"", "sh"} {
		line := refused(t, w.env("SHELL="+shell))
		if !strings.Contains(line, "SHELL") {
			t.Errorf("SHELL=%q: stderr %q does not name SHELL", shell, line)
		}
	}
}

func TestRefusesAShellThatIsNotExecutable(t *testing.T) {
	w := newWorld(t)
	plain := filepath.Join(w.home, "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.home, "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(w.home, "missing")
	for _, shell := range []string{plain, dir, missing} {
		line := refused(t, w.env("SHELL="+shell))
		if !strings.Contains(line, shell) {
			t.Errorf("SHELL=%s: stderr %q does not name the shell", shell, line)
		}
		if shell != missing && !strings.Contains(line, "not executable") {
			t.Errorf("SHELL=%s: stderr %q does not say it is not executable", shell, line)
		}
	}
}

func TestStateDirectoryIsCreated0700(t *testing.T) {
	w := newWorld(t)
	p := start(t, w.env())
	w.ready(t, p)
	m := mode(t, w.stateDir)
	if !m.IsDir() || m.Perm() != 0o700 {
		t.Errorf("state directory mode %v, want a directory of 0700", m)
	}
	p.signal(syscall.SIGTERM)
	p.wait(10 * time.Second)
}

func TestStateDirectoryModeIsCorrected(t *testing.T) {
	w := newWorld(t)
	if err := os.MkdirAll(w.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(w.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := start(t, w.env())
	w.ready(t, p)
	if m := mode(t, w.stateDir); m.Perm() != 0o700 {
		t.Errorf("state directory mode %v after start, want 0700", m.Perm())
	}
	p.signal(syscall.SIGTERM)
	p.wait(10 * time.Second)
}

func TestAStateDirectoryThatIsNotADirectoryIsRefused(t *testing.T) {
	w := newWorld(t)
	if err := os.MkdirAll(filepath.Dir(w.stateDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(w.home, w.stateDir); err != nil {
		t.Fatal(err)
	}
	line := refused(t, w.env())
	if !strings.Contains(line, w.stateDir) {
		t.Errorf("stderr %q does not name the state directory", line)
	}
}

func TestSocketPathTooLongIsRefusedNamingThePath(t *testing.T) {
	w := newWorld(t)
	state := filepath.Join(w.home, strings.Repeat("x", 90))
	sock := filepath.Join(state, "devtools", "server.sock")
	if len(sock) <= MaxSocketPath {
		t.Fatalf("the test path is only %d bytes", len(sock))
	}
	line := refused(t, w.env("XDG_STATE_HOME="+state))
	if !strings.Contains(line, sock) {
		t.Errorf("stderr %q does not name the socket path %s", line, sock)
	}
	if !strings.Contains(line, strconv.Itoa(MaxSocketPath)) {
		t.Errorf("stderr %q does not name the limit", line)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a socket was created at the long path: %v", err)
	}
}

func TestASocketOutsideTheStateDirectoryMustBeInAPrivateDirectory(t *testing.T) {
	w := newWorld(t)
	open := filepath.Join(w.home, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	line := refused(t, w.env("DEVTOOLS_SOCKET="+filepath.Join(open, "s.sock")))
	if !strings.Contains(line, open) || !strings.Contains(line, "0700") {
		t.Errorf("stderr %q does not name the directory and the mode", line)
	}

	private := filepath.Join(w.home, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(private, "s.sock")
	p := start(t, w.env("DEVTOOLS_SOCKET="+sock))
	testkit.Eventually(t, 10*time.Second, "the server answers at DEVTOOLS_SOCKET", func() bool {
		if !p.running() {
			t.Fatalf("the server exited: %s", p.stderr.String())
		}
		return greet(sock) == nil
	})
	if m := mode(t, sock); m.Perm() != 0o600 {
		t.Errorf("socket mode %v, want 0600", m.Perm())
	}
	p.signal(syscall.SIGTERM)
	if code := p.wait(10 * time.Second); code != 0 {
		t.Errorf("exit status %d after SIGTERM", code)
	}
}

func TestASecondServerIsRefusedByTheLock(t *testing.T) {
	w := newWorld(t)
	first := start(t, w.env())
	w.ready(t, first)

	line := refused(t, w.env())
	if !strings.Contains(line, "already running") || !strings.Contains(line, w.lock) {
		t.Errorf("stderr %q does not say that a server is already running and name the lock", line)
	}
	if err := greet(w.socket); err != nil {
		t.Errorf("the first server no longer answers: %v", err)
	}

	// The lock alone decides, whoever holds it.
	first.signal(syscall.SIGTERM)
	first.wait(10 * time.Second)
	f, err := platform.Lock(w.lock)
	if err != nil {
		t.Fatalf("take the lock: %v", err)
	}
	defer f.Close()
	line = refused(t, w.env())
	if !strings.Contains(line, "already running") {
		t.Errorf("with the lock held by the test, stderr %q does not say already running", line)
	}
	if _, err := os.Lstat(w.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused server created the socket: %v", err)
	}
}

// staleSocket leaves a socket file at path that nobody listens on.
func staleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if m := mode(t, path); m&os.ModeSocket == 0 {
		t.Fatalf("no socket file left at %s", path)
	}
}

func TestAStaleSocketIsRemovedOnlyAfterARefusedConnect(t *testing.T) {
	w := newWorld(t)
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Something that is not a socket is never removed.
	if err := os.WriteFile(w.socket, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	line := refused(t, w.env())
	if !strings.Contains(line, w.socket) || !strings.Contains(line, "not a socket") {
		t.Errorf("stderr %q does not say that the path is not a socket", line)
	}
	if b, err := os.ReadFile(w.socket); err != nil || string(b) != "keep me" {
		t.Fatalf("the file at the socket path was changed: %q, %v", b, err)
	}
	os.Remove(w.socket)

	// A socket whose connect is refused is removed and replaced.
	staleSocket(t, w.socket)
	p := start(t, w.env())
	w.ready(t, p)
	if !w.logHas("accept") {
		t.Errorf("the new server did not get the connection; log:\n%s", w.log())
	}
	p.signal(syscall.SIGTERM)
	if code := p.wait(10 * time.Second); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

func TestALiveSocketIsNeverRemoved(t *testing.T) {
	w := newWorld(t)
	if err := os.MkdirAll(w.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A server that answers at the socket but holds no lock (an older
	// devtools, or one that lost its lock file).
	l, err := net.Listen("unix", w.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accepted := make(chan struct{}, 16)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
			accepted <- struct{}{}
		}
	}()
	before, err := os.Lstat(w.socket)
	if err != nil {
		t.Fatal(err)
	}

	line := refused(t, w.env())
	if !strings.Contains(line, "already answering") || !strings.Contains(line, w.socket) {
		t.Errorf("stderr %q does not say that a server answers at the socket", line)
	}
	after, err := os.Lstat(w.socket)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("the live socket was removed or replaced: %v", err)
	}
	c, err := net.DialTimeout("unix", w.socket, time.Second)
	if err != nil {
		t.Fatalf("the live socket no longer accepts: %v", err)
	}
	c.Close()
	// The probe of the refused server and the dial above.
	for i := 0; i < 2; i++ {
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatalf("the live listener saw %d connections, want 2", i)
		}
	}
	// The refused server released the lock.
	f, err := platform.Lock(w.lock)
	if err != nil {
		t.Fatalf("the lock is still held: %v", err)
	}
	f.Close()
}

func TestSocketModeIs0600(t *testing.T) {
	w := newWorld(t)
	p := start(t, w.env())
	w.ready(t, p)
	m := mode(t, w.socket)
	if m&os.ModeSocket == 0 || m.Perm() != 0o600 {
		t.Errorf("socket mode %v, want a socket of 0600", m)
	}
	if m := mode(t, w.stateDir); m.Perm() != 0o700 {
		t.Errorf("state directory mode %v, want 0700", m.Perm())
	}
	p.signal(syscall.SIGTERM)
	p.wait(10 * time.Second)
}

func TestAPeerWithAnotherUIDIsClosedBeforeHello(t *testing.T) {
	const hello = `{"type":"hello","gen":1}`

	t.Run("the wrapper", func(t *testing.T) {
		dir := testkit.ShortTempDir(t)
		sock := filepath.Join(dir, "s.sock")
		inner, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		me := uint32(os.Getuid())
		peerErr := errors.New("no credentials")
		cases := []struct {
			name  string
			first func(*net.UnixConn) (uint32, error) // the peer as the first connection reads
		}{
			{"another uid", func(c *net.UnixConn) (uint32, error) {
				uid, err := platform.PeerUID(c)
				return uid + 1, err
			}},
			{"unreadable peer", func(*net.UnixConn) (uint32, error) { return 0, peerErr }},
		}
		for _, tc := range cases {
			// The first connection gets the refusing answer, every later one
			// the real uid of its peer.
			var calls atomic.Int32
			peer := func(c *net.UnixConn) (uint32, error) {
				if calls.Add(1) == 1 {
					return tc.first(c)
				}
				return platform.PeerUID(c)
			}
			l := newPeerListener(inner, me, peer, nil)
			got := make(chan net.Conn, 1)
			go func() {
				c, err := l.Accept()
				if err == nil {
					got <- c
				}
			}()
			c, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			c.Write([]byte(hello))
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := c.Read(make([]byte, 64))
			c.Close()
			if n != 0 || err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("%s: read %d bytes, %v; want the connection closed at once", tc.name, n, err)
			}
			// The same Accept call now gets a connection that passes: it was
			// never handed the refused one.
			select {
			case s := <-got:
				s.Close()
				t.Fatalf("%s: Accept returned the refused connection", tc.name)
			default:
			}
			ok, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case s := <-got:
				s.Close()
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: a connection of this user was not accepted", tc.name)
			}
			ok.Close()
		}
		inner.Close()
	})

	t.Run("Run uses the wrapper", func(t *testing.T) {
		w := newWorld(t)
		p := start(t, w.env(), "-uid-offset", "1")
		testkit.Eventually(t, 10*time.Second, "the socket exists", func() bool {
			if !p.running() {
				t.Fatalf("the server exited: %s", p.stderr.String())
			}
			_, err := os.Lstat(w.socket)
			return err == nil && w.logHas("ready")
		})
		for i := 0; i < 3; i++ {
			c, err := net.DialTimeout("unix", w.socket, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			c.Write([]byte(hello))
			c.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := c.Read(make([]byte, 64))
			c.Close()
			if n != 0 || err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("read %d bytes, %v; want the connection closed before the server saw it", n, err)
			}
		}
		if w.logHas("accept") {
			t.Errorf("the server saw a connection of another uid; log:\n%s", w.log())
		}
		p.signal(syscall.SIGTERM)
		if code := p.wait(10 * time.Second); code != 0 {
			t.Errorf("exit status %d", code)
		}
	})
}

func TestSIGTERMSavesClosesAndRemovesTheSocket(t *testing.T) {
	w := newWorld(t)
	p := start(t, w.env())
	w.ready(t, p)
	p.signal(syscall.SIGTERM)
	if code := p.wait(10 * time.Second); code != 0 {
		t.Fatalf("exit status %d; stderr:\n%s", code, p.stderr.String())
	}
	log := w.log()
	if !w.logHas("shutdown") || !w.logHas("saved") {
		t.Errorf("the server was not shut down through Shutdown; log:\n%s", log)
	}
	if i, j := strings.Index(log, "accept"), strings.Index(log, "shutdown"); i < 0 || j < i {
		t.Errorf("order of the log is wrong:\n%s", log)
	}
	if _, err := os.Lstat(w.socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket is still there: %v", err)
	}
	f, err := platform.Lock(w.lock)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	f.Close()
	if !strings.Contains(p.stderr.String(), "shutting down") {
		t.Errorf("no log line of the shutdown on stderr:\n%s", p.stderr.String())
	}
}

func TestSIGHUPIsHandledNotIgnored(t *testing.T) {
	// The server is started with SIGHUP ignored, as nohup would: the
	// handler of Run must replace the ignored disposition, so that the
	// programs it starts do not inherit it.
	signal.Ignore(syscall.SIGHUP)
	w := newWorld(t)
	p := start(t, w.env(), "-sigstate")
	signal.Reset(syscall.SIGHUP)
	w.ready(t, p)
	if !w.logHas("sigstate ignored=false <nil>") {
		t.Errorf("a child of the server inherited an ignored SIGHUP; log:\n%s", w.log())
	}
	for i := 0; i < 3; i++ {
		p.signal(syscall.SIGHUP)
	}
	if err := greet(w.socket); err != nil {
		t.Fatalf("the server does not answer after SIGHUP: %v; stderr:\n%s", err, p.stderr.String())
	}
	if w.logHas("shutdown") {
		t.Fatalf("SIGHUP stopped the server; log:\n%s", w.log())
	}
	// SIGHUP was sent first and has the lower number, so a SIGHUP left at
	// its default would end the process before SIGTERM does: wait fails the
	// test when the process died of a signal.
	p.signal(syscall.SIGTERM)
	if code := p.wait(10 * time.Second); code != 0 {
		t.Errorf("exit status %d", code)
	}
	if !w.logHas("shutdown") {
		t.Errorf("the server did not shut down through SIGTERM; log:\n%s", w.log())
	}
}

func TestTheWatchdogEndsAHangingShutdown(t *testing.T) {
	w := newWorld(t)
	p := start(t, w.env(), "-hang", "-child", "-watchdog", "300ms")
	w.ready(t, p)
	var child int
	for _, l := range strings.Split(w.log(), "\n") {
		if v, ok := strings.CutPrefix(l, "child "); ok {
			child, _ = strconv.Atoi(v)
		}
	}
	if child <= 0 {
		t.Fatalf("the fake server started no child; log:\n%s", w.log())
	}
	t.Cleanup(func() { syscall.Kill(-child, syscall.SIGKILL) })
	if err := syscall.Kill(child, 0); err != nil {
		t.Fatalf("the child is not running: %v", err)
	}

	began := time.Now()
	p.signal(syscall.SIGTERM)
	if code := p.wait(10 * time.Second); code != 1 {
		t.Fatalf("exit status %d after a hanging shutdown, want 1; stderr:\n%s", code, p.stderr.String())
	}
	if d := time.Since(began); d < 300*time.Millisecond {
		t.Errorf("exited after %v, before the watchdog delay", d)
	}
	if !w.logHas("shutdown") || w.logHas("saved") {
		t.Errorf("the shutdown did not hang as arranged; log:\n%s", w.log())
	}
	if !strings.Contains(p.stderr.String(), "shutdown did not finish") {
		t.Errorf("no watchdog line on stderr:\n%s", p.stderr.String())
	}
	testkit.Eventually(t, 5*time.Second, "the pane group is killed", func() bool {
		return errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
	})
	f, err := platform.Lock(w.lock)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	f.Close()
}

func TestExitStatusIsZeroAfterSIGTERM(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		w := newWorld(t)
		p := start(t, w.env())
		w.ready(t, p)
		p.signal(sig)
		if code := p.wait(10 * time.Second); code != 0 {
			t.Errorf("exit status %d after %v, want 0; stderr:\n%s", code, sig, p.stderr.String())
		}
		if !w.logHas("new version=test-version host=true") {
			t.Errorf("the server was not given the version and the host; log:\n%s", w.log())
		}
	}
}

func TestKillsReachEveryGroupBelowTheServer(t *testing.T) {
	procs := []platform.Proc{
		{Pid: 100, PPid: 1, Pgid: 100},   // the server
		{Pid: 101, PPid: 100, Pgid: 101}, // a pane shell
		{Pid: 102, PPid: 101, Pgid: 102}, // a job of that shell
		{Pid: 103, PPid: 101, Pgid: 101}, // a child in the shell's group
		{Pid: 104, PPid: 100, Pgid: 100}, // a child in the server's group
		{Pid: 200, PPid: 1, Pgid: 200},   // not below the server
		{Pid: 201, PPid: 200, Pgid: 101}, // in a pane group but not below the server
	}
	got := descendants(100, procs)
	want := []target{{101, true}, {104, false}, {102, true}}
	if len(got) != len(want) {
		t.Fatalf("kills %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kills %v, want %v", got, want)
		}
	}
}
