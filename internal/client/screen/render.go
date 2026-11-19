package screen

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// The encoder side of the row grammar of 4.2.5. Every row starts in the
// default style; an SGR is written only where the style changes and always
// starts from reset ("ESC [ 0 ..."), with the attributes in the order of
// the grammar; a link is opened with "ESC ] 8 ; ; uri ESC \" and closed
// with an empty uri. Trailing blank cells in the default style are left
// out. A row that ends in a style other than the default ends with a reset
// and a link left open is closed, so nothing carries over into the next
// row of the outer terminal either.
//
// Cell text is never trusted: a cell whose text is not one grapheme
// cluster of valid UTF-8 without control characters, whose width is not
// the cell's width, or which would join the cluster before it when the row
// is read again (the rule of 3.2.2), is written as U+FFFD (and a space for
// the second cell of a wide cell), so the grid stays aligned and no control
// byte reaches the outer terminal from cell text.

// maxURI is the longest uri of a link the grammar allows.
const maxURI = 2048

// Render returns the rows joined by "\n" in the grammar of 4.2.5; unchanged
// rows are not re-encoded.
func (c *Canvas) Render() string { return strings.Join(c.Lines(), "\n") }

// Lines returns the same rows as Render, for tests.
func (c *Canvas) Lines() []string {
	if c.root {
		b := c.b
		out := make([]string, b.h)
		for y := range b.h {
			r := &b.rows[y]
			if r.dirty {
				r.enc = encodeRow(b.cells[y*b.w : (y+1)*b.w])
				r.dirty = false
				b.encodes++
			}
			out[y] = r.enc
		}
		return out
	}
	bd := c.bounds()
	out := make([]string, 0, bd[3]-bd[1])
	for gy := bd[1]; gy < bd[3]; gy++ {
		out = append(out, encodeRow(c.b.cells[gy*c.b.w+bd[0]:gy*c.b.w+bd[2]]))
	}
	return out
}

// encodeRow encodes one row of cells.
func encodeRow(cells []Cell) string {
	last := len(cells) - 1
	for last >= 0 && isBlank(cells[last]) {
		last--
	}
	if last < 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(last + 16)
	var (
		cur  Style // the SGR part of the style written last; Link unused
		link string
		prev string // the last cluster written, for the joining rule
	)
	for x := 0; x <= last; x++ {
		cell := cells[x]
		w := int(cell.W)
		if w == 0 {
			if x > 0 && cells[x-1].W == 2 {
				continue // the second cell of a wide character
			}
			cell = Cell{S: " ", W: 1, Style: cell.Style} // a second cell on its own
			w = 1
		}
		if w == 2 && x+1 >= len(cells) {
			// a Sub's window cuts the character: its half is a blank
			cell, w = Cell{S: " ", W: 1, Style: cell.Style}, 1
		}
		st := normalize(cell.Style)
		if l := st.Link; l != link {
			b.WriteString("\x1b]8;;")
			b.WriteString(l)
			b.WriteString("\x1b\\")
			link = l
		}
		st.Link = ""
		if st != cur {
			writeSGR(&b, st)
			cur = st
		}
		text, tail := cellText(cell.S, w, prev)
		b.WriteString(text)
		prev = tail
	}
	if cur != (Style{}) {
		b.WriteString("\x1b[0m")
	}
	if link != "" {
		b.WriteString("\x1b]8;;\x1b\\")
	}
	return b.String()
}

func isBlank(c Cell) bool { return c.W == 1 && c.S == " " && c.Style == (Style{}) }

// cellText returns what is written for a cell of width w with text s after
// the cluster prev, and the last cluster it writes.
func cellText(s string, w int, prev string) (text, tail string) {
	ascii := len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f
	if !(ascii && w == 1) && !validCluster(s, w) {
		return bad(w)
	}
	if prev != "" && (!ascii || !isASCII(prev)) && joins(prev, s) {
		return bad(w)
	}
	return s, s
}

// bad is the text of a cell that cannot be written as it is.
func bad(w int) (string, string) {
	if w == 2 {
		return replacement + " ", " "
	}
	return replacement, replacement
}

// validCluster reports whether s is one grapheme cluster of valid UTF-8
// without control characters that is w cells wide.
func validCluster(s string, w int) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if isControlRune(r) {
			return false
		}
	}
	cl, cw := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	return len(cl) == len(s) && cw == w
}

// joins reports whether the cluster cur, written right after the cluster
// prev, would be read as part of prev.
func joins(prev, cur string) bool {
	cl, _ := ansi.FirstGraphemeCluster(prev+cur, ansi.GraphemeWidth)
	return len(cl) > len(prev)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// normalize drops what the grammar cannot carry: a colour out of its range
// becomes the default, an underline style out of the list none, attribute
// bits outside the list are cleared, and a link that is not a uri of the
// grammar is no link.
func normalize(st Style) Style {
	st.Fg, st.Bg, st.Ul = normColor(st.Fg), normColor(st.Bg), normColor(st.Ul)
	st.Attrs &= Bold | Faint | Italic | Blink | Reverse | Conceal | Strike
	if st.Underline > 5 {
		st.Underline = 0
	}
	if !validURI(st.Link) {
		st.Link = ""
	}
	return st
}

func normColor(c Color) Color {
	switch c.Kind {
	case 1:
		if c.V < 16 {
			return c
		}
	case 2:
		if c.V < 256 {
			return c
		}
	case 3:
		return Color{Kind: 3, V: c.V & 0xffffff}
	}
	return Color{}
}

func validURI(s string) bool {
	if len(s) > maxURI {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

var underlineSGR = [...]string{"", ";4", ";4:2", ";4:3", ";4:4", ";4:5"}

// writeSGR writes the absolute SGR of st (normalized, without link).
func writeSGR(b *strings.Builder, st Style) {
	b.WriteString("\x1b[0")
	for _, a := range [...]struct {
		bit Attr
		p   string
	}{{Bold, ";1"}, {Faint, ";2"}, {Italic, ";3"}, {Blink, ";5"}, {Reverse, ";7"}, {Conceal, ";8"}, {Strike, ";9"}} {
		if st.Attrs&a.bit != 0 {
			b.WriteString(a.p)
		}
	}
	b.WriteString(underlineSGR[st.Underline])
	writeColor(b, st.Fg, 30, 90, 38)
	writeColor(b, st.Bg, 40, 100, 48)
	writeColor(b, st.Ul, 0, 0, 58)
	b.WriteByte('m')
}

// writeColor writes one colour: base+V or bright+V-8 for the 16 (the
// underline colour has no such form and uses ext;5;V), ext;5;V for the 256,
// ext;2;r;g;b for RGB.
func writeColor(b *strings.Builder, c Color, base, bright, ext int) {
	switch c.Kind {
	case 1:
		switch {
		case base == 0:
			writeExt(b, ext, 5, c.V)
		case c.V < 8:
			b.WriteByte(';')
			b.WriteString(strconv.Itoa(base + int(c.V)))
		default:
			b.WriteByte(';')
			b.WriteString(strconv.Itoa(bright + int(c.V) - 8))
		}
	case 2:
		writeExt(b, ext, 5, c.V)
	case 3:
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(ext))
		b.WriteString(";2;")
		b.WriteString(strconv.Itoa(int(c.V >> 16 & 0xff)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(int(c.V >> 8 & 0xff)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(int(c.V & 0xff)))
	}
}

func writeExt(b *strings.Builder, ext, mode int, v uint32) {
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(ext))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(mode))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(int(v)))
}
