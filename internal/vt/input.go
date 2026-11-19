//go:build cgo

package vt

/*
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
#include <ghostty/vt.h>

// The C helpers of the input side. Their names start with dt_in_ so that
// they never meet a helper of engine.go or history.go (cgo gives every
// static function of the package one Go name).

// dt_in_mode reads one DEC mode; 0 when the library reports no value.
static int dt_in_mode(GhosttyTerminal t, uint16_t v) {
	GhosttyTerminalModeConfig m = { ghostty_mode_new(v, false), false };
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_MODE, &m) != GHOSTTY_SUCCESS) return 0;
	return m.value ? 1 : 0;
}

// dt_in_kitty reads the kitty keyboard flags of the active screen, the
// value the key encoder takes from the terminal.
static uint8_t dt_in_kitty(GhosttyTerminal t) {
	uint8_t f = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_KITTY_KEYBOARD_FLAGS, &f) != GHOSTTY_SUCCESS) return 0;
	return f;
}

// dt_in_key sets the key encoder from the terminal, fills the event and
// encodes it. The event does not copy its text (key/event.h), so the text,
// which is Go memory, is cleared again before the call returns: the
// library keeps no pointer to it.
static GhosttyResult dt_in_key(GhosttyKeyEncoder enc, GhosttyKeyEvent ev, GhosttyTerminal t,
	int key, int action, uint16_t mods, uint16_t consumed, const uint8_t *text, size_t tlen,
	uint32_t unshifted, uint8_t *buf, size_t cap, size_t *out) {
	*out = 0;
	GhosttyResult r = ghostty_key_encoder_set_from_terminal(enc, t);
	if (r != GHOSTTY_SUCCESS) return r;
	if ((r = ghostty_key_event_set_key(ev, (GhosttyKey)key)) != GHOSTTY_SUCCESS) return r;
	if ((r = ghostty_key_event_set_action(ev, (GhosttyKeyAction)action)) != GHOSTTY_SUCCESS) return r;
	ghostty_key_event_set_mods(ev, mods);
	ghostty_key_event_set_consumed_mods(ev, consumed);
	ghostty_key_event_set_composing(ev, false);
	GhosttyString s = { text, tlen };
	ghostty_key_event_set_utf8(ev, s);
	ghostty_key_event_set_unshifted_codepoint(ev, unshifted);
	r = ghostty_key_encoder_encode(enc, ev, buf, cap, out);
	GhosttyString none = { NULL, 0 };
	ghostty_key_event_set_utf8(ev, none);
	return r;
}

// dt_in_mouse does the four steps of EncodeMouse (5.3.2) in one call: the
// encoder from the terminal (tracking mode and format; this clears the
// encoder's last cell), the size of the surface with a cell of 10 x 20
// pixels and no padding, whether a button is held, then the event.
static GhosttyResult dt_in_mouse(GhosttyMouseEncoder enc, GhosttyMouseEvent ev, GhosttyTerminal t,
	uint32_t sw, uint32_t sh, bool held, int action, int button, uint16_t mods, float x, float y,
	uint8_t *buf, size_t cap, size_t *out) {
	*out = 0;
	GhosttyResult r = ghostty_mouse_encoder_set_from_terminal(enc, t);
	if (r != GHOSTTY_SUCCESS) return r;
	GhosttyMouseEncoderSize sz = GHOSTTY_INIT_SIZED(GhosttyMouseEncoderSize);
	sz.screen_width = sw;
	sz.screen_height = sh;
	sz.cell_width = 10;
	sz.cell_height = 20;
	if ((r = ghostty_mouse_encoder_set(enc, GHOSTTY_MOUSE_ENCODER_OPT_SIZE, &sz)) != GHOSTTY_SUCCESS) return r;
	if ((r = ghostty_mouse_encoder_set(enc, GHOSTTY_MOUSE_ENCODER_OPT_ANY_BUTTON_PRESSED, &held)) != GHOSTTY_SUCCESS) return r;
	if ((r = ghostty_mouse_event_set_action(ev, (GhosttyMouseAction)action)) != GHOSTTY_SUCCESS) return r;
	if (button == 0) r = ghostty_mouse_event_clear_button(ev);
	else r = ghostty_mouse_event_set_button(ev, (GhosttyMouseButton)button);
	if (r != GHOSTTY_SUCCESS) return r;
	ghostty_mouse_event_set_mods(ev, mods);
	GhosttyMousePosition pos = { x, y };
	ghostty_mouse_event_set_position(ev, pos);
	return ghostty_mouse_encoder_encode(enc, ev, buf, cap, out);
}
*/
import "C"

