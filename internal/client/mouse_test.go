package client_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/clienttest"
	"devtools/internal/fleet"
	"devtools/internal/proto"
)

// --- fixtures of the mouse, scrolling and copy mode ---

// histFrame is a full frame of pane with history fields and modes; top is
// set while the client is scrolled in the pane.
func histFrame(pane string, seq uint64, cols, rows int, first, base int64, top *int64, modes proto.Modes, lines ...string) fleet.Update {
	f := proto.Frame{Pane: pane, Seq: seq, Cols: cols, Rows: rows, Full: true, First: first, Base: base, Top: top,
		Cursor: proto.Cursor{Visible: true, Shape: "block"}, Modes: modes}
	for y := 0; y < rows; y++ {
		s := ""
		if y < len(lines) {
			s = lines[y]
		}
		f.Lines = append(f.Lines, proto.Line{Y: y, S: s})
	}
	return fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(f)}
}

// showModes is a driver showing one pane p1 of 80 by 23 with the modes and
// history given.
func showModes(t *testing.T, opts client.Options, modes proto.Modes, first, base int64, lines ...string) *clienttest.Driver {
	t.Helper()
	d := clienttest.New(t, opts)
	s := snapshot("", proto.Pane{ID: "p1", Program: "zsh"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(histFrame("p1", 1, 80, 23, first, base, nil, modes, lines...))
	return d
}

// showSplit is a driver showing p1 and p2 side by side (split right, 40 and
// 39 columns) or stacked (split down, 11 rows each), p1 focused, with the
// modes of each.
func showSplit(t *testing.T, split string, m1, m2 proto.Modes) *clienttest.Driver {
	t.Helper()
	d := clienttest.New(t, client.Options{})
	s := snapshot(split, proto.Pane{ID: "p1", Program: "zsh"}, proto.Pane{ID: "p2", Program: "zsh"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	if split == proto.SplitDown {
		d.Update(histFrame("p1", 1, 80, 11, 0, 0, nil, m1))
		d.Update(histFrame("p2", 1, 80, 11, 0, 0, nil, m2))
	} else {
		d.Update(histFrame("p1", 1, 40, 23, 0, 0, nil, m1))
		d.Update(histFrame("p2", 1, 39, 23, 0, 0, nil, m2))
	}
	return d
}

func press(x, y int) tea.Msg   { return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft} }
func release(x, y int) tea.Msg { return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft} }
func drag(x, y int) tea.Msg    { return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft} }
func hover(x, y int) tea.Msg   { return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseNone} }
func wheel(x, y int, up bool) tea.Msg {
	b := tea.MouseWheelDown
	if up {
		b = tea.MouseWheelUp
	}
	return tea.MouseWheelMsg{X: x, Y: y, Button: b}
}

// inputsOf are the Input calls of one message type, in order.
func inputsOf(f *clienttest.Fleet, typ string) []proto.Msg {
	var out []proto.Msg
	for _, m := range f.Inputs() {
		if m.T == typ {
			out = append(out, m)
		}
	}
	return out
}

