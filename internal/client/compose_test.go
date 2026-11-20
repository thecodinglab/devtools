package client_test

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/clienttest"
	"devtools/internal/client/screen"
	"devtools/internal/fleet"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/testkit/golden"
)

func TestTheFirstShownDrawsThePaneAndTheStatusRow(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	d.Send(tea.WindowSizeMsg{Width: 40, Height: 8})
	s := snapshot("", proto.Pane{ID: "p1", Program: "zsh"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frameWith("p1", 1, 40, 7, proto.Cursor{X: 2, Y: 1, Visible: true, Shape: "block"}, proto.Modes{},
		"~/dev/devtools/main", "$ \x1b[0;1mls\x1b[0m", "go.mod  internal"))
	golden.Assert(t, "first-shown-40x8", d.Screen().Runs())
}

func TestFramesRedrawOnlyDirtyRows(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"}, "row 0", "row 1", "row 2")
	d.Screen()
	// a mark on the canvas that no frame wrote: a row that is redrawn
	// loses it
	c := client.CanvasOf(d.M)
	c.Set(70, 0, screen.Cell{S: "#", W: 1})
	c.Set(70, 1, screen.Cell{S: "#", W: 1})
	draws := client.StatusDrawsOf(d.M)
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(proto.Frame{Pane: "p1", Seq: 2, Cols: 80, Rows: 23,
		Lines: []proto.Line{{Y: 1, S: "row 1 changed"}}, Cursor: proto.Cursor{Visible: true}})})
	sc := d.Screen()
	if got := sc.Row(0); !strings.HasSuffix(got, "#") {
		t.Errorf("row 0 was redrawn although it did not change: %q", got)
	}
	if got := sc.Row(1); got != "row 1 changed" {
		t.Errorf("row 1 = %q, want the changed row and nothing else", got)
	}
	if got := client.StatusDrawsOf(d.M); got != draws {
		t.Errorf("the status row was drawn %d more times, want none: its state did not change", got-draws)
	}
}

func TestClippedAndPaddedViews(t *testing.T) {
	t.Run("clipped", func(t *testing.T) {
		d := clienttest.New(t, client.Options{})
		d.Send(tea.WindowSizeMsg{Width: 24, Height: 6})
		s := snapshot(proto.SplitRight, proto.Pane{ID: "p1", Program: "zsh"}, proto.Pane{ID: "p2", Program: "nvim"})
		d.F.SetState(s)
		d.Update(online())
		d.Update(shown("", s))
		d.Update(frame("p1", 1, 15, 9, true, "left 0123456789", "left row 1"))
		d.Update(frame("p2", 1, 15, 9, true, "right pane", "right row 1"))
		golden.Assert(t, "view-clipped-24x6", d.Screen().Plain())
	})
	t.Run("padded", func(t *testing.T) {
		d := clienttest.New(t, client.Options{})
		d.Send(tea.WindowSizeMsg{Width: 30, Height: 8})
		s := snapshot(proto.SplitDown, proto.Pane{ID: "p1", Program: "zsh"}, proto.Pane{ID: "p2", Program: "zsh"})
		d.F.SetState(s)
		d.Update(online())
		d.Update(shown("", s))
		d.Update(frame("p1", 1, 20, 3, true, "top pane"))
		d.Update(frame("p2", 1, 20, 2, true, "bottom pane"))
		golden.Assert(t, "view-padded-30x8", d.Screen().Plain())
	})
}

func TestTheCursorFollowsTheFocusedPane(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	s := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	s.Tabs[0].Focused = "p2"
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frameWith("p1", 1, 40, 23, proto.Cursor{X: 1, Y: 1, Visible: true}, proto.Modes{}))
	d.Update(frameWith("p2", 1, 39, 23, proto.Cursor{X: 3, Y: 5, Visible: true, Shape: "bar"}, proto.Modes{}))
	sc := d.Screen()
	if !sc.CursorVisible || sc.CursorX != 41+3 || sc.CursorY != 5 {
		t.Fatalf("cursor %d,%d visible %v, want 44,5: p2 starts after p1 and the divider", sc.CursorX, sc.CursorY, sc.CursorVisible)
	}
	if v := d.View(); v.Cursor.Shape != tea.CursorBar {
		t.Errorf("cursor shape %v, want the pane's bar", v.Cursor.Shape)
	}
	s.Tabs[0].Focused = "p1"
	s.Rev++
	d.F.SetState(s)
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(s)})
	sc = d.Screen()
	if !sc.CursorVisible || sc.CursorX != 1 || sc.CursorY != 1 {
		t.Fatalf("after the focus moved: cursor %d,%d visible %v, want 1,1", sc.CursorX, sc.CursorY, sc.CursorVisible)
	}
}

