//go:build cgo

package vt

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"devtools/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

// checkRow is a reading of the grammar of 4.2.5, kept in the tests of the
// encoder so the encoder is checked against the text of the plan and not
// against itself (the filter of the client is P1-08's). It returns nil for
// a row inside the grammar.
func checkRow(row string) error {
	if len(row) > proto.MaxRow {
		return fmt.Errorf("longer than %d bytes", proto.MaxRow)
	}
	if !utf8.ValidString(row) {
		return fmt.Errorf("not UTF-8")
	}
	for i := 0; i < len(row); {
		switch {
		case strings.HasPrefix(row[i:], "\x1b[0"):
			n, err := sgrLen(row[i:])
			if err != nil {
				return fmt.Errorf("at %d: %v", i, err)
			}
			i += n
		case strings.HasPrefix(row[i:], "\x1b]8;;"):
			end := strings.Index(row[i+5:], "\x1b\\")
			if end < 0 {
				return fmt.Errorf("at %d: link without ESC \\", i)
			}
			uri := row[i+5 : i+5+end]
			if len(uri) > 2048 {
				return fmt.Errorf("at %d: uri longer than 2048", i)
			}
			for j := 0; j < len(uri); j++ {
				if uri[j] < 0x21 || uri[j] > 0x7e {
					return fmt.Errorf("at %d: byte %#x in a uri", i, uri[j])
				}
			}
			i += 5 + end + 2
		default:
			r, size := utf8.DecodeRuneInString(row[i:])
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				return fmt.Errorf("at %d: control character %U", i, r)
			}
			i += size
		}
	}
	return nil
}

var simpleAttrs = map[string]bool{
	"1": true, "2": true, "3": true, "5": true, "7": true, "8": true, "9": true,
	"4": true, "4:2": true, "4:3": true, "4:4": true, "4:5": true,
}

// sgrLen checks one SGR of the grammar at the start of s and returns its
// length. It also checks the order of the attributes and that none repeats.
func sgrLen(s string) (int, error) {
	end := strings.IndexByte(s, 'm')
	if end < 0 {
		return 0, fmt.Errorf("sgr without m")
	}
	params := s[2:end]
	for _, c := range params {
		if !(c >= '0' && c <= '9') && c != ';' && c != ':' {
			return 0, fmt.Errorf("sgr with %q", c)
		}
	}
	parts := strings.Split(params, ";")
	if parts[0] != "0" {
		return 0, fmt.Errorf("sgr does not start with 0")
	}
	rank := -1 // the position in the order of the grammar of the last attribute
	for i := 1; i < len(parts); {
		p := parts[i]
		var r int
		switch {
		case simpleAttrs[p]:
			r = strings.Index("1235789", p[:1])
			if p[0] == '4' {
				r = 7
			}
			i++
		case len(p) == 2 && (p[0] == '3' || p[0] == '9') && p[1] >= '0' && p[1] <= '7':
			r = 8
			i++
		case len(p) == 2 && p[0] == '4' && p[1] >= '0' && p[1] <= '7', len(p) == 3 && p[:2] == "10" && p[2] >= '0' && p[2] <= '7':
			r = 9
			i++
		case p == "38" || p == "48" || p == "58":
			r = map[string]int{"38": 8, "48": 9, "58": 10}[p]
			if i+1 >= len(parts) {
				return 0, fmt.Errorf("colour %s without form", p)
			}
			want := 0
			switch parts[i+1] {
			case "5":
				want = 1
			case "2":
				want = 3
			default:
				return 0, fmt.Errorf("colour %s with form %q", p, parts[i+1])
			}
			if i+2+want > len(parts) {
				return 0, fmt.Errorf("colour %s cut short", p)
			}
			for _, n := range parts[i+2 : i+2+want] {
				if len(n) < 1 || len(n) > 3 {
					return 0, fmt.Errorf("colour value %q", n)
				}
				if v, err := strconv.Atoi(n); err != nil || v > 255 {
					return 0, fmt.Errorf("colour value %q", n)
				}
			}
			i += 2 + want
		default:
			return 0, fmt.Errorf("attribute %q", p)
		}
		if r <= rank {
			return 0, fmt.Errorf("attribute %q out of the order of 4.2.5 or repeated", p)
		}
		rank = r
	}
	return end + 1, nil
}

