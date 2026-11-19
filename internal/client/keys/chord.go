package keys

// Chords: the grammar of the first field of a `bind` and an `unbind` line
// (4.6, 5a.11), and how a chord matches a key event.
//
// Grammar:
//
//	chord     = [ "prefix+" ] { modifier "+" } key
//	modifier  = "ctrl" | "alt" | "shift" | "super" | "hyper" | "meta"
//	key       = name | character
//	name      = a key name of 4.2.6 (enter tab backspace escape space up down
//	            left right home end pageup pagedown insert delete f1..f12),
//	            or "esc" for escape
//	character = one Unicode scalar that is not a control character
//
// Modifiers and names are lower case and each modifier is written at most
// once. "prefix+" names a key pressed after the prefix; a chord without it
// is a direct binding. The key "+" is written as the last character, as in
// "prefix++" or "ctrl++".
//
// A character with no modifier but shift is printable: it matches by the
// text the key produces, so "prefix+%" matches shift+5 on a US layout and
// on any other layout that types "%" (TestPrintableChordMatchesByText).
// "shift+a" is the same chord as "A". Every other chord matches by key
// name with exactly its modifiers; caps lock and num lock are ignored
// (TestNamedKeyNeedsItsExactModifiers). An upper-case letter with a
// modifier other than shift means that letter with shift: "ctrl+H" is
// "ctrl+shift+h".

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"devtools/internal/proto"
)

// lockMods are the modifier bits a chord never names and a match ignores.
const lockMods = proto.ModCaps | proto.ModNum

// modNames are the modifiers of the grammar in the order a chord is
// written back.
var modNames = []struct {
	name string
	bit  int
}{
	{"ctrl", proto.ModCtrl},
	{"alt", proto.ModAlt},
	{"shift", proto.ModShift},
	{"super", proto.ModSuper},
	{"hyper", proto.ModHyper},
	{"meta", proto.ModMeta},
}

// chord is one parsed chord.
type chord struct {
	prefix bool   // pressed after the prefix
	mod    int    // modifier bits, never a lock bit; 0 when text is set
	key    string // a key name of 4.2.6, or one character
	text   bool   // a printable character matched by its text
}

// String writes the chord back in the canonical form of the grammar:
// modifiers in the order of modNames, "escape" as written in 4.2.6, a
// shifted letter as the upper-case letter.
func (c chord) String() string {
	var b strings.Builder
	if c.prefix {
		b.WriteString("prefix+")
	}
	for _, m := range modNames {
		if c.mod&m.bit != 0 {
			b.WriteString(m.name)
			b.WriteByte('+')
		}
	}
	b.WriteString(c.key)
	return b.String()
}

// same reports whether two chords name the same key with the same
// modifiers, after the prefix or not.
func (c chord) same(o chord) bool {
	return c.key == o.key && c.mod == o.mod && c.text == o.text
}

// parseChord reads one chord of the grammar above.
func parseChord(s string) (chord, error) {
	var c chord
	if s == "" {
		return c, fmt.Errorf("empty chord")
	}
	rest := s
	if strings.HasPrefix(rest, "prefix+") {
		c.prefix = true
		rest = rest[len("prefix+"):]
	}
	var parts []string
	switch {
	case rest == "":
		return c, fmt.Errorf("chord %q names no key", s)
	case strings.HasSuffix(rest, "++") || rest == "+":
		// the key is "+" itself
		head := strings.TrimSuffix(rest, "+")
		head = strings.TrimSuffix(head, "+")
		if head != "" {
			parts = strings.Split(head, "+")
		}
		parts = append(parts, "+")
	default:
		parts = strings.Split(rest, "+")
	}
	key := parts[len(parts)-1]
	for _, m := range parts[:len(parts)-1] {
		bit := modBit(m)
		switch {
		case m == "prefix":
			return c, fmt.Errorf("chord %q: prefix must come first and only once", s)
		case bit == 0:
			return c, fmt.Errorf("chord %q: unknown modifier %q (ctrl, alt, shift, super, hyper, meta)", s, m)
		case c.mod&bit != 0:
			return c, fmt.Errorf("chord %q: modifier %q twice", s, m)
		}
		c.mod |= bit
	}
	if key == "" {
		return c, fmt.Errorf("chord %q names no key", s)
	}
	if key == "esc" {
		key = "escape"
	}
	if key == "prefix" {
		return c, fmt.Errorf("chord %q names no key", s)
	}
	if utf8.RuneCountInString(key) > 1 {
		if !isName(key) {
			return c, fmt.Errorf("chord %q: unknown key %q", s, key)
		}
		c.key = key
		return c, nil
	}
	r, _ := utf8.DecodeRuneInString(key)
	if r == utf8.RuneError || !proto.ValidKey(key) || r == ' ' {
		return c, fmt.Errorf("chord %q: unknown key %q", s, key)
	}
	if unicode.IsUpper(r) && r < utf8.RuneSelf {
		// an upper-case ASCII letter is that letter with shift
		c.mod |= proto.ModShift
		key = string(unicode.ToLower(r))
	}
	if c.mod&^proto.ModShift == 0 {
		// printable: matched by the text it types
		c.text = true
		if c.mod&proto.ModShift != 0 {
			up, ok := shiftedLetter(key)
			if !ok {
				// shift with a character that is not a letter: what it
				// types depends on the layout, so it matches by name
				c.text = false
				c.key = key
				return c, nil
			}
			key = up
		}
		c.mod = 0
		c.key = key
		return c, nil
	}
	c.key = key
	return c, nil
}

