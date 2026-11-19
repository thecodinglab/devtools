//go:build cgo

package vt

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <ghostty/vt.h>

// One cell as the row encoder needs it. text and link are byte ranges of
// the arena the caller passes; a length of 0 means no text or no link.
typedef struct {
	uint32_t text_off, text_len;
	uint32_t link_off, link_len;
	uint16_t flags;                 // GhosttyStyleFlags, underline style included
	uint8_t wide;                   // GhosttyCellWide
	uint8_t fg_tag, bg_tag, ul_tag; // GhosttyStyleColorTag
	uint8_t fg[3], bg[3], ul[3];    // a palette index in [0], or r, g, b
	uint8_t bad;                    // the text could not be read: write U+FFFD
} dt_cell;

static void dt_color(uint8_t *tag, uint8_t *v, const GhosttyStyleColor *c) {
	*tag = c->tag;
	if (c->tag == GHOSTTY_STYLE_COLOR_PALETTE) {
		v[0] = c->value.palette;
	} else if (c->tag == GHOSTTY_STYLE_COLOR_RGB) {
		v[0] = c->value.rgb.r;
		v[1] = c->value.rgb.g;
		v[2] = c->value.rgb.b;
	}
}

// dt_style fills the style of a cell from its style and its raw value: a
// cell without text can carry a background colour as its content (an
// erase with a coloured background), which wins over the style's.
static void dt_style(dt_cell *o, const GhosttyStyle *st, GhosttyCell raw) {
	o->flags = st->flags;
	dt_color(&o->fg_tag, o->fg, &st->fg_color);
	dt_color(&o->bg_tag, o->bg, &st->bg_color);
	dt_color(&o->ul_tag, o->ul, &st->underline_color);
	GhosttyCellContentTag ct = GHOSTTY_CELL_CONTENT_CODEPOINT;
	if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_CONTENT_TAG, &ct) != GHOSTTY_SUCCESS) return;
	if (ct == GHOSTTY_CELL_CONTENT_BG_COLOR_PALETTE) {
		GhosttyColorPaletteIndex i = 0;
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_COLOR_PALETTE, &i) == GHOSTTY_SUCCESS) {
			o->bg_tag = GHOSTTY_STYLE_COLOR_PALETTE;
			o->bg[0] = i;
		}
	} else if (ct == GHOSTTY_CELL_CONTENT_BG_COLOR_RGB) {
		GhosttyColorRgb c;
		memset(&c, 0, sizeof c);
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_COLOR_RGB, &c) == GHOSTTY_SUCCESS) {
			o->bg_tag = GHOSTTY_STYLE_COLOR_RGB;
			o->bg[0] = c.r;
			o->bg[1] = c.g;
			o->bg[2] = c.b;
		}
	}
}

// The arena of one row. used grows with every text and link; need counts
// what did not fit, so the caller can retry with a larger arena.
typedef struct {
	uint8_t *buf;
	size_t cap, used, need;
	uint32_t last_off, last_len; // the previous link, for sharing
} dt_arena;

// dt_link reads the hyperlink of the cell at a point into the arena. A link
// equal to the previous cell's shares its bytes.
static void dt_link(dt_arena *a, GhosttyTerminal t, GhosttyPoint p, dt_cell *o) {
	GhosttyGridRef ref;
	memset(&ref, 0, sizeof ref);
	if (ghostty_terminal_grid_ref(t, p, &ref) != GHOSTTY_SUCCESS) return;
	size_t room = a->cap - a->used, n = 0;
	GhosttyResult r = ghostty_grid_ref_hyperlink_uri(&ref, a->buf + a->used, room, &n);
	if (r == GHOSTTY_OUT_OF_SPACE) {
		a->need += n;
		return;
	}
	if (r != GHOSTTY_SUCCESS || n == 0) return;
	if (a->last_len == n && memcmp(a->buf + a->last_off, a->buf + a->used, n) == 0) {
		o->link_off = a->last_off;
		o->link_len = a->last_len;
		return;
	}
	o->link_off = (uint32_t)a->used;
	o->link_len = (uint32_t)n;
	a->last_off = o->link_off;
	a->last_len = o->link_len;
	a->used += n;
}

