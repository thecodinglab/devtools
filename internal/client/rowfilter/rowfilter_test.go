package rowfilter_test

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"devtools/internal/client/rowfilter"
	"devtools/internal/client/screen"
	"devtools/internal/proto"
	"github.com/charmbracelet/x/ansi"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket or a multiplexer reaches them (10.1). The
// filter reads no environment; this is the convention.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-rowfilter-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// readRows reads one of the example files of internal/proto/testdata/rows:
// one Go-quoted string per line.
func readRows(tb testing.TB, name string) []string {
	tb.Helper()
	f, err := os.Open("../../proto/testdata/rows/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	var rows []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, err := strconv.Unquote(line)
		if err != nil {
			tb.Fatalf("%s:%d: %v", name, n, err)
		}
		rows = append(rows, s)
	}
	if err := sc.Err(); err != nil {
		tb.Fatal(err)
	}
	return rows
}

func mustParse(t *testing.T, s string, cols int) []screen.Cell {
	t.Helper()
	cells, err := rowfilter.Parse(s, cols)
	if err != nil {
		t.Fatalf("Parse(%q, %d): %v", s, cols, err)
	}
	return cells
}

func mustReject(t *testing.T, s string, cols int) {
	t.Helper()
	if cells, err := rowfilter.Parse(s, cols); err == nil {
		t.Errorf("Parse(%q, %d) accepted the row as %d cells", s, cols, len(cells))
	} else if cells != nil {
		t.Errorf("Parse(%q, %d) returned cells with its error", s, cols)
	}
}

// text is the text of cells, the second cells of wide characters left out.
func text(cells []screen.Cell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(c.S)
	}
	return b.String()
}

func width(cells []screen.Cell) int {
	w := 0
	for _, c := range cells {
		w += int(c.W)
	}
	return w
}

func TestAcceptsEveryValidExampleRow(t *testing.T) {
	rows := readRows(t, "valid.txt")
	if len(rows) < 20 {
		t.Fatalf("only %d example rows", len(rows))
	}
	for i, row := range rows {
		cells, err := rowfilter.Parse(row, 80)
		if err != nil {
			t.Errorf("valid.txt:%d %q: %v", i+1, row, err)
			continue
		}
		checkCells(t, row, 80, cells)
		// The cells hold the row's text, every byte of it, and nothing
		// of its sequences.
		if got, want := text(cells), ansi.Strip(row); got != want {
			t.Errorf("valid.txt:%d %q: text %q, want %q", i+1, row, got, want)
		}
		if got, want := width(cells), ansi.StringWidth(row); got != want {
			t.Errorf("valid.txt:%d %q: %d cells wide, ansi.StringWidth says %d", i+1, row, got, want)
		}
	}
}

func TestRejectsEveryInvalidExampleRow(t *testing.T) {
	rows := readRows(t, "invalid.txt")
	if len(rows) < 30 {
		t.Fatalf("only %d example rows", len(rows))
	}
	for _, row := range rows {
		mustReject(t, row, 80)
	}
}

