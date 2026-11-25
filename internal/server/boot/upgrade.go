package boot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"devtools/internal/handover"
	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/server"
)

// The live upgrade, boot's part (PLAN-live-upgrade.md 5, 6). The old image:
// the server froze everything and hands over (server.Handover); the serve
// loop, the one owner of stop against exec, duplicates the kept descriptors
// above the reserved floor, writes the payload into an anonymous file,
// journals the attempt, lets the requester know, resets the stop signals
// and execs the new build in the same process. The new image (adopt):
// signals first, the inventory of DEVTOOLS_HANDOVER close-on-exec at once,
// then everything is staged without consuming anything; any failure before
// the commit execs the old build again with the same payload, and only when
// that is impossible does the server start cold.

// healthAfter is how long an adopted server must serve before its attempt
// is journaled ok.
const healthAfter = 60 * time.Second

// The variables of the tests of the live upgrade, honoured only with
// DEVTOOLS_UPGRADE_FORCE=1 (e2e).
const (
	envAuto      = "DEVTOOLS_AUTO_UPGRADE"       // 0: the automatic upgrade is off (default on); not a test knob
	envForce     = "DEVTOOLS_UPGRADE_FORCE"      // 1: server restart --force may upgrade to the running build
	envSettle    = "DEVTOOLS_UPGRADE_SETTLE"     // the settle time of the automatic upgrade
	envInterval  = "DEVTOOLS_UPGRADE_INTERVAL"   // how often the watch of the installed build looks
	envIdle      = "DEVTOOLS_UPGRADE_IDLE"       // the idle time it waits for
	envTargetDir = "DEVTOOLS_UPGRADE_TARGET_DIR" // the automatic upgrade may take builds in this directory
	envFailAdopt = "DEVTOOLS_UPGRADE_FAIL_ADOPT" // 1: the new image refuses to adopt; both: the old one too
	envCrash     = "DEVTOOLS_UPGRADE_CRASH"      // <version>: an image of that version crashes a second after it adopted
	envDelay     = "DEVTOOLS_UPGRADE_DELAY"      // <phase>:<duration>: the attempt waits that long at the phase (testDelay)
)

var errStopping = errors.New("the server is stopping")

// execReq is one exec request of the server to the serve loop.
type execReq struct {
	h     *server.Handover
	reply chan error
}

// exec is server.Options.Exec: it hands the request to the serve loop and
// returns only when the exec did not happen.
func (r *run) exec(h *server.Handover) error {
	req := execReq{h: h, reply: make(chan error, 1)}
	select {
	case r.execc <- req:
	case <-r.serveDone:
		return errStopping
	}
	return <-req.reply
}

// upgradeOptions fill the options of the live upgrade; Program and Self,
// which the watch of the installed build needs too, are options' own.
func (r *run) upgradeOptions(o *server.Options) {
	if r.selfErr != nil || r.argv0 == "" || r.lock == nil {
		if r.selfErr != nil {
			r.log.Warn("upgrade: no live upgrade: the running build is unknown", "err", r.selfErr.Error())
		}
		return
	}
	o.Exec = r.exec
	o.Journal = r.journal
	// On unless the service's environment turns it off: the startup-crash
	// tests under disposable launchd and systemd jobs (internal/e2e/
	// service_test.go) gate this default.
	o.AutoUpgrade = os.Getenv(envAuto) != "0"
	if os.Getenv(envForce) != "1" {
		return
	}
	o.UpgradeForce = true
	o.UpgradeDelay = func(ctx context.Context, phase string) error {
		d := r.testDelay(phase)
		if d == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
			return nil
		}
	}
	for name, d := range map[string]*time.Duration{envSettle: &o.UpgradeSettle, envInterval: &o.UpdateInterval, envIdle: &o.UpgradeIdle} {
		if v, err := time.ParseDuration(os.Getenv(name)); err == nil && v > 0 {
			*d = v
		}
	}
	if dir := os.Getenv(envTargetDir); filepath.IsAbs(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		dir = filepath.Clean(dir)
		o.UpgradeTargetOK = func(path string) bool { return strings.HasPrefix(path, dir+"/") }
		o.UpgradeEvery = time.Second
	}
}