func mouseOf(t *testing.T, m proto.Msg) proto.Mouse {
	t.Helper()
	var v proto.Mouse
	if err := m.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func scrollOf(t *testing.T, m proto.Msg) proto.PaneScroll {
	t.Helper()
	var v proto.PaneScroll
	if err := m.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func modeOf(d *clienttest.Driver) string { return client.ChromeStateOf(d.M).Mode }

// --- the tests of 5a.12 ---

func TestAClickFocusesAndIsForwardedWhenTheProgramReportsMouse(t *testing.T) {
	d := showSplit(t, proto.SplitRight, proto.Modes{}, proto.Modes{Mouse: "press"})
	d.F.Reset()
	d.Send(press(45, 3))
	reqs := d.F.Requests()
	if len(reqs) != 1 || reqs[0].T != proto.TypePaneFocus {
		t.Fatalf("requests %v, want one pane.focus", reqs)
	}
	var pf proto.PaneFocus
	reqs[0].Decode(&pf)
	if pf != (proto.PaneFocus{Pane: "p2"}) {
		t.Errorf("pane.focus %+v, want the pane under the pointer", pf)
	}
	ms := inputsOf(d.F, proto.TypeInputMouse)
	if len(ms) != 1 {
		t.Fatalf("input.mouse %v, want the press", ms)
	}
	if got, want := mouseOf(t, ms[0]), (proto.Mouse{Pane: "p2", Kind: "press", Button: "left", X: 4, Y: 3}); got != want {
		t.Errorf("press %+v, want %+v", got, want)
	}
	d.Send(release(45, 3))
	ms = inputsOf(d.F, proto.TypeInputMouse)
	if len(ms) != 2 || mouseOf(t, ms[1]).Kind != "release" {
		t.Fatalf("input.mouse %v, want the press and its release", ms)
	}

	// a click into the focused pane of a program that does not read the
	// mouse is neither a request nor input
	d = showSplit(t, proto.SplitRight, proto.Modes{}, proto.Modes{Mouse: "press"})
	d.F.Reset()
	d.Send(press(3, 3))
	d.Send(release(3, 3))
	if rs, in := d.F.Requests(), d.F.Inputs(); len(rs) != 0 || len(in) != 0 {
		t.Errorf("requests %v, inputs %v; want none", rs, in)
	}
}

func TestMouseCoordinatesAreRelativeToThePane(t *testing.T) {
	cases := []struct {
		split      string
		x, y       int
		wantX, wtY int
	}{
		{proto.SplitRight, 41, 0, 0, 0},
		{proto.SplitRight, 79, 22, 38, 22},
		{proto.SplitDown, 7, 12, 7, 0},
		{proto.SplitDown, 7, 15, 7, 3},
	}
	for _, c := range cases {
		d := showSplit(t, c.split, proto.Modes{}, proto.Modes{Mouse: "all"})
		d.Send(press(c.x, c.y))
		ms := inputsOf(d.F, proto.TypeInputMouse)
		if len(ms) != 1 {
			t.Fatalf("%s at %d,%d: input.mouse %v, want one", c.split, c.x, c.y, ms)
		}
		if got := mouseOf(t, ms[0]); got.Pane != "p2" || got.X != c.wantX || got.Y != c.wtY {
			t.Errorf("%s at %d,%d: %+v, want p2 at %d,%d", c.split, c.x, c.y, got, c.wantX, c.wtY)
		}
	}
}

func TestMotionIsForwardedOnlyInModeAll(t *testing.T) {
	motions := func(d *clienttest.Driver) []proto.Mouse {
		var out []proto.Mouse
		for _, m := range inputsOf(d.F, proto.TypeInputMouse) {
			if mm := mouseOf(t, m); mm.Kind == "motion" {
				out = append(out, mm)
			}
		}
		return out
	}
	for _, mode := range []string{"press", "drag", "all"} {
		d := showModes(t, client.Options{}, proto.Modes{Mouse: mode}, 0, 0)
		d.Send(hover(5, 5))
		hovered := len(motions(d))
		d.Send(press(5, 5))
		d.Send(drag(6, 5))
		dragged := len(motions(d)) - hovered
		d.Send(release(6, 5))
		if want := map[bool]int{true: 1, false: 0}[mode == "all"]; hovered != want {
			t.Errorf("mode %q: %d motions without a button, want %d", mode, hovered, want)
		}
		if want := map[bool]int{true: 1, false: 0}[mode != "press"]; dragged != want {
			t.Errorf("mode %q: %d motions with the button held, want %d", mode, dragged, want)
		}
		if mode == "all" {
			if m := motions(d)[0]; m.Button != "none" || m.X != 5 || m.Y != 5 {
				t.Errorf("mode all: motion %+v, want button none at 5,5", m)
			}
		}
	}
	// a program that reads no mouse gets no motion at all
	d := showModes(t, client.Options{}, proto.Modes{}, 0, 0)
	d.Send(hover(5, 5))
	if ms := inputsOf(d.F, proto.TypeInputMouse); len(ms) != 0 {
		t.Errorf("input.mouse %v for a program that reads no mouse, want none", ms)
	}
}

func TestTheWheelBecomesArrowKeysInTheAlternateScreen(t *testing.T) {
	d := showModes(t, client.Options{}, proto.Modes{Alt: true}, 0, 0)
	d.Send(wheel(10, 10, true))
	d.Send(wheel(10, 10, false))
	var got []string
	for _, m := range d.F.Inputs() {
		if m.T != proto.TypeInputKey {
			t.Fatalf("input %s, want only keys", m.T)
		}
		k := keyOf(t, m)
		if k.Pane != "p1" {
			t.Errorf("key for pane %q, want p1", k.Pane)
		}
		got = append(got, k.Key)
	}
	want := []string{"up", "up", "up", "down", "down", "down"}
	if len(got) != len(want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys %v, want %v", got, want)
		}
	}
	if modeOf(d) != "normal" {
		t.Errorf("mode %q, want normal: the alternate screen has no history", modeOf(d))
	}
}

func TestTheWheelScrollsHistoryOtherwise(t *testing.T) {
	d := showModes(t, client.Options{}, proto.Modes{}, 0, 100, "live row")
	d.Send(wheel(10, 10, true))
	ss := inputsOf(d.F, proto.TypePaneScroll)
	if len(ss) != 1 {
		t.Fatalf("pane.scroll %v, want one", ss)
	}
	if s := scrollOf(t, ss[0]); s.Pane != "p1" || s.Top == nil || *s.Top != 97 {
		t.Fatalf("pane.scroll %+v, want p1 from line 97", s)
	}
	if modeOf(d) != "scrolled" {
		t.Errorf("mode %q, want scrolled", modeOf(d))
	}
	top := int64(97)
	d.Update(histFrame("p1", 2, 80, 23, 0, 100, &top, proto.Modes{}, "old line"))
	if sc := d.Screen(); sc.Row(0) != "old line" || !sc.Contains("scrolled") {
		t.Errorf("screen %q / status %q, want the old line and the scrolled row", sc.Row(0), sc.Row(23))
	}
	if c := client.ChromeStateOf(d.M).Copy; c == nil || c.Line != 98 || c.Total != 123 {
		t.Errorf("position %+v, want line 98 of 123", c)
	}
	// down to the live bottom: live again
	d.Send(wheel(10, 10, false))
	ss = inputsOf(d.F, proto.TypePaneScroll)
	if len(ss) != 2 || scrollOf(t, ss[1]).Top != nil {
		t.Fatalf("pane.scroll %v, want a second one that returns to live", ss)
	}
	if modeOf(d) != "normal" {
		t.Errorf("mode %q, want normal at the live bottom", modeOf(d))
	}
	// a program that reads the mouse gets the wheel itself
	d = showModes(t, client.Options{}, proto.Modes{Mouse: "press"}, 0, 100)
	d.Send(wheel(10, 10, true))
	ms := inputsOf(d.F, proto.TypeInputMouse)
	if len(ms) != 1 {
		t.Fatalf("input.mouse %v, want the wheel", ms)
	}
	if m := mouseOf(t, ms[0]); m.Kind != "wheel" || m.Button != "up" {
		t.Errorf("wheel %+v, want kind wheel, button up", m)
	}
	if ss := inputsOf(d.F, proto.TypePaneScroll); len(ss) != 0 {
		t.Errorf("pane.scroll %v, want none for a program that reads the mouse", ss)
	}
}

func TestTypingReturnsToLive(t *testing.T) {
	d := showModes(t, client.Options{}, proto.Modes{}, 0, 100)
	d.Send(wheel(10, 10, true))
	d.F.Reset()
	d.Type("a")
	in := d.F.Inputs()
	if len(in) != 2 || in[0].T != proto.TypePaneScroll || in[1].T != proto.TypeInputKey {
		t.Fatalf("inputs %v, want pane.scroll to live, then the key", in)
	}
	if s := scrollOf(t, in[0]); s.Top != nil {
		t.Errorf("pane.scroll %+v, want live", s)
	}
	if k := keyOf(t, in[1]); k.Key != "a" || k.Pane != "p1" {
		t.Errorf("key %+v, want a for p1", k)
	}
	if modeOf(d) != "normal" {
		t.Errorf("mode %q, want normal", modeOf(d))
	}
}

func TestAPressOnTheStatusRowDoesNothing(t *testing.T) {
	d := showSplit(t, proto.SplitRight, proto.Modes{Mouse: "all"}, proto.Modes{Mouse: "all"})
	before := d.Screen()
	d.F.Reset()
	for _, msg := range []tea.Msg{
		press(10, 23), release(10, 23), wheel(10, 23, true), hover(10, 23), // the status row
		press(40, 5), release(40, 5), wheel(40, 5, true), // the divider
	} {
		d.Send(msg)
	}
	if calls := d.F.Calls(); len(calls) != 0 {
		t.Errorf("fleet calls %v, want none", calls)
	}
	if modeOf(d) != "normal" {
		t.Errorf("mode %q, want normal", modeOf(d))
	}
	if after := d.Screen(); after.Plain() != before.Plain() {
		t.Errorf("the screen changed:\n%s\nwas\n%s", after.Plain(), before.Plain())
	}
}
