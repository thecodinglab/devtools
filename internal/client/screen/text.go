package screen

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Text and Styled turn strings into cells. Width is measured per grapheme
// cluster with x/ansi in grapheme mode, the width function of the row
// filter (4.2.5), so a cell drawn here and a cell parsed from a row agree.

// replacement is what a cluster that is not valid UTF-8 is drawn as.
const replacement = "�"

// Text draws plain text: control characters are dropped, the text is cut at
// a cell boundary, so a wide character that does not fit in maxW cells (or
// before the right edge) is not drawn. It returns the width drawn.
func (c *Canvas) Text(x, y int, s string, st Style, maxW int) int {
	return c.drawText(x, y, 0, s, st, c.avail(x, maxW))
}

// avail is the number of cells text starting at x may use.
func (c *Canvas) avail(x, maxW int) int {
	if x >= 0 {
		maxW = min(maxW, c.w-x)
	}
	return max(maxW, 0)
}

// drawText draws the clusters of s from column x+drawn on and returns the
// width drawn so far; it stops at avail.
func (c *Canvas) drawText(x, y, drawn int, s string, st Style, avail int) int {
	for len(s) > 0 && drawn < avail {
		cl, w := nextCluster(s)
		s = s[len(cl):]
		if isControl(cl) {
			continue
		}
		if !utf8.ValidString(cl) {
			cl, w = replacement, 1
		}
		if w <= 0 {
			// a combining mark or joiner with nothing to join: no cell
			continue
		}
		w = min(w, 2)
		if drawn+w > avail {
			return drawn
		}
		c.Set(x+drawn, y, Cell{S: cl, W: uint8(w), Style: st})
		drawn += w
	}
	return drawn
}

// nextCluster returns the first grapheme cluster of s and its width. A run
// of ASCII is split without the segmenter: two ASCII bytes never form one
// cluster, except CR LF, which is a control and dropped either way.
func nextCluster(s string) (string, int) {
	if b := s[0]; b < utf8.RuneSelf && (len(s) == 1 || s[1] < utf8.RuneSelf) {
		if b < 0x20 || b == 0x7f {
			return s[:1], 0
		}
		return s[:1], 1
	}
	cl, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	if cl == "" { // cannot happen for a non-empty s; never loop forever
		return s[:1], 0
	}
	return cl, w
}

// isControl reports whether a cluster is a control character: C0, DEL or
// C1. The segmenter always gives a control character a cluster of its own
// (CR LF is the one pair).
func isControl(cl string) bool {
	r, _ := utf8.DecodeRuneInString(cl)
	return isControlRune(r)
}

func isControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0)
}

// Styled draws one line of text with SGR (lipgloss output), read by a
// lenient SGR reader: every SGR a terminal knows is applied, relative ones
// included, starting from the default style; every other sequence (cursor
// movement, OSC, hyperlinks, DCS, a lone ESC) and every control character
// is dropped. It returns the width drawn.
func (c *Canvas) Styled(x, y int, s string, maxW int) int {
	avail := c.avail(x, maxW)
	var st Style
	drawn := 0
	for len(s) > 0 && drawn < avail {
		i := strings.IndexByte(s, 0x1b)
		if i < 0 {
			return c.drawText(x, y, drawn, s, st, avail)
		}
		if i > 0 {
			drawn = c.drawText(x, y, drawn, s[:i], st, avail)
		}
		var n int
		n, st = escape(s[i:], st)
		s = s[i+n:]
	}
	return drawn
}

// escape reads the escape sequence at the start of s (s[0] is ESC) and
// returns its length and the style after it. An SGR changes the style;
// anything else is skipped. A sequence cut short by the end of s or by a
// byte that cannot be in it ends there; that byte is read as text again.
func escape(s string, st Style) (int, Style) {
	if len(s) < 2 {
		return len(s), st
	}
	switch s[1] {
	case '[':
		i := 2
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
			i++
		}
		params := s[2:i]
		inter := i
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) {
			return len(s), st
		}
		if s[i] < 0x40 || s[i] > 0x7e {
			return i, st
		}
		if s[i] == 'm' && inter == i && plainParams(params) {
			st = applySGR(st, params)
		}
		return i + 1, st
	case ']', 'P', 'X', '^', '_':
		// a string: OSC ends with BEL or ST, the others with ST
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 && s[1] == ']' {
				return i + 1, st
			}
			if s[i] == 0x1b {
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2, st
				}
				return i, st // another sequence starts: this one was cut
			}
		}
		return len(s), st
	}
	i := 1
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		return i + 1, st
	}
	return i, st
}

