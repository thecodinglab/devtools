package server

import (
	"fmt"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// waitSize waits until the session and its pane have the size.
func (ts *testServer) waitSize(session string, f *fakePane, want [2]int) {
	ts.t.Helper()
	testkit.Eventually(ts.t, 5*time.Second, "the size "+sizeText(want), func() bool {
		return ts.size(session) == want && f.lastResize() == want
	})
}

func sizeText(s [2]int) string { return fmt.Sprintf("%dx%d", s[0], s[1]) }

func TestTheMostRecentlyInteractingClientOwnsTheSize(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a, b := ts.interactive(), ts.interactive()
	ts.view(a, s, 100, 30)
	f := ts.pane(p)
	ts.waitSize(s, f, [2]int{100, 30})
	ts.view(b, s, 80, 20)
	ts.waitSize(s, f, [2]int{80, 20})

	a.Key(p, "x", 0, "x")
	ts.waitSize(s, f, [2]int{100, 30})
	b.Send(proto.PaneScroll{Pane: p})
	ts.waitSize(s, f, [2]int{80, 20})
	a.Send(proto.Mouse{Pane: p, Kind: "press", Button: "left", X: 1, Y: 1})
	ts.waitSize(s, f, [2]int{100, 30})
	b.Send(proto.Paste{Pane: p, Text: "y"})
	ts.waitSize(s, f, [2]int{80, 20})
	a.Send(proto.Focus{Focused: true})
	ts.waitSize(s, f, [2]int{100, 30})
	// The owner's own resize changes the session.
	a.Send(proto.ViewResize{Cols: 90, Rows: 25})
	ts.waitSize(s, f, [2]int{90, 25})
}

func TestANonOwnersResizeChangesNothing(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a, b := ts.interactive(), ts.interactive()
	ts.view(b, s, 80, 20)
	ts.view(a, s, 100, 30)
	f := ts.pane(p)
	ts.waitSize(s, f, [2]int{100, 30})
	resizes := len(f.resizes)

	b.Send(proto.ViewResize{Cols: 60, Rows: 18})
	b.Send(proto.ViewResize{Cols: 61, Rows: 19})
	ts.sync(b)
	if got := ts.size(s); got != [2]int{100, 30} {
		t.Fatalf("a non-owner's view.resize set the size to %v", got)
	}
	f.mu.Lock()
	n := len(f.resizes)
	f.mu.Unlock()
	if n != resizes {
		t.Fatalf("a non-owner's view.resize resized the pane")
	}
	// The resize was stored: when the owner leaves, b's newest area counts.
	a.Send(proto.ViewClear{})
	ts.waitSize(s, f, [2]int{61, 19})
}

func TestTheNextClientIsPromotedWhenTheOwnerLeaves(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	other, _, _ := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b, d := ts.interactive(), ts.interactive(), ts.interactive()
	ts.view(a, s, 100, 30)
	ts.view(b, s, 90, 28)
	ts.view(d, s, 80, 20)
	f := ts.pane(p)
	ts.waitSize(s, f, [2]int{80, 20})

	d.Close() // the owner disconnects: the next most recent is b
	ts.waitSize(s, f, [2]int{90, 28})
	ts.view(b, other, 70, 20) // the owner views another session: a is left
	ts.waitSize(s, f, [2]int{100, 30})
}

func TestTheSizeStaysWhenNobodyDisplays(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 100, 30)
	f := ts.pane(p)
	ts.waitSize(s, f, [2]int{100, 30})
	a.Close()
	testkit.Eventually(t, 5*time.Second, "the connection is gone", func() bool {
		n := -1
		ts.do(func(c *core) { n = c.reg.interactive() })
		return n == 0
	})
	ok(t, c, proto.TabNew{Session: s})
	ts.sync(c)
	if got := ts.size(s); got != [2]int{100, 30} {
		t.Fatalf("with nobody displaying the session its size became %v", got)
	}
	if got := f.lastResize(); got != [2]int{100, 30} {
		t.Fatalf("with nobody displaying the session its pane was resized to %v", got)
	}
}

func TestAOneShotNeverResizes(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	if got := ts.size(s); got != [2]int{120, 40} {
		t.Fatalf("a session opened by a one-shot is %v, want 120x40", got)
	}
	if r := c.Request(proto.ViewSet{Session: s, Cols: 50, Rows: 10}); r.OK || r.Code != proto.CodeInvalid {
		t.Fatalf("view.set of a one-shot: %s %s", r.Msg.T, r.Code)
	}
	c.Send(proto.ViewResize{Cols: 50, Rows: 10})
	c.Send(proto.Key{Pane: p, Key: "a", Text: "a"})
	ok(t, c, proto.TabNew{Session: s})
	ts.sync(c)
	if got := ts.size(s); got != [2]int{120, 40} {
		t.Fatalf("a one-shot changed the size to %v", got)
	}
	if ts.panes.started() != 0 {
		t.Fatal("a one-shot spawned a pane")
	}
	ts.display(s)
	var tab proto.Tab
	for _, x := range ts.stateGet(c).Tabs {
		if x.ID == ts.stateGet(c).Sessions[0].Current {
			tab = x
		}
	}
	if f := ts.pane(tab.Panes[0]); f.o.Cols != 120 || f.o.Rows != 40 {
		t.Fatalf("the pane started at %dx%d, want 120x40", f.o.Cols, f.o.Rows)
	}
}

func withColors(fg, bg string, dark bool) proto.Hello {
	h := prototest.Interactive()
	h.Colors = &proto.Colors{Fg: fg, Bg: bg, Dark: dark}
	return h
}

func TestColoursFollowTheSizeOwner(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	dark := vt.Colors{Fg: "#c0caf5", Bg: "#1a1b26", Dark: true}
	light := vt.Colors{Fg: "#1a1b26", Bg: "#e1e2e7", Dark: false}
	a := prototest.Dial(t, ts.socket, withColors(dark.Fg, dark.Bg, dark.Dark))
	b := prototest.Dial(t, ts.socket, withColors(light.Fg, light.Bg, light.Dark))

	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	if f.o.Colors != dark {
		t.Fatalf("the pane started with %+v, want the owner's %+v", f.o.Colors, dark)
	}
	last := func() vt.Colors { l := f.colorLog(); return l[len(l)-1] }
	ts.view(b, s, 80, 24)
	testkit.Eventually(t, 5*time.Second, "the new owner's colours", func() bool { return last() == light })

	// A non-owner's new colours change nothing until it owns the size.
	other := vt.Colors{Fg: "#ffffff", Bg: "#101010", Dark: true}
	a.Send(proto.ViewColors{Fg: other.Fg, Bg: other.Bg, Dark: other.Dark})
	ts.sync(a)
	if last() != light {
		t.Fatalf("a non-owner's view.colors reached the pane: %+v", last())
	}
	a.Key(p, "x", 0, "x")
	testkit.Eventually(t, 5*time.Second, "the colours of a, the owner again", func() bool { return last() == other })
	// The owner's own change reaches the pane at once.
	a.Send(proto.ViewColors{Fg: dark.Fg, Bg: dark.Bg, Dark: dark.Dark})
	testkit.Eventually(t, 5*time.Second, "the owner's changed colours", func() bool { return last() == dark })
}
