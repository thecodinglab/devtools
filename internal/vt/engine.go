//go:build cgo

package vt

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <ghostty/vt.h>

// The Go functions of callbacks.go. cgo writes the same prototypes into
// _cgo_export.h; they are repeated here because a file with //export may
// hold only declarations in its preamble, so the trampolines live here.
extern void dtWritePty(uintptr_t h, uint8_t *data, size_t len);
extern void dtXtversion(uintptr_t h, uint8_t **ptr, size_t *len);
extern void dtTitle(uintptr_t h, uint8_t *ptr, size_t len);
extern void dtPwd(uintptr_t h, uint8_t *ptr, size_t len);
extern void dtBell(uintptr_t h);
extern int dtClipboard(uintptr_t h, uint8_t *ptr, size_t len);
extern void dtStatus(uintptr_t h, int state, int kind, size_t idlen);
extern int dtColorScheme(uintptr_t h);

// The trampolines. Userdata is a runtime/cgo.Handle, an integer, never a
// Go pointer. Each copies nothing itself and calls into Go once; the Go
// side copies what it is handed (callbacks.go). None of them calls a
// library function that changes the terminal: the title and the pwd are
// read with ghostty_terminal_get inside their callback, as the header asks
// (terminal.h, GhosttyTerminalTitleChangedFn and GhosttyTerminalPwdChangedFn).
static void dt_tr_write_pty(GhosttyTerminal t, void *ud, const uint8_t *d, size_t n) {
	(void)t;
	dtWritePty((uintptr_t)ud, (uint8_t *)d, n);
}
static GhosttyString dt_tr_xtversion(GhosttyTerminal t, void *ud) {
	(void)t;
	uint8_t *p = NULL;
	size_t n = 0;
	dtXtversion((uintptr_t)ud, &p, &n);
	GhosttyString s = { p, n };
	return s; // n == 0: the library answers with its default
}
static void dt_tr_title(GhosttyTerminal t, void *ud) {
	GhosttyString s;
	memset(&s, 0, sizeof s);
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_TITLE, &s) != GHOSTTY_SUCCESS) {
		s.ptr = NULL;
		s.len = 0;
	}
	dtTitle((uintptr_t)ud, (uint8_t *)s.ptr, s.len);
}
static void dt_tr_pwd(GhosttyTerminal t, void *ud) {
	GhosttyString s;
	memset(&s, 0, sizeof s);
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_PWD, &s) != GHOSTTY_SUCCESS) {
		s.ptr = NULL;
		s.len = 0;
	}
	dtPwd((uintptr_t)ud, (uint8_t *)s.ptr, s.len);
}
static void dt_tr_bell(GhosttyTerminal t, void *ud) {
	(void)t;
	dtBell((uintptr_t)ud);
}
// OSC 52 set (and the other clipboard writes the library normalises): the
// first text representation goes to Go, which keeps it or drops it. A
// request without contents asks to clear the clipboard and is ignored.
static void dt_tr_clipboard(GhosttyTerminal t, void *ud, const GhosttyClipboardWrite *w) {
	(void)t;
	int ok = 0;
	for (size_t i = 0; i < w->contents_len; i++) {
		const GhosttyClipboardContent *c = &w->contents[i];
		if (c->mime.len >= 4 && memcmp(c->mime.ptr, "text", 4) == 0) {
			ok = dtClipboard((uintptr_t)ud, (uint8_t *)c->data.ptr, c->data.len);
			break;
		}
	}
	GhosttyClipboardWriteReply r = GHOSTTY_INIT_SIZED(GhosttyClipboardWriteReply);
	r.result = ok ? GHOSTTY_CLIPBOARD_WRITE_RESULT_SUCCESS : GHOSTTY_CLIPBOARD_WRITE_RESULT_DENIED;
	w->reply(w, &r);
}
static void dt_tr_status(GhosttyTerminal t, void *ud, const GhosttyTerminalProgramStatus *r) {
	(void)t;
	dtStatus((uintptr_t)ud, (int)r->state, (int)r->kind, r->id.len);
}
static bool dt_tr_color_scheme(GhosttyTerminal t, void *ud, GhosttyColorScheme *out) {
	(void)t;
	int s = dtColorScheme((uintptr_t)ud);
	if (s < 0) return false;
	*out = s ? GHOSTTY_COLOR_SCHEME_DARK : GHOSTTY_COLOR_SCHEME_LIGHT;
	return true;
}

