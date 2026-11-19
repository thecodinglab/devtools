// Package rowfilter is the only way a row a server sent becomes cells
// (3.2.4, 4.2.5, SEC16 of the plan). Parse accepts exactly the row grammar
// of 4.2.5 and nothing else:
//
//	row   = *( sgr / link / text )
//	sgr   = ESC "[" "0" *( ";" attr ) "m"
//	attr  = "1" / "2" / "3" / "5" / "7" / "8" / "9"
//	      / "4" / "4:2" / "4:3" / "4:4" / "4:5"
//	      / "30".."37" / "90".."97" / "38;5;" n / "38;2;" n ";" n ";" n
//	      / "40".."47" / "100".."107" / "48;5;" n / "48;2;" n ";" n ";" n
//	      / "58;5;" n / "58;2;" n ";" n ";" n
//	n     = 1*3DIGIT                              ; 0..255
//	link  = ESC "]8;;" uri ESC "\"                ; empty uri closes the link
//	uri   = *2048( %x21-7E )
//	text  = 1*( any Unicode scalar >= U+0020 except U+007F and U+0080..U+009F )
//
// Any other byte sequence, an attribute outside the list, invalid UTF-8 or
// a row longer than 128 KiB is an error, and the caller drops the frame and
// the link it came on. Nothing of a row reaches the outer terminal as
// bytes: the canvas encodes again from the cells.
//
// The package imports screen, x/ansi and the standard library only.
package rowfilter

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"devtools/internal/client/screen"
	"github.com/charmbracelet/x/ansi"
)

// Limits of 4.2.5. maxRow equals proto.MaxRow (TestRejectsARowLongerThanTheLimit
// holds them equal); it is repeated here because the package imports
// nothing of the protocol.
const (
	maxRow = 128 << 10
	maxURI = 2048
)

// errInvalid is the error every invalid row wraps. The messages name the
// byte offset and the rule, never the bytes of the row.
var errInvalid = errors.New("invalid row")

func invalid(at int, why string) error {
	return fmt.Errorf("%w: byte %d: %s", errInvalid, at, why)
}

// replacement stands for a cluster the client measures with no width: the
// server never sends a cell of width 0 (every cell it writes is 1 or 2
// wide, and its encoder replaces a cluster x/ansi measures otherwise), so
// such a cluster is one the two width tables disagree on, and it becomes
// one cell of U+FFFD, as the encoder would have written it. provisional
// (E9): E9 found the engine's widths equal to x/ansi's in 35 of 35 cases;
// how a newer table on another machine measures is not known.
const replacement = "\uFFFD"

// Parse parses one row of a frame (4.2.4) for a pane cols cells wide. It
// returns the cells of the row from column 0; trailing blank cells, which
// the encoder omits, are not returned, so a row of fewer cells than cols is
// blank after its last cell. A wide character is a cell of width 2 followed
// by its second cell (S "", W 0) with the same style.
//
// Width is measured with ansi.FirstGraphemeCluster in grapheme mode, the
// measure of ansi.StringWidth. A row whose cells are wider than cols is
// invalid, except when it fits without its last cluster: then that cluster
// is cut off (the server's and the client's width tables can differ for
// one cluster, 4.2.5), so the effect is a misplaced cell and not a dropped
// link.
func Parse(s string, cols int) ([]screen.Cell, error) {
	if len(s) > maxRow {
		return nil, fmt.Errorf("%w: %d bytes, longer than the limit of %d", errInvalid, len(s), maxRow)
	}
	if s == "" {
		return nil, nil
	}
	if cols < 0 {
		cols = 0
	}
	p := parser{s: s, cols: cols}
	if err := p.run(); err != nil {
		return nil, err
	}
	if p.width > cols {
		// Only the last cluster may overflow; anything wider was refused
		// while the cells were built.
		p.cells = p.cells[:p.lastAt]
	}
	return p.cells, nil
}