// absArgv0 is the path this process was started as, absolute: as the
// service managers start it (the profile's symlink, nix/service.nix), or
// found on PATH like the shell that started it found it. "" when it cannot
// be told: then nothing is watched and the server cannot upgrade itself.
func absArgv0(argv0 string) string {
	if argv0 == "" {
		return ""
	}
	if strings.Contains(argv0, "/") {
		if p, err := filepath.Abs(argv0); err == nil {
			return p
		}
		return ""
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, argv0)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// testDelay is how long an attempt waits at phase, by DEVTOOLS_UPGRADE_DELAY
// (honoured only with DEVTOOLS_UPGRADE_FORCE=1, e2e): "freeze" (the old
// image, every pane frozen), "exec" (the old image, the attempt accepted,
// a stop still wins), "boundary" (the old image, after the reset of the
// stop signals: a stop ends the process), "stage" (the new image, before
// it stages anything). It logs the wait, so that a test knows it began.
func (r *run) testDelay(phase string) time.Duration {
	if os.Getenv(envForce) != "1" {
		return 0
	}
	p, v, ok := strings.Cut(os.Getenv(envDelay), ":")
	if !ok || p != phase {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	r.log.Info("upgrade: waiting, as "+envDelay+" says", "phase", phase, "for", d.String())
	return d
}

// maxRSS is the peak resident size of this process so far, in bytes
// (getrusage; it counts the image before an exec too on Linux).
func maxRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) << 10
}

// selfExecutable is platform.SelfExecutable; the tests replace it.
var selfExecutable = platform.SelfExecutable

