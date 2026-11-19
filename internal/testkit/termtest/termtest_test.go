package termtest_test

import (
	"encoding/hex"
	"os"
	"strings"
	"syscall"
	"testing"

	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/testkit/termtest"
)

// TestMain makes the test binary the commands of the tests: the helper
// programs of testkit and "bells" of this file.
func TestMain(m *testing.M) {
	testkit.RegisterHelper("bells", func(args []string) int {
		// Three bells, then "rung", then it waits for its input to end.
		os.Stdout.WriteString("\a\a\arung\r\n")
		buf := make([]byte, 64)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				return 0
			}
		}
	})
	testkit.RunHelper()
	os.Exit(m.Run())
}

// start runs a helper of the test binary in a Terminal.
func start(t *testing.T, o termtest.Options, name string, args ...string) *termtest.Terminal {
	t.Helper()
	argv, env := testkit.HelperCommand(name, args...)
	o.Env = testkit.CleanEnv(testkit.ShortTempDir(t), env...)
	return termtest.Start(t, argv, o)
}

// replies decodes what the helper query printed: one reply per line in hex,
// then "end".
func replies(t *testing.T, x *termtest.Terminal) []string {
	t.Helper()
	out, _, _ := strings.Cut(x.Output(), "end")
	var got []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "end" {
			continue
		}
		b, err := hex.DecodeString(strings.ReplaceAll(line, " ", ""))
		if err != nil {
			t.Fatalf("a reply line %q is not hex: %v", line, err)
		}
		got = append(got, string(b))
	}
	return got
}

// queryTerminal runs the helper query until it printed its replies and
// "end"; it then waits for its input to end, which the end of the test
// brings.
func queryTerminal(t *testing.T, o termtest.Options) *termtest.Terminal {
	t.Helper()
	x := start(t, o, "query")
	x.WaitFor("end")
	return x
}

func TestTheTerminalAnswersQueries(t *testing.T) {
	x := queryTerminal(t, termtest.Options{Name: "ghostty 1.3.1"})
	got := replies(t, x)
	want := []struct{ what, prefix, contains string }{
		{"DA1", "\x1b[?", "c"},
		{"XTVERSION", "\x1bP>|", "ghostty 1.3.1"},
		{"OSC 10", "\x1b]10;", "rgb:d0d0/d0d0/d0d0"},
		{"OSC 11", "\x1b]11;", "rgb:0000/0000/0000"},
		{"kitty query", "\x1b[?", "u"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d replies, want %d: %q", len(got), len(want), got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w.prefix) || !strings.Contains(got[i], w.contains) {
			t.Errorf("reply %d (%s) is %q, want %q ... %q", i, w.what, got[i], w.prefix, w.contains)
		}
	}
}

func TestTheTerminalAnswersXTVersionWithItsDefaultName(t *testing.T) {
	x := queryTerminal(t, termtest.Options{})
	got := replies(t, x)
	if len(got) < 2 || got[1] != "\x1bP>|"+termtest.DefaultName+"\x1b\\" {
		t.Fatalf("the replies are %q", got)
	}
}

func TestTheTerminalShowsWhatTheCommandDrew(t *testing.T) {
	x := start(t, termtest.Options{Cols: 40, Rows: 6}, "modes", "\\e[1;31m", "red\\e[0m ")
	s := x.WaitFor("ready")
	if s.Cols != 40 || s.Rows != 6 {
		t.Fatalf("the screen is %dx%d", s.Cols, s.Rows)
	}
	if got := golden.PlainRow(s.Lines[0]); got != "red ready" {
		t.Fatalf("row 0 is %q", got)
	}
	if !strings.HasPrefix(s.Lines[0], "\x1b[0;1;31mred") {
		t.Fatalf("row 0 does not carry the style the command drew: %q", s.Lines[0])
	}
	x.Type("ab")
	x.WaitFor("61 62")
	x.Resize(30, 4)
	if s := x.Screen(); s.Cols != 30 || s.Rows != 4 {
		t.Fatalf("after the resize the screen is %dx%d", s.Cols, s.Rows)
	}
	x.Type("qqq")
	if code, out := x.Wait(); code != 0 || !strings.Contains(out, "red ready") {
		t.Fatalf("exit %d, output %q", code, out)
	}
}

func TestTheCommandSeesTheSizeOfTheTerminal(t *testing.T) {
	x := start(t, termtest.Options{Cols: 50, Rows: 7}, "size")
	x.WaitFor("50 x 7")
	x.Resize(61, 9)
	x.WaitFor("61 x 9")
}

func TestASilentTerminalAnswersNothing(t *testing.T) {
	x := queryTerminal(t, termtest.Options{Silent: true})
	if got := replies(t, x); len(got) != 0 {
		t.Fatalf("a silent terminal answered %q", got)
	}
}

func TestClipboardAndBellsAreRecorded(t *testing.T) {
	x := start(t, termtest.Options{}, "osc", "52;c;"+"aGVsbG8gdGVybXRlc3Q=")
	testkit.Eventually(t, termtest.Timeout, "the clipboard payload", func() bool { return len(x.Clipboard()) == 1 })
	if got := x.Clipboard(); got[0] != "hello termtest" {
		t.Fatalf("the clipboard holds %q", got)
	}
	b := start(t, termtest.Options{}, "bells")
	b.WaitFor("rung")
	if n := b.Bells(); n != 3 {
		t.Fatalf("%d bells, want 3", n)
	}
	if n := x.Bells(); n != 0 {
		t.Fatalf("the terminal of the clipboard counted %d bells", n)
	}
}

func TestTheCommandInheritsNothing(t *testing.T) {
	t.Setenv("DEVTOOLS_TERMTEST_LEAK", "1")
	home := testkit.ShortTempDir(t)
	argv, env := testkit.HelperCommand("env")
	x := termtest.Start(t, argv, termtest.Options{Rows: 30, Env: testkit.CleanEnv(home, env...)})
	code, out := x.Wait()
	if code != 0 {
		t.Fatalf("env exited %d", code)
	}
	if strings.Contains(out, "DEVTOOLS_TERMTEST_LEAK") {
		t.Fatalf("the command inherited the test's environment:\n%s", out)
	}
	for _, want := range []string{"HOME=" + home, "PATH=/usr/bin:/bin", "\n" + home} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
}

func TestTheCommandRunsInASessionWithThePTYAsItsTerminal(t *testing.T) {
	x := start(t, termtest.Options{}, "hex")
	x.WaitFor("ready")
	if !strings.HasPrefix(x.TTY(), "/dev/") {
		t.Fatalf("the terminal device is %q", x.TTY())
	}
	// The hex helper put the PTY in raw mode, so ^C arrives as a byte and
	// not as a signal.
	x.Type("\x03")
	x.WaitFor("03")
	if pgid, err := syscall.Getpgid(x.Pid()); err != nil || pgid != x.Pid() {
		t.Fatalf("the command is not the leader of its own group: pgid %d, %v", pgid, err)
	}
	x.Type("qqq")
	if code, _ := x.Wait(); code != 0 {
		t.Fatalf("hex exited %d", code)
	}
}
