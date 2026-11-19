package testkit

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

// HelperVar names the helper program a test binary runs as (RunHelper).
const HelperVar = "DEVTOOLS_TEST_HELPER"

// A Helper is one helper program: it gets the arguments after the program
// name and returns the exit code.
type Helper func(args []string) int

var helpers = struct {
	sync.Mutex
	m map[string]Helper
}{m: map[string]Helper{
	"hex":      helperHex,
	"size":     helperSize,
	"modes":    helperModes,
	"flood":    helperFlood,
	"exit":     helperExit,
	"killself": helperKillself,
	"sighup":   helperSighup,
	"env":      helperEnv,
	"osc":      helperOSC,
	"query":    helperQuery,
	"sync":     helperSync,
	"cat":      helperCat,
	"lines":    helperLines,
}}

// RegisterHelper adds a helper program, for the helper modes other test
// packages need (the server and the client of level 5, P1-18). Call it from
// an init function or from TestMain before RunHelper. A name that is
// already taken panics.
func RegisterHelper(name string, h Helper) {
	helpers.Lock()
	defer helpers.Unlock()
	if _, dup := helpers.m[name]; dup {
		panic("testkit: helper " + name + " registered twice")
	}
	helpers.m[name] = h
}

// RunHelper turns the test binary into a helper program when
// DEVTOOLS_TEST_HELPER is set, and then never returns. Call it first in
// TestMain of every package that starts panes.
func RunHelper() {
	name := os.Getenv(HelperVar)
	if name == "" {
		return
	}
	helpers.Lock()
	h, ok := helpers.m[name]
	helpers.Unlock()
	if !ok {
		fmt.Fprintf(os.Stderr, "testkit: no helper %q\n", name)
		os.Exit(127)
	}
	os.Exit(h(os.Args[1:]))
}

// HelperCommand returns the argv and the environment that run the helper
// name with args: the test binary itself, by absolute path, and the one
// variable that selects the helper. env is meant for CleanEnv:
//
//	argv, env := testkit.HelperCommand("hex")
//	cmd.Env = testkit.CleanEnv(home, env...)
func HelperCommand(name string, args ...string) (argv []string, env []string) {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return append([]string{exe}, args...), []string{HelperVar + "=" + name}
}

// --- the helper programs (10.1.3) ---

// stdinFd is the descriptor of the helper's terminal.
func stdinFd() uintptr { return os.Stdin.Fd() }

// rawMode puts the terminal on standard input in raw mode and returns the
// function that restores it; nothing happens when standard input is not a
// terminal.
func rawMode() func() {
	fd := stdinFd()
	if !term.IsTerminal(fd) {
		return func() {}
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testkit: raw mode: %v\n", err)
		return func() {}
	}
	return func() { term.Restore(fd, st) }
}

// waitInput reads and drops standard input until it ends: EOF, or EIO when
// the terminal is gone. "Waits" in the table of 10.1.3 means this.
func waitInput() {
	io.Copy(io.Discard, os.Stdin)
}

// hexLoop prints every byte of standard input as two hex digits and a
// space, and returns after the third "q" in a row.
func hexLoop() int {
	w := bufio.NewWriter(os.Stdout)
	buf := make([]byte, 4096)
	qs := 0
	for {
		n, err := os.Stdin.Read(buf)
		for _, c := range buf[:n] {
			fmt.Fprintf(w, "%02x ", c)
			if c == 'q' {
				qs++
			} else {
				qs = 0
			}
			if qs == 3 {
				w.Flush()
				return 0
			}
		}
		w.Flush()
		if err != nil {
			return 0
		}
	}
}

// helperHex puts its terminal in raw mode, prints "ready" and a line end,
// then every byte it reads as two hex digits and a space; "q" three times
// in a row exits.
func helperHex(args []string) int {
	restore := rawMode()
	defer restore()
	os.Stdout.WriteString("ready\r\n")
	return hexLoop()
}

// helperSize prints "cols x rows" at start and on every SIGWINCH, until its
// input ends.
func helperSize(args []string) int {
	winch := make(chan os.Signal, 8)
	signal.Notify(winch, syscall.SIGWINCH)
	print := func() {
		rows, cols, err := pty.Getsize(os.Stdin)
		if err != nil {
			fmt.Printf("size: %v\n", err)
			return
		}
		fmt.Printf("%d x %d\n", cols, rows)
	}
	print()
	done := make(chan struct{})
	go func() {
		waitInput()
		close(done)
	}()
	for {
		select {
		case <-winch:
			print()
		case <-done:
			return 0
		}
	}
}

