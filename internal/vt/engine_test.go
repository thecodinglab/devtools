//go:build cgo

package vt

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The identity every test terminal answers XTVERSION with.
const testVersion, testHost = "9.8.7-test", "test-host"

func TestMain(m *testing.M) {
	// Hermetic: nothing of the developer's multiplexer or devtools session
	// reaches a test (10.1).
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "DEVTOOLS_TEST_NO_PTY" {
			continue
		}
		if strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "ZELLIJ") ||
			name == "TMUX" || name == "TMUX_PANE" || name == "STY" {
			os.Unsetenv(name)
		}
	}
	SetIdentity("devtools", testVersion, testHost)
	os.Exit(m.Run())
}

func TestRepliesAreQueuedByResize(t *testing.T) {
	tm := termAt(t, 80, 24)
	tm.Write([]byte("\x1b[?2048h"))
	if r := tm.Replies(); len(r) != 0 {
		t.Fatalf("setting mode 2048 replied %q", r)
	}
	// A reply of a Write that was not drained yet, then one of the resize:
	// both come out of Replies, in order.
	tm.Write([]byte("\x1b[c"))
	tm.Resize(100, 30)
	want := "\x1b[?62;22c\x1b[48;30;100;600;1000t"
	if got := string(tm.Replies()); got != want {
		t.Fatalf("Replies after Write and Resize = %q, want %q", got, want)
	}
	if got := tm.Replies(); len(got) != 0 {
		t.Fatalf("a second Replies = %q, want nothing", got)
	}
	tm.Write([]byte("\x1b[?2048l"))
	tm.Resize(90, 30)
	if got := tm.Replies(); len(got) != 0 {
		t.Fatalf("a resize without mode 2048 replied %q", got)
	}
}

func TestXTVersionAnswersWithTheDevtoolsIdentity(t *testing.T) {
	tm := termAt(t, 80, 24)
	for _, q := range []string{"\x1b[>q", "\x1b[>0q"} {
		tm.Write([]byte(q))
		got := string(tm.Replies())
		want := "\x1bP>|devtools " + testVersion + " " + testHost + "\x1b\\"
		if got != want {
			t.Errorf("%q answered %q, want %q", q, got, want)
		}
		if strings.Contains(got, "libghostty") {
			t.Errorf("%q answered with the library's name: %q", q, got)
		}
	}
}

func TestIdentityKeepsOnlyTheCharactersTheGuardReads(t *testing.T) {
	defer SetIdentity("devtools", testVersion, testHost)
	SetIdentity("devtools", "1.0+dirty build", "evil host\x1b\\;x")
	tm := termAt(t, 80, 24)
	tm.Write([]byte("\x1b[>q"))
	if got, want := string(tm.Replies()), "\x1bP>|devtools 1.0+dirtybuild evilhostx\x1b\\"; got != want {
		t.Errorf("answer %q, want %q", got, want)
	}
	SetIdentity("devtools", "", "")
	tm2 := termAt(t, 80, 24)
	tm2.Write([]byte("\x1b[>q"))
	if got, want := string(tm2.Replies()), "\x1bP>|devtools unknown unknown\x1b\\"; got != want {
		t.Errorf("answer with empty words %q, want %q", got, want)
	}
	// A terminal keeps the answer it was made with.
	tm.Write([]byte("\x1b[>q"))
	if got := string(tm.Replies()); !strings.Contains(got, "1.0+dirtybuild") {
		t.Errorf("an older terminal answers %q", got)
	}
}

