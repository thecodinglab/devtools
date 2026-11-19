package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const esc = "\x1b"

// recorder stands in for a test in the tests of Assert, so a mismatch can
// be looked at without failing the real test.
type recorder struct{ errs []string }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func TestGoldenPlainRemovesStylesAndTrims(t *testing.T) {
	s := Screen{
		Cols: 20, Rows: 4,
		Lines: []string{
			esc + "[0;1mhello" + esc + "[0m world   ",
			"",
			esc + "]8;;https://example.com" + esc + `\` + "link" + esc + "]8;;" + esc + `\` + " 中文 é",
		},
		CursorX: 3, CursorY: 1, CursorVisible: true,
	}
	want := "size 20x4 cursor 3,1 visible\n" +
		"hello world|\n" +
		"|\n" +
		"link 中文 é|\n" +
		"|\n"
	if got := s.Plain(); got != want {
		t.Fatalf("Plain:\n%s\nwant:\n%s", got, want)
	}
	if !s.Contains("o wor") || s.Contains("hello world   x") || !s.Contains("中文") {
		t.Fatal("Contains does not search the rows without styles")
	}
	if got := s.Row(0); got != "hello world" {
		t.Fatalf("Row(0) = %q", got)
	}
	hidden := Screen{Cols: 2, Rows: 1}
	if got := hidden.Plain(); got != "size 2x1 cursor 0,0 hidden\n|\n" {
		t.Fatalf("Plain of a hidden cursor: %q", got)
	}
	styled := s.Styled()
	if !strings.Contains(styled, `\e[0;1mhello\e[0m world   |`) {
		t.Fatalf("Styled does not show ESC as \\e and keeps the row as it is:\n%s", styled)
	}
	if strings.Contains(styled, esc) {
		t.Fatal("Styled contains a raw ESC")
	}
}

func TestGoldenMismatchNamesTheFirstDifferingCell(t *testing.T) {
	want := "size 10x2 cursor 0,0 visible\nab中cdef|\nsame|\n"
	got := "size 10x2 cursor 0,0 visible\nab中cXef|\nother|\n"
	msg, ok := Diff(want, got)
	if ok {
		t.Fatal("Diff reports equal texts")
	}
	// "ab中c" is five cells wide: the first difference is in column 6.
	if !strings.Contains(msg, "line 2 differs at column 6") {
		t.Fatalf("message does not name line 2, column 6:\n%s", msg)
	}
	lines := strings.Split(msg, "\n")
	if len(lines) != 4 {
		t.Fatalf("message has %d lines, want 4 (header, want, got, caret):\n%s", len(lines), msg)
	}
	if lines[1] != " want: ab中cdef|" || lines[2] != "  got: ab中cXef|" {
		t.Fatalf("message does not show both versions of the line:\n%s", msg)
	}
	if lines[3] != "            ^" {
		t.Fatalf("caret is not under column 6: %q", lines[3])
	}
	if strings.Contains(msg, "same") || strings.Contains(msg, "other") {
		t.Fatalf("message shows more than the first differing line:\n%s", msg)
	}

	if msg, ok := Diff("a\nb\n", "a\n"); ok || !strings.Contains(msg, "line 2 is missing") {
		t.Fatalf("a shorter text: %q", msg)
	}
	if msg, ok := Diff("a\n", "a\nb\n"); ok || !strings.Contains(msg, "line 2 is extra") {
		t.Fatalf("a longer text: %q", msg)
	}
	if msg, ok := Diff("a\n", "a"); ok || !strings.Contains(msg, "newline") {
		t.Fatalf("a text without its last newline: %q", msg)
	}
	if _, ok := Diff("x\n", "x\n"); !ok {
		t.Fatal("equal texts differ")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "screen.txt"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	var r recorder
	assertFile(&r, dir, "screen", got, false)
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "golden screen") || !strings.Contains(r.errs[0], "line 2 differs at column 6") {
		t.Fatalf("assert reported %q", r.errs)
	}
	r = recorder{}
	assertFile(&r, dir, "screen", want, false)
	if len(r.errs) != 0 {
		t.Fatalf("assert of an equal text reported %q", r.errs)
	}
	r = recorder{}
	assertFile(&r, dir, "missing", want, false)
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "does not exist") {
		t.Fatalf("assert of a missing file reported %q", r.errs)
	}
}

func TestGoldenUpdateRewritesTheFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "testdata", "golden")
	var r recorder
	assertFile(&r, dir, "new", "first\n", true)
	assertFile(&r, dir, "new", "second\n", true)
	if len(r.errs) != 0 {
		t.Fatalf("update reported %q", r.errs)
	}
	b, err := os.ReadFile(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "second\n" {
		t.Fatalf("file holds %q after the update", b)
	}
	assertFile(&r, dir, "new", "second\n", false)
	if len(r.errs) != 0 {
		t.Fatalf("compare after the update reported %q", r.errs)
	}

	// The typed text layer of a look golden is never written: Look writes the
	// two style layers only, and with the flag set too.
	old := *update
	*update = true
	defer func() { *update = old }()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	s := Screen{Cols: 4, Rows: 1, Lines: []string{esc + "[0;1mab"}}
	var lr recorder
	look(&lr, "look-x", s, s)
	if _, err := os.Stat(filepath.Join(root, Dir, "look-x.txt")); !os.IsNotExist(err) {
		t.Fatalf("-update wrote the typed text layer (%v)", err)
	}
	if len(lr.errs) != 1 || !strings.Contains(lr.errs[0], "look-x.txt does not exist") {
		t.Fatalf("a missing text layer reported %q", lr.errs)
	}
	for _, layer := range []string{"look-x.truecolor.txt", "look-x.palette.txt"} {
		b, err := os.ReadFile(filepath.Join(root, Dir, layer))
		if err != nil {
			t.Fatalf("-update did not write %s: %v", layer, err)
		}
		if string(b) != s.Runs() {
			t.Fatalf("%s holds %q", layer, b)
		}
	}
}

func TestGoldenRunsListsEveryStyledRunAndNoDefaultCell(t *testing.T) {
	s := Screen{
		Cols: 80, Rows: 4,
		Lines: []string{
			// attributes in a fixed order whatever order the row names them
			esc + "[0;7;1mabcdefgh" + esc + "[0m plain",
			// RGB colours on an RGB ground; the default style in between is not listed
			esc + "[0;1;38;2;230;231;235;48;2;48;50;58mxy" + esc + "[0m  " + esc + "[0;3;4;31mz",
			// palette colours, a change of style is a new run, the same style twice is one
			esc + "[0;94ma" + esc + "[0;94mb" + esc + "[0;95;100mc" + esc + "[0;38;5;3md" + esc + "[0;38;5;200me",
			// a wide character counts two cells; a run never crosses a row
			esc + "[0;7m中x",
		},
	}
	want := s.Plain() + "styles\n" +
		"0 0 8 bold reverse\n" +
		"1 0 2 bold #e6e7eb on #30323a\n" +
		"1 4 1 italic underline p1\n" +
		"2 0 2 p12\n" +
		"2 2 1 p13 on p8\n" +
		"2 3 1 p3\n" +
		"2 4 1 p200\n" +
		"3 0 3 reverse\n"
	if got := s.Runs(); got != want {
		t.Fatalf("Runs:\n%s\nwant:\n%s", got, want)
	}

	more := Screen{Cols: 20, Rows: 1, Lines: []string{
		esc + "[0;9;8;7;5;4:3;3;2;1;58;5;9ma" + esc + "[0;4:2mb" + esc + "[0;1mc" + esc + "[0;22md",
	}}
	wantMore := "0 0 1 bold faint italic curly-underline blink reverse conceal strike ul p9\n" +
		"0 1 1 double-underline\n" +
		"0 2 1 bold\n"
	if got := strings.SplitN(more.Runs(), "styles\n", 2)[1]; got != wantMore {
		t.Fatalf("runs of the rest of the grammar:\n%s\nwant:\n%s", got, wantMore)
	}
}
