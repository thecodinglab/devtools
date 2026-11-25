//go:build cgo

package vt

/*
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
#include <ghostty/vt.h>

// dt_track points a tracked reference at column 0 of row y of a point space
// of the active screen: a new reference when *ref is NULL, else the same one
// moved there (which clears its "no value" state). 0 on success; on failure
// *ref is unchanged.
static int dt_track(GhosttyTerminal t, GhosttyTrackedGridRef *ref, int tag, uint32_t y) {
	GhosttyPoint p;
	memset(&p, 0, sizeof p);
	p.tag = (GhosttyPointTag)tag;
	p.value.coordinate.x = 0;
	p.value.coordinate.y = y;
	if (*ref != NULL) return ghostty_tracked_grid_ref_set(*ref, t, p) == GHOSTTY_SUCCESS ? 0 : -1;
	GhosttyTrackedGridRef r = NULL;
	if (ghostty_terminal_grid_ref_track(t, p, &r) != GHOSTTY_SUCCESS || r == NULL) return -1;
	*ref = r;
	return 0;
}

// dt_where is the row of a tracked reference in screen coordinates of the
// page list that owns it (history first); -1 when it has no value.
static int64_t dt_where(GhosttyTrackedGridRef ref) {
	if (ref == NULL) return -1;
	GhosttyPointCoordinate c;
	memset(&c, 0, sizeof c);
	if (ghostty_tracked_grid_ref_point(ref, GHOSTTY_POINT_TAG_SCREEN, &c) != GHOSTTY_SUCCESS) return -1;
	return (int64_t)c.y;
}

// What the history side reads after every piece and every resize, in one
// call. Every field is written; a value the library did not report is 0 or
// -1.
typedef struct {
	int alt;            // the alternate screen is active
	int64_t anchor;     // dt_where of the anchor
	int64_t sentinel;   // dt_where of the sentinel
	uint64_t hist;      // history rows of the active screen
} dt_hist_t;

static void dt_hist_read(GhosttyTerminal t, GhosttyTrackedGridRef a, GhosttyTrackedGridRef s, dt_hist_t *o) {
	memset(o, 0, sizeof *o);
	GhosttyTerminalScreen scr = GHOSTTY_TERMINAL_SCREEN_PRIMARY;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN, &scr) == GHOSTTY_SUCCESS)
		o->alt = scr == GHOSTTY_TERMINAL_SCREEN_ALTERNATE;
	o->anchor = dt_where(a);
	o->sentinel = dt_where(s);
	size_t n = 0;
	if (ghostty_terminal_get(t, GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS, &n) == GHOSTTY_SUCCESS) o->hist = n;
}

// dt_wrapped reports whether row y of the screen (history first) is
// soft-wrapped: 1 wrapped, 0 not, -1 no such row.
static int dt_wrapped(GhosttyTerminal t, uint32_t y) {
	GhosttyPoint p;
	memset(&p, 0, sizeof p);
	p.tag = GHOSTTY_POINT_TAG_SCREEN;
	p.value.coordinate.y = y;
	GhosttyGridRef ref;
	memset(&ref, 0, sizeof ref);
	if (ghostty_terminal_grid_ref(t, p, &ref) != GHOSTTY_SUCCESS) return -1;
	GhosttyRow row = 0;
	if (ghostty_grid_ref_row(&ref, &row) != GHOSTTY_SUCCESS) return -1;
	bool w = false;
	if (ghostty_row_get(row, GHOSTTY_ROW_DATA_WRAP, &w) != GHOSTTY_SUCCESS) return 0;
	return w ? 1 : 0;
}

// dt_text reads the plain text of row y of the screen (history first) into
// buf as UTF-8: a cell without text is a space, the second half of a wide
// character and the spacer of one that wrapped are left out, a cluster the
// library cannot hand over in 16 code points is U+FFFD. It returns the
// number of bytes, -1 when buf was too small (cap/4/16 cells always fit),
// -2 when the row does not exist.
static int dt_text(GhosttyTerminal t, uint32_t y, int cols, uint8_t *buf, int cap) {
	int n = 0;
	uint32_t cp[16];
	for (int x = 0; x < cols; x++) {
		GhosttyPoint p;
		memset(&p, 0, sizeof p);
		p.tag = GHOSTTY_POINT_TAG_SCREEN;
		p.value.coordinate.x = (uint16_t)x;
		p.value.coordinate.y = y;
		GhosttyGridRef ref;
		memset(&ref, 0, sizeof ref);
		if (ghostty_terminal_grid_ref(t, p, &ref) != GHOSTTY_SUCCESS) return x == 0 ? -2 : n;
		GhosttyCell raw = 0;
		if (ghostty_grid_ref_cell(&ref, &raw) != GHOSTTY_SUCCESS) raw = 0;
		GhosttyCellWide wide = GHOSTTY_CELL_WIDE_NARROW;
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_WIDE, &wide) != GHOSTTY_SUCCESS) wide = GHOSTTY_CELL_WIDE_NARROW;
		if (wide == GHOSTTY_CELL_WIDE_SPACER_TAIL || wide == GHOSTTY_CELL_WIDE_SPACER_HEAD) continue;
		size_t k = 0;
		GhosttyResult r = ghostty_grid_ref_graphemes(&ref, cp, 16, &k);
		if (r == GHOSTTY_OUT_OF_SPACE) {
			cp[0] = 0xFFFD;
			k = 1;
		} else if (r != GHOSTTY_SUCCESS || k == 0) {
			cp[0] = ' ';
			k = 1;
		}
		if (n + 4 * (int)k > cap) return -1;
		for (size_t i = 0; i < k; i++) {
			uint32_t c = cp[i];
			if (c > 0x10FFFF || (c >= 0xD800 && c <= 0xDFFF)) c = 0xFFFD;
			if (c < 0x80) {
				buf[n++] = (uint8_t)c;
			} else if (c < 0x800) {
				buf[n++] = (uint8_t)(0xC0 | (c >> 6));
				buf[n++] = (uint8_t)(0x80 | (c & 0x3F));
			} else if (c < 0x10000) {
				buf[n++] = (uint8_t)(0xE0 | (c >> 12));
				buf[n++] = (uint8_t)(0x80 | ((c >> 6) & 0x3F));
				buf[n++] = (uint8_t)(0x80 | (c & 0x3F));
			} else {
				buf[n++] = (uint8_t)(0xF0 | (c >> 18));
				buf[n++] = (uint8_t)(0x80 | ((c >> 12) & 0x3F));
				buf[n++] = (uint8_t)(0x80 | ((c >> 6) & 0x3F));
				buf[n++] = (uint8_t)(0x80 | (c & 0x3F));
			}
		}
	}
	return n;
}
*/
import "C"

