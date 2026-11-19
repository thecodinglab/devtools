//go:build cgo

package vt

import (
	"bytes"
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// recorder collects what the callbacks of one terminal were handed.
type recorder struct {
	titles, cwds []string
	clips        [][]byte
	bells        int
	agents       []AgentReport
	reflows      int
}

func (r *recorder) options(o *Options) {
	o.OnTitle = func(s string) { r.titles = append(r.titles, s) }
	o.OnCwd = func(s string) { r.cwds = append(r.cwds, s) }
	o.OnClipboard = func(b []byte) { r.clips = append(r.clips, b) }
	o.OnBell = func() { r.bells++ }
	o.OnAgent = func(a AgentReport) { r.agents = append(r.agents, a) }
	o.OnReflow = func(func(int64) (int64, bool)) { r.reflows++ }
}

func recorded(t *testing.T, cols, rows int) (*term, *recorder) {
	r := &recorder{}
	return termAt(t, cols, rows, r.options), r
}

func TestAClipboardReadIsIgnored(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	for _, q := range []string{"\x1b]52;c;?\x07", "\x1b]52;c;?\x1b\\", "\x1b]52;p;?\x07", "\x1b]52;;?\x07"} {
		tm.Write([]byte(q))
		if got := tm.Replies(); got != nil {
			t.Errorf("the read %q was answered with %q; it must get no answer at all", q, got)
		}
	}
	if len(r.clips) != 0 {
		t.Errorf("a clipboard read reached OnClipboard: %q", r.clips)
	}
}

func TestClipboardSetAboveOneMiBIsDropped(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	set := func(b []byte) { tm.Write([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString(b) + "\x07")) }
	small := []byte("hello")
	full := bytes.Repeat([]byte("x"), 1<<20)
	set(small)
	set(full)
	set(append(full, 'y'))
	set(bytes.Repeat([]byte("z"), 2<<20))
	tm.Write([]byte("\x1b]52;c;\x07")) // empty: never delivered
	if len(r.clips) != 2 || !bytes.Equal(r.clips[0], small) || !bytes.Equal(r.clips[1], full) {
		var sizes []int
		for _, c := range r.clips {
			sizes = append(sizes, len(c))
		}
		t.Fatalf("OnClipboard got sets of %v bytes, want [5 1048576]", sizes)
	}
	// The callback owns its bytes: they survive the next write.
	set([]byte("other"))
	if !bytes.Equal(r.clips[0], small) {
		t.Fatalf("a delivered clipboard changed afterwards: %q", r.clips[0])
	}
}

func TestTitleIsCutAndSanitised(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	long := strings.Repeat("é", 300)
	tm.Write([]byte("\x1b]0;" + long + "\x07"))
	tm.Write([]byte("\x1b]2;a‮b​c‍d\x1b\\"))
	tm.Write([]byte("\x1b]2;tab\there\x07"))
	if len(r.titles) != 3 {
		t.Fatalf("titles %q, want three", r.titles)
	}
	if n := utf8.RuneCountInString(r.titles[0]); n != 256 || r.titles[0] != strings.Repeat("é", 256) {
		t.Errorf("a title of 300 runes became %d runes", n)
	}
	if r.titles[1] != "abcd" {
		t.Errorf("title with format characters = %q, want %q", r.titles[1], "abcd")
	}
	if strings.ContainsAny(r.titles[2], "\t\x1b\x07") {
		t.Errorf("title with a control character = %q", r.titles[2])
	}
	if got := sanitizeTitle("a\xffb\u009bc"); got != "a�bc" {
		t.Errorf("sanitizeTitle of invalid UTF-8 and C1 = %q", got)
	}
}

func TestOSC7FromAnotherHostIsIgnored(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	me, _ := os.Hostname()
	reports := []struct {
		uri  string
		want string // "" when ignored
	}{
		{"file:///home/u/dev", "/home/u/dev"},
		{"file://localhost/tmp/a%20b", "/tmp/a b"},
		{"file://" + me + "/srv", "/srv"},
		{"file://other-host/home/u", ""},
		{"file://other-host.example.com/home/u", ""},
		{"http://" + me + "/x", ""},
		{"file:relative/path", ""},
		{"/bare/path", ""},
		{"file:///home/%0a/evil", ""},
		{"file:///x?q=1", ""},
	}
	for _, rep := range reports {
		r.cwds = nil
		tm.Write([]byte("\x1b]7;" + rep.uri + "\x07"))
		switch {
		case rep.want == "" && len(r.cwds) != 0:
			t.Errorf("%q reached OnCwd as %q", rep.uri, r.cwds)
		case rep.want != "" && (len(r.cwds) != 1 || r.cwds[0] != rep.want):
			t.Errorf("%q gave %q, want [%q]", rep.uri, r.cwds, rep.want)
		}
	}
}

func TestAnEmptyCwdIsIgnored(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	tm.Write([]byte("\x1b]7;file:///a\x07\x1b]7;\x07\x1b]7;\x1b\\\x1b]7;file://\x07"))
	if !reflect.DeepEqual(r.cwds, []string{"/a"}) {
		t.Fatalf("cwds %q, want only [/a]", r.cwds)
	}
}

func TestOSC7501ReachesOnAgent(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	var want []AgentReport
	for _, st := range []string{"idle", "working", "done", "blocked", "error"} {
		tm.Write([]byte("\x1b]7501;state=" + st + "\x1b\\"))
		want = append(want, AgentReport{State: st})
	}
	for _, k := range []string{"permission", "question", "auth"} {
		tm.Write([]byte("\x1b]7501;state=blocked:kind=" + k + ":app=claude-code\x07"))
		want = append(want, AgentReport{State: "blocked", Kind: k})
	}
	tm.Write([]byte("\x1b]7501;state=clear\x1b\\"))
	want = append(want, AgentReport{State: "clear"})
	tm.Write([]byte("\x1b]7501;state=bogus\x1b\\")) // not a valid report: the library drops it
	if !reflect.DeepEqual(r.agents, want) {
		t.Fatalf("OnAgent got %+v\nwant %+v", r.agents, want)
	}
}

func TestOSC7501RecordsWithAnIdAreDropped(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	tm.Write([]byte("\x1b]7501;state=done\x1b\\"))
	tm.Write([]byte("\x1b]7501;state=working:id=t1\x1b\\"))
	tm.Write([]byte("\x1b]7501;state=clear:id=t1\x1b\\"))
	if !reflect.DeepEqual(r.agents, []AgentReport{{State: "done"}}) {
		t.Fatalf("OnAgent got %+v, want one call with done", r.agents)
	}
}

func TestBellReachesOnBell(t *testing.T) {
	tm, r := recorded(t, 80, 24)
	tm.Write([]byte("a\x07b\x07\x1b]0;t\x07"))
	if r.bells != 2 {
		t.Fatalf("OnBell called %d times, want 2 (a BEL that ends an OSC does not ring)", r.bells)
	}
}

func TestCallbacksWithoutAFunctionAreSkipped(t *testing.T) {
	tm := termAt(t, 80, 24)
	tm.Write([]byte("\x07\x1b]0;t\x07\x1b]7;file:///x\x07\x1b]52;c;aGk=\x07\x1b]7501;state=idle\x07"))
	if got := tm.Replies(); got != nil {
		t.Fatalf("replies %q", got)
	}
}

func TestNoCallbackCallsTheLibrary(t *testing.T) {
	// A callback runs inside a library call; the terminal is not
	// reentrant (3.2.1). The Go side of every callback is in callbacks.go,
	// and nothing there names a function of the library.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "callbacks.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	exports := 0
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && x.Name == "C" && strings.HasPrefix(n.Sel.Name, "ghostty_") {
				t.Errorf("%s: callbacks.go calls %s", fset.Position(n.Pos()), n.Sel.Name)
			}
		case *ast.CallExpr:
			// No method of term that reaches the library.
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "Write", "Resize", "Row", "Cursor", "Modes", "SyncActive", "SetColors", "Close", "render", "liveCells", "screenRow", "scrollbackRows", "libSize", "setLineLimit", "keepSize":
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "t" {
						t.Errorf("%s: a callback calls t.%s", fset.Position(n.Pos()), sel.Sel.Name)
					}
				}
			}
		case *ast.FuncDecl:
			if hasExport(n) {
				exports++
			}
		}
		return true
	})
	if exports != 8 {
		t.Errorf("found %d //export functions in callbacks.go, want the 8 the trampolines call", exports)
	}
}

func hasExport(fn *ast.FuncDecl) bool {
	if fn.Doc == nil {
		return false
	}
	for _, c := range fn.Doc.List {
		if strings.HasPrefix(c.Text, "//export ") {
			return true
		}
	}
	return false
}