import (
	"bytes"
	"strconv"
	"unicode/utf8"
	"unsafe"
)

// inputState holds the library's encoders of one terminal, created in init
// and freed in close, and the two pieces of mouse state the mouse encoder
// loses whenever it is set from the terminal (5.3.2).
type inputState struct {
	ke  C.GhosttyKeyEncoder
	kev C.GhosttyKeyEvent
	me  C.GhosttyMouseEncoder
	mev C.GhosttyMouseEvent

	// held has one bit per mouse button that is down: left 1, middle 2,
	// right 4. Set on a press, cleared on the release; a wheel event does
	// not touch it.
	held uint8
	// The cell of the last mouse event that produced bytes. A motion event
	// inside it is dropped: set_from_terminal clears the library's own
	// memory of it on every call.
	lastX, lastY int
	haveLast     bool
}

// init creates the encoders. A failure of the library to allocate them
// panics, as New does when it cannot create the terminal.
func (s *inputState) init(t *term) {
	steps := []struct {
		what string
		r    C.GhosttyResult
	}{
		{"key_encoder_new", C.ghostty_key_encoder_new(nil, &s.ke)},
		{"key_event_new", C.ghostty_key_event_new(nil, &s.kev)},
		{"mouse_encoder_new", C.ghostty_mouse_encoder_new(nil, &s.me)},
		{"mouse_event_new", C.ghostty_mouse_event_new(nil, &s.mev)},
	}
	for _, st := range steps {
		if st.r != C.GHOSTTY_SUCCESS {
			s.close(t)
			panic("vt: " + st.what + ": result " + strconv.Itoa(int(st.r)))
		}
	}
}

// close frees the encoders; the Encode methods return nil afterwards.
func (s *inputState) close(t *term) {
	if s.kev != nil {
		C.ghostty_key_event_free(s.kev)
		s.kev = nil
	}
	if s.ke != nil {
		C.ghostty_key_encoder_free(s.ke)
		s.ke = nil
	}
	if s.mev != nil {
		C.ghostty_mouse_event_free(s.mev)
		s.mev = nil
	}
	if s.me != nil {
		C.ghostty_mouse_encoder_free(s.me)
		s.me = nil
	}
}

// legacyForms is the one table of key bytes devtools keeps (5.3.2, E10
// item 9): bytes the library's encoder produces, mapped to the bytes the
// pane gets instead. It applies only while the program has not enabled the
// kitty protocol (flags 0), that is in legacy mode and with
// modifyOtherKeys; with kitty flags the library's bytes are the protocol's
// and stay. Each entry is a key whose legacy bytes xterm documents and the
// library sends otherwise; each has a test row.
//
// What the table does not hold, decided by P1-03 and kept as the library
// sends it: in legacy mode shift, ctrl, ctrl+shift and ctrl+alt with
// Enter, Escape and (but shift) Tab give fixterms' CSI 27 ; m ; 13|27|9 ~
// where xterm with modifyOtherKeys 0 sends CR, ESC and TAB; ctrl+Backspace
// in modifyOtherKeys 2 is BS. No document of xterm fixes these (E10 item
// 9 recorded them, it did not judge them), terminfo has no entry for them,
// they are the bytes the user's own terminal (ghostty, the same library)
// sends to a program that runs in it directly (E3: 116 of 116 events
// equal), and they keep Shift+Enter and Ctrl+Enter distinct from Enter for
// the agent programs that read them. The same holds for the library's
// fixterms forms of ctrl+i and ctrl+m (CSI 105 ; 5 u, CSI 109 ; 5 u), which
// are text keys and not names of E10's list. TestFixtermsFormsAreKept pins
// them. With F3 the table holds 1 of the 5 names of 5.3.2.
var legacyForms = func() map[string]string {
	m := map[string]string{}
	// F3 with any modifier: the library sends CSI 13 ; m ~
	// (function_keys.zig:97, kitty's form), xterm documents CSI 1 ; m R. The
	// pane's TERM is xterm-256color (4.7), whose terminfo has kf15=\E[1;2R
	// up to kf63; a program that reads terminfo knows the xterm form and not
	// the library's. That CSI 1 ; m R reads like a cursor position report is
	// xterm's own property, which every program under xterm lives with.
	// TestF3WithAModifier.
	for p := 2; p <= 16; p++ {
		m["\x1b[13;"+strconv.Itoa(p)+"~"] = "\x1b[1;" + strconv.Itoa(p) + "R"
	}
	return m
}()