func TestTheCursorIsHiddenWhenItsCellIsClipped(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	d.Send(tea.WindowSizeMsg{Width: 30, Height: 10})
	s := snapshot("", proto.Pane{ID: "p1"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frameWith("p1", 1, 80, 23, proto.Cursor{X: 5, Y: 3, Visible: true}, proto.Modes{}))
	if sc := d.Screen(); !sc.CursorVisible || sc.CursorX != 5 || sc.CursorY != 3 {
		t.Fatalf("cursor inside the window: %d,%d visible %v, want 5,3", sc.CursorX, sc.CursorY, sc.CursorVisible)
	}
	d.Update(frameWith("p1", 2, 80, 23, proto.Cursor{X: 50, Y: 3, Visible: true}, proto.Modes{}))
	if sc := d.Screen(); sc.CursorVisible {
		t.Fatalf("cursor at column 50 of a window of 30 is visible at %d,%d", sc.CursorX, sc.CursorY)
	}
	d.Update(frameWith("p1", 3, 80, 23, proto.Cursor{X: 5, Y: 12, Visible: true}, proto.Modes{}))
	if sc := d.Screen(); sc.CursorVisible {
		t.Fatalf("cursor at row 12 of a pane area of 9 rows is visible at %d,%d", sc.CursorX, sc.CursorY)
	}
}

func TestKeyboardEnhancementsFollowTheFocusedPane(t *testing.T) {
	d := clienttest.New(t, client.Options{OpenHub: fakeHub(nil)})
	s := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frameWith("p1", 1, 40, 23, proto.Cursor{}, proto.Modes{Keys: 31}))
	d.Update(frameWith("p2", 1, 39, 23, proto.Cursor{}, proto.Modes{Keys: 1}))
	all := tea.KeyboardEnhancements{ReportEventTypes: true, ReportAlternateKeys: true, ReportAllKeysAsEscapeCodes: true, ReportAssociatedText: true}
	if got := d.View().KeyboardEnhancements; got != all {
		t.Fatalf("p1 enabled flags 31: enhancements %+v, want all four", got)
	}
	d.Update(frameWith("p1", 2, 40, 23, proto.Cursor{}, proto.Modes{Keys: 2 | 8}))
	want := tea.KeyboardEnhancements{ReportEventTypes: true, ReportAllKeysAsEscapeCodes: true}
	if got := d.View().KeyboardEnhancements; got != want {
		t.Fatalf("p1 flags 10: enhancements %+v, want %+v", got, want)
	}
	s.Tabs[0].Focused = "p2"
	s.Rev++
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(s)})
	if got := d.View().KeyboardEnhancements; got != (tea.KeyboardEnhancements{}) {
		t.Fatalf("p2 (flag 1 only, which bubbletea always asks) has the focus: %+v, want none of the four", got)
	}
	s.Tabs[0].Focused = "p1"
	s.Rev++
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(s)})
	d.Key("ctrl+space")
	d.Key("o")
	if got := d.View().KeyboardEnhancements; got != (tea.KeyboardEnhancements{}) {
		t.Fatalf("the hub is open: enhancements %+v, want none", got)
	}
}

