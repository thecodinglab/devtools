package e2e

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"devtools/internal/proto"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/vt"
)

// Term is a fake outer terminal: it owns a PTY, runs a client in it,
// answers XTVERSION and DA1 like a plain terminal, and keeps a screen
// through internal/vt.
//
// The screen is a vt.Term made by vt.New, the real engine (10.1.9 point 2).
// Everything the program writes is fed to it, and its replies (DA1, DSR,
// OSC 10 and 11, the kitty query) are written back to the program. XTVERSION
// is the one query the Term answers itself: the engine's answer is the one
// identity of the process, while a Term answers with TermOptions.Identity,
// so each Term of a test can stand in another context.
//
// The shape is that of termtest.Terminal (10.1.6). termtest is P1-18's
// (wave 3) and did not exist when this package was written (wave 1), so
// the Term is built on the engine and creack/pty directly; it does not
// import termtest.
type Term struct {
	t        *testing.T
	m        *Machine
	o        TermOptions
	master   *os.File
	tty      string
	p        *proc
	readDone chan struct{}

	mu     sync.Mutex
	vt     vt.Term
	raw    bytes.Buffer
	filter queryFilter
	clips  []string
	bells  atomic.Int64
}

// TermOptions are the options of a Term.
type TermOptions struct {
	Cols, Rows int
	Env        []string // the marks of a context, e.g. TMUX=... and TMUX_PANE=..., or DEVTOOLS_MULTIPLEXER=tmux
	Identity   string   // XTVERSION answer; "" answers DA1 only
	Silent     bool     // answers nothing: the probe must time out
}

// attachHooks run in Attach after the PTY is open and before the program
// starts, with the path of the terminal device. The fake multiplexers of
// the test files use them to report this device for their pane.
var attachHooks []func(m *Machine, o TermOptions, tty string)

// The colours the fake terminal answers OSC 10 and 11 with.
var termColors = vt.Colors{Fg: "#d0d0d0", Bg: "#000000", Dark: true}

// Attach starts `devtools <args>` (`devtools attach` without args) of
// machine m on a new PTY of the size of o (80 x 24 when zero), with
// m.Env(o.Env...) as its complete environment, and returns the terminal.
// The program runs in a new session with the PTY as its controlling
// terminal. When the test ends the program and its process group are
// killed if they still run.
func Attach(t *testing.T, m *Machine, o TermOptions, args ...string) *Term {
	t.Helper()
	if len(args) == 0 {
		args = []string{"attach"}
	}
	return StartIn(t, m, o, append([]string{m.Bin(devtools)}, args...), nil)
}

// StartIn starts argv in a Term as Attach starts devtools: on a new PTY,
// with m.Env(o.Env...) and then extra as its complete environment. It is
// for a program other than the client, a helper of testkit for instance.
func StartIn(t *testing.T, m *Machine, o TermOptions, argv, extra []string) *Term {
	t.Helper()
	testkit.RequirePTY(t)
	if o.Cols <= 0 {
		o.Cols = 80
	}
	if o.Rows <= 0 {
		o.Rows = 24
	}
	x := &Term{t: t, m: m, o: o, readDone: make(chan struct{})}
	screen, err := newScreen(o.Cols, o.Rows, x)
	if err != nil {
		t.Fatalf("e2e: the fake terminal: %v", err)
	}
	x.vt = screen

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("e2e: open a PTY: %v", err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(o.Cols), Rows: uint16(o.Rows)}); err != nil {
		master.Close()
		slave.Close()
		t.Fatalf("e2e: size the PTY: %v", err)
	}
	x.master = master
	x.tty = slave.Name()
	for _, hook := range attachHooks {
		hook(m, o, x.tty)
	}
	env := mergeEnv(m.Env(o.Env...), extra...)
	p, err := startProc(argv[0], argv, env, []*os.File{slave, slave, slave}, m.Home,
		&syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0})
	slave.Close()
	if err != nil {
		master.Close()
		t.Fatalf("e2e: start %s: %v", strings.Join(argv, " "), err)
	}
	x.p = p
	m.addTerm(p.p.Pid)
	go x.readLoop()
	t.Cleanup(x.close)
	return x
}

// newScreen makes the engine terminal of a Term; x receives its callbacks.
// A build whose engine is still a stub panics in vt.New;
// that panic becomes an error, so a test fails with the reason instead of
// ending the test binary.
func newScreen(cols, rows int, x *Term) (screen vt.Term, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the terminal engine is missing: %v", r)
		}
	}()
	return vt.New(vt.Options{
		Cols: cols, Rows: rows, History: 1000, Colors: termColors,
		OnBell: func() { x.bells.Add(1) },
		// Runs inside Write, under x.mu.
		OnClipboard: func(b []byte) { x.clips = append(x.clips, string(b)) },
	}), nil
}

// TTY returns the path of the terminal device the program runs on.
func (x *Term) TTY() string { return x.tty }

