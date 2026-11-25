// Package boot is the body of `devtools server`: the start steps of 3.9
// (preconditions, state directory, lock, socket), the listener that closes
// connections of another user before the server sees them (SEC2), the
// signals, and the stop order around server.Shutdown with its watchdog.
package boot

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"devtools/internal/handover"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/server"
	"devtools/internal/vt"
)

// Names and limits of the start steps (3.1, 3.9).
const (
	// LockName is the file in the state directory whose flock says that a
	// server runs.
	LockName = "server.lock"
	// MaxSocketPath is the longest socket path that fits sun_path with its
	// terminating NUL on macOS (104 bytes); Linux allows 108.
	MaxSocketPath = 103
	// FDLimit is the open-file limit the server asks for.
	FDLimit = 8192
	// Watchdog is how long a shutdown may take before the watchdog kills
	// every remaining pane group and the process exits 1.
	Watchdog = 5 * time.Second
	// probeTimeout bounds the connect of step 5 to an existing socket.
	probeTimeout = time.Second
)

// Server is what Run needs of a server: *server.Server satisfies it.
type Server interface {
	Serve(l net.Listener) error
	Shutdown()
}

// The injection points of the tests. Production code never changes them.
var (
	// newServer creates the server of step 10; tests install a fake.
	newServer = func(o server.Options) (Server, error) {
		s, err := server.New(o)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	// setIdentity is step 9.
	setIdentity = vt.SetIdentity
	// ownUID is the uid a peer must have to reach the server.
	ownUID = func() uint32 { return uint32(os.Getuid()) }
	// peerUID reads the uid of a connected peer.
	peerUID = platform.PeerUID
	// watchdogAfter is Watchdog; the tests shorten it.
	watchdogAfter = Watchdog
	// exit ends the process from the watchdog.
	exit = os.Exit
	// stderr receives the one line of a failed start step and the log.
	stderr io.Writer = os.Stderr
)

// failure is a start step that failed: its text is the one line on stderr.
type failure struct{ text string }

func (f *failure) Error() string { return f.text }

func failf(format string, args ...any) error {
	return &failure{text: fmt.Sprintf(format, args...)}
}

// Run is the body of `devtools server`: the start steps of 3.9, then
// server.New and Serve, until SIGTERM or SIGINT ends it through the stop
// steps. It returns the exit status: 0 after an orderly stop, 1 when a start
// step failed (with one line on stderr) or the server ended on its own.
func Run(version string) int {
	r := &run{version: version}
	return r.main()
}

// run holds what the start steps acquired, so that the stop steps can give
// it back.
type run struct {
	version  string
	stateDir string
	socket   string
	lock     *os.File
	log      *slog.Logger

	// The live upgrade (upgrade.go).
	argv0     string            // the path this process was started as, made absolute
	self      platform.Identity // the file this process runs
	selfErr   error
	ul        *net.UnixListener // the listener beneath the peer listener
	execc     chan execReq      // the server's exec requests, to the serve loop
	serveDone chan struct{}     // closed when the serve loop ended
	upgrade   *proto.Upgraded   // welcome.upgraded of the server this run starts
	adopted   bool              // the server was adopted from a handover
	attempt   string            // the attempt that made it
	healthy   *time.Timer       // writes "ok" to the journal after the health interval
	jmu       sync.Mutex        // the writers of the journal take turns
}

func (r *run) fail(err error) int {
	text := err.Error()
	var f *failure
	if !errors.As(err, &f) {
		text = "unexpected error: " + text
	}
	fmt.Fprintf(stderr, "devtools server: %s\n", oneLine(text))
	r.release()
	return 1
}

// release gives back the lock; the socket is removed by the caller that
// created it.
func (r *run) release() {
	if r.lock != nil {
		r.lock.Close()
		r.lock = nil
	}
}

func (r *run) main() int {
	r.log = slog.New(slog.NewTextHandler(stderr, nil)).With("component", "server")
	// The file this process runs, at once, and the path the service started
	// it through, before step 7 changes the directory: the server watches
	// that path for an installed update and upgrades to what it names
	// (upgrade.go).
	r.self, r.selfErr = selfExecutable()
	r.argv0 = absArgv0(os.Args[0])
	r.execc = make(chan execReq)
	r.serveDone = make(chan struct{})

	// (8, first) SIGHUP is handled and does nothing (not ignored, so
	// children do not inherit an ignored SIGHUP); SIGTERM and SIGINT shut
	// down. They are taken from the first moment, so a stop that arrives
	// while a new image adopts the panes waits in the channel.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	if v, ok := os.LookupEnv(handover.EnvVar); ok {
		os.Unsetenv(handover.EnvVar)
		return r.adopt(v, sigs)
	}
	return r.start(sigs)
}

