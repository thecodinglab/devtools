//go:build cgo

package vt

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"devtools/internal/testkit/golden"
)

// The conformance set of 10.1.5: one text file per case under
// testdata/conform, run against the real library through Term only.
//
//	# a description
//	size <cols> <rows>                 the terminal; History 10,000 (default 80 24)
//	write "<Go-quoted bytes>"          program output
//	resize <cols> <rows>
//	colors <#fg> <#bg> [dark]          SetColors
//	expect row <y> "<plain>"           the row with styles removed, trailing blanks trimmed
//	expect styled <y> "<wire row>"     the wire row exactly (4.2.5)
//	expect cursor <x> <y> [visible|hidden] [block|underline|bar] [blink|steady]
//	expect alt true|false
//	expect mouse none|press|drag|all [sgr]
//	expect keys <flags>                the input modes set: none, or a comma list of
//	                                   appcursor, appkeypad, paste, focus, kitty=<n>
//	expect replies "<bytes>"           everything Replies returned since the last expect replies
//	expect title "<s>"                 the last title ("" for none since the last expect title)
//	expect cwd "<s>"                   the last cwd (likewise)
//	expect clipboard "<s>"             the last clipboard set (likewise)
//	expect bell <n>                    bells since the last expect bell
//	expect agent "<state>" "<kind>"    the last OSC 7501 root report (likewise; "" "" for none)
//	expect range <first> <base>        Range (P1-20)
//	expect line <abs> "<plain>"        Line (P1-20)
//	expect sync true|false
//	golden <name>                      the screen, plain and its styles, against testdata/golden/<name>.txt
//
// The cases of this package use neither expect range nor expect line:
// both call into history.go, which P1-20 writes.

func TestConformance(t *testing.T) {
	files, err := filepath.Glob("testdata/conform/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 60 {
		t.Fatalf("%d conformance cases, want at least 60", len(files))
	}
	groups := map[string]int{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		groups[strings.SplitN(name, "-", 2)[0]]++
		t.Run(name, func(t *testing.T) { runCase(t, f) })
	}
	for _, g := range []string{"c01", "c02", "c03", "c04", "c05", "c06", "c07", "c08", "c09", "c10", "c11"} {
		if groups[g] == 0 {
			t.Errorf("group %s has no case", g)
		}
	}
}

// conformCase is the state of one running case.
type conformCase struct {
	t                  *testing.T
	file               string
	line               int
	tm                 *term
	replies            []byte
	title, cwd, clip   string
	hasTitle, hasCwd   bool
	hasClip            bool
	bells              int
	agent              AgentReport
	cols, rows, hist   int
	tokens             []string
	firstCommandIsSize bool
}

func (c *conformCase) failf(format string, args ...any) {
	c.t.Helper()
	c.t.Errorf("%s:%d: %s", c.file, c.line, fmt.Sprintf(format, args...))
}

func (c *conformCase) fatalf(format string, args ...any) {
	c.t.Helper()
	c.t.Fatalf("%s:%d: %s", c.file, c.line, fmt.Sprintf(format, args...))
}

func runCase(t *testing.T, file string) {
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := &conformCase{t: t, file: file, cols: 80, rows: 24, hist: 10000}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	described := false
	for sc.Scan() {
		c.line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#") {
			described = described || c.line == 1
			continue
		}
		toks, err := tokenize(text)
		if err != nil {
			c.fatalf("%v", err)
		}
		c.tokens = toks
		c.step()
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !described {
		t.Errorf("%s: the first line is not a description", file)
	}
	if c.tm == nil {
		t.Errorf("%s: the case writes nothing and checks nothing", file)
	}
}