import (
	"strings"
	"unsafe"
)

// historyState keeps the absolute line numbers of one terminal and the lines
// it follows (3.2.2, 5.3.2).
//
// The library has no line number that survives trimming. The adapter holds
// one tracked reference, the anchor, on column 0 of the top row of the live
// screen, and knows its number (anchorAbs). After every piece of a Write and
// after every Resize it reads where the anchor is in screen coordinates
// (row 0 is the oldest row the library holds): the oldest row then has the
// number anchorAbs minus that row, and the top row of the live screen,
// base, that number plus the history rows the library holds. Range cuts
// the history to Options.History lines. Then the anchor is planted again on
// the top row of the live screen, so it is never older than what one piece
// scrolled.
//
// A second tracked reference, the sentinel, stands on the newest history
// row, the row just above the anchor. Output can move both only together,
// by scrolling and by trimming the oldest rows; only an erase of the history
// (ESC [ 3 J) takes the sentinel and leaves the anchor, which the library
// keeps on the live screen. A sentinel without a value, or one that is no
// longer the row above the anchor, after a piece is therefore treated as the
// loss of the anchor: what a viewer looked at no longer exists (5.3.2,
// "what a viewer loses is its scroll position at the moment a program
// resets the terminal or erases the history"). No byte sequence is looked
// for in the stream.
type historyState struct {
	anchor    C.GhosttyTrackedGridRef
	sentinel  C.GhosttyTrackedGridRef
	anchorAbs int64 // the number of the anchor's row
	hasSent   bool  // the sentinel was planted at the last renumbering

	first, base int64
	rows        int // the screen height the last numbers were handed out with

	// A resize while the alternate screen is active reflows the primary
	// screen too; the library reads only the active screen, so the numbers
	// are taken again when the primary screen returns.
	pendingReflow bool

	// follows holds, per followed line, one entry per Follow: a tracked
	// reference, or nil for a Follow that could not plant one (a line
	// outside Range, a history line while the alternate screen is active).
	// A nil entry is counted and answers not ok.
	follows map[int64][]C.GhosttyTrackedGridRef

	textBuf []byte // reused by Text
}