// dt_utf8 encodes code points into the arena.
static void dt_utf8(dt_arena *a, const uint32_t *cp, size_t n, dt_cell *o) {
	size_t need = 0;
	for (size_t i = 0; i < n; i++) need += cp[i] < 0x80 ? 1 : cp[i] < 0x800 ? 2 : cp[i] < 0x10000 ? 3 : 4;
	if (need > a->cap - a->used) {
		a->need += need;
		return;
	}
	uint8_t *p = a->buf + a->used;
	o->text_off = (uint32_t)a->used;
	for (size_t i = 0; i < n; i++) {
		uint32_t c = cp[i];
		if (c < 0x80) {
			*p++ = (uint8_t)c;
		} else if (c < 0x800) {
			*p++ = (uint8_t)(0xC0 | (c >> 6));
			*p++ = (uint8_t)(0x80 | (c & 0x3F));
		} else if (c < 0x10000) {
			*p++ = (uint8_t)(0xE0 | (c >> 12));
			*p++ = (uint8_t)(0x80 | ((c >> 6) & 0x3F));
			*p++ = (uint8_t)(0x80 | (c & 0x3F));
		} else {
			*p++ = (uint8_t)(0xF0 | (c >> 18));
			*p++ = (uint8_t)(0x80 | ((c >> 12) & 0x3F));
			*p++ = (uint8_t)(0x80 | ((c >> 6) & 0x3F));
			*p++ = (uint8_t)(0x80 | (c & 0x3F));
		}
	}
	o->text_len = (uint32_t)need;
	a->used += need;
}

// dt_row_cells reads row y of the live screen from the render state, every
// cell in one call. It returns the number of cells; -1 when the arena was
// too small (*need is then the size that fits); -2 when the row does not
// exist. Every cell is zeroed before it is filled, and a value is read only
// when the library reported one (5.3.2, binding rules).
static int dt_row_cells(GhosttyTerminal t, GhosttyRenderState rs, GhosttyRenderStateRowIterator it,
	int *pos, GhosttyRenderStateRowCells cells, uint16_t y, dt_cell *out, int max,
	uint8_t *arena, size_t cap, size_t *need) {
	*need = 0;
	// *pos is the row the iterator stands on, -1 before its first row. Rows
	// read in order walk the iterator once per frame.
	if (*pos < 0 || *pos > (int)y) {
		*pos = -1;
		if (ghostty_render_state_get(rs, GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR, &it) != GHOSTTY_SUCCESS) return -2;
	}
	while (*pos < (int)y) {
		if (!ghostty_render_state_row_iterator_next(it)) {
			*pos = -2; // past the end: start again next time
			return -2;
		}
		(*pos)++;
	}
	if (ghostty_render_state_row_get(it, GHOSTTY_RENDER_STATE_ROW_DATA_CELLS, &cells) != GHOSTTY_SUCCESS) return -2;
	dt_arena a = { arena, cap, 0, 0, 0, 0 };
	int n = 0;
	while (n < max && ghostty_render_state_row_cells_next(cells)) {
		dt_cell *o = &out[n];
		memset(o, 0, sizeof *o);
		GhosttyCell raw = 0;
		ghostty_render_state_row_cells_get(cells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_RAW, &raw);
		GhosttyCellWide wide = GHOSTTY_CELL_WIDE_NARROW;
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_WIDE, &wide) == GHOSTTY_SUCCESS) o->wide = (uint8_t)wide;
		GhosttyStyle st;
		ghostty_style_default(&st);
		if (ghostty_render_state_row_cells_get(cells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_STYLE, &st) != GHOSTTY_SUCCESS)
			ghostty_style_default(&st);
		dt_style(o, &st, raw);
		GhosttyBuffer b = { a.buf + a.used, a.cap - a.used, 0 };
		GhosttyResult r = ghostty_render_state_row_cells_get(cells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_UTF8, &b);
		if (r == GHOSTTY_SUCCESS) {
			o->text_off = (uint32_t)a.used;
			o->text_len = (uint32_t)b.len;
			a.used += b.len;
		} else if (r == GHOSTTY_OUT_OF_SPACE) {
			a.need += b.len;
		}
		bool link = false;
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_HAS_HYPERLINK, &link) == GHOSTTY_SUCCESS && link) {
			GhosttyPoint p;
			memset(&p, 0, sizeof p);
			p.tag = GHOSTTY_POINT_TAG_ACTIVE;
			p.value.coordinate.x = (uint16_t)n;
			p.value.coordinate.y = y;
			dt_link(&a, t, p, o);
		}
		n++;
	}
	if (a.need > 0) {
		*need = a.used + a.need;
		return -1;
	}
	return n;
}

