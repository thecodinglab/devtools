package integration

// The helper programs of level 5 (10.1.1, 10.1.6): attach, the client in a
// process of its own, and the pane programs the tests of level 5 and of
// skew_test.go need beside those of testkit and main_test.go.
//
// attach builds a client the way DevtoolsMultiplexer.Enter does
// (internal/backend/devtools.go), with two differences: it runs guard.Check
// on its own terminal first, as internal/cli/entry.go does, and prints the
// refusal and exits 1 when it gets no pass; and it calls client.Run with
// chrome.Plain, fleet.New with no machines and the local dialer of
// internal/remote. It is a test file: rule 10 of 5.2 and
// TestClientStartsAreInsideEnterOnly read non-test files only.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"

	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/guard"
	"devtools/internal/fleet"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
	"devtools/internal/testkit"
	"devtools/internal/ui"
)

// attachVersion is the version the attach helper says in its hello.
const attachVersion = "0.0.0-l5"

// attachProbeTimeout is how long the attach helper waits for its terminal to
// answer the probe: the 2 s of 3.4 c, as internal/cli/entry.go waits.
const attachProbeTimeout = 2 * time.Second

// refusedPrefix starts the one line the attach helper prints when the guard
// gives no pass; noServerPrefix the line when no server answers.
const (
	refusedPrefix  = "refused: "
	noServerPrefix = "noserver: "
)

func init() {
	testkit.RegisterHelper("attach", helperAttach)
	testkit.RegisterHelper("fg", helperFg)
	testkit.RegisterHelper("once", helperOnce)
	testkit.RegisterHelper("run", helperRun)
	testkit.RegisterHelper("wide", helperWide)
	testkit.RegisterHelper("paint", helperPaint)
	testkit.RegisterHelper("agentfake", helperAgentFake)
	testkit.RegisterHelper("counter", helperCounter)
}

// helperCounter puts its terminal in raw mode, prints "n=0" and, for every
// byte it reads, the number of bytes so far in place of it on the first row.
func helperCounter([]string) int {
	fd := os.Stdin.Fd()
	if st, err := term.MakeRaw(fd); err == nil {
		defer term.Restore(fd, st)
	}
	os.Stdout.WriteString("\x1b[H\x1b[2Jn=0")
	buf := make([]byte, 4096)
	n := 0
	for {
		k, err := os.Stdin.Read(buf)
		if k > 0 {
			n += k
			fmt.Printf("\x1b[H\x1b[2Kn=%d", n)
		}
		if err != nil {
			return 0
		}
	}
}

// oneLine joins the lines of a message with a blank, so that a refusal is
// one line of output.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// helperAttach is the client: attach [<dir> <name>]. With a directory it
// shows that directory's session first.
//
// The guard runs as in internal/cli/entry.go: the environment check with no
// dead marks, then the probe of the terminal through a descriptor of its
// own in raw mode, DEVTOOLS_TRUST_TERMINAL as the trust switch. A refusal is
// one line, "refused: <where>[ <host>]: <words>", and exit 1. No server is
// "noserver: <words>" and exit 1. Detach is exit 0.
func helperAttach(args []string) int {
	pass, nested, err := attachGuard()
	if err != nil {
		fmt.Printf("%serror: %v\r\n", refusedPrefix, err)
		return 1
	}
	if !pass.Valid() {
		fmt.Printf("%s%s\r\n", refusedPrefix, refusalLine(nested))
		return 1
	}

	socket, err := proto.SocketPath()
	if err != nil {
		fmt.Printf("attach: %v\r\n", err)
		return 1
	}
	hello := proto.Hello{
		Gen:       proto.Gen,
		Version:   attachVersion,
		Caps:      []string{proto.CapCatalog, proto.CapAgents, proto.CapRoute, proto.CapStateGet},
		Kind:      proto.KindInteractive,
		Ancestors: append([]int{os.Getpid()}, platform.Ancestors(os.Getpid())...),
	}
	if tty, err := platform.TTYName(os.Stdin); err == nil {
		hello.TTY = tty
	}
	cols, rows, _ := platform.Winsize(os.Stdout)
	f := fleet.New(fleet.Options{
		Dialer: remote.NewDialer(remote.DialOptions{SSH: "ssh", Socket: socket, Hello: hello}),
		Cols:   cols,
		Rows:   rows,
	})
	o := client.Options{
		Pass:    pass,
		Fleet:   f,
		Chrome:  chrome.Plain{},
		Version: attachVersion,
	}
	if len(args) >= 2 {
		o.Initial = &session.Target{Dir: args[0], Name: args[1]}
	}
	err = client.Run(o)
	switch {
	case errors.Is(err, client.ErrNoServer):
		fmt.Printf("%s%s\r\n", noServerPrefix, oneLine(ui.Message("noserver")))
		return 1
	case err != nil:
		fmt.Printf("attach: %v\r\n", err)
		return 1
	}
	return 0
}

