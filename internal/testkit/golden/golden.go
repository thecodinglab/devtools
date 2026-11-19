// Package golden holds the screens tests compare and the golden files they
// are compared with (10.1.2 of the plan).
//
// A Screen is text, a cursor and, inside its rows, styles in the wire row
// format of 4.2.5. It has three printed forms: Plain (the characters),
// Styled (the rows with their escape sequences made visible) and Runs (the
// characters, then every styled run as one readable line). Assert compares
// a form with testdata/golden/<name>.txt of the package under test and
// rewrites the file under "go test ./<pkg> -update".
//
// It is a test-only package (import rule 8).
package golden

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// update is the one -update flag of the tree: "go test ./<pkg> -update"
// rewrites the golden files the tests of that package compare with. No
// other package defines a flag of that name.
var update = flag.Bool("update", false, "rewrite the golden files under testdata/golden")

// Dir is the directory of the golden files, relative to the directory of
// the package under test (the working directory of its tests).
const Dir = "testdata/golden"

// Screen is what a test compares: text, cursor, and optionally styles.
type Screen struct {
	Cols, Rows       int
	Lines            []string // one per row, wire row format of 4.2.5
	CursorX, CursorY int
	CursorVisible    bool
}

// Header is the first line of every form: "size 80x24 cursor 3,5 visible"
// (or "hidden"); the cursor is column, row.
func (s Screen) Header() string {
	vis := "hidden"
	if s.CursorVisible {
		vis = "visible"
	}
	return fmt.Sprintf("size %dx%d cursor %d,%d %s", s.Cols, s.Rows, s.CursorX, s.CursorY, vis)
}

// rows returns the rows of the screen: Rows of them, blank where Lines has
// none, and every line of Lines when it holds more (a defect the golden
// then shows).
func (s Screen) rows() []string {
	n := max(s.Rows, len(s.Lines))
	out := make([]string, n)
	copy(out, s.Lines)
	return out
}

// PlainRow returns one row with its styles and links removed and its
// trailing blanks trimmed.
func PlainRow(row string) string {
	return strings.TrimRight(ansi.Strip(row), " ")
}

// Plain is the header line, then every row with styles removed,
// right-trimmed, each ending in "|".
func (s Screen) Plain() string {
	var b strings.Builder
	b.WriteString(s.Header())
	b.WriteByte('\n')
	for _, r := range s.rows() {
		b.WriteString(PlainRow(r))
		b.WriteString("|\n")
	}
	return b.String()
}

// Styled is the header line, then every row as it is with ESC shown as
// "\e", each ending in "|" like the rows of Plain. It is used only where
// style is the subject (SGR conformance, selection highlight, the
// default-background rule).
func (s Screen) Styled() string {
	var b strings.Builder
	b.WriteString(s.Header())
	b.WriteByte('\n')
	for _, r := range s.rows() {
		b.WriteString(strings.ReplaceAll(r, "\x1b", `\e`))
		b.WriteString("|\n")
	}
	return b.String()
}

// Runs is Plain, then the line "styles", then one line per run of cells
// that share a style other than the default (runs.go has the format).
func (s Screen) Runs() string {
	var b strings.Builder
	b.WriteString(s.Plain())
	b.WriteString("styles\n")
	for y, r := range s.rows() {
		for _, run := range rowRuns(r) {
			fmt.Fprintf(&b, "%d %s\n", y, run)
		}
	}
	return b.String()
}

// Contains reports whether text occurs in one row of the screen, with
// styles removed. Text that spans two rows is not found.
func (s Screen) Contains(text string) bool {
	for _, r := range s.rows() {
		if strings.Contains(ansi.Strip(r), text) {
			return true
		}
	}
	return false
}

// Row returns row y with styles removed and trailing blanks trimmed, "" for
// a row outside the screen.
func (s Screen) Row(y int) string {
	rows := s.rows()
	if y < 0 || y >= len(rows) {
		return ""
	}
	return PlainRow(rows[y])
}