// init plants the anchor on the top row of the live screen, line 0.
func (s *historyState) init(t *term) {
	s.follows = map[int64][]C.GhosttyTrackedGridRef{}
	s.rows = t.rows
	if t.h == nil {
		return
	}
	s.plant(t, 0)
}

// restore is init for a terminal restored from a snapshot (Restore): the
// numbers continue at first and base. The anchor is planted when the
// primary screen is active; on the alternate screen it cannot be (the
// library places points on the active screen only), and it stays without a
// value: the first sync that sees the primary screen again finds no anchor
// and renumbers the history above every number handed out (lose), telling
// OnReflow that no followed line can be placed. A number a client held is
// then never silently wrong.
func (s *historyState) restore(t *term, first, base int64) {
	s.follows = map[int64][]C.GhosttyTrackedGridRef{}
	s.rows = t.rows
	s.first, s.base = first, base
	s.anchorAbs = base
	if t.h == nil || t.altActive() {
		return
	}
	s.plant(t, base)
}

// plant puts the anchor on the top row of the live screen with the number
// abs and the sentinel on the newest history row, if there is one. The
// primary screen must be active.
func (s *historyState) plant(t *term, abs int64) {
	s.anchorAbs = abs
	C.dt_track(t.h, &s.anchor, C.GHOSTTY_POINT_TAG_ACTIVE, 0)
	s.hasSent = false
	if hist := t.scrollbackRows(); hist > 0 {
		s.hasSent = C.dt_track(t.h, &s.sentinel, C.GHOSTTY_POINT_TAG_SCREEN, C.uint32_t(hist-1)) == 0
	}
}

// afterWrite runs after every piece of a Write: reads the anchor, replants
// it, handles its loss.
func (s *historyState) afterWrite(t *term) { s.sync(t, false) }

// beforeResize runs before the library's resize. The followed lines need
// nothing noted: their tracked references move with their text, and
// afterResize reads where each one went.
func (s *historyState) beforeResize(t *term) {}

// afterResize runs after the library's resize; it calls o.OnReflow when a
// followed line moved.
func (s *historyState) afterResize(t *term) { s.sync(t, true) }

// sync takes the numbers again after a piece of output (resized false) or a
// resize.
func (s *historyState) sync(t *term, resized bool) {
	if t.h == nil {
		return
	}
	r := t.histRead()
	if r.alt {
		// The alternate screen feeds no history and the primary screen does
		// not change under it, except by a resize. The anchor stays on the
		// primary screen (planting now would move it to the alternate one),
		// and the numbers stay as they are until the primary screen returns.
		s.pendingReflow = s.pendingReflow || resized
		return
	}
	reflow := resized || s.pendingReflow
	s.pendingReflow = false
	pos := r.anchor
	if pos < 0 || (!reflow && s.hasSent && (r.sentinel < 0 || r.sentinel != pos-1)) {
		s.lose(t, r.hist)
		return
	}
	hist := r.hist
	off := s.anchorAbs - pos // the number of the oldest row the library holds
	base := off + hist
	first := max(off, base-int64(t.history))
	// A reflow never lowers first. A narrowing reflow adds rows above the
	// anchor, which would number the oldest rows below first (in E10 item 2
	// first fell to -1079, and -1 is "gone" on the wire); the numbering then
	// moves up by the difference, as E10's binding fix does, so that no
	// retained row is cut off, and the followed lines are reported at their
	// new numbers below. base may go back in a resize: a taller screen takes
	// rows from the history, and they keep their numbers.
	shift := max(0, s.first-first)
	off, base, first = off+shift, base+shift, first+shift
	s.first, s.base, s.rows = first, base, t.rows
	s.plant(t, base)
	if (reflow || shift > 0) && len(s.follows) > 0 {
		s.remap(t, off)
	}
}

