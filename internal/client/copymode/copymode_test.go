package copymode

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"devtools/internal/client/screen"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a config file or a multiplexer reaches them. The model
// reads no environment; this is the rule of 10.1 all the same.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-copymode-test")
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

// fake is a Source built from text: one string per absolute line from
// first on. A rune of the CJK ranges takes two cells.
type fake struct {
	cols, rows  int
	top         int64
	first, base int64
	lines       map[int64]string
}

// newFake holds text as the lines first, first+1, ...; the live screen is
// the last rows lines and is what is shown.
func newFake(cols, rows int, first int64, text ...string) *fake {
	f := &fake{cols: cols, rows: rows, first: first, lines: map[int64]string{}}
	for i, s := range text {
		f.lines[first+int64(i)] = s
	}
	f.base = first + int64(len(text)) - int64(rows)
	if f.base < first {
		f.base = first
	}
	f.top = f.base
	return f
}

// numbered is a fake of n lines "line <i>".
func numbered(cols, rows int, first int64, n int) *fake {
	text := make([]string, n)
	for i := range text {
		text[i] = "line " + itoa(int(first)+i)
	}
	return newFake(cols, rows, first, text...)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func (f *fake) Size() (int, int)      { return f.cols, f.rows }
func (f *fake) Top() int64            { return f.top }
func (f *fake) Range() (int64, int64) { return f.first, f.base }
func (f *fake) Cell(row, col int) screen.Cell {
	return f.cells(f.top + int64(row))[col]
}

func wide(r rune) bool {
	return r >= 0x2E80 && r <= 0x9FFF || r >= 0xAC00 && r <= 0xD7A3 || r >= 0xFF00 && r <= 0xFF60
}

func (f *fake) cells(line int64) []screen.Cell {
	out := make([]screen.Cell, 0, f.cols)
	for _, r := range f.lines[line] {
		if wide(r) {
			out = append(out, screen.Cell{S: string(r), W: 2}, screen.Cell{})
		} else {
			out = append(out, screen.Cell{S: string(r), W: 1})
		}
	}
	for len(out) < f.cols {
		out = append(out, screen.Cell{S: " ", W: 1})
	}
	return out[:f.cols]
}

// key presses one key of the table, written as in 5a.11.4.
func key(m *Model, k string) []Effect {
	switch {
	case strings.HasPrefix(k, "ctrl+"):
		return m.Key(strings.TrimPrefix(k, "ctrl+"), modCtrl, "")
	case len(k) == 1 && k[0] >= 'A' && k[0] <= 'Z':
		return m.Key(strings.ToLower(k), modShift, k)
	case len(k) == 1:
		return m.Key(k, 0, k)
	}
	return m.Key(k, 0, "")
}

func keys(m *Model, ks ...string) []Effect {
	var all []Effect
	for _, k := range ks {
		all = append(all, key(m, k)...)
	}
	return all
}

func wantCursor(t *testing.T, m *Model, line int64, col int) {
	t.Helper()
	if got := m.Cursor(); got != (Pos{line, col}) {
		t.Fatalf("cursor = %+v, want {%d %d}", got, line, col)
	}
}

func wantEffects(t *testing.T, got []Effect, want ...Effect) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effects = %+v, want %+v", got, want)
	}
}

func TestCellAndLineMotions(t *testing.T) {
	f := newFake(10, 3, 100, "abcdefghij", "klmnopqrst", "uvwxyz")
	m := Enter(f, Pos{101, 2})
	wantCursor(t, m, 101, 2)
	for _, c := range []struct {
		key  string
		line int64
		col  int
	}{
		{"l", 101, 3}, {"right", 101, 4}, {"h", 101, 3}, {"left", 101, 2},
		{"j", 102, 2}, {"down", 102, 2}, {"k", 101, 2}, {"up", 100, 2}, {"k", 100, 2},
	} {
		wantEffects(t, key(m, c.key))
		wantCursor(t, m, c.line, c.col)
	}
	keys(m, "0", "h")
	wantCursor(t, m, 100, 0)
	keys(m, "l", "l", "l", "l", "l", "l", "l", "l", "l", "l", "l")
	wantCursor(t, m, 100, 9)
}