func TestCellsCarryTextWidthStyleAndLink(t *testing.T) {
	row := "a\x1b[0;1;3;4:3;31;48;5;236;58;2;1;2;3m日\x1b]8;;https://example.com/x\x1b\\e\u0301\x1b[0m😀\x1b]8;;\x1b\\z"
	got := mustParse(t, row, 20)
	styled := screen.Style{
		Fg:        screen.Color{Kind: 1, V: 1},
		Bg:        screen.Color{Kind: 2, V: 236},
		Ul:        screen.Color{Kind: 3, V: 0x010203},
		Attrs:     screen.Bold | screen.Italic,
		Underline: 3,
	}
	linked := styled
	linked.Link = "https://example.com/x"
	want := []screen.Cell{
		{S: "a", W: 1},
		{S: "日", W: 2, Style: styled},
		{S: "", W: 0, Style: styled},
		{S: "e\u0301", W: 1, Style: linked},
		{S: "😀", W: 2, Style: screen.Style{Link: "https://example.com/x"}},
		{S: "", W: 0, Style: screen.Style{Link: "https://example.com/x"}},
		{S: "z", W: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("%d cells %#v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d = %#v, want %#v", i, got[i], want[i])
		}
	}

	// Every colour form and every attribute of the list.
	for _, c := range []struct {
		row  string
		want screen.Style
	}{
		{"\x1b[0;2;5;7;8;9mx", screen.Style{Attrs: screen.Faint | screen.Blink | screen.Reverse | screen.Conceal | screen.Strike}},
		{"\x1b[0;4mx", screen.Style{Underline: 1}},
		{"\x1b[0;4:2mx", screen.Style{Underline: 2}},
		{"\x1b[0;4:4mx", screen.Style{Underline: 4}},
		{"\x1b[0;4:5mx", screen.Style{Underline: 5}},
		{"\x1b[0;37;40mx", screen.Style{Fg: screen.Color{Kind: 1, V: 7}, Bg: screen.Color{Kind: 1, V: 0}}},
		{"\x1b[0;90;107mx", screen.Style{Fg: screen.Color{Kind: 1, V: 8}, Bg: screen.Color{Kind: 1, V: 15}}},
		{"\x1b[0;97;100mx", screen.Style{Fg: screen.Color{Kind: 1, V: 15}, Bg: screen.Color{Kind: 1, V: 8}}},
		{"\x1b[0;38;5;0;48;5;255mx", screen.Style{Fg: screen.Color{Kind: 2, V: 0}, Bg: screen.Color{Kind: 2, V: 255}}},
		{"\x1b[0;38;2;255;128;0;48;2;0;0;0mx", screen.Style{Fg: screen.Color{Kind: 3, V: 0xff8000}, Bg: screen.Color{Kind: 3, V: 0}}},
		{"\x1b[0;58;5;208mx", screen.Style{Ul: screen.Color{Kind: 2, V: 208}}},
		{"\x1b[0;38;5;007mx", screen.Style{Fg: screen.Color{Kind: 2, V: 7}}},
		{"\x1b[0;31;32mx", screen.Style{Fg: screen.Color{Kind: 1, V: 2}}}, // the later attribute wins
		{"\x1b[0;1m\x1b[0;3mx", screen.Style{Attrs: screen.Italic}},       // every sgr starts from reset
		{"\x1b[0;1m\x1b[0mx", screen.Style{}},
	} {
		cells := mustParse(t, c.row, 10)
		if len(cells) != 1 || cells[0].S != "x" || cells[0].W != 1 || cells[0].Style != c.want {
			t.Errorf("Parse(%q) = %#v, want one cell x with %#v", c.row, cells, c.want)
		}
	}

	// A reset does not close the link; an empty uri does, and a link left
	// open ends with the row.
	cells := mustParse(t, "\x1b]8;;a:b\x1b\\x\x1b[0;1my\x1b[0mz\x1b]8;;\x1b\\w", 10)
	for i, want := range []string{"a:b", "a:b", "a:b", ""} {
		if cells[i].Style.Link != want {
			t.Errorf("cell %d %q has link %q, want %q", i, cells[i].S, cells[i].Style.Link, want)
		}
	}
	if cells := mustParse(t, "\x1b]8;;a:b\x1b\\", 10); len(cells) != 0 {
		t.Errorf("a link without text made cells: %#v", cells)
	}
}

func TestRejectsAnySequenceButSGRAndOSC8(t *testing.T) {
	for _, s := range []string{
		"\x1b[2J", "\x1b[K", "\x1b[H", "\x1b[1;1H", "\x1b[A", "\x1b[?25l", "\x1b[?2026h",
		"\x1b[>4;1m", "\x1b[0 q", "\x1b[6n", "\x1b[c", "\x1b[0;1H", "\x1b[0;1r", "\x1b[0;1M",
		"\x1b[0;1m\x1b[s", "\x1b]0;title\x1b\\", "\x1b]2;title\x1b\\", "\x1b]7;file:///\x1b\\",
		"\x1b]52;c;aGk=\x1b\\", "\x1b]10;?\x1b\\", "\x1b]133;A\x1b\\", "\x1b]7501;x\x1b\\",
		"\x1b]8;id=x;u\x1b\\", "\x1b]8;u\x1b\\", "\x1b]9;note\x1b\\", "\x1b]80;;u\x1b\\",
		"\x1bPq\x1b\\", "\x1bP+q544e\x1b\\", "\x1b_Gf=1\x1b\\", "\x1b^pm\x1b\\", "\x1bXsos\x1b\\",
		"\x1b7", "\x1b8", "\x1bc", "\x1bD", "\x1bE", "\x1bM", "\x1b=", "\x1b>", "\x1b(0", "\x1b#8",
		"\x1b\\", "\x1b", "x\x1b", "\x1b[", "\x1b]", "\x1b]8", "\x1b]8;", "\x1b]8;;",
		"\x1b\x1b[0m", "\x1b[0;1\x1b[0m", "\x1b[0;1;\x1bm",
	} {
		mustReject(t, s, 80)
		mustReject(t, "ok "+s+" ok", 80)
	}
	// The two sequences of the grammar are accepted.
	mustParse(t, "\x1b[0;1mok\x1b]8;;u\x1b\\ok\x1b]8;;\x1b\\", 80)
}