// execStep is the exec of the old image, on the serve loop. It returns
// only when the exec did not happen: the error, and the stop signal that
// came at the boundary, if one did (the caller stops then).
func (r *run) execStep(h *server.Handover, sigs chan os.Signal) (error, os.Signal) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var dups []int
	var payload *os.File
	undo := func() {
		for _, fd := range dups {
			unix.Close(fd)
		}
		if payload != nil {
			payload.Close()
		}
	}
	fail := func(err error) (error, os.Signal) {
		undo()
		r.journal(handover.Attempt{ID: h.Attempt, Target: h.Target.Path, From: r.version, To: h.Header.Upgraded.To, Result: handover.Failed, Reason: err.Error()})
		return err, nil
	}
	// notYet is the end of an attempt that never began: nothing to journal.
	notYet := func(err error) (error, os.Signal) {
		undo()
		return err, nil
	}
	dup := func(fd int, err error) (int, error) {
		if err == nil {
			dups = append(dups, fd)
		}
		return fd, err
	}
	if r.ul == nil || r.lock == nil {
		return fail(errors.New("boot: no listener or lock to hand over"))
	}
	lockFD, err := dup(platform.DupFile(r.lock))
	if err != nil {
		return fail(fmt.Errorf("the lock: %w", err))
	}
	lnFD, err := dup(platform.DupConn(r.ul))
	if err != nil {
		return fail(fmt.Errorf("the listener: %w", err))
	}
	// The rollback handle: the running build's file, open across the exec,
	// so that the new image can exec it again if it cannot adopt. Held
	// open, a store path is a runtime root of the Nix collector (which
	// reads the open files of every process) for as long as a rollback can
	// happen; a file that is not the running image any more (a local build
	// replaced in place) is no rollback, and the attempt does not happen.
	exeFD, err := dup(keepExecutable(r.self))
	if err != nil {
		return fail(fmt.Errorf("the running build cannot be kept for a rollback: %w", err))
	}
	lo, hi := 0, -1
	for i, m := range h.Masters {
		if m == nil {
			continue
		}
		fd, err := dup(platform.DupFile(m))
		if err != nil {
			return fail(fmt.Errorf("pane %s: %w", h.Header.Panes[i].ID, err))
		}
		h.Header.Panes[i].Master = fd
		if hi < lo {
			lo, hi = fd, fd
		}
		lo, hi = min(lo, fd), max(hi, fd)
	}
	if hi < lo {
		lo, hi = lnFD+1, lnFD
	}
	// The range holds the masters and nothing else (handover.Env).
	for _, fd := range []int{lockFD, lnFD, exeFD} {
		if fd >= lo && fd <= hi {
			return fail(errors.New("boot: the kept descriptors are not in order"))
		}
	}
	payload, err = platform.HandoverFile(r.stateDir)
	if err != nil {
		return fail(fmt.Errorf("the payload file: %w", err))
	}
	if err := h.Data.Write(payload, h.Header); err != nil {
		return fail(fmt.Errorf("the payload: %w", err))
	}
	payloadFD := int(payload.Fd())
	env := handover.Env{V: handover.Version, ID: h.Attempt, Attempt: 1, Payload: payloadFD, Lock: lockFD, Listener: lnFD, Lo: lo, Hi: hi, Exe: exeFD, From: r.self.Path}
	// The new image reads the inventory first, and one it cannot read ends
	// every program: what it would refuse is refused here.
	if back, err := handover.ParseEnv(env.String()); err != nil || back != env {
		return fail(fmt.Errorf("the inventory %q would not be read back (%v)", env.String(), err))
	}
	keep := append([]int{payloadFD}, dups...)

	// The target is still the build the preflight checked (and, for the
	// automatic upgrade, still the installed one).
	if h.Verify != nil {
		if err := h.Verify(); err != nil {
			return notYet(err)
		}
	}
	// The journal is the guard against a crash loop: no exec without its
	// record of the attempt.
	if err := r.journal(handover.Attempt{ID: h.Attempt, Target: h.Target.Path, From: r.version, To: h.Header.Upgraded.To, Pid: os.Getpid(), Result: handover.Pending, Auto: h.Auto, Started: time.Now().UnixMilli()}); err != nil {
		return fail(fmt.Errorf("the journal of the upgrade cannot be written: %w", err))
	}

	h.Accept()
	if d := r.testDelay("exec"); d > 0 {
		time.Sleep(d)
	}

	// The boundary: from the reset on, a SIGTERM or SIGINT ends the process
	// at once (the residual window up to the new image's signal.Notify); one
	// caught before is in the channel, and stop wins over the exec.
	signal.Reset(syscall.SIGTERM, syscall.SIGINT)
	if sig := takeStop(sigs); sig != nil {
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
		undo()
		r.journal(handover.Attempt{ID: h.Attempt, Target: h.Target.Path, From: r.version, To: h.Header.Upgraded.To, Result: handover.Failed, Reason: "a stop came first"})
		return errStopping, sig
	}
	if h.Verify != nil {
		if err := h.Verify(); err != nil {
			signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
			return fail(err)
		}
	}
	if d := r.testDelay("boundary"); d > 0 {
		time.Sleep(d)
	}
	argv := []string{r.argv0, "server"}
	envv := append(withoutHandover(os.Environ()), handover.EnvVar+"="+env.String())
	var size int64
	if fi, err := payload.Stat(); err == nil {
		size = fi.Size()
	}
	r.log.Info("upgrade: exec", "attempt", h.Attempt, "target", h.Target.Path, "panes", len(h.Masters), "bytes", size, "maxrss", maxRSS())
	err = execKeeping(h.Target.Path, argv, envv, keep)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	runtime.KeepAlive(payload)
	return fail(err)
}

// execKeeping is platform.ExecKeeping; the tests replace it.
var execKeeping = platform.ExecKeeping

// takeStop is the one filter of the stop signals at both exec boundaries
// (the exec of the new build and the exec back): it takes what is queued
// and returns the first SIGTERM or SIGINT, nil when none came. SIGHUP is
// handled and does nothing, as while serving.
func takeStop(sigs chan os.Signal) os.Signal {
	for {
		select {
		case sig := <-sigs:
			if sig == syscall.SIGHUP {
				continue
			}
			return sig
		default:
			return nil
		}
	}
}

