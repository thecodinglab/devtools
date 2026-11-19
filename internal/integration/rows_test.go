package integration

// The cross-package row tests of 10.1.5: the engine's row encoder on the
// server and the client's row filter and canvas meet here, the two halves
// of the row grammar of 4.2.5. This test-only package may import both
// sides.

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"devtools/internal/client/rowfilter"
	"devtools/internal/client/screen"
	"devtools/internal/vt"
)

// encode draws cells into a canvas one row high and returns the canvas's
// encoding of that row, the client's half of the grammar.
func encode(cols int, cells []screen.Cell) string {
	c := screen.New(cols, 1)
	c.Cells(0, 0, cells)
	return c.Lines()[0]
}

// textCluster is the most code points of one cell that Term.Text writes;
// a longer cluster is U+FFFD there (internal/vt/history.go, dt_text),
// while the row carries it whole.
const textCluster = 16

// cellText is the text of cells as Term.Text writes a line: one character
// per cell, the second cell of a wide character left out, trailing blanks
// removed.
func cellText(cells []screen.Cell) string {
	var b strings.Builder
	for _, c := range cells {
		switch {
		case c.W == 0:
		case utf8.RuneCountInString(c.S) > textCluster:
			b.WriteRune(utf8.RuneError)
		default:
			b.WriteString(c.S)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// checkRows asserts, for every live row of tm, that the filter parses the
// engine's row, that the parsed cells carry the engine's text and fill at
// most the width, and that the canvas writes them back as the same cells.
// A cell the encoder replaced with U+FFFD, because it would join the
// cluster before it when read (3.2.2), stands for the engine's cell there.
func checkRows(t testing.TB, what string, tm vt.Term) {
	t.Helper()
	cols, rows := tm.Size()
	_, base := tm.Range()
	for y := range rows {
		row := tm.Row(y)
		cells, err := rowfilter.Parse(row, cols)
		if err != nil {
			t.Fatalf("%s: row %d does not parse: %v\nrow %q", what, y, err, row)
		}
		width := 0
		for i, c := range cells {
			width += int(c.W)
			if c.W == 2 && (i+1 >= len(cells) || cells[i+1].W != 0) {
				t.Fatalf("%s: row %d: the wide cell %d has no second cell", what, y, i)
			}
		}
		if width > cols {
			t.Fatalf("%s: row %d is %d cells wide in a terminal of %d", what, y, width, cols)
		}
		text, wrapped, ok := tm.Text(base + int64(y))
		if !ok {
			t.Fatalf("%s: row %d has no text", what, y)
		}
		if wrapped {
			text = strings.TrimRight(text, " ")
		}
		if got := cellText(cells); got != text && !sameButReplaced(got, text) {
			t.Fatalf("%s: row %d: the cells read %q, the engine's text is %q\nrow %q", what, y, got, text, row)
		}
		// The canvas writes the cells again, as the client sends them to the
		// outer terminal. The two encoders need not write the same bytes,
		// and they do not: the canvas ends a row whose last cell is styled
		// with a reset and closes a link left open, so that nothing carries
		// over in the outer terminal (the grammar ends both with the row);
		// it opens a link before the SGR of the cell where the engine
		// writes the SGR first; and the engine writes a wide cell it
		// replaced as U+FFFD and a blank (3.2.2), whose blank the canvas
		// leaves out at the end of a row. What must hold is that both
		// strings are the same cells, and that the canvas's form is stable.
		again := encode(cols, cells)
		back, err := rowfilter.Parse(again, cols)
		if err != nil {
			t.Fatalf("%s: row %d: the canvas wrote %q, which does not parse: %v", what, y, again, err)
		}
		if d := cellDiff(back, cells); d != "" {
			t.Fatalf("%s: row %d: %s\nthe canvas writes %q, the engine wrote %q", what, y, d, again, row)
		}
		if third := encode(cols, back); third != again {
			t.Fatalf("%s: row %d: the canvas writes %q, then %q for the same cells", what, y, again, third)
		}
	}
}

// cellDiff describes the first difference between two rows of cells,
// trailing blanks in the default style left out; "" when they are equal.
func cellDiff(got, want []screen.Cell) string {
	trim := func(cs []screen.Cell) []screen.Cell {
		for len(cs) > 0 && cs[len(cs)-1] == (screen.Cell{S: " ", W: 1}) {
			cs = cs[:len(cs)-1]
		}
		return cs
	}
	got, want = trim(got), trim(want)
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("cell %d is %#v, want %#v", i, got[i], want[i])
		}
	}
	if len(got) != len(want) {
		return fmt.Sprintf("%d cells, want %d", len(got), len(want))
	}
	return ""
}

// sameButReplaced reports whether got is want with the text of some cells
// replaced by U+FFFD, as the encoder writes a cluster that would join the
// one before it (3.2.2). A replaced cell's text is at least one rune; a
// replaced wide cell reads as U+FFFD and a blank.
func sameButReplaced(got, want string) bool {
	parts := strings.Split(strings.ReplaceAll(got, "� ", "�"), "�")
	if len(parts) < 2 || !strings.HasPrefix(want, parts[0]) {
		return false
	}
	rest := want[len(parts[0]):]
	for i, p := range parts[1:] {
		if rest == "" {
			return false
		}
		_, size := utf8.DecodeRuneInString(rest)
		rest = rest[size:]
		if i == len(parts)-2 {
			return strings.HasSuffix(rest, p)
		}
		j := strings.Index(rest, p)
		if j < 0 {
			return false
		}
		rest = rest[j+len(p):]
	}
	return false
}

// conformCases returns the C02 (style) and C03 (width) cases of the
// engine's conformance set: their size, colour, write and resize steps.
func conformCases(t *testing.T) map[string][][]string {
	t.Helper()
	files, err := filepath.Glob("../vt/testdata/conform/c0[23]-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("only %d C02 and C03 cases under internal/vt/testdata/conform", len(files))
	}
	out := map[string][][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var steps [][]string
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || line[0] == '#' {
				continue
			}
			toks, err := tokens(line)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			switch toks[0] {
			case "size", "write", "resize", "colors":
				steps = append(steps, toks)
			}
		}
		out[filepath.Base(f)] = steps
	}
	return out
}

