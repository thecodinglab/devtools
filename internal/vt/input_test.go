//go:build cgo

package vt

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"devtools/internal/testkit/golden"
)

// The tests of the input side (P1-03, C12 to C14 of 10.1.5). Each builds a
// terminal with New and sets its modes by writing the sequences a program
// would send; none constructs a Modes value.

// keyMode is one of the five keyboard modes of the record of E10 item 9.
type keyMode struct{ name, seq string }

var keyModes = []keyMode{
	{"legacy", ""},
	{"kitty flag 1", "\x1b[>1u"},
	{"modifyOtherKeys 2", "\x1b[>4;2m"},
	{"application cursor", "\x1b[?1h"},
	{"application keypad", "\x1b="},
}

// recordMods are the modifier sets of the record, in wire bits: none,
// shift, alt, ctrl, ctrl+shift, ctrl+alt.
var recordMods = []int{0, 1, 2, 4, 5, 6}

// recordTextKeys are the text keys of the record beside the names of 4.2.6.
var recordTextKeys = []string{"a", "z", "1", "/", "ä"}

// wireKey is the input.key a client sends for a key of the record: the text
// a key produces (none with ctrl), the capital of a letter with shift.
func wireKey(name string, mod int) Key {
	k := Key{Name: name, Mod: mod}
	if r := []rune(name); len(r) == 1 {
		k.Text = name
		if mod&1 != 0 && r[0] >= 'a' && r[0] <= 'z' {
			k.Text = strings.ToUpper(name)
			k.Shifted = []rune(k.Text)[0]
		}
		if mod&4 != 0 {
			k.Text = ""
		}
	}
	if name == "space" && mod&4 == 0 {
		k.Text = " "
	}
	return k
}

const keyRecord = "testdata/keys/keys.txt"

// recordLine is one line of testdata/keys/keys.txt.
func recordLine(mode, name string, mod int, b []byte) string {
	return fmt.Sprintf("%s\t%s\t%d\t%s", mode, name, mod, strconv.Quote(string(b)))
}

// encodeRecord encodes every case of the record with the engine.
func encodeRecord(t *testing.T) []string {
	t.Helper()
	var lines []string
	for _, md := range keyModes {
		tm := termAt(t, 80, 24)
		tm.Write([]byte(md.seq))
		tm.Replies()
		for _, name := range append(append([]string{}, wireKeyNames...), recordTextKeys...) {
			for _, mod := range recordMods {
				if name == "ä" && mod&4 != 0 || name == "1" && mod&1 != 0 {
					continue // the shifted digit and ctrl with ä depend on the layout (E10 item 9)
				}
				lines = append(lines, recordLine(md.name, name, mod, tm.EncodeKey(wireKey(name, mod))))
			}
		}
	}
	return lines
}

// C12. Every name of 4.2.6 has an entry in keytable.go, and the bytes of
// every name in the five modes equal the record under testdata/keys, which
// started as the record of E10 item 9 (F3 with a modifier then took the
// xterm form, input.go).
func TestEveryKeyNameOfTheWireHasAnEntry(t *testing.T) {
	for _, name := range wireKeyNames {
		if _, ok := keyNames[name]; !ok {
			t.Errorf("key name %q of 4.2.6 has no entry in keytable.go", name)
		}
	}
	got := encodeRecord(t)
	if golden.Updating() {
		head := "# The bytes of every key name of 4.2.6 and five text keys in five modes, as\n" +
			"# EncodeKey gives them (P1-03; the record of E10 item 9, F3 with a modifier\n" +
			"# in the xterm form). Fields: mode, key, modifier bits of the wire, bytes.\n" +
			"# Written by go test ./internal/vt -update; every change is read.\n"
		if err := os.MkdirAll(filepath.Dir(keyRecord), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyRecord, []byte(head+strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := readKeyRecord(keyRecord)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("the record has %d cases, the engine gave %d", len(want), len(got))
	}
	for i := range got {
		if got[i] != want[i].line {
			t.Errorf("record line %d:\n got %s\nwant %s", want[i].n, got[i], want[i].line)
		}
	}
}

type recordEntry struct {
	n    int
	line string
}

func readKeyRecord(path string) ([]recordEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []recordEntry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if l := sc.Text(); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, recordEntry{n, l})
		}
	}
	return out, sc.Err()
}

