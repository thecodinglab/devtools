package remote

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"devtools/internal/proto"
)

const protoGen = proto.Gen

// sshOptions are the fixed options of 3.5.2 and 4.4. Nothing forwards
// anything (SEC14): the user's ssh config decides. ServerAliveCountMax=40
// at ServerAliveInterval=15 keeps a shared connection through 10 minutes
// without an answer, a train in a tunnel.
var sshOptions = []string{
	"-T",
	"-o", "BatchMode=yes",
	"-o", "StrictHostKeyChecking=yes",
	"-o", "ConnectTimeout=10",
	"-o", "ConnectionAttempts=1",
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=40",
}

// SSHArgs is the complete argument vector after "ssh" (3.5.2) of a bridge
// that shares no connection: the fixed options, "--", the alias and the
// constant words "devtools bridge". The alias is the only variable
// argument and comes after "--", so it can never be read as an option
// (SEC3, SEC5). SSHArgs, bridgeArgs and checkArgs, all through sshArgv,
// are the only constructors of an ssh command line in the tree (3.4 e).
func SSHArgs(machine string) []string {
	return bridgeArgs(machine, sharing{})
}

// sharing is how one ssh uses the shared connection of its alias (3.5.2):
// the first bridge to a machine becomes the master of a control socket,
// and the bridges after it, of every window on this machine, run over it
// without a new login. ssh owns the master: ControlPersist keeps it for
// 15 minutes after its last bridge ended.
type sharing struct {
	path  string // the ControlPath, <ControlDir>/%C; "" shares nothing
	login bool   // may log in and become the master; else only reuse one
}

// options are the ssh options of s.
func (s sharing) options() []string {
	switch {
	case s.path == "":
		return nil
	case s.login:
		return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + s.path, "-o", "ControlPersist=15m"}
	}
	return noLogin(s.path)
}

// noLogin are the options of an ssh that must not log in (3.5.2): it only
// talks to the master at the ControlPath path. ssh has no mode that only
// multiplexes: when the master cannot be reached, refuses the session (its
// sessions are full, MaxSessions) or answers no hello, ssh connects and
// logs in as usual, and a ControlMaster of yes or ask in the user's config
// skips the master even for "-O check". So ControlMaster=no, which the
// command line sets before any config, and PreferredAuthentications=none:
// such a login offers no method but "none", which a server that does not
// let anyone in refuses with "Permission denied", and asks neither the ssh
// agent nor a key. The hash %C covers neither option, so the ControlPath
// is the one of the master.
func noLogin(path string) []string {
	return []string{"-o", "ControlMaster=no", "-o", "ControlPath=" + path, "-o", "PreferredAuthentications=none"}
}

// bridgeArgs is the argument vector of the bridge to machine with the
// sharing s.
func bridgeArgs(machine string, s sharing) []string {
	return sshArgv(s.options(), machine, "devtools", "bridge")
}

// checkArgs asks the master at the ControlPath path whether it still runs:
// "-O check" talks only to the local control socket, and noLogin keeps it
// from logging in when it cannot.
func checkArgs(machine, path string) []string {
	return sshArgv(append(noLogin(path), "-O", "check"), machine)
}

// sshArgv is the fixed options, extra, "--", the alias and command.
func sshArgv(extra []string, machine string, command ...string) []string {
	args := make([]string, 0, len(sshOptions)+len(extra)+2+len(command))
	args = append(args, sshOptions...)
	args = append(args, extra...)
	args = append(args, "--", machine)
	return append(args, command...)
}

// The room a control socket needs beyond ControlDir: "/", the 40 hex
// digits of %C, and the temporary suffix ssh binds a new socket under
// before it renames it (17 bytes in OpenSSH), with a margin.
const (
	controlHash   = 40
	controlSuffix = 20
)

// controlPath makes dir and returns <dir>/%C, the ControlPath of the
// shared connections (3.5.2). ok is false, and nothing is shared, when dir
// is "" or relative, holds a byte the option parser of ssh reads (white
// space, quotes, backslash, "%", "$"), leaves no room for a socket path
// within the platform's limit (104 bytes on macOS, 108 on Linux), or is
// not a directory owned by us with mode 0700, as private as the state
// directory (SEC2): whoever reaches a control socket uses its login.
func controlPath(dir string) (path string, ok bool) {
	if dir == "" || !filepath.IsAbs(dir) || !plainPath(dir) {
		return "", false
	}
	dir = filepath.Clean(dir)
	if len(dir)+len("/")+controlHash+controlSuffix > len(syscall.RawSockaddrUnix{}.Path) {
		return "", false
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return "", false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return "", false
	}
	return dir + "/%C", true
}