// tokens splits a case line into words and Go-quoted strings, unquoted.
func tokens(s string) ([]string, error) {
	var out []string
	for s = strings.TrimLeftFunc(s, unicode.IsSpace); s != ""; s = strings.TrimLeftFunc(s, unicode.IsSpace) {
		if s[0] == '"' || s[0] == '`' {
			q, err := strconv.QuotedPrefix(s)
			if err != nil {
				return nil, fmt.Errorf("bad quoted string at %q", s)
			}
			u, _ := strconv.Unquote(q)
			out = append(out, u)
			s = s[len(q):]
			continue
		}
		i := strings.IndexFunc(s, unicode.IsSpace)
		if i < 0 {
			i = len(s)
		}
		out = append(out, s[:i])
		s = s[i:]
	}
	return out, nil
}

// runCase feeds the steps of a case to a new terminal and checks its rows
// after every write and resize.
func runCase(t *testing.T, name string, steps [][]string) {
	t.Helper()
	cols, rows := 80, 24
	var tm vt.Term
	open := func() vt.Term {
		if tm == nil {
			tm = vt.New(vt.Options{Cols: cols, Rows: rows, History: 10000})
			t.Cleanup(tm.Close)
		}
		return tm
	}
	num := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("%s: %q is not a number", name, s)
		}
		return n
	}
	for i, st := range steps {
		switch st[0] {
		case "size":
			cols, rows = num(st[1]), num(st[2])
			continue
		case "colors":
			open().SetColors(vt.Colors{Fg: st[1], Bg: st[2], Dark: len(st) > 3 && st[3] == "dark"})
			continue
		case "write":
			open().Write([]byte(st[1]))
		case "resize":
			open().Resize(num(st[1]), num(st[2]))
		}
		tm.Replies()
		checkRows(t, fmt.Sprintf("%s step %d", name, i+1), tm)
	}
}

// replayRecordings returns the recordings of the replay group C16
// (internal/vt/testdata/replay, the advanced timing format of util-linux
// script): the size from the header, then each output and resize.
// provisional (E1): until the user's recordings exist the directory holds
// the synthetic recording of internal/vt.
func replayRecordings(t *testing.T) map[string][][]string {
	t.Helper()
	timings, err := filepath.Glob("../vt/testdata/replay/*.tm")
	if err != nil {
		t.Fatal(err)
	}
	if len(timings) == 0 {
		t.Fatal("internal/vt/testdata/replay holds no recording")
	}
	out := map[string][][]string{}
	for _, tmf := range timings {
		raw, err := os.ReadFile(strings.TrimSuffix(tmf, ".tm") + ".raw")
		if err != nil {
			t.Fatal(err)
		}
		if bytes.HasPrefix(raw, []byte("Script started")) {
			raw = raw[bytes.IndexByte(raw, '\n')+1:]
		}
		timing, err := os.ReadFile(tmf)
		if err != nil {
			t.Fatal(err)
		}
		var cols, rows string
		var steps [][]string
		off := 0
		for _, line := range strings.Split(string(timing), "\n") {
			fs := strings.Fields(line)
			if len(fs) < 3 {
				continue
			}
			switch fs[0] {
			case "H":
				if len(fs) > 3 && fs[2] == "COLUMNS" {
					cols = fs[3]
				}
				if len(fs) > 3 && fs[2] == "LINES" {
					rows = fs[3]
				}
			case "O":
				n, _ := strconv.Atoi(fs[2])
				if off+n > len(raw) {
					t.Fatalf("%s: the output log ends before the timing log", tmf)
				}
				steps = append(steps, []string{"write", string(raw[off : off+n])})
				off += n
			case "S":
				if fs[2] != "SIGWINCH" {
					continue
				}
				var c, r string
				for _, kv := range fs[3:] {
					if v, ok := strings.CutPrefix(kv, "ROWS="); ok {
						r = v
					}
					if v, ok := strings.CutPrefix(kv, "COLS="); ok {
						c = v
					}
				}
				steps = append(steps, []string{"resize", c, r})
			}
		}
		if cols == "" || rows == "" {
			t.Fatalf("%s: no COLUMNS and LINES header", tmf)
		}
		out[filepath.Base(tmf)] = append([][]string{{"size", cols, rows}}, steps...)
	}
	return out
}

