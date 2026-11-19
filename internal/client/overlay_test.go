package client_test

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/clienttest"
	"devtools/internal/client/keys"
	"devtools/internal/client/screen"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"
)

// insetOverlay is a fakeOverlay that wants a rectangle inside the pane area.
type insetOverlay struct{ fakeOverlay }

func (o *insetOverlay) Bounds(w, h int) screen.Rect { return screen.Rect{X: 2, Y: 1, W: 20, H: 4} }

func TestTheOverlayGetsInputAndCoversThePanes(t *testing.T) {
	var opened []*fakeOverlay
	d := clienttest.New(t, client.Options{OpenHub: fakeHub(&opened)})
	d.Send(tea.WindowSizeMsg{Width: 40, Height: 8})
	s := snapshot("", proto.Pane{ID: "p1", Program: "zsh"})
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frame("p1", 1, 40, 7, true, "pane row 0 that the hub covers", "pane row 1", "pane row 2"))
	d.Key("ctrl+space")
	d.Key("o")
	if len(opened) != 1 {
		t.Fatalf("%d hubs opened, want 1", len(opened))
	}
	o := opened[0]
	d.F.Reset()
	d.Type("ab")
	d.Send(tea.PasteMsg{Content: "pasted"})
	d.Send(press(3, 2))
	d.Key("ctrl+j")
	var keysGot, pastes, clicks int
	for _, msg := range o.got {
		switch msg := msg.(type) {
		case tea.KeyPressMsg:
			keysGot++
		case tea.PasteMsg:
			pastes++
		case tea.MouseClickMsg:
			clicks++
			if msg.X != 3 || msg.Y != 2 {
				t.Errorf("click at %d,%d, want 3,2 in the hub's cells", msg.X, msg.Y)
			}
		}
	}
	if keysGot != 3 || pastes != 1 || clicks != 1 {
		t.Errorf("the hub got %d keys, %d pastes, %d clicks; want 3, 1, 1", keysGot, pastes, clicks)
	}
	if o.w != 40 || o.h != 7 {
		t.Errorf("the hub's size %dx%d, want the pane area 40x7", o.w, o.h)
	}
	if in, rs := d.F.Inputs(), d.F.Requests(); len(in) != 0 || len(rs) != 0 {
		t.Errorf("inputs %v, requests %v under the hub; want none (ctrl+j is the hub's, not the pane focus)", in, rs)
	}
	if rs := d.F.CallsOf("Resize"); len(rs) != 0 {
		t.Errorf("resizes %v, want none: the hub never resizes a pane", rs)
	}
	golden.Assert(t, "overlay-covers-40x8", d.Screen().Plain())

	t.Run("an overlay inside the pane area gets the mouse in its own cells", func(t *testing.T) {
		in := &insetOverlay{}
		d := clienttest.New(t, client.Options{Hub: &fleet.HubRequest{},
			OpenHub: func(fleet.HubRequest, <-chan hub.Event, ui.Theme) chrome.Overlay { return in }})
		d.Send(press(5, 3))
		d.Send(press(30, 3)) // outside it
		clicks := 0
		for _, msg := range in.got {
			if c, ok := msg.(tea.MouseClickMsg); ok {
				clicks++
				if c.X != 3 || c.Y != 2 {
					t.Errorf("click at %d,%d, want 3,2", c.X, c.Y)
				}
			}
		}
		if clicks != 1 {
			t.Errorf("%d clicks reached the hub, want the one inside it", clicks)
		}
		if v := d.View(); v.Cursor == nil || v.Cursor.X != 6 || v.Cursor.Y != 1 {
			t.Errorf("cursor %+v, want the hub's cursor 4,0 offset by its bounds", v.Cursor)
		}
	})
}