// TestKeyConstantsAreTheHeaders compares the plain integers of keytable.go
// with the constants of the pinned header.
func TestKeyConstantsAreTheHeaders(t *testing.T) {
	for _, c := range keyConstants() {
		if c.go_ != c.lib {
			t.Errorf("GHOSTTY_KEY_%s: keytable.go %d, header %d", c.name, c.go_, c.lib)
		}
	}
}

func keysIn(t *testing.T, seq string) *term {
	t.Helper()
	tm := termAt(t, 80, 24)
	tm.Write([]byte(seq))
	tm.Replies()
	return tm
}

func wantKey(t *testing.T, tm *term, k Key, want string) {
	t.Helper()
	if got := tm.EncodeKey(k); string(got) != want {
		t.Errorf("EncodeKey(%+v) = %q, want %q", k, got, want)
	}
}

// Shift+Enter in the user's ghostty is ESC CR (systems ghostty.nix), which
// the client reads as alt+enter; a legacy pane must get exactly ESC CR.
func TestAltEnterIsEscCR(t *testing.T) {
	for _, seq := range []string{"", "\x1b[?1h", "\x1b="} {
		wantKey(t, keysIn(t, seq), Key{Name: "enter", Mod: 2}, "\x1b\r")
	}
	// With kitty flag 1 it is the protocol's form.
	wantKey(t, keysIn(t, "\x1b[>1u"), Key{Name: "enter", Mod: 2}, "\x1b[13;3u")
}

func TestCtrlSpaceIsNUL(t *testing.T) {
	tm := keysIn(t, "")
	wantKey(t, tm, Key{Name: "space", Mod: 4}, "\x00")
	wantKey(t, tm, Key{Name: " ", Mod: 4}, "\x00")
	wantKey(t, tm, Key{Name: "space", Mod: 6}, "\x1b\x00")
}

// Ctrl with a letter is its C0 byte in legacy mode and with modifyOtherKeys
// 0, alt adds ESC, shift changes nothing (xterm). Two letters are the
// exception the library keeps on purpose and devtools keeps with it
// (input.go, legacyForms): ctrl+i and ctrl+m are fixterms' CSI 105 ; 5 u
// and CSI 109 ; 5 u, because TAB and CR are the keys Tab and Enter, which
// have their own names on the wire. The user's ghostty sends the same.
func TestCtrlLettersAreC0(t *testing.T) {
	tm := keysIn(t, "")
	for r := 'a'; r <= 'z'; r++ {
		name := string(r)
		c0 := string(rune(r - 'a' + 1))
		switch r {
		case 'i', 'm':
			wantKey(t, tm, Key{Name: name, Mod: 4}, fmt.Sprintf("\x1b[%d;5u", r))
			continue
		}
		wantKey(t, tm, Key{Name: name, Mod: 4}, c0)
		wantKey(t, tm, Key{Name: name, Mod: 6}, "\x1b"+c0)
		wantKey(t, tm, Key{Name: name, Mod: 5, Shifted: r - 'a' + 'A'}, c0)
	}
	// A key of another layout is ctrl+ the letter of its US position.
	wantKey(t, tm, Key{Name: "с", Mod: 4, Base: 'c'}, "\x03")
}

func TestApplicationCursorKeysFollowDECCKM(t *testing.T) {
	tm := keysIn(t, "")
	arrows := map[string]string{"up": "A", "down": "B", "right": "C", "left": "D", "home": "H", "end": "F"}
	for name, l := range arrows {
		wantKey(t, tm, Key{Name: name}, "\x1b["+l)
	}
	tm.Write([]byte("\x1b[?1h"))
	for name, l := range arrows {
		wantKey(t, tm, Key{Name: name}, "\x1bO"+l)
		wantKey(t, tm, Key{Name: name, Mod: 4}, "\x1b[1;5"+l) // a modifier keeps the CSI form
	}
	tm.Write([]byte("\x1b[?1l"))
	wantKey(t, tm, Key{Name: "up"}, "\x1b[A")
}