func TestEveryEngineRowSurvivesTheWire(t *testing.T) {
	for name, steps := range conformCases(t) {
		t.Run(name, func(t *testing.T) { runCase(t, name, steps) })
	}
	for name, steps := range replayRecordings(t) {
		t.Run(name, func(t *testing.T) { runCase(t, name, steps) })
	}
}

func FuzzRowRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"hello\r\nworld",
		"\x1b[1;31mred\x1b[0m \x1b[4:3;58;5;1mcurly\x1b[0m",
		"\x1b[38;2;1;2;3;48;5;200m rgb \x1b[7mrev\x1b[0m",
		"漢字abc 😀 👍🏽 🇩🇪 é",
		"\x1b[1;3H\U0001F3FD\x1b[1;1H\U0001F44D",
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ after",
		"\x1b[?1049h\x1b[2Jalt\x1b[5;5Hx\x1b[?1049l",
		"wrap " + strings.Repeat("0123456789", 9),
		"\t\ttabs\x08\x08bs\x1b[2K\x1b[3@ins",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		tm := vt.New(vt.Options{Cols: 40, Rows: 8, History: 100})
		defer tm.Close()
		tm.Write(data)
		tm.Replies()
		checkRows(t, "after the write", tm)
		tm.Resize(23, 5)
		tm.Replies()
		checkRows(t, "after a resize", tm)
	})
}

// The clusters and styles of the canvas test: every cluster has the same
// width in the canvas and in the filter (both measure with x/ansi), and no
// two of them join when written side by side.
var (
	canvasClusters = []string{"a", "Z", "0", " ", "~", "ä", "ß", "€", "é", "é", "Ω", "→", "日", "語", "😀", "👍🏽"}
	canvasLinks    = []string{"", "", "", "https://example.com/a?b=c", "file:///tmp/x"}
)

func randomColor(r *rand.Rand) screen.Color {
	switch r.IntN(4) {
	case 1:
		return screen.Color{Kind: 1, V: uint32(r.IntN(16))}
	case 2:
		return screen.Color{Kind: 2, V: uint32(16 + r.IntN(240))}
	case 3:
		return screen.Color{Kind: 3, V: uint32(r.IntN(1 << 24))}
	}
	return screen.Color{}
}

func randomStyle(r *rand.Rand) screen.Style {
	var st screen.Style
	if r.IntN(3) == 0 {
		return st
	}
	st.Attrs = screen.Attr(r.IntN(1 << 7))
	if r.IntN(3) == 0 {
		st.Underline = uint8(1 + r.IntN(5))
		st.Ul = randomColor(r)
	}
	st.Fg, st.Bg = randomColor(r), randomColor(r)
	st.Link = canvasLinks[r.IntN(len(canvasLinks))]
	return st
}

func TestCanvasRenderSurvivesTheRowFilter(t *testing.T) {
	const cols, rows = 60, 12
	r := rand.New(rand.NewPCG(18, 3))
	for n := range 300 {
		c := screen.New(cols, rows)
		// Text and styles in random runs, some of them overlapping, so that
		// later writes cut wide characters of earlier ones.
		for range 20 {
			x, y := r.IntN(cols), r.IntN(rows)
			var b strings.Builder
			for range 1 + r.IntN(12) {
				b.WriteString(canvasClusters[r.IntN(len(canvasClusters))])
			}
			c.Text(x, y, b.String(), randomStyle(r), cols-x)
		}
		if r.IntN(2) == 0 {
			c.Fill(screen.Rect{X: r.IntN(cols), Y: r.IntN(rows), W: 1 + r.IntN(10), H: 1 + r.IntN(3)},
				screen.Cell{S: " ", W: 1, Style: randomStyle(r)})
		}
		lines := c.Lines()
		again := screen.New(cols, rows)
		for y, row := range lines {
			cells, err := rowfilter.Parse(row, cols)
			if err != nil {
				t.Fatalf("canvas %d row %d does not parse: %v\nrow %q", n, y, err, row)
			}
			for x, cell := range cells {
				want := c.At(x, y)
				if cell.S != want.S || cell.W != want.W {
					t.Fatalf("canvas %d row %d cell %d is %q/%d after the filter, %q/%d in the canvas\nrow %q",
						n, y, x, cell.S, cell.W, want.S, want.W, row)
				}
			}
			again.Cells(0, y, cells)
		}
		if got := again.Lines(); strings.Join(got, "\n") != strings.Join(lines, "\n") {
			for y := range lines {
				if got[y] != lines[y] {
					t.Fatalf("canvas %d row %d is written as %q after the filter, %q before", n, y, got[y], lines[y])
				}
			}
		}
	}
}
