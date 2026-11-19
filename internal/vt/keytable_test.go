package vt

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The tests of keytable.go and of the key record under testdata/keys. They
// use no engine, so this file has no build constraint.

func TestEveryWireKeyNameHasAConstant(t *testing.T) {
	seen := map[int]string{}
	for _, name := range wireKeyNames {
		c, ok := keyCode(name)
		if !ok || c == keyUnidentified {
			t.Errorf("keyCode(%q) = %d, %v", name, c, ok)
		}
		if other, dup := seen[c]; dup {
			t.Errorf("%q and %q share the constant %d", name, other, c)
		}
		seen[c] = name
	}
	if len(keyNames) != len(wireKeyNames) {
		t.Errorf("keyNames has %d entries, 4.2.6 names %d", len(keyNames), len(wireKeyNames))
	}
}

func TestCharactersMapToTheirUSKey(t *testing.T) {
	for _, c := range []struct {
		name string
		code int
	}{
		{"a", keyA}, {"z", keyA + 25}, {"A", keyA}, {"0", keyDigit0}, {"9", keyDigit0 + 9},
		{"/", keySlash}, {"[", keyBracketLeft}, {"`", keyBackquote}, {" ", keySpace},
		{"ä", keyUnidentified}, {"界", keyUnidentified}, {"!", keyUnidentified}, {"😀", keyUnidentified},
	} {
		got, ok := keyCode(c.name)
		if !ok || got != c.code {
			t.Errorf("keyCode(%q) = %d, %v; want %d", c.name, got, ok, c.code)
		}
	}
}

func TestKeyCodeRefusesWhatIsNotAKey(t *testing.T) {
	for _, name := range []string{"", "ab", "bogus", "Enter", "f13", "\x00", "\x1b", "\x7f", "\u0090", "\xff", "a\x00", "\xe2\x82"} {
		if c, ok := keyCode(name); ok {
			t.Errorf("keyCode(%q) = %d, true; want not ok", name, c)
		}
	}
}