// modeSequences are the names helperModes knows; any other argument is
// written as it is, with "\e" read as ESC.
var modeSequences = map[string]string{
	"kitty":      "\x1b[>1u",    // kitty keyboard flags: disambiguate
	"kitty-all":  "\x1b[>31u",   // every kitty flag
	"paste":      "\x1b[?2004h", // bracketed paste
	"mouse":      "\x1b[?1000h\x1b[?1006h",
	"mouse-drag": "\x1b[?1002h\x1b[?1006h",
	"mouse-all":  "\x1b[?1003h\x1b[?1006h",
	"focus":      "\x1b[?1004h",
	"alt":        "\x1b[?1049h",
}

// helperModes writes the given escape sequences (by name or as they are),
// then behaves like hex.
func helperModes(args []string) int {
	restore := rawMode()
	defer restore()
	for _, a := range args {
		if seq, ok := modeSequences[a]; ok {
			os.Stdout.WriteString(seq)
		} else {
			os.Stdout.WriteString(strings.ReplaceAll(a, `\e`, "\x1b"))
		}
	}
	os.Stdout.WriteString("ready\r\n")
	return hexLoop()
}

// FloodLine is the length of one line of the helper flood, its newline
// included.
const FloodLine = 64

// helperFlood writes <mib> MiB of numbered lines of FloodLine bytes, then
// the line "done", then waits for the end of its input.
func helperFlood(args []string) int {
	mib := 1
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 0 {
			fmt.Fprintf(os.Stderr, "flood: bad size %q\n", args[0])
			return 2
		}
		mib = v
	}
	w := bufio.NewWriterSize(os.Stdout, 64<<10)
	pad := strings.Repeat("x", FloodLine-10)
	lines := mib << 20 / FloodLine
	for i := range lines {
		fmt.Fprintf(w, "%08d %s\n", i, pad)
	}
	w.WriteString("done\n")
	w.Flush()
	waitInput()
	return 0
}

// helperExit exits with the code.
func helperExit(args []string) int {
	if len(args) == 0 {
		return 0
	}
	code, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "exit: bad code %q\n", args[0])
		return 2
	}
	return code
}

// signals are the names killself accepts, with or without "SIG".
var signals = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT,
	"KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2, "ABRT": syscall.SIGABRT, "SEGV": syscall.SIGSEGV,
	"PIPE": syscall.SIGPIPE, "ALRM": syscall.SIGALRM,
}

// ParseSignal reads a signal name ("TERM", "SIGTERM") or number.
func ParseSignal(s string) (syscall.Signal, bool) {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return syscall.Signal(n), true
	}
	sig, ok := signals[strings.TrimPrefix(strings.ToUpper(s), "SIG")]
	return sig, ok
}

// helperKillself sends itself the signal. The signal's default action is
// restored first, so the parent sees the process end by that signal.
func helperKillself(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "killself: no signal")
		return 2
	}
	sig, ok := ParseSignal(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "killself: bad signal %q\n", args[0])
		return 2
	}
	signal.Reset(sig)
	syscall.Kill(os.Getpid(), sig)
	time.Sleep(10 * time.Second)
	fmt.Fprintf(os.Stderr, "killself: still alive after %v\n", sig)
	return 125
}

// helperSighup ignores nothing, prints "ready" and then waits for a signal
// without reading its input; used to check that SIGHUP reaches pane
// children.
func helperSighup(args []string) int {
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}

// helperEnv prints its environment sorted, then its argv[0] and its working
// directory, each on a line of its own.
func helperEnv(args []string) int {
	env := os.Environ()
	sort.Strings(env)
	for _, kv := range env {
		fmt.Println(kv)
	}
	fmt.Println(os.Args[0])
	wd, err := os.Getwd()
	if err != nil {
		wd = "?" + err.Error()
	}
	fmt.Println(wd)
	return 0
}

// helperOSC writes one OSC with the payload ("52;c;aGk=", "7;file://h/d",
// "0;title", "7501;...") ended by ST, then waits for the end of its input.
func helperOSC(args []string) int {
	payload := strings.Join(args, " ")
	os.Stdout.WriteString("\x1b]" + payload + "\x1b\\")
	waitInput()
	return 0
}