#define DT_SET(opt, v) do { if ((r = ghostty_terminal_set(t, opt, (const void *)(v))) < 0) return r; } while (0)

// The host settings of 5.3.2 that New always applies. TITLE_REPORT is left
// off (the library's default) and CLIPBOARD_READ is not set, so an OSC 52
// read gets no answer at all.
static GhosttyResult dt_install(GhosttyTerminal t, uintptr_t h, size_t lines) {
	GhosttyResult r;
	DT_SET(GHOSTTY_TERMINAL_OPT_USERDATA, (void *)h);
	DT_SET(GHOSTTY_TERMINAL_OPT_WRITE_PTY, dt_tr_write_pty);
	DT_SET(GHOSTTY_TERMINAL_OPT_XTVERSION, dt_tr_xtversion);
	DT_SET(GHOSTTY_TERMINAL_OPT_TITLE_CHANGED, dt_tr_title);
	DT_SET(GHOSTTY_TERMINAL_OPT_PWD_CHANGED, dt_tr_pwd);
	DT_SET(GHOSTTY_TERMINAL_OPT_BELL, dt_tr_bell);
	DT_SET(GHOSTTY_TERMINAL_OPT_CLIPBOARD_WRITE, dt_tr_clipboard);
	DT_SET(GHOSTTY_TERMINAL_OPT_PROGRAM_STATUS, dt_tr_status);
	DT_SET(GHOSTTY_TERMINAL_OPT_COLOR_SCHEME, dt_tr_color_scheme);
	GhosttyTerminalModeConfig m = { GHOSTTY_MODE_GRAPHEME_CLUSTER, true };
	DT_SET(GHOSTTY_TERMINAL_OPT_MODE_DEFAULT, &m);
	size_t nobytes = SIZE_MAX;
	DT_SET(GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_BYTES, &nobytes);
	DT_SET(GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES, &lines);
	return GHOSTTY_SUCCESS;
}

static GhosttyResult dt_set_lines(GhosttyTerminal t, size_t n) {
	return ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_SCROLLBACK_MAX_LINES, &n);
}

static GhosttyResult dt_set_colors(GhosttyTerminal t, uint8_t fr, uint8_t fg, uint8_t fb, uint8_t br, uint8_t bg, uint8_t bb) {
	GhosttyResult r;
	GhosttyColorRgb f = { fr, fg, fb };
	GhosttyColorRgb b = { br, bg, bb };
	if ((r = ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_COLOR_FOREGROUND, &f)) < 0) return r;
	return ghostty_terminal_set(t, GHOSTTY_TERMINAL_OPT_COLOR_BACKGROUND, &b);
}

static size_t dt_scrollback_rows(GhosttyTerminal t) {
	size_t n = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS, &n) != GHOSTTY_SUCCESS) return 0;
	return n;
}

static void dt_lib_size(GhosttyTerminal t, uint16_t *cols, uint16_t *rows) {
	*cols = 0;
	*rows = 0;
	uint16_t c = 0, r = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_COLS, &c) == GHOSTTY_SUCCESS) *cols = c;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_ROWS, &r) == GHOSTTY_SUCCESS) *rows = r;
}

static int dt_mode(GhosttyTerminal t, uint16_t v) {
	GhosttyTerminalModeConfig m = { ghostty_mode_new(v, false), false };
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_MODE, &m) != GHOSTTY_SUCCESS) return 0;
	return m.value ? 1 : 0;
}

// The modes of Modes in one call. Every out-parameter is zeroed first and
// written only when the library reported a value (5.3.2, binding rules).
typedef struct {
	uint8_t alt, mouse, sgr, paste, focus, appcursor, appkeypad, kitty;
} dt_modes_t;