// start is a start without a handover: the steps of 3.9 from the first.
func (r *run) start(sigs chan os.Signal) int {
	if err := r.preconditions(); err != nil {
		return r.fail(err)
	}
	// (4) An exclusive lock on server.lock.
	lockPath := filepath.Join(r.stateDir, LockName)
	if r.lock == nil {
		lock, err := platform.Lock(lockPath)
		if errors.Is(err, platform.ErrLocked) {
			return r.fail(failf("a server is already running (%s is locked)", lockPath))
		}
		if err != nil {
			return r.fail(failf("cannot lock %s: %v", lockPath, err))
		}
		r.lock = lock
	}
	// This start holds the lock without a handover: a live upgrade the
	// journal still calls pending or committed ended with its process.
	handover.Update(r.stateDir, func(j *handover.Journal) {
		if a, ok := j.ColdStart(time.Now().UnixMilli()); ok {
			r.log.Error("upgrade: the last live upgrade did not survive", "attempt", a.ID, "target", a.Target, "reason", a.Reason)
			// server status says it too: the programs of that upgrade
			// ended with it, and this start is a cold one.
			if r.upgrade == nil {
				r.upgrade = &proto.Upgraded{ID: a.ID, From: a.From, To: a.To, At: a.At, Error: "the new build ended: " + a.Reason, Cold: true}
			}
		}
	})

	// (5) An existing socket file: accepted is an error, refused removes it.
	if err := clearStaleSocket(r.socket); err != nil {
		return r.fail(err)
	}

	if err := r.environment(); err != nil {
		return r.fail(err)
	}

	// (9) The emulator identity, once, before the first terminal exists.
	host := shortHost()
	setIdentity("devtools", r.version, host)

	// (10) Load the state, listen with mode 0600, serve.
	o := r.options(host)
	srv, err := newServer(o)
	if err != nil {
		return r.fail(failf("cannot start: %v", err))
	}
	if r.upgrade != nil {
		if s, ok := srv.(interface{ SetUpgraded(proto.Upgraded) }); ok {
			s.SetUpgraded(*r.upgrade)
		}
	}
	l, err := listen(r.socket)
	if err != nil {
		return r.fail(err)
	}
	r.ul, _ = l.(*net.UnixListener)
	r.log.Info("listening", "socket", r.socket, "version", r.version, "pid", os.Getpid())

	return r.serve(srv, newPeerListener(l, ownUID(), peerUID, r.log), sigs)
}

// preconditions are steps 1 to 3: the shell, the state directory and the
// socket path.
func (r *run) preconditions() error {
	// (1) SHELL is set and executable.
	if err := checkShell(os.Getenv("SHELL")); err != nil {
		return err
	}

	// (2) The state directory exists, is 0700 and belongs to the user.
	dir, err := proto.StateDir()
	if err != nil {
		return failf("no state directory: %v", err)
	}
	if err := ensureStateDir(dir); err != nil {
		return err
	}
	r.stateDir = dir

	// (3) The socket path is at most 103 bytes, in a directory that is as
	// private as the state directory (SEC2).
	sock, err := proto.SocketPath()
	if err != nil {
		return failf("no socket path: %v", err)
	}
	if n := len(sock); n > MaxSocketPath {
		return failf("the socket path %s is %d bytes, longer than the %d a unix socket allows", sock, n, MaxSocketPath)
	}
	if sd := filepath.Dir(sock); sd != filepath.Clean(dir) {
		if err := checkPrivateDir(sd); err != nil {
			return err
		}
	}
	r.socket = sock
	return nil
}

