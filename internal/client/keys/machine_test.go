package keys

import (
	"testing"

	"devtools/internal/config"
	"devtools/internal/proto"
)

// The events as the client builds them from what the terminal reported
// (4.2.6): the key without modifiers applied, the modifier bits, the text.
var (
	ctrlSpace = Event{Name: "space", Mod: proto.ModCtrl}
	escape    = Event{Name: "escape"}
)

// key is a plain key press that types its character.
func key(s string) Event { return Event{Name: s, Text: s} }

func release(e Event) Event { e.Release = true; e.Text = ""; return e }
func repeat(e Event) Event  { e.Repeat = true; return e }

// want asserts the verdict, the pending flag and the action of a result.
func want(t *testing.T, what string, r Result, v Verdict, pending bool, a Action) {
	t.Helper()
	if r.Verdict != v || r.Pending != pending || r.Action != a {
		t.Errorf("%s = %+v, want verdict %d, pending %v, action %v", what, r, v, pending, a)
	}
}

func TestPrefixThenBoundKeyActs(t *testing.T) {
	m := NewMachine(Defaults())
	for _, c := range []struct {
		e Event
		a Action
	}{
		{key("o"), Action{"hub", ""}},
		{key("s"), Action{"hub", "sessions"}},
		{key("a"), Action{"hub", "agents"}},
		{key("c"), Action{"tab-new", ""}},
		{key("n"), Action{"tab-next", ""}},
		{key("p"), Action{"tab-prev", ""}},
		{key("1"), Action{"tab-select", "1"}},
		{key("9"), Action{"tab-select", "9"}},
		{Event{Name: "5", Mod: proto.ModShift, Text: "%"}, Action{"split-right", ""}},
		{key("v"), Action{"split-right", ""}},
		{Event{Name: "'", Mod: proto.ModShift, Text: `"`}, Action{"split-down", ""}},
		{key("-"), Action{"split-down", ""}},
		{key("x"), Action{"pane-close", ""}},
		{key("["), Action{"copy-mode", ""}},
		{Event{Name: "9", Mod: proto.ModShift, Text: "("}, Action{"session-prev", ""}},
		{Event{Name: "0", Mod: proto.ModShift, Text: ")"}, Action{"session-next", ""}},
		{key("d"), Action{"detach", ""}},
		{key("q"), Action{"detach", ""}},
	} {
		want(t, "prefix", m.Handle(ctrlSpace, Context{}), Swallow, true, Action{})
		want(t, "prefix+"+c.e.Text, m.Handle(c.e, Context{}), Act, false, c.a)
		// The machine is idle again: the same key goes to the pane.
		want(t, c.e.Text+" after the action", m.Handle(c.e, Context{}), Forward, false, Action{})
	}
	// A bound prefix+f acts too.
	m = NewMachine(mustNew(t, binds([3]string{"prefix+f", "hub", "new"}), nil))
	m.Handle(ctrlSpace, Context{})
	want(t, "prefix+f", m.Handle(key("f"), Context{}), Act, false, Action{"hub", "new"})
	// The nil table is the defaults.
	m = NewMachine(nil)
	m.Handle(ctrlSpace, Context{})
	want(t, "prefix+o on nil", m.Handle(key("o"), Context{}), Act, false, Action{"hub", ""})
}

func TestPrefixTwiceSendsThePrefix(t *testing.T) {
	m := NewMachine(Defaults())
	m.Handle(ctrlSpace, Context{})
	want(t, "prefix twice", m.Handle(ctrlSpace, Context{}), SendPrefix, false, Action{})
	want(t, "a after it", m.Handle(key("a"), Context{}), Forward, false, Action{})
	// With caps lock on it is the same chord.
	m.Handle(Event{Name: "space", Mod: proto.ModCtrl | proto.ModCaps}, Context{})
	want(t, "prefix twice, caps lock", m.Handle(Event{Name: "space", Mod: proto.ModCtrl | proto.ModCaps}, Context{}), SendPrefix, false, Action{})
}