// dt_probe asks the mouse encoder, set from the terminal, whether it would
// report one event. The library keeps one effective tracking mode (the last
// of 9, 1000, 1002 and 1003 set; resetting any of them ends tracking), which
// no data item of the terminal names, so the modes are read the way the
// encoder will act on them. *sgr is set when the report is in SGR form.
static int dt_probe(GhosttyMouseEncoder enc, GhosttyMouseEvent ev, GhosttyTerminal t,
	uint16_t cols, uint16_t rows, GhosttyMouseAction action, int button, bool held, uint8_t *sgr) {
	if (ghostty_mouse_encoder_set_from_terminal(enc, t) != GHOSTTY_SUCCESS) return 0;
	GhosttyMouseEncoderSize sz = GHOSTTY_INIT_SIZED(GhosttyMouseEncoderSize);
	sz.screen_width = (uint32_t)cols * 10;
	sz.screen_height = (uint32_t)rows * 20;
	sz.cell_width = 10;
	sz.cell_height = 20;
	if (ghostty_mouse_encoder_set(enc, GHOSTTY_MOUSE_ENCODER_OPT_SIZE, &sz) != GHOSTTY_SUCCESS) return 0;
	if (ghostty_mouse_encoder_set(enc, GHOSTTY_MOUSE_ENCODER_OPT_ANY_BUTTON_PRESSED, &held) != GHOSTTY_SUCCESS) return 0;
	ghostty_mouse_event_set_action(ev, action);
	if (button) ghostty_mouse_event_set_button(ev, (GhosttyMouseButton)button);
	else ghostty_mouse_event_clear_button(ev);
	ghostty_mouse_event_set_mods(ev, 0);
	GhosttyMousePosition pos = { 5, 10 };
	ghostty_mouse_event_set_position(ev, pos);
	uint8_t buf[32];
	size_t n = 0;
	if (ghostty_mouse_encoder_encode(enc, ev, buf, sizeof buf, &n) != GHOSTTY_SUCCESS || n == 0) return 0;
	*sgr = n >= 3 && buf[0] == 0x1b && buf[1] == '[' && buf[2] == '<';
	return 1;
}

static void dt_modes(GhosttyTerminal t, GhosttyMouseEncoder enc, GhosttyMouseEvent ev, uint16_t cols, uint16_t rows, dt_modes_t *o) {
	memset(o, 0, sizeof *o);
	GhosttyTerminalScreen s = GHOSTTY_TERMINAL_SCREEN_PRIMARY;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN, &s) == GHOSTTY_SUCCESS)
		o->alt = s == GHOSTTY_TERMINAL_SCREEN_ALTERNATE;
	bool tracking = false;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_MOUSE_TRACKING, &tracking) == GHOSTTY_SUCCESS && tracking) {
		// 3 "all": motion without a button; 2 "drag": motion with one held;
		// 1 "press": a press.
		if (dt_probe(enc, ev, t, cols, rows, GHOSTTY_MOUSE_ACTION_MOTION, 0, false, &o->sgr)) o->mouse = 3;
		else if (dt_probe(enc, ev, t, cols, rows, GHOSTTY_MOUSE_ACTION_MOTION, GHOSTTY_MOUSE_BUTTON_LEFT, true, &o->sgr)) o->mouse = 2;
		else if (dt_probe(enc, ev, t, cols, rows, GHOSTTY_MOUSE_ACTION_PRESS, GHOSTTY_MOUSE_BUTTON_LEFT, false, &o->sgr)) o->mouse = 1;
	}
	if (!o->mouse) o->sgr = dt_mode(t, 1006);
	o->paste = dt_mode(t, 2004);
	o->focus = dt_mode(t, 1004);
	o->appcursor = dt_mode(t, 1);
	o->appkeypad = dt_mode(t, 66);
	uint8_t f = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_KITTY_KEYBOARD_FLAGS, &f) == GHOSTTY_SUCCESS)
		o->kitty = f;
}

// The cursor: position and visibility from the terminal, shape and blink
// from the render state, which New's caller keeps up to date.
typedef struct {
	uint16_t x, y;
	uint8_t visible, blink, style;
} dt_cursor_t;

static void dt_cursor(GhosttyTerminal t, GhosttyRenderState rs, dt_cursor_t *o) {
	memset(o, 0, sizeof *o);
	o->style = GHOSTTY_CURSOR_STYLE_BLOCK;
	uint16_t v = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_CURSOR_X, &v) == GHOSTTY_SUCCESS) o->x = v;
	v = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_CURSOR_Y, &v) == GHOSTTY_SUCCESS) o->y = v;
	bool vis = false;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_CURSOR_VISIBLE, &vis) == GHOSTTY_SUCCESS) o->visible = vis;
	GhosttyRenderStateCursor c = GHOSTTY_INIT_SIZED(GhosttyRenderStateCursor);
	if (ghostty_render_state_get(rs, GHOSTTY_RENDER_STATE_DATA_CURSOR, &c) == GHOSTTY_SUCCESS) {
		o->blink = c.blinking;
		o->style = (uint8_t)c.visual_style;
	}
}
*/
import "C"

