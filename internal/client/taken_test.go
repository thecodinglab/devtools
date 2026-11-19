package client_test

// A press consumed by the hub, the confirm or copy mode consumes its
// release and repeats (3.2.4), also once the key has ended that mode and
// the next event of the key would otherwise go to the pane.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/clienttest"
	"devtools/internal/fleet"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// showReporting is a driver showing p, whose program asked for releases
// and repeats (modes.keys bit 2): only the client can hold them back.
func showReporting(t *testing.T, opts client.Options, p proto.Pane) *clienttest.Driver {
	t.Helper()
	d := showOne(t, opts, p, "hello")
	d.Update(frameWith(p.ID, 2, 80, 23, proto.Cursor{}, proto.Modes{Keys: 3}, "hello"))
	return d
}

// repeatOf is the chord's auto-repeat.
func repeatOf(t *testing.T, chord string) tea.KeyPressMsg {
	t.Helper()
	k := clienttest.ParseKey(t, chord)
	k.IsRepeat = true
	return tea.KeyPressMsg(k)
}

// assertTaken shows p, runs enter to open a mode, presses chord, which the
// mode consumes and which ends it, and then sends the chord's repeats, its
// release, or both: none reaches the fleet. A new press of the key
// afterwards is the pane's again, with its repeat and its release.
func assertTaken(t *testing.T, opts func() client.Options, p proto.Pane, chord string, enter func(t *testing.T, d *clienttest.Driver)) {
	t.Helper()
	for _, tc := range []struct {
		name            string
		repeat, release bool
	}{{"release", false, true}, {"repeat", true, false}, {"repeat then release", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			d := showReporting(t, opts(), p)
			enter(t, d)
			d.Key(chord)
			if m := modeOf(d); m != "normal" {
				t.Fatalf("mode %q after %s, want normal: the key ends the mode", m, chord)
			}
			d.F.Reset()
			if tc.repeat {
				d.Send(repeatOf(t, chord))
				d.Send(repeatOf(t, chord))
			}
			if tc.release {
				d.Release(chord)
			}
			for _, in := range d.F.Inputs() {
				t.Fatalf("the mode consumed the press of %s, but a later event reached the fleet: %s %s", chord, in.T, in.Body)
			}
			d.Key(chord)
			d.Send(repeatOf(t, chord))
			d.Release(chord)
			var got []proto.Key
			for _, in := range d.F.Inputs() {
				if in.T == proto.TypeInputKey {
					got = append(got, keyOf(t, in))
				}
			}
			if len(got) != 3 || got[0].Release || got[0].Repeat || !got[1].Repeat || !got[2].Release {
				t.Fatalf("a new press of %s, its repeat and its release sent %+v, want all three to the pane", chord, got)
			}
		})
	}
}

