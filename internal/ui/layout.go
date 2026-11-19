package ui

// Width, cutting, numbers and centring (5.3.9, 5a.15.2): what the chrome
// and the hub measure and cut text with.

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Width is the number of cells s takes, by the canvas's width function:
// grapheme clusters with the widths of mode 2027 (ansi.StringWidth of the
// pinned x/ansi in grapheme mode, 4.2.5). Escape sequences take no cells.
func Width(s string) int { return ansi.StringWidth(s) }

// Number writes n with a comma between groups of three digits: "7,412".
func Number(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if digits[0] == '-' {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	b.Grow(len(sign) + len(digits) + len(digits)/3)
	b.WriteString(sign)
	for i := range len(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(digits[i])
	}
	return b.String()
}

// Measure is the width of the hub's column: min(width, 100), never below 0.
func Measure(width int) int { return max(0, min(width, 100)) }

// Centre is the first column x0 of a column of measure cells centred in
// width; the odd cell goes to the right.
func Centre(width, measure int) int { return max(0, (width-measure)/2) }

// cluster is one grapheme cluster and the cells it takes.
type cluster struct {
	s string
	w int
}

// clusters splits plain text into grapheme clusters with the canvas's
// widths.
func clusters(s string) []cluster {
	var out []cluster
	for len(s) > 0 {
		c, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if c == "" {
			break
		}
		out = append(out, cluster{c, w})
		s = s[len(c):]
	}
	return out
}

// mark is the theme's ellipsis if at least one cell of text stays beside it
// in w cells, else "": a cut that has no room for its mark is a plain cut.
func (t Theme) mark(w int) string {
	m := t.Glyph(GlyphEllipsis)
	if Width(m) >= w {
		return ""
	}
	return m
}

// CutEnd cuts s to at most w cells, the cut marked at the end with the
// theme's ellipsis (… or ...). It cuts between grapheme clusters, so a wide
// character or a letter with its combining marks is kept whole or left out
// whole; the result is then one cell short of w. Text that fits is returned
// unchanged, w of 0 or less gives "". When w has no room for the mark and
// one cell of text, the text is cut without a mark. Escape sequences in s
// are kept and take no cells.
func (t Theme) CutEnd(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, t.mark(w))
}

// CutMiddle cuts s to at most w cells by taking whole grapheme clusters out
// of its middle and putting the theme's ellipsis in their place: the head
// keeps the larger half of the room. It is meant for plain text such as
// session labels; escape sequences are dropped from a string that has to be
// cut. When w is too small for a head, the mark and a tail, the text is
// cut at the end instead.
func (t Theme) CutMiddle(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	m := t.mark(w)
	room := w - Width(m)
	if m == "" || room < 2 {
		return t.CutEnd(s, w)
	}
	cs := clusters(ansi.Strip(s))
	headRoom := (room + 1) / 2
	var head strings.Builder
	used, i := 0, 0
	for ; i < len(cs) && used+cs[i].w <= headRoom; i++ {
		head.WriteString(cs[i].s)
		used += cs[i].w
	}
	// What the head could not use, because a wide cluster did not fit, goes
	// to the tail.
	tailRoom := room - used
	j := len(cs)
	tailW := 0
	for j > i && tailW+cs[j-1].w <= tailRoom {
		j--
		tailW += cs[j].w
	}
	var b strings.Builder
	b.WriteString(head.String())
	b.WriteString(m)
	for _, c := range cs[j:] {
		b.WriteString(c.s)
	}
	return b.String()
}