import (
	"runtime/cgo"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"
)

// writePiece is the largest piece of program output Write hands the library
// in one call; historyState.afterWrite runs after each piece, so the anchor
// of history.go is replanted before the next piece can scroll it out
// (5.3.2). E10 item 1 kept it at 4 KiB: the largest piece that lost no
// anchor was 20,736 bytes of line feeds.
const writePiece = 4 << 10

// cellWidthPx and cellHeightPx are the pixel size of one cell the library is
// told about. devtools has cells, not pixels; the same 10 by 20 is the cell
// of the mouse encoder (5.3.2), so in-band size reports and the mouse agree.
const cellWidthPx, cellHeightPx = 10, 20

// maxDim is the largest size the library takes (uint16_t).
const maxDim = 1<<16 - 1

// historyHeadroom is the number of lines the library may hold beyond
// Options.History at a width of cols columns: one page at that width plus a
// margin, max(1000, ceil(46500 / cols) + 100). The library prunes whole
// pages and so holds up to one page fewer rows than its limit, and a page
// holds fewer rows the wider they are.
//
// provisional (E10 item 1), measured against libghostty-vt 1.0.1: the page
// capacity of 46,500 cells is not a documented value of the header. 1,000 at
// 80 to 200 columns, 1,263 at 40, 11,725 at 4.
func historyHeadroom(cols int) int {
	cols = max(cols, 1)
	return max(1000, (46500+cols-1)/cols+100)
}

// term is the one implementation of Term (5.3.2). inputState is written by
// input.go (P1-03) and historyState by history.go (P1-20); engine.go calls
// their hooks at the named moments and touches nothing else of them.
type term struct {
	h C.GhosttyTerminal
	o Options
	inputState
	historyState

	handle     cgo.Handle
	cols, rows int
	history    int // Options.History, never negative

	// The render state the rows and the cursor are read from (rows.go).
	// dirty is set by every call that can change the screen and cleared
	// when the render state is brought up to date.
	rs    C.GhosttyRenderState
	it    C.GhosttyRenderStateRowIterator
	cells C.GhosttyRenderStateRowCells
	dirty bool
	itPos C.int // the row the render state's row iterator stands on; -1 before the first
	rb    rowBuf

	// A mouse encoder and event of this file only, which Modes asks for the
	// effective tracking mode (dt_probe). The encoders of the input side
	// are inputState's.
	probe   C.GhosttyMouseEncoder
	probeEv C.GhosttyMouseEvent

	// Filled by the callbacks inside Write and Resize, drained by Replies.
	replies []byte

	// xtv is this terminal's copy of the XTVERSION answer in C memory,
	// valid until Close; nil when SetIdentity was not called.
	xtv    *C.char
	xtvLen int

	colors    Colors
	hasColors bool

	// onPiece, when set, runs after every piece Write hands the library
	// (TestWriteFeedsTheLibraryInPieces). Nil in the product.
	onPiece func()
}

var _ Term = (*term)(nil)

// identity is the XTVERSION answer set by SetIdentity; nil until then.
var identity atomic.Pointer[string]

// SetIdentity sets the XTVERSION answer "devtools <version> <host>";
// process-wide, once, before New. Each word is restricted to the characters
// the guard of 3.4 reads ([A-Za-z0-9._-], and "+" in the version); an empty
// word becomes "unknown", so the answer always has three words. Terminals
// made before the call keep the answer they were made with. Without a call
// the library answers with its default, "libghostty".
func SetIdentity(name, version, host string) {
	s := identWord(name, false, "devtools") + " " + identWord(version, true, "unknown") + " " + identWord(host, false, "unknown")
	identity.Store(&s)
}

func identWord(s string, plus bool, empty string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case plus && r == '+':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return empty
	}
	return b.String()
}

// clampDim keeps a size inside what the library takes.
func clampDim(n int) int { return min(max(n, 1), maxDim) }