// Pid returns the pid of the program started in the terminal.
func (x *Term) Pid() int { return x.p.p.Pid }

// readLoop feeds everything the program writes into the engine and writes
// the replies back.
func (x *Term) readLoop() {
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

// xtversionReply is the answer to XTVERSION: DCS > | text ST.
func xtversionReply(identity string) []byte {
	return []byte("\x1bP>|" + identity + "\x1b\\")
}

// feed hands one read to the engine, piece by piece around the XTVERSION
// queries, which the Term answers itself, and writes the replies in the
// order of the queries.
func (x *Term) feed(b []byte) {
	x.mu.Lock()
	x.raw.Write(b)
	var out []byte
	for _, piece := range x.filter.split(b) {
		if piece.query {
			if x.o.Identity != "" {
				out = append(out, xtversionReply(x.o.Identity)...)
			}
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

// piece is a run of program output, or one XTVERSION query.
type piece struct {
	data  []byte
	query bool
}

// xtversionQueries are the two forms of XTVERSION: CSI > q and CSI > 0 q.
var xtversionQueries = [][]byte{[]byte("\x1b[>q"), []byte("\x1b[>0q")}

// queryFilter splits a byte stream at the XTVERSION queries. A query cut
// by a read is held back until the next read completes or refutes it.
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

// Type writes raw bytes to the program, as typed on the terminal.
func (x *Term) Type(s string) {
	x.t.Helper()
	if _, err := x.master.WriteString(s); err != nil {
		x.t.Fatalf("e2e: type into the terminal: %v", err)
	}
}

// Key types one chord: "ctrl+space", "prefix+o", "enter", "alt+x",
// "shift+tab", "up", a single character. "prefix" is the devtools prefix,
// ctrl+space (8.3); "prefix+o" is the prefix followed by o. The bytes are
// those a terminal sends for the modes the program set: legacy encoding,
// with DECCKM for the cursor keys, or the engine's encoder when the program
// asked for kitty keyboard flags.
func (x *Term) Key(chord string) {
	x.t.Helper()
	b, err := x.encodeChord(chord)
	if err != nil {
		x.t.Fatalf("e2e: Key(%q): %v", chord, err)
	}
	x.Type(string(b))
}

// prefixChord is the devtools prefix (8.3).
const prefixChord = "ctrl+space"

func (x *Term) encodeChord(chord string) ([]byte, error) {
	if rest, ok := strings.CutPrefix(chord, "prefix+"); ok {
		p, err := x.encodeChord(prefixChord)
		if err != nil {
			return nil, err
		}
		k, err := x.encodeChord(rest)
		if err != nil {
			return nil, err
		}
		return append(p, k...), nil
	}
	if chord == "prefix" {
		return x.encodeChord(prefixChord)
	}
	k, err := parseChord(chord)
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	modes := x.vt.Modes()
	var enc []byte
	if modes.Kitty != 0 {
		enc = encodeWithEngine(x.vt, k)
	}
	x.mu.Unlock()
	if enc != nil {
		return enc, nil
	}
	return legacyKey(k, modes.AppCursor)
}

// encodeWithEngine asks the engine's key encoder, nil when it has none yet
// (its stub panics until P1-03) or produced nothing.
func encodeWithEngine(t vt.Term, k vt.Key) (b []byte) {
	defer func() {
		if recover() != nil {
			b = nil
		}
	}()
	return t.EncodeKey(k)
}

// parseChord reads "mod+mod+key" into a key event of 4.2.6.
func parseChord(chord string) (vt.Key, error) {
	parts := strings.Split(chord, "+")
	name := parts[len(parts)-1]
	if name == "" && strings.HasSuffix(chord, "+") {
		name = "+"
		parts = parts[:len(parts)-1]
	}
	mod := 0
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "shift":
			mod |= proto.ModShift
		case "alt":
			mod |= proto.ModAlt
		case "ctrl":
			mod |= proto.ModCtrl
		case "super":
			mod |= proto.ModSuper
		case "":
		default:
			return vt.Key{}, fmt.Errorf("unknown modifier %q", p)
		}
	}
	if name == "esc" {
		name = "escape"
	}
	if !proto.ValidKey(name) {
		return vt.Key{}, fmt.Errorf("unknown key %q", name)
	}
	k := vt.Key{Name: name, Mod: mod}
	if len([]rune(name)) == 1 && mod&^proto.ModShift == 0 {
		k.Text = name
	}
	if name == "space" && mod&^proto.ModShift == 0 {
		k.Text = " "
	}
	return k, nil
}

// legacyKeys are the legacy encodings of the named keys without modifiers.
var legacyKeys = map[string]string{
	"enter": "\r", "tab": "\t", "backspace": "\x7f", "escape": "\x1b", "space": " ",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
	"insert": "\x1b[2~", "delete": "\x1b[3~",
	"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS",
	"f5": "\x1b[15~", "f6": "\x1b[17~", "f7": "\x1b[18~", "f8": "\x1b[19~",
	"f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~",
}

// legacyKey encodes k as a terminal without kitty flags does (xterm).
func legacyKey(k vt.Key, appCursor bool) ([]byte, error) {
	alt := k.Mod&proto.ModAlt != 0
	ctrl := k.Mod&proto.ModCtrl != 0
	shift := k.Mod&proto.ModShift != 0
	var s string
	switch {
	case k.Name == "tab" && shift && !ctrl && !alt:
		s = "\x1b[Z"
	case k.Name == "space" && ctrl:
		s = "\x00"
	case ctrl && len(k.Name) == 1:
		c := k.Name[0]
		switch {
		case c >= 'a' && c <= 'z':
			s = string(rune(c - 'a' + 1))
		case c >= '@' && c <= '_':
			s = string(rune(c - '@'))
		case c == '?':
			s = "\x7f"
		default:
			return nil, fmt.Errorf("ctrl+%s has no legacy encoding", k.Name)
		}
	case k.Mod&^(proto.ModAlt|proto.ModShift) != 0:
		return nil, fmt.Errorf("%s with these modifiers needs the kitty encoder", k.Name)
	default:
		if seq, ok := legacyKeys[k.Name]; ok {
			s = seq
			if appCursor && len(seq) == 3 && strings.ContainsRune("ABCD", rune(seq[2])) {
				s = "\x1bO" + seq[2:]
			}
			if shift && k.Name != "space" {
				return nil, fmt.Errorf("shift+%s needs the kitty encoder", k.Name)
			}
		} else {
			s = k.Name
			if shift {
				s = strings.ToUpper(s)
			}
		}
	}
	if alt {
		s = "\x1b" + s
	}
	return []byte(s), nil
}

// Resize changes the size of the PTY and of the screen; the kernel sends
// SIGWINCH to the program.
func (x *Term) Resize(cols, rows int) {
	x.t.Helper()
	x.mu.Lock()
	x.vt.Resize(cols, rows)
	out := x.vt.Replies()
	x.mu.Unlock()
	if err := pty.Setsize(x.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		x.t.Fatalf("e2e: resize the PTY: %v", err)
	}
	if len(out) > 0 && !x.o.Silent {
		x.master.Write(out)
	}
}

// Golden returns the screen in the form of the golden files (10.1.2).
func (x *Term) Golden() golden.Screen {
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

// Screen returns the rows of the screen as plain text, trailing blanks
// removed.
func (x *Term) Screen() []string {
	s := x.Golden()
	out := make([]string, len(s.Lines))
	for i, row := range s.Lines {
		out[i] = golden.PlainRow(row)
	}
	return out
}

// WaitFor waits until text is on the screen and returns how long that
// took. The test fails after d, with the screen.
func (x *Term) WaitFor(text string, d time.Duration) time.Duration {
	x.t.Helper()
	start := time.Now()
	for {
		if strings.Contains(strings.Join(x.Screen(), "\n"), text) {
			return time.Since(start)
		}
		if time.Since(start) > d {
			x.t.Fatalf("e2e: %q did not appear within %v; screen:\n%s", text, d, x.Golden().Plain())
		}
		time.Sleep(time.Millisecond)
	}
}

// BellCount returns how many times the program rang the bell.
func (x *Term) BellCount() int { return int(x.bells.Load()) }

// Clipboard returns the OSC 52 payloads the program wrote, decoded.
func (x *Term) Clipboard() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.clips...)
}

// Output returns everything the program wrote so far, escape sequences
// removed.
func (x *Term) Output() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return ansi.Strip(x.raw.String())
}

// Exited reports whether the program has ended.
func (x *Term) Exited() bool { return x.p.exited() }

// Exit waits up to 30 s for the program to end and returns its exit code
// (-1 when a signal ended it) and everything it wrote, escape sequences
// removed.
func (x *Term) Exit() (code int, output string) {
	x.t.Helper()
	if !x.p.wait(30 * time.Second) {
		x.t.Fatalf("e2e: the program did not end within 30 s; screen:\n%s", x.Golden().Plain())
	}
	select {
	case <-x.readDone:
	case <-time.After(2 * time.Second):
	}
	code, _ = testkit.ExitStatus(x.p.state)
	return code, x.Output()
}

// close runs when the test ends: the program's group is killed if it still
// runs, then the PTY and the screen are closed.
func (x *Term) close() {
	if !x.p.exited() {
		x.p.killGroup(syscall.SIGHUP)
		if !x.p.wait(2 * time.Second) {
			x.p.killGroup(syscall.SIGKILL)
			x.p.wait(5 * time.Second)
		}
	}
	x.master.Close()
	select {
	case <-x.readDone:
	case <-time.After(5 * time.Second):
	}
	x.mu.Lock()
	x.vt.Close()
	x.mu.Unlock()
}