// wireCell is one cell of a parsed wire row.
type wireCell struct {
	text  string
	width int
	sgr   string // the SGR in force, "" for the default style
	link  string
}

// parseRow reads a row of the grammar into its cells as the client measures
// them: grapheme clusters of the text between escape sequences, with the
// widths of x/ansi in grapheme mode. A wide cell is one entry.
func parseRow(row string) []wireCell {
	var cells []wireCell
	sgr, link := "", ""
	for i := 0; i < len(row); {
		switch {
		case strings.HasPrefix(row[i:], "\x1b[0"):
			n, _ := sgrLen(row[i:])
			if n == 0 {
				return cells
			}
			sgr = row[i : i+n]
			if sgr == "\x1b[0m" {
				sgr = ""
			}
			i += n
		case strings.HasPrefix(row[i:], "\x1b]8;;"):
			end := strings.Index(row[i+5:], "\x1b\\")
			if end < 0 {
				return cells
			}
			link = row[i+5 : i+5+end]
			i += 5 + end + 2
		default:
			j := i
			for j < len(row) && row[j] != 0x1b {
				j++
			}
			text := row[i:j]
			for len(text) > 0 {
				c, w := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
				cells = append(cells, wireCell{text: c, width: w, sgr: sgr, link: link})
				text = text[len(c):]
			}
			i = j
		}
	}
	return cells
}

// rowWidth is the width of a row as the client measures it.
func rowWidth(row string) int { return ansi.StringWidth(row) }

// termAt makes a terminal for a test and closes it when the test ends.
func termAt(tb testing.TB, cols, rows int, o ...func(*Options)) *term {
	tb.Helper()
	opts := Options{Cols: cols, Rows: rows, History: 1000}
	for _, f := range o {
		f(&opts)
	}
	t := New(opts).(*term)
	tb.Cleanup(t.Close)
	return t
}

func TestRowIsAbsoluteAndStartsEverySGRWithReset(t *testing.T) {
	tm := termAt(t, 40, 3)
	tm.Write([]byte("\x1b[1mbold\x1b[3mitalic\x1b[22mnotbold\x1b[31mred\x1b[39mdef\x1b[0m plain"))
	row := tm.Row(0)
	if err := checkRow(row); err != nil {
		t.Fatalf("row %q: %v", row, err)
	}
	want := "\x1b[0;1mbold\x1b[0;1;3mitalic\x1b[0;3mnotbold\x1b[0;3;31mred\x1b[0;3mdef\x1b[0m plain"
	if row != want {
		t.Fatalf("row\n got %q\nwant %q", row, want)
	}
	for i := 0; i < len(row); i++ {
		if strings.HasPrefix(row[i:], "\x1b[") && !strings.HasPrefix(row[i:], "\x1b[0") {
			t.Fatalf("an SGR at %d does not start from reset: %q", i, row)
		}
	}
}

