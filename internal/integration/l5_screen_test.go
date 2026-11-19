package integration

// Level 5: the mouse, copy mode and the clipboard, the size of the outer
// terminal, two terminals, detach and reattach, a server restart, and the
// colours of the outer terminal.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit/golden"
)

// sgrMouse is the SGR report of a mouse event at the cell x, y (from 0):
// button 0 is the left button, 64 the wheel up; release ends in "m".
func sgrMouse(button, x, y int, release bool) string {
	end := "M"
	if release {
		end = "m"
	}
	return fmt.Sprintf("\x1b[<%d;%d;%d%s", button, x+1, y+1, end)
}

func TestAClickFocusesAndTheWheelScrolls(t *testing.T) {
	// The first pane prints 200 lines and waits: history to scroll. The
	// pane split from it runs hex.
	s := newL5(t, "once first lines 200")
	x := s.attach(80, 24)
	x.WaitFor("line 200")
	press(x, "v")
	x.WaitScreen("the hex pane on the right", func(s golden.Screen) bool {
		r := []rune(plainRows(s)[0] + strings.Repeat(" ", 80))
		return r[40] == '|' && strings.HasPrefix(string(r[41:]), "ready")
	})
	// A click into the left pane focuses it: what is typed next reaches the
	// program there, whose terminal echoes it under "line 200".
	x.Type(sgrMouse(0, 5, 5, false) + sgrMouse(0, 5, 5, true))
	x.Type("a")
	x.WaitScreen("a echoed in the left pane", func(s golden.Screen) bool {
		rows := plainRows(s)
		for i := 1; i < s.Rows-1; i++ {
			if strings.HasPrefix(rows[i-1], "line 200 ") && strings.HasPrefix(rows[i], "a ") {
				return hexIn(s, 41, 80) == ""
			}
		}
		return false
	})
	// The wheel over the left pane scrolls its history: older lines appear
	// and the status row names the scrolled view.
	before := x.Screen()
	if paneHas(before, "line 150 ") || !paneHas(before, "line 200") {
		t.Fatalf("the live screen of the left pane:\n%s", before.Plain())
	}
	for range 5 {
		x.Type(sgrMouse(64, 5, 5, false))
	}
	x.WaitScreen("the scrolled view", func(s golden.Screen) bool {
		return strings.Contains(statusRow(s), "scrolled") && !paneHas(s, "line 200")
	})
	// A click into the right pane focuses it again, and keys go there.
	x.Type(sgrMouse(0, 60, 5, false) + sgrMouse(0, 60, 5, true))
	x.Type("k")
	x.WaitScreen("k in the right pane", func(s golden.Screen) bool { return hexIn(s, 41, 80) == hexBytes("k") })
}

func TestCopyModeYankWritesOSC52(t *testing.T) {
	s := newL5(t, "lines 30")
	x := s.attach(80, 24)
	x.WaitFor("line 30")
	press(x, "[")
	x.WaitScreen("copy mode", func(s golden.Screen) bool { return strings.Contains(statusRow(s), "copy") })
	// The copy cursor starts on the live cursor, the empty row under
	// "line 30": up once, select the line, copy and leave.
	x.Type("k")
	x.Type("V")
	x.WaitScreen("a line selected", func(s golden.Screen) bool { return strings.Contains(statusRow(s), "selecting") })
	x.Type("y")
	x.WaitScreen("copy mode left", func(s golden.Screen) bool { return !strings.Contains(statusRow(s), "copy") })
	waitClipboard(t, x.Clipboard, "line 30")
}

