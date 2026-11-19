package client_test

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"devtools/internal/client"
	"devtools/internal/fleet"
	"devtools/internal/proto"
	"devtools/internal/session"
)

func TestAProgramsClipboardEventIsWritten(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "nvim"})
	d.Update(fleet.Update{Kind: fleet.Clipboard, Target: session.Target{Session: "s1"}, Data: []byte("hi there")})
	want := ansi.SetSystemClipboard("hi there")
	n := 0
	for _, r := range d.Raw() {
		if r == want {
			n++
		}
	}
	if n != 1 {
		t.Errorf("raw %q, want OSC 52 with the text once", d.Raw())
	}
	// one of a target that is no longer on screen is dropped
	d.Update(fleet.Update{Kind: fleet.Clipboard, Target: session.Target{Machine: "macmini", Session: "s1"}, Data: []byte("late")})
	if rawHas(d, ansi.SetSystemClipboard("late")) {
		t.Errorf("the clipboard of another target was written")
	}
	// the client never reads the clipboard
	for _, r := range d.Raw() {
		if r == ansi.RequestSystemClipboard {
			t.Errorf("the client asked the terminal for its clipboard")
		}
	}
}

func TestAnEmptyClipboardIsNeverWritten(t *testing.T) {
	d := showModes(t, client.Options{}, proto.Modes{}, 0, 0, "hello")
	d.Update(fleet.Update{Kind: fleet.Clipboard, Target: session.Target{Session: "s1"}})
	d.Update(fleet.Update{Kind: fleet.Clipboard, Target: session.Target{Session: "s1"}, Data: []byte{}})
	// a copy whose text is empty
	d.Key("ctrl+space")
	d.Key("[")
	d.Type("vy")
	req, _ := paneTextOf(t, d.F)
	d.Reply(proto.OKReply(req.ID, proto.PaneTextResult{Text: ""}))
	if raw := d.Raw(); len(raw) != 0 {
		t.Errorf("raw %q, want nothing written", raw)
	}
	if d.Screen().Contains("Copied") {
		t.Errorf("a notice counts an empty copy")
	}
}
