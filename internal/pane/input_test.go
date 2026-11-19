package pane

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"devtools/internal/testkit"
	"devtools/internal/vt"
)

func TestKeysArriveByteExact(t *testing.T) {
	p, _ := startHelper(t, "hex")
	waitScreen(t, p, "ready")
	for _, k := range []vt.Key{
		{Name: "a", Text: "a"},
		{Name: "a", Mod: 1, Text: "A", Shifted: 'A'},
		{Name: "enter"},
		{Name: "up"},
		{Name: "c", Mod: 4},
		{Name: "ä", Text: "ä"},
		{Name: "tab"},
		{Name: "backspace"},
		{Name: "no-such-key"}, // unknown: nothing
	} {
		if !p.Key(k) {
			t.Fatalf("Key(%+v) = false on a running pane", k)
		}
	}
	waitBytes(t, p, hex("aA\r\x1b[A\x03ä\t\x7f"))
}

func TestAltEnterArrivesAsEscCRInALegacyProgram(t *testing.T) {
	p, _ := startHelper(t, "hex")
	waitScreen(t, p, "ready")
	p.Key(vt.Key{Name: "enter", Mod: 2})
	waitBytes(t, p, "1b 0d")
}

func TestAKittyProgramGetsCSIu(t *testing.T) {
	p, _ := startHelper(t, "modes", "kitty")
	waitScreen(t, p, "ready")
	p.Key(vt.Key{Name: "a", Mod: 4})
	p.Key(vt.Key{Name: "b", Text: "b"})
	waitBytes(t, p, hex("\x1b[97;5ub"))
	if s := p.Snapshot(0, false); s.Modes.Kitty != 1 {
		t.Errorf("Modes.Kitty = %d, want 1", s.Modes.Kitty)
	}
}

func TestPasteIsBracketedWhenTheProgramAskedForIt(t *testing.T) {
	plain, _ := startHelper(t, "hex")
	waitScreen(t, plain, "ready")
	plain.Paste("hi", true, false)
	plain.Paste("yo", false, true)
	waitBytes(t, plain, hex("hiyo"))

	asked, _ := startHelper(t, "modes", "paste")
	waitScreen(t, asked, "ready")
	asked.Paste("hi", true, false)
	asked.Paste("yo", false, true)
	waitBytes(t, asked, hex("\x1b[200~hiyo\x1b[201~"))
}

func TestMouseArrivesOnlyWhenTheProgramAskedForIt(t *testing.T) {
	plain, _ := startHelper(t, "hex")
	waitScreen(t, plain, "ready")
	plain.Mouse(vt.Mouse{Kind: "press", Button: "left", X: 3, Y: 2})
	plain.Mouse(vt.Mouse{Kind: "release", Button: "left", X: 3, Y: 2})
	plain.Key(vt.Key{Name: "z", Text: "z"}) // input is applied in order: the key comes after the mouse
	waitBytes(t, plain, hex("z"))

	asked, _ := startHelper(t, "modes", "mouse")
	waitScreen(t, asked, "ready")
	asked.Mouse(vt.Mouse{Kind: "press", Button: "left", X: 3, Y: 2})
	asked.Mouse(vt.Mouse{Kind: "release", Button: "left", X: 3, Y: 2})
	asked.Mouse(vt.Mouse{Kind: "motion", Button: "none", X: 9, Y: 9}) // press mode reports no motion
	asked.Key(vt.Key{Name: "z", Text: "z"})
	waitBytes(t, asked, hex("\x1b[<0;4;3M\x1b[<0;4;3mz"))
}

func TestFocusReports(t *testing.T) {
	plain, _ := startHelper(t, "hex")
	waitScreen(t, plain, "ready")
	plain.Focus(true)
	plain.Key(vt.Key{Name: "z", Text: "z"})
	waitBytes(t, plain, hex("z"))

	asked, _ := startHelper(t, "modes", "focus")
	waitScreen(t, asked, "ready")
	asked.Focus(true)
	asked.Focus(false)
	waitBytes(t, asked, hex("\x1b[I\x1b[O"))
}