// attachGuard runs guard.Check on the terminal of standard input, opened a
// second time and put in raw mode, as openProbe of internal/cli does.
func attachGuard() (*guard.Pass, *guard.Nested, error) {
	if n := guard.Env(os.Environ(), nil); n != nil {
		// Check refuses the same way without touching the terminal; the
		// terminal is not opened either.
		return nil, n, nil
	}
	path, err := platform.TTYName(os.Stdin)
	if err != nil {
		return nil, nil, err
	}
	tty, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer tty.Close()
	// The descriptor stays in non-blocking mode (File.Fd would switch it
	// to blocking), so the probe's read deadline works.
	sc, err := tty.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var st *term.State
	var rerr error
	if err := sc.Control(func(fd uintptr) { st, rerr = term.MakeRaw(fd) }); err != nil {
		return nil, nil, err
	}
	if rerr != nil {
		return nil, nil, rerr
	}
	defer sc.Control(func(fd uintptr) { term.Restore(fd, st) })
	trust := os.Getenv("DEVTOOLS_TRUST_TERMINAL") == "1"
	return guard.Check(tty, os.Environ(), nil, attachProbeTimeout, trust)
}

// refusalLine names where the guard found the terminal and the words a
// person reads for it (refusalWords of internal/cli), on one line.
func refusalLine(n *guard.Nested) string {
	if n == nil {
		return "unknown terminal: " + ui.Message("unknown")
	}
	where := n.Where
	if n.Host != "" {
		where += " " + n.Host
	}
	var words string
	switch n.Where {
	case "unknown terminal":
		words = ui.Message("unknown")
	case "devtools":
		host := n.Host
		if host == "" {
			host = "this machine"
		}
		words = ui.Message("nested", "devtools", host)
	default:
		words = ui.Message("othermux", "devtools", n.Where)
	}
	return where + ": " + oneLine(words)
}

// helperEnviron is the environment of this process with the helper variable
// set to name.
func helperEnviron(name string) []string {
	env := []string{testkit.HelperVar + "=" + name}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, testkit.HelperVar+"=") {
			env = append(env, kv)
		}
	}
	return env
}

// execHelper replaces this process with the helper name and its arguments.
func execHelper(name string, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "exec %s: %v\n", name, err)
		return 127
	}
	err = syscall.Exec(exe, append([]string{exe}, args...), helperEnviron(name))
	fmt.Fprintf(os.Stderr, "exec %s: %v\n", name, err)
	return 127
}

// helperFg is fg <name> <helper> [args]: it runs the helper as a child whose
// program is the test binary linked under name (so the process table names
// it so, and its argv[0] is name), in a process group of its own that it
// makes the terminal's foreground group, as a shell runs a job. It exits
// with the child's code. A pane that runs it is busy, and its foreground
// group is the child's: `fg nvim hex` is an editor (3.2.1), `fg claude
// agentfake` is Claude Code to the agent rules.
func helperFg(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "fg: need a name and a helper")
		return 2
	}
	name, helper := args[0], args[1]
	exe, err := os.Executable()
	if err != nil {
		return 127
	}
	dir, err := os.MkdirTemp("/tmp", "dt-fg-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "fg: %v\n", err)
		return 1
	}
	link := filepath.Join(dir, name)
	if err := os.Symlink(exe, link); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "fg: %v\n", err)
		return 1
	}
	// Taking the terminal back after the child is not needed (this helper
	// ends with it), but a background write must not stop it.
	signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)
	cmd := exec.Command(link, args[2:]...)
	cmd.Args[0] = name
	cmd.Env = helperEnviron(helper)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
	err = cmd.Start()
	// The link is needed only for the exec, which Start waited for; this
	// helper usually ends by a signal, which runs no deferred call.
	os.RemoveAll(dir)
	if err != nil {
		fmt.Printf("fg: %v\r\n", err)
		return 1
	}
	cmd.Wait()
	code, _ := testkit.ExitStatus(cmd.ProcessState)
	return code
}

