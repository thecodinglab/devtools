package golden

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// A run line of Runs is
//
//	row col width attributes fg [on bg] [ul colour]
//
// for example "23 0 8 bold reverse" or "3 0 80 bold #e6e7eb on #30323a".
// Attributes come in the fixed order bold, faint, italic, underline (or
// double-underline, curly-underline, dotted-underline, dashed-underline),
// blink, reverse, conceal, strike: the four the look uses (bold, italic,
// underline, reverse) keep the order of 10.1.2 and the rest of the row
// grammar of 4.2.5 is placed between them. A colour is p0 to p15 for the 16
// colours (30-37, 90-97, 38;5;0-15), p16 to p255 for the rest of the 256,
// and #rrggbb for RGB; the default colour is not written. Adjacent cells
// with the same style are one run, a run never crosses a row, and cells in
// the default style are not listed. A link does not make a style.

// color is a colour of a style: kind 0 default, 1 palette index, 2 RGB.
type color struct {
	kind uint8
	v    uint32
}

func (c color) String() string {
	switch c.kind {
	case 1:
		return "p" + strconv.Itoa(int(c.v))
	case 2:
		return fmt.Sprintf("#%06x", c.v)
	}
	return ""
}

// style is the style of one cell.
type style struct {
	bold, faint, italic, blink, reverse, conceal, strike bool
	underline                                            uint8 // 0 none, 1 single, 2 double, 3 curly, 4 dotted, 5 dashed
	fg, bg, ul                                           color
}

var underlineNames = [...]string{"", "underline", "double-underline", "curly-underline", "dotted-underline", "dashed-underline"}

func (s style) String() string {
	var parts []string
	add := func(on bool, name string) {
		if on {
			parts = append(parts, name)
		}
	}
	add(s.bold, "bold")
	add(s.faint, "faint")
	add(s.italic, "italic")
	if s.underline > 0 && int(s.underline) < len(underlineNames) {
		parts = append(parts, underlineNames[s.underline])
	}
	add(s.blink, "blink")
	add(s.reverse, "reverse")
	add(s.conceal, "conceal")
	add(s.strike, "strike")
	if s.fg.kind != 0 {
		parts = append(parts, s.fg.String())
	}
	if s.bg.kind != 0 {
		parts = append(parts, "on", s.bg.String())
	}
	if s.ul.kind != 0 {
		parts = append(parts, "ul", s.ul.String())
	}
	return strings.Join(parts, " ")
}

// run is one run of a row: start column, width in cells, style.
type run struct {
	col, width int
	st         style
}

func (r run) String() string { return fmt.Sprintf("%d %d %s", r.col, r.width, r.st) }

// rowRuns splits one row in the format of 4.2.5 into its styled runs. A
// row outside the grammar is read as far as it goes: an unknown escape
// sequence is skipped and counts no cells.
func rowRuns(row string) []run {
	var (
		runs []run
		cur  style
		col  int
		st   byte
	)
	for len(row) > 0 {
		seq, width, n, newState := ansi.DecodeSequence(row, st, nil)
		st = newState
		row = row[n:]
		if n == 0 {
			break
		}
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			cur = applySGR(cur, seq[2:len(seq)-1])
			continue
		}
		if width == 0 { // another escape sequence, a control character, a lone combining mark
			continue
		}
		if cur != (style{}) {
			if k := len(runs) - 1; k >= 0 && runs[k].st == cur && runs[k].col+runs[k].width == col {
				runs[k].width += width
			} else {
				runs = append(runs, run{col: col, width: width, st: cur})
			}
		}
		col += width
	}
	return runs
}

// applySGR applies the parameters of one SGR sequence (the part between
// "ESC [" and "m") to s. Rows of 4.2.5 start every SGR with 0, so the
// result does not depend on s for them; other rows are read as a terminal
// would.
func applySGR(s style, params string) style {
	if params == "" {
		return style{}
	}
	ps := strings.Split(params, ";")
	num := func(i int) (int, bool) {
		if i >= len(ps) {
			return 0, false
		}
		v, err := strconv.Atoi(ps[i])
		return v, err == nil
	}
	ext := func(i int) (color, int) { // 38/48/58 at ps[i]; returns the colour and the parameters used
		mode, ok := num(i + 1)
		if !ok {
			return color{}, 1
		}
		switch mode {
		case 5:
			v, ok := num(i + 2)
			if !ok || v < 0 || v > 255 {
				return color{}, 3
			}
			return color{kind: 1, v: uint32(v)}, 3
		case 2:
			r, ok1 := num(i + 2)
			g, ok2 := num(i + 3)
			b, ok3 := num(i + 4)
			if !ok1 || !ok2 || !ok3 {
				return color{}, 5
			}
			return color{kind: 2, v: uint32(r&0xff)<<16 | uint32(g&0xff)<<8 | uint32(b&0xff)}, 5
		}
		return color{}, 2
	}
	for i := 0; i < len(ps); {
		p := ps[i]
		if strings.HasPrefix(p, "4:") {
			if v, err := strconv.Atoi(p[2:]); err == nil && v >= 0 && v <= 5 {
				s.underline = uint8(v)
			}
			i++
			continue
		}
		v, ok := num(i)
		if !ok {
			i++
			continue
		}
		switch {
		case v == 0:
			s = style{}
		case v == 1:
			s.bold = true
		case v == 2:
			s.faint = true
		case v == 3:
			s.italic = true
		case v == 4:
			s.underline = 1
		case v == 5:
			s.blink = true
		case v == 7:
			s.reverse = true
		case v == 8:
			s.conceal = true
		case v == 9:
			s.strike = true
		case v == 22:
			s.bold, s.faint = false, false
		case v == 23:
			s.italic = false
		case v == 24:
			s.underline = 0
		case v == 25:
			s.blink = false
		case v == 27:
			s.reverse = false
		case v == 28:
			s.conceal = false
		case v == 29:
			s.strike = false
		case v >= 30 && v <= 37:
			s.fg = color{kind: 1, v: uint32(v - 30)}
		case v == 38:
			c, used := ext(i)
			s.fg = c
			i += used
			continue
		case v == 39:
			s.fg = color{}
		case v >= 40 && v <= 47:
			s.bg = color{kind: 1, v: uint32(v - 40)}
		case v == 48:
			c, used := ext(i)
			s.bg = c
			i += used
			continue
		case v == 49:
			s.bg = color{}
		case v == 58:
			c, used := ext(i)
			s.ul = c
			i += used
			continue
		case v == 59:
			s.ul = color{}
		case v >= 90 && v <= 97:
			s.fg = color{kind: 1, v: uint32(v - 90 + 8)}
		case v >= 100 && v <= 107:
			s.bg = color{kind: 1, v: uint32(v - 100 + 8)}
		}
		i++
	}
	return s
}