// lose is the anchor-loss rule of 5.3.2: first goes above every number ever
// handed out, base follows with the history the library holds now (at most
// History), a new anchor is planted, every followed line ends and OnReflow
// hears that no line can be placed.
func (s *historyState) lose(t *term, hist int64) {
	s.first = s.base + int64(max(s.rows, t.rows))
	s.base = s.first + min(hist, int64(t.history))
	s.rows = t.rows
	s.dropFollows()
	s.plant(t, s.base)
	if t.o.OnReflow != nil {
		t.o.OnReflow(func(int64) (int64, bool) { return 0, false })
	}
}

// remap reads where every followed line is now, keeps the follows of the
// lines that can be placed under their new numbers, ends the others, and
// calls OnReflow when at least one followed line moved or ended. off is the
// number of the oldest row the library holds.
func (s *historyState) remap(t *term, off int64) {
	now := make(map[int64]int64, len(s.follows))
	next := make(map[int64][]C.GhosttyTrackedGridRef, len(s.follows))
	moved := false
	for abs, refs := range s.follows {
		n, ok := int64(0), false
		for _, r := range refs {
			if y := int64(C.dt_where(r)); y >= 0 {
				n, ok = off+y, true
				break
			}
		}
		// A line the library still holds but below first is outside Range
		// (E10 item 2): it ends like a trimmed one.
		if !ok || n < s.first {
			freeRefs(refs)
			moved = true
			continue
		}
		if n != abs {
			moved = true
		}
		now[abs] = n
		// Every Follow of the line is kept under the new number, so that the
		// count of Unfollow calls stays right. References that lost their
		// value are freed and counted as nil.
		for i, r := range refs {
			if r != nil && C.dt_where(r) < 0 {
				C.ghostty_tracked_grid_ref_free(r)
				refs[i] = nil
			}
		}
		next[n] = append(next[n], refs...)
	}
	s.follows = next
	if moved && t.o.OnReflow != nil {
		t.o.OnReflow(func(abs int64) (int64, bool) {
			n, ok := now[abs]
			return n, ok
		})
	}
}

func (s *historyState) dropFollows() {
	for _, refs := range s.follows {
		freeRefs(refs)
	}
	s.follows = map[int64][]C.GhosttyTrackedGridRef{}
}

func freeRefs(refs []C.GhosttyTrackedGridRef) {
	for _, r := range refs {
		if r != nil {
			C.ghostty_tracked_grid_ref_free(r)
		}
	}
}

// close runs in Close before the terminal is freed.
func (s *historyState) close(t *term) {
	s.dropFollows()
	if s.anchor != nil {
		C.ghostty_tracked_grid_ref_free(s.anchor)
		s.anchor = nil
	}
	if s.sentinel != nil {
		C.ghostty_tracked_grid_ref_free(s.sentinel)
		s.sentinel = nil
	}
	s.hasSent = false
}

// Range is the oldest retained line and the line in screen row 0. base -
// first is never more than History, and neither number ever decreases.
func (t *term) Range() (first, base int64) { return t.historyState.first, t.historyState.base }

