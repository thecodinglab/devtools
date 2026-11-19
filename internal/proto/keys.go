package proto

import "unicode/utf8"

// Modifier bits of input.key and input.mouse, in kitty's order (4.2.6).
const (
	ModShift = 1 << iota
	ModAlt
	ModCtrl
	ModSuper
	ModHyper
	ModMeta
	ModCaps
	ModNum
)

// keyNames is the fixed list of key names of 4.2.6.
var keyNames = []string{
	"enter", "tab", "backspace", "escape", "space",
	"up", "down", "left", "right",
	"home", "end", "pageup", "pagedown", "insert", "delete",
	"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12",
}

var keyNameSet = func() map[string]bool {
	m := make(map[string]bool, len(keyNames))
	for _, n := range keyNames {
		m[n] = true
	}
	return m
}()

// KeyNames returns the fixed key names of 4.2.6, in the order of the list
// there. The slice is a copy.
func KeyNames() []string { return append([]string(nil), keyNames...) }

// ValidKey reports whether name is a key of 4.2.6: one of the fixed names, or
// exactly one Unicode scalar that is not a control character (C0, DEL, C1).
func ValidKey(name string) bool {
	if keyNameSet[name] {
		return true
	}
	r, size := utf8.DecodeRuneInString(name)
	if size == 0 || size != len(name) {
		return false
	}
	if r == utf8.RuneError && size == 1 {
		return false
	}
	return r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f)
}
