// Package termtest is the terminal harness of level 5 (10.1.6 of the plan):
// the test is the outer terminal. Start opens a PTY, starts a command with
// the PTY as its controlling terminal, and feeds everything the command
// writes into a vt.Term, the real engine; the replies that terminal returns
// from Replies (DA1, DSR, colour queries, the kitty query) are written back,
// so the command sees a terminal that answers like a real one.
//
// It is a test-only package (import rule 8).
package termtest

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/vt"
)

// DefaultName is the XTVERSION answer of a Terminal whose Options name none.
const DefaultName = "termtest"

// Timeout is how long WaitFor, WaitScreen and Wait wait before they fail
// the test.
var Timeout = 10 * time.Second

// Colors are the colours a Terminal answers OSC 10 and OSC 11 with.
var Colors = vt.Colors{Fg: "#d0d0d0", Bg: "#000000", Dark: true}

// Options configure a Terminal.
type Options struct {
	Cols, Rows int      // 80 x 24 when zero
	Name       string   // XTVERSION name this terminal answers with; default "termtest"
	Env        []string // complete environment of the command; nothing is inherited
	Silent     bool     // answer no queries at all (guard timeout test)
}

// Terminal is a PTY master and the vt.Term that keeps its screen.
//
// XTVERSION is the one query the Terminal answers itself, with Options.Name:
// the engine's answer is the identity of the whole process (vt.SetIdentity,
// once per process), and a test process may hold a server of its own
// (servertest) whose panes must keep answering "devtools <version> <host>".
// So the Terminal takes the query out of the stream before the engine sees
// it and writes the answer in its place, in the order of the queries.
type Terminal struct {
	t        *testing.T
	o        Options
	master   *os.File
	tty      string
	proc     *os.Process
	done     chan struct{} // closed when the command was reaped
	state    *os.ProcessState
	readDone chan struct{}

	mu     sync.Mutex
	vt     vt.Term
	raw    bytes.Buffer
	filter queryFilter
	clips  []string
	bells  int
	closed bool
}

// Start starts argv on a new PTY of the size of o, in a session of its own
// with the PTY as its controlling terminal, and returns the terminal. The
// command's environment is o.Env and nothing else; when o.Env is nil it is
// testkit.CleanEnv of a new short temporary directory. When the test ends
// the command's process group gets SIGHUP and, if it is still there after
// two seconds, SIGKILL; then the PTY and the engine are closed.
func Start(t *testing.T, argv []string, o Options) *Terminal {
	t.Helper()
	testkit.RequirePTY(t)
	if len(argv) == 0 {
		t.Fatal("termtest: no command")
	}
	if o.Cols <= 0 {
		o.Cols = 80
	}
	if o.Rows <= 0 {
		o.Rows = 24
	}
	if o.Name == "" {
		o.Name = DefaultName
	}
	env := o.Env
	if env == nil {
		env = testkit.CleanEnv(testkit.ShortTempDir(t))
	}
	x := &Terminal{t: t, o: o, done: make(chan struct{}), readDone: make(chan struct{})}
	x.vt = vt.New(vt.Options{
		Cols: o.Cols, Rows: o.Rows, History: 1000, Colors: Colors,
		// The callbacks run inside Write, under x.mu.
		OnBell:      func() { x.bells++ },
		OnClipboard: func(b []byte) { x.clips = append(x.clips, string(b)) },
	})

	master, slave, err := pty.Open()
	if err != nil {
		x.vt.Close()
		t.Fatalf("termtest: open a PTY: %v", err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(o.Cols), Rows: uint16(o.Rows)}); err != nil {
		master.Close()
		slave.Close()
		x.vt.Close()
		t.Fatalf("termtest: size the PTY: %v", err)
	}
	x.master, x.tty = master, slave.Name()
	dir := ""
	if home, ok := testkit.LookupEnv(env, "HOME"); ok {
		dir = home
	}
	p, err := os.StartProcess(argv[0], argv, &os.ProcAttr{
		Dir: dir, Env: env, Files: []*os.File{slave, slave, slave},
		Sys: &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0},
	})
	slave.Close()
	if err != nil {
		master.Close()
		x.vt.Close()
		t.Fatalf("termtest: start %s: %v", strings.Join(argv, " "), err)
	}
	x.proc = p
	go func() {
		st, _ := p.Wait()
		x.state = st
		close(x.done)
	}()
	go x.readLoop()
	t.Cleanup(x.close)
	return x
}

// TTY returns the path of the terminal device the command runs on.
func (x *Terminal) TTY() string { return x.tty }

// Pid returns the pid of the command.
func (x *Terminal) Pid() int { return x.proc.Pid }