// plainPath reports whether p has only bytes that ssh takes as they are
// in an option value: letters, digits, "/._-+,@:~" and non-ASCII.
func plainPath(p string) bool {
	for i := 0; i < len(p); i++ {
		c := p[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("/._-+,@:~", c) >= 0 || c >= 0x80
		if !ok {
			return false
		}
	}
	return true
}

// sharedUp runs prog with checkArgs(machine, path) from an argument vector
// (SEC3) and reports whether it exited 0: the master of machine runs. It
// is killed when ctx ends.
func sharedUp(ctx context.Context, prog, machine, path string) bool {
	if prog == "" {
		prog = "ssh"
	}
	cmd := exec.CommandContext(ctx, prog, checkArgs(machine, path)...)
	cmd.WaitDelay = time.Second
	return cmd.Run() == nil
}

// sshProc is one running ssh: its pipes, the tail of its stderr and its
// exit status once it ended.
type sshProc struct {
	cmd    *exec.Cmd
	stdin  *os.File // our end: the bridge's stdin
	stdout *os.File // our end: the bridge's stdout
	stderr *tail

	exited chan struct{} // closed once the process was reaped
	code   int           // exit status, -1 for a signal; valid after exited

	killOnce sync.Once
}

// startSSH runs prog with args, a vector of bridgeArgs, never through a
// shell (SEC3).
func startSSH(prog string, args []string) (*sshProc, error) {
	if prog == "" {
		prog = "ssh"
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	p := &sshProc{stdin: inW, stdout: outR, stderr: &tail{max: keptStderr}, exited: make(chan struct{})}
	p.cmd = exec.Command(prog, args...)
	p.cmd.Stdin = inR
	p.cmd.Stdout = outW
	p.cmd.Stderr = p.stderr
	// Something ssh started may hold its stderr open after ssh ended; Wait
	// gives up on it after this long.
	p.cmd.WaitDelay = time.Second
	err = p.cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, err
	}
	go func() {
		p.cmd.Wait()
		p.code = p.cmd.ProcessState.ExitCode()
		close(p.exited)
	}()
	return p, nil
}

// kill ends the process and closes our ends of its pipes, which unblocks
// every read and write on them. It may be called any number of times.
func (p *sshProc) kill() {
	p.killOnce.Do(func() {
		p.cmd.Process.Kill()
		p.stdin.Close()
		p.stdout.Close()
	})
}

// wait returns the exit status once the process ended. When it has not
// ended within d (wall time) it is killed first.
func (p *sshProc) wait(d time.Duration) int {
	select {
	case <-p.exited:
	case <-time.After(d):
		p.kill()
		<-p.exited
	}
	return p.code
}

// classify waits for the end of the process and classifies it.
func (p *sshProc) classify(bridgeLine string) *Failure {
	code := p.wait(2 * time.Second)
	return Classify(code, p.stderr.Bytes(), bridgeLine)
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tail) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

// markerResult is what readMarker found.
type markerResult int

const (
	markerReady markerResult = iota // the ready line of a generation this build speaks
	markerError                     // a devtools-bridge-error line
	markerEnd                       // the stream ended before either
	markerFail                      // a Failure: too much before the line, another generation
)

// readMarker skips at most maxBanner bytes of other output (login banners,
// shell noise) up to the bridge's line (4.4). It reads byte by byte from r,
// so the protocol bytes after the line stay in r, and it never holds more
// than maxBanner plus one line of bounded length.
func readMarker(r *bufio.Reader) (markerResult, string, *Failure) {
	const maxLine = 1024 // the longest line of the bridge that is accepted
	skipped := 0
	var line []byte
	tooMuch := func() *Failure {
		return failure(Protocol, "more than 64 KiB of output before devtools-bridge-ready", "")
	}
	for {
		c, err := r.ReadByte()
		if err != nil {
			return markerEnd, "", nil
		}
		if c != '\n' {
			line = append(line, c)
			// The line under construction can only still be the bridge's
			// line while it is short; beyond that it is banner and counts.
			if skipped+len(line) > maxBanner+maxLine {
				return markerFail, "", tooMuch()
			}
			continue
		}
		l := strings.TrimSuffix(string(line), "\r")
		switch {
		case strings.HasPrefix(l, readyPrefix) && len(line) <= maxLine:
			gen := strings.TrimPrefix(l, readyPrefix)
			if n, err := strconv.Atoi(gen); err == nil && strconv.Itoa(n) == gen {
				if proto.Speaks(n) {
					return markerReady, l, nil
				}
				return markerFail, l, failure(Incompatible,
					"the bridge speaks protocol "+strconv.Itoa(n)+", this client "+strconv.Itoa(protoGen), "")
			}
		case strings.HasPrefix(l, errorPrefix) && len(line) <= maxLine:
			return markerError, l, nil
		}
		skipped += len(line) + 1
		line = line[:0]
		if skipped > maxBanner {
			return markerFail, "", tooMuch()
		}
	}
}