type parser struct {
	s     string
	i     int
	cols  int
	st    screen.Style
	cells []screen.Cell
	width int // the sum of the widths of cells
	// lastAt is the index of the first cell of the last cluster, and
	// lastW its width: the row may be cut there once.
	lastAt int
	lastW  int
}

func (p *parser) run() error {
	for p.i < len(p.s) {
		if p.s[p.i] == 0x1b {
			if err := p.escape(); err != nil {
				return err
			}
			continue
		}
		if err := p.text(); err != nil {
			return err
		}
	}
	return nil
}

// escape reads one sgr or link at p.i, which holds ESC.
func (p *parser) escape() error {
	start := p.i
	if p.i+1 >= len(p.s) {
		return invalid(start, "ESC at the end of the row")
	}
	switch p.s[p.i+1] {
	case '[':
		p.i += 2
		return p.sgr(start)
	case ']':
		p.i += 2
		return p.link(start)
	}
	return invalid(start, "an escape sequence other than SGR and OSC 8")
}

// sgr reads the rest of an sgr after ESC "[". Every sgr is absolute: it
// starts from the default style. The link is not part of the style an sgr
// sets and stays as it is.
func (p *parser) sgr(start int) error {
	if !p.take("0") {
		return invalid(start, "an SGR that does not start with 0 (relative SGR, or not an SGR)")
	}
	st := screen.Style{Link: p.st.Link}
	for {
		if p.i >= len(p.s) {
			return invalid(start, "an SGR without its end")
		}
		switch p.s[p.i] {
		case 'm':
			p.i++
			p.st = st
			return nil
		case ';':
			p.i++
		default:
			return invalid(p.i, "a byte in an SGR outside the grammar")
		}
		at := p.i
		f, ok := p.field()
		if !ok {
			return invalid(at, "an empty or malformed SGR attribute")
		}
		if !p.attr(f, &st) {
			return invalid(at, "an SGR attribute outside the list of 4.2.5")
		}
	}
}

// field reads one parameter of an sgr: digits, and for the underline styles
// one colon. It stops before ";" or "m" and fails on anything else.
func (p *parser) field() (string, bool) {
	j := p.i
	for j < len(p.s) && j-p.i < 4 && (isDigit(p.s[j]) || p.s[j] == ':') {
		j++
	}
	if j == p.i || j >= len(p.s) || (p.s[j] != ';' && p.s[j] != 'm') {
		return "", false
	}
	f := p.s[p.i:j]
	p.i = j
	return f, true
}

// attr applies one attribute f; for the extended colours it reads the
// fields that follow.
func (p *parser) attr(f string, st *screen.Style) bool {
	switch f {
	case "1":
		st.Attrs |= screen.Bold
	case "2":
		st.Attrs |= screen.Faint
	case "3":
		st.Attrs |= screen.Italic
	case "5":
		st.Attrs |= screen.Blink
	case "7":
		st.Attrs |= screen.Reverse
	case "8":
		st.Attrs |= screen.Conceal
	case "9":
		st.Attrs |= screen.Strike
	case "4":
		st.Underline = 1
	case "4:2", "4:3", "4:4", "4:5":
		st.Underline = f[2] - '0'
	case "38":
		return p.extended(&st.Fg)
	case "48":
		return p.extended(&st.Bg)
	case "58":
		return p.extended(&st.Ul)
	default:
		if len(f) == 2 && f[1] >= '0' && f[1] <= '7' {
			v := uint32(f[1] - '0')
			switch f[0] {
			case '3':
				st.Fg = screen.Color{Kind: 1, V: v}
				return true
			case '9':
				st.Fg = screen.Color{Kind: 1, V: 8 + v}
				return true
			case '4':
				st.Bg = screen.Color{Kind: 1, V: v}
				return true
			}
		}
		if len(f) == 3 && f[0] == '1' && f[1] == '0' && f[2] >= '0' && f[2] <= '7' {
			st.Bg = screen.Color{Kind: 1, V: 8 + uint32(f[2]-'0')}
			return true
		}
		return false
	}
	return true
}