func TestAKeyThatEndsCopyModeConsumesItsReleaseAndRepeats(t *testing.T) {
	enter := func(t *testing.T, d *clienttest.Driver) {
		d.Key("ctrl+space")
		d.Key("[")
		if m := modeOf(d); m != "copy" {
			t.Fatalf("mode %q, want copy", m)
		}
	}
	plain := func() client.Options { return client.Options{} }
	for _, chord := range []string{"q", "esc"} {
		t.Run(chord, func(t *testing.T) {
			assertTaken(t, plain, proto.Pane{ID: "p1", Program: "zsh"}, chord, enter)
		})
	}
	t.Run("a copy-mode key released after copy mode ended", func(t *testing.T) {
		d := showReporting(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
		enter(t, d)
		d.Key("k")
		d.Key("q")
		d.F.Reset()
		d.Release("k")
		d.Release("q")
		if in := d.F.Inputs(); len(in) != 0 {
			t.Fatalf("releases of copy mode's keys reached the pane: %v", in)
		}
	})
}

func TestAKeyThatClosesTheHubConsumesItsReleaseAndRepeats(t *testing.T) {
	var opened []*fakeOverlay
	withHub := func() client.Options { opened = nil; return client.Options{OpenHub: fakeHub(&opened)} }
	open := func(t *testing.T, d *clienttest.Driver) {
		d.Key("ctrl+space")
		d.Key("o")
		if len(opened) != 1 {
			t.Fatalf("%d hubs opened, want 1", len(opened))
		}
	}
	p := proto.Pane{ID: "p1", Program: "zsh"}
	t.Run("esc", func(t *testing.T) {
		assertTaken(t, withHub, p, "esc", open)
	})
	t.Run("a choice: nothing is held for the switch's target", func(t *testing.T) {
		d := showReporting(t, withHub(), p)
		open(t, d)
		pick := session.Target{Machine: "macmini", Session: "s9"}
		opened[0].pick = &pick
		d.Key("enter")
		if sw := d.F.CallsOf("Switch"); len(sw) != 1 {
			t.Fatalf("switches %v, want the choice", sw)
		}
		d.F.Reset()
		d.Send(repeatOf(t, "enter"))
		d.Release("enter")
		if in := d.F.Inputs(); len(in) != 0 {
			t.Fatalf("the enter that chose in the hub went on as input: %v", in)
		}
	})
}

func TestAnAnswerToTheConfirmConsumesItsReleaseAndRepeats(t *testing.T) {
	ask := func(t *testing.T, d *clienttest.Driver) {
		d.Key("ctrl+space")
		d.Key("x")
		if m := modeOf(d); m != "confirm" {
			t.Fatalf("mode %q, want confirm", m)
		}
	}
	busy := proto.Pane{ID: "p1", Program: "nvim", Busy: true}
	t.Run("esc", func(t *testing.T) {
		assertTaken(t, func() client.Options { return client.Options{} }, busy, "esc", ask)
	})
	t.Run("enter", func(t *testing.T) {
		d := showReporting(t, client.Options{}, busy)
		ask(t, d)
		d.Key("enter")
		rs := d.F.Requests()
		if len(rs) == 0 || rs[len(rs)-1].T != proto.TypePaneClose {
			t.Fatalf("requests %v, want pane.close", rs)
		}
		req := rs[len(rs)-1]
		d.F.Reset()
		d.Send(repeatOf(t, "enter"))
		d.Release("enter")
		// pane.close opens the typeahead gate: the events wait for its
		// reply and are routed then
		d.Reply(proto.OKReply(req.ID, nil))
		if in := d.F.Inputs(); len(in) != 0 {
			t.Fatalf("the enter that answered the confirm reached the pane: %v", in)
		}
	})
}

// TestARepeatOfAKeyThePrefixConsumedStaysOutOfTheHub: while the hub is
// open, the repeats of a press the key table consumed (the key after the
// prefix, the key that opened the hub) are consumed too, until the key is
// released or pressed anew (3.2.4); the repeats of a key the hub got, a
// direct binding such as ctrl+j too, are the hub's.
func TestARepeatOfAKeyThePrefixConsumedStaysOutOfTheHub(t *testing.T) {
	hubKeys := func(o *fakeOverlay) int {
		n := 0
		for _, msg := range o.got {
			if _, ok := msg.(tea.KeyPressMsg); ok {
				n++
			}
		}
		return n
	}
	// start opens the hub with nothing displayed, where closing it ends the
	// client (5a.10.2)
	start := func(t *testing.T) (*clienttest.Driver, *fakeOverlay) {
		t.Helper()
		var opened []*fakeOverlay
		d := clienttest.New(t, client.Options{Hub: &fleet.HubRequest{}, OpenHub: fakeHub(&opened)})
		d.Update(online())
		if len(opened) != 1 {
			t.Fatalf("%d hubs opened at the start, want 1", len(opened))
		}
		return d, opened[0]
	}
	for _, chord := range []string{"c", "esc"} {
		t.Run("prefix+"+chord+" held", func(t *testing.T) {
			d, o := start(t)
			d.Key("ctrl+space")
			d.Key(chord)
			d.Send(repeatOf(t, chord))
			d.Send(repeatOf(t, chord))
			if n := hubKeys(o); n != 0 || d.Quit() {
				t.Fatalf("after prefix+%s and its repeats the hub got %d keys and quit is %v; want none and the client running", chord, n, d.Quit())
			}
			if hc := d.F.CallsOf("HubClosed"); len(hc) != 0 {
				t.Fatalf("HubClosed calls %v, want none", hc)
			}
			d.Release(chord)
			d.Key(chord)
			if n := hubKeys(o); n != 1 {
				t.Fatalf("a new press of %s after the release: the hub got %d keys, want 1", chord, n)
			}
		})
	}
	t.Run("a repeat without the release, after a new press, is the hub's", func(t *testing.T) {
		d, o := start(t)
		d.Key("ctrl+space")
		d.Key("c")
		d.Key("c")
		d.Send(repeatOf(t, "c"))
		if n := hubKeys(o); n != 2 {
			t.Fatalf("the hub got %d keys, want the new press of c and its repeat", n)
		}
	})
	t.Run("the repeats of ctrl+j are the hub's", func(t *testing.T) {
		d, o := start(t)
		d.Key("ctrl+j")
		d.Send(repeatOf(t, "ctrl+j"))
		d.Send(repeatOf(t, "ctrl+j"))
		if n := hubKeys(o); n != 3 {
			t.Fatalf("the hub got %d keys of ctrl+j held, want the press and its two repeats", n)
		}
	})
	t.Run("the key that opened the hub, held", func(t *testing.T) {
		var opened []*fakeOverlay
		d := showReporting(t, client.Options{OpenHub: fakeHub(&opened)}, proto.Pane{ID: "p1", Program: "zsh"})
		d.Key("ctrl+space")
		d.Key("o")
		if len(opened) != 1 {
			t.Fatalf("%d hubs opened, want 1", len(opened))
		}
		d.F.Reset()
		d.Send(repeatOf(t, "o"))
		d.Release("o")
		if n := hubKeys(opened[0]); n != 0 {
			t.Errorf("the hub got %d repeats of the o that opened it, want none", n)
		}
		if in := d.F.Inputs(); len(in) != 0 {
			t.Errorf("inputs %v, want none", in)
		}
	})
}