// tokenize splits a line into words and Go-quoted strings; a quoted string
// is returned unquoted.
func tokenize(s string) ([]string, error) {
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

func (c *conformCase) arg(i int) string {
	c.t.Helper()
	if i >= len(c.tokens) {
		c.fatalf("%q needs more arguments", strings.Join(c.tokens, " "))
	}
	return c.tokens[i]
}

func (c *conformCase) num(i int) int {
	c.t.Helper()
	n, err := strconv.Atoi(c.arg(i))
	if err != nil {
		c.fatalf("argument %d of %q is not a number", i, c.tokens[0])
	}
	return n
}

// term returns the case's terminal, made at the size of the last size.
func (c *conformCase) term() *term {
	if c.tm == nil {
		o := Options{Cols: c.cols, Rows: c.rows, History: c.hist}
		o.OnTitle = func(s string) { c.title, c.hasTitle = s, true }
		o.OnCwd = func(s string) { c.cwd, c.hasCwd = s, true }
		o.OnClipboard = func(b []byte) { c.clip, c.hasClip = string(b), true }
		o.OnBell = func() { c.bells++ }
		o.OnAgent = func(a AgentReport) { c.agent = a }
		c.tm = New(o).(*term)
		c.t.Cleanup(c.tm.Close)
	}
	return c.tm
}

func (c *conformCase) drain() { c.replies = append(c.replies, c.tm.Replies()...) }

func (c *conformCase) step() {
	c.t.Helper()
	switch c.tokens[0] {
	case "size":
		if c.tm != nil {
			c.fatalf("size after the terminal was made")
		}
		c.cols, c.rows = c.num(1), c.num(2)
	case "write":
		c.term().Write([]byte(c.arg(1)))
		c.drain()
	case "resize":
		c.term().Resize(c.num(1), c.num(2))
		c.drain()
	case "colors":
		col := Colors{Fg: c.arg(1), Bg: c.arg(2), Dark: len(c.tokens) > 3 && c.tokens[3] == "dark"}
		c.term().SetColors(col)
	case "golden":
		golden.Assert(c.t, c.arg(1), screenOf(c.term()).Runs())
	case "expect":
		c.expect()
	default:
		c.fatalf("unknown verb %q", c.tokens[0])
	}
}

// screenOf is the screen of a terminal as the golden files show it.
func screenOf(tm *term) golden.Screen {
	cols, rows := tm.Size()
	s := golden.Screen{Cols: cols, Rows: rows}
	for y := range rows {
		s.Lines = append(s.Lines, tm.Row(y))
	}
	cur := tm.Cursor()
	s.CursorX, s.CursorY, s.CursorVisible = cur.X, cur.Y, cur.Visible
	return s
}

func (c *conformCase) expect() {
	c.t.Helper()
	tm := c.term()
	what := c.arg(1)
	switch what {
	case "row":
		y := c.num(2)
		if got := golden.PlainRow(tm.Row(y)); got != c.arg(3) {
			c.failf("row %d = %q, want %q", y, got, c.arg(3))
		}
	case "styled":
		y := c.num(2)
		if got := tm.Row(y); got != c.arg(3) {
			c.failf("styled row %d = %q, want %q", y, got, c.arg(3))
		}
	case "cursor":
		cur := tm.Cursor()
		if cur.X != c.num(2) || cur.Y != c.num(3) {
			c.failf("cursor at %d,%d, want %d,%d", cur.X, cur.Y, c.num(2), c.num(3))
		}
		for _, w := range c.tokens[4:] {
			ok := true
			switch w {
			case "visible":
				ok = cur.Visible
			case "hidden":
				ok = !cur.Visible
			case "block", "underline", "bar":
				ok = cur.Shape == w
			case "blink":
				ok = cur.Blink
			case "steady":
				ok = !cur.Blink
			default:
				c.fatalf("unknown cursor word %q", w)
			}
			if !ok {
				c.failf("cursor %+v is not %s", cur, w)
			}
		}
	case "alt":
		if got := strconv.FormatBool(tm.Modes().Alt); got != c.arg(2) {
			c.failf("alt = %s, want %s", got, c.arg(2))
		}
	case "mouse":
		m := tm.Modes()
		got := m.Mouse
		if got == "" {
			got = "none"
		}
		if m.MouseSGR {
			got += " sgr"
		}
		if want := strings.Join(c.tokens[2:], " "); got != want {
			c.failf("mouse = %q, want %q", got, want)
		}
	case "keys":
		m := tm.Modes()
		var got []string
		for _, f := range []struct {
			on   bool
			name string
		}{{m.AppCursor, "appcursor"}, {m.AppKeypad, "appkeypad"}, {m.Paste, "paste"}, {m.Focus, "focus"}} {
			if f.on {
				got = append(got, f.name)
			}
		}
		if m.Kitty != 0 {
			got = append(got, "kitty="+strconv.Itoa(m.Kitty))
		}
		g := strings.Join(got, ",")
		if g == "" {
			g = "none"
		}
		if g != c.arg(2) {
			c.failf("keys = %s, want %s", g, c.arg(2))
		}
	case "replies":
		c.drain()
		if got := string(c.replies); got != c.arg(2) {
			c.failf("replies = %q, want %q", got, c.arg(2))
		}
		c.replies = nil
	case "title":
		c.checkLast("title", c.title, c.hasTitle)
		c.title, c.hasTitle = "", false
	case "cwd":
		c.checkLast("cwd", c.cwd, c.hasCwd)
		c.cwd, c.hasCwd = "", false
	case "clipboard":
		c.checkLast("clipboard", c.clip, c.hasClip)
		c.clip, c.hasClip = "", false
	case "bell":
		if c.bells != c.num(2) {
			c.failf("bells = %d, want %d", c.bells, c.num(2))
		}
		c.bells = 0
	case "agent":
		if want := (AgentReport{State: c.arg(2), Kind: c.arg(3)}); c.agent != want {
			c.failf("agent = %+v, want %+v", c.agent, want)
		}
		c.agent = AgentReport{}
	case "range":
		first, base := tm.Range()
		if first != int64(c.num(2)) || base != int64(c.num(3)) {
			c.failf("range = %d %d, want %d %d", first, base, c.num(2), c.num(3))
		}
	case "line":
		abs, _ := strconv.ParseInt(c.arg(2), 10, 64)
		row, ok := tm.Line(abs)
		if got := golden.PlainRow(row); !ok || got != c.arg(3) {
			c.failf("line %d = %q, %v, want %q", abs, got, ok, c.arg(3))
		}
	case "sync":
		if got := strconv.FormatBool(tm.SyncActive()); got != c.arg(2) {
			c.failf("sync = %s, want %s", got, c.arg(2))
		}
	default:
		c.fatalf("unknown expectation %q", what)
	}
}

func (c *conformCase) checkLast(what, got string, has bool) {
	c.t.Helper()
	want := c.arg(2)
	if (want == "") == has || got != want {
		c.failf("%s = %q (seen %v), want %q", what, got, has, want)
	}
}
