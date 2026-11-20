package integration

// Level 3 (10.1.1): the real server with real panes over the wire. Keys,
// screens, sizes, splits, directories and the pane environment.

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit/golden"
)

func TestTypedKeysReachTheProgramAndItsOutputComesBack(t *testing.T) {
	home, socket := startServer(t)
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	if len(v.Panes) != 1 {
		t.Fatalf("view.ok %+v", v)
	}
	p := v.Panes[0]
	c.WaitFor(p, "ready")
	// The hex helper prints every byte it reads: what comes back is what
	// the server's encoder wrote to the PTY for the keys.
	c.Type(p, "aZ ")
	c.WaitFor(p, "61 5a 20")
	c.Key(p, "enter", 0, "")
	c.WaitFor(p, "61 5a 20 0d")
	c.Key(p, "c", proto.ModCtrl, "")
	c.WaitFor(p, "0d 03")
	c.Key(p, "up", 0, "")
	c.WaitFor(p, "03 1b 5b 41")
	c.Key(p, "x", proto.ModAlt, "x")
	c.WaitFor(p, "1b 5b 41 1b 78")
	c.Send(proto.Paste{Pane: p, Text: "é"})
	c.WaitFor(p, "1b 78 c3 a9")
	// A key for a pane the connection does not display is dropped.
	c.Key("p999", "a", 0, "a")
	c.Type(p, "b")
	s := c.WaitFor(p, "c3 a9 62")
	if strings.Contains(strings.Join(plainRows(s), " "), "c3 a9 61") {
		t.Fatalf("a key for another pane reached this one:\n%s", s.Plain())
	}
}

func TestTwoClientsSeeTheSamePane(t *testing.T) {
	home, socket := startServer(t)
	dir := work(t, home)
	a := prototest.Dial(t, socket, prototest.Interactive())
	va := a.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	b := prototest.Dial(t, socket, prototest.Interactive())
	vb := b.View(proto.ViewSet{Session: va.Session, Cols: 80, Rows: 24})
	if vb.Session != va.Session || len(vb.Panes) != 1 || vb.Panes[0] != va.Panes[0] {
		t.Fatalf("the second view is %+v, the first %+v", vb, va)
	}
	p := va.Panes[0]
	a.Type(p, "a")
	b.WaitFor(p, "61")
	b.Type(p, "b")
	sa := a.WaitFor(p, "61 62")
	sb := b.WaitFor(p, "61 62")
	if sa.Plain() != sb.Plain() {
		t.Fatalf("the two clients show different screens:\n%s\n%s", sa.Plain(), sb.Plain())
	}
	if w := b.Welcome(); w.Clients != 2 {
		t.Fatalf("welcome.clients of the second client is %d", w.Clients)
	}
}

func TestASmallerClientIsClippedAndALargerOneIsPadded(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("size"))
	dir := work(t, home)
	small := prototest.Dial(t, socket, prototest.Interactive())
	v := small.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 60, Rows: 20})
	p := v.Panes[0]
	waitSize(small, p, 60, 20)

	// The larger client interacted last: the session takes its size, and
	// the smaller one receives frames of that size, of which it shows the
	// top left part.
	large := prototest.Dial(t, socket, prototest.Interactive())
	large.View(proto.ViewSet{Session: v.Session, Cols: 100, Rows: 30})
	waitSize(large, p, 100, 30)
	s := waitSize(small, p, 100, 30)
	if s.Cols != 100 || s.Rows != 30 {
		t.Fatalf("the smaller client gets frames of %dx%d", s.Cols, s.Rows)
	}
	shown, err := draw(t, 60, 20, s.Lines[:20])
	if err != nil {
		t.Fatal(err)
	}
	if !canvasHas(shown, "100 x 30") {
		t.Fatalf("the clipped canvas lacks the program's output:\n%s", strings.Join(shown, "\n"))
	}

	// The smaller client types: the session takes its size again, and the
	// larger one receives frames of 60x20, around which it pads.
	small.Type(p, "x")
	waitSize(small, p, 60, 20)
	s = waitSize(large, p, 60, 20)
	if s.Cols != 60 || s.Rows != 20 || len(s.Lines) != 20 {
		t.Fatalf("the larger client gets frames of %dx%d", s.Cols, s.Rows)
	}
	padded, err := draw(t, 100, 30, append(append([]string(nil), s.Lines...), make([]string, 10)...))
	if err != nil {
		t.Fatal(err)
	}
	for y := 20; y < 30; y++ {
		if padded[y] != "" {
			t.Fatalf("row %d of the padded canvas is %q", y, padded[y])
		}
	}
}