func TestWordMotions(t *testing.T) {
	f := newFake(20, 2, 0, "foo bar.baz  qux", "next line")
	m := Enter(f, Pos{0, 0})
	for _, want := range []int{4, 7, 8, 13} {
		key(m, "w")
		wantCursor(t, m, 0, want)
	}
	key(m, "w")
	wantCursor(t, m, 1, 0)

	m = Enter(f, Pos{0, 0})
	for _, want := range []int{2, 6, 7, 10, 15} {
		key(m, "e")
		wantCursor(t, m, 0, want)
	}
	key(m, "e")
	wantCursor(t, m, 1, 3)

	m = Enter(f, Pos{1, 0})
	for _, want := range []int{13, 8, 7, 4, 0} {
		key(m, "b")
		wantCursor(t, m, 0, want)
	}
	key(m, "b")
	wantCursor(t, m, 0, 0)

	m = Enter(f, Pos{0, 5})
	key(m, "b")
	wantCursor(t, m, 0, 4)
}

func TestWordMotionsStopAtALineThatIsNotShown(t *testing.T) {
	f := numbered(20, 3, 0, 10) // shows lines 7 to 9
	f.top = 3                   // shows lines 3 to 5
	m := Enter(f, Pos{5, 0})
	wantEffects(t, key(m, "w"))
	wantCursor(t, m, 5, 5)
	got := key(m, "w")
	wantCursor(t, m, 6, 0)
	wantEffects(t, got, Effect{Kind: ScrollTo, Top: 4})
}

func TestLineStartAndEnd(t *testing.T) {
	f := newFake(12, 2, 0, "  hello  ", "")
	m := Enter(f, Pos{0, 3})
	key(m, "$")
	wantCursor(t, m, 0, 6)
	key(m, "0")
	wantCursor(t, m, 0, 0)
	key(m, "end")
	wantCursor(t, m, 0, 6)
	key(m, "home")
	wantCursor(t, m, 0, 0)
	key(m, "j")
	key(m, "$")
	wantCursor(t, m, 1, 0)
	keys(m, "k", "0")
	m.Key("4", modShift, "$") // the key without modifiers, and its text
	wantCursor(t, m, 0, 6)
}

func TestWideCharactersAreOneStep(t *testing.T) {
	f := newFake(10, 2, 0, "a世界b", "abcdef")
	m := Enter(f, Pos{0, 0})
	for _, want := range []int{1, 3, 5} {
		key(m, "l")
		wantCursor(t, m, 0, want)
	}
	for _, want := range []int{3, 1, 0} {
		key(m, "h")
		wantCursor(t, m, 0, want)
	}
	m = Enter(f, Pos{1, 2})
	key(m, "k")
	wantCursor(t, m, 0, 1)
	m = Enter(f, Pos{0, 4}) // the second cell of 界
	wantCursor(t, m, 0, 3)

	m = Enter(f, Pos{0, 0})
	keys(m, "v", "l")
	from, to, kind, ok := m.Selection()
	if !ok || kind != Char || from != (Pos{0, 0}) || to != (Pos{0, 2}) {
		t.Fatalf("selection = %+v %+v %q %v, want both cells of 世", from, to, kind, ok)
	}
}

func TestLeavingTheShownRowsYieldsScrollTo(t *testing.T) {
	f := numbered(20, 5, 0, 30) // base 25, shows 25 to 29
	m := Enter(f, Pos{25, 0})
	wantEffects(t, key(m, "k"), Effect{Kind: ScrollTo, Top: 24})
	wantCursor(t, m, 24, 0)
	if m.Top() != 24 {
		t.Fatalf("top = %d", m.Top())
	}
	wantEffects(t, key(m, "j"))
	m = Enter(f, Pos{29, 0})
	wantEffects(t, key(m, "j")) // the newest line: nothing below
	wantCursor(t, m, 29, 0)

	f.top = 10 // shows 10 to 14
	m = Enter(f, Pos{14, 0})
	wantEffects(t, key(m, "j"), Effect{Kind: ScrollTo, Top: 11})

	m = Enter(f, Pos{2, 0}) // entered on a line that is not shown
	if m.Top() != 2 {
		t.Fatalf("top = %d after entering at line 2, want 2", m.Top())
	}
}