func TestRowNeverCarriesStyleAcrossRows(t *testing.T) {
	tm := termAt(t, 10, 3)
	// The style is open at the end of row 0 and still in force for row 1.
	tm.Write([]byte("\x1b[1;41mtop\r\nnext\x1b[0m\r\nplain"))
	if got, want := tm.Row(0), "\x1b[0;1;41mtop"; got != want {
		t.Errorf("row 0 = %q, want %q", got, want)
	}
	if got, want := tm.Row(1), "\x1b[0;1;41mnext"; got != want {
		t.Errorf("row 1 = %q, want %q (it states its own style)", got, want)
	}
	if got, want := tm.Row(2), "plain"; got != want {
		t.Errorf("row 2 = %q, want %q (nothing carried over)", got, want)
	}
	// A link open at the end of a row ends with the row.
	tm.Write([]byte("\x1b[2J\x1b[H\x1b]8;;https://example.com\x1b\\linked\r\nalso\x1b]8;;\x1b\\"))
	if got, want := tm.Row(0), "\x1b]8;;https://example.com\x1b\\linked"; got != want {
		t.Errorf("linked row 0 = %q, want %q", got, want)
	}
	if got, want := tm.Row(1), "\x1b]8;;https://example.com\x1b\\also"; got != want {
		t.Errorf("linked row 1 = %q, want %q", got, want)
	}
}

func TestRowOmitsTrailingDefaultBlanks(t *testing.T) {
	tm := termAt(t, 20, 4)
	tm.Write([]byte("abc   \r\n\x1b[44mab \x1b[0m  \r\n   \r\n\x1b[7m \x1b[0m"))
	cases := []struct {
		y    int
		want string
	}{
		{0, "abc"},
		{1, "\x1b[0;44mab "},
		{2, ""},
		{3, "\x1b[0;7m "},
	}
	for _, c := range cases {
		if got := tm.Row(c.y); got != c.want {
			t.Errorf("row %d = %q, want %q", c.y, got, c.want)
		}
	}
	// A blank inside the row is kept; the cells of an erase with a
	// background colour are not blank.
	tm.Write([]byte("\x1b[2J\x1b[Ha  b\r\n\x1b[42m\x1b[K\x1b[0m"))
	if got, want := tm.Row(0), "a  b"; got != want {
		t.Errorf("row with an inner blank = %q, want %q", got, want)
	}
	if got, want := tm.Row(1), "\x1b[0;42m"+strings.Repeat(" ", 20); got != want {
		t.Errorf("erased with a background = %q, want %q", got, want)
	}
}

func TestRowReplacesCellsThatWouldReclusterWithFFFD(t *testing.T) {
	tm := termAt(t, 20, 4)
	// Each pair is written right cell first, so the library holds two
	// clusters that would join into one when the row is parsed: a thumb and
	// its skin tone, two regional indicators, a letter and a ZWJ sequence.
	tm.Write([]byte("\x1b[1;3H\U0001F3FD\x1b[1;1H\U0001F44D"))
	tm.Write([]byte("\x1b[2;3H\U0001F1EA\x1b[2;1H\U0001F1E9"))
	cases := []struct {
		y    int
		want string
	}{
		{0, "\U0001F44D� "},
		{1, "\U0001F1E9� "},
	}
	for _, c := range cases {
		got := tm.Row(c.y)
		if got != c.want {
			t.Errorf("row %d = %q, want %q", c.y, got, c.want)
		}
		if w := rowWidth(got); w != 4 {
			t.Errorf("row %d measures %d cells, want 4 (the grid stays aligned)", c.y, w)
		}
	}
	// A Prepend character (U+11A87, Unicode GB9b) joins whatever follows
	// it, U+FFFD and a space included: the cell is replaced itself.
	tm.Write([]byte("\x1b[4;1H\U00011A870"))
	if got, want := tm.Row(3), "�0"; got != want {
		t.Errorf("row 3 = %q, want %q", got, want)
	}
	if w := rowWidth(tm.Row(3)); w != 2 {
		t.Errorf("row 3 measures %d cells, want 2", w)
	}
	// A cluster written whole stays as it is.
	tm.Write([]byte("\x1b[3;1H\U0001F44D\U0001F3FD|"))
	if got, want := tm.Row(2), "\U0001F44D\U0001F3FD|"; got != want {
		t.Errorf("row 2 = %q, want %q", got, want)
	}
	// Every cell the encoder kept parses back to the library's cell.
	for y := range 3 {
		cells, _ := tm.liveCells(y)
		var lib []string
		for _, c := range cells {
			if c.wide != wideSpacerTail {
				lib = append(lib, string(c.text))
			}
		}
		for i, w := range parseRow(tm.Row(y)) {
			if i < len(lib) && w.text != lib[i] && w.text != "�" && !(w.text == " " && lib[i] == "") {
				t.Errorf("row %d cell %d parses as %q, the library holds %q", y, i, w.text, lib[i])
			}
		}
	}
}