func TestTheSizeFollowsTheClientThatTypedLast(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("size"))
	dir := work(t, home)
	a := prototest.Dial(t, socket, prototest.Interactive())
	v := a.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	waitSize(a, p, 80, 24)
	b := prototest.Dial(t, socket, prototest.Interactive())
	b.View(proto.ViewSet{Session: v.Session, Cols: 70, Rows: 21})
	waitSize(a, p, 70, 21)
	// b's own screen must show 70 x 21 too before a types: otherwise its
	// last size row can still be the first 80 x 24, the wait below passes
	// before the server counted a's key, and b's key may be counted first.
	waitSize(b, p, 70, 21)
	a.Type(p, "a")
	waitSize(b, p, 80, 24)
	// a's own screen must show 80 x 24 too before b types: otherwise its
	// last size row can still be the 70 x 21 above, and the wait below
	// passes before the server counted b's key.
	waitSize(a, p, 80, 24)
	b.Type(p, "b")
	waitSize(a, p, 70, 21)
	// The owner's resize changes the session's size; the resize of a window
	// that does not own the size is stored and changes nothing until that
	// client types (TestANonOwnersResizeChangesNothing, P1-16).
	b.Send(proto.ViewResize{Cols: 72, Rows: 22})
	waitSize(a, p, 72, 22)
	a.Send(proto.ViewResize{Cols: 90, Rows: 25})
	// The loop applies a's notifications and requests in the order sent:
	// after the reply the resize was applied.
	a.Request(proto.StateGet{})
	b.Type(p, "c")
	s := b.WaitScreen(p, "the echo of c", func(s golden.Screen) bool {
		return slices.Contains(plainRows(s), "c")
	})
	if s.Cols != 72 || s.Rows != 22 {
		t.Fatalf("a non-owner's resize changed the size to %dx%d", s.Cols, s.Rows)
	}
	a.Type(p, "d")
	waitSize(b, p, 90, 25)
}

func TestSplitAndCloseResizeThePrograms(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("size"))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 81, Rows: 24})
	first := v.Panes[0]
	waitSize(c, first, 81, 24)

	r := c.Request(proto.PaneSplit{Pane: first, Direction: proto.SplitRight})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	var sp proto.PaneSplitResult
	r.Msg.Decode(&sp)
	// 3.3: w1 = cols / 2, one divider column, the rest for the second.
	waitSize(c, first, 40, 24)
	waitSize(c, sp.Pane, 40, 24)
	st := c.State()
	tab := st.Tabs[st.Panes[first].Tab]
	if tab.Split != proto.SplitRight || len(tab.Panes) != 2 || tab.Focused != sp.Pane {
		t.Fatalf("the tab after the split: %+v", tab)
	}
	if s := c.Screen(first); s.Cols != 40 || s.Rows != 24 {
		t.Fatalf("the frame of the first pane is %dx%d", s.Cols, s.Rows)
	}

	// A third pane nests: the second is split down, 12 and 11 rows around
	// the divider row.
	r = c.Request(proto.PaneSplit{Pane: sp.Pane, Direction: proto.SplitDown})
	if !r.OK {
		t.Fatalf("a second split: %s: %s", r.Code, r.Text)
	}
	var third proto.PaneSplitResult
	r.Msg.Decode(&third)
	waitSize(c, sp.Pane, 40, 12)
	waitSize(c, third.Pane, 40, 11)
	waitSize(c, first, 40, 24)

	// A resize moves the border between the first pane and the other two.
	r = c.Request(proto.PaneResize{Pane: first, Direction: "right", Cells: 5})
	var rs proto.PaneResizeResult
	r.Msg.Decode(&rs)
	if !r.OK || !rs.Moved {
		t.Fatalf("pane.resize was answered %+v", r)
	}
	waitSize(c, first, 45, 24)
	waitSize(c, sp.Pane, 35, 12)
	waitSize(c, third.Pane, 35, 11)

	// Closing the third gives the second its rows back.
	if r := c.Request(proto.PaneClose{Pane: third.Pane}); !r.OK {
		t.Fatalf("pane.close: %s: %s", r.Code, r.Text)
	}
	waitSize(c, sp.Pane, 35, 24)

	if r := c.Request(proto.PaneClose{Pane: sp.Pane}); !r.OK {
		t.Fatalf("pane.close: %s: %s", r.Code, r.Text)
	}
	waitSize(c, first, 81, 24)

	r = c.Request(proto.PaneSplit{Pane: first, Direction: proto.SplitDown})
	if !r.OK {
		t.Fatalf("pane.split down: %s: %s", r.Code, r.Text)
	}
	r.Msg.Decode(&sp)
	// h1 = rows / 2, one divider row, the rest for the second.
	waitSize(c, first, 81, 12)
	waitSize(c, sp.Pane, 81, 11)
	if r := c.Request(proto.PaneClose{Pane: first}); !r.OK {
		t.Fatalf("pane.close of the first: %s: %s", r.Code, r.Text)
	}
	waitSize(c, sp.Pane, 81, 24)
	if tab := c.State().Tabs[tab.ID]; tab.Split != "" || len(tab.Panes) != 1 || tab.Focused != sp.Pane {
		t.Fatalf("the tab after the close: %+v", tab)
	}
}