func TestOldestAndLiveBottom(t *testing.T) {
	f := numbered(20, 5, 1000, 50) // first 1000, base 1045
	m := Enter(f, Pos{1047, 3})
	wantEffects(t, key(m, "g"), Effect{Kind: ScrollTo, Top: 1000})
	wantCursor(t, m, 1000, 0)
	wantEffects(t, key(m, "g"))
	wantEffects(t, key(m, "G"), Effect{Kind: ScrollTo, Top: 1045})
	wantCursor(t, m, 1049, 0)
}

func TestHalfAndFullPages(t *testing.T) {
	f := numbered(20, 10, 0, 100) // base 90
	m := Enter(f, Pos{95, 0})
	wantEffects(t, key(m, "ctrl+u"), Effect{Kind: ScrollTo, Top: 85})
	wantCursor(t, m, 90, 0)
	wantEffects(t, key(m, "ctrl+b"), Effect{Kind: ScrollTo, Top: 75})
	wantCursor(t, m, 80, 0)
	wantEffects(t, key(m, "pageup"), Effect{Kind: ScrollTo, Top: 65})
	wantEffects(t, key(m, "ctrl+f"), Effect{Kind: ScrollTo, Top: 75})
	wantEffects(t, key(m, "pagedown"), Effect{Kind: ScrollTo, Top: 85})
	wantEffects(t, key(m, "ctrl+d"), Effect{Kind: ScrollTo, Top: 90})
	wantCursor(t, m, 95, 0)
	wantEffects(t, key(m, "ctrl+d")) // at the live bottom the view stays
	wantCursor(t, m, 99, 0)

	key(m, "g")
	wantEffects(t, key(m, "ctrl+b"))
	wantCursor(t, m, 0, 0)
}

func TestSelectionByCharacterLineAndBlock(t *testing.T) {
	f := newFake(10, 4, 0, "abcdefghij", "klmnopqrst", "uvwxyz", "0123456789")
	m := Enter(f, Pos{1, 5})
	if _, _, _, ok := m.Selection(); ok {
		t.Fatal("a selection before v")
	}
	keys(m, "v", "k", "h", "h")
	check := func(kind string, from, to Pos) {
		t.Helper()
		gf, gt, gk, ok := m.Selection()
		if !ok || gk != kind || gf != from || gt != to {
			t.Fatalf("selection = %+v %+v %q %v, want %+v %+v %q", gf, gt, gk, ok, from, to, kind)
		}
	}
	check(Char, Pos{0, 3}, Pos{1, 5})
	key(m, "V")
	check(Line, Pos{0, 0}, Pos{1, 9})
	keys(m, "ctrl+v")
	check(Block, Pos{0, 3}, Pos{1, 5})
	keys(m, "j", "j", "j", "l", "l", "l", "l")
	check(Block, Pos{1, 5}, Pos{3, 7})
	if got := m.Marks(); !reflect.DeepEqual(got, []Pos{{3, 7}, {1, 5}}) {
		t.Fatalf("marks = %+v", got)
	}
	key(m, "ctrl+v")
	if _, _, _, ok := m.Selection(); ok {
		t.Fatal("ctrl+v twice left a selection")
	}
	if got := m.Marks(); !reflect.DeepEqual(got, []Pos{{3, 7}}) {
		t.Fatalf("marks without a selection = %+v", got)
	}
}

func TestSelectionStaysOnItsTextWhileOutputArrives(t *testing.T) {
	f := numbered(20, 5, 0, 30)
	m := Enter(f, Pos{26, 1})
	keys(m, "v", "j", "l")
	from, to, _, _ := m.Selection()
	top := m.Top()
	f.top = top // the server holds the scrolled view
	for i := 0; i < 50; i++ {
		f.lines[f.base+int64(f.rows)] = "more"
		f.base++
		if got := m.Sync(f); got != nil {
			t.Fatalf("Sync after output yielded %+v", got)
		}
	}
	gf, gt, _, _ := m.Selection()
	if gf != from || gt != to || m.Top() != top {
		t.Fatalf("selection moved: %+v %+v top %d, want %+v %+v top %d", gf, gt, m.Top(), from, to, top)
	}
	wantCursor(t, m, 27, 2)
}