// helperOnce is once <marker> <helper> [args]: the first time it runs, when
// the file marker (a name in DEVTOOLS_TEST_DIR) does not exist yet, it
// creates it and becomes the helper; every later time it becomes hex. So the
// first pane of a server runs something else than the panes split or opened
// after it.
func helperOnce(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "once: need a marker and a helper")
		return 2
	}
	marker := filepath.Join(os.Getenv(envTestDir), args[0])
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return execHelper("hex", nil)
	}
	f.Close()
	return execHelper(args[1], args[2:])
}

// helperRun is run <helper> [args]: it runs the helper as a child on the
// same terminal, then prints "exit <code>" and waits for the end of its
// input, so the pane stays and shows what the child printed.
func helperRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "run: need a helper")
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		return 127
	}
	cmd := exec.Command(exe, args[1:]...)
	cmd.Env = helperEnviron(args[0])
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil && cmd.ProcessState == nil {
		fmt.Printf("run: %v\r\n", err)
		return 1
	}
	code, _ := testkit.ExitStatus(cmd.ProcessState)
	fmt.Printf("exit %d\r\n", code)
	waitInput()
	return 0
}

// waitWidth waits up to 5 s until the terminal of standard output is cols
// columns wide: a pane starts at the size of its session and gets the size
// of the view that displays it a moment later.
func waitWidth(cols int) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, _, err := platform.Winsize(os.Stdout); err == nil && c == cols {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// helperWide is wide <n> <width>: once its terminal is width columns wide,
// n numbered lines of exactly width characters, then "done", then it waits
// for the end of its input. A pane of that width keeps each line as one row
// of history.
func helperWide(args []string) int {
	n, width := 10000, 200
	if len(args) > 0 {
		n, _ = strconv.Atoi(args[0])
	}
	if len(args) > 1 {
		width, _ = strconv.Atoi(args[1])
	}
	waitWidth(width)
	w := bufio.NewWriterSize(os.Stdout, 64<<10)
	for i := range n {
		line := fmt.Sprintf("%08d ", i)
		for len(line) < width {
			line += string(rune('a' + (len(line)+i)%26))
		}
		w.WriteString(line[:width])
		w.WriteString("\r\n")
	}
	w.WriteString("done\r\n")
	w.Flush()
	waitInput()
	return 0
}

// helperPaint is paint [<cols>]: once its terminal is cols columns wide (any
// width without the argument) it fills the whole terminal with cells that
// each carry a foreground and a background colour of their own, so that
// every cell of a row is a style run of its own on the wire; then it prints
// nothing more and waits for the end of its input. The row in the middle
// starts with "painted", which is written last.
func helperPaint(args []string) int {
	if len(args) > 0 {
		if c, err := strconv.Atoi(args[0]); err == nil {
			waitWidth(c)
		}
	}
	cols, rows, err := platform.Winsize(os.Stdout)
	if err != nil || cols < 10 || rows < 2 {
		cols, rows = 80, 24
	}
	w := bufio.NewWriterSize(os.Stdout, 256<<10)
	w.WriteString("\x1b[H\x1b[2J")
	for y := range rows {
		fmt.Fprintf(w, "\x1b[%d;1H", y+1)
		for x := range cols {
			r, g, b := (x*7+y)%256, (x*13+y*3)%256, (x*31+y*5)%256
			fmt.Fprintf(w, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm%c", r, g, b, 255-r, 255-g, 255-b, 'A'+rune((x+y)%26))
		}
	}
	fmt.Fprintf(w, "\x1b[0m\x1b[%d;1Hpainted", rows/2+1)
	w.Flush()
	waitInput()
	return 0
}

// helperAgentFake reports agent states through OSC 7501 the way Claude Code
// does, run as `fg claude agentfake`: working, then blocked, every 200 ms
// in turn, until its input ends. Each blocked after a working is a
// transition the server sends as event.agent.
//
// provisional E4: the process name "claude" and the report
// "ESC ]7501;state=<state> ST" are what internal/agent recognises today; a
// real Claude Code session is pending [GO] in E4, and this fixture follows
// whatever E4 records.
func helperAgentFake([]string) int {
	done := make(chan struct{})
	go func() {
		waitInput()
		close(done)
	}()
	states := []string{"working", "blocked"}
	for i := 0; ; i++ {
		os.Stdout.WriteString("\x1b]7501;state=" + states[i%2] + "\x1b\\")
		select {
		case <-done:
			return 0
		case <-time.After(200 * time.Millisecond):
		}
	}
}