// keyEvent is a Key of the wire made ready for the library's key event.
type keyEvent struct {
	code      int // GhosttyKey (keytable.go)
	action    C.int
	mods      int    // wire bits (4.2.6)
	consumed  bool   // shift was used to produce text
	text      string // the text of the key before ctrl and alt; "" for a named key
	unshifted rune   // the key's own character; 0 for a named key
}

// newKeyEvent maps a Key to the library's event. ok is false for an
// unknown key name.
//
// The text the library wants is "the unmodified character before any
// Ctrl/Meta transformations", never a control character (key/event.h). It
// is the wire's text when that is printable; otherwise, for a key of one
// character, the character itself, shifted when shift is held (the wire's
// shifted character, or the capital of a letter). A named key has no text,
// except space. The physical key comes from the name, or from the base
// character of the US layout when the outer terminal reported one, so that
// a key of another layout keeps its position (ctrl+с is ctrl+c).
func newKeyEvent(k Key, alternates bool) (keyEvent, bool) {
	code, ok := keyCode(k.Name)
	if !ok {
		return keyEvent{}, false
	}
	e := keyEvent{code: code, mods: k.Mod & 0xff, action: C.GHOSTTY_KEY_ACTION_PRESS}
	switch {
	case k.Release:
		e.action = C.GHOSTTY_KEY_ACTION_RELEASE
	case k.Repeat:
		e.action = C.GHOSTTY_KEY_ACTION_REPEAT
	}
	r, single := keyScalar(k.Name)
	if k.Name == "space" {
		r, single = ' ', true
	}
	if !single {
		return e, true
	}
	e.unshifted = r
	if k.Base != 0 && r != ' ' {
		if c := runeCode(k.Base); c != keyUnidentified {
			e.code = c
		}
	}
	e.text = keyText(k, r, alternates)
	e.consumed = e.mods&1 != 0 && e.text != string(r)
	return e, true
}

// keyText is the text of a key of one character r (see newKeyEvent). With
// ctrl held it is r itself, unshifted, unless the program asked kitty for
// alternate keys: xterm sends ctrl+shift+a as ^A, and the library sends
// the C0 byte only for the unshifted text (its ctrlSeq; E10 item 9 recorded
// ctrl+shift+a as ^A this way). Kitty's alternate keys need the shifted
// text, which the library reports as the shifted key.
func keyText(k Key, r rune, alternates bool) string {
	if k.Mod&4 != 0 && !alternates {
		return string(r)
	}
	if printable(k.Text) {
		return k.Text
	}
	if k.Mod&1 != 0 {
		if k.Shifted != 0 && printable(string(k.Shifted)) {
			return string(k.Shifted)
		}
		if r >= 'a' && r <= 'z' {
			return string(r - 'a' + 'A')
		}
	}
	return string(r)
}

// printable reports a non-empty valid UTF-8 text without control characters.
func printable(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if isControlRune(r) {
			return false
		}
	}
	return true
}

// libMods maps the modifier bits of the wire (kitty's order: shift 1, alt 2,
// ctrl 4, super 8, hyper 16, meta 32, caps lock 64, num lock 128) to the
// library's (shift, ctrl, alt, super, caps lock, num lock). The library has
// no hyper and no meta; they are dropped.
func libMods(m int) C.uint16_t {
	var g C.uint16_t
	for _, b := range [...]struct {
		wire int
		lib  C.uint16_t
	}{
		{1, C.GHOSTTY_MODS_SHIFT}, {2, C.GHOSTTY_MODS_ALT}, {4, C.GHOSTTY_MODS_CTRL},
		{8, C.GHOSTTY_MODS_SUPER}, {64, C.GHOSTTY_MODS_CAPS_LOCK}, {128, C.GHOSTTY_MODS_NUM_LOCK},
	} {
		if m&b.wire != 0 {
			g |= b.lib
		}
	}
	return g
}