// environment is steps 6 and 7: the open-file limit, the session and the
// home directory.
func (r *run) environment() error {
	// (6) Raise the open-file limit.
	got, err := platform.RaiseFDLimit(FDLimit)
	if err != nil {
		return failf("cannot raise the open-file limit to %d: %v", FDLimit, err)
	}
	if got < FDLimit {
		r.log.Warn("open-file limit below the wanted value", "limit", got, "want", FDLimit)
	}

	// (7) A session of its own, in the home directory.
	if err := newSession(); err != nil {
		r.log.Warn("staying in the caller's session", "reason", err.Error())
	}
	home, err := homeDir()
	if err != nil {
		return err
	}
	if err := os.Chdir(home); err != nil {
		return failf("cannot change to the home directory %s: %v", home, err)
	}
	return nil
}

// options are the server's options of step 10.
func (r *run) options(host string) server.Options {
	o := server.Defaults()
	o.StateDir = r.stateDir
	o.Env = os.Environ()
	o.Version = r.version
	o.Host = host
	o.Program = r.argv0
	if r.selfErr == nil {
		o.Self = r.self
	}
	o.Log = r.log
	r.upgradeOptions(&o)
	return o
}

// serve runs the server until a signal or its own end, then the stop
// steps of boot: remove the socket, release the lock.
func (r *run) serve(srv Server, l net.Listener, sigs chan os.Signal) int {
	defer close(r.serveDone)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l) }()

	var stopped chan struct{} // closed when Shutdown returned
	var watchdog *time.Timer
	stop := func(why string) {
		if stopped != nil {
			return
		}
		r.log.Info("shutting down", "reason", why)
		watchdog = r.startWatchdog()
		stopped = make(chan struct{})
		go func() {
			srv.Shutdown()
			close(stopped)
		}()
	}

	for {
		select {
		case sig := <-sigs:
			if sig == syscall.SIGHUP {
				continue // handled, nothing to do
			}
			stop(sig.String())
		case req := <-r.execc:
			// The live upgrade: the serve loop is the one owner of stop
			// against exec, and the only reader of sigs.
			if stopped != nil {
				req.reply <- errStopping
				continue
			}
			err, sig := r.execStep(req.h, sigs)
			req.reply <- err
			if sig != nil {
				stop(sig.String())
			}
		case err := <-served:
			status := 0
			if stopped == nil {
				// The server ended without being asked to: stop the rest in
				// order and report a failure.
				r.log.Error("server ended on its own", "err", errString(err))
				status = 1
				stop("serve ended")
			} else if err != nil {
				r.log.Error("serve", "err", err.Error())
				status = 1
			}
			r.waitStopped(stopped, sigs)
			l.Close()
			if err := os.Remove(r.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
				r.log.Warn("cannot remove the socket", "socket", r.socket, "err", err.Error())
			}
			r.stopped()
			r.release()
			// The stop is complete: a caller that does not exit at once (a
			// test) must not be ended by the watchdog later.
			watchdog.Stop()
			r.log.Info("stopped", "status", status)
			return status
		}
	}
}

// waitStopped waits until Shutdown returned, ignoring further signals.
func (r *run) waitStopped(stopped <-chan struct{}, sigs <-chan os.Signal) {
	for {
		select {
		case <-stopped:
			return
		case <-sigs:
		case req := <-r.execc:
			req.reply <- errStopping
		}
	}
}

// startWatchdog ends the process with status 1 when the shutdown has not
// finished after watchdogAfter: every remaining pane group is killed, the
// socket is removed, and the lock goes with the process.
func (r *run) startWatchdog() *time.Timer {
	return time.AfterFunc(watchdogAfter, func() {
		r.log.Error("shutdown did not finish; killing the remaining panes", "after", watchdogAfter.String())
		killPaneGroups()
		os.Remove(r.socket)
		exit(1)
	})
}