func TestTheOuterTitleFollowsTheCommittedView(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	if got := d.View().WindowTitle; got != "devtools" {
		t.Fatalf("title with no view %q, want devtools", got)
	}
	s := snapshot("", proto.Pane{ID: "p1", Title: "vim: secret.txt"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	if got := d.View().WindowTitle; got != "devtools-main" {
		t.Fatalf("title %q, want the session's label", got)
	}
	remote := shown("macmini", s)
	remote.Target = session.Target{Machine: "macmini", Session: "s1"}
	d.Update(remote)
	if got := d.View().WindowTitle; got != "macmini: devtools-main" {
		t.Fatalf("title %q, want the machine before the label", got)
	}
}

func TestTheStatusRowIsRedrawnAtEachMinute(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	if row := d.Screen().Row(23); !strings.Contains(row, "14:32") || !strings.Contains(row, "Tue 17 Nov") {
		t.Fatalf("status row %q, want the date and 14:32", row)
	}
	draws := client.StatusDrawsOf(d.M)
	d.Advance(29 * time.Second)
	d.Screen()
	if got := client.StatusDrawsOf(d.M); got != draws {
		t.Fatalf("the status row was drawn again before the minute changed")
	}
	d.Advance(time.Second)
	if row := d.Screen().Row(23); !strings.Contains(row, "14:33") {
		t.Fatalf("status row %q at 14:33:00, want 14:33", row)
	}
	if got := client.StatusDrawsOf(d.M); got != draws+1 {
		t.Fatalf("the status row was drawn %d times at the minute, want once", got-draws)
	}
	d.Advance(time.Minute)
	if row := d.Screen().Row(23); !strings.Contains(row, "14:34") {
		t.Fatalf("status row %q a minute later, want 14:34", row)
	}
}

func TestAPaneWhoseProgramEndedShowsThePaneMessage(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh", State: proto.PaneExited,
		Exit: &proto.PaneExit{Code: 0, Signal: "", Text: "zsh was killed (signal 9)."}}, "old output")
	sc := d.Screen()
	if sc.Row(0) != "zsh was killed (signal 9)." || !strings.Contains(sc.Row(1), "Press enter") {
		t.Fatalf("rows %q, %q: want the pane message", sc.Row(0), sc.Row(1))
	}
}

// The divider's pointer points at the pane with the keys and is drawn in
// comment while the window has lost the terminal's focus (5a.6.5). Neither
// a move of the focus between the panes nor the outer focus changes the
// layout, and the divider follows both all the same.
func TestTheDividerFollowsTheFocusedPaneAndTheWindowFocus(t *testing.T) {
	d := clienttest.New(t, client.Options{Chrome: chrome.NewLook(chrome.LookOptions{})})
	s := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frame("p1", 1, 40, 23, true))
	d.Update(frame("p2", 1, 39, 23, true))
	d.Screen()
	pointer := func() screen.Cell { return client.CanvasOf(d.M).At(40, 11) }
	left := pointer()

	s.Tabs[0].Focused = "p2"
	s.Rev++
	d.F.SetState(s)
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(s)})
	d.Screen()
	right := pointer()
	if right.S == left.S {
		t.Fatalf("the focus moved from p1 to p2 and the pointer stayed %q", right.S)
	}

	d.Send(tea.BlurMsg{})
	d.Screen()
	blurred := pointer()
	if blurred.S != right.S || blurred.Style == right.Style {
		t.Fatalf("after the blur the pointer is %q %+v, want %q in another style than %+v", blurred.S, blurred.Style, right.S, right.Style)
	}
	d.Send(tea.FocusMsg{})
	d.Screen()
	if got := pointer(); got != right {
		t.Fatalf("after the focus came back the pointer is %+v, want %+v", got, right)
	}

	s.Tabs[0].Focused = "p1"
	s.Rev++
	d.F.SetState(s)
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(s)})
	d.Screen()
	if got := pointer(); got != left {
		t.Fatalf("the focus moved back to p1 and the pointer is %+v, want %+v", got, left)
	}
}

// With p1 | (p2 / p3) every divider is drawn, and only those the focused
// pane touches point at it.
func TestEveryDividerIsDrawnAndThoseAtTheFocusedPanePoint(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	s := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"}, proto.Pane{ID: "p3"})
	s.Tabs[0].Layout = proto.Node{Split: proto.SplitRight, Nodes: []proto.Node{
		{Pane: "p1"},
		{Split: proto.SplitDown, Nodes: []proto.Node{{Pane: "p2"}, {Pane: "p3"}}},
	}}
	s.Tabs[0].Focused = "p3"
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frame("p1", 1, 40, 23, true))
	d.Update(frame("p2", 1, 39, 11, true))
	d.Update(frame("p3", 1, 39, 11, true))
	sc := d.Screen()
	// the vertical divider at column 40 points right at row 17, the middle
	// of p3's rows; the horizontal one at row 11 points down
	if got := []rune(sc.Row(17))[40]; got != '>' {
		t.Fatalf("row 17 %q: want > at column 40", sc.Row(17))
	}
	if got := []rune(sc.Row(5))[40]; got != '|' {
		t.Fatalf("row 5 %q: want | at column 40, no pointer beside p2", sc.Row(5))
	}
	if row := sc.Row(11); !strings.HasPrefix(string([]rune(row)[41:]), "-") || !strings.Contains(row, "v") {
		t.Fatalf("row 11 %q: want the divider of p2 and p3 pointing down", row)
	}
}