func TestOSC1011AreAnsweredFromTheColoursOfNew(t *testing.T) {
	tm := termAt(t, 80, 24, func(o *Options) { o.Colors = Colors{Fg: "#c0caf5", Bg: "#1a1b26", Dark: true} })
	tm.Write([]byte("\x1b]10;?\x1b\\\x1b]11;?\x07"))
	want := "\x1b]10;rgb:c0c0/caca/f5f5\x1b\\\x1b]11;rgb:1a1a/1b1b/2626\x07"
	if got := string(tm.Replies()); got != want {
		t.Fatalf("OSC 10 and 11 right after New answered %q, want %q", got, want)
	}
	tm.SetColors(Colors{Fg: "#ffffff", Bg: "#000000"})
	tm.Write([]byte("\x1b]11;?\x07"))
	if got, want := string(tm.Replies()), "\x1b]11;rgb:0000/0000/0000\x07"; got != want {
		t.Fatalf("after SetColors OSC 11 answered %q, want %q", got, want)
	}
	tm.Write([]byte("\x1b[?996n"))
	if got, want := string(tm.Replies()), "\x1b[?997;2n"; got != want {
		t.Fatalf("the colour scheme of a light theme is %q, want %q", got, want)
	}
}

func TestTheOSC7501QueryIsAnswered(t *testing.T) {
	tm := termAt(t, 80, 24)
	want := ";?:states=idle,working,done,blocked,error:kinds=permission,question,auth"
	for _, st := range []string{"\x1b\\", "\x07"} {
		tm.Write([]byte("\x1b]7501;?" + st))
		if got := string(tm.Replies()); got != "\x1b]7501"+want+st {
			t.Errorf("query with %q answered %q", st, got)
		}
	}
}

func TestKeypadApplicationModeIsReported(t *testing.T) {
	tm := termAt(t, 80, 24)
	if tm.Modes().AppKeypad {
		t.Fatal("application keypad is set in a fresh terminal")
	}
	steps := []struct {
		in   string
		want bool
	}{{"\x1b=", true}, {"\x1b>", false}, {"\x1b[?66h", true}, {"\x1b[?66l", false}}
	for _, s := range steps {
		tm.Write([]byte(s.in))
		if got := tm.Modes().AppKeypad; got != s.want {
			t.Errorf("after %q AppKeypad = %v, want %v", s.in, got, s.want)
		}
	}
}

func TestGraphemeClusteringIsOnByDefault(t *testing.T) {
	tm := termAt(t, 20, 3)
	for round, prefix := range []string{"", "\x1bc"} { // and after a full reset
		tm.Write([]byte(prefix + "\x1b[?2027$p"))
		if got, want := string(tm.Replies()), "\x1b[?2027;1$y"; got != want {
			t.Errorf("round %d: DECRQM 2027 = %q, want %q", round, got, want)
		}
		family := "\U0001F468‍\U0001F469‍\U0001F467"
		tm.Write([]byte("\x1b[2J\x1b[H" + family + "|"))
		if got, want := tm.Row(0), family+"|"; got != want {
			t.Errorf("round %d: row %q, want %q", round, got, want)
		}
		if c := tm.Cursor(); c.X != 3 {
			t.Errorf("round %d: cursor after one cluster of two cells and a bar at column %d, want 3", round, c.X)
		}
		cells, _ := tm.liveCells(0)
		if len(cells) < 2 || string(cells[0].text) != family || cells[0].wide != wideWide || cells[1].wide != wideSpacerTail {
			t.Errorf("round %d: the cluster is not one wide cell", round)
		}
	}
}

func TestWriteFeedsTheLibraryInPieces(t *testing.T) {
	tm := termAt(t, 80, 24)
	n := 0
	tm.onPiece = func() { n++ }
	tm.Write(bytes.Repeat([]byte("y\n"), 32<<10)) // 64 KiB
	if n != 16 {
		t.Fatalf("one Write of 64 KiB gave %d pieces, want 16", n)
	}
	n = 0
	tm.Write(make([]byte, writePiece+1))
	if n != 2 {
		t.Fatalf("one Write of 4 KiB + 1 gave %d pieces, want 2", n)
	}
	n = 0
	tm.Write(nil)
	if n != 0 {
		t.Fatalf("an empty Write gave %d pieces", n)
	}
}