// modBit is the bit of a modifier name; 0 for anything else.
func modBit(name string) int {
	for _, m := range modNames {
		if m.name == name {
			return m.bit
		}
	}
	return 0
}

// isName reports whether s is one of the fixed key names of 4.2.6.
func isName(s string) bool {
	return utf8.RuneCountInString(s) > 1 && proto.ValidKey(s)
}

// shiftedLetter is the upper-case form of a lower-case ASCII letter.
func shiftedLetter(k string) (string, bool) {
	if len(k) == 1 && k[0] >= 'a' && k[0] <= 'z' {
		return string(k[0] - 'a' + 'A'), true
	}
	return "", false
}

// modifierKeys are the names under which a terminal reports a modifier key
// pressed on its own (the kitty protocol with all keys as escape codes, as
// bubbletea's key names write them, and the plain names). Such a press
// never ends the prefix (TestAModifierAloneKeepsThePrefixPending).
var modifierKeys = map[string]bool{
	"shift": true, "ctrl": true, "alt": true, "super": true, "hyper": true, "meta": true,
	"leftshift": true, "rightshift": true,
	"leftctrl": true, "rightctrl": true,
	"leftalt": true, "rightalt": true,
	"leftsuper": true, "rightsuper": true,
	"lefthyper": true, "righthyper": true,
	"leftmeta": true, "rightmeta": true,
	"isolevel3shift": true, "isolevel5shift": true,
	"capslock": true, "numlock": true, "scrolllock": true,
}

// keyName is the event's key name as the matcher reads it: "esc" is
// "escape", a space is "space", an ASCII letter is lower case (the key
// without modifiers applied, 4.2.6).
func keyName(e Event) string {
	switch e.Name {
	case "esc":
		return "escape"
	case " ":
		return "space"
	}
	if len(e.Name) == 1 && e.Name[0] >= 'A' && e.Name[0] <= 'Z' {
		return string(e.Name[0] - 'A' + 'a')
	}
	return e.Name
}

// eventText is the text a printable chord is compared with: the event's
// text, or when the terminal sent none (a kitty mode without associated
// text), the text the key would type on its own or with shift for a letter.
func eventText(e Event) string {
	if e.Text != "" {
		return e.Text
	}
	if len(e.Name) == 1 && e.Name[0] >= 'A' && e.Name[0] <= 'Z' {
		return e.Name // a terminal that reported the shifted letter itself
	}
	name := keyName(e)
	if utf8.RuneCountInString(name) != 1 {
		return ""
	}
	if e.Mod&proto.ModShift == 0 {
		return name
	}
	up, _ := shiftedLetter(name)
	return up
}

// matches reports whether the key event is this chord, ignoring whether it
// is pressed after the prefix.
func (c chord) matches(e Event) bool {
	mod := e.Mod &^ lockMods
	if c.text {
		if mod&^proto.ModShift != 0 {
			return false
		}
		return eventText(e) == c.key
	}
	return mod == c.mod && keyName(e) == c.key
}