func TestUnboundKeyAfterThePrefixEndsItWithANotice(t *testing.T) {
	m := NewMachine(Defaults())
	for _, e := range []Event{key("f"), key("z"), key("ä"), {Name: "enter"}, {Name: "o", Mod: proto.ModAlt}, {Name: "f5"}, {Name: "space"}} {
		m.Handle(ctrlSpace, Context{})
		r := m.Handle(e, Context{})
		if r.Verdict != Swallow || !r.Unbound || r.Pending || r.More {
			t.Errorf("prefix then %+v = %+v, want swallowed and unbound, the prefix ended", e, r)
		}
		// The next key reaches the pane: the stray prefix cost one key.
		want(t, "the key after", m.Handle(key("y"), Context{}), Forward, false, Action{})
	}
}

func TestQuestionMarkAfterThePrefixReportsMoreAndKeepsItPending(t *testing.T) {
	m := NewMachine(Defaults())
	m.Handle(ctrlSpace, Context{})
	// As a legacy terminal and as the kitty protocol report "?".
	for _, e := range []Event{key("?"), {Name: "/", Mod: proto.ModShift, Text: "?"}} {
		r := m.Handle(e, Context{})
		if r.Verdict != Swallow || !r.More || !r.Pending || r.Unbound {
			t.Errorf("? %+v = %+v, want More with the prefix pending", e, r)
		}
	}
	want(t, "o after ?", m.Handle(key("o"), Context{}), Act, false, Action{"hub", ""})
	// Without the prefix ? is typed.
	want(t, "? alone", m.Handle(key("?"), Context{}), Forward, false, Action{})
}

func TestEscapeCancelsThePrefix(t *testing.T) {
	m := NewMachine(Defaults())
	for _, e := range []Event{escape, {Name: "esc"}, {Name: "escape", Mod: proto.ModNum}} {
		m.Handle(ctrlSpace, Context{})
		r := m.Handle(e, Context{})
		if r.Verdict != Swallow || r.Pending || r.Unbound || r.More {
			t.Errorf("esc %+v = %+v, want swallowed, cancelled, no notice", e, r)
		}
		want(t, "o after esc", m.Handle(key("o"), Context{}), Forward, false, Action{})
	}
	// esc without the prefix goes to the pane.
	want(t, "esc alone", m.Handle(escape, Context{}), Forward, false, Action{})
	// After ? esc still cancels.
	m.Handle(ctrlSpace, Context{})
	m.Handle(key("?"), Context{})
	want(t, "esc after ?", m.Handle(escape, Context{}), Swallow, false, Action{})
	// Reset ends a pending prefix as well.
	m.Handle(ctrlSpace, Context{})
	m.Reset()
	want(t, "o after Reset", m.Handle(key("o"), Context{}), Forward, false, Action{})
}

func TestAModifierAloneKeepsThePrefixPending(t *testing.T) {
	m := NewMachine(Defaults())
	m.Handle(ctrlSpace, Context{})
	for _, name := range []string{"leftshift", "rightshift", "leftctrl", "rightalt", "leftsuper", "leftmeta", "lefthyper", "isolevel3shift", "capslock", "shift"} {
		r := m.Handle(Event{Name: name, Mod: proto.ModShift}, Context{})
		if r.Verdict != Swallow || !r.Pending || r.Unbound {
			t.Errorf("%s after the prefix = %+v, want swallowed and pending", name, r)
		}
	}
	// The shifted key that follows is read: prefix then shift then 5 is %.
	want(t, "shift+5", m.Handle(Event{Name: "5", Mod: proto.ModShift, Text: "%"}, Context{}), Act, false, Action{"split-right", ""})
	// A modifier without the prefix goes to the pane.
	want(t, "leftshift alone", m.Handle(Event{Name: "leftshift"}, Context{}), Forward, false, Action{})
}