func TestKittyFlagOneDisambiguates(t *testing.T) {
	tm := keysIn(t, "\x1b[>1u")
	wantKey(t, tm, Key{Name: "escape"}, "\x1b[27u")
	wantKey(t, tm, Key{Name: "a", Mod: 4}, "\x1b[97;5u")
	wantKey(t, tm, Key{Name: "a", Mod: 2}, "\x1b[97;3u")
	wantKey(t, tm, Key{Name: "i", Mod: 4}, "\x1b[105;5u")
	// Enter, Tab and Backspace keep their legacy bytes without a modifier,
	// and text keys stay text.
	wantKey(t, tm, Key{Name: "enter"}, "\r")
	wantKey(t, tm, Key{Name: "tab"}, "\t")
	wantKey(t, tm, Key{Name: "backspace"}, "\x7f")
	wantKey(t, tm, Key{Name: "a", Text: "a"}, "a")
	wantKey(t, tm, Key{Name: "enter", Mod: 1}, "\x1b[13;2u")
	// Popping the flags returns to legacy.
	tm.Write([]byte("\x1b[<u"))
	wantKey(t, tm, Key{Name: "escape"}, "\x1b")
}

func TestKittyReleaseAndRepeatOnlyWithEventTypes(t *testing.T) {
	for _, seq := range []string{"", "\x1b[>1u"} {
		tm := keysIn(t, seq)
		if got := tm.EncodeKey(Key{Name: "a", Text: "a", Release: true}); got != nil {
			t.Errorf("mode %q: a release = %q, want nil", seq, got)
		}
		wantKey(t, tm, Key{Name: "a", Text: "a", Repeat: true}, "a")
	}
	tm := keysIn(t, "\x1b[>3u")
	wantKey(t, tm, Key{Name: "a", Text: "a", Release: true}, "\x1b[97;1:3u")
	wantKey(t, tm, Key{Name: "a", Text: "a", Repeat: true}, "a")
	wantKey(t, tm, Key{Name: "a", Mod: 4, Repeat: true}, "\x1b[97;5:2u")
	wantKey(t, tm, Key{Name: "escape", Release: true}, "\x1b[27;1:3u")
	// Enter, Tab and Backspace report no release without "report all".
	if got := tm.EncodeKey(Key{Name: "enter", Release: true}); got != nil {
		t.Errorf("enter release with flags 3 = %q, want nil", got)
	}
}

// With kitty flag 4 the shifted key and the key of the US layout are
// reported beside the key: shift+a is 97:65, and a Cyrillic с on the
// position of c is 1089::99.
func TestKittyAlternateKeysUseShiftedAndBase(t *testing.T) {
	tm := keysIn(t, "\x1b[>5u")
	wantKey(t, tm, Key{Name: "a", Mod: 5, Text: "A", Shifted: 'A'}, "\x1b[97:65;6u")
	wantKey(t, tm, Key{Name: "с", Mod: 4, Base: 'c'}, "\x1b[1089::99;5u")
	wantKey(t, tm, Key{Name: "с", Mod: 5, Shifted: 'С', Base: 'c'}, "\x1b[1089:1057:99;6u")
	// Without flag 4 there are no alternates.
	tm = keysIn(t, "\x1b[>1u")
	wantKey(t, tm, Key{Name: "с", Mod: 4, Base: 'c'}, "\x1b[1089;5u")
}