func TestANewTabStartsInTheSessionDirectoryAndASplitInThePanesDirectory(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("envwait"))
	dir := work(t, home)
	elsewhere := work(t, home, "elsewhere")
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 120, Rows: 40})
	waitWd(t, c, v.Panes[0], dir)

	// A tab opened in another directory: its pane starts there.
	r := c.Request(proto.TabNew{Session: v.Session, Cwd: elsewhere})
	if !r.OK {
		t.Fatalf("tab.new: %s: %s", r.Code, r.Text)
	}
	var tn proto.TabNewResult
	r.Msg.Decode(&tn)
	waitWd(t, c, tn.Pane, elsewhere)

	// A split of that pane without cwd starts in the pane's directory, not
	// in the session's.
	r = c.Request(proto.PaneSplit{Pane: tn.Pane, Direction: proto.SplitRight})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	var sp proto.PaneSplitResult
	r.Msg.Decode(&sp)
	waitWd(t, c, sp.Pane, elsewhere)

	// A new tab without cwd starts in the session directory, wherever the
	// focused pane is.
	r = c.Request(proto.TabNew{Session: v.Session})
	if !r.OK {
		t.Fatalf("tab.new: %s: %s", r.Code, r.Text)
	}
	r.Msg.Decode(&tn)
	waitWd(t, c, tn.Pane, dir)

	// A split with cwd starts there.
	r = c.Request(proto.PaneSplit{Pane: tn.Pane, Direction: proto.SplitDown, Cwd: elsewhere})
	if !r.OK {
		t.Fatalf("pane.split with cwd: %s: %s", r.Code, r.Text)
	}
	r.Msg.Decode(&sp)
	waitWd(t, c, sp.Pane, elsewhere)
}

// waitWd waits until the helper envwait of the pane printed its working
// directory and checks it.
func waitWd(t *testing.T, c *prototest.Client, pane, want string) {
	t.Helper()
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	s := c.WaitFor(pane, "end")
	got, ok := field(s, "wd")
	if !ok {
		t.Fatalf("pane %s printed no directory:\n%s", pane, s.Plain())
	}
	if got != want {
		t.Fatalf("pane %s started in %s, want %s", pane, got, want)
	}
}