// dt_point_cells reads row y of a point space (GHOSTTY_POINT_TAG_SCREEN: the
// whole retained screen, history first) through grid references, one cell
// at a time inside this one call. Slower than the render state, but it
// reaches history rows, which the render state does not. Same results.
static int dt_point_cells(GhosttyTerminal t, int tag, uint32_t y, int cols, dt_cell *out,
	uint8_t *arena, size_t cap, size_t *need) {
	*need = 0;
	dt_arena a = { arena, cap, 0, 0, 0, 0 };
	uint32_t stack[16];
	for (int x = 0; x < cols; x++) {
		dt_cell *o = &out[x];
		memset(o, 0, sizeof *o);
		GhosttyPoint p;
		memset(&p, 0, sizeof p);
		p.tag = (GhosttyPointTag)tag;
		p.value.coordinate.x = (uint16_t)x;
		p.value.coordinate.y = y;
		GhosttyGridRef ref;
		memset(&ref, 0, sizeof ref);
		if (ghostty_terminal_grid_ref(t, p, &ref) != GHOSTTY_SUCCESS) return x == 0 ? -2 : x;
		GhosttyCell raw = 0;
		if (ghostty_grid_ref_cell(&ref, &raw) != GHOSTTY_SUCCESS) raw = 0;
		GhosttyCellWide wide = GHOSTTY_CELL_WIDE_NARROW;
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_WIDE, &wide) == GHOSTTY_SUCCESS) o->wide = (uint8_t)wide;
		GhosttyStyle st;
		ghostty_style_default(&st);
		if (ghostty_grid_ref_style(&ref, &st) != GHOSTTY_SUCCESS) ghostty_style_default(&st);
		dt_style(o, &st, raw);
		size_t n = 0;
		GhosttyResult r = ghostty_grid_ref_graphemes(&ref, stack, 16, &n);
		if (r == GHOSTTY_SUCCESS) {
			dt_utf8(&a, stack, n, o);
		} else if (r == GHOSTTY_OUT_OF_SPACE) {
			// More than 16 code points in one cluster: read it again into a
			// buffer of the size the library named, so a history row hands
			// over the same text as the live row of the render state and the
			// encoder applies the same limits to both. Only when that fails
			// is the cell replaced.
			uint32_t *big = n > 0 ? malloc(n * sizeof *big) : NULL;
			size_t m = 0;
			if (big != NULL && ghostty_grid_ref_graphemes(&ref, big, n, &m) == GHOSTTY_SUCCESS) {
				dt_utf8(&a, big, m, o);
			} else {
				o->bad = 1;
			}
			free(big);
		}
		bool link = false;
		if (ghostty_cell_get(raw, GHOSTTY_CELL_DATA_HAS_HYPERLINK, &link) == GHOSTTY_SUCCESS && link) dt_link(&a, t, p, o);
	}
	if (a.need > 0) {
		*need = a.used + a.need;
		return -1;
	}
	return cols;
}
*/
import "C"