func TestAConsumedPressConsumesItsReleaseAndRepeats(t *testing.T) {
	m := NewMachine(Defaults())
	// The prefix: its repeats and its release, also with ctrl let go first.
	want(t, "prefix", m.Handle(ctrlSpace, Context{}), Swallow, true, Action{})
	want(t, "prefix repeat", m.Handle(repeat(ctrlSpace), Context{}), Swallow, true, Action{})
	want(t, "prefix release", m.Handle(release(Event{Name: "space"}), Context{}), Swallow, true, Action{})
	// The bound key: press acts, repeats and release are swallowed.
	want(t, "o", m.Handle(key("o"), Context{}), Act, false, Action{"hub", ""})
	want(t, "o repeat", m.Handle(repeat(key("o")), Context{}), Swallow, false, Action{})
	want(t, "o repeat", m.Handle(repeat(key("o")), Context{}), Swallow, false, Action{})
	want(t, "o release", m.Handle(release(key("o")), Context{}), Swallow, false, Action{})
	// Once released, the key is the pane's again, release included.
	want(t, "o again", m.Handle(key("o"), Context{}), Forward, false, Action{})
	want(t, "o release again", m.Handle(release(key("o")), Context{}), Forward, false, Action{})
	// A swallowed unbound key, esc, ? and the second prefix are consumed too.
	for _, e := range []Event{key("z"), escape, key("?"), ctrlSpace} {
		m.Reset()
		m.Handle(ctrlSpace, Context{})
		m.Handle(release(ctrlSpace), Context{})
		pending := m.Handle(e, Context{}).Pending
		want(t, e.Name+" repeat", m.Handle(repeat(e), Context{}), Swallow, pending, Action{})
		want(t, e.Name+" release", m.Handle(release(e), Context{}), Swallow, pending, Action{})
	}
	// A direct binding: ctrl+h acts, its release is consumed.
	m = NewMachine(Defaults())
	want(t, "ctrl+h", m.Handle(Event{Name: "h", Mod: proto.ModCtrl}, Context{}), Act, false, Action{"focus-left", "vim"})
	want(t, "ctrl+h repeat", m.Handle(repeat(Event{Name: "h", Mod: proto.ModCtrl}), Context{}), Swallow, false, Action{})
	want(t, "h release", m.Handle(release(Event{Name: "h"}), Context{}), Swallow, false, Action{})
	// A terminal that reports no releases: a new press of the key is read
	// as a press, not as a stale repeat.
	m.Handle(ctrlSpace, Context{})
	m.Handle(key("o"), Context{})
	want(t, "o pressed again, no release seen", m.Handle(key("o"), Context{}), Forward, false, Action{})
}

func TestOtherReleasesAreForwarded(t *testing.T) {
	m := NewMachine(Defaults())
	want(t, "a", m.Handle(key("a"), Context{}), Forward, false, Action{})
	want(t, "a repeat", m.Handle(repeat(key("a")), Context{}), Forward, false, Action{})
	want(t, "a release", m.Handle(release(key("a")), Context{}), Forward, false, Action{})
	// A key held from before the prefix is released while it is pending:
	// the release goes to the pane, which had the press, and the prefix stays.
	m.Handle(key("b"), Context{})
	m.Handle(Event{Name: "leftctrl", Mod: proto.ModCtrl}, Context{})
	m.Handle(ctrlSpace, Context{})
	want(t, "b release while pending", m.Handle(release(key("b")), Context{}), Forward, true, Action{})
	want(t, "leftctrl release while pending", m.Handle(release(Event{Name: "leftctrl"}), Context{}), Forward, true, Action{})
	want(t, "o", m.Handle(key("o"), Context{}), Act, false, Action{"hub", ""})
	// A repeat of a key held from before the prefix is held back while the
	// prefix waits, and is no key after the prefix; its release is the pane's.
	m.Handle(key("c"), Context{})
	m.Handle(ctrlSpace, Context{})
	want(t, "c repeat while pending", m.Handle(repeat(key("c")), Context{}), Swallow, true, Action{})
	want(t, "c release while pending", m.Handle(release(key("c")), Context{}), Forward, true, Action{})
	want(t, "c pressed after the prefix", m.Handle(key("c"), Context{}), Act, false, Action{"tab-new", ""})
	want(t, "c release", m.Handle(release(key("c")), Context{}), Swallow, false, Action{})
	// A release the machine never saw pressed is the pane's.
	want(t, "unknown release", m.Handle(release(Event{Name: "f9"}), Context{}), Forward, false, Action{})
}

