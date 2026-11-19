package testkit

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	RunHelper()
	// Hermetic: nothing of the developer's multiplexer or devtools session
	// reaches a test, except the one skip switch.
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == NoPTY {
			continue
		}
		if strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "ZELLIJ") ||
			name == "TMUX" || name == "TMUX_PANE" || name == "STY" {
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}

// ptyRecorder stands in for a test in the test of RequirePTY.
type ptyRecorder struct {
	skipped bool
	fatal   string
}

func (r *ptyRecorder) Helper()                   {}
func (r *ptyRecorder) Skip(...any)               { r.skipped = true }
func (r *ptyRecorder) Fatalf(f string, a ...any) { r.fatal = fmt.Sprintf(f, a...) }

func TestRequirePTYSkipsOnlyWhenTold(t *testing.T) {
	opened := 0
	works := func() (*os.File, *os.File, error) {
		opened++
		r, w, err := os.Pipe()
		return r, w, err
	}
	broken := func() (*os.File, *os.File, error) {
		opened++
		return nil, nil, fmt.Errorf("no ptmx")
	}
	for _, c := range []struct {
		value    string
		set      bool
		open     func() (*os.File, *os.File, error)
		skip     bool
		fail     bool
		openings int
	}{
		{set: false, open: works, openings: 1},
		{value: "1", set: true, open: works, skip: true},
		{value: "1", set: true, open: broken, skip: true},
		{value: "0", set: true, open: works, openings: 1},
		{value: "", set: true, open: works, openings: 1},
		{value: "yes", set: true, open: works, openings: 1},
		{set: false, open: broken, fail: true, openings: 1},
		{value: "0", set: true, open: broken, fail: true, openings: 1},
	} {
		if c.set {
			t.Setenv(NoPTY, c.value)
		} else {
			t.Setenv(NoPTY, "")
			os.Unsetenv(NoPTY)
		}
		opened = 0
		var r ptyRecorder
		requirePTY(&r, c.open)
		if r.skipped != c.skip || (r.fatal != "") != c.fail || opened != c.openings {
			t.Errorf("%s=%q (set %v): skipped %v, failed %q, opened %d; want skipped %v, failed %v, opened %d",
				NoPTY, c.value, c.set, r.skipped, r.fatal, opened, c.skip, c.fail, c.openings)
		}
	}
}

func TestRequirePTYOpensARealPTY(t *testing.T) {
	RequirePTY(t)
	m, s := OpenPTY(t, 80, 24)
	if m == nil || s == nil {
		t.Fatal("no PTY pair")
	}
}

// startHelper starts a helper on a PTY with a clean environment.
func startHelper(t *testing.T, cols, rows int, name string, args ...string) *PTY {
	t.Helper()
	argv, env := HelperCommand(name, args...)
	return StartPTY(t, argv, CleanEnv(ShortTempDir(t), env...), cols, rows)
}

func TestHelperHexPrintsEveryByte(t *testing.T) {
	p := startHelper(t, 80, 24, "hex")
	p.WaitFor("ready\r\n", 10*time.Second)
	var in []byte
	var want strings.Builder
	for c := 0; c < 256; c++ {
		in = append(in, byte(c))
	}
	in = append(in, 'q', 'q', 'q')
	for _, c := range in {
		fmt.Fprintf(&want, "%02x ", c)
	}
	p.Write(string(in))
	if code, sig := p.Wait(10 * time.Second); code != 0 || sig != 0 {
		t.Fatalf("hex ended with code %d, signal %v", code, sig)
	}
	_, got, _ := strings.Cut(p.Output(), "ready\r\n")
	// The PTY may still hold the tail of the output after the exit.
	Eventually(t, 5*time.Second, "every byte in hex", func() bool {
		_, got, _ = strings.Cut(p.Output(), "ready\r\n")
		return got == want.String()
	})
	if got != want.String() {
		t.Fatalf("hex printed\n%q\nwant\n%q", got, want.String())
	}
}