// cBytes is a pointer to the first byte of b for a call into the library,
// nil for an empty slice.
func cBytes(b []byte) *C.uint8_t {
	if len(b) == 0 {
		return nil
	}
	return (*C.uint8_t)(unsafe.Pointer(&b[0]))
}

// EncodeKey returns the bytes a key becomes for the program in its current
// modes: the library's key encoder, set from the terminal (legacy, kitty
// flags, modifyOtherKeys, application cursor and keypad), then the table
// legacyForms. nil for an unknown key name and for an event the program did
// not ask for (a release without kitty's event types).
func (t *term) EncodeKey(k Key) []byte {
	s := &t.inputState
	if t.h == nil || s.ke == nil {
		return nil
	}
	kitty := C.dt_in_kitty(t.h)
	e, ok := newKeyEvent(k, kitty&C.GHOSTTY_KITTY_KEY_REPORT_ALTERNATES != 0)
	if !ok {
		return nil
	}
	text := []byte(e.text)
	var consumed C.uint16_t
	if e.consumed {
		consumed = C.GHOSTTY_MODS_SHIFT
	}
	buf := make([]byte, 64)
	for {
		var n C.size_t
		r := C.dt_in_key(s.ke, s.kev, t.h, C.int(e.code), e.action, libMods(e.mods), consumed,
			cBytes(text), C.size_t(len(text)), C.uint32_t(e.unshifted), cBytes(buf), C.size_t(len(buf)), &n)
		if r == C.GHOSTTY_OUT_OF_SPACE && int(n) > len(buf) {
			buf = make([]byte, int(n))
			continue
		}
		if r != C.GHOSTTY_SUCCESS || n == 0 {
			return nil
		}
		out := buf[:n]
		if kitty == 0 {
			if b, ok := legacyForms[string(out)]; ok {
				return []byte(b)
			}
		}
		return out
	}
}

// mouseButton maps a button of the wire (4.2.6) to the library's button and
// its bit in inputState.held. ok is false for a name the kind does not take.
func mouseButton(kind, name string) (button C.int, bit uint8, ok bool) {
	if kind == "wheel" {
		switch name {
		case "up":
			return C.GHOSTTY_MOUSE_BUTTON_FOUR, 0, true
		case "down":
			return C.GHOSTTY_MOUSE_BUTTON_FIVE, 0, true
		case "left-wheel":
			return C.GHOSTTY_MOUSE_BUTTON_SIX, 0, true
		case "right-wheel":
			return C.GHOSTTY_MOUSE_BUTTON_SEVEN, 0, true
		}
		return 0, 0, false
	}
	switch name {
	case "left":
		return C.GHOSTTY_MOUSE_BUTTON_LEFT, 1, true
	case "middle":
		return C.GHOSTTY_MOUSE_BUTTON_MIDDLE, 2, true
	case "right":
		return C.GHOSTTY_MOUSE_BUTTON_RIGHT, 4, true
	case "none", "":
		return 0, 0, kind != "press"
	}
	return 0, 0, false
}

// heldButton is the lowest button that is down, for a motion or release
// event of the wire that names none.
func (s *inputState) heldButton() (C.int, uint8) {
	switch {
	case s.held&1 != 0:
		return C.GHOSTTY_MOUSE_BUTTON_LEFT, 1
	case s.held&2 != 0:
		return C.GHOSTTY_MOUSE_BUTTON_MIDDLE, 2
	case s.held&4 != 0:
		return C.GHOSTTY_MOUSE_BUTTON_RIGHT, 4
	}
	return 0, 0
}