func TestTheVimRuleForwardsFocusKeysWhenThePaneSaysEditor(t *testing.T) {
	m := NewMachine(Defaults())
	editor := Context{Program: "git", Editor: true} // git commit with neovim as its editor
	shell := Context{Program: "nvim"}               // the name is for display only; Editor decides
	for _, c := range []struct {
		name string
		a    Action
	}{{"h", Action{"focus-left", "vim"}}, {"j", Action{"focus-down", "vim"}}, {"k", Action{"focus-up", "vim"}}, {"l", Action{"focus-right", "vim"}}} {
		e := Event{Name: c.name, Mod: proto.ModCtrl}
		want(t, "ctrl+"+c.name+" in an editor", m.Handle(e, editor), Forward, false, Action{})
		want(t, "its release in an editor", m.Handle(release(e), editor), Forward, false, Action{})
		want(t, "ctrl+"+c.name+" elsewhere", m.Handle(e, shell), Act, false, c.a)
		m.Handle(release(e), shell)
		// A single pane does not change it (F13): the rule reads Editor only.
		want(t, "ctrl+"+c.name+" in a plain shell", m.Handle(e, Context{}), Act, false, c.a)
		m.Handle(release(e), Context{})
	}
	// A focus binding without the vim argument acts in an editor too.
	m = NewMachine(mustNew(t, binds([3]string{"alt+h", "focus-left", ""}, [3]string{"prefix+h", "focus-left", "vim"}), nil))
	want(t, "alt+h in an editor", m.Handle(Event{Name: "h", Mod: proto.ModAlt}, editor), Act, false, Action{"focus-left", ""})
	// After the prefix the key was meant for devtools, not for the editor.
	m.Handle(ctrlSpace, editor)
	want(t, "prefix+h in an editor", m.Handle(key("h"), editor), Act, false, Action{"focus-left", "vim"})
	// Other direct bindings are not touched by the rule.
	m = NewMachine(mustNew(t, binds([3]string{"alt+o", "hub", ""}), nil))
	want(t, "alt+o in an editor", m.Handle(Event{Name: "o", Mod: proto.ModAlt}, editor), Act, false, Action{"hub", ""})
}

func TestPrintableChordMatchesByText(t *testing.T) {
	m := NewMachine(Defaults())
	// prefix+% is what types %: shift+5 on a US layout through the kitty
	// protocol, "%" from a legacy terminal, another key on another layout.
	for _, e := range []Event{
		{Name: "5", Mod: proto.ModShift, Text: "%"},
		{Name: "%", Text: "%"},
		{Name: "%", Mod: proto.ModShift, Text: "%"},
		{Name: "ù", Mod: proto.ModShift, Text: "%"},
	} {
		m.Handle(ctrlSpace, Context{})
		want(t, "prefix then "+e.Name, m.Handle(e, Context{}), Act, false, Action{"split-right", ""})
	}
	// Plain 5 is tab 5, not %.
	m.Handle(ctrlSpace, Context{})
	want(t, "prefix+5", m.Handle(key("5"), Context{}), Act, false, Action{"tab-select", "5"})
	// A character chord takes no modifier but shift: alt+% is not prefix+%.
	m.Handle(ctrlSpace, Context{})
	if r := m.Handle(Event{Name: "5", Mod: proto.ModShift | proto.ModAlt, Text: "%"}, Context{}); !r.Unbound {
		t.Errorf("prefix then alt+%% = %+v, want unbound", r)
	}
	// An upper-case chord is that letter with shift, and only that.
	m = NewMachine(mustNew(t, binds([3]string{"prefix+O", "hub", "agents"}), nil))
	for _, c := range []struct {
		e Event
		a Action
	}{
		{Event{Name: "o", Mod: proto.ModShift, Text: "O"}, Action{"hub", "agents"}},
		{Event{Name: "O", Text: "O"}, Action{"hub", "agents"}},
		{Event{Name: "O"}, Action{"hub", "agents"}},
		{Event{Name: "O", Mod: proto.ModShift}, Action{"hub", "agents"}},
		{Event{Name: "o", Mod: proto.ModShift}, Action{"hub", "agents"}}, // kitty without associated text
		{key("o"), Action{"hub", ""}},
		{Event{Name: "o"}, Action{"hub", ""}},
	} {
		m.Handle(ctrlSpace, Context{})
		want(t, "prefix then "+c.e.Name, m.Handle(c.e, Context{}), Act, false, c.a)
	}
}