func TestHelperSizeReportsAWindowChange(t *testing.T) {
	p := startHelper(t, 80, 24, "size")
	p.WaitFor("80 x 24", 10*time.Second)
	p.Resize(100, 30)
	p.WaitFor("100 x 30", 10*time.Second)
	p.Resize(40, 10)
	p.WaitFor("40 x 10", 10*time.Second)
}

func TestHelperFloodEndsWithDone(t *testing.T) {
	argv, env := HelperCommand("flood", "1")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = CleanEnv(ShortTempDir(t), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("flood: %v", err)
	}
	if out.Len() < 1<<20 {
		t.Fatalf("flood wrote %d bytes, want at least 1 MiB", out.Len())
	}
	sc := bufio.NewScanner(&out)
	n := 0
	last := ""
	for sc.Scan() {
		last = sc.Text()
		if last == "done" {
			break
		}
		num, _, _ := strings.Cut(last, " ")
		if v, err := strconv.Atoi(num); err != nil || v != n || len(last)+1 != FloodLine {
			t.Fatalf("line %d is %q", n, last)
		}
		n++
	}
	if last != "done" {
		t.Fatalf("the last line is %q, not done", last)
	}
	if sc.Scan() {
		t.Fatalf("something follows done: %q", sc.Text())
	}
	if n != 1<<20/FloodLine {
		t.Fatalf("%d numbered lines, want %d", n, 1<<20/FloodLine)
	}
}

func TestHelpersExitAndKillThemselves(t *testing.T) {
	run := func(name string, args ...string) *os.ProcessState {
		argv, env := HelperCommand(name, args...)
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = CleanEnv(ShortTempDir(t), env...)
		cmd.Run()
		return cmd.ProcessState
	}
	if code, _ := ExitStatus(run("exit", "7")); code != 7 {
		t.Fatalf("exit 7 ended with %d", code)
	}
	if _, sig := ExitStatus(run("killself", "TERM")); sig.String() != "terminated" {
		t.Fatalf("killself TERM ended by %v", sig)
	}
	if code, _ := ExitStatus(run("no-such-helper")); code != 127 {
		t.Fatalf("an unknown helper ended with %d", code)
	}
}

func TestHelperEnvPrintsOnlyTheCleanEnvironment(t *testing.T) {
	home := ShortTempDir(t)
	argv, env := HelperCommand("env")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = CleanEnv(home, append(env, "EXTRA=1")...)
	cmd.Dir = home
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := []string{
		"DEVTOOLS_TEST_HELPER=env", "EXTRA=1", "HOME=" + home, "LANG=C.UTF-8", "PATH=/usr/bin:/bin",
		"SHELL=/bin/sh", "XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		argv[0],
	}
	if len(lines) != len(want)+1 {
		t.Fatalf("env printed %q", lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("line %d is %q, want %q (all: %q)", i, lines[i], w, lines)
		}
	}
	wd, _ := filepath.EvalSymlinks(home)
	if got, _ := filepath.EvalSymlinks(lines[len(lines)-1]); got != wd {
		t.Fatalf("working directory %q, want %q", lines[len(lines)-1], home)
	}
}

func TestHelperQueryPrintsEachReplyInHex(t *testing.T) {
	p := startHelper(t, 80, 24, "query")
	// The test is the terminal: it reads the five queries and answers three.
	p.WaitFor("\x1b[?u", 10*time.Second)
	p.Write("\x1b[?62;22c" + "\x1bP>|termtest 1\x1b\\" + "\x1b]11;rgb:0000/0000/0000\x07")
	out := p.WaitFor("end", 10*time.Second)
	for _, want := range []string{
		"1b 5b 3f 36 32 3b 32 32 63",
		"1b 50 3e 7c 74 65 72 6d 74 65 73 74 20 31 1b 5c",
		"1b 5d 31 31 3b 72 67 62 3a 30 30 30 30 2f 30 30 30 30 2f 30 30 30 30 07",
	} {
		if !strings.Contains(out, want+"\r\n") {
			t.Fatalf("query did not print the reply %s on a line of its own:\n%q", want, out)
		}
	}
}

