package testkit

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// NoPTY is the variable of the one skip switch of the tree (6.1 rule 4):
// DEVTOOLS_TEST_NO_PTY=1 skips every test that needs a PTY, for the Nix
// sandbox where none can be opened.
const NoPTY = "DEVTOOLS_TEST_NO_PTY"

// RequirePTY skips the test when DEVTOOLS_TEST_NO_PTY=1.
// Otherwise it opens and closes one PTY and fails the test if that does not
// work, so a run cannot pass by silently skipping.
func RequirePTY(t *testing.T) {
	t.Helper()
	requirePTY(t, pty.Open)
}

// ptyT is the part of testing.T that RequirePTY uses; its test hands in a
// recorder.
type ptyT interface {
	Helper()
	Skip(args ...any)
	Fatalf(format string, args ...any)
}

func requirePTY(t ptyT, open func() (*os.File, *os.File, error)) {
	t.Helper()
	if os.Getenv(NoPTY) == "1" {
		t.Skip("no PTY: " + NoPTY + "=1")
		return
	}
	master, slave, err := open()
	if err != nil {
		t.Fatalf("testkit: cannot open a PTY (set %s=1 only where none can exist): %v", NoPTY, err)
		return
	}
	slave.Close()
	master.Close()
}

// OpenPTY opens a PTY pair that is closed when the test ends: the master,
// which the test drives as the terminal, and the slave, the terminal device
// of the code under test. The test is skipped as RequirePTY says.
func OpenPTY(t *testing.T, cols, rows int) (master, slave *os.File) {
	t.Helper()
	RequirePTY(t)
	m, s, err := pty.Open()
	if err != nil {
		t.Fatalf("testkit: open a PTY: %v", err)
	}
	if err := pty.Setsize(m, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		m.Close()
		s.Close()
		t.Fatalf("testkit: size the PTY: %v", err)
	}
	t.Cleanup(func() {
		s.Close()
		m.Close()
	})
	return m, s
}

// PTY is a process started with a PTY as its controlling terminal; the
// test is the terminal. Everything the process writes is collected.
type PTY struct {
	t      *testing.T
	Master *os.File
	Cmd    *exec.Cmd

	mu     sync.Mutex
	out    bytes.Buffer
	read   chan struct{} // closed when the reader has ended
	waited chan struct{} // closed when the process has been waited for
	state  *os.ProcessState
}

// StartPTY starts argv with env as its complete environment (nothing is
// inherited; use CleanEnv) on a new PTY of cols x rows, in a new session
// with the PTY as its controlling terminal. The test is skipped as
// RequirePTY says. When the test ends the process is killed if it still
// runs, and the PTY is closed.
func StartPTY(t *testing.T, argv, env []string, cols, rows int) *PTY {
	t.Helper()
	RequirePTY(t)
	if len(argv) == 0 {
		t.Fatal("testkit: StartPTY without a program")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append([]string{}, env...)
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	m, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		t.Fatalf("testkit: start %v on a PTY: %v", argv, err)
	}
	p := &PTY{t: t, Master: m, Cmd: cmd, read: make(chan struct{}), waited: make(chan struct{})}
	go p.readLoop()
	go func() {
		cmd.Wait()
		p.mu.Lock()
		p.state = cmd.ProcessState
		p.mu.Unlock()
		close(p.waited)
	}()
	t.Cleanup(func() {
		select {
		case <-p.waited:
		default:
			cmd.Process.Kill()
			<-p.waited
		}
		// The read ends when the last slave descriptor is closed; a
		// descendant that kept one open does not hold up the test.
		select {
		case <-p.read:
		case <-time.After(5 * time.Second):
		}
		m.Close()
	})
	return p
}

func (p *PTY) readLoop() {
	defer close(p.read)
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Master.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.out.Write(buf[:n])
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Write writes s to the process as typed on the terminal and fails the test
// when that does not work.
func (p *PTY) Write(s string) {
	p.t.Helper()
	if _, err := io.WriteString(p.Master, s); err != nil {
		p.t.Fatalf("testkit: write to the PTY: %v", err)
	}
}

// Output returns everything the process wrote so far.
func (p *PTY) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

// WaitFor waits until the output contains text and returns the output.
func (p *PTY) WaitFor(text string, timeout time.Duration) string {
	p.t.Helper()
	var out string
	if !poll(timeout, func() bool {
		out = p.Output()
		return strings.Contains(out, text)
	}) {
		p.t.Fatalf("testkit: timed out after %v waiting for %q; output so far:\n%q", timeout, text, out)
	}
	return out
}

// Resize changes the size of the PTY; the kernel sends SIGWINCH to the
// foreground process group.
func (p *PTY) Resize(cols, rows int) {
	p.t.Helper()
	if err := pty.Setsize(p.Master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		p.t.Fatalf("testkit: resize the PTY: %v", err)
	}
}

// Wait waits until the process ended and returns how it ended: the exit
// code, or -1 and the signal that ended it.
func (p *PTY) Wait(timeout time.Duration) (code int, sig syscall.Signal) {
	p.t.Helper()
	select {
	case <-p.waited:
	case <-time.After(timeout):
		p.t.Fatalf("testkit: the process did not end within %v; output:\n%q", timeout, p.Output())
	}
	p.mu.Lock()
	st := p.state
	p.mu.Unlock()
	return ExitStatus(st)
}

// ExitStatus returns the exit code of a process that exited, or -1 and the
// signal of one that a signal ended.
func ExitStatus(st *os.ProcessState) (code int, sig syscall.Signal) {
	if st == nil {
		return -1, 0
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, ws.Signal()
	}
	return st.ExitCode(), 0
}