func TestPaneEnvironmentOverTheWire(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("envwait"), servertest.WithOptions(func(o *server.Options) {
		// The marks of the multiplexers and terminals the server could
		// have been started from, named in DEVTOOLS_PASS_ENV too: none
		// reaches a pane (3.2.1, 4.7). The server's own values of the
		// fixed variables do not either.
		marks := []string{"TMUX=/tmp/tmux-1/default,1,0", "TMUX_PANE=%1", "STY=1.pts", "ZELLIJ=0",
			"ZELLIJ_SESSION_NAME=z", "HERDR_ENV=1", "HERDR_PANE_ID=p1", "CLAUDECODE=1",
			"KITTY_WINDOW_ID=1", "GHOSTTY_RESOURCES_DIR=/x", "DEVTOOLS_PANE=p77", "TERM=dumb",
			"XDG_CONFIG_HOME=/nowhere", "LC_TIME=C"}
		var names []string
		var env []string
		for _, kv := range o.Env {
			if v, ok := strings.CutPrefix(kv, "DEVTOOLS_PASS_ENV="); ok {
				names = strings.Split(v, ",")
				continue
			}
			env = append(env, kv)
		}
		for _, kv := range marks {
			name, _, _ := strings.Cut(kv, "=")
			if name != "XDG_CONFIG_HOME" && name != "LC_TIME" {
				names = append(names, name)
			}
		}
		o.Env = append(append(env, marks...), "DEVTOOLS_PASS_ENV="+strings.Join(names, ","))
	}))
	hello := prototest.Interactive()
	hello.Env = map[string]string{"DISPLAY": ":7", "WAYLAND_DISPLAY": "wayland-7", "EDITOR": "ed"}
	c := prototest.Dial(t, socket, hello)
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 200, Rows: 50})
	p := v.Panes[0]
	s := c.WaitFor(p, "end")
	env := map[string]string{}
	for _, row := range plainRows(s) {
		if name, val, ok := strings.Cut(row, "="); ok && !strings.Contains(name, " ") {
			env[name] = val
		}
	}
	want := map[string]string{
		"TERM":            "xterm-256color",
		"COLORTERM":       "truecolor",
		"DEVTOOLS_PANE":   p,
		"DEVTOOLS_SOCKET": socket,
		"HOME":            home,
		"PATH":            "/usr/bin:/bin",
		"LANG":            "C.UTF-8",
		"LC_TIME":         "C",
		"DISPLAY":         ":7",
		"WAYLAND_DISPLAY": "wayland-7",
	}
	for name, val := range want {
		if env[name] != val {
			t.Errorf("%s=%q in the pane, want %q", name, env[name], val)
		}
	}
	if tty := env["DEVTOOLS_TTY"]; !strings.HasPrefix(tty, "/dev/") {
		t.Errorf("DEVTOOLS_TTY=%q", tty)
	}
	for _, name := range []string{"TMUX", "TMUX_PANE", "STY", "ZELLIJ", "ZELLIJ_SESSION_NAME", "HERDR_ENV",
		"HERDR_PANE_ID", "CLAUDECODE", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "XDG_STATE_HOME",
		"XDG_CONFIG_HOME", "EDITOR"} {
		if v, ok := env[name]; ok {
			t.Errorf("%s=%s reached the pane", name, v)
		}
	}
	if t.Failed() {
		t.Logf("the pane's environment:\n%s", s.Plain())
	}
}

func TestDetachAndReattachShowTheSameScreen(t *testing.T) {
	home, socket := startServer(t)
	a := prototest.Dial(t, socket, prototest.Interactive())
	v := a.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	a.WaitFor(p, "ready")
	a.Type(p, "detach")
	before := a.WaitFor(p, "64 65 74 61 63 68")
	a.Close()

	b := prototest.Dial(t, socket, prototest.Interactive())
	st := b.State()
	if _, ok := st.Sessions[v.Session]; !ok || st.Panes[p].State != proto.PaneRunning {
		t.Fatalf("after the detach the session or its pane is gone: %+v", st)
	}
	vb := b.View(proto.ViewSet{Session: v.Session, Cols: 80, Rows: 24})
	if vb.Panes[0] != p {
		t.Fatalf("the reattached view shows %v", vb.Panes)
	}
	after := b.WaitScreen(p, "the screen of before the detach", func(s golden.Screen) bool {
		return s.Plain() == before.Plain()
	})
	if after.Styled() != before.Styled() {
		t.Fatalf("the styled screens differ:\n%s\n%s", before.Styled(), after.Styled())
	}
	b.Type(p, "!")
	b.WaitFor(p, "68 21")
}