// readLoop feeds everything the command writes into the engine and writes
// the replies back.
func (x *Terminal) readLoop() {
	defer close(x.readDone)
	buf := make([]byte, 32<<10)
	for {
		n, err := x.master.Read(buf)
		if n > 0 {
			x.feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// feed hands one read to the engine, piece by piece around the XTVERSION
// queries, and writes the replies in the order of the queries.
func (x *Terminal) feed(b []byte) {
	x.mu.Lock()
	if x.closed {
		x.mu.Unlock()
		return
	}
	x.raw.Write(b)
	var out []byte
	for _, piece := range x.filter.split(b) {
		if piece.query {
			out = append(out, xtversionReply(x.o.Name)...)
			continue
		}
		x.vt.Write(piece.data)
		out = append(out, x.vt.Replies()...)
	}
	x.mu.Unlock()
	if len(out) > 0 && !x.o.Silent {
		x.master.Write(out)
	}
}

// xtversionReply is the answer to XTVERSION: DCS > | text ST.
func xtversionReply(name string) []byte {
	return []byte("\x1bP>|" + name + "\x1b\\")
}

// piece is a run of command output, or one XTVERSION query.
type piece struct {
	data  []byte
	query bool
}

// xtversionQueries are the two forms of XTVERSION: CSI > q and CSI > 0 q.
var xtversionQueries = [][]byte{[]byte("\x1b[>q"), []byte("\x1b[>0q")}

// queryFilter splits a byte stream at the XTVERSION queries. A query cut by
// a read is held back until the next read completes or refutes it.
type queryFilter struct{ pending []byte }

func (f *queryFilter) split(b []byte) []piece {
	data := append(f.pending, b...)
	f.pending = nil
	var out []piece
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] != 0x1b {
			continue
		}
		rest := data[i:]
		matched, partial := 0, false
		for _, q := range xtversionQueries {
			if bytes.HasPrefix(rest, q) {
				matched = len(q)
			} else if len(rest) < len(q) && bytes.HasPrefix(q, rest) {
				partial = true
			}
		}
		switch {
		case matched > 0:
			if i > start {
				out = append(out, piece{data: data[start:i]})
			}
			out = append(out, piece{query: true})
			start = i + matched
			i = start - 1
		case partial:
			if i > start {
				out = append(out, piece{data: data[start:i]})
			}
			f.pending = append([]byte(nil), rest...)
			return out
		}
	}
	if start < len(data) {
		out = append(out, piece{data: data[start:]})
	}
	return out
}

// Type writes raw bytes to the command, as a terminal sends them.
func (x *Terminal) Type(s string) {
	x.t.Helper()
	if _, err := x.master.WriteString(s); err != nil {
		x.t.Fatalf("termtest: type into the terminal: %v", err)
	}
}

// Resize changes the size of the screen and of the PTY; the kernel sends
// SIGWINCH to the command's foreground group.
func (x *Terminal) Resize(cols, rows int) {
	x.t.Helper()
	x.mu.Lock()
	x.vt.Resize(cols, rows)
	out := x.vt.Replies()
	x.mu.Unlock()
	if err := pty.Setsize(x.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		x.t.Fatalf("termtest: resize the PTY: %v", err)
	}
	if len(out) > 0 && !x.o.Silent {
		x.master.Write(out)
	}
}

// Screen returns the screen in the form of the golden files (10.1.2): one
// wire row per screen row and the cursor.
func (x *Terminal) Screen() golden.Screen {
	x.mu.Lock()
	defer x.mu.Unlock()
	cols, rows := x.vt.Size()
	s := golden.Screen{Cols: cols, Rows: rows, Lines: make([]string, rows)}
	for y := range rows {
		s.Lines[y] = x.vt.Row(y)
	}
	c := x.vt.Cursor()
	s.CursorX, s.CursorY, s.CursorVisible = c.X, c.Y, c.Visible
	return s
}

// WaitFor waits until a row of the screen contains text and returns that
// screen.
func (x *Terminal) WaitFor(text string) golden.Screen {
	x.t.Helper()
	return x.WaitScreen(strings.TrimSpace(text), func(s golden.Screen) bool { return s.Contains(text) })
}

// WaitScreen waits until pred holds for the screen and returns that screen.
// what names the awaited screen in the failure, which shows the screen as it
// was.
func (x *Terminal) WaitScreen(what string, pred func(golden.Screen) bool) golden.Screen {
	x.t.Helper()
	deadline := time.Now().Add(Timeout)
	for {
		s := x.Screen()
		if pred(s) {
			return s
		}
		if time.Now().After(deadline) {
			x.t.Fatalf("termtest: timed out after %v waiting for %s; the screen:\n%s", Timeout, what, s.Plain())
		}
		time.Sleep(testkit.PollInterval)
	}
}

// Clipboard returns the OSC 52 payloads the command wrote, decoded.
func (x *Terminal) Clipboard() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.clips...)
}

// Bells returns how many times the command rang the bell.
func (x *Terminal) Bells() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.bells
}

// Output returns everything the command wrote so far, escape sequences
// removed.
func (x *Terminal) Output() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return ansi.Strip(x.raw.String())
}

// Exited reports whether the command has ended.
func (x *Terminal) Exited() bool {
	select {
	case <-x.done:
		return true
	default:
		return false
	}
}

// Wait waits for the command to end, at most Timeout, and returns its exit
// code (-1 when a signal ended it) and everything it wrote, escape sequences
// removed.
func (x *Terminal) Wait() (exitCode int, output string) {
	x.t.Helper()
	select {
	case <-x.done:
	case <-time.After(Timeout):
		x.t.Fatalf("termtest: the command did not end within %v; the screen:\n%s", Timeout, x.Screen().Plain())
	}
	// The last output is read before it is returned: the reader ends with
	// EIO once the command and every other holder of the PTY closed it.
	select {
	case <-x.readDone:
	case <-time.After(2 * time.Second):
	}
	code, _ := testkit.ExitStatus(x.state)
	return code, x.Output()
}

// close runs when the test ends.
func (x *Terminal) close() {
	if !x.Exited() {
		pgid := x.proc.Pid
		syscall.Kill(-pgid, syscall.SIGHUP)
		select {
		case <-x.done:
		case <-time.After(2 * time.Second):
			syscall.Kill(-pgid, syscall.SIGKILL)
			<-x.done
		}
	}
	x.master.Close()
	select {
	case <-x.readDone:
	case <-time.After(5 * time.Second):
	}
	x.mu.Lock()
	x.closed = true
	x.vt.Close()
	x.mu.Unlock()
}