// EncodeMouse returns the report of a mouse event for the program's
// tracking mode and format (X10, normal, button-event, any-event; legacy or
// SGR), or nil when the program did not ask for the event. The library's
// encoder takes pixels: the cell (x, y) is passed as its centre, x*10+5 and
// y*20+10, on a surface of cols*10 x rows*20 pixels, so the report depends
// only on the cell, the button and the modes (5.3.2, E10 item 10).
func (t *term) EncodeMouse(e Mouse) []byte {
	s := &t.inputState
	if t.h == nil || s.me == nil {
		return nil
	}
	if e.X < 0 || e.Y < 0 || e.X >= t.cols || e.Y >= t.rows {
		return nil
	}
	var action C.int
	switch e.Kind {
	case "press", "wheel":
		action = C.GHOSTTY_MOUSE_ACTION_PRESS
	case "release":
		action = C.GHOSTTY_MOUSE_ACTION_RELEASE
	case "motion":
		action = C.GHOSTTY_MOUSE_ACTION_MOTION
	default:
		return nil
	}
	button, bit, ok := mouseButton(e.Kind, e.Button)
	if !ok {
		return nil
	}
	if button == 0 && e.Kind != "wheel" {
		button, bit = s.heldButton()
	}
	if e.Kind == "motion" && s.haveLast && e.X == s.lastX && e.Y == s.lastY {
		return nil
	}
	switch e.Kind {
	case "press":
		s.held |= bit
	case "release":
		if bit == 0 {
			s.held = 0
		}
		s.held &^= bit
	}
	buf := make([]byte, 32)
	for {
		var n C.size_t
		r := C.dt_in_mouse(s.me, s.mev, t.h, C.uint32_t(t.cols*cellWidthPx), C.uint32_t(t.rows*cellHeightPx),
			C.bool(s.held != 0), action, button, libMods(e.Mod),
			C.float(e.X*cellWidthPx+cellWidthPx/2), C.float(e.Y*cellHeightPx+cellHeightPx/2),
			cBytes(buf), C.size_t(len(buf)), &n)
		if r == C.GHOSTTY_OUT_OF_SPACE && int(n) > len(buf) {
			buf = make([]byte, int(n))
			continue
		}
		if r != C.GHOSTTY_SUCCESS || n == 0 {
			return nil
		}
		if e.Kind != "wheel" {
			s.lastX, s.lastY, s.haveLast = e.X, e.Y, true
		}
		return buf[:n]
	}
}

// The brackets of mode 2004.
var (
	pasteOpen  = []byte("\x1b[200~")
	pasteClose = []byte("\x1b[201~")
)

// EncodePaste returns one chunk of a paste, cleaned by the library's paste
// encoder (control bytes such as ESC become spaces, so the chunk cannot
// close its own bracket). The library brackets one whole call and knows no
// chunks: with mode 2004 the chunk is encoded bracketed, the library's two
// brackets are taken off, and the opening bracket is written when first and
// the closing one when last (5.3.2, E10 item 11). Without mode 2004 there
// are no brackets and a newline becomes CR. The encoder writes into its
// input, so it works on a copy; the caller's text is never changed.
func (t *term) EncodePaste(text string, first, last bool) []byte {
	if t.h == nil {
		return nil
	}
	bracketed := C.dt_in_mode(t.h, 2004) != 0
	var body []byte
	if text != "" {
		data := []byte(text)
		var need C.size_t
		r := C.ghostty_paste_encode((*C.char)(unsafe.Pointer(&data[0])), C.size_t(len(data)), C.bool(bracketed), nil, 0, &need)
		if r != C.GHOSTTY_SUCCESS && r != C.GHOSTTY_OUT_OF_SPACE {
			return nil
		}
		if need > 0 {
			out := make([]byte, int(need))
			var n C.size_t
			r = C.ghostty_paste_encode((*C.char)(unsafe.Pointer(&data[0])), C.size_t(len(data)), C.bool(bracketed), cBytes(out), need, &n)
			if r != C.GHOSTTY_SUCCESS {
				return nil
			}
			body = out[:n]
		}
	}
	if !bracketed {
		if len(body) == 0 {
			return nil
		}
		return body
	}
	body = bytes.TrimPrefix(body, pasteOpen)
	body = bytes.TrimSuffix(body, pasteClose)
	var res []byte
	if first {
		res = append(res, pasteOpen...)
	}
	res = append(res, body...)
	if last {
		res = append(res, pasteClose...)
	}
	if len(res) == 0 {
		return nil
	}
	return res
}