// extended reads ";5;" n or ";2;" n ";" n ";" n after 38, 48 or 58.
func (p *parser) extended(c *screen.Color) bool {
	switch {
	case p.take(";5;"):
		n, ok := p.number()
		if !ok {
			return false
		}
		*c = screen.Color{Kind: 2, V: n}
		return true
	case p.take(";2;"):
		var v uint32
		for k := range 3 {
			if k > 0 && !p.take(";") {
				return false
			}
			n, ok := p.number()
			if !ok {
				return false
			}
			v = v<<8 | n
		}
		*c = screen.Color{Kind: 3, V: v}
		return true
	}
	return false
}

// number reads n: one to three digits with a value of at most 255, followed
// by ";" or "m".
func (p *parser) number() (uint32, bool) {
	var v uint32
	j := p.i
	for j < len(p.s) && j-p.i < 3 && isDigit(p.s[j]) {
		v = v*10 + uint32(p.s[j]-'0')
		j++
	}
	if j == p.i || v > 255 || j >= len(p.s) || (p.s[j] != ';' && p.s[j] != 'm') {
		return 0, false
	}
	p.i = j
	return v, true
}

// link reads the rest of a link after ESC "]": "8;;", the uri and ESC "\".
// An empty uri closes the link.
func (p *parser) link(start int) error {
	if !p.take("8;;") {
		return invalid(start, "an OSC other than 8, or OSC 8 with parameters")
	}
	j := p.i
	for j < len(p.s) && p.s[j] >= 0x21 && p.s[j] <= 0x7e {
		j++
	}
	if j-p.i > maxURI {
		return invalid(p.i, "a link longer than 2048 bytes")
	}
	if j+1 >= len(p.s) || p.s[j] != 0x1b || p.s[j+1] != '\\' {
		return invalid(j, "a link with a byte outside the URI set, or not ended by ESC \\")
	}
	p.st.Link = p.s[p.i:j]
	p.i = j + 2
	return nil
}

// text reads one run of text up to the next ESC or the end of the row and
// appends its clusters as cells.
func (p *parser) text() error {
	j := p.i
	for j < len(p.s) {
		b := p.s[j]
		if b < utf8.RuneSelf {
			if b == 0x1b {
				break
			}
			if b < 0x20 || b == 0x7f {
				return invalid(j, "a C0 control character or DEL")
			}
			j++
			continue
		}
		r, size := utf8.DecodeRuneInString(p.s[j:])
		if r == utf8.RuneError && size == 1 {
			return invalid(j, "invalid UTF-8")
		}
		if r >= 0x80 && r <= 0x9f {
			return invalid(j, "a C1 control character")
		}
		j += size
	}
	run := p.s[p.i:j]
	p.i = j
	for run != "" {
		cl, w := ansi.FirstGraphemeCluster(run, ansi.GraphemeWidth)
		if len(cl) == 0 { // cannot happen for valid UTF-8; never loop
			cl = run[:1]
		}
		run = run[len(cl):]
		switch {
		case w <= 0:
			cl, w = replacement, 1
		case w > 2:
			// No table gives a cluster more than 2 today; a cell is at
			// most 2 wide whatever a later one says.
			w = 2
		}
		if err := p.cell(cl, w); err != nil {
			return err
		}
	}
	return nil
}

// cell appends one cluster of width w. The width sum may pass cols only by
// the cluster that is last in the row, so a cluster after an overflowing
// one makes the row invalid.
func (p *parser) cell(s string, w int) error {
	if p.width > p.cols {
		return invalid(p.i, fmt.Sprintf("cells wider than the %d columns of the pane", p.cols))
	}
	p.lastAt, p.lastW = len(p.cells), w
	p.cells = append(p.cells, screen.Cell{S: s, W: uint8(w), Style: p.st})
	if w == 2 {
		p.cells = append(p.cells, screen.Cell{S: "", W: 0, Style: p.st})
	}
	p.width += w
	return nil
}

func (p *parser) take(lit string) bool {
	if len(p.s)-p.i >= len(lit) && p.s[p.i:p.i+len(lit)] == lit {
		p.i += len(lit)
		return true
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