func TestRejectsRelativeSGR(t *testing.T) {
	for _, s := range []string{
		"\x1b[m", "\x1b[1m", "\x1b[22m", "\x1b[39m", "\x1b[49m", "\x1b[24m", "\x1b[31m",
		"\x1b[;1m", "\x1b[00m", "\x1b[01m", "\x1b[0:1m", "\x1b[ 0m",
		"\x1b[0;0m", "\x1b[0;22m", "\x1b[0;23m", "\x1b[0;24m", "\x1b[0;25m", "\x1b[0;27m",
		"\x1b[0;28m", "\x1b[0;29m", "\x1b[0;39m", "\x1b[0;49m", "\x1b[0;59m", "\x1b[0;4:0m",
		"\x1b[0;1;0m", "\x1b[0;1;22m",
	} {
		mustReject(t, s+"x", 80)
	}
}

func TestRejectsAnAttributeOutsideTheList(t *testing.T) {
	// Every single parameter up to 999, with and without a colon form: the
	// accepted ones are exactly the attributes of 4.2.5.
	accepted := map[string]bool{"1": true, "2": true, "3": true, "5": true, "7": true, "8": true, "9": true, "4": true}
	for i := 30; i <= 37; i++ {
		accepted[strconv.Itoa(i)] = true
		accepted[strconv.Itoa(i+60)] = true
		accepted[strconv.Itoa(i+10)] = true
		accepted[strconv.Itoa(i+70)] = true
	}
	for n := 0; n <= 999; n++ {
		for _, f := range []string{strconv.Itoa(n), fmt.Sprintf("%02d", n), fmt.Sprintf("%03d", n)} {
			_, err := rowfilter.Parse("\x1b[0;"+f+"mx", 10)
			if accepted[f] != (err == nil) {
				t.Errorf("attribute %q: accepted %v, want %v", f, err == nil, accepted[f])
			}
		}
	}
	for _, f := range []string{"4:2", "4:3", "4:4", "4:5"} {
		mustParse(t, "\x1b[0;"+f+"mx", 10)
	}
	for _, f := range []string{"4:0", "4:1", "4:6", "4:9", "4:", ":4", "4::2", "1:2", "4:2:1", "4:22", "3:3", "38:5:1", "38:2:1:2:3", "48:5:1"} {
		mustReject(t, "\x1b[0;"+f+"mx", 10)
	}
	// The extended colours: 5 with one n, 2 with three, n of 1 to 3 digits
	// up to 255.
	for _, base := range []string{"38", "48", "58"} {
		for _, ok := range []string{";5;0", ";5;255", ";5;07", ";2;0;0;0", ";2;255;255;255", ";2;001;002;003"} {
			mustParse(t, "\x1b[0;"+base+ok+"mx", 10)
			mustParse(t, "\x1b[0;"+base+ok+";1mx", 10)
		}
		for _, bad := range []string{"", ";5", ";5;", ";5;256", ";5;1000", ";5;-1", ";2;1;2", ";2;1;2;", ";2;1;2;256", ";3;1", ";6;1", ";1;1", ";0", ";5;;1", ";2;1;;2;3", ";2"} {
			mustReject(t, "\x1b[0;"+base+bad+"mx", 10)
		}
	}
}

