package integration

// Level 5: the client starts only in a plain terminal (3.4 c, R3). The
// attach helper runs guard.Check as internal/cli/entry.go does and prints
// one line when it is refused; with no server it prints one line too.

import (
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit/termtest"
	"devtools/internal/ui"
)

// refusedWith waits for the client to end, checks that it exited 1 and
// printed exactly one line that starts with prefix, and returns that line.
func refusedWith(t *testing.T, x *termtest.Terminal, prefix string) string {
	t.Helper()
	code, out := x.Wait()
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(out, "\r", ""), "\n"), "\n")
	if code != 1 || len(lines) != 1 || !strings.HasPrefix(lines[0], prefix) {
		t.Fatalf("the client exited %d and printed %q, want exit 1 and one line starting %q", code, out, prefix)
	}
	return lines[0]
}

func TestTheClientRefusesInsideAPane(t *testing.T) {
	// The first pane runs the attach helper as its program, with the
	// pane's environment (DEVTOOLS_PANE among it), and then prints its
	// exit code.
	s := newL5(t, "once first run attach")
	x := s.attach(80, 24)
	sc := x.WaitFor("exit 1")
	if !paneHas(sc, refusedPrefix+"devtools") {
		t.Fatalf("the pane does not show the refusal:\n%s", sc.Plain())
	}
	// One line of refusal, and no second status row inside the pane.
	refusals, statusRows := 0, 0
	for _, r := range plainRows(sc) {
		if strings.Contains(r, refusedPrefix) {
			refusals++
		}
		if strings.Contains(r, "work") && strings.Contains(r, "[1") {
			statusRows++
		}
	}
	if refusals != 1 || statusRows != 1 {
		t.Fatalf("%d refusal lines and %d status rows, want one of each:\n%s", refusals, statusRows, sc.Plain())
	}
}

func TestTheClientRefusesWithTmuxOrHerdrInTheEnvironment(t *testing.T) {
	// No server runs: the guard refuses before anything is dialled, and it
	// does not detect contexts, so a live or dead TMUX is the same to it.
	for _, c := range []struct{ env, where string }{
		{"TMUX=/tmp/tmux-1000/default,1234,0", "tmux"},
		{"TMUX=", "tmux"},
		{"HERDR_ENV=1", "herdr"},
		{"STY=1234.pts-0.host", "screen"},
		{"ZELLIJ=0", "zellij"},
	} {
		t.Run(c.env, func(t *testing.T) {
			s := newL5(t, "")
			x := s.attachWith(termtest.Options{}, c.env)
			line := refusedWith(t, x, refusedPrefix+c.where+": ")
			if !strings.Contains(line, oneLine(ui.Message("othermux", "devtools", c.where))) {
				t.Fatalf("the refusal %q does not carry the othermux words", line)
			}
		})
	}
}

func TestTheClientRefusesATerminalThatAnswersAsDevtools(t *testing.T) {
	// A devtools pane reached over ssh: no variable survives, the terminal
	// answers XTVERSION as devtools.
	s := newL5(t, "hex")
	x := s.attachWith(termtest.Options{Name: "devtools 1.2.3 otherhost"})
	line := refusedWith(t, x, refusedPrefix+"devtools otherhost: ")
	if !strings.Contains(line, "otherhost") {
		t.Fatalf("the refusal %q does not name the host", line)
	}
	if snap := stateGet(t, oneshot(t, s.srv.socket())); len(snap.Sessions) != 0 {
		t.Fatalf("the refused client opened sessions: %+v", snap.Sessions)
	}
}

func TestTheClientFailsClosedOnASilentTerminal(t *testing.T) {
	s := newL5(t, "hex")
	start := time.Now()
	x := s.attachWith(termtest.Options{Silent: true})
	refusedWith(t, x, refusedPrefix+"unknown terminal: ")
	if d := time.Since(start); d < attachProbeTimeout {
		t.Fatalf("the refusal came after %v, before the probe's %v", d, attachProbeTimeout)
	}
	// The refused client dialled nothing.
	if snap := stateGet(t, oneshot(t, s.srv.socket())); len(snap.Sessions) != 0 {
		t.Fatalf("the refused client opened sessions: %+v", snap.Sessions)
	}
}

func TestNoServerIsOneLineWithoutTheAlternateScreen(t *testing.T) {
	s := newL5(t, "")
	// Wide enough that the line does not wrap.
	x := s.attach(200, 24)
	line := refusedWith(t, x, noServerPrefix)
	if line != noServerPrefix+oneLine(ui.Message("noserver")) {
		t.Fatalf("the line is %q", line)
	}
	// Nothing was drawn: everything the client wrote, escape sequences
	// removed, is that line, and the screen shows it on its first row with
	// every other row blank.
	if out := strings.TrimSpace(x.Output()); out != line {
		t.Fatalf("the client wrote %q besides its line", out)
	}
	sc := x.Screen()
	rows := plainRows(sc)
	if rows[0] != line {
		t.Fatalf("the first row is %q:\n%s", rows[0], sc.Plain())
	}
	for _, r := range rows[1:] {
		if r != "" {
			t.Fatalf("a row besides the line is not blank:\n%s", sc.Plain())
		}
	}
}
