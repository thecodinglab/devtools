package client_test

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"devtools/internal/client"
	"devtools/internal/client/clienttest"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
)

// paneTextOf is the newest pane.text request.
func paneTextOf(t *testing.T, f *clienttest.Fleet) (proto.Msg, proto.PaneText) {
	t.Helper()
	var last proto.Msg
	found := false
	for _, m := range f.Requests() {
		if m.T == proto.TypePaneText {
			last, found = m, true
		}
	}
	if !found {
		t.Fatal("no pane.text was sent")
	}
	var q proto.PaneText
	if err := last.Decode(&q); err != nil {
		t.Fatal(err)
	}
	return last, q
}

// rawHas reports whether the model wrote s past the renderer.
func rawHas(d *clienttest.Driver, s string) bool {
	for _, r := range d.Raw() {
		if r == s {
			return true
		}
	}
	return false
}

// reflowUpdate is an event.reflow of the displayed target.
func reflowUpdate(pane string, top *int64, marks ...proto.Pos) fleet.Update {
	return fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(proto.Reflow{Pane: pane, Top: top, Marks: marks})}
}

func TestADragSelectsAndCopiesOnRelease(t *testing.T) {
	d := showModes(t, client.Options{}, proto.Modes{}, 0, 0, "hello world")
	d.Send(press(0, 0))
	if modeOf(d) != "normal" {
		t.Fatalf("mode %q after a press, want normal: a press alone selects nothing", modeOf(d))
	}
	d.Send(drag(2, 0))
	d.Send(drag(4, 0))
	if modeOf(d) != "copy" {
		t.Fatalf("mode %q during the drag, want copy", modeOf(d))
	}
	if st := client.ChromeStateOf(d.M); st.Copy == nil || st.Copy.Selecting != "char" {
		t.Errorf("copy %+v, want a selection by character", st.Copy)
	}
	d.Send(release(4, 0))
	req, q := paneTextOf(t, d.F)
	if want := (proto.PaneText{Pane: "p1", From: proto.Pos{Line: 0, Col: 0}, To: proto.Pos{Line: 0, Col: 4}}); q != want {
		t.Errorf("pane.text %+v, want %+v", q, want)
	}
	if modeOf(d) != "normal" {
		t.Errorf("mode %q after the release, want normal", modeOf(d))
	}
	d.Reply(proto.OKReply(req.ID, proto.PaneTextResult{Text: "hello"}))
	if !rawHas(d, ansi.SetSystemClipboard("hello")) {
		t.Errorf("raw %q, want OSC 52 with hello", d.Raw())
	}
	if !d.Screen().Contains("Copied 5 characters") {
		t.Errorf("status %q, want the notice of the copy", d.Screen().Row(23))
	}
	// the selection was drawn in the pane while it lasted: not now
	if st := client.CanvasOf(d.M).At(0, 0).Style; st.Attrs != 0 {
		t.Errorf("cell 0,0 styled %+v after copy mode ended", st)
	}
}

func TestCopyModeYankSetsTheClipboardAndLeaves(t *testing.T) {
	d := showModes(t, client.Options{}, proto.Modes{}, 0, 50, "", "", "hello world")
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(proto.Frame{Pane: "p1", Seq: 2, Cols: 80, Rows: 23,
		First: 0, Base: 50, Cursor: proto.Cursor{X: 0, Y: 2, Visible: true}})})
	d.F.Reset()
	d.Key("ctrl+space")
	d.Key("[")
	if modeOf(d) != "copy" {
		t.Fatalf("mode %q, want copy", modeOf(d))
	}
	if v := d.View(); v.Cursor != nil {
		t.Errorf("the terminal cursor is shown in copy mode at %d,%d", v.Cursor.X, v.Cursor.Y)
	}
	ss := inputsOf(d.F, proto.TypePaneScroll)
	if len(ss) != 1 {
		t.Fatalf("pane.scroll %v, want the first position of copy mode", ss)
	}
	if s := scrollOf(t, ss[0]); s.Top == nil || *s.Top != 50 || len(s.Marks) != 1 || s.Marks[0] != (proto.Pos{Line: 52, Col: 0}) {
		t.Errorf("pane.scroll %+v, want top 50 and the copy cursor at line 52 as its mark", s)
	}
	d.Type("v")
	d.Type("llll")
	ss = inputsOf(d.F, proto.TypePaneScroll)
	if s := scrollOf(t, ss[len(ss)-1]); len(s.Marks) != 2 || s.Marks[0] != (proto.Pos{Line: 52, Col: 4}) || s.Marks[1] != (proto.Pos{Line: 52, Col: 0}) {
		t.Errorf("marks %+v, want the cursor at 52,4 and the anchor at 52,0", s.Marks)
	}
	sc := d.Screen()
	if st := client.CanvasOf(d.M).At(1, 2).Style; st.Attrs == 0 {
		t.Errorf("a selected cell is drawn unstyled")
	}
	if !sc.Contains("copy") {
		t.Errorf("status %q, want the copy row", sc.Row(23))
	}
	d.F.Reset()
	d.Type("y")
	req, q := paneTextOf(t, d.F)
	if want := (proto.PaneText{Pane: "p1", From: proto.Pos{Line: 52, Col: 0}, To: proto.Pos{Line: 52, Col: 4}}); q != want {
		t.Errorf("pane.text %+v, want %+v", q, want)
	}
	if modeOf(d) != "normal" {
		t.Errorf("mode %q after y, want normal", modeOf(d))
	}
	ss = inputsOf(d.F, proto.TypePaneScroll)
	if len(ss) != 1 || scrollOf(t, ss[0]).Top != nil {
		t.Errorf("pane.scroll %v, want one that returns to live", ss)
	}
	d.Reply(proto.OKReply(req.ID, proto.PaneTextResult{Text: "hello"}))
	if !rawHas(d, ansi.SetSystemClipboard("hello")) {
		t.Errorf("raw %q, want OSC 52 with hello", d.Raw())
	}
	if n := len(d.F.Inputs()); n != 1 {
		t.Errorf("%d inputs, want only the return to live: the client never pastes what it copied", n)
	}
}