// modifyOtherKeys is not a field of Modes; it is read from the bytes.
func TestModifyOtherKeysTwo(t *testing.T) {
	tm := keysIn(t, "")
	wantKey(t, tm, Key{Name: "a", Mod: 4}, "\x01")
	tm.Write([]byte("\x1b[>4;2m"))
	wantKey(t, tm, Key{Name: "a", Mod: 4}, "\x1b[27;5;97~")
	wantKey(t, tm, Key{Name: "a", Mod: 2}, "\x1b[27;3;97~")
	wantKey(t, tm, Key{Name: "a", Mod: 1, Text: "A", Shifted: 'A'}, "\x1b[27;2;65~")
	wantKey(t, tm, Key{Name: "enter", Mod: 2}, "\x1b[27;3;13~")
	wantKey(t, tm, Key{Name: "a", Text: "a"}, "a")
	tm.Write([]byte("\x1b[>4;0m"))
	wantKey(t, tm, Key{Name: "a", Mod: 4}, "\x01")
	tm.Write([]byte("\x1b[>4;2m\x1bc")) // a full reset ends it
	wantKey(t, tm, Key{Name: "a", Mod: 4}, "\x01")
}

func TestTextKeyWithoutModifiersIsItsText(t *testing.T) {
	for _, md := range keyModes {
		tm := keysIn(t, md.seq)
		for _, k := range []Key{
			{Name: "a", Text: "a"}, {Name: "/", Text: "/"}, {Name: "ä", Text: "ä"}, {Name: "界", Text: "界"},
			{Name: "a", Mod: 1, Text: "A", Shifted: 'A'}, {Name: "x"},
		} {
			want := k.Text
			if want == "" {
				want = k.Name
			}
			if md.name == "modifyOtherKeys 2" && k.Mod == 1 {
				want = "\x1b[27;2;65~" // the library's copy of xterm's ModifyOtherKeys
			}
			wantKey(t, tm, k, want)
		}
	}
}

func TestUnknownKeyNameEncodesNothing(t *testing.T) {
	tm := keysIn(t, "")
	for _, name := range []string{"", "bogus", "F1", "f13", "kp_enter", "ab", "\x01", "\x7f", "\u0085", "\xff", "a\x00"} {
		if got := tm.EncodeKey(Key{Name: name, Text: "x"}); got != nil {
			t.Errorf("EncodeKey(%q) = %q, want nil", name, got)
		}
	}
}

// F3 with a modifier is the one key name whose bytes come from the table
// of input.go (E10 item 9): CSI 1 ; m R as xterm documents it and as the
// pane's terminfo (xterm-256color, kf15=\E[1;2R) names it, instead of the
// library's CSI 13 ; m ~. With the kitty protocol the library's form is the
// protocol's and stays.
func TestF3WithAModifier(t *testing.T) {
	for _, seq := range []string{"", "\x1b[>4;2m", "\x1b[?1h", "\x1b="} {
		tm := keysIn(t, seq)
		wantKey(t, tm, Key{Name: "f3"}, "\x1bOR")
		for _, mod := range []int{1, 2, 3, 4, 5, 6, 7, 8} {
			p := 1 + mod&1 + mod&2 + mod&4 + mod&8
			wantKey(t, tm, Key{Name: "f3", Mod: mod}, fmt.Sprintf("\x1b[1;%dR", p))
		}
	}
	tm := keysIn(t, "\x1b[>1u")
	wantKey(t, tm, Key{Name: "f3", Mod: 1}, "\x1b[13;2~")
}

// The forms E10 recorded for P1-03 to decide, kept as the library sends
// them (input.go, legacyForms): fixterms for modified Enter, Escape and Tab
// in legacy mode, BS for ctrl+Backspace with modifyOtherKeys 2.
func TestFixtermsFormsAreKept(t *testing.T) {
	tm := keysIn(t, "")
	for _, c := range []struct {
		k    Key
		want string
	}{
		{Key{Name: "enter", Mod: 1}, "\x1b[27;2;13~"},
		{Key{Name: "enter", Mod: 4}, "\x1b[27;5;13~"},
		{Key{Name: "escape", Mod: 1}, "\x1b[27;2;27~"},
		{Key{Name: "escape", Mod: 4}, "\x1b[27;5;27~"},
		{Key{Name: "tab", Mod: 4}, "\x1b[27;5;9~"},
		{Key{Name: "tab", Mod: 1}, "\x1b[Z"},
		{Key{Name: "backspace", Mod: 4}, "\b"},
	} {
		wantKey(t, tm, c.k, c.want)
	}
	tm.Write([]byte("\x1b[>4;2m"))
	wantKey(t, tm, Key{Name: "backspace", Mod: 4}, "\b")
}