func TestNamedKeyNeedsItsExactModifiers(t *testing.T) {
	tab := mustNew(t, []config.Bind{
		{Chord: "alt+enter", Action: "tab-new", Line: 1},
		{Chord: "prefix+ctrl+x", Action: "detach", Line: 2},
		{Chord: "f5", Action: "hub", Line: 3},
	}, nil)
	m := NewMachine(tab)
	for _, c := range []struct {
		e    Event
		want Verdict
	}{
		{Event{Name: "h", Mod: proto.ModCtrl}, Act},
		{Event{Name: "h", Mod: proto.ModCtrl | proto.ModCaps}, Act},
		{Event{Name: "h", Mod: proto.ModCtrl | proto.ModNum}, Act},
		{Event{Name: "h", Mod: proto.ModCtrl | proto.ModShift}, Forward},
		{Event{Name: "h", Mod: proto.ModCtrl | proto.ModAlt}, Forward},
		{Event{Name: "h", Mod: proto.ModCtrl | proto.ModSuper}, Forward},
		{Event{Name: "H", Mod: proto.ModCtrl}, Act}, // a terminal that reports the letter upper case
		{Event{Name: "h"}, Forward},
		{Event{Name: "enter", Mod: proto.ModAlt}, Act},
		{Event{Name: "enter"}, Forward},
		{Event{Name: "enter", Mod: proto.ModAlt | proto.ModShift}, Forward},
		{Event{Name: "enter", Mod: proto.ModAlt | proto.ModCtrl}, Forward},
		{Event{Name: "f5"}, Act},
		{Event{Name: "f5", Mod: proto.ModShift}, Forward},
		{Event{Name: "space"}, Forward},
		{Event{Name: "space", Mod: proto.ModCtrl | proto.ModShift}, Forward},
	} {
		r := m.Handle(c.e, Context{})
		if r.Verdict != c.want {
			t.Errorf("%+v = %+v, want verdict %d", c.e, r, c.want)
		}
		m.Handle(release(c.e), Context{})
	}
	m.Handle(ctrlSpace, Context{})
	want(t, "prefix+ctrl+x", m.Handle(Event{Name: "x", Mod: proto.ModCtrl}, Context{}), Act, false, Action{"detach", ""})
	m.Handle(ctrlSpace, Context{})
	want(t, "prefix+x is a different chord", m.Handle(key("x"), Context{}), Act, false, Action{"pane-close", ""})
	m.Handle(ctrlSpace, Context{})
	if r := m.Handle(Event{Name: "x", Mod: proto.ModCtrl | proto.ModAlt}, Context{}); !r.Unbound {
		t.Errorf("prefix+ctrl+alt+x = %+v, want unbound", r)
	}
}

// fuzzNames is the alphabet of FuzzHandle: fewer names than maxConsumed,
// so the model below is exact.
var fuzzNames = []string{"space", "o", "a", "f", "?", "/", "5", "escape", "esc", "h", "enter", "leftshift", "x", "A", " ", "%"}