func TestHistoryHeadroomIsOnePageAtTheWidth(t *testing.T) {
	cases := map[int]int{1: 46600, 4: 11725, 9: 5267, 40: 1263, 44: 1157, 47: 1090, 80: 1000, 200: 1000, 1000: 1000}
	for cols, want := range cases {
		if got := historyHeadroom(cols); got != want {
			t.Errorf("historyHeadroom(%d) = %d, want %d", cols, got, want)
		}
	}
}

// fillLines writes n lines of the full width.
func fillLines(tm *term, n int) {
	cols, _ := tm.Size()
	line := make([]byte, 0, cols+2)
	var buf []byte
	for i := range n {
		line = line[:0]
		s := strconv.Itoa(i)
		for len(line) < cols {
			line = append(line, s...)
			line = append(line, '.')
		}
		buf = append(buf, line[:cols]...)
		buf = append(buf, '\r', '\n')
		if len(buf) > 1<<20 {
			tm.Write(buf)
			buf = buf[:0]
		}
	}
	tm.Write(buf)
}

func TestHistoryHoldsTenThousandWideLines(t *testing.T) {
	tm := termAt(t, 200, 50, func(o *Options) { o.History = 10000 })
	fillLines(tm, 11000+50)
	if got := tm.scrollbackRows(); got < 10000 {
		t.Fatalf("the library holds %d history rows of 200 columns, want at least 10,000", got)
	}
}

func TestHistoryHoldsHistoryRowsAtEveryWidth(t *testing.T) {
	// provisional (E10 item 1): the headroom is measured against
	// libghostty-vt 1.0.1, not documented by its header.
	const history = 10000
	for _, cols := range []int{4, 10, 20, 40, 80, 120, 200} {
		tm := termAt(t, cols, 24, func(o *Options) { o.History = history })
		fillLines(tm, history+historyHeadroom(cols)+24)
		if got := tm.scrollbackRows(); got < history {
			t.Errorf("at %d columns the library holds %d history rows, want at least %d", cols, got, history)
		}
	}
	tm := termAt(t, 200, 24, func(o *Options) { o.History = history })
	fillLines(tm, history+historyHeadroom(200)+24)
	tm.Resize(4, 24)
	if got := tm.scrollbackRows(); got < history {
		t.Errorf("after a resize from 200 to 4 columns the library holds %d history rows, want at least %d", got, history)
	}
}

// rss is the resident set of this process in bytes.
func rss(t *testing.T) int64 {
	t.Helper()
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Fields(string(b))
		pages, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return pages * int64(os.Getpagesize())
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		t.Fatal(err)
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return kb << 10
}

func TestCloseFreesTheTerminal(t *testing.T) {
	pair := func() {
		tm := New(Options{Cols: 80, Rows: 24, History: 1000, Colors: Colors{Fg: "#ffffff", Bg: "#000000"}})
		tm.Write([]byte("hello \x1b[1mworld\x1b[0m\r\n\x1b]0;title\x07\x1b[c"))
		_ = tm.Row(0)
		_ = tm.Replies()
		tm.Close()
		tm.Close() // a second Close does nothing
	}
	for range 1000 {
		pair()
	}
	runtime.GC()
	after1000 := rss(t)
	for range 9000 {
		pair()
	}
	runtime.GC()
	after10000 := rss(t)
	// ESTIMATE of the bound (6.5.2, P1-02): a terminal that leaked would
	// grow the resident set by about 190 KiB each (E10 item 13).
	if grow := after10000 - after1000; grow > 4<<20 {
		t.Fatalf("the resident set grew by %d KiB over 9,000 New and Close, want at most 4 MiB", grow>>10)
	}
}

