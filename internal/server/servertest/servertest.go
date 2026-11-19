// Package servertest starts the real devtools server inside the test
// process (10.1.4 of the plan): server.New with real panes, on a unix
// socket in a directory of the test, until the test ends. The panes run a
// helper program of the test binary (testkit, 10.1.3) instead of a shell, so
// a test drives a known program over the wire with prototest.
//
// A package that uses Start calls testkit.RunHelper first in its TestMain:
// the panes are the test binary itself.
//
// It is a test-only package (import rule 8).
package servertest

import (
	"bytes"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/pane"
	"devtools/internal/server"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// Names of the server that Start runs: welcome.version and welcome.host,
// and so the XTVERSION answer of its panes ("devtools <Version> <Host>").
const (
	Version = "0.0.0-servertest"
	Host    = "servertest"
)

// SocketName is the file name of the socket inside the directory of Start.
const SocketName = "server.sock"

// stopWait bounds how long the end of the test waits for Serve to return
// after Shutdown.
const stopWait = 10 * time.Second

// Option changes how Start runs the server.
type Option func(*config)

type config struct {
	helper []string // helper name and its arguments
	opts   []func(*server.Options)
}

// WithHelper names the helper program the panes run (10.1.3), with its
// arguments; the default is "hex".
func WithHelper(name string, args ...string) Option {
	return func(c *config) { c.helper = append([]string{name}, args...) }
}

// WithOptions changes the server's options after Start filled them, before
// server.New; for shorter intervals, a further variable of the server's
// environment, or another pane program.
func WithOptions(f func(*server.Options)) Option {
	return func(c *config) { c.opts = append(c.opts, f) }
}

// Env returns the environment Start gives the server for a directory and a
// helper: testkit.CleanEnv with HOME at dir, the variables of helperEnv (the
// one that selects the helper), GORACE without the exit delay of the race
// detector, and DEVTOOLS_PASS_ENV naming them, so that pane.Env hands them
// on to every pane (3.2.1).
func Env(dir string, helperEnv ...string) []string {
	helperEnv = append(append([]string(nil), helperEnv...), "GORACE=atexit_sleep_ms=0")
	names := make([]string, 0, len(helperEnv))
	for _, kv := range helperEnv {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	extra := append([]string(nil), helperEnv...)
	extra = append(extra, pane.EnvPassEnv+"="+strings.Join(names, ","))
	return testkit.CleanEnv(dir, extra...)
}

// Start runs a real server on a socket in dir until the test ends. Panes run
// the helper child named by WithHelper (default "hex") instead of a shell.
//
// dir is the server's state directory (state.json lives there) and the HOME
// of its environment; the socket is dir/server.sock. Every interval is the
// production value of server.Defaults unless WithOptions changes it. The
// XTVERSION identity of the process is set to "devtools <Version> <Host>"
// before the server starts, as boot.Run does (3.9 step 9).
//
// When the test ends the server stops through Shutdown, the stop steps of
// 3.9: the state is saved, every pane closed, every connection drained. A
// test that restarts a server in the same directory runs each server in a
// subtest of its own, so that the first one stops when its subtest ends.
// When the test failed, the server's log is printed.
func Start(t *testing.T, dir string, opts ...Option) (socket string) {
	t.Helper()
	testkit.RequirePTY(t)
	c := config{helper: []string{"hex"}}
	for _, o := range opts {
		o(&c)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("servertest: state directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("servertest: state directory: %v", err)
	}
	socket = filepath.Join(dir, SocketName)
	if n := len(socket); n > testkit.MaxSocketPath {
		t.Fatalf("servertest: the socket path %s is %d bytes, longer than %d; use testkit.ShortTempDir", socket, n, testkit.MaxSocketPath)
	}

	argv, helperEnv := testkit.HelperCommand(c.helper[0], c.helper[1:]...)
	log := &logBuffer{}
	o := server.Defaults()
	o.StateDir = dir
	o.Shell = argv
	o.Env = Env(dir, helperEnv...)
	o.Version = Version
	o.Host = Host
	o.Log = slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for _, f := range c.opts {
		f(&o)
	}

	vt.SetIdentity("devtools", Version, Host)
	srv, err := server.New(o)
	if err != nil {
		t.Fatalf("servertest: server.New: %v", err)
	}
	os.Remove(socket)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		srv.Shutdown()
		t.Fatalf("servertest: listen on %s: %v", socket, err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		srv.Shutdown()
		t.Fatalf("servertest: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Shutdown()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("servertest: Serve returned %v after Shutdown", err)
			}
		case <-time.After(stopWait):
			t.Errorf("servertest: Serve did not return within %v of Shutdown", stopWait)
		}
		ln.Close()
		os.Remove(socket)
		if t.Failed() {
			t.Logf("servertest: the server's log:\n%s", log.String())
		}
	})
	return socket
}

// logBuffer collects the server's log; it is written from many goroutines.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.b.Len() < 1<<20 {
		l.b.Write(p)
	}
	return len(p), nil
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