func TestRowWidthNeverExceedsCols(t *testing.T) {
	inputs := []string{
		strings.Repeat("漢", 30),
		strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 30),
		strings.Repeat("é", 50),
		strings.Repeat("❤️", 30),
		strings.Repeat("a\U0001F1E9\U0001F1EA", 30),
		"\x1b[1;20H漢",  // a wide character at the last column wraps
		"\x1b[1;19H漢x", // and one that fits
		strings.Repeat("\x1b[38;2;1;2;3m漢\x1b[0mx", 20),
	}
	for _, cols := range []int{2, 3, 7, 20} {
		for i, in := range inputs {
			tm := termAt(t, cols, 6)
			tm.Write([]byte(in))
			for y := range 6 {
				row := tm.Row(y)
				if err := checkRow(row); err != nil {
					t.Errorf("cols %d input %d row %d %q: %v", cols, i, y, row, err)
				}
				if w := rowWidth(row); w > cols {
					t.Errorf("cols %d input %d row %d %q measures %d cells", cols, i, y, row, w)
				}
			}
		}
	}
}

// readLines reads a file of Go-quoted strings, one per line.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s, err := strconv.Unquote(line)
		if err != nil {
			t.Fatalf("%s:%d: %v", path, n, err)
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEncoderProducesEveryValidExampleRow(t *testing.T) {
	rows := readLines(t, "../proto/testdata/rows/valid.txt")
	if len(rows) < 20 {
		t.Fatalf("only %d example rows", len(rows))
	}
	for i, want := range rows {
		// An example row is itself program output: written to a fresh
		// terminal, it must come back from the encoder byte for byte.
		tm := termAt(t, 80, 2)
		tm.Write([]byte(want))
		if got := tm.Row(0); got != want {
			t.Errorf("valid.txt:%d\n got %q\nwant %q", i+1, got, want)
		}
	}
	for i, bad := range readLines(t, "../proto/testdata/rows/invalid.txt") {
		// Whatever a program writes, the encoder emits only the grammar.
		tm := termAt(t, 80, 3)
		tm.Write([]byte(bad))
		for y := range 3 {
			if err := checkRow(tm.Row(y)); err != nil {
				t.Errorf("invalid.txt:%d written as output, row %d %q: %v", i+1, y, tm.Row(y), err)
			}
		}
	}
}

func TestDefaultColoursAreNotEncoded(t *testing.T) {
	tm := termAt(t, 40, 3, func(o *Options) { o.Colors = Colors{Fg: "#e6e7eb", Bg: "#30323a", Dark: true} })
	tm.Write([]byte("plain \x1b[39;49mdefault\x1b[38;5;1;48;5;2m pal\x1b[0m\r\n\x1b]10;#ff0000\x1b\\\x1b]11;#00ff00\x1b\\still plain"))
	if got, want := tm.Row(0), "plain default\x1b[0;31;42m pal"; got != want {
		t.Errorf("row 0 = %q, want %q", got, want)
	}
	tm.SetColors(Colors{Fg: "#000000", Bg: "#ffffff"})
	if got, want := tm.Row(1), "still plain"; got != want {
		t.Errorf("row 1 = %q, want %q: the terminal's default colours never reach the row", got, want)
	}
	for _, c := range []string{"38;2;230;231;235", "48;2;48;50;58", "39", "49"} {
		if strings.Contains(tm.Row(0)+tm.Row(1), c) {
			t.Errorf("a default colour is encoded as %q", c)
		}
	}
}

func TestTheScreenRowOfALiveRowEqualsRow(t *testing.T) {
	tm := termAt(t, 30, 4)
	tm.Write([]byte("\x1b[1;31mred\x1b[0m 漢\U0001F44D\U0001F3FD \x1b]8;;https://e.x\x1b\\l\x1b]8;;\x1b\\\r\n"))
	for i := range 10 {
		fmt.Fprintf(writerOf(tm), "line %d\r\n", i)
	}
	sb := tm.scrollbackRows()
	for y := range 4 {
		got, ok := tm.screenRow(sb + y)
		if !ok || got != tm.Row(y) {
			t.Errorf("screen row %d = %q, %v; Row(%d) = %q", sb+y, got, ok, y, tm.Row(y))
		}
	}
	if got, ok := tm.screenRow(0); !ok || got != "\x1b[0;1;31mred\x1b[0m 漢\U0001F44D\U0001F3FD \x1b]8;;https://e.x\x1b\\l" {
		t.Errorf("history row 0 = %q, %v", got, ok)
	}
	if _, ok := tm.screenRow(sb + 4); ok {
		t.Errorf("a row below the screen is reported")
	}
}

// TestHistoryRowsKeepLongClustersLikeLiveRows: the grid-reference reader of
// history rows hands over every cluster the render-state reader of live rows
// does, around the sixteen code points of its first buffer and around the
// cluster limit of the encoder, at the end of a row and inside it; a cell
// it replaces is written as U+FFFD like the live reader's, never omitted as
// a trailing blank.
func TestHistoryRowsKeepLongClustersLikeLiveRows(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 17, 40, 126, 127, 128, 255, 256, 257, 300} {
		cluster := "a" + strings.Repeat("́", n)
		for _, line := range []string{cluster, "x" + cluster + "y", "\x1b[1m" + cluster + "\x1b[0m"} {
			tm := termAt(t, 20, 3)
			tm.Write([]byte(line))
			live := tm.Row(0)
			if live == "" {
				t.Fatalf("%d accents, %q: the live row is empty", n, line)
			}
			sb := tm.scrollbackRows()
			if got, ok := tm.screenRow(sb); !ok || got != live {
				t.Errorf("%d accents, %q: screen row of live row 0 = %q, %v; Row(0) = %q", n, line, got, ok, live)
			}
			tm.Write([]byte("\r\n\r\n\r\n\r\n"))
			if tm.scrollbackRows() == 0 {
				t.Fatalf("%d accents: the row did not reach the history", n)
			}
			if got, ok := tm.screenRow(0); !ok || got != live {
				t.Errorf("%d accents, %q: history row 0 = %q, %v; the live row was %q", n, line, got, ok, live)
			}
		}
	}
	// A cell the reader could not hand over is U+FFFD, also at the end of
	// a row: leaving it out would shorten the row.
	var e encoder
	for _, cells := range [][]cell{{{bad: true}}, {{text: []byte("a")}, {bad: true}, {}}} {
		if got := e.encode(cells); !strings.HasSuffix(got, "�") {
			t.Errorf("cells %v encode as %q: the unreadable cell is left out", cells, got)
		}
	}
}

func BenchmarkRow200x50(b *testing.B) {
	tm := termAt(b, 200, 50)
	var s strings.Builder
	for y := range 50 {
		for x := range 20 {
			fmt.Fprintf(&s, "\x1b[3%dm%-9d\x1b[0m ", (x+y)%8, x*y)
		}
		if y < 49 {
			s.WriteString("\r\n")
		}
	}
	tm.Write([]byte(s.String()))
	b.ResetTimer()
	for b.Loop() {
		tm.dirty = true
		for y := range 50 {
			_ = tm.Row(y)
		}
	}
}

// writerOf lets fmt write to a terminal.
type termWriter struct{ t *term }

func (w termWriter) Write(p []byte) (int, error) { w.t.Write(p); return len(p), nil }

func writerOf(t *term) termWriter { return termWriter{t} }