// keepExecutable opens the file of the running build and checks that it is
// the running image; the descriptor (close-on-exec) is the caller's.
func keepExecutable(self platform.Identity) (int, error) {
	f, err := os.Open(self.Path)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	dev, ino, err := platform.FDIdentity(int(f.Fd()))
	if err != nil {
		return -1, err
	}
	if dev != self.Dev || ino != self.Ino {
		return -1, fmt.Errorf("%s is not the file this server runs any more", self.Path)
	}
	return platform.DupFD(int(f.Fd()))
}

func withoutHandover(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, handover.EnvVar+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// journal records an attempt in upgrade.json of the state directory. The
// writers (the serve loop, the health timer, the stop) take turns.
func (r *run) journal(a handover.Attempt) error {
	if r.stateDir == "" {
		return errors.New("no state directory")
	}
	r.jmu.Lock()
	defer r.jmu.Unlock()
	a.At = time.Now().UnixMilli()
	err := handover.Update(r.stateDir, func(j *handover.Journal) {
		if old, ok := j.Get(a.ID); ok {
			if a.Target == "" {
				a.Target = old.Target
			}
			if a.From == "" {
				a.From = old.From
			}
			if a.To == "" {
				a.To = old.To
			}
			if a.Pid == 0 {
				a.Pid = old.Pid
			}
			if a.Started == 0 {
				a.Auto, a.Started = old.Auto, old.Started
			}
		}
		j.Put(a)
	})
	if err != nil {
		r.log.Warn("upgrade: cannot write the journal", "err", err.Error())
	}
	return err
}

// inherited is what the new image wrapped of the inventory, so that a
// fallback closes each descriptor once, through its file.
type inherited struct {
	env     handover.Env
	files   map[int]*os.File
	ln      net.Listener // net.FileListener's own descriptor
	payload *os.File
	reader  *handover.Reader
	masters map[int]*os.File
}

func (in *inherited) file(fd int, name string) *os.File {
	if f := in.files[fd]; f != nil {
		return f
	}
	f := os.NewFile(uintptr(fd), name)
	in.files[fd] = f
	return f
}

// adopt is the start of a new image after a live upgrade.
func (r *run) adopt(v string, sigs chan os.Signal) int {
	env, err := handover.ParseEnv(v)
	if err != nil {
		// Nothing tells which descriptors are what: none of them may reach
		// a child. The lock is one of them, so the start below fails and
		// the process ends; the service manager starts it again, cold.
		r.log.Error("upgrade: the handover inventory is malformed", "err", err.Error())
		if fds, err := platform.OpenFDs(); err == nil {
			for _, fd := range fds {
				if fd > 2 {
					platform.SetCloexec(fd, true)
				}
			}
		}
		return r.start(sigs)
	}
	// No fork may inherit the inventory: close-on-exec first of all.
	inv := &inherited{env: env, files: map[int]*os.File{}, masters: map[int]*os.File{}}
	for _, fd := range r.inventory(env) {
		platform.SetCloexec(fd, true)
	}
	staged, err := r.stage(inv)
	if err != nil {
		return r.fallback(inv, err, sigs)
	}

	// Commit: from here on the old image is no valid rollback.
	srv := staged.Commit()
	if l := inv.files[env.Listener]; l != nil {
		l.Close() // net.FileListener holds its own descriptor of the socket
	}
	inv.payload.Close()
	unix.Close(env.Exe) // the rollback handle: the old image is no rollback any more
	r.adopted, r.attempt = true, env.ID
	if env.Attempt == 2 {
		reason := "the new build could not take over"
		if a, ok := handover.LoadJournal(r.stateDir).Get(env.ID); ok && a.Reason != "" {
			reason = a.Reason
		}
		if u := srv.Upgraded(); u != nil {
			srv.SetUpgraded(proto.Upgraded{ID: env.ID, From: u.From, To: u.To, At: time.Now().UnixMilli(), Kept: u.Kept, Error: reason, Back: true})
		}
		r.log.Error("upgrade: the old build took the programs back", "attempt", env.ID, "reason", reason)
	} else {
		r.journal(handover.Attempt{ID: env.ID, Result: handover.Committed, Pid: os.Getpid()})
		r.healthy = time.AfterFunc(healthAfter, func() {
			r.journal(handover.Attempt{ID: env.ID, Result: handover.OK})
		})
		r.log.Info("upgrade: adopted", "attempt", env.ID, "panes", staged.Panes(), "version", r.version, "maxrss", maxRSS())
		if v := os.Getenv(envCrash); v != "" && v == r.version && os.Getenv(envForce) == "1" {
			// The fault of the e2e tests: a build that takes over and then
			// crashes before its health interval. A cold start of it works.
			r.log.Error("upgrade: crashing in a second, as "+envCrash+" says", "attempt", env.ID)
			time.AfterFunc(time.Second, func() { panic("e2e: " + envCrash + "=" + v) })
		}
	}
	r.log.Info("listening", "socket", r.socket, "version", r.version, "pid", os.Getpid())
	return r.serve(srv, newPeerListener(inv.ln, ownUID(), peerUID, r.log), sigs)
}

// stopped runs when the server stopped: an adopted server that stops
// before its health interval says so in the journal.
func (r *run) stopped() {
	if r.healthy != nil && r.healthy.Stop() {
		r.journal(handover.Attempt{ID: r.attempt, Result: handover.Stopped})
	}
}

// inventory is every descriptor DEVTOOLS_HANDOVER names, the open ones of
// the range included.
func (r *run) inventory(env handover.Env) []int {
	fds := []int{env.Payload, env.Lock, env.Listener, env.Exe}
	if open, err := platform.OpenFDs(); err == nil {
		for _, fd := range open {
			if env.InRange(fd) {
				fds = append(fds, fd)
			}
		}
	}
	return fds
}

// stage reads and checks everything the old image handed over and builds
// the server, without serving, reading a terminal or reaping a child.
func (r *run) stage(in *inherited) (st *server.Staged, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("staging panicked: %v", p)
		}
	}()
	env := in.env
	if cur, _, err := platform.FDLimit(); err != nil || cur <= uint64(env.Hi+64) {
		if _, err := platform.RaiseFDLimit(uint64(env.Hi + 1024)); err != nil {
			return nil, fmt.Errorf("the open-file limit: %w", err)
		}
	}
	if err := r.preconditions(); err != nil {
		return nil, err
	}
	if r.selfErr != nil {
		return nil, r.selfErr
	}
	lockPath := filepath.Join(r.stateDir, LockName)
	if err := sameFile(env.Lock, lockPath); err != nil {
		return nil, fmt.Errorf("the lock: %w", err)
	}
	r.lock = in.file(env.Lock, lockPath)

	in.payload = in.file(env.Payload, "handover")
	in.reader, err = handover.Open(in.payload)
	if err != nil {
		return nil, err
	}
	h := in.reader.Header()
	if h.Attempt != env.ID {
		return nil, fmt.Errorf("the payload is of attempt %s, the inventory of %s", h.Attempt, env.ID)
	}
	if h.Socket != r.socket {
		return nil, fmt.Errorf("the payload's socket is %s, this server's %s", h.Socket, r.socket)
	}
	lf := in.file(env.Listener, "listener")
	l, err := net.FileListener(lf)
	if err != nil {
		return nil, fmt.Errorf("the listener: %w", err)
	}
	in.ln = l
	ul, ok := l.(*net.UnixListener)
	if !ok {
		return nil, errors.New("the listener is not a unix socket")
	}
	if a, ok := ul.Addr().(*net.UnixAddr); !ok || a.Name != r.socket {
		return nil, fmt.Errorf("the listener is bound to %v, not %s", ul.Addr(), r.socket)
	}
	r.ul = ul
	for _, p := range h.Panes {
		if p.Master < 0 {
			continue
		}
		if !env.InRange(p.Master) {
			return nil, fmt.Errorf("pane %s: descriptor %d outside the range %d-%d", p.ID, p.Master, env.Lo, env.Hi)
		}
		in.masters[p.Master] = in.file(p.Master, "/dev/ptmx")
	}
	if d := r.testDelay("stage"); d > 0 {
		time.Sleep(d)
	}
	if err := r.environment(); err != nil {
		return nil, err
	}
	if fault := os.Getenv(envFailAdopt); fault != "" && os.Getenv(envForce) == "1" {
		if fault == "both" || env.Attempt == 1 {
			return nil, fmt.Errorf("refused by %s=%s", envFailAdopt, fault)
		}
	}
	host := shortHost()
	setIdentity("devtools", r.version, host)
	o := r.options(host)
	st, err = stageServer(o, in.reader, in.masters)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// stageServer is server.Stage; the tests replace it.