// ---------------------------------------------------------------- mouse (C13)

func mouseIn(t *testing.T, cols, rows int, seq string) *term {
	t.Helper()
	tm := termAt(t, cols, rows)
	tm.Write([]byte(seq))
	tm.Replies()
	return tm
}

func wantMouse(t *testing.T, tm *term, e Mouse, want string) {
	t.Helper()
	got := tm.EncodeMouse(e)
	if string(got) != want || (want == "" && got != nil) {
		t.Errorf("EncodeMouse(%+v) = %q, want %q", e, got, want)
	}
}

func TestMouseSGRAndLegacyEncodings(t *testing.T) {
	tm := mouseIn(t, 80, 24, "\x1b[?1000h")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 0, Y: 0}, "\x1b[M !!")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "left", X: 0, Y: 0}, "\x1b[M#!!")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "right", X: 9, Y: 4, Mod: 4}, "\x1b[M2*%")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "right", X: 9, Y: 4}, "\x1b[M#*%")
	wantMouse(t, tm, Mouse{Kind: "wheel", Button: "up", X: 2, Y: 3}, "\x1b[M`#$")
	wantMouse(t, tm, Mouse{Kind: "wheel", Button: "down", X: 2, Y: 3}, "\x1b[Ma#$")

	tm.Write([]byte("\x1b[?1006h"))
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 0, Y: 0}, "\x1b[<0;1;1M")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "left", X: 0, Y: 0}, "\x1b[<0;1;1m")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "middle", X: 5, Y: 6}, "\x1b[<1;6;7M")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "middle", X: 5, Y: 6}, "\x1b[<1;6;7m")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "right", X: 9, Y: 4, Mod: 4}, "\x1b[<18;10;5M")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "right", X: 9, Y: 4, Mod: 4}, "\x1b[<18;10;5m")
	wantMouse(t, tm, Mouse{Kind: "wheel", Button: "up", X: 2, Y: 3}, "\x1b[<64;3;4M")
	wantMouse(t, tm, Mouse{Kind: "wheel", Button: "down", X: 2, Y: 3, Mod: 4}, "\x1b[<81;3;4M")
	// Turning SGR off returns to the legacy form.
	tm.Write([]byte("\x1b[?1006l"))
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 0, Y: 0}, "\x1b[M !!")
}

// The legacy form carries a coordinate as one byte, 32 + 1 + cell: column
// 223 (cell 222) is the last it can carry; beyond it the event is dropped,
// while SGR reports it.
func TestMouseLegacyBeyond223IsDropped(t *testing.T) {
	tm := mouseIn(t, 300, 260, "\x1b[?1000h")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 222, Y: 0}, "\x1b[M \xff!")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 223, Y: 0}, "")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 299, Y: 0}, "")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 0, Y: 250}, "")
	tm.Write([]byte("\x1b[?1006h"))
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 299, Y: 250}, "\x1b[<0;300;251M")
}