// enterCopyAt is a driver in copy mode in p1 with a history of 50 lines,
// the cursor at line 55, a selection by character from 55,0 to 55,1.
func enterCopyAt(t *testing.T, opts client.Options) *clienttest.Driver {
	t.Helper()
	d := showModes(t, opts, proto.Modes{}, 0, 50)
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(proto.Frame{Pane: "p1", Seq: 2, Cols: 80, Rows: 23,
		First: 0, Base: 50, Cursor: proto.Cursor{X: 0, Y: 5, Visible: true}})})
	d.Key("ctrl+space")
	d.Key("[")
	d.Type("vl")
	if modeOf(d) != "copy" {
		t.Fatalf("mode %q, want copy", modeOf(d))
	}
	return d
}

func TestCopyModeSurvivesAResizeOnItsText(t *testing.T) {
	d := enterCopyAt(t, client.Options{})
	d.F.Reset()
	// the pane was narrowed: the server moved the marks with their text
	d.Update(reflowUpdate("p1", nil, proto.Pos{Line: 70, Col: 1}, proto.Pos{Line: 70, Col: 0}))
	d.Update(histFrame("p1", 3, 60, 23, 0, 65, nil, proto.Modes{}))
	if modeOf(d) != "copy" {
		t.Fatalf("mode %q after the reflow, want copy", modeOf(d))
	}
	d.Type("y")
	_, q := paneTextOf(t, d.F)
	if want := (proto.PaneText{Pane: "p1", From: proto.Pos{Line: 70, Col: 0}, To: proto.Pos{Line: 70, Col: 1}}); q != want {
		t.Errorf("pane.text %+v, want the new numbers %+v", q, want)
	}
}

func TestCopyModeEndsWhenAMarksTextIsGone(t *testing.T) {
	d := enterCopyAt(t, client.Options{})
	d.F.Reset()
	d.Update(reflowUpdate("p1", nil, proto.Pos{Line: 70, Col: 1}, proto.Pos{Line: -1, Col: 0}))
	if modeOf(d) != "normal" {
		t.Fatalf("mode %q, want normal: the anchor's text is gone", modeOf(d))
	}
	ss := inputsOf(d.F, proto.TypePaneScroll)
	if len(ss) != 1 || scrollOf(t, ss[0]).Top != nil || len(scrollOf(t, ss[0]).Marks) != 0 {
		t.Errorf("pane.scroll %v, want one that returns to live without marks", ss)
	}
	if rs := d.F.Requests(); len(rs) != 0 {
		t.Errorf("requests %v, want none: nothing is copied", rs)
	}
}

func TestAStaleViewEndsCopyModeAndKeepsThePrefix(t *testing.T) {
	var opened []*fakeOverlay
	d := enterCopyAt(t, client.Options{OpenHub: fakeHub(&opened)})
	d.Key("ctrl+space")
	if st := client.ChromeStateOf(d.M); st.Mode != "copy" {
		t.Fatalf("mode %q with the prefix pending in copy mode, want copy (precedence)", st.Mode)
	}
	d.Update(fleet.Update{Kind: fleet.Stale, Problem: hub.Problem{Reason: "lost"}})
	st := client.ChromeStateOf(d.M)
	if st.Mode != "prefix" || st.View != "stale" || st.Copy != nil {
		t.Errorf("mode %q view %q copy %+v, want the prefix in a stale view and no copy mode", st.Mode, st.View, st.Copy)
	}
	// the prefix that waited acts
	d.Key("o")
	if len(opened) != 1 {
		t.Fatalf("%d hubs opened by the prefix that survived, want 1", len(opened))
	}
	d.Key("esc")
	// copy mode cannot start again until the link is back
	d.Key("ctrl+space")
	d.Key("[")
	if modeOf(d) != "normal" {
		t.Errorf("mode %q after prefix+[ in a stale view, want normal", modeOf(d))
	}

	t.Run("the scrolled view and the confirm end too", func(t *testing.T) {
		d := showModes(t, client.Options{}, proto.Modes{}, 0, 100)
		d.Send(wheel(10, 10, true))
		if modeOf(d) != "scrolled" {
			t.Fatalf("mode %q, want scrolled", modeOf(d))
		}
		d.Update(fleet.Update{Kind: fleet.Stale, Problem: hub.Problem{Reason: "lost"}})
		if modeOf(d) != "normal" {
			t.Errorf("mode %q in a stale view, want normal", modeOf(d))
		}
		d.F.Reset()
		d.Send(wheel(10, 10, true))
		if in := d.F.Inputs(); len(in) != 0 {
			t.Errorf("inputs %v, want none: the scrolled view cannot start in a stale view", in)
		}

		d = showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "nvim", Busy: true})
		d.Key("ctrl+space")
		d.Key("x")
		if modeOf(d) != "confirm" {
			t.Fatalf("mode %q, want confirm", modeOf(d))
		}
		d.Update(fleet.Update{Kind: fleet.Stale, Problem: hub.Problem{Reason: "lost"}})
		if modeOf(d) != "normal" {
			t.Errorf("mode %q in a stale view, want normal", modeOf(d))
		}
	})
}