// EncodeFocus returns the focus report (CSI I, CSI O) when the program
// enabled mode 1004, else nil.
func (t *term) EncodeFocus(focused bool) []byte {
	if t.h == nil || C.dt_in_mode(t.h, 1004) == 0 {
		return nil
	}
	ev := C.GhosttyFocusEvent(C.GHOSTTY_FOCUS_GAINED)
	if !focused {
		ev = C.GHOSTTY_FOCUS_LOST
	}
	buf := make([]byte, 8)
	var n C.size_t
	if C.ghostty_focus_encode(ev, cBytes(buf), C.size_t(len(buf)), &n) != C.GHOSTTY_SUCCESS || n == 0 {
		return nil
	}
	return buf[:n]
}

// keyConstants are the constants of keytable.go next to the header's, for
// TestKeyConstantsAreTheHeaders (a test file cannot use import "C").
func keyConstants() []struct {
	name     string
	go_, lib int
} {
	return []struct {
		name     string
		go_, lib int
	}{
		{"UNIDENTIFIED", keyUnidentified, C.GHOSTTY_KEY_UNIDENTIFIED},
		{"BACKQUOTE", keyBackquote, C.GHOSTTY_KEY_BACKQUOTE},
		{"BACKSLASH", keyBackslash, C.GHOSTTY_KEY_BACKSLASH},
		{"BRACKET_LEFT", keyBracketLeft, C.GHOSTTY_KEY_BRACKET_LEFT},
		{"BRACKET_RIGHT", keyBracketRight, C.GHOSTTY_KEY_BRACKET_RIGHT},
		{"COMMA", keyComma, C.GHOSTTY_KEY_COMMA},
		{"DIGIT_0", keyDigit0, C.GHOSTTY_KEY_DIGIT_0},
		{"DIGIT_9", keyDigit0 + 9, C.GHOSTTY_KEY_DIGIT_9},
		{"EQUAL", keyEqual, C.GHOSTTY_KEY_EQUAL},
		{"A", keyA, C.GHOSTTY_KEY_A},
		{"Z", keyA + 25, C.GHOSTTY_KEY_Z},
		{"MINUS", keyMinus, C.GHOSTTY_KEY_MINUS},
		{"PERIOD", keyPeriod, C.GHOSTTY_KEY_PERIOD},
		{"QUOTE", keyQuote, C.GHOSTTY_KEY_QUOTE},
		{"SEMICOLON", keySemicolon, C.GHOSTTY_KEY_SEMICOLON},
		{"SLASH", keySlash, C.GHOSTTY_KEY_SLASH},
		{"BACKSPACE", keyBackspace, C.GHOSTTY_KEY_BACKSPACE},
		{"ENTER", keyEnter, C.GHOSTTY_KEY_ENTER},
		{"SPACE", keySpace, C.GHOSTTY_KEY_SPACE},
		{"TAB", keyTab, C.GHOSTTY_KEY_TAB},
		{"DELETE", keyDelete, C.GHOSTTY_KEY_DELETE},
		{"END", keyEnd, C.GHOSTTY_KEY_END},
		{"HOME", keyHome, C.GHOSTTY_KEY_HOME},
		{"INSERT", keyInsert, C.GHOSTTY_KEY_INSERT},
		{"PAGE_DOWN", keyPageDown, C.GHOSTTY_KEY_PAGE_DOWN},
		{"PAGE_UP", keyPageUp, C.GHOSTTY_KEY_PAGE_UP},
		{"ARROW_DOWN", keyArrowDown, C.GHOSTTY_KEY_ARROW_DOWN},
		{"ARROW_LEFT", keyArrowLeft, C.GHOSTTY_KEY_ARROW_LEFT},
		{"ARROW_RIGHT", keyArrowRight, C.GHOSTTY_KEY_ARROW_RIGHT},
		{"ARROW_UP", keyArrowUp, C.GHOSTTY_KEY_ARROW_UP},
		{"ESCAPE", keyEscape, C.GHOSTTY_KEY_ESCAPE},
		{"F1", keyF1, C.GHOSTTY_KEY_F1},
		{"F12", keyF1 + 11, C.GHOSTTY_KEY_F12},
	}
}