// plainParams reports whether the parameters of a CSI hold only digits,
// ';' and ':', so that "ESC [ ? 4 m" or "ESC [ > 4 ; 2 m" (other
// sequences that end in m) are not read as SGR.
func plainParams(p string) bool {
	for i := 0; i < len(p); i++ {
		if b := p[i]; (b < '0' || b > '9') && b != ';' && b != ':' {
			return false
		}
	}
	return true
}

// applySGR applies the parameters of one SGR, the part between "ESC [" and
// "m", to st, as a terminal would. Unknown parameters are ignored.
func applySGR(st Style, params string) Style {
	if params == "" {
		return Style{Link: st.Link}
	}
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.IndexByte(f, ':') >= 0 {
			st = applySub(st, strings.Split(f, ":"))
			continue
		}
		n, ok := atoi(f)
		if !ok {
			continue
		}
		switch {
		case n == 0:
			st = Style{Link: st.Link}
		case n == 1:
			st.Attrs |= Bold
		case n == 2:
			st.Attrs |= Faint
		case n == 3:
			st.Attrs |= Italic
		case n == 4:
			st.Underline = 1
		case n == 5 || n == 6:
			st.Attrs |= Blink
		case n == 7:
			st.Attrs |= Reverse
		case n == 8:
			st.Attrs |= Conceal
		case n == 9:
			st.Attrs |= Strike
		case n == 21:
			st.Underline = 2
		case n == 22:
			st.Attrs &^= Bold | Faint
		case n == 23:
			st.Attrs &^= Italic
		case n == 24:
			st.Underline = 0
		case n == 25:
			st.Attrs &^= Blink
		case n == 27:
			st.Attrs &^= Reverse
		case n == 28:
			st.Attrs &^= Conceal
		case n == 29:
			st.Attrs &^= Strike
		case n >= 30 && n <= 37:
			st.Fg = Color{Kind: 1, V: uint32(n - 30)}
		case n == 39:
			st.Fg = Color{}
		case n >= 40 && n <= 47:
			st.Bg = Color{Kind: 1, V: uint32(n - 40)}
		case n == 49:
			st.Bg = Color{}
		case n == 59:
			st.Ul = Color{}
		case n >= 90 && n <= 97:
			st.Fg = Color{Kind: 1, V: uint32(n - 90 + 8)}
		case n >= 100 && n <= 107:
			st.Bg = Color{Kind: 1, V: uint32(n - 100 + 8)}
		case n == 38 || n == 48 || n == 58:
			col, used := extColor(fields[i+1:])
			i += used
			st = setColor(st, n, col)
		}
	}
	return st
}

// applySub applies one parameter with sub-parameters: "4:3", "38:5:n",
// "38:2:r:g:b" or "38:2:cs:r:g:b".
func applySub(st Style, subs []string) Style {
	n, ok := atoi(subs[0])
	if !ok {
		return st
	}
	switch n {
	case 4:
		if v, ok := atoi(subs[1]); ok && v <= 5 {
			st.Underline = uint8(v)
		}
	case 38, 48, 58:
		rest := subs[1:]
		if len(rest) == 5 && rest[0] == "2" { // 2:colour-space:r:g:b
			rest = append([]string{"2"}, rest[2:]...)
		}
		col, _ := extColor(rest)
		st = setColor(st, n, col)
	}
	return st
}

// extColor reads the colour after a 38, 48 or 58: "5;n" or "2;r;g;b". It
// returns the colour (Kind 0 when it is not one) and how many parameters it
// used.
func extColor(p []string) (Color, int) {
	if len(p) == 0 {
		return Color{}, 0
	}
	mode, _ := atoi(p[0])
	switch mode {
	case 5:
		if len(p) < 2 {
			return Color{}, len(p)
		}
		v, ok := atoi(p[1])
		if !ok || v > 255 {
			return Color{}, 2
		}
		return Color{Kind: 2, V: uint32(v)}, 2
	case 2:
		if len(p) < 4 {
			return Color{}, len(p)
		}
		r, ok1 := atoi(p[1])
		g, ok2 := atoi(p[2])
		b, ok3 := atoi(p[3])
		if !ok1 || !ok2 || !ok3 || r > 255 || g > 255 || b > 255 {
			return Color{}, 4
		}
		return Color{Kind: 3, V: uint32(r)<<16 | uint32(g)<<8 | uint32(b)}, 4
	}
	return Color{}, 1
}

func setColor(st Style, n int, col Color) Style {
	switch n {
	case 38:
		st.Fg = col
	case 48:
		st.Bg = col
	case 58:
		st.Ul = col
	}
	return st
}

// atoi reads a parameter of digits; an empty one is 0. Values above
// 65535 are kept at 65536, which no parameter accepts.
func atoi(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b < '0' || b > '9' {
			return 0, false
		}
		if n <= 65535 {
			n = n*10 + int(b-'0')
		}
	}
	return min(n, 65536), true
}