func TestRejectsC0DELAndC1(t *testing.T) {
	for b := 0; b < 0x20; b++ {
		if b == 0x1b {
			continue // ESC starts a sequence; alone it is refused by TestRejectsAnySequenceButSGRAndOSC8
		}
		mustReject(t, "a"+string(rune(b))+"b", 80)
		mustReject(t, string(rune(b)), 80)
	}
	mustReject(t, "a\x7fb", 80)
	for r := rune(0x80); r <= 0x9f; r++ {
		mustReject(t, "a"+string(r)+"b", 80)
		mustReject(t, "\x1b[0;1m"+string(r), 80)
	}
	// Raw C1 bytes, a cut sequence, surrogates, overlong forms and code
	// points above U+10FFFF are not UTF-8 of scalars.
	for _, s := range []string{"\x85", "\x9b", "\xc2", "\xc0\x80", "\xc1\xbf", "\xe0\x80\x80", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xf8\x88\x80\x80\x80", "\xff", "a\xe6\x97"} {
		mustReject(t, s, 80)
	}
	// The scalars at the edges of the text set are accepted.
	for _, s := range []string{" ", "~", "\u00a0", "\u00ff", "\uFFFD", "\U0001F600"} {
		mustParse(t, s, 80)
	}
}

func TestRejectsOSC8EndedByBEL(t *testing.T) {
	mustReject(t, "\x1b]8;;https://example.com\x07x", 80)
	mustReject(t, "\x1b]8;;\x07x", 80)
	mustReject(t, "\x1b]8;;https://example.com\x9cx", 80)
	mustReject(t, "\x1b]8;;https://example.com\u009cx", 80)
	mustParse(t, "\x1b]8;;https://example.com\x1b\\x", 80)
}

func TestRejectsALinkWithBytesOutsideTheURISet(t *testing.T) {
	for b := 0; b < 0x100; b++ {
		if b >= 0x21 && b <= 0x7e {
			cells := mustParse(t, "\x1b]8;;a"+string([]byte{byte(b)})+"b\x1b\\x", 10)
			if want := "a" + string([]byte{byte(b)}) + "b"; cells[0].Style.Link != want {
				t.Errorf("link %q, want %q", cells[0].Style.Link, want)
			}
			continue
		}
		mustReject(t, "\x1b]8;;a"+string([]byte{byte(b)})+"b\x1b\\x", 10)
	}
	for _, uri := range []string{"exa mple", "é", "\u00a0", "a\u0085b", "\U0001F600"} {
		mustReject(t, "\x1b]8;;"+uri+"\x1b\\x", 10)
	}
	long := strings.Repeat("u", 2048)
	if cells := mustParse(t, "\x1b]8;;"+long+"\x1b\\x", 10); cells[0].Style.Link != long {
		t.Error("a link of 2048 bytes is not the cell's link")
	}
	mustReject(t, "\x1b]8;;"+long+"u\x1b\\x", 10)
}

func TestRejectsARowLongerThanTheLimit(t *testing.T) {
	const limit = 128 << 10
	if proto.MaxRow != limit {
		t.Fatalf("proto.MaxRow is %d, the filter's limit %d", proto.MaxRow, limit)
	}
	// A row of exactly the limit, which a server may send, is accepted.
	sgr := "\x1b[0;1m"
	exact := strings.Repeat(sgr, limit/len(sgr))
	exact += strings.Repeat("x", limit-len(exact))
	if len(exact) != limit {
		t.Fatalf("built a row of %d bytes", len(exact))
	}
	if cells := mustParse(t, exact, 1000); len(cells) != limit%len(sgr) {
		t.Errorf("%d cells", len(cells))
	}
	mustReject(t, exact+"x", 1000)
	mustReject(t, strings.Repeat("x", limit+1), 1000)
	mustReject(t, strings.Repeat("\x1b[0m", limit/4+1), 1000)
}

func TestRejectsARowClearlyWiderThanCols(t *testing.T) {
	mustReject(t, "abcdefghijkl", 10) // two clusters past
	mustReject(t, "日本語日本語x", 10)      // 13 cells
	mustReject(t, "abcdefghij日x", 10) // the overflowing cluster is not the last
	mustReject(t, "ab", 0)
	mustReject(t, strings.Repeat("x", 1000), 80)
	// A row exactly cols wide is accepted as it is.
	if cells := mustParse(t, "abcdefghij", 10); width(cells) != 10 {
		t.Errorf("width %d", width(cells))
	}
	if cells := mustParse(t, "日本語日本", 10); width(cells) != 10 || len(cells) != 10 {
		t.Errorf("width %d, %d cells", width(cells), len(cells))
	}
}

