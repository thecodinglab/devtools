//go:build cgo

package vt

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Extract returns the text between two positions, at most max bytes; cut is
// true when it was cut (4.2.7, pane.text). Both positions are included and
// may come in either order; lines outside Range are left out. Soft-wrapped
// lines are joined without a newline, trailing blanks of each line are
// removed, block takes the same column range of every line, and a
// soft-wrapped line is not joined then. Columns are cells: a wide character
// is taken when one of its two cells is inside the range. A max of 0 or less
// is no limit. The text is cut at a character, never inside one.
func Extract(t Term, from, to Pos, block bool, max int) (text string, cut bool) {
	if to.Line < from.Line || (to.Line == from.Line && to.Col < from.Col) {
		from, to = to, from
	}
	first, base := t.Range()
	_, rows := t.Size()
	lo, hi := from.Line, to.Line
	if lo < first {
		lo = first
	}
	if last := base + int64(rows) - 1; hi > last {
		hi = last
	}
	c0, c1 := from.Col, to.Col
	if block && c0 > c1 {
		c0, c1 = c1, c0
	}
	var (
		out  strings.Builder
		line []byte // the logical line being joined
	)
	// emit appends the logical line without its trailing blanks; false when
	// max was reached.
	emit := func(newline bool) bool {
		s := strings.TrimRight(string(line), " ")
		line = line[:0]
		if newline {
			s += "\n"
		}
		if max > 0 && out.Len()+len(s) > max {
			out.WriteString(cutText(s, max-out.Len()))
			return false
		}
		out.WriteString(s)
		return true
	}
	for l := lo; l <= hi; l++ {
		txt, wrapped, ok := t.Text(l)
		if !ok {
			continue
		}
		start, end := 0, int(^uint(0)>>1)
		switch {
		case block:
			start, end = c0, c1
			wrapped = false
		default:
			if l == from.Line {
				start = from.Col
			}
			if l == to.Line {
				end = to.Col
			}
		}
		line = append(line, textColumns(txt, start, end)...)
		if wrapped && l < hi {
			if max > 0 && out.Len()+len(line) > max {
				out.WriteString(cutText(string(line), max-out.Len()))
				return out.String(), true
			}
			continue
		}
		if !emit(l < hi) {
			return out.String(), true
		}
	}
	if len(line) > 0 && !emit(false) {
		return out.String(), true
	}
	return out.String(), false
}

// textColumns is the part of a line's text that lies in the cells start to end,
// both included. The cells are counted by the client's measure, the
// grapheme clusters of x/ansi with their width.
func textColumns(s string, start, end int) string {
	if start <= 0 && end >= len(s)*2 {
		return s
	}
	var b strings.Builder
	col := 0
	for s != "" && col <= end {
		c, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if c == "" {
			break
		}
		last := col + max(w, 1) - 1
		if last >= start && col <= end {
			b.WriteString(c)
		}
		col += w
		s = s[len(c):]
	}
	return b.String()
}

// cutText is the longest prefix of s of at most n bytes that ends at a
// character.
func cutText(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
