package keys

import (
	"os"
	"slices"
	"strings"
	"testing"

	"devtools/internal/config"
)

// TestMain is hermetic by construction: internal/client/keys reads no
// environment, no clock and no file.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// bind is a bind line of a test config, numbered from 1 in the order given.
func binds(lines ...[3]string) []config.Bind {
	out := make([]config.Bind, len(lines))
	for i, l := range lines {
		out[i] = config.Bind{Chord: l[0], Action: l[1], Arg: l[2], Line: i + 1}
	}
	return out
}

func mustNew(t *testing.T, b []config.Bind, u []config.Unbind) *Table {
	t.Helper()
	tab, err := New(b, u)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tab
}

// startError asserts that New refuses the lines with an error that
// contains every one of want.
func startError(t *testing.T, b []config.Bind, u []config.Unbind, want ...string) {
	t.Helper()
	tab, err := New(b, u)
	if err == nil {
		t.Fatalf("New accepted the lines, table %v", tab.Bindings())
	}
	if tab != nil {
		t.Errorf("New returned a table with its error")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

func TestDefaultsAreTheTableOfThePlan(t *testing.T) {
	// 5a.11.2, row by row, in the order of the table.
	want := [][3]string{
		{"ctrl+space", "prefix", ""},
		{"prefix+o", "hub", ""},
		{"prefix+s", "hub", "sessions"},
		{"prefix+a", "hub", "agents"},
		{"prefix+c", "tab-new", ""},
		{"prefix+n", "tab-next", ""},
		{"prefix+p", "tab-prev", ""},
		{"prefix+1", "tab-select", "1"},
		{"prefix+2", "tab-select", "2"},
		{"prefix+3", "tab-select", "3"},
		{"prefix+4", "tab-select", "4"},
		{"prefix+5", "tab-select", "5"},
		{"prefix+6", "tab-select", "6"},
		{"prefix+7", "tab-select", "7"},
		{"prefix+8", "tab-select", "8"},
		{"prefix+9", "tab-select", "9"},
		{"prefix+%", "split-right", ""},
		{"prefix+v", "split-right", ""},
		{`prefix+"`, "split-down", ""},
		{"prefix+-", "split-down", ""},
		{"prefix+x", "pane-close", ""},
		{"prefix+[", "copy-mode", ""},
		{"prefix+(", "session-prev", ""},
		{"prefix+)", "session-next", ""},
		{"prefix+d", "detach", ""},
		{"prefix+q", "detach", ""},
		{"ctrl+h", "focus-left", "vim"},
		{"ctrl+j", "focus-down", "vim"},
		{"ctrl+k", "focus-up", "vim"},
		{"ctrl+l", "focus-right", "vim"},
		{"prefix+H", "resize-left", "5"},
		{"prefix+J", "resize-down", "5"},
		{"prefix+K", "resize-up", "5"},
		{"prefix+L", "resize-right", "5"},
		{"prefix+ctrl+left", "resize-left", ""},
		{"prefix+ctrl+down", "resize-down", ""},
		{"prefix+ctrl+up", "resize-up", ""},
		{"prefix+ctrl+right", "resize-right", ""},
	}
	for _, tab := range []*Table{Defaults(), mustNew(t, nil, nil)} {
		got := tab.Bindings()
		if !slices.Equal(got, want) {
			t.Fatalf("defaults:\n got %v\nwant %v", got, want)
		}
	}
	// prefix+a is hub agents; hub new has no default chord, prefix+f is unbound.
	tab := Defaults()
	if got := tab.Chords("hub", "agents"); !slices.Equal(got, []string{"prefix+a"}) {
		t.Errorf("Chords(hub, agents) = %v", got)
	}
	if got := tab.Chords("hub", "new"); got != nil {
		t.Errorf("Chords(hub, new) = %v, want none", got)
	}
	m := NewMachine(tab)
	m.Handle(ctrlSpace, Context{})
	if r := m.Handle(key("f"), Context{}); !r.Unbound || r.Verdict != Swallow {
		t.Errorf("prefix+f = %+v, want unbound", r)
	}
	// Defaults returns a table of its own each time.
	a, b := Defaults(), Defaults()
	a.rows[0].action.Name = "changed"
	if b.rows[0].action.Name != "prefix" || Defaults().rows[0].action.Name != "prefix" {
		t.Errorf("Defaults shares its rows")
	}
}

func TestChordGrammar(t *testing.T) {
	valid := map[string]string{ // as written: canonical form
		"ctrl+space":             "ctrl+space",
		"prefix+o":               "prefix+o",
		"prefix+%":               "prefix+%",
		`prefix+"`:               `prefix+"`,
		"prefix+-":               "prefix+-",
		"prefix++":               "prefix++",
		"ctrl++":                 "ctrl++",
		"+":                      "+",
		"prefix+?":               "prefix+?",
		"prefix+esc":             "prefix+escape",
		"prefix+escape":          "prefix+escape",
		"prefix+A":               "prefix+A",
		"prefix+shift+a":         "prefix+A",
		"ctrl+H":                 "ctrl+shift+h",
		"shift+ctrl+h":           "ctrl+shift+h",
		"meta+hyper+super+alt+x": "alt+super+hyper+meta+x",
		"alt+enter":              "alt+enter",
		"prefix+f12":             "prefix+f12",
		"prefix+ä":               "prefix+ä",
		"shift+5":                "shift+5",
		"pageup":                 "pageup",
		"prefix+space":           "prefix+space",
	}
	for in, want := range valid {
		c, err := parseChord(in)
		if err != nil {
			t.Errorf("parseChord(%q): %v", in, err)
			continue
		}
		if c.String() != want {
			t.Errorf("parseChord(%q) = %q, want %q", in, c, want)
		}
		again, err := parseChord(c.String())
		if err != nil || again != c {
			t.Errorf("the canonical form %q of %q does not parse to the same chord: %v %v", c, in, again, err)
		}
	}
	invalid := []string{
		"", "prefix", "prefix+", "ctrl+", "Ctrl+a", "control+a", "ctrl+ctrl+a",
		"ctrl+prefix+a", "prefix+prefix+a", "prefix+f13", "prefix+Enter",
		"prefix+ab", "prefix+\x01", "prefix+\x7f", "prefix+ ", "a++", "ctrl+++",
		"cmd+a", "prefix+ctrl", "ctrl+caps+a",
	}
	for _, in := range invalid {
		if c, err := parseChord(in); err == nil {
			t.Errorf("parseChord(%q) = %q, want an error", in, c)
		}
	}
}

func TestUnbindRemovesADefault(t *testing.T) {
	tab := mustNew(t, nil, []config.Unbind{{Chord: "prefix+x", Line: 1}, {Chord: "prefix+v", Line: 2}})
	if got := tab.Chords("pane-close", ""); got != nil {
		t.Errorf("pane-close still has %v", got)
	}
	if got := tab.Chords("split-right", ""); !slices.Equal(got, []string{"prefix+%"}) {
		t.Errorf("split-right = %v, want only prefix+%%", got)
	}
	m := NewMachine(tab)
	m.Handle(ctrlSpace, Context{})
	if r := m.Handle(key("x"), Context{}); !r.Unbound {
		t.Errorf("prefix+x after unbind = %+v, want unbound", r)
	}
	// An unbind of a chord without a default changes nothing, so that a
	// local file can make room for its own bind line.
	tab = mustNew(t, binds([3]string{"prefix+f", "hub", ""}), []config.Unbind{{Chord: "prefix+f", Line: 3}})
	if got := tab.Chords("hub", ""); !slices.Equal(got, []string{"prefix+o", "prefix+f"}) {
		t.Errorf("hub = %v", got)
	}
	// Unbinds go before binds: a bind of the unbound chord is kept.
	tab = mustNew(t, binds([3]string{"prefix+x", "detach", ""}), []config.Unbind{{Chord: "prefix+x", Line: 9}})
	if got := tab.Chords("detach", ""); !slices.Equal(got, []string{"prefix+d", "prefix+q", "prefix+x"}) {
		t.Errorf("detach = %v", got)
	}
	// An unbind line that does not parse is a start error.
	startError(t, nil, []config.Unbind{{Chord: "prefix+nope", Line: 4}}, "line 4", "unbind prefix+nope")
}

func TestALaterBindWins(t *testing.T) {
	tab := mustNew(t, binds(
		[3]string{"prefix+f", "hub", "new"},
		[3]string{"prefix+f", "hub", "sessions"},
		[3]string{"prefix+o", "detach", ""},
		[3]string{"prefix+shift+g", "tab-new", ""},
		[3]string{"prefix+G", "tab-next", ""},
	), nil)
	if got := tab.Chords("hub", "new"); got != nil {
		t.Errorf("hub new = %v, the later line replaced it", got)
	}
	if got := tab.Chords("hub", "sessions"); !slices.Equal(got, []string{"prefix+s", "prefix+f"}) {
		t.Errorf("hub sessions = %v", got)
	}
	if got := tab.Chords("hub", ""); got != nil {
		t.Errorf("hub = %v, prefix+o was rebound", got)
	}
	if got := tab.Chords("detach", ""); !slices.Equal(got, []string{"prefix+o", "prefix+d", "prefix+q"}) {
		t.Errorf("detach = %v: a replaced default keeps its place", got)
	}
	if got := tab.Chords("tab-new", ""); !slices.Equal(got, []string{"prefix+c"}) {
		t.Errorf("tab-new = %v: prefix+G and prefix+shift+g are one chord", got)
	}
	m := NewMachine(tab)
	for _, c := range []struct {
		e    Event
		want Action
	}{
		{key("f"), Action{"hub", "sessions"}},
		{key("o"), Action{"detach", ""}},
		{Event{Name: "g", Mod: 1, Text: "G"}, Action{"tab-next", ""}},
	} {
		m.Handle(ctrlSpace, Context{})
		if r := m.Handle(c.e, Context{}); r.Verdict != Act || r.Action != c.want {
			t.Errorf("prefix+%s = %+v, want %v", c.e.Name, r, c.want)
		}
	}
	// The prefix can be moved: unbind it, then bind another chord.
	tab = mustNew(t, binds([3]string{"ctrl+a", "prefix", ""}), []config.Unbind{{Chord: "ctrl+space", Line: 1}})
	m = NewMachine(tab)
	if r := m.Handle(Event{Name: "a", Mod: 4}, Context{}); !r.Pending {
		t.Errorf("ctrl+a = %+v, want the prefix", r)
	}
	if r := m.Handle(key("c"), Context{}); r.Action != (Action{"tab-new", ""}) {
		t.Errorf("ctrl+a c = %+v", r)
	}
	if r := m.Handle(ctrlSpace, Context{}); r.Verdict != Forward {
		t.Errorf("ctrl+space after the move = %+v, want forward", r)
	}
	// Or moved by binding the old chord to something else in the same file.
	mustNew(t, binds([3]string{"ctrl+a", "prefix", ""}, [3]string{"ctrl+space", "detach", ""}), nil)
}

func TestUnknownActionIsAStartErrorNamingTheChord(t *testing.T) {
	// The DROP rows of section 2 are not in the vocabulary (R10).
	for _, action := range []string{
		"tab-rename", "rename-tab", "tab-close", "close-tab", "last-pane", "pane-last",
		"resize", "pane-resize", "zoom", "pane-zoom", "workspace-rename", "settings",
		"sidebar", "goto", "help", "reload", "Hub", "hub-agents", "", "focus",
	} {
		startError(t, binds([3]string{"prefix+g", "tab-new", ""}, [3]string{"prefix+f", action, ""}),
			nil, "line 2", "prefix+f", "unknown action")
	}
	// A known action with an argument it does not take.
	for _, b := range [][3]string{
		{"prefix+f", "hub", "everything"},
		{"prefix+f", "tab-new", "2"},
		{"prefix+f", "resize-left", "0"},
		{"prefix+f", "resize-left", "100"},
		{"prefix+f", "resize-left", "05"},
		{"prefix+f", "tab-select", ""},
		{"prefix+f", "tab-select", "0"},
		{"prefix+f", "tab-select", "10"},
		{"prefix+f", "tab-select", "01"},
		{"prefix+f", "focus-left", "emacs"},
		{"prefix+f", "detach", "now"},
	} {
		startError(t, binds(b), nil, "line 1", "prefix+f", b[1])
	}
	// A chord that does not parse names itself too.
	startError(t, binds([3]string{"prefx+f", "hub", ""}), nil, "line 1", "prefx+f")
	// Every action of the vocabulary with a valid argument is accepted.
	for _, b := range [][3]string{
		{"prefix+f", "hub", ""}, {"prefix+f", "hub", "sessions"}, {"prefix+f", "hub", "agents"},
		{"prefix+f", "hub", "new"}, {"prefix+f", "tab-new", ""}, {"prefix+f", "tab-next", ""},
		{"prefix+f", "tab-prev", ""}, {"prefix+f", "tab-select", "9"}, {"prefix+f", "split-right", ""},
		{"prefix+f", "split-down", ""}, {"prefix+f", "pane-close", ""}, {"prefix+f", "copy-mode", ""},
		{"prefix+f", "session-prev", ""}, {"prefix+f", "session-next", ""}, {"prefix+f", "detach", ""},
		{"prefix+f", "focus-left", ""}, {"prefix+f", "focus-down", "vim"}, {"prefix+f", "focus-up", ""},
		{"prefix+f", "focus-right", "vim"},
	} {
		if _, err := New(binds(b), nil); err != nil {
			t.Errorf("bind %v: %v", b, err)
		}
	}
}

func TestQuestionMarkAfterThePrefixCannotBeBound(t *testing.T) {
	startError(t, binds([3]string{"prefix+?", "hub", ""}), nil, "line 1", "prefix+?")
	startError(t, binds([3]string{"prefix+shift+?", "hub", ""}), nil, "line 1", "prefix+shift+?")
	// esc after the prefix is built in the same way.
	startError(t, binds([3]string{"prefix+esc", "detach", ""}), nil, "line 1", "prefix+esc")
	startError(t, binds([3]string{"prefix+escape", "detach", ""}), nil, "line 1", "prefix+escape")
	// ? without the prefix is a plain character, refused for that reason.
	startError(t, binds([3]string{"?", "hub", ""}), nil, "line 1")
	// With a modifier esc is an ordinary key after the prefix.
	mustNew(t, binds([3]string{"prefix+alt+esc", "detach", ""}), nil)
}

func TestADirectBindingOnAPlainCharacterIsRefused(t *testing.T) {
	for _, c := range []string{"a", "A", "shift+a", "%", "shift+5", "ä", "1", "space", "shift+space", "-"} {
		startError(t, binds([3]string{c, "hub", ""}), nil, "line 1", "bind "+c, "plain character")
	}
	// A direct binding with ctrl, alt or the like, or on a named key, is allowed.
	for _, c := range []string{"ctrl+a", "alt+a", "alt+A", "super+1", "f5", "alt+space", "ctrl+shift+%"} {
		if _, err := New(binds([3]string{c, "hub", ""}), nil); err != nil {
			t.Errorf("bind %s hub: %v", c, err)
		}
	}
}

func TestThePrefixChordCannotBeBoundAfterThePrefix(t *testing.T) {
	startError(t, binds([3]string{"prefix+ctrl+space", "hub", ""}), nil, "line 1", "prefix+ctrl+space")
	// The rule follows a moved prefix.
	startError(t, binds([3]string{"prefix+ctrl+a", "hub", ""}, [3]string{"ctrl+a", "prefix", ""}),
		[]config.Unbind{{Chord: "ctrl+space", Line: 5}}, "line 1", "prefix+ctrl+a")
	// And it is no longer the old one.
	mustNew(t, binds([3]string{"prefix+ctrl+space", "hub", ""}, [3]string{"ctrl+a", "prefix", ""}),
		[]config.Unbind{{Chord: "ctrl+space", Line: 5}})
	// The action prefix after the prefix is refused as well.
	startError(t, binds([3]string{"prefix+g", "prefix", ""}), nil, "line 1", "prefix+g")
}

func TestTwoPrefixesAreRefused(t *testing.T) {
	startError(t, binds([3]string{"ctrl+a", "prefix", ""}), nil, "line 1", "ctrl+a", "ctrl+space")
	startError(t, binds([3]string{"ctrl+a", "prefix", ""}, [3]string{"alt+a", "prefix", ""}),
		[]config.Unbind{{Chord: "ctrl+space", Line: 1}}, "alt+a")
	// Without a prefix the bindings after it could never act.
	startError(t, nil, []config.Unbind{{Chord: "ctrl+space", Line: 1}}, "prefix+o", "no chord is bound to prefix")
	// Without any binding after the prefix there need not be one.
	var all []config.Unbind
	for _, row := range Defaults().Bindings() {
		all = append(all, config.Unbind{Chord: row[0], Line: len(all) + 1})
	}
	tab := mustNew(t, binds([3]string{"alt+o", "hub", ""}), all)
	if got := tab.Bindings(); !slices.Equal(got, [][3]string{{"alt+o", "hub", ""}}) {
		t.Errorf("table = %v", got)
	}
}

func TestChordsListsTheBindingsOfAnActionAndArgument(t *testing.T) {
	tab := Defaults()
	for _, c := range []struct {
		action, arg string
		want        []string
	}{
		{"prefix", "", []string{"ctrl+space"}},
		{"hub", "", []string{"prefix+o"}},
		{"hub", "sessions", []string{"prefix+s"}},
		{"hub", "agents", []string{"prefix+a"}},
		{"hub", "new", nil},
		{"split-right", "", []string{"prefix+%", "prefix+v"}},
		{"split-down", "", []string{`prefix+"`, "prefix+-"}},
		{"tab-select", "3", []string{"prefix+3"}},
		{"tab-select", "", []string{"prefix+1", "prefix+2", "prefix+3", "prefix+4", "prefix+5", "prefix+6", "prefix+7", "prefix+8", "prefix+9"}},
		{"session-prev", "", []string{"prefix+("}},
		{"session-next", "", []string{"prefix+)"}},
		{"detach", "", []string{"prefix+d", "prefix+q"}},
		{"focus-left", "vim", []string{"ctrl+h"}},
		{"focus-left", "", nil},
		{"pane-close", "x", nil},
		{"no-such-action", "", nil},
	} {
		if got := tab.Chords(c.action, c.arg); !slices.Equal(got, c.want) {
			t.Errorf("Chords(%q, %q) = %q, want %q", c.action, c.arg, got, c.want)
		}
	}
	// A bound hub new appears; written forms come back canonical.
	tab = mustNew(t, binds([3]string{"prefix+f", "hub", "new"}, [3]string{"prefix+shift+n", "hub", "new"}, [3]string{"alt+esc", "detach", ""}), nil)
	if got := tab.Chords("hub", "new"); !slices.Equal(got, []string{"prefix+f", "prefix+N"}) {
		t.Errorf("hub new = %v", got)
	}
	if got := tab.Chords("detach", ""); !slices.Equal(got, []string{"prefix+d", "prefix+q", "alt+escape"}) {
		t.Errorf("detach = %v", got)
	}
	// Every chord Chords returns parses again to a row of the table.
	for _, row := range tab.Bindings() {
		for _, c := range tab.Chords(row[1], row[2]) {
			p, err := parseChord(c)
			if err != nil || tab.find(p) < 0 {
				t.Errorf("chord %q of %v: %v", c, row, err)
			}
		}
	}
}