// TestTheKeyRecordFollowsXtermAndKitty reads testdata/keys/keys.txt and
// judges every case that xterm's ctlseqs or the kitty keyboard protocol
// fixes, with the rules of E10 item 9. F3 with a modifier is now judged as
// xterm documents it, CSI 1 ; m R, outside the kitty protocol (input.go).
// Cases neither document fixes are recorded only; E10 recorded 94 of them,
// and the record keeps their bytes (P1-03 compared them).
func TestTheKeyRecordFollowsXtermAndKitty(t *testing.T) {
	f, err := os.Open("testdata/keys/keys.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	cases, judged := 0, 0
	modes := map[string]bool{}
	for n := 1; sc.Scan(); n++ {
		l := sc.Text()
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		fs := strings.Split(l, "\t")
		if len(fs) != 4 {
			t.Fatalf("line %d: %d fields", n, len(fs))
		}
		mod, err1 := strconv.Atoi(fs[2])
		got, err2 := strconv.Unquote(fs[3])
		if err1 != nil || err2 != nil {
			t.Fatalf("line %d: %v %v", n, err1, err2)
		}
		cases++
		modes[fs[0]] = true
		want, ok := documented(fs[0], fs[1], mod)
		if !ok {
			continue
		}
		judged++
		if got != want {
			t.Errorf("line %d, %s %s mod %d: record %q, documented %q", n, fs[0], fs[1], mod, got, want)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// 27 names and 5 text keys, 6 modifier sets, 5 modes, less the layout
	// cases (E10 item 9: 935 cases, 841 judged).
	if cases != 935 || judged != 841 || len(modes) != 5 {
		t.Errorf("record: %d cases, %d judged, %d modes; want 935, 841, 5", cases, judged, len(modes))
	}
}

// xmod is the modifier parameter of xterm and kitty for the wire bits
// shift 1, alt 2, ctrl 4.
func xmod(m int) int { return 1 + m&1 + m&2 + m&4 }

var (
	docCursor  = map[string]string{"up": "A", "down": "B", "right": "C", "left": "D", "home": "H", "end": "F"}
	docTilde   = map[string]int{"insert": 2, "delete": 3, "pageup": 5, "pagedown": 6, "f5": 15, "f6": 17, "f7": 18, "f8": 19, "f9": 20, "f10": 21, "f11": 23, "f12": 24}
	docSS3     = map[string]string{"f1": "P", "f2": "Q", "f3": "R", "f4": "S"}
	docSpecial = map[string]int{"enter": 13, "tab": 9, "backspace": 127, "escape": 27, "space": 32}
	docPlain   = map[string]string{"enter": "\r", "tab": "\t", "backspace": "\x7f", "escape": "\x1b", "space": " "}
)

// documented returns the bytes xterm's ctlseqs ("PC-Style Function Keys",
// "Alt and Meta Keys" with metaSendsEscape, modifyOtherKeys) or, for kitty
// flag 1, the kitty protocol ("Disambiguate escape codes") fixes for a
// case, and false when neither does. A port of the judge of E10 item 9.
func documented(mode, key string, m int) (string, bool) {
	p := xmod(m)
	kitty := mode == "kitty flag 1"
	if l, ok := docCursor[key]; ok {
		switch {
		case m != 0:
			return fmt.Sprintf("\x1b[1;%d%s", p, l), true
		case mode == "application cursor":
			return "\x1bO" + l, true
		}
		return "\x1b[" + l, true
	}
	if n, ok := docTilde[key]; ok {
		if m == 0 {
			return fmt.Sprintf("\x1b[%d~", n), true
		}
		return fmt.Sprintf("\x1b[%d;%d~", n, p), true
	}
	if l, ok := docSS3[key]; ok {
		switch {
		case m == 0 && kitty:
			return "", false // kitty's table, ported from foot: CSI P, CSI Q, CSI 13 ~, CSI S
		case m == 0:
			return "\x1bO" + l, true
		case kitty && key == "f3":
			return fmt.Sprintf("\x1b[13;%d~", p), true // kitty dropped CSI R for F3
		}
		return fmt.Sprintf("\x1b[1;%d%s", p, l), true
	}
	if c, ok := docSpecial[key]; ok {
		plain := docPlain[key]
		switch mode {
		case "kitty flag 1":
			switch {
			case key == "escape" && m == 0:
				return "\x1b[27u", true
			case key == "escape":
				return fmt.Sprintf("\x1b[27;%du", p), true
			case m == 0:
				return plain, true
			case key == "space" && m == 1:
				return "", false
			}
			return fmt.Sprintf("\x1b[%d;%du", c, p), true
		case "modifyOtherKeys 2":
			switch {
			case m == 0:
				return plain, true
			case key == "backspace" && m == 4:
				return "", false
			case m&6 != 0:
				return fmt.Sprintf("\x1b[27;%d;%d~", p, c), true
			}
			return "", false
		}
		switch {
		case m == 0:
			return plain, true
		case m == 2:
			return "\x1b" + plain, true
		case key == "tab" && m == 1:
			return "\x1b[Z", true
		case key == "space" && m == 4:
			return "\x00", true
		}
		return "", false
	}
	// The text keys a, z, 1, / and ä.
	r := []rune(key)[0]
	ctrl := ""
	switch {
	case r >= 'a' && r <= 'z':
		ctrl = string(r - 'a' + 1)
	case r == '/':
		ctrl = "\x1f"
	}
	switch mode {
	case "kitty flag 1":
		switch {
		case m == 0:
			return key, true
		case m == 1 && r >= 'a' && r <= 'z':
			return strings.ToUpper(key), true
		case m == 1 || r > 127:
			return "", false
		}
		return fmt.Sprintf("\x1b[%d;%du", r, p), true
	case "modifyOtherKeys 2":
		switch {
		case m == 0:
			return key, true
		case m == 1 || r > 127:
			return "", false
		}
		return fmt.Sprintf("\x1b[27;%d;%d~", p, r), true
	}
	switch {
	case m == 0:
		return key, true
	case m == 1 && r >= 'a' && r <= 'z':
		return strings.ToUpper(key), true
	case m == 2:
		return "\x1b" + key, true
	case m == 4 && ctrl != "":
		return ctrl, true
	case m == 6 && ctrl != "":
		return "\x1b" + ctrl, true
	}
	return "", false
}
