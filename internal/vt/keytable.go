package vt

import "unicode/utf8"

// The key names of 4.2.6 to the library's physical key constants
// (GhosttyKey, key/event.h of libghostty-vt 1.0.1) as plain integers. This
// file is pure Go and has no build constraint; input.go hands the constant
// to the library's key encoder, and TestKeyConstantsAreTheHeaders compares
// every constant used here with the header.
//
// devtools writes no key encoder of its own: this table only names the key.
// Which bytes a key becomes is the library's, from the terminal's own modes
// (input.go), with the exceptions of the table in input.go.

// The constants of GhosttyKey that the table uses, with the header's values.
const (
	keyUnidentified = 0

	keyBackquote    = 1
	keyBackslash    = 2
	keyBracketLeft  = 3
	keyBracketRight = 4
	keyComma        = 5
	keyDigit0       = 6 // keyDigit0 + n is the digit n
	keyEqual        = 16
	keyA            = 20 // keyA + n is the n-th letter of the alphabet
	keyMinus        = 46
	keyPeriod       = 47
	keyQuote        = 48
	keySemicolon    = 49
	keySlash        = 50

	keyBackspace = 53
	keyEnter     = 58
	keySpace     = 63
	keyTab       = 64

	keyDelete   = 68
	keyEnd      = 69
	keyHome     = 71
	keyInsert   = 72
	keyPageDown = 73
	keyPageUp   = 74

	keyArrowDown  = 75
	keyArrowLeft  = 76
	keyArrowRight = 77
	keyArrowUp    = 78

	keyEscape = 120
	keyF1     = 121 // keyF1 + n is the function key n+1, up to F12
)

// wireKeyNames is the fixed list of key names of 4.2.6, in the order of
// that section. Every other key travels as one Unicode scalar.
var wireKeyNames = []string{
	"enter", "tab", "backspace", "escape", "space",
	"up", "down", "left", "right",
	"home", "end", "pageup", "pagedown", "insert", "delete",
	"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12",
}

// keyNames maps the names of 4.2.6 to their constants.
var keyNames = map[string]int{
	"enter": keyEnter, "tab": keyTab, "backspace": keyBackspace, "escape": keyEscape, "space": keySpace,
	"up": keyArrowUp, "down": keyArrowDown, "left": keyArrowLeft, "right": keyArrowRight,
	"home": keyHome, "end": keyEnd, "pageup": keyPageUp, "pagedown": keyPageDown,
	"insert": keyInsert, "delete": keyDelete,
	"f1": keyF1, "f2": keyF1 + 1, "f3": keyF1 + 2, "f4": keyF1 + 3, "f5": keyF1 + 4, "f6": keyF1 + 5,
	"f7": keyF1 + 6, "f8": keyF1 + 7, "f9": keyF1 + 8, "f10": keyF1 + 9, "f11": keyF1 + 10, "f12": keyF1 + 11,
}

// keyChars maps the characters of the US layout that are not letters or
// digits to the physical key that types them without shift. Shifted
// characters ("!", "{") are not in it: 4.2.6 sends the key without
// modifiers applied, and which key types a shifted character depends on the
// layout.
var keyChars = map[rune]int{
	'`': keyBackquote, '\\': keyBackslash, '[': keyBracketLeft, ']': keyBracketRight,
	',': keyComma, '=': keyEqual, '-': keyMinus, '.': keyPeriod, '\'': keyQuote,
	';': keySemicolon, '/': keySlash, ' ': keySpace,
}

// keyCode returns the physical key of a key name of 4.2.6. A name of the
// fixed list and a character of the US layout have their own key; any other
// single Unicode scalar is keyUnidentified, and the library encodes it from
// its text. ok is false for anything else: a word that is not in the list,
// an empty or invalid string, a control character.
func keyCode(name string) (code int, ok bool) {
	if c, ok := keyNames[name]; ok {
		return c, true
	}
	r, ok := keyScalar(name)
	if !ok {
		return 0, false
	}
	return runeCode(r), true
}

// runeCode is the physical key of one character of the US layout, and
// keyUnidentified for every other character.
func runeCode(r rune) int {
	switch {
	case r >= 'a' && r <= 'z':
		return keyA + int(r-'a')
	case r >= 'A' && r <= 'Z':
		return keyA + int(r-'A')
	case r >= '0' && r <= '9':
		return keyDigit0 + int(r-'0')
	}
	if c, ok := keyChars[r]; ok {
		return c
	}
	return keyUnidentified
}

// keyScalar returns the one Unicode scalar a key name consists of. It is
// false for a name of two or more scalars, for invalid UTF-8 and for a
// control character (C0, DEL, C1), which the library must not be given as
// the text of a key (key/event.h, ghostty_key_event_set_utf8).
func keyScalar(name string) (rune, bool) {
	r, n := utf8.DecodeRuneInString(name)
	if n == 0 || n != len(name) || (r == utf8.RuneError && n == 1) || isControlRune(r) {
		return 0, false
	}
	return r, true
}

// isControlRune reports the characters that are never the text of a key.
func isControlRune(r rune) bool { return r < 0x20 || (r >= 0x7f && r < 0xa0) }