// histRow is the row of the library's screen (history first) that holds the
// absolute line abs; false outside Range and below the screen. While the
// alternate screen is active the library reads only that screen, so only
// the lines of the live screen are there.
func (t *term) histRow(abs int64) (int, bool) {
	s := &t.historyState
	if t.h == nil || abs < s.first || abs >= s.base+int64(t.rows) {
		return 0, false
	}
	if t.altActive() && abs < s.base {
		return 0, false
	}
	return int(abs - s.base + int64(t.scrollbackRows())), true
}

// histSample is what the history side reads after every piece and every
// resize: whether the alternate screen is active, the screen rows of the
// anchor and the sentinel (-1 without a value) and the history rows of the
// active screen.
type histSample struct {
	alt                    bool
	anchor, sentinel, hist int64
}

// histRead reads a histSample in one cgo call.
func (t *term) histRead() histSample {
	var r C.dt_hist_t
	C.dt_hist_read(t.h, t.historyState.anchor, t.historyState.sentinel, &r)
	return histSample{alt: r.alt != 0, anchor: int64(r.anchor), sentinel: int64(r.sentinel), hist: int64(r.hist)}
}

// altActive reports whether the alternate screen is the active one.
func (t *term) altActive() bool {
	if t.h == nil {
		return false
	}
	var r C.dt_hist_t
	C.dt_hist_read(t.h, nil, nil, &r)
	return r.alt != 0
}

// Line is the wire row (4.2.5) of an absolute line, history or screen.
func (t *term) Line(abs int64) (row string, ok bool) {
	y, ok := t.histRow(abs)
	if !ok {
		return "", false
	}
	return t.screenRow(y)
}

// Text is the plain text of an absolute line: one character per cell, a
// blank cell as a space, a wide character once. wrapped is true when the
// line continues on the next one (soft wrap); the text of a line that does
// not continue has no trailing blanks, the text of one that does keeps them,
// because they are part of the joined line.
func (t *term) Text(abs int64) (text string, wrapped, ok bool) {
	y, ok := t.histRow(abs)
	if !ok {
		return "", false, false
	}
	w := C.dt_wrapped(t.h, C.uint32_t(y))
	if w < 0 {
		return "", false, false
	}
	s := &t.historyState
	if need := t.cols * 16 * 4; len(s.textBuf) < need {
		s.textBuf = make([]byte, need)
	}
	n := C.dt_text(t.h, C.uint32_t(y), C.int(t.cols), (*C.uint8_t)(unsafe.Pointer(&s.textBuf[0])), C.int(len(s.textBuf)))
	if n < 0 {
		return "", false, false
	}
	text = string(s.textBuf[:n])
	if w == 0 {
		text = strings.TrimRight(text, " ")
	}
	return text, w == 1, true
}

// Follow pins a line to its text, so that OnReflow can say where it went.
// Each Follow needs one Unfollow. A line outside Range, or a history line
// while the alternate screen is active, is counted but cannot be placed
// after a reflow.
func (t *term) Follow(abs int64) {
	s := &t.historyState
	if s.follows == nil {
		s.follows = map[int64][]C.GhosttyTrackedGridRef{}
	}
	var ref C.GhosttyTrackedGridRef
	if y, ok := t.histRow(abs); ok && !t.altActive() {
		if C.dt_track(t.h, &ref, C.GHOSTTY_POINT_TAG_SCREEN, C.uint32_t(y)) != 0 {
			ref = nil
		}
	}
	s.follows[abs] = append(s.follows[abs], ref)
}

// Unfollow ends one Follow of a line. A line that is not followed, or no
// longer (it ended in a reflow or with the anchor), is ignored.
func (t *term) Unfollow(abs int64) {
	s := &t.historyState
	refs := s.follows[abs]
	if len(refs) == 0 {
		return
	}
	if r := refs[len(refs)-1]; r != nil {
		C.ghostty_tracked_grid_ref_free(r)
	}
	if len(refs) == 1 {
		delete(s.follows, abs)
		return
	}
	s.follows[abs] = refs[:len(refs)-1]
}