func TestAnOverlayOutcomeSwitchesAndCloses(t *testing.T) {
	var opened []*fakeOverlay
	d := showOne(t, client.Options{OpenHub: fakeHub(&opened)}, proto.Pane{ID: "p1", Program: "zsh"}, "the pane")
	d.Key("ctrl+space")
	d.Key("o")
	pick := session.Target{Machine: "macmini", Session: "s9"}
	opened[0].pick = &pick
	d.F.Reset()
	d.Key("enter")
	calls := d.F.Calls()
	var names []string
	for _, c := range calls {
		names = append(names, c.Name)
	}
	if len(calls) != 2 || calls[0].Name != "HubClosed" || calls[1].Name != "Switch" || calls[1].Target != pick {
		t.Fatalf("calls %v, want HubClosed and then Switch to %+v", names, pick)
	}
	if sc := d.Screen(); sc.Contains("HUB") || sc.Row(0) != "the pane" {
		t.Errorf("row 0 %q, want the pane again once the hub closed", sc.Row(0))
	}
	if d.Quit() {
		t.Error("the client ended")
	}
	// closing without a choice switches nothing
	d.Key("ctrl+space")
	d.Key("o")
	d.F.Reset()
	d.Key("esc")
	if sw := d.F.CallsOf("Switch"); len(sw) != 0 || d.Quit() {
		t.Errorf("switches %v, quit %v; want none: a session is on screen", sw, d.Quit())
	}
}

func TestAnOpenHubUpdateOpensTheOverlay(t *testing.T) {
	var opened []*fakeOverlay
	d := showOne(t, client.Options{OpenHub: fakeHub(&opened)}, proto.Pane{ID: "p1", Program: "zsh"})
	d.F.Reset()
	d.Update(fleet.Update{Kind: fleet.OpenHub, Hub: fleet.HubRequest{Mode: hub.ModeAgents, Query: "fix"}})
	if len(opened) != 1 || opened[0].req.Mode != hub.ModeAgents || opened[0].req.Query != "fix" {
		t.Fatalf("opened %+v, want the agents hub with its query", opened)
	}
	if hs := d.F.CallsOf("HubOpened"); len(hs) != 1 {
		t.Errorf("HubOpened calls %v, want 1", hs)
	}
	if !d.Screen().Contains("HUB agents") {
		t.Errorf("screen %q, want the hub drawn", d.Screen().Row(0))
	}
	if v := d.View(); v.Cursor == nil || v.Cursor.X != 4 || v.Cursor.Y != 0 {
		t.Errorf("cursor %+v, want the hub's", v.Cursor)
	}
}

func TestHubActionsOpenTheirScope(t *testing.T) {
	table, err := keys.New([]config.Bind{{Chord: "prefix+f", Action: "hub", Arg: "new", Line: 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]hub.Mode{"o": hub.ModeAll, "s": hub.ModeSessions, "a": hub.ModeAgents, "f": hub.ModeNew} {
		var opened []*fakeOverlay
		d := showOne(t, client.Options{OpenHub: fakeHub(&opened), Keys: table}, proto.Pane{ID: "p1", Program: "zsh"})
		d.Key("ctrl+space")
		d.Key(key)
		if len(opened) != 1 || opened[0].req.Mode != want {
			t.Errorf("prefix+%s opened %+v, want the hub of mode %v", key, opened, want)
			continue
		}
		if here := opened[0].req.Here; here.Dir != dir {
			t.Errorf("prefix+%s: here %+v, want the displayed directory", key, here)
		}
	}
}

func TestClosingTheHubWithNoSessionEndsTheClient(t *testing.T) {
	var opened []*fakeOverlay
	d := clienttest.New(t, client.Options{Hub: &fleet.HubRequest{}, OpenHub: fakeHub(&opened)})
	d.Update(online())
	if len(opened) != 1 {
		t.Fatalf("%d hubs opened at the start, want 1", len(opened))
	}
	d.Key("esc")
	if !d.Quit() {
		t.Error("closing the hub with nothing displayed did not end the client")
	}
	if hc := d.F.CallsOf("HubClosed"); len(hc) != 1 {
		t.Errorf("HubClosed calls %v, want 1", hc)
	}
	// a choice does not end it
	opened = nil
	d = clienttest.New(t, client.Options{Hub: &fleet.HubRequest{}, OpenHub: fakeHub(&opened)})
	pick := session.Target{Session: "s1"}
	opened[0].pick = &pick
	d.Key("enter")
	if d.Quit() {
		t.Error("a choice in the hub ended the client")
	}
}