func TestSelectionIsClampedWhenHistoryIsTrimmed(t *testing.T) {
	f := numbered(20, 5, 0, 30)
	m := Enter(f, Pos{2, 4})
	keys(m, "g", "l", "l", "v")
	for range 5 {
		key(m, "j")
	}
	f.top = m.Top()
	f.first = 3 // lines 0 to 2 were trimmed
	f.top = 3   // the server clamps the view into the range
	got := m.Sync(f)
	wantEffects(t, got, Effect{Kind: ScrollTo, Top: 3})
	from, to, kind, ok := m.Selection()
	if !ok || kind != Char || from != (Pos{3, 0}) || to != (Pos{5, 2}) {
		t.Fatalf("selection = %+v %+v %q %v, want {3 0} {5 2}", from, to, kind, ok)
	}

	f.first = 8 // the cursor's line too
	m.Sync(f)
	wantCursor(t, m, 8, 0)
	from, to, _, _ = m.Selection()
	if from != (Pos{8, 0}) || to != (Pos{8, 0}) {
		t.Fatalf("selection = %+v %+v, want the one cell left", from, to)
	}
	if m.Done() {
		t.Fatal("trimming ended copy mode")
	}
}

func TestCopyYieldsTheRangeAndExits(t *testing.T) {
	f := newFake(10, 3, 40, "abcdefghij", "klmnopqrst", "uvwxyz")
	m := Enter(f, Pos{40, 2})
	keys(m, "v", "j", "l")
	wantEffects(t, key(m, "y"),
		Effect{Kind: Copy, From: Pos{40, 2}, To: Pos{41, 3}},
		Effect{Kind: Exit})
	if !m.Done() {
		t.Fatal("not done after y")
	}
	if got := keys(m, "j", "y", "q"); got != nil {
		t.Fatalf("keys after the end yielded %+v", got)
	}
	if got := m.Mouse(MousePress, 0, 0); got != nil {
		t.Fatalf("mouse after the end yielded %+v", got)
	}

	m = Enter(f, Pos{41, 6})
	keys(m, "V", "j")
	wantEffects(t, key(m, "enter"),
		Effect{Kind: Copy, From: Pos{41, 0}, To: Pos{42, 9}},
		Effect{Kind: Exit})

	m = Enter(f, Pos{42, 4})
	keys(m, "ctrl+v", "k", "k", "h", "h")
	wantEffects(t, key(m, "y"),
		Effect{Kind: Copy, From: Pos{40, 2}, To: Pos{42, 4}, Block: true},
		Effect{Kind: Exit})
}

func TestAnEmptySelectionCopiesNothing(t *testing.T) {
	f := newFake(10, 3, 0, "abc", "def", "ghi")
	m := Enter(f, Pos{1, 1})
	wantEffects(t, key(m, "y"), Effect{Kind: Exit})
	m = Enter(f, Pos{1, 1})
	keys(m, "v", "l", "v")
	wantEffects(t, key(m, "enter"), Effect{Kind: Exit})
	m = Enter(f, Pos{1, 1})
	wantEffects(t, key(m, "q"), Effect{Kind: Exit})
}

func TestEscapeClearsTheSelectionFirst(t *testing.T) {
	f := newFake(10, 3, 0, "abc", "def", "ghi")
	m := Enter(f, Pos{1, 1})
	keys(m, "v", "l")
	wantEffects(t, key(m, "escape"))
	if _, _, _, ok := m.Selection(); ok || m.Done() {
		t.Fatalf("after the first escape: selection %v, done %v", ok, m.Done())
	}
	wantCursor(t, m, 1, 2)
	wantEffects(t, key(m, "escape"), Effect{Kind: Exit})
	if !m.Done() {
		t.Fatal("the second escape did not leave")
	}
}