// QueryQuiet is how long the helper query waits for a further byte of a
// reply before it prints what it has.
var QueryQuiet = 500 * time.Millisecond

// queries are what the helper query writes: DA1, XTVERSION, OSC 10, OSC 11
// and the kitty keyboard query.
var queries = []string{"\x1b[c", "\x1b[>0q", "\x1b]10;?\x1b\\", "\x1b]11;?\x1b\\", "\x1b[?u"}

// helperQuery writes DA1, XTVERSION, OSC 10, OSC 11 and CSI ? u, then
// prints each reply in hex, one reply per line, until five replies arrived
// or none came for QueryQuiet; then the line "end", and it waits for the
// end of its input.
func helperQuery(args []string) int {
	restore := rawMode()
	for _, q := range queries {
		os.Stdout.WriteString(q)
	}
	bytesIn := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				bytesIn <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(bytesIn)
				return
			}
		}
	}()
	var pending []byte
	var replies [][]byte
	quiet := time.NewTimer(QueryQuiet)
loop:
	for len(replies) < len(queries) {
		select {
		case b, ok := <-bytesIn:
			if !ok {
				break loop
			}
			pending = append(pending, b...)
			for {
				n := replyLen(pending)
				if n == 0 {
					break
				}
				replies = append(replies, pending[:n])
				pending = pending[n:]
			}
			quiet.Reset(QueryQuiet)
		case <-quiet.C:
			break loop
		}
	}
	if len(pending) > 0 {
		replies = append(replies, pending)
	}
	restore()
	for _, r := range replies {
		var b strings.Builder
		for i, c := range r {
			if i > 0 {
				b.WriteByte(' ')
			}
			fmt.Fprintf(&b, "%02x", c)
		}
		fmt.Println(b.String())
	}
	fmt.Println("end")
	for range bytesIn {
	}
	return 0
}

// replyLen returns the length of the complete reply at the start of b, 0
// when b holds none yet. A reply is a CSI (ended by a final byte), or an
// OSC or DCS (ended by BEL or ST); a byte that starts none of them is a
// reply of its own.
func replyLen(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	if b[0] != 0x1b {
		return 1
	}
	if len(b) < 2 {
		return 0
	}
	switch b[1] {
	case '[':
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return i + 1
			}
		}
		return 0
	case ']', 'P':
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				return i + 2
			}
		}
		return 0
	}
	return 2
}

// helperSync opens mode 2026, draws the upper half of the screen, sleeps
// <ms>, draws the rest, closes the mode and waits for the end of its input.
// Row y holds "sync" and its number; the upper half says "top", the lower
// "bottom".
func helperSync(args []string) int {
	ms := 100
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 0 {
			fmt.Fprintf(os.Stderr, "sync: bad time %q\n", args[0])
			return 2
		}
		ms = v
	}
	rows, _, err := pty.Getsize(os.Stdin)
	if err != nil || rows < 2 {
		rows = 24
	}
	half := rows / 2
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[H\x1b[2J")
	for y := range half {
		fmt.Fprintf(&b, "\x1b[%d;1Hsync %02d top", y+1, y)
	}
	os.Stdout.WriteString(b.String())
	time.Sleep(time.Duration(ms) * time.Millisecond)
	b.Reset()
	for y := half; y < rows; y++ {
		fmt.Fprintf(&b, "\x1b[%d;1Hsync %02d bottom", y+1, y)
	}
	b.WriteString("\x1b[?2026l")
	os.Stdout.WriteString(b.String())
	waitInput()
	return 0
}

// helperCat copies its input to its output, with the terminal left in
// canonical mode: the line discipline echoes and edits, the helper repeats
// each line. It ends with its input.
func helperCat(args []string) int {
	io.Copy(os.Stdout, os.Stdin)
	return 0
}

// helperLines prints <n> numbered lines ("line 1" to "line n") and waits for
// the end of its input.
func helperLines(args []string) int {
	n := 100
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 0 {
			fmt.Fprintf(os.Stderr, "lines: bad count %q\n", args[0])
			return 2
		}
		n = v
	}
	w := bufio.NewWriter(os.Stdout)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	w.Flush()
	waitInput()
	return 0
}