// C13: each tracking mode reports only the events it asks for.
func TestMouseEventsFollowTheTrackingMode(t *testing.T) {
	events := []struct {
		name string
		e    Mouse
	}{
		{"motion", Mouse{Kind: "motion", Button: "none", X: 1, Y: 1}},
		{"press", Mouse{Kind: "press", Button: "left", X: 2, Y: 1}},
		{"drag", Mouse{Kind: "motion", Button: "left", X: 3, Y: 1}},
		{"release", Mouse{Kind: "release", Button: "left", X: 3, Y: 1}},
		{"motion after", Mouse{Kind: "motion", Button: "none", X: 4, Y: 1}},
		{"wheel", Mouse{Kind: "wheel", Button: "up", X: 4, Y: 1}},
	}
	for _, c := range []struct {
		mode string
		seq  string
		want map[string]bool
	}{
		{"off", "", map[string]bool{}},
		{"X10 (9)", "\x1b[?9h", map[string]bool{"press": true}},
		{"normal (1000)", "\x1b[?1000h", map[string]bool{"press": true, "release": true, "wheel": true}},
		{"button-event (1002)", "\x1b[?1002h", map[string]bool{"press": true, "drag": true, "release": true, "wheel": true}},
		{"any-event (1003)", "\x1b[?1003h", map[string]bool{"motion": true, "press": true, "drag": true, "release": true, "motion after": true, "wheel": true}},
		{"1000 set and reset", "\x1b[?1000h\x1b[?1000l", map[string]bool{}},
	} {
		for _, sgr := range []string{"", "\x1b[?1006h"} {
			tm := mouseIn(t, 80, 24, c.seq+sgr)
			for _, ev := range events {
				got := tm.EncodeMouse(ev.e)
				if (got != nil) != c.want[ev.name] {
					t.Errorf("%s sgr=%v: %s gave %q, reported %v, want %v", c.mode, sgr != "", ev.name, got, got != nil, c.want[ev.name])
				}
			}
		}
	}
}

func TestMouseReportsTheCellItWasGiven(t *testing.T) {
	tm := mouseIn(t, 200, 50, "\x1b[?1000h\x1b[?1006h")
	for _, c := range []struct{ x, y int }{{0, 0}, {199, 49}, {100, 25}} {
		wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: c.x, Y: c.y}, fmt.Sprintf("\x1b[<0;%d;%dM", c.x+1, c.y+1))
		wantMouse(t, tm, Mouse{Kind: "release", Button: "left", X: c.x, Y: c.y}, fmt.Sprintf("\x1b[<0;%d;%dm", c.x+1, c.y+1))
	}
	// A cell outside the terminal is never reported.
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 200, Y: 0}, "")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 0, Y: -1}, "")
	// After a resize the cells of the new size are reported.
	tm.Resize(80, 24)
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 79, Y: 23}, "\x1b[<0;80;24M")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 100, Y: 0}, "")
}

func TestMouseMotionInTheSameCellIsReportedOnce(t *testing.T) {
	tm := mouseIn(t, 80, 24, "\x1b[?1003h\x1b[?1006h")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 5, Y: 5}, "\x1b[<35;6;6M")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 5, Y: 5}, "")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 6, Y: 5}, "\x1b[<35;7;6M")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 6, Y: 5}, "")
	// A wheel event in between does not make the cell new again.
	wantMouse(t, tm, Mouse{Kind: "wheel", Button: "up", X: 6, Y: 5}, "\x1b[<64;7;6M")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 6, Y: 5}, "")
	// A press is always reported, also in the same cell.
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 6, Y: 5}, "\x1b[<0;7;6M")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 6, Y: 5}, "\x1b[<0;7;6M")
}

func TestMouseMotionWithAButtonHeldIsReportedInButtonEventMode(t *testing.T) {
	tm := mouseIn(t, 80, 24, "\x1b[?1002h\x1b[?1006h")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 1, Y: 1}, "")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 1, Y: 1}, "\x1b[<0;2;2M")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "left", X: 2, Y: 1}, "\x1b[<32;3;2M")
	// The wire may name no button for a drag; the held one is used.
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 3, Y: 1}, "\x1b[<32;4;2M")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "left", X: 3, Y: 1}, "\x1b[<0;4;2m")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 4, Y: 1}, "")
	// A wheel event does not count as a held button.
	wantMouse(t, tm, Mouse{Kind: "wheel", Button: "down", X: 4, Y: 1}, "\x1b[<65;5;2M")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 5, Y: 1}, "")
	// Two buttons: the motion is reported until the last is released.
	wantMouse(t, tm, Mouse{Kind: "press", Button: "left", X: 5, Y: 1}, "\x1b[<0;6;2M")
	wantMouse(t, tm, Mouse{Kind: "press", Button: "right", X: 5, Y: 1}, "\x1b[<2;6;2M")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "left", X: 5, Y: 1}, "\x1b[<0;6;2m")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "right", X: 6, Y: 1}, "\x1b[<34;7;2M")
	wantMouse(t, tm, Mouse{Kind: "release", Button: "right", X: 6, Y: 1}, "\x1b[<2;7;2m")
	wantMouse(t, tm, Mouse{Kind: "motion", Button: "none", X: 7, Y: 1}, "")
}