func TestMousePressDragAndWheel(t *testing.T) {
	f := numbered(20, 5, 0, 30) // shows 25 to 29
	m := Enter(f, Pos{29, 0})
	wantEffects(t, m.Mouse(MousePress, 1, 2))
	wantCursor(t, m, 26, 2)
	wantEffects(t, m.Mouse(MouseRelease, 1, 2)) // a click selects nothing
	if _, _, _, ok := m.Selection(); ok || m.Done() {
		t.Fatal("a click selected or left")
	}
	m.Mouse(MousePress, 1, 2)
	m.Mouse(MouseDrag, 2, 4)
	m.Mouse("motion", 3, 5)
	from, to, kind, ok := m.Selection()
	if !ok || kind != Char || from != (Pos{26, 2}) || to != (Pos{28, 5}) {
		t.Fatalf("selection = %+v %+v %q %v", from, to, kind, ok)
	}
	wantEffects(t, m.Mouse(MouseRelease, 3, 5),
		Effect{Kind: Copy, From: Pos{26, 2}, To: Pos{28, 5}},
		Effect{Kind: Exit})

	// A drag that entered copy mode: no press reached the model.
	m = Enter(f, Pos{27, 1})
	m.Mouse(MouseDrag, 0, 3)
	from, to, _, _ = m.Selection()
	if from != (Pos{25, 3}) || to != (Pos{27, 1}) {
		t.Fatalf("selection = %+v %+v", from, to)
	}

	// Positions outside the pane are clamped to it.
	m = Enter(f, Pos{27, 1})
	m.Mouse(MousePress, 99, 99)
	wantCursor(t, m, 29, 19)

	m = Enter(f, Pos{29, 4})
	wantEffects(t, m.Mouse(MouseWheelUp, 0, 0), Effect{Kind: ScrollTo, Top: 22})
	wantCursor(t, m, 26, 4)
	wantEffects(t, m.Mouse(MouseWheelDown, 0, 0), Effect{Kind: ScrollTo, Top: 25})
	wantCursor(t, m, 26, 4)
	wantEffects(t, m.Mouse(MouseWheelDown, 0, 0))
	if got := m.Mouse("unknown", 0, 0); got != nil {
		t.Fatalf("an unknown kind yielded %+v", got)
	}
}

func TestAReflowKeepsTheCursorAndTheSelectionOnTheirText(t *testing.T) {
	f := numbered(40, 5, 100, 30) // base 125
	m := Enter(f, Pos{120, 0})
	keys(m, "l", "l", "v", "j", "j", "l")
	f.top = m.Top()
	marks := m.Marks()
	if !reflect.DeepEqual(marks, []Pos{{122, 3}, {120, 2}}) {
		t.Fatalf("marks = %+v", marks)
	}
	// The pane narrowed: every line before these wrapped once more.
	wantEffects(t, m.Reflow([]Pos{{140, 3}, {137, 2}}))
	wantCursor(t, m, 140, 3)
	from, to, kind, ok := m.Selection()
	if !ok || kind != Char || from != (Pos{137, 2}) || to != (Pos{140, 3}) {
		t.Fatalf("selection = %+v %+v %q %v", from, to, kind, ok)
	}
	// The next frame carries the top the server moved with the text.
	f2 := numbered(20, 5, 100, 60)
	f2.top = 137
	wantEffects(t, m.Sync(f2))
	if m.Top() != 137 {
		t.Fatalf("top = %d, want the server's 137", m.Top())
	}
	if m.Done() {
		t.Fatal("a reflow ended copy mode")
	}

	// Without a selection the one mark is the cursor.
	m = Enter(f, Pos{125, 1})
	wantEffects(t, m.Reflow([]Pos{{150, 1}}))
	wantCursor(t, m, 150, 1)
}

func TestAReflowThatLostAMarkExits(t *testing.T) {
	f := numbered(40, 5, 100, 30)
	for _, marks := range [][]Pos{
		{{-1, 0}, {120, 2}},
		{{122, 3}, {-1, 0}},
		{{122, 3}},
		nil,
	} {
		m := Enter(f, Pos{120, 2})
		keys(m, "v", "j")
		wantEffects(t, m.Reflow(marks), Effect{Kind: Exit})
		if !m.Done() {
			t.Fatalf("Reflow(%+v) did not end copy mode", marks)
		}
		if got := m.Sync(f); got != nil {
			t.Fatalf("Sync after the end yielded %+v", got)
		}
	}
	m := Enter(f, Pos{120, 2})
	wantEffects(t, m.Reflow([]Pos{{-1, 0}}), Effect{Kind: Exit})
}