var stageServer = server.Stage

// sameFile checks that the descriptor fd is the file at path.
func sameFile(fd int, path string) error {
	dev, ino, err := platform.FDIdentity(fd)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return err
	}
	if uint64(st.Dev) != dev || uint64(st.Ino) != ino {
		return fmt.Errorf("descriptor %d is not %s", fd, path)
	}
	return nil
}

// fallback gives the process back when the new image cannot adopt: the
// old build, with the same payload and inventory (attempt 2), which still
// tells the truth, because staging consumed nothing; when that is not
// possible, a cold start.
func (r *run) fallback(in *inherited, why error, sigs chan os.Signal) int {
	env := in.env
	r.log.Error("upgrade: this build cannot take the programs over", "attempt", env.ID, "attempt-no", env.Attempt, "err", why.Error())
	if env.Attempt == 1 {
		r.journal(handover.Attempt{ID: env.ID, Result: handover.Fallback, Reason: oneLine(why.Error())})
		err, sig := r.execBack(in, sigs)
		if sig != nil {
			return r.stopInstead(in, why, sig)
		}
		r.log.Error("upgrade: the old build cannot be started again", "from", env.From, "err", err.Error())
		why = fmt.Errorf("%v; the old build: %v", why, err)
	}
	return r.coldRecovery(in, why, sigs)
}