import (
	"unicode/utf8"
	"unsafe"

	"devtools/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

// Style flags of the library (style.h) that the row grammar knows.
const (
	flagBold      = 1 << 0
	flagItalic    = 1 << 1
	flagFaint     = 1 << 2
	flagBlink     = 1 << 3
	flagInverse   = 1 << 4
	flagInvisible = 1 << 5
	flagStrike    = 1 << 6
	// bit 7 is overline, which the grammar of 4.2.5 has no attribute for.
	underlineShift = 8
	underlineMask  = 0x7 << underlineShift
	knownFlags     = flagBold | flagItalic | flagFaint | flagBlink | flagInverse | flagInvisible | flagStrike | underlineMask
)

// The cell widths of the library (screen.h).
const (
	wideNarrow = iota
	wideWide
	wideSpacerTail
	wideSpacerHead
)

// Colour kinds of the library (style.h).
const (
	colorNone = iota
	colorPalette
	colorRGB
)

// color is one colour of a style: kind colorNone, a palette index in v[0],
// or RGB.
type color struct {
	kind uint8
	v    [3]uint8
}

// style is the style of a cell as the row grammar can say it.
type style struct {
	flags      uint16
	fg, bg, ul color
}

// cell is one cell read from the library; text and link are views of the
// row's arena, empty for none. Its style is normal: only what the grammar
// can say.
type cell struct {
	text, link []byte
	wide       uint8
	bad        bool // the library could not hand over the text: U+FFFD
	st         style
}

// Limits of the encoder.
const (
	maxURI     = 2048 // 4.2.5
	maxCluster = 256  // longer clusters are replaced; no character needs them
	// plainCluster is the cluster limit of the second try, when a row is
	// longer than proto.MaxRow with its links and clusters as they are.
	plainCluster = 32
	// arenaPerCell is the arena a row starts with per cell; a row that
	// needs more is read again with what it needs.
	arenaPerCell = 16
)

// rowBuf holds the buffers Row reuses from call to call.
type rowBuf struct {
	raw    []C.dt_cell
	arena  []byte
	parsed []cell
	enc    encoder
}

// Row is the wire row (4.2.5) of live screen row y; "" outside the screen.
func (t *term) Row(y int) string {
	if t.h == nil || y < 0 || y >= t.rows {
		return ""
	}
	cells, ok := t.liveCells(y)
	if !ok {
		return ""
	}
	return t.rb.enc.encode(cells)
}

// liveCells reads the cells of live screen row y with one cgo call. The
// result is valid until the next read of a row.
func (t *term) liveCells(y int) ([]cell, bool) {
	if t.h == nil || y < 0 || y >= t.rows {
		return nil, false
	}
	t.render()
	return t.readRow(func(raw *C.dt_cell, arena *C.uint8_t, size C.size_t, need *C.size_t) C.int {
		return C.dt_row_cells(t.h, t.rs, t.it, &t.itPos, t.cells, C.uint16_t(y), raw, C.int(t.cols), arena, size, need)
	})
}

// screenRow is the wire row of row y of the whole retained screen (history
// first, the live screen at the end), read through grid references. It is
// for the history side (history.go), whose lines the render state does not
// reach; for a row of the live screen it equals Row.
func (t *term) screenRow(y int) (string, bool) {
	if t.h == nil || y < 0 {
		return "", false
	}
	cells, ok := t.readRow(func(raw *C.dt_cell, arena *C.uint8_t, size C.size_t, need *C.size_t) C.int {
		return C.dt_point_cells(t.h, C.GHOSTTY_POINT_TAG_SCREEN, C.uint32_t(y), C.int(t.cols), raw, arena, size, need)
	})
	if !ok {
		return "", false
	}
	return t.rb.enc.encode(cells), true
}

// readRow runs one C reader with the row buffers, grows the arena once when
// the reader says it was too small, and converts the cells.
func (t *term) readRow(read func(raw *C.dt_cell, arena *C.uint8_t, size C.size_t, need *C.size_t) C.int) ([]cell, bool) {
	if len(t.rb.raw) < t.cols {
		t.rb.raw = make([]C.dt_cell, t.cols)
	}
	if len(t.rb.arena) < t.cols*arenaPerCell {
		t.rb.arena = make([]byte, t.cols*arenaPerCell)
	}
	var need C.size_t
	n := read(&t.rb.raw[0], (*C.uint8_t)(unsafe.Pointer(&t.rb.arena[0])), C.size_t(len(t.rb.arena)), &need)
	if n == -1 {
		t.rb.arena = make([]byte, int(need)+t.cols*arenaPerCell)
		n = read(&t.rb.raw[0], (*C.uint8_t)(unsafe.Pointer(&t.rb.arena[0])), C.size_t(len(t.rb.arena)), &need)
	}
	if n < 0 {
		return nil, false
	}
	t.rb.parsed = t.rb.parsed[:0]
	for i := range int(n) {
		r := &t.rb.raw[i]
		c := cell{
			wide: uint8(r.wide),
			bad:  r.bad != 0,
			st: normal(style{
				flags: uint16(r.flags),
				fg:    color{uint8(r.fg_tag), [3]uint8{uint8(r.fg[0]), uint8(r.fg[1]), uint8(r.fg[2])}},
				bg:    color{uint8(r.bg_tag), [3]uint8{uint8(r.bg[0]), uint8(r.bg[1]), uint8(r.bg[2])}},
				ul:    color{uint8(r.ul_tag), [3]uint8{uint8(r.ul[0]), uint8(r.ul[1]), uint8(r.ul[2])}},
			}),
		}
		if r.text_len > 0 {
			c.text = t.rb.arena[r.text_off : r.text_off+r.text_len]
		}
		if r.link_len > 0 {
			c.link = t.rb.arena[r.link_off : r.link_off+r.link_len]
		}
		t.rb.parsed = append(t.rb.parsed, c)
	}
	return t.rb.parsed, true
}

// encoder turns the cells of one row into the strict grammar of 4.2.5. It
// is devtools' own: the library has no output in this grammar (3.2.2).
//
//   - Every SGR starts from reset and lists the attributes in the order of
//     the grammar; an SGR that changes nothing is never written. The row
//     starts in the default style and nothing carries over to the next row.
//   - Default colours are not written: a colour the program never named
//     stays the outer terminal's default.
//   - Trailing cells that are blank in the default style without a link are
//     omitted. A wide cell is written once and occupies two cells.
//   - A cell whose text the client would not read as exactly that one cell
//     becomes U+FFFD (one cell; a wide cell is U+FFFD and a space), so the
//     grid stays aligned (3.2.2, E9): a cluster that would join the
//     previous cell's when the row is parsed, a cluster that the client's
//     measure (x/ansi in grapheme mode, the measure of the row filter)
//     gives another width than the library did, and a cluster that is not
//     text of the grammar.
//   - A row never exceeds proto.MaxRow: a row that would is encoded again
//     without links and with long clusters replaced, and cut at a cell when
//     that is still too long.
type encoder struct {
	out  []byte
	join []byte // scratch for the join test
	ends []int  // end of the output after each cell, for the cut
}

func (e *encoder) encode(cells []cell) string {
	e.run(cells, false)
	if len(e.out) > proto.MaxRow {
		e.run(cells, true)
		if len(e.out) > proto.MaxRow {
			cut := 0
			for _, end := range e.ends {
				if end > proto.MaxRow {
					break
				}
				cut = end
			}
			e.out = e.out[:cut]
		}
	}
	return string(e.out)
}

// fffd is U+FFFD in UTF-8.
var fffd = []byte("\uFFFD")

func (e *encoder) run(cells []cell, plain bool) {
	e.out, e.ends = e.out[:0], e.ends[:0]
	last := len(cells) - 1
	for last >= 0 && blank(&cells[last], plain) {
		last--
	}
	var (
		cur  style
		link []byte
		prev []byte // the text written for the previous cell
	)
	limit := maxCluster
	if plain {
		limit = plainCluster
	}
	for i := 0; i <= last; i++ {
		c := &cells[i]
		if c.wide == wideSpacerTail {
			continue
		}
		width := 1
		if c.wide == wideWide {
			width = 2
		}
		st := c.st
		var l []byte
		if !plain && validURI(c.link) {
			l = c.link
		}
		text := c.text
		if len(text) == 0 || c.wide == wideSpacerHead {
			text = space
		}
		pad := false
		if c.bad || !fits(prev, text, width, limit, &e.join) {
			text, pad = fffd, width == 2
		}
		if st != cur {
			e.out = appendSGR(e.out, st)
			cur = st
		}
		if string(l) != string(link) {
			e.out = append(e.out, "\x1b]8;;"...)
			e.out = append(e.out, l...)
			e.out = append(e.out, "\x1b\\"...)
			link = l
		}
		e.out = append(e.out, text...)
		prev = text
		if pad {
			e.out = append(e.out, ' ')
			prev = space
		}
		e.ends = append(e.ends, len(e.out))
	}
}

var space = []byte(" ")

// blank reports whether a cell may be left out at the end of a row: no text
// (or a space), the default style, no link, not a wide character, and not a
// cell whose text could not be read (it is written as U+FFFD).
func blank(c *cell, plain bool) bool {
	if c.bad || c.wide == wideWide || c.st != (style{}) {
		return false
	}
	if !plain && validURI(c.link) {
		return false
	}
	return len(c.text) == 0 || string(c.text) == " " || c.wide == wideSpacerTail || c.wide == wideSpacerHead
}

// normal drops what the grammar cannot say: overline, and an underline
// colour without an underline.
func normal(s style) style {
	s.flags &= knownFlags
	if s.flags&underlineMask == 0 {
		s.ul = color{}
	}
	if u := (s.flags & underlineMask) >> underlineShift; u > 5 {
		s.flags = s.flags&^underlineMask | 1<<underlineShift
	}
	if s.fg.kind > colorRGB {
		s.fg = color{}
	}
	if s.bg.kind > colorRGB {
		s.bg = color{}
	}
	if s.ul.kind > colorRGB {
		s.ul = color{}
	}
	return s
}

// validURI reports whether a link can be written: 1 to 2048 bytes of
// %x21-7E.
func validURI(u []byte) bool {
	if len(u) == 0 || len(u) > maxURI {
		return false
	}
	for _, b := range u {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

// fits reports whether text can be written as one cell of the given width
// after prev: it is text of the grammar, at most limit bytes, one grapheme
// cluster of that width by the client's measure, it does not join
// prev's cluster, and nothing written after it can join it.
func fits(prev, text []byte, width, limit int, scratch *[]byte) bool {
	if len(text) == 1 {
		b := text[0]
		if b < 0x20 || b == 0x7f || width != 1 {
			return false
		}
		if b < 0x80 && (len(prev) == 0 || prev[len(prev)-1] < 0x80) {
			return true // ASCII after ASCII never joins
		}
	}
	if len(text) > limit || !utf8.Valid(text) {
		return false
	}
	for _, r := range string(text) {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	first, w := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
	if len(first) != len(text) || w != width {
		return false
	}
	// A cluster that would swallow what follows it (it ends in a Prepend
	// character, Unicode GB9b) is replaced itself: U+FFFD or a space
	// written after it would join it too.
	s := append(append((*scratch)[:0], text...), fffd...)
	*scratch = s
	if probe, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth); len(probe) != len(text) {
		return false
	}
	if len(prev) == 0 {
		return true
	}
	s = append(append((*scratch)[:0], prev...), text...)
	*scratch = s
	joined, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	return len(joined) == len(prev)
}

// appendSGR writes the SGR of a style: reset, then the attributes in the
// order of the grammar (4.2.5).
func appendSGR(b []byte, s style) []byte {
	b = append(b, "\x1b[0"...)
	for _, a := range [...]struct {
		flag uint16
		code string
	}{
		{flagBold, ";1"}, {flagFaint, ";2"}, {flagItalic, ";3"}, {flagBlink, ";5"},
		{flagInverse, ";7"}, {flagInvisible, ";8"}, {flagStrike, ";9"},
	} {
		if s.flags&a.flag != 0 {
			b = append(b, a.code...)
		}
	}
	switch (s.flags & underlineMask) >> underlineShift {
	case 1:
		b = append(b, ";4"...)
	case 2:
		b = append(b, ";4:2"...)
	case 3:
		b = append(b, ";4:3"...)
	case 4:
		b = append(b, ";4:4"...)
	case 5:
		b = append(b, ";4:5"...)
	}
	b = appendColor(b, s.fg, 30, 90, "38")
	b = appendColor(b, s.bg, 40, 100, "48")
	b = appendColor(b, s.ul, 0, 0, "58")
	return append(b, 'm')
}

// appendColor writes one colour: the sixteen named colours in their short
// form (base, bright) where the attribute has one, else the 256-colour or
// the RGB form.
func appendColor(b []byte, c color, base, bright int, ext string) []byte {
	switch c.kind {
	case colorPalette:
		i := int(c.v[0])
		switch {
		case base != 0 && i < 8:
			return appendInt(append(b, ';'), base+i)
		case base != 0 && i < 16:
			return appendInt(append(b, ';'), bright+i-8)
		}
		b = append(append(append(b, ';'), ext...), ";5;"...)
		return appendInt(b, i)
	case colorRGB:
		b = append(append(append(b, ';'), ext...), ";2;"...)
		b = appendInt(b, int(c.v[0]))
		b = appendInt(append(b, ';'), int(c.v[1]))
		return appendInt(append(b, ';'), int(c.v[2]))
	}
	return b
}

func appendInt(b []byte, n int) []byte {
	if n >= 100 {
		b = append(b, byte('0'+n/100))
	}
	if n >= 10 {
		b = append(b, byte('0'+n/10%10))
	}
	return append(b, byte('0'+n%10))
}
