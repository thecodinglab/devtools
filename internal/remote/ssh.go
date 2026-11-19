package remote

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"devtools/internal/proto"
)

const protoGen = proto.Gen

// sshOptions are the fixed options of 3.5.2 and 4.4. Nothing forwards
// anything (SEC14): the user's ssh config decides.
var sshOptions = []string{
	"-T",
	"-o", "BatchMode=yes",
	"-o", "StrictHostKeyChecking=yes",
	"-o", "ConnectTimeout=10",
	"-o", "ConnectionAttempts=1",
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=4",
}

// SSHArgs is the complete argument vector after "ssh" (3.5.2): the fixed
// options, "--", the alias and the constant words "devtools bridge". The
// alias is the only variable argument and comes after "--", so it can never
// be read as an option (SEC3, SEC5). It is the only constructor of an ssh
// command line in the tree (3.4 e).
func SSHArgs(machine string) []string {
	args := make([]string, 0, len(sshOptions)+4)
	args = append(args, sshOptions...)
	return append(args, "--", machine, "devtools", "bridge")
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

// startSSH runs prog with SSHArgs(machine) from an argument vector, never
// through a shell (SEC3).
func startSSH(prog, machine string) (*sshProc, error) {
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
	p.cmd = exec.Command(prog, SSHArgs(machine)...)
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
	markerReady markerResult = iota // the ready line of this generation
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
		case l == readyLine && len(line) <= maxLine:
			return markerReady, l, nil
		case strings.HasPrefix(l, readyPrefix) && len(line) <= maxLine:
			gen := strings.TrimPrefix(l, readyPrefix)
			if n, err := strconv.Atoi(gen); err == nil && n != protoGen {
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
