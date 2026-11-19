package screen

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// rowsDir holds the example rows of 4.2.5 shared with the row filter.
var rowsDir = filepath.Join("..", "..", "proto", "testdata", "rows")

// readRows reads one file of example rows: one Go-quoted string per line.
func readRows(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(rowsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := strconv.Unquote(line)
		if err != nil {
			t.Fatalf("%s: %q: %v", name, line, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// grammarError checks row against the grammar of 4.2.5 and the encoder
// rules beside it: no sgr that changes nothing, no link that changes
// nothing, a width of at most cols. It returns "" for a valid row. This is
// a second, independent reading of the grammar, written for this test; the
// filter of the client is internal/client/rowfilter.
func grammarError(row string, cols int) string {
	if len(row) > 128<<10 {
		return "longer than 128 KiB"
	}
	var (
		width int
		sgr   = "0" // the attributes of the current style, "0" is default
		link  string
		text  strings.Builder
	)
	flush := func() string {
		s := text.String()
		text.Reset()
		if s == "" {
			return ""
		}
		if !utf8.ValidString(s) {
			return "text is not UTF-8"
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				return fmt.Sprintf("text holds the control U+%04X", r)
			}
		}
		width += ansi.StringWidth(s)
		return ""
	}
	for i := 0; i < len(row); {
		if row[i] != 0x1b {
			text.WriteByte(row[i])
			i++
			continue
		}
		if e := flush(); e != "" {
			return e
		}
		rest := row[i:]
		switch {
		case strings.HasPrefix(rest, "\x1b[0"):
			end := strings.IndexByte(rest, 'm')
			if end < 0 {
				return "sgr without m"
			}
			params := rest[3:end]
			if params != "" && params[0] != ';' {
				return "sgr does not start with 0"
			}
			if e := checkAttrs(params); e != "" {
				return e
			}
			if a := "0" + params; a == sgr {
				return "sgr changes nothing: " + strconv.Quote(rest[:end+1])
			} else {
				sgr = a
			}
			i += end + 1
		case strings.HasPrefix(rest, "\x1b]8;;"):
			end := strings.Index(rest, "\x1b\\")
			if end < 0 {
				return "link without ST"
			}
			uri := rest[5:end]
			if len(uri) > 2048 {
				return "uri longer than 2048"
			}
			for j := 0; j < len(uri); j++ {
				if uri[j] < 0x21 || uri[j] > 0x7e {
					return "uri byte outside 21-7E"
				}
			}
			if uri == link {
				return "link changes nothing"
			}
			link = uri
			i += end + 2
		default:
			return "a sequence outside the grammar: " + strconv.Quote(rest[:min(len(rest), 8)])
		}
	}
	if e := flush(); e != "" {
		return e
	}
	if cols >= 0 && width > cols {
		return fmt.Sprintf("width %d is more than %d", width, cols)
	}
	return ""
}

// checkAttrs checks the part of an sgr after the leading 0: ";attr" each.
func checkAttrs(p string) string {
	if p == "" {
		return ""
	}
	fields := strings.Split(p[1:], ";")
	n := func(s string) bool { // n = 1*3DIGIT, 0..255
		if len(s) < 1 || len(s) > 3 || strings.Trim(s, "0123456789") != "" {
			return false
		}
		v, _ := strconv.Atoi(s)
		return v <= 255
	}
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch f {
		case "1", "2", "3", "5", "7", "8", "9", "4", "4:2", "4:3", "4:4", "4:5":
			continue
		case "38", "48", "58":
			if i+1 >= len(fields) {
				return "colour without form"
			}
			switch fields[i+1] {
			case "5":
				if i+2 >= len(fields) || !n(fields[i+2]) {
					return "bad 256 colour"
				}
				i += 2
			case "2":
				if i+4 >= len(fields) || !n(fields[i+2]) || !n(fields[i+3]) || !n(fields[i+4]) {
					return "bad rgb colour"
				}
				i += 4
			default:
				return "colour without form"
			}
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil || strconv.Itoa(v) != f {
			return "attribute " + strconv.Quote(f)
		}
		if (v >= 30 && v <= 37) || (v >= 90 && v <= 97) || (v >= 40 && v <= 47) || (v >= 100 && v <= 107) {
			continue
		}
		return "attribute " + strconv.Quote(f) + " is not in the list"
	}
	return ""
}

func TestTheGrammarCheckOfTheTestAgreesWithTheExamples(t *testing.T) {
	for _, r := range readRows(t, "valid.txt") {
		if e := grammarError(r, -1); e != "" {
			t.Errorf("valid example %q: %s", r, e)
		}
	}
	for _, r := range readRows(t, "invalid.txt") {
		if grammarError(r, -1) == "" {
			t.Errorf("invalid example %q passes", r)
		}
	}
}

// randomCell is a cell of a random style and text, wide sometimes, with
// text that is often not what a cell should hold.
func randomCell(r *rand.Rand) Cell {
	texts := []string{"a", "Z", " ", "~", "é", "é", "́", "日", "👍🏽", "\U0001F1E9\U0001F1EA",
		"\x1b", "\x1b[2J", "\x07", "\u0085", "\u009b", "ab", "\xff", "", "‍", "؀", "1", " "}
	colors := func() Color {
		switch r.IntN(5) {
		case 0:
			return Color{Kind: 1, V: uint32(r.IntN(18))}
		case 1:
			return Color{Kind: 2, V: uint32(r.IntN(300))}
		case 2:
			return Color{Kind: 3, V: r.Uint32()}
		case 3:
			return Color{Kind: uint8(r.IntN(9)), V: r.Uint32()}
		}
		return Color{}
	}
	st := Style{}
	if r.IntN(3) == 0 {
		st = Style{Fg: colors(), Bg: colors(), Ul: colors(), Attrs: Attr(r.IntN(1 << 9)), Underline: uint8(r.IntN(8))}
	}
	switch r.IntN(8) {
	case 0:
		st.Link = "https://example.com/" + strconv.Itoa(r.IntN(3))
	case 1:
		st.Link = "bad link\x1b"
	}
	s := texts[r.IntN(len(texts))]
	w := uint8(1)
	if r.IntN(4) == 0 {
		w = 2
	}
	return Cell{S: s, W: w, Style: st}
}

func TestRenderProducesOnlyValidExampleGrammar(t *testing.T) {
	// the styles of every valid example, read by Styled, come out in the
	// grammar, and with the same cells
	for _, ex := range readRows(t, "valid.txt") {
		c := New(80, 1)
		c.Styled(0, 0, ex, 80)
		out := c.Lines()[0]
		if e := grammarError(out, 80); e != "" {
			t.Errorf("example %q rendered %q: %s", ex, out, e)
		}
		d := New(80, 1)
		d.Styled(0, 0, out, 80)
		if a, b := cellsOf(c, 0), cellsOf(d, 0); fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("example %q: rendering and reading again changed the cells", ex)
		}
	}
	// random canvases, written through every method
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 400 {
		w, h := 1+r.IntN(40), 1+r.IntN(4)
		c := New(w, h)
		for range r.IntN(60) {
			x, y := r.IntN(w+4)-2, r.IntN(h+2)-1
			switch r.IntN(5) {
			case 0, 1:
				c.Set(x, y, randomCell(r))
			case 2:
				cells := make([]Cell, r.IntN(8))
				for k := range cells {
					cells[k] = randomCell(r)
				}
				c.Cells(x, y, cells)
			case 3:
				c.Text(x, y, randomCell(r).S+randomCell(r).S, randomCell(r).Style, r.IntN(10))
			case 4:
				c.Sub(Rect{x, y, r.IntN(10), r.IntN(3)}).Fill(Rect{0, 0, 10, 10}, randomCell(r))
			}
		}
		for y, row := range c.Lines() {
			if e := grammarError(row, w); e != "" {
				t.Fatalf("canvas %d row %d %q: %s", i, y, row, e)
			}
		}
	}
}

func TestRenderNeverEmitsAControlByteFromCellText(t *testing.T) {
	bad := []string{"\x1b", "\x1b[2J", "\x1b]0;x\x07", "\x07", "\r", "\n", "\x00", "\x7f", "\u0085", "\u009b",
		"\xc2\x9b", "\x9b", "a\x1b", "\t"}
	c := New(len(bad)*2, 2)
	for i, s := range bad {
		c.Set(i*2, 0, Cell{S: s, W: 1})
		c.Set(i*2, 1, Cell{S: s, W: 2})
	}
	for y, row := range c.Lines() {
		if e := grammarError(row, len(bad)*2); e != "" {
			t.Errorf("row %d %q: %s", y, row, e)
		}
		for j := 0; j < len(row); j++ {
			if b := row[j]; b < 0x20 || b == 0x7f {
				t.Errorf("row %d has the byte %#x at %d: %q", y, b, j, row)
			}
		}
		if n := ansi.StringWidth(row); n != len(bad)*2 && y == 1 {
			t.Errorf("row %d is %d cells wide, want %d: the grid moved", y, n, len(bad)*2)
		}
	}
	// a cell that would join the one before it when the row is read
	c = New(4, 1)
	c.Set(0, 0, Cell{S: "e", W: 1})
	c.Set(1, 0, Cell{S: "́", W: 1})
	c.Set(2, 0, Cell{S: "x", W: 1})
	if got := c.Lines()[0]; got != "e"+replacement+"x" {
		t.Errorf("a combining mark in its own cell gave %q", got)
	}
}

func TestRenderReencodesOnlyTouchedRows(t *testing.T) {
	c := New(10, 5)
	c.Render()
	base := c.b.encodes
	c.Render()
	if n := c.b.encodes - base; n != 0 {
		t.Errorf("an unchanged canvas re-encoded %d rows", n)
	}
	c.Text(0, 2, "hi", Style{}, 10)
	c.Text(0, 2, "hi", Style{}, 10) // the same cells again
	c.Set(3, 4, Cell{S: "x", W: 1})
	out := c.Render()
	if n := c.b.encodes - base; n != 2 {
		t.Errorf("re-encoded %d rows, want 2", n)
	}
	if want := "\n\nhi\n\n   x"; out != want {
		t.Errorf("Render = %q, want %q", out, want)
	}
	// a frame drawn from scratch in painter's order (Clear, then
	// everything) re-encodes only the rows whose cells it wrote over
	base = c.b.encodes
	c.Clear()
	c.Text(0, 2, "hi", Style{}, 10)
	c.Set(3, 4, Cell{S: "x", W: 1})
	c.Restyle(Rect{0, 0, 10, 5}, func(s Style) Style { return s })
	c.Render()
	c.Render()
	if n := c.b.encodes - base; n != 2 {
		t.Errorf("clearing and drawing the same picture re-encoded %d rows, want 2 (rows 2 and 4)", n)
	}
}

func TestDefaultColoursStayDefault(t *testing.T) {
	c := New(20, 3)
	c.Text(0, 0, "plain", Style{}, 20)
	c.Text(0, 1, "bold", Style{Attrs: Bold}, 20)
	c.Styled(0, 2, "\x1b[31ma\x1b[39mb\x1b[44mc\x1b[49md\x1b[0m", 20)
	want := []string{"plain", "\x1b[0;1mbold\x1b[0m", "\x1b[0;31ma\x1b[0mb\x1b[0;44mc\x1b[0md"}
	got := c.Lines()
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, row := range got {
		for _, never := range []string{"39", "49", "59", "38;5;", "48;5;", "38;2;", "48;2;"} {
			if strings.Contains(row, never) {
				t.Errorf("row %q names a colour (%s) that should stay the terminal's", row, never)
			}
		}
	}
	// a cell drawn and restyled keeps the default colours it had
	c.Restyle(Rect{0, 0, 20, 3}, func(s Style) Style { s.Attrs |= Reverse; return s })
	for y := range 2 {
		for x := range 4 {
			if st := c.At(x, y).Style; st.Fg != (Color{}) || st.Bg != (Color{}) {
				t.Errorf("cell %d,%d has colours %+v after Restyle", x, y, st)
			}
		}
	}
}

func TestRenderEncodesEveryAttributeInTheOrderOfTheGrammar(t *testing.T) {
	c := New(4, 1)
	st := Style{
		Attrs: Bold | Faint | Italic | Blink | Reverse | Conceal | Strike, Underline: 3,
		Fg: Color{Kind: 1, V: 9}, Bg: Color{Kind: 2, V: 200}, Ul: Color{Kind: 3, V: 0x0a0b0c},
		Link: "https://example.com",
	}
	c.Set(0, 0, Cell{S: "a", W: 1, Style: st})
	want := "\x1b]8;;https://example.com\x1b\\\x1b[0;1;2;3;5;7;8;9;4:3;91;48;5;200;58;2;10;11;12ma\x1b[0m\x1b]8;;\x1b\\"
	if got := c.Lines()[0]; got != want {
		t.Errorf("row = %q, want %q", got, want)
	}
	if e := grammarError(want, 4); e != "" {
		t.Errorf("%s", e)
	}
}

func BenchmarkRender200x50(b *testing.B) {
	c := New(200, 50)
	styles := []Style{{}, {Attrs: Bold}, {Fg: Color{Kind: 1, V: 2}}, {Fg: Color{Kind: 3, V: 0xe6e7eb}, Bg: Color{Kind: 3, V: 0x30323a}}}
	line := strings.Repeat("the quick brown fox jumps over 日本語 ", 6)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		for y := range 50 {
			c.Text(0, y, line[i%7:], styles[(y+i)%len(styles)], 200)
		}
		_ = c.Render()
	}
}
