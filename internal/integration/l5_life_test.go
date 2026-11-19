package integration

// Level 5: a key during a flood, and a whole session from attach to the
// server's stop without a process left behind.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/testkit/termtest"
)

// floodKeyLimit is the bound of TestAKeyTypedDuringAFloodIsAnsweredWithinHalfASecond.
// T5 of 10.4 asks 250 ms at level 6; level 5 runs the race-instrumented
// test binary as the server and allows twice that.
const floodKeyLimit = 500 * time.Millisecond

func TestAKeyTypedDuringAFloodIsAnsweredWithinHalfASecond(t *testing.T) {
	longerScreens(t, 30*time.Second)
	// The first pane writes numbered lines without an end; the pane split
	// from it runs hex and gets the keys.
	s := newL5(t, "once first flood 1000000")
	x := s.attach(80, 24)
	x.WaitScreen("the flood", func(s golden.Screen) bool { return paneHas(s, "xxxxxxxxxx") })
	press(x, "v")
	x.WaitScreen("the hex pane on the right", func(s golden.Screen) bool {
		r := []rune(plainRows(s)[0] + strings.Repeat(" ", 80))
		return r[40] == '|' && strings.Contains(string(r[41:]), "ready")
	})
	want := ""
	var worst time.Duration
	for i := range 10 {
		k := string(rune('a' + i))
		want += hexBytes(k)
		start := time.Now()
		x.Type(k)
		x.WaitScreen("the echo of "+k+" during the flood", func(s golden.Screen) bool {
			return strings.HasSuffix(hexIn(s, 41, 80), want)
		})
		d := time.Since(start)
		worst = max(worst, d)
		if !paneHas(x.Screen(), "xxxxxxxxxx") {
			t.Fatal("the flood is no longer on the screen")
		}
	}
	t.Logf("the slowest of 10 keys during the flood took %v", worst)
	if worst > floodKeyLimit {
		t.Fatalf("a key typed during the flood took %v, more than %v", worst, floodKeyLimit)
	}
}

// pidsPattern is what the helper pidwait prints.
var pidsPattern = regexp.MustCompile(`pids (\d+) (\d+)`)

// panePids returns the pids every pidwait on the screen printed.
func panePids(s golden.Screen) []int {
	var out []int
	for _, r := range plainRows(s) {
		for _, m := range pidsPattern.FindAllStringSubmatch(r, -1) {
			for _, v := range m[1:] {
				n, _ := strconv.Atoi(v)
				out = append(out, n)
			}
		}
	}
	return out
}

func TestNoProcessIsLeftAfterAFullSession(t *testing.T) {
	s := newL5(t, "pidwait")
	x := s.attach(160, 24)
	x.WaitScreen("the first pane's pids", func(s golden.Screen) bool { return len(panePids(s)) == 2 })
	press(x, "v")
	sc := x.WaitScreen("both panes' pids", func(s golden.Screen) bool { return len(panePids(s)) == 4 })
	press(x, "c")
	x.WaitScreen("the second tab's pids", func(s golden.Screen) bool {
		return strings.Contains(statusRow(s), "[2]") && len(panePids(s)) == 2
	})
	pids := append(panePids(sc), panePids(x.Screen())...)
	client := x.Pid()
	// A second window on the same session, then both detach.
	y := s.attach(80, 24)
	y.WaitScreen("the second window", func(s golden.Screen) bool { return strings.Contains(statusRow(s), "work") })
	for _, w := range []*termtest.Terminal{x, y} {
		press(w, "d")
		if code, out := w.Wait(); code != 0 {
			t.Fatalf("detach exited %d: %s", code, out)
		}
	}
	for _, pid := range pids {
		if !alive(pid) {
			t.Fatalf("pane process %d ended with the detach", pid)
		}
	}
	server := s.srv.pid()
	if code := s.srv.stop(); code != 0 {
		t.Fatalf("the server stopped with %d", code)
	}
	all := append([]int{server, client, y.Pid()}, pids...)
	testkit.Eventually(t, 10*time.Second, "every process of the session ends", func() bool {
		for _, pid := range all {
			if alive(pid) {
				return false
			}
		}
		return true
	})
}