func TestSixteenTerminalsInParallel(t *testing.T) {
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(g), 1))
			tm := New(Options{Cols: 40, Rows: 10, History: 500})
			defer tm.Close()
			for round := range 50 {
				fmt.Fprintf(writerOf(tm.(*term)), "\x1b[3%dmterminal %d round %d 漢\x1b[0m\r\n", round%8, g, round)
				tm.Resize(20+rng.IntN(60), 5+rng.IntN(20))
				_ = tm.Replies()
				_, rows := tm.Size()
				for y := range rows {
					if err := checkRow(tm.Row(y)); err != nil {
						t.Errorf("terminal %d round %d row %d: %v", g, round, y, err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func TestNoEngineTypeIsExported(t *testing.T) {
	// The exported API of the package as a cgo build compiles it, type
	// checked with C faked: a type from package C becomes invalid, so an
	// invalid type anywhere in the exported API is a type of the engine.
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if !inCgoBuild(f) {
			continue
		}
		files = append(files, f)
	}
	conf := types.Config{FakeImportC: true, Importer: emptyImporter{}, Error: func(error) {}}
	pkg, _ := conf.Check("devtools/internal/vt", fset, files, nil)
	if pkg == nil || pkg.Scope().Lookup("Term") == nil || pkg.Scope().Lookup("New") == nil {
		t.Fatal("the package did not type-check far enough to read its API")
	}
	seen := map[types.Type]bool{}
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}
		if bad := engineType(obj.Type(), seen); bad != "" {
			t.Errorf("exported %s has a type of the engine (%s)", name, bad)
		}
		if tn, ok := obj.(*types.TypeName); ok {
			ms := types.NewMethodSet(types.NewPointer(tn.Type()))
			for i := range ms.Len() {
				m := ms.At(i).Obj()
				if m.Exported() {
					if bad := engineType(m.Type(), seen); bad != "" {
						t.Errorf("method %s.%s has a type of the engine (%s)", name, m.Name(), bad)
					}
				}
			}
		}
	}
	// The check sees an engine type: the unexported term holds one.
	if engineType(pkg.Scope().Lookup("term").Type(), map[types.Type]bool{}) == "" {
		t.Error("the check does not see the engine type inside term")
	}
}

// inCgoBuild reports whether a file is part of the default cgo build.
func inCgoBuild(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				expr, err := constraint.Parse(c.Text)
				if err != nil {
					return false
				}
				return expr.Eval(func(tag string) bool {
					return tag == "cgo" || tag == runtime.GOOS || tag == runtime.GOARCH
				})
			}
		}
	}
	return true
}

// emptyImporter gives every import an empty package; the exported API of
// internal/vt uses none of them.
type emptyImporter struct{}

func (emptyImporter) Import(p string) (*types.Package, error) {
	pkg := types.NewPackage(p, path.Base(p))
	pkg.MarkComplete()
	return pkg, nil
}

// engineType returns a description of the first invalid type (a type of
// package C under FakeImportC) in t, or "".
func engineType(t types.Type, seen map[types.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind() == types.Invalid {
			return "invalid type"
		}
	case *types.Named:
		if t.Obj().Pkg() != nil && t.Obj().Pkg().Path() == "C" {
			return t.String()
		}
		if t.Obj().Pkg() != nil && t.Obj().Pkg().Path() != "devtools/internal/vt" {
			return ""
		}
		return engineType(t.Underlying(), seen)
	case *types.Pointer:
		return engineType(t.Elem(), seen)
	case *types.Slice:
		return engineType(t.Elem(), seen)
	case *types.Array:
		return engineType(t.Elem(), seen)
	case *types.Map:
		if s := engineType(t.Key(), seen); s != "" {
			return s
		}
		return engineType(t.Elem(), seen)
	case *types.Chan:
		return engineType(t.Elem(), seen)
	case *types.Signature:
		for _, tup := range []*types.Tuple{t.Params(), t.Results()} {
			for i := range tup.Len() {
				if s := engineType(tup.At(i).Type(), seen); s != "" {
					return s
				}
			}
		}
	case *types.Struct:
		for i := range t.NumFields() {
			if f := t.Field(i); f.Exported() || f.Name() == "h" {
				if s := engineType(f.Type(), seen); s != "" {
					return f.Name() + ": " + s
				}
			}
		}
	case *types.Interface:
		for i := range t.NumMethods() {
			if s := engineType(t.Method(i).Type(), seen); s != "" {
				return t.Method(i).Name() + ": " + s
			}
		}
	}
	return ""
}