func TestClipsWhenOnlyTheLastClusterOverflows(t *testing.T) {
	for _, c := range []struct {
		row  string
		cols int
		want string
	}{
		{"abcdefghijk", 10, "abcdefghij"},
		{"abcdefghi日", 10, "abcdefghi"},   // a wide last cluster that half fits is cut whole
		{"abcdefghij日", 10, "abcdefghij"}, // and one that does not fit at all
		{"日本語日本語", 11, "日本語日本"},
		{"\x1b[0;1mabc\x1b[0md\x1b]8;;u\x1b\\😀\x1b]8;;\x1b\\", 4, "abcd"}, // sequences after the cut are still read
		{"a", 0, ""},
		{"日", 1, ""},
	} {
		cells := mustParse(t, c.row, c.cols)
		if got := text(cells); got != c.want {
			t.Errorf("Parse(%q, %d) = %q, want %q", c.row, c.cols, got, c.want)
		}
		if width(cells) > c.cols {
			t.Errorf("Parse(%q, %d) is %d wide", c.row, c.cols, width(cells))
		}
	}
	// A sequence that is not of the grammar after the cut is still refused.
	mustReject(t, "abcdefghijk\x1b[2J", 10)
}

func TestEmptyRowIsBlank(t *testing.T) {
	for _, s := range []string{"", "\x1b[0m", "\x1b[0;41m", "\x1b]8;;u\x1b\\", "\x1b[0;1m\x1b[0m\x1b]8;;\x1b\\"} {
		for _, cols := range []int{0, 1, 80} {
			if cells := mustParse(t, s, cols); len(cells) != 0 {
				t.Errorf("Parse(%q, %d) = %#v, want no cells", s, cols, cells)
			}
		}
	}
}

func TestZeroWidthClusterIsOneReplacementCell(t *testing.T) {
	// A cluster x/ansi gives no width is never what the server's encoder
	// writes; it stands for one cell of U+FFFD (provisional E9).
	for _, s := range []string{"\u200b", "\u0301", "\u200d"} {
		cells := mustParse(t, "a"+s, 10)
		if len(cells) == 1 && cells[0].S == "a"+s && cells[0].W == 1 {
			continue // joined the cluster before it
		}
		if len(cells) != 2 || cells[1].S != "\uFFFD" || cells[1].W != 1 {
			t.Errorf("Parse(%q) = %#v", "a"+s, cells)
		}
	}
	cells := mustParse(t, "\x1b[0;1m\u0301", 10)
	if len(cells) != 1 || cells[0].S != "\uFFFD" || cells[0].W != 1 || cells[0].Style.Attrs != screen.Bold {
		t.Errorf("a lone combining mark after an sgr = %#v", cells)
	}
}

// checkCells holds the properties of FuzzParse for one accepted row.
func checkCells(tb testing.TB, s string, cols int, cells []screen.Cell) {
	tb.Helper()
	w := 0
	for i, c := range cells {
		switch c.W {
		case 0:
			if i == 0 || cells[i-1].W != 2 || c.S != "" || c.Style != cells[i-1].Style {
				tb.Fatalf("Parse(%q, %d): cell %d of width 0 is not the second cell of a wide character", s, cols, i)
			}
		case 1, 2:
			if c.S == "" {
				tb.Fatalf("Parse(%q, %d): cell %d has no text", s, cols, i)
			}
			if c.W == 2 && (i+1 >= len(cells) || cells[i+1].W != 0) {
				tb.Fatalf("Parse(%q, %d): wide cell %d without its second cell", s, cols, i)
			}
		default:
			tb.Fatalf("Parse(%q, %d): cell %d has width %d", s, cols, i, c.W)
		}
		w += int(c.W)
		if !utf8.ValidString(c.S) {
			tb.Fatalf("Parse(%q, %d): cell %d text %q is not UTF-8", s, cols, i, c.S)
		}
		for _, r := range c.S {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				tb.Fatalf("Parse(%q, %d): cell %d text %q holds %U", s, cols, i, c.S, r)
			}
		}
		if l := c.Style.Link; len(l) > 2048 || strings.ContainsFunc(l, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
			tb.Fatalf("Parse(%q, %d): cell %d link %q outside the URI set", s, cols, i, l)
		}
		st := c.Style
		if st.Underline > 5 || st.Attrs&^(screen.Bold|screen.Faint|screen.Italic|screen.Blink|screen.Reverse|screen.Conceal|screen.Strike) != 0 {
			tb.Fatalf("Parse(%q, %d): cell %d style %#v outside the grammar", s, cols, i, st)
		}
	}
	if w > cols {
		tb.Fatalf("Parse(%q, %d): %d cells wide", s, cols, w)
	}
}