func TestUnknownMouseEventsEncodeNothing(t *testing.T) {
	tm := mouseIn(t, 80, 24, "\x1b[?1003h\x1b[?1006h")
	for _, e := range []Mouse{
		{Kind: "click", Button: "left"}, {Kind: "press", Button: "fourth"}, {Kind: "press", Button: "none"},
		{Kind: "wheel", Button: "left"}, {Kind: "press", Button: "up"}, {Kind: ""},
	} {
		wantMouse(t, tm, e, "")
	}
}

// ---------------------------------------------------------------- paste (C14)

func TestPasteIsBracketedOnlyWithMode2004(t *testing.T) {
	tm := keysIn(t, "")
	if got := string(tm.EncodePaste("one\ntwo", true, true)); got != "one\rtwo" {
		t.Errorf("without 2004: %q, want %q", got, "one\rtwo")
	}
	tm.Write([]byte("\x1b[?2004h"))
	if got := string(tm.EncodePaste("one\ntwo", true, true)); got != "\x1b[200~one\ntwo\x1b[201~" {
		t.Errorf("with 2004: %q", got)
	}
	tm.Write([]byte("\x1b[?2004l"))
	if got := string(tm.EncodePaste("x", true, true)); got != "x" {
		t.Errorf("after 2004 was reset: %q", got)
	}
	// Nothing to send: an empty paste without brackets.
	if got := tm.EncodePaste("", true, true); got != nil {
		t.Errorf("empty paste without 2004 = %q, want nil", got)
	}
}

// The library's cleaning (3.8): ESC, NUL and DEL become spaces, so a pasted
// text cannot close the bracket and run the rest as typed input.
func TestPasteCannotCloseItsOwnBracket(t *testing.T) {
	tm := keysIn(t, "\x1b[?2004h")
	got := string(tm.EncodePaste("echo one\x1b[201~ rm -rf /\x00\x7f\n", true, true))
	want := "\x1b[200~echo one [201~ rm -rf /  \n\x1b[201~"
	if got != want {
		t.Errorf("paste = %q, want %q", got, want)
	}
	if strings.Count(got, "\x1b") != 2 {
		t.Errorf("paste %q holds an ESC of its own", got)
	}
	// Also without 2004 no ESC of the text reaches the program.
	tm.Write([]byte("\x1b[?2004l"))
	if got := tm.EncodePaste("a\x1b[Ab", true, true); bytes.IndexByte(got, 0x1b) >= 0 {
		t.Errorf("unbracketed paste %q holds an ESC", got)
	}
}

// A paste sent in chunks (4.2.6: more:true on every chunk but the last)
// has one opening and one closing bracket, also when the text's own closing
// bracket is split between two chunks.
func TestPasteChunksShareOneBracket(t *testing.T) {
	tm := keysIn(t, "\x1b[?2004h")
	var all []byte
	chunks := []string{"echo one\ntwo  \x1b[20", "1~ rm -rf /\n", "three"}
	for i, c := range chunks {
		all = append(all, tm.EncodePaste(c, i == 0, i == len(chunks)-1)...)
	}
	want := "\x1b[200~echo one\ntwo   [201~ rm -rf /\nthree\x1b[201~"
	if string(all) != want {
		t.Errorf("chunks = %q, want %q", all, want)
	}
	// An empty last chunk still closes the bracket, an empty middle one
	// sends nothing.
	if got := string(tm.EncodePaste("", false, true)); got != "\x1b[201~" {
		t.Errorf("empty last chunk = %q", got)
	}
	if got := tm.EncodePaste("", false, false); got != nil {
		t.Errorf("empty middle chunk = %q, want nil", got)
	}
	if got := string(tm.EncodePaste("x", false, false)); got != "x" {
		t.Errorf("a middle chunk = %q, want %q", got, "x")
	}
}