// New creates a terminal with the host settings of 5.3.2 applied. A failure
// of the library to create the terminal (out of memory) panics: there is no
// terminal to return.
func New(o Options) Term {
	t := &term{o: o, cols: clampDim(o.Cols), rows: clampDim(o.Rows), history: max(o.History, 0), dirty: true}
	if r := C.ghostty_terminal_new(nil, &t.h, C.uint16_t(t.cols), C.uint16_t(t.rows)); r != C.GHOSTTY_SUCCESS {
		panic("vt: ghostty_terminal_new: result " + strconv.Itoa(int(r)))
	}
	t.handle = cgo.NewHandle(t)
	if id := identity.Load(); id != nil {
		t.xtv = C.CString(*id)
		t.xtvLen = len(*id)
	}
	steps := []struct {
		what string
		r    C.GhosttyResult
	}{
		{"install", C.dt_install(t.h, C.uintptr_t(t.handle), C.size_t(t.lineLimit(t.cols)))},
		// The cell size in pixels; ghostty_terminal_new sets none.
		{"resize", C.ghostty_terminal_resize(t.h, C.uint16_t(t.cols), C.uint16_t(t.rows), cellWidthPx, cellHeightPx)},
		{"render_state_new", C.ghostty_render_state_new(nil, &t.rs)},
		{"row_iterator_new", C.ghostty_render_state_row_iterator_new(nil, &t.it)},
		{"row_cells_new", C.ghostty_render_state_row_cells_new(nil, &t.cells)},
		{"mouse_encoder_new", C.ghostty_mouse_encoder_new(nil, &t.probe)},
		{"mouse_event_new", C.ghostty_mouse_event_new(nil, &t.probeEv)},
	}
	for _, s := range steps {
		if s.r != C.GHOSTTY_SUCCESS {
			t.free()
			panic("vt: " + s.what + ": result " + strconv.Itoa(int(s.r)))
		}
	}
	// The library answers OSC 10 and 11 only once colours were set through
	// the API (E10 item 5).
	t.SetColors(o.Colors)
	t.replies = nil
	t.inputState.init(t)
	t.historyState.init(t)
	return t
}

// lineLimit is the library's line limit at a width: History plus the
// headroom of one page.
func (t *term) lineLimit(cols int) int { return t.history + historyHeadroom(cols) }

func (t *term) setLineLimit(cols int) { C.dt_set_lines(t.h, C.size_t(t.lineLimit(cols))) }

// Write feeds program output to the library in pieces of at most writePiece
// bytes and calls historyState.afterWrite after each piece.
func (t *term) Write(p []byte) {
	if t.h == nil || len(p) == 0 {
		return
	}
	t.dirty = true
	for len(p) > 0 {
		n := min(len(p), writePiece)
		C.ghostty_terminal_vt_write(t.h, (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(p))), C.size_t(n))
		t.keepSize()
		t.historyState.afterWrite(t)
		if t.onPiece != nil {
			t.onPiece()
		}
		p = p[n:]
	}
}

// keepSize puts the library back to the pane's size when a program changed
// it (DECCOLM with mode 40 set switches to 80 or 132 columns). The pane's
// size is the PTY's; the grid must not disagree with it.
func (t *term) keepSize() {
	var c, r C.uint16_t
	C.dt_lib_size(t.h, &c, &r)
	if int(c) != t.cols || int(r) != t.rows {
		cols, rows := t.cols, t.rows
		t.cols, t.rows = int(c), int(r)
		t.Resize(cols, rows)
	}
}

// Resize changes the grid. The line limit is raised for the narrower of the
// two widths before the library reflows and set for the new width after it,
// so a narrowing reflow cannot prune below History (E10 item 1).
func (t *term) Resize(cols, rows int) {
	if t.h == nil {
		return
	}
	cols, rows = clampDim(cols), clampDim(rows)
	t.dirty = true
	t.historyState.beforeResize(t)
	t.setLineLimit(min(t.cols, cols))
	C.ghostty_terminal_resize(t.h, C.uint16_t(cols), C.uint16_t(rows), cellWidthPx, cellHeightPx)
	t.setLineLimit(cols)
	t.cols, t.rows = cols, rows
	t.historyState.afterResize(t)
}

// Size is the size the terminal was made or last resized with.
func (t *term) Size() (cols, rows int) { return t.cols, t.rows }

// Replies returns the bytes the library wants written to the PTY (DA, DSR,
// XTVERSION, OSC answers, size reports) and empties the queue.
func (t *term) Replies() []byte {
	r := t.replies
	t.replies = nil
	return r
}