func TestKeepSizeRestoresThePaneSizeAfterDECCOLM(t *testing.T) {
	tm := termAt(t, 100, 30)
	// Mode 40 allows DECCOLM; DECCOLM then asks for 132 columns.
	tm.Write([]byte("\x1b[?40h\x1b[?3h"))
	if c, r := tm.libSize(); c != 100 || r != 30 {
		t.Fatalf("the library is %dx%d after DECCOLM, want the pane's 100x30", c, r)
	}
	if c, r := tm.Size(); c != 100 || r != 30 {
		t.Fatalf("Size is %dx%d", c, r)
	}
}

func TestNewClampsTheSizeTheLibraryTakes(t *testing.T) {
	tm := termAt(t, 0, -3)
	if c, r := tm.Size(); c != 1 || r != 1 {
		t.Fatalf("Size of a terminal made with 0x-3 is %dx%d, want 1x1", c, r)
	}
	tm.Resize(70000, 2)
	if c, _ := tm.Size(); c != maxDim {
		t.Fatalf("Size after a resize to 70,000 columns is %d, want %d", c, maxDim)
	}
}

func TestCursorShapeAndModesAreRead(t *testing.T) {
	tm := termAt(t, 20, 5)
	tm.Write([]byte("ab\r\nc"))
	if got, want := tm.Cursor(), (Cursor{X: 1, Y: 1, Visible: true, Shape: "block"}); got != want {
		t.Errorf("cursor %+v, want %+v", got, want)
	}
	steps := []struct {
		in    string
		shape string
		blink bool
	}{{"\x1b[1 q", "block", true}, {"\x1b[3 q", "underline", true}, {"\x1b[4 q", "underline", false}, {"\x1b[5 q", "bar", true}, {"\x1b[6 q", "bar", false}, {"\x1b[2 q", "block", false}}
	for _, s := range steps {
		tm.Write([]byte(s.in))
		if c := tm.Cursor(); c.Shape != s.shape || c.Blink != s.blink {
			t.Errorf("after %q cursor %+v, want %s blink %v", s.in, c, s.shape, s.blink)
		}
	}
	tm.Write([]byte("\x1b[?25l"))
	if tm.Cursor().Visible {
		t.Error("cursor visible after DECTCEM reset")
	}
	tm.Write([]byte("\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?2004h\x1b[?1004h\x1b[?1h\x1b[>5u"))
	want := Modes{Alt: true, Mouse: "all", MouseSGR: true, Paste: true, Focus: true, AppCursor: true, Kitty: 5}
	if got := tm.Modes(); got != want {
		t.Errorf("modes %+v, want %+v", got, want)
	}
	tm.Write([]byte("\x1bc"))
	if got := tm.Modes(); got != (Modes{}) {
		t.Errorf("modes after RIS %+v, want none", got)
	}
}

func TestATermIsUnusableButHarmlessAfterClose(t *testing.T) {
	tm := New(Options{Cols: 10, Rows: 2}).(*term)
	tm.Close()
	tm.Write([]byte("x"))
	tm.Resize(20, 3)
	tm.SetColors(Colors{Fg: "#000000", Bg: "#ffffff"})
	if tm.Row(0) != "" || tm.Replies() != nil || tm.SyncActive() || tm.Modes() != (Modes{}) || tm.Cursor() != (Cursor{}) {
		t.Fatal("a closed terminal reports state")
	}
}