func FuzzHandle(f *testing.F) {
	f.Add([]byte{0x40, 0x01, 0x00})
	f.Add([]byte{0x40, 0x40, 0x04, 0x84, 0x40, 0x07})
	f.Add([]byte{0x49, 0x89, 0xc9, 0x49, 0x40, 0x0b, 0x40, 0x06})
	f.Add([]byte{0x40, 0x0f, 0x40, 0x0e, 0x30, 0x12})
	tab, err := New(binds([3]string{"prefix+f", "hub", "new"}, [3]string{"alt+enter", "tab-new", ""}), nil)
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m := NewMachine(tab)
		consumed := map[string]bool{} // the model: presses that did not reach the pane
		for i, b := range data {
			// Bits 0-3: the name; bit 4: shift; bit 5: alt; bit 6: ctrl;
			// bit 7 with bit 4 of the next byte: release, repeat.
			e := Event{Name: fuzzNames[b&0x0f]}
			if b&0x10 != 0 {
				e.Mod |= proto.ModShift
			}
			if b&0x20 != 0 {
				e.Mod |= proto.ModAlt
			}
			if b&0x40 != 0 {
				e.Mod |= proto.ModCtrl
			}
			if b&0x80 != 0 {
				if i+1 < len(data) && data[i+1]&0x10 != 0 {
					e.Repeat = true
				} else {
					e.Release = true
				}
			}
			if e.Mod&^proto.ModShift == 0 && len(e.Name) == 1 && !e.Release {
				e.Text = e.Name
			}
			wasPending := m.pending
			c := Context{Editor: i%3 == 0}
			r := m.Handle(e, c)
			name := keyName(e)

			if r.Verdict < Forward || r.Verdict > SendPrefix {
				t.Fatalf("event %d %+v: verdict %d", i, e, r.Verdict)
			}
			if r.Pending != m.pending {
				t.Fatalf("event %d %+v: Pending %v, machine %v", i, e, r.Pending, m.pending)
			}
			if r.Unbound && (r.Verdict != Swallow || r.Pending || r.More) {
				t.Fatalf("event %d %+v: Unbound with %+v", i, e, r)
			}
			if r.More && (r.Verdict != Swallow || !r.Pending) {
				t.Fatalf("event %d %+v: More with %+v", i, e, r)
			}
			if (r.Verdict == Act) != (r.Action != Action{}) {
				t.Fatalf("event %d %+v: action %v with verdict %d", i, e, r.Action, r.Verdict)
			}
			if r.Verdict == Act {
				valid, ok := vocabulary[r.Action.Name]
				if !ok || !valid(r.Action.Arg) || r.Action.Name == "prefix" {
					t.Fatalf("event %d %+v: acts %v", i, e, r.Action)
				}
				if r.Pending {
					t.Fatalf("event %d %+v: acts and stays pending", i, e)
				}
			}
			if r.Verdict == SendPrefix && (!wasPending || r.Pending) {
				t.Fatalf("event %d %+v: SendPrefix from pending %v to %v", i, e, wasPending, r.Pending)
			}
			if !wasPending && r.Pending && !(r.Verdict == Swallow && !e.Release && !e.Repeat) {
				t.Fatalf("event %d %+v: the prefix started with %+v", i, e, r)
			}
			if e.Release {
				if want := consumed[name]; (r.Verdict == Swallow) != want || (r.Verdict != Swallow && r.Verdict != Forward) {
					t.Fatalf("event %d %+v: release %+v, its press consumed %v", i, e, r, want)
				}
				if wasPending != r.Pending {
					t.Fatalf("event %d %+v: a release changed the prefix", i, e)
				}
				delete(consumed, name)
				continue
			}
			if wasPending && r.Verdict == Forward {
				t.Fatalf("event %d %+v: a key reached the pane while the prefix was pending", i, e)
			}
			if e.Repeat && consumed[name] && r.Verdict != Swallow {
				t.Fatalf("event %d %+v: a repeat of a consumed press gave %+v", i, e, r)
			}
			if e.Repeat {
				if wasPending != r.Pending || (r.Verdict != Swallow && r.Verdict != Forward) {
					t.Fatalf("event %d %+v: a repeat gave %+v", i, e, r)
				}
			} else {
				consumed[name] = r.Verdict != Forward
			}
			if len(m.consumed) > maxConsumed {
				t.Fatalf("event %d: %d consumed presses", i, len(m.consumed))
			}
		}
	})
}