// Cursor is the cursor of the live screen.
func (t *term) Cursor() Cursor {
	if t.h == nil {
		return Cursor{}
	}
	t.render()
	var c C.dt_cursor_t
	C.dt_cursor(t.h, t.rs, &c)
	shape := "block"
	switch c.style {
	case C.GHOSTTY_CURSOR_STYLE_BAR:
		shape = "bar"
	case C.GHOSTTY_CURSOR_STYLE_UNDERLINE:
		shape = "underline"
	}
	return Cursor{X: int(c.x), Y: int(c.y), Visible: c.visible != 0, Shape: shape, Blink: c.blink != 0}
}

// Modes reads the modes of Modes from the library in one call.
func (t *term) Modes() Modes {
	if t.h == nil {
		return Modes{}
	}
	var m C.dt_modes_t
	C.dt_modes(t.h, t.probe, t.probeEv, C.uint16_t(t.cols), C.uint16_t(t.rows), &m)
	mouse := [...]string{"", "press", "drag", "all"}[min(int(m.mouse), 3)]
	return Modes{
		Alt:       m.alt != 0,
		Mouse:     mouse,
		MouseSGR:  m.sgr != 0,
		Paste:     m.paste != 0,
		Focus:     m.focus != 0,
		AppCursor: m.appcursor != 0,
		AppKeypad: m.appkeypad != 0,
		Kitty:     int(m.kitty),
	}
}

// SyncActive reports whether the program holds synchronized output (mode
// 2026) open. A resize ends it (the library's rule).
func (t *term) SyncActive() bool {
	if t.h == nil {
		return false
	}
	return C.dt_mode(t.h, 2026) != 0
}

// SetColors sets the default colours the library answers OSC 10 and 11
// with, and the colour scheme it answers CSI ? 996 n with. A colour that is
// not "#rrggbb" leaves the library's colours as they are.
func (t *term) SetColors(c Colors) {
	if t.h == nil {
		return
	}
	fg, ok1 := parseHex(c.Fg)
	bg, ok2 := parseHex(c.Bg)
	if !ok1 || !ok2 {
		return
	}
	t.colors, t.hasColors = c, true
	t.dirty = true
	C.dt_set_colors(t.h, C.uint8_t(fg[0]), C.uint8_t(fg[1]), C.uint8_t(fg[2]), C.uint8_t(bg[0]), C.uint8_t(bg[1]), C.uint8_t(bg[2]))
}

// parseHex reads "#rrggbb".
func parseHex(s string) ([3]uint8, bool) {
	var c [3]uint8
	if len(s) != 7 || s[0] != '#' {
		return c, false
	}
	for i := range 3 {
		v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return c, false
		}
		c[i] = uint8(v)
	}
	return c, true
}

// Close runs the close hooks of the input and the history side, then frees
// the library's memory and the handle. A second Close does nothing.
func (t *term) Close() {
	if t.h == nil {
		return
	}
	t.inputState.close(t)
	t.historyState.close(t)
	t.free()
}

// free releases what New allocated, as far as it got.
func (t *term) free() {
	if t.probeEv != nil {
		C.ghostty_mouse_event_free(t.probeEv)
		t.probeEv = nil
	}
	if t.probe != nil {
		C.ghostty_mouse_encoder_free(t.probe)
		t.probe = nil
	}
	if t.cells != nil {
		C.ghostty_render_state_row_cells_free(t.cells)
		t.cells = nil
	}
	if t.it != nil {
		C.ghostty_render_state_row_iterator_free(t.it)
		t.it = nil
	}
	if t.rs != nil {
		C.ghostty_render_state_free(t.rs)
		t.rs = nil
	}
	if t.h != nil {
		C.ghostty_terminal_free(t.h)
		t.h = nil
	}
	if t.xtv != nil {
		C.free(unsafe.Pointer(t.xtv))
		t.xtv = nil
	}
	t.handle.Delete()
}

// render brings the render state up to date with the terminal when
// something changed since the last read.
func (t *term) render() {
	if !t.dirty {
		return
	}
	C.ghostty_render_state_update(t.rs, t.h)
	t.dirty = false
	t.itPos = -1
}

// scrollbackRows is the number of history rows the library holds now
// (TestHistoryHoldsTenThousandWideLines).
func (t *term) scrollbackRows() int { return int(C.dt_scrollback_rows(t.h)) }

// libSize is the size the library has now (keepSize, TestGarbageNeverCrashes).
func (t *term) libSize() (cols, rows int) {
	var c, r C.uint16_t
	C.dt_lib_size(t.h, &c, &r)
	return int(c), int(r)
}