// waitClipboard waits until the outer terminal received an OSC 52 whose
// text, without its line end, is want.
func waitClipboard(t *testing.T, clips func() []string, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, c := range clips() {
			if strings.TrimRight(c, "\r\n") == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the outer terminal's clipboard got %q, want %q", clips(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAProgramsClipboardReachesTheOuterTerminal(t *testing.T) {
	// The program sets the clipboard to "hi" (base64 aGk=) with OSC 52.
	s := newL5(t, "once first osc 52;c;aGk=")
	x := s.attach(80, 24)
	x.WaitScreen("the status row", func(s golden.Screen) bool { return strings.Contains(statusRow(s), "work") })
	waitClipboard(t, x.Clipboard, "hi")
}

func TestResizingTheOuterTerminalResizesTheProgram(t *testing.T) {
	s := newL5(t, "size")
	x := s.attach(80, 24)
	// The pane area is the window without the status row.
	x.WaitFor("80 x 23")
	x.Resize(100, 30)
	x.WaitFor("100 x 29")
	x.Resize(60, 20)
	x.WaitFor("60 x 19")
	if r := statusRow(x.Screen()); !strings.Contains(r, "work") {
		t.Fatalf("after the resize the last row is %q, not the status row", r)
	}
}

func TestTwoAttachedTerminalsOfDifferentSizes(t *testing.T) {
	s := newL5(t, "size")
	small := s.attach(80, 24)
	small.WaitFor("80 x 23")
	large := s.attach(100, 30)
	large.WaitScreen("the status row of the second window", func(s golden.Screen) bool {
		return strings.Contains(statusRow(s), "work")
	})
	// The size follows the window that was used last (3.2.3): typing into
	// one makes the program print its size, and both windows show it, the
	// smaller one clipped, the larger one padded.
	large.Type("a")
	large.WaitFor("100 x 29")
	small.WaitFor("100 x 29")
	small.Type("b")
	small.WaitFor("80 x 23")
	large.WaitScreen("the smaller size in the larger window, padded", func(s golden.Screen) bool {
		rows := plainRows(s)
		for i := s.Rows - 2; i >= 0; i-- {
			r := []rune(rows[i])
			if strings.Trim(rows[i], ".") == "" {
				continue // a row of the pad under the program's 23 rows
			}
			if len(r) > 80 && strings.Trim(string(r[80:]), ".") != "" {
				return false // the pad beside the program's 80 columns
			}
			if text := strings.TrimSpace(string(r[:min(len(r), 80)])); text != "" {
				return strings.HasSuffix(text, "80 x 23")
			}
		}
		return false
	})
}

func TestDetachLeavesTheProgramRunningAndReattachShowsIt(t *testing.T) {
	s := newL5(t, "hex")
	x := s.attach(80, 24)
	shows(x, "ready")
	x.Type("abc")
	waitHex(x, "abc in the pane", 0, 80, hexBytes("abc"))
	press(x, "d")
	if code, out := x.Wait(); code != 0 {
		t.Fatalf("detach exited %d: %s", code, out)
	}
	// The pane ran on; a new window shows what it printed and what it
	// prints now.
	y := s.attach(80, 24)
	waitHex(y, "abc after the reattach", 0, 80, hexBytes("abc"))
	y.Type("d")
	waitHex(y, "abcd", 0, 80, hexBytes("abcd"))
}

func TestAServerRestartUnderAnAttachedClientShowsTheSessionAgain(t *testing.T) {
	longerScreens(t, 30*time.Second)
	s := newL5(t, "hex")
	x := s.attach(80, 24)
	shows(x, "ready")
	x.Type("abc")
	waitHex(x, "abc in the pane", 0, 80, hexBytes("abc"))
	if code := s.srv.stop(); code != 0 {
		t.Fatalf("the server stopped with %d", code)
	}
	// The client stays: its local link retries (3.5.2).
	time.Sleep(200 * time.Millisecond)
	if x.Exited() {
		code, out := x.Wait()
		t.Fatalf("the client ended with the server (%d): %s", code, out)
	}
	s.srv = startBoot(t, s.home, "hex", envTestDir+"="+s.testDir)
	// The restored session is shown again: a new pane in the same session,
	// whose program has read nothing yet.
	x.WaitScreen("the restored session", func(sc golden.Screen) bool {
		return strings.Contains(statusRow(sc), "work") && paneHas(sc, "ready") && hexOf(sc) == "" &&
			!strings.Contains(plainRows(sc)[0], "retrying")
	})
	x.Type("z")
	waitHex(x, "z in the restored pane", 0, 80, hexBytes("z"))
}

func TestDefaultColoursReachTheOuterTerminalAsDefaults(t *testing.T) {
	// The program writes "plain" in the default colours and "red" in
	// palette colour 1, then behaves like hex.
	s := newL5(t, `modes plain \e[31mred\e[0m`)
	x := s.attach(80, 24)
	sc := x.WaitFor("plainredready")
	runs := strings.SplitN(sc.Runs(), "styles\n", 2)[1]
	var paneRuns []string
	for _, line := range strings.Split(strings.TrimSpace(runs), "\n") {
		if line == "" || strings.HasPrefix(line, fmt.Sprintf("%d ", sc.Rows-1)) {
			continue // the status row: attributes only
		}
		paneRuns = append(paneRuns, line)
	}
	// Only the three red cells carry a colour; "plain", "ready" and every
	// blank cell are in the outer terminal's own colours, and no cell has
	// a background.
	if len(paneRuns) != 1 || !strings.HasPrefix(paneRuns[0], "0 5 3 ") || !strings.Contains(paneRuns[0], "p1") {
		t.Fatalf("the style runs of the pane area are %q, want one run of three cells in p1 at 0,5:\n%s", paneRuns, sc.Runs())
	}
	if strings.Contains(runs, " on ") {
		t.Fatalf("a cell has a background colour:\n%s", sc.Runs())
	}
}