// execBack execs the old build with attempt 2. It returns only when that
// did not happen: the error, and the stop signal that came at the boundary,
// if one did (the caller stops then, in order, instead of starting cold).
func (r *run) execBack(in *inherited, sigs chan os.Signal) (error, os.Signal) {
	env := in.env
	if err := rollbackTarget(env); err != nil {
		return err, nil
	}
	if in.ln != nil {
		in.ln.Close() // its own descriptor; the inventory's stays
		in.ln = nil
	}
	back := env
	back.Attempt = 2
	argv := []string{r.argv0, "server"}
	if r.argv0 == "" {
		argv[0] = env.From
	}
	envv := append(withoutHandover(os.Environ()), handover.EnvVar+"="+back.String())
	signal.Reset(syscall.SIGTERM, syscall.SIGINT)
	if sig := takeStop(sigs); sig != nil {
		signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
		return errStopping, sig
	}
	err := execKeeping(env.From, argv, envv, r.inventory(env))
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	runtime.KeepAlive(in)
	return err, nil
}

// rollbackTarget checks that env.From is still the file the old image ran
// from: the one the rollback handle (env.Exe) holds open, executable.
func rollbackTarget(env handover.Env) error {
	dev, ino, err := platform.FDIdentity(env.Exe)
	if err != nil {
		return fmt.Errorf("the rollback handle: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Stat(env.From, &st); err != nil {
		return fmt.Errorf("%s: %w", env.From, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", env.From)
	}
	if uint64(st.Dev) != dev || uint64(st.Ino) != ino {
		return fmt.Errorf("%s is not the build the old server ran any more", env.From)
	}
	return nil
}

// stopInstead is the end of a failed adoption when a stop came before the
// old build could run again: the programs of the panes end, as in every
// stop, and the process ends in order without serving.
func (r *run) stopInstead(in *inherited, why error, sig os.Signal) int {
	env := in.env
	r.log.Info("shutting down", "reason", sig.String(), "attempt", env.ID)
	if in.ln != nil {
		in.ln.Close()
		in.ln = nil
	}
	r.closeInherited(in, false)
	wait := reapOrphans(r.log)
	r.journal(handover.Attempt{ID: env.ID, Result: handover.Fallback, Reason: oneLine(why.Error()) + "; stopped by " + sig.String() + " before the old build ran again"})
	wait()
	r.release()
	r.log.Info("stopped", "status", 0)
	return 0
}

// closeInherited closes every descriptor of the inventory, the lock only
// when keepLock is false or it is not the state directory's lock.
func (r *run) closeInherited(in *inherited, keepLock bool) {
	env := in.env
	keepLock = keepLock && r.lock != nil && sameFile(env.Lock, filepath.Join(r.stateDir, LockName)) == nil
	for _, fd := range r.inventory(env) {
		if fd == env.Lock && keepLock {
			continue
		}
		if f := in.files[fd]; f != nil {
			f.Close()
			delete(in.files, fd)
			continue
		}
		unix.Close(fd)
	}
	if !keepLock {
		r.lock = nil
	}
}

// coldRecovery is the start when no build can adopt: every inherited
// descriptor but the lock is closed, the programs of the panes are ended
// and reaped, and the server starts as after a restart, from the state the
// old image saved before the exec. The windows see a new boot.
func (r *run) coldRecovery(in *inherited, why error, sigs chan os.Signal) int {
	env := in.env
	r.log.Error("upgrade: starting cold; the programs of the panes end", "attempt", env.ID, "reason", why.Error())
	if in.ln != nil {
		in.ln.Close()
	}
	r.closeInherited(in, true)
	reapOrphans(r.log)
	r.journal(handover.Attempt{ID: env.ID, Result: handover.Cold, Reason: oneLine(why.Error())})
	r.upgrade = &proto.Upgraded{ID: env.ID, At: time.Now().UnixMilli(), Error: oneLine(why.Error()), Cold: true}
	return r.start(sigs)
}

// reapOrphans ends the children this process inherited from the old image
// (the programs of panes nobody adopted): SIGHUP to each one's process
// group, SIGKILL after a grace, and reaps them. The returned wait waits
// until every one was reaped.
func reapOrphans(log interface{ Warn(string, ...any) }) (wait func()) {
	var wg sync.WaitGroup
	procs, err := platform.Procs()
	if err != nil {
		log.Warn("upgrade: cannot read the process table to end the old programs", "err", err.Error())
		return wg.Wait
	}
	self := os.Getpid()
	for _, p := range procs {
		if p.PPid != self {
			continue
		}
		pid, pgid := p.Pid, p.Pgid
		if pgid > 1 && pgid != syscall.Getpgrp() {
			syscall.Kill(-pgid, syscall.SIGHUP)
		} else {
			syscall.Kill(pid, syscall.SIGHUP)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			reapChild(pid, orphanGrace)
		}()
	}
	return wg.Wait
}

// orphanGrace is how long an old program has between SIGHUP and SIGKILL.
const orphanGrace = time.Second

// killLater runs f after d unless the returned stop, called first, reports
// true; the tests replace it.
var killLater = func(d time.Duration, f func()) (stop func() bool) {
	return time.AfterFunc(d, f).Stop
}

// reapChild sends the child pid SIGKILL after grace and reaps it. Its pid
// stays its own until the reap: the child is waited for without being
// reaped first, and a SIGKILL that has begun ends before the reap, so the
// delayed signal can never reach another process that got the pid.
func reapChild(pid int, grace time.Duration) {
	fired := make(chan struct{})
	stop := killLater(grace, func() {
		defer close(fired)
		syscall.Kill(pid, syscall.SIGKILL)
	})
	pane.WaitExited(pid)
	if !stop() {
		<-fired
	}
	var ws unix.WaitStatus
	for {
		if _, err := unix.Wait4(pid, &ws, 0, nil); err != unix.EINTR {
			break
		}
	}
}