func TestPasteDoesNotChangeTheCallersBytes(t *testing.T) {
	tm := keysIn(t, "\x1b[?2004h")
	src := []byte("a\x1bb\x00c\nd")
	keep := append([]byte(nil), src...)
	s := string(src)
	out := tm.EncodePaste(s, true, true)
	if !bytes.Equal(src, keep) || s != string(keep) {
		t.Errorf("the caller's bytes changed: %q", src)
	}
	// The result is the caller's to keep: a second paste leaves it alone.
	first := string(out)
	tm.EncodePaste("zzzzzzzzzzzz", true, true)
	if string(out) != first {
		t.Errorf("a later paste changed an earlier result: %q", out)
	}
}

// ---------------------------------------------------------------- focus

func TestFocusOnlyWithMode1004(t *testing.T) {
	tm := keysIn(t, "")
	if got := tm.EncodeFocus(true); got != nil {
		t.Errorf("focus without 1004 = %q, want nil", got)
	}
	tm.Write([]byte("\x1b[?1004h"))
	if got := string(tm.EncodeFocus(true)); got != "\x1b[I" {
		t.Errorf("focus gained = %q", got)
	}
	if got := string(tm.EncodeFocus(false)); got != "\x1b[O" {
		t.Errorf("focus lost = %q", got)
	}
	tm.Write([]byte("\x1b[?1004l"))
	if got := tm.EncodeFocus(false); got != nil {
		t.Errorf("focus after 1004 was reset = %q, want nil", got)
	}
}

// After Close every encoder returns nil instead of reaching freed memory.
func TestEncodersReturnNilAfterClose(t *testing.T) {
	tm := keysIn(t, "\x1b[?1000h\x1b[?2004h\x1b[?1004h")
	tm.Close()
	if tm.EncodeKey(Key{Name: "a", Text: "a"}) != nil || tm.EncodeMouse(Mouse{Kind: "press", Button: "left"}) != nil ||
		tm.EncodePaste("x", true, true) != nil || tm.EncodeFocus(true) != nil {
		t.Fatal("an encoder returned bytes after Close")
	}
}

// ---------------------------------------------------------------- fuzz

// FuzzEncodeKey feeds random mode sequences and random keys: no crash, no
// fault of the library, and an unknown name never gives bytes.
func FuzzEncodeKey(f *testing.F) {
	for _, md := range keyModes {
		f.Add([]byte(md.seq), "a", 4, "", int32(0), int32(0), false, false)
	}
	f.Add([]byte("\x1b[>31u"), "enter", 1, "", int32(0), int32(0), true, false)
	f.Add([]byte("\x1b[>5u\x1b[?1h"), "с", 5, "С", int32('С'), int32('c'), false, true)
	f.Add([]byte("\x1b[>4;2m"), "f3", 255, "\x00", int32(-1), int32(0x10ffff+1), false, false)
	f.Fuzz(func(t *testing.T, modes []byte, name string, mod int, text string, shifted, base int32, repeat, release bool) {
		tm := termAt(t, 80, 24)
		if len(modes) > 4096 {
			modes = modes[:4096]
		}
		tm.Write(modes)
		tm.Replies()
		k := Key{Name: name, Mod: mod, Text: text, Shifted: rune(shifted), Base: rune(base), Repeat: repeat, Release: release}
		got := tm.EncodeKey(k)
		if got != nil && len(got) == 0 {
			t.Fatalf("EncodeKey(%+v) returned an empty, non-nil slice", k)
		}
		if _, ok := keyCode(name); !ok && got != nil {
			t.Fatalf("EncodeKey(%+v) = %q for an unknown name", k, got)
		}
		// The terminal is still sound afterwards.
		if cols, rows := tm.Size(); cols != 80 || rows != 24 {
			t.Fatalf("size %dx%d after EncodeKey", cols, rows)
		}
	})
}