func TestResizeReachesTheProgram(t *testing.T) {
	p, _ := startHelper(t, "size")
	waitScreen(t, p, "200 x 24")
	p.Resize(100, 30)
	waitScreen(t, p, "100 x 30")
	if s := p.Snapshot(0, false); s.Cols != 100 || s.Rows != 30 || len(s.Lines) != 30 {
		t.Errorf("after Resize the snapshot is %dx%d with %d lines", s.Cols, s.Rows, len(s.Lines))
	}
}

func TestOnlyTheNewestResizeIsApplied(t *testing.T) {
	p, _ := startHelper(t, "size")
	waitScreen(t, p, "200 x 24")
	// Hold the actor, so the sizes pile up.
	release := make(chan struct{})
	p.ops.push(func(*actor) { <-release })
	for i := 1; i <= 50; i++ {
		p.Resize(60+i, 20) // never blocks, although the actor is held
	}
	close(release)
	waitScreen(t, p, "110 x 20")
	if n := p.a.resizes.Load(); n != 1 {
		t.Errorf("%d resizes reached the terminal, want 1", n)
	}
	if strings.Contains(screen(p), "61 x 20") {
		t.Errorf("an older size reached the program:\n%s", screen(p))
	}
	if s := p.Snapshot(0, false); s.Cols != 110 || s.Rows != 20 {
		t.Errorf("the snapshot is %dx%d, want 110x20", s.Cols, s.Rows)
	}
}

func TestInputBlocksWhileTheProgramDoesNotRead(t *testing.T) {
	p, _ := startHelper(t, "rawsleep")
	waitScreen(t, p, "ready")
	chunk := strings.Repeat("x", 256<<10)
	var sent atomic.Int32
	result := make(chan bool, 1)
	go func() {
		for i := range 64 { // 16 MiB: far more than the queue and the kernel hold
			if !p.Paste(chunk, i == 0, i == 63) {
				result <- false
				return
			}
			sent.Add(1)
		}
		result <- true
	}()
	testkit.Eventually(t, 20*time.Second, "the input queue is full", p.out.full)
	// While the queue is full the actor takes no input: the next Paste
	// blocks, and output and other requests are still served.
	n := sent.Load()
	if n >= 64 {
		t.Fatalf("all %d chunks were taken although the program reads nothing", n)
	}
	if s := p.Snapshot(0, false); s.Rows != 24 {
		t.Fatalf("Snapshot while input blocks: %d rows", s.Rows)
	}
	select {
	case r := <-result:
		t.Fatalf("Paste returned %v while the program reads nothing", r)
	default:
	}
	// The pane ends: the blocked Paste returns false.
	p.Close()
	select {
	case r := <-result:
		if r {
			t.Errorf("the blocked Paste returned true after Close")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("a blocked Paste did not return after Close")
	}
	if p.Key(vt.Key{Name: "a", Text: "a"}) {
		t.Errorf("Key on a closed pane returned true")
	}
}

func TestBlockedInputDoesNotStopOutput(t *testing.T) {
	p, _ := startHelper(t, "ticks")
	waitScreen(t, p, "tick 000001")
	chunk := strings.Repeat("y", 256<<10)
	go func() {
		for i := 0; p.Paste(chunk, i == 0, false); i++ {
		}
	}()
	testkit.Eventually(t, 20*time.Second, "the input queue is full", p.out.full)
	last := lastTick(screen(p))
	testkit.Eventually(t, 20*time.Second, "ticks after the input blocked", func() bool {
		return lastTick(screen(p)) >= last+10
	})
	if !p.out.full() {
		t.Errorf("the queue drained although the program reads nothing")
	}
}

// lastTick returns the highest tick number the helper ticks printed.
func lastTick(s string) int {
	n := -1
	for _, line := range strings.Split(s, "\n") {
		var v int
		if _, err := fmt.Sscanf(line, "tick %d", &v); err == nil && v > n {
			n = v
		}
	}
	return n
}