// reporter is the part of testing.TB that Assert uses; tests of this
// package hand in a recorder.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Assert compares got with testdata/golden/<name>.txt. Under -update it
// writes got to that file instead and reports nothing. A mismatch fails the
// test with the first differing line, both versions and a caret under the
// first differing column; never the whole file twice.
func Assert(t *testing.T, name string, got string) {
	t.Helper()
	assertFile(t, Dir, name, got, *update)
}

// AssertTyped compares got with testdata/golden/<name>.txt like Assert, but
// the file is typed by a person from the plan and is never written by
// -update (6.1 rule 5): the text layer of a golden of the look, and the
// messages of 5a.9.3. A mismatch is a finding for the orchestrator, not a
// file to regenerate.
func AssertTyped(t *testing.T, name string, got string) {
	t.Helper()
	assertFile(t, Dir, name, got, false)
}

// Look asserts the three layers of a golden of the look (5a.17): the text
// <id>.txt, the Plain form of the truecolor screen, which is typed from the
// mockup and never written by -update; <id>.truecolor.txt, the Runs form of
// the truecolor screen; and <id>.palette.txt, the Runs form of the palette
// screen. -update writes the two style layers only.
func Look(t *testing.T, id string, truecolor, palette Screen) {
	t.Helper()
	look(t, id, truecolor, palette)
}

func look(t reporter, id string, truecolor, palette Screen) {
	t.Helper()
	assertFile(t, Dir, id, truecolor.Plain(), false)
	assertFile(t, Dir, id+".truecolor", truecolor.Runs(), *update)
	assertFile(t, Dir, id+".palette", palette.Runs(), *update)
}

// Updating reports whether the test binary runs with -update.
func Updating() bool { return *update }

func assertFile(t reporter, dir, name, got string, write bool) {
	t.Helper()
	path := filepath.Join(dir, name+".txt")
	if write {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Errorf("golden %s: %v", name, err)
			return
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Errorf("golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Errorf("golden %s: %s does not exist; got:\n%s", name, path, got)
			return
		}
		t.Errorf("golden %s: %v", name, err)
		return
	}
	if msg, ok := Diff(string(want), got); !ok {
		t.Errorf("golden %s (%s): %s", name, path, msg)
	}
}

// Diff compares two texts line by line. When they differ it returns a
// message that names the first differing line and column (counted in
// cells, from 1), shows that line of both and puts a caret under the
// column, and false.
func Diff(want, got string) (string, bool) {
	if want == got {
		return "", true
	}
	wl := strings.Split(strings.TrimSuffix(want, "\n"), "\n")
	gl := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	n := max(len(wl), len(gl))
	for i := range n {
		var w, g string
		wOK, gOK := i < len(wl), i < len(gl)
		if wOK {
			w = wl[i]
		}
		if gOK {
			g = gl[i]
		}
		if wOK && gOK && w == g {
			continue
		}
		switch {
		case !wOK:
			return fmt.Sprintf("line %d is extra (want %d lines, got %d)\n  got: %s", i+1, len(wl), len(gl), g), false
		case !gOK:
			return fmt.Sprintf("line %d is missing (want %d lines, got %d)\n want: %s", i+1, len(wl), len(gl), w), false
		}
		prefix := commonPrefix(w, g)
		col := ansi.StringWidth(prefix)
		return fmt.Sprintf("line %d differs at column %d\n want: %s\n  got: %s\n       %s^",
			i+1, col+1, w, g, strings.Repeat(" ", col)), false
	}
	return "the texts differ only in the newline at their end", false
}

// commonPrefix returns the longest common prefix of a and b that ends on a
// grapheme cluster boundary of both.
func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) {
		ca, _ := ansi.FirstGraphemeCluster(a[i:], ansi.GraphemeWidth)
		cb, _ := ansi.FirstGraphemeCluster(b[i:], ansi.GraphemeWidth)
		if ca != cb || ca == "" {
			break
		}
		i += len(ca)
	}
	return a[:i]
}