func TestHelperLinesAndOSCWriteAndWait(t *testing.T) {
	p := startHelper(t, 80, 24, "lines", "3")
	p.WaitFor("line 1\r\nline 2\r\nline 3\r\n", 10*time.Second)
	o := startHelper(t, 80, 24, "osc", "52;c;aGk=")
	o.WaitFor("\x1b]52;c;aGk=\x1b\\", 10*time.Second)
	s := startHelper(t, 80, 4, "sync", "1")
	s.WaitFor("\x1b[?2026l", 10*time.Second)
	if out := s.Output(); !strings.Contains(out, "\x1b[?2026h") || !strings.Contains(out, "sync 01 top") || !strings.Contains(out, "sync 03 bottom") {
		t.Fatalf("sync wrote %q", out)
	}
	m := startHelper(t, 80, 24, "modes", "paste", "focus")
	m.WaitFor("\x1b[?2004h\x1b[?1004hready\r\n", 10*time.Second)
	h := startHelper(t, 80, 24, "sighup")
	h.WaitFor("ready", 10*time.Second)
}

func TestCleanEnvInheritsNothing(t *testing.T) {
	t.Setenv("DEVTOOLS_SOCKET", "/tmp/should-not-leak")
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr.sock")
	env := CleanEnv("/tmp/home", "EXTRA=1", "PATH=/opt/bin", "EXTRA=2")
	want := []string{
		"HOME=/tmp/home",
		"XDG_STATE_HOME=/tmp/home/.local/state",
		"PATH=/opt/bin",
		"LANG=C.UTF-8",
		"SHELL=/bin/sh",
		"EXTRA=2",
	}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CleanEnv = %q, want %q", env, want)
	}
	if v, ok := LookupEnv(env, "EXTRA"); !ok || v != "2" {
		t.Fatalf("LookupEnv EXTRA = %q, %v", v, ok)
	}
	if _, ok := LookupEnv(env, "TMUX"); ok {
		t.Fatal("TMUX was inherited")
	}
	for _, kv := range CleanEnv("/h") {
		for _, bad := range []string{"DEVTOOLS_", "HERDR_", "TMUX", "STY", "ZELLIJ"} {
			if strings.HasPrefix(kv, bad) {
				t.Fatalf("CleanEnv holds %s", kv)
			}
		}
	}
}

func TestShortTempDirFitsASocketPath(t *testing.T) {
	dir := ShortTempDir(t)
	if !strings.HasPrefix(dir, "/tmp/") {
		t.Fatalf("%s is not under /tmp", dir)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("%s: %v", dir, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("%s is not empty", dir)
	}
	// A server's socket in it, and one in its state directory below a HOME
	// there, both fit the 104 bytes of macOS.
	for _, sock := range []string{
		filepath.Join(dir, "server.sock"),
		filepath.Join(dir, ".local", "state", "devtools", "server.sock"),
	} {
		if len(sock) > MaxSocketPath-20 {
			t.Fatalf("%s is %d bytes, too close to %d", sock, len(sock), MaxSocketPath)
		}
	}
	var other string
	t.Run("another", func(t *testing.T) {
		other = ShortTempDir(t)
		if other == dir {
			t.Fatal("two calls returned one directory")
		}
	})
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("the directory of a finished test still exists: %v", err)
	}
}

func TestEventuallyWaitsForTheCondition(t *testing.T) {
	n := 0
	Eventually(t, 5*time.Second, "the third call", func() bool { n++; return n == 3 })
	if n != 3 {
		t.Fatalf("condition asked %d times", n)
	}
	if poll(20*time.Millisecond, func() bool { return false }) {
		t.Fatal("poll of a false condition returned true")
	}
}