// checkShell is step 1: SHELL names an absolute path to an executable
// regular file.
func checkShell(shell string) error {
	if shell == "" {
		return failf("SHELL is not set; the server needs it to start the shells of panes")
	}
	if !filepath.IsAbs(shell) {
		return failf("SHELL %s is not an absolute path", shell)
	}
	fi, err := os.Stat(shell)
	if err != nil {
		return failf("SHELL %s cannot be used: %v", shell, unwrapPath(err))
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 || unix.Access(shell, unix.X_OK) != nil {
		return failf("SHELL %s is not executable", shell)
	}
	return nil
}

// ensureStateDir is step 2: it creates the directory with mode 0700 when it
// is missing, refuses one that is not a directory or belongs to another
// user, and corrects a mode other than 0700.
func ensureStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return failf("cannot create the state directory %s: %v", dir, unwrapPath(err))
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return failf("cannot read the state directory %s: %v", dir, unwrapPath(err))
	}
	if err := ownedDir(dir, fi); err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return failf("cannot set the state directory %s to mode 0700: %v", dir, unwrapPath(err))
		}
		fi, err = os.Lstat(dir)
		if err != nil || fi.Mode().Perm() != 0o700 {
			return failf("the state directory %s is not mode 0700", dir)
		}
	}
	return nil
}

// checkPrivateDir checks a socket directory other than the state directory
// (DEVTOOLS_SOCKET names it): it must already be a directory of the user
// with mode 0700. It is never created or changed.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return failf("the socket directory %s cannot be used: %v", dir, unwrapPath(err))
	}
	if err := ownedDir(dir, fi); err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o700 {
		return failf("the socket directory %s is mode %04o, not 0700", dir, fi.Mode().Perm())
	}
	return nil
}

// ownedDir refuses what is not a real directory of this user.
func ownedDir(dir string, fi os.FileInfo) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return failf("%s is a symbolic link, not a directory", dir)
	}
	if !fi.IsDir() {
		return failf("%s is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return failf("cannot read the owner of %s", dir)
	}
	uid := uint32(os.Getuid())
	if uint32(st.Uid) != uid {
		return failf("%s belongs to uid %d, not to this user (uid %d)", dir, st.Uid, uid)
	}
	return nil
}

// clearStaleSocket is step 5. Nothing at the path is fine. A socket that
// accepts a connection belongs to a live server and is an error; one whose
// connect is refused is left over by a server that died and is removed.
// Anything else (not a socket, another error) is an error and nothing is
// removed.
func clearStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return failf("cannot read %s: %v", path, unwrapPath(err))
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return failf("%s exists and is not a socket; it was left as it is", path)
	}
	c, err := net.DialTimeout("unix", path, probeTimeout)
	if err == nil {
		c.Close()
		return failf("a server is already answering at %s", path)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return failf("cannot remove the stale socket %s: %v", path, unwrapPath(err))
		}
		return nil
	}
	if errors.Is(err, syscall.ENOENT) {
		return nil // removed in the meantime
	}
	return failf("cannot tell whether a server answers at %s: %v", path, err)
}

// listen is the listen of step 10: the socket is created under a umask
// that leaves it 0600 from the first moment, then set to 0600 explicitly
// and checked.
func listen(path string) (net.Listener, error) {
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, failf("cannot listen on %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, failf("cannot set %s to mode 0600: %v", path, unwrapPath(err))
	}
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		l.Close()
		return nil, failf("the socket %s is not a socket of mode 0600", path)
	}
	return l, nil
}

// newSession is the setsid of step 7. A process that already leads its
// session (as under systemd and launchd) keeps it; a process group leader
// (a server started by hand from an interactive shell) cannot start a new
// session and stays in the caller's.
func newSession() error {
	if _, err := unix.Setsid(); err == nil {
		return nil
	} else if sid, gerr := unix.Getsid(0); gerr == nil && sid == os.Getpid() {
		return nil
	} else {
		return fmt.Errorf("setsid: %v", err)
	}
}

// homeDir is the directory of step 7: HOME, absolute.
func homeDir() (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", failf("no home directory: %v", err)
		}
		home = h
	}
	if !filepath.IsAbs(home) {
		return "", failf("HOME %s is not an absolute path", home)
	}
	return home, nil
}

// shortHost is the host name up to its first dot, for welcome.host and
// XTVERSION.
func shortHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// unwrapPath drops the path of a *PathError, because every text above
// names the path itself.
func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// oneLine keeps the failure text on one line of stderr.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}
