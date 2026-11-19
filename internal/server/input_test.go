package server

import (
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

func TestInputReachesOnlyDisplayedPanes(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, t1, p1 := ts.open(c)
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	other, _, p3 := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b := ts.interactive(), ts.interactive()
	ts.view(a, s, 80, 24) // tab 2 is current: its pane runs
	ts.view(b, other, 80, 24)
	ok(t, c, proto.TabSelect{Session: s, Tab: t1})
	ts.sync(c)
	f1, f2, f3 := ts.pane(p1), ts.pane(tn.Pane), ts.pane(p3)

	a.Key(tn.Pane, "x", 0, "x") // a pane of the tab that is not current
	a.Key(p3, "y", 0, "y")      // a pane of another session
	a.Send(proto.Paste{Pane: tn.Pane, Text: "z"})
	a.Send(proto.Mouse{Pane: p3, Kind: "press", Button: "left"})
	a.Key("p999", "w", 0, "w")
	a.Key(p1, "a", 0, "a")
	testkit.Eventually(t, 5*time.Second, "the key reaches the displayed pane", func() bool { return f1.hasInput("key a") })
	if in := f2.inputLog(); len(in) != 0 {
		t.Fatalf("a pane that is not displayed got %v", in)
	}
	if in := f3.inputLog(); len(in) != 0 {
		t.Fatalf("a pane of another view got %v", in)
	}
	// Without a view nothing reaches a pane.
	a.Send(proto.ViewClear{})
	ts.sync(a) // view.clear goes through the loop, keys do not (W6)
	a.Key(p1, "b", 0, "b")
	ts.sync(a)
	if f1.hasInput("key b") {
		t.Fatal("a key reached a pane after view.clear")
	}
}

func TestInputIsAppliedInTheOrderSent(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	var want []string
	for n := 0; n < 300; n++ {
		switch n % 3 {
		case 0:
			k := string(rune('a' + n%26))
			a.Key(p, k, 0, k)
			want = append(want, "key "+k)
		case 1:
			a.Send(proto.Mouse{Pane: p, Kind: "press", Button: "left", X: n % 80, Y: n % 24})
			want = append(want, fmt.Sprintf("mouse press left %d %d", n%80, n%24))
		case 2:
			a.Send(proto.Paste{Pane: p, Text: fmt.Sprint(n), More: n%2 == 0})
			want = append(want, "")
		}
	}
	testkit.Eventually(t, 5*time.Second, "every input", func() bool { return len(f.inputLog()) >= len(want) })
	got := f.inputLog()
	open := false
	for n, w := range want {
		if w == "" {
			// A chunk with more opens the paste, the next one without closes it.
			more := n%2 == 0
			w = fmt.Sprintf("paste %q first=%t last=%t", fmt.Sprint(n), !open, !more)
			open = more
		}
		if got[n] != w {
			t.Fatalf("input %d is %q, want %q", n, got[n], w)
		}
	}
}

func TestABlockedPaneHoldsOnlyItsOwnClient(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s1, _, p1 := ts.open(c)
	s2, _, p2 := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b := ts.interactive(), ts.interactive()
	ts.view(a, s1, 80, 24)
	ts.view(b, s2, 80, 24)
	f1, f2 := ts.pane(p1), ts.pane(p2)
	f1.block()
	a.Key(p1, "x", 0, "x") // a's reader waits in the pane
	a.Key(p1, "y", 0, "y")

	b.Key(p2, "z", 0, "z")
	testkit.Eventually(t, 5*time.Second, "the other client's key", func() bool { return f2.hasInput("key z") })
	ts.openDir(c, mkdir(t, ts.dir+"/third")) // the loop goes on
	f2.write(0, "still drawn")
	b.WaitFor(p2, "still drawn")
	if len(f1.inputLog()) != 0 {
		t.Fatal("the blocked pane applied a key")
	}
	f1.unblock()
	testkit.Eventually(t, 5*time.Second, "the held keys, in order", func() bool {
		return slices.Equal(f1.inputLog(), []string{"key x", "key y"})
	})
}

func TestABlockedReaderIsNotALivenessFailure(t *testing.T) {
	const ping = 100 * time.Millisecond
	ts := newTestServer(t, func(o *Options) { o.PingInterval = ping })
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	a.KeepAlive(ping / 2)
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	f.block()
	a.Key(p, "x", 0, "x")
	time.Sleep(6 * ping) // the reader is held for three receive timeouts
	if a.Closed() {
		t.Fatal("the connection was closed while its reader waited for a pane")
	}
	f.unblock()
	testkit.Eventually(t, 5*time.Second, "the key", func() bool { return f.hasInput("key x") })
	ts.stateGet(a) // the connection still works
	if a.Closed() {
		t.Fatal("the connection was closed")
	}
}

func TestASilentClientIsClosedAfterTwoPingIntervals(t *testing.T) {
	const ping = 100 * time.Millisecond
	ts := newTestServer(t, func(o *Options) { o.PingInterval = ping })
	// A client that keeps alive stays.
	alive := ts.interactive()
	alive.KeepAlive(ping / 2)

	nc, m := rawHello(t, ts.socket, prototest.Interactive())
	if m.T != proto.TypeWelcome {
		t.Fatalf("hello answered %s", m.T)
	}
	start := time.Now()
	nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, err := proto.ReadMsg(nc) // the snapshot, the server's pings; nothing is answered
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("the silent connection ended with %v", err)
		}
	}
	if d := time.Since(start); d < 2*ping-20*time.Millisecond || d > 2*time.Second {
		t.Fatalf("the silent client was closed after %v; the receive timeout is %v", d, 2*ping)
	}
	time.Sleep(4 * ping)
	if alive.Closed() {
		t.Fatal("a client that sent pings was closed")
	}
	ts.stateGet(alive)
}

// scrolled sets up a session displayed by a at 80x24 whose pane holds the
// lines 0 to 1000.
func scrolledSetup(t *testing.T, ts *testServer, clients ...*prototest.Client) (s, p string, f *fakePane) {
	t.Helper()
	c := ts.oneshot()
	s, _, p = ts.open(c)
	ts.panes.mu.Lock()
	ts.panes.onStart = func(f *fakePane) { f.setRange(0, 1000) }
	ts.panes.mu.Unlock()
	for _, cl := range clients {
		ts.view(cl, s, 80, 24)
	}
	f = ts.pane(p)
	return s, p, f
}

func top(n int64) *int64 { return &n }

// lastTop returns the top of the newest frame of the pane on c, nil when it
// is live.
func lastTop(t *testing.T, c *prototest.Client, paneID string) (*int64, int) {
	fs, _ := framesOf(t, c, 0, paneID)
	if len(fs) == 0 {
		return nil, 0
	}
	return fs[len(fs)-1].Top, len(fs)
}

func waitTop(t *testing.T, c *prototest.Client, paneID string, want *int64) {
	t.Helper()
	testkit.Eventually(t, 5*time.Second, fmt.Sprintf("a frame with top %v", topText(want)), func() bool {
		got, _ := lastTop(t, c, paneID)
		return (got == nil) == (want == nil) && (got == nil || *got == *want)
	})
}

func topText(p *int64) string {
	if p == nil {
		return "live"
	}
	return fmt.Sprint(*p)
}

func TestTypingEndsScrolling(t *testing.T) {
	ts := newTestServer(t)
	a := ts.interactive()
	_, p, f := scrolledSetup(t, ts, a)
	a.Send(proto.PaneScroll{Pane: p, Top: top(500)})
	waitTop(t, a, p, top(500))
	if got := f.followed(); got[500] != 1 || len(got) != 1 {
		t.Fatalf("scrolled to 500 the pane follows %v", got)
	}
	a.Key(p, "q", 0, "q")
	waitTop(t, a, p, nil)
	testkit.Eventually(t, 5*time.Second, "the line is let go", func() bool { return len(f.followed()) == 0 })
	if !f.hasInput("key q") {
		t.Fatal("the key that ended scrolling did not reach the pane")
	}
	// A paste ends it too; a top at or below base is live; a top above the
	// history is clamped to first.
	a.Send(proto.PaneScroll{Pane: p, Top: top(-50)})
	waitTop(t, a, p, top(0))
	a.Send(proto.Paste{Pane: p, Text: "x"})
	waitTop(t, a, p, nil)
	a.Send(proto.PaneScroll{Pane: p, Top: top(1000)})
	ts.sync(a)
	waitTop(t, a, p, nil)
}

func TestScrollPositionsArePerConnection(t *testing.T) {
	ts := newTestServer(t)
	a, b := ts.interactive(), ts.interactive()
	_, p, f := scrolledSetup(t, ts, a, b)
	a.Send(proto.PaneScroll{Pane: p, Top: top(400)})
	b.Send(proto.PaneScroll{Pane: p, Top: top(600)})
	waitTop(t, a, p, top(400))
	waitTop(t, b, p, top(600))
	a.Key(p, "x", 0, "x")
	waitTop(t, a, p, nil)
	f.write(0, "new output")
	a.WaitFor(p, "new output")
	ts.sync(b)
	if got, _ := lastTop(t, b, p); got == nil || *got != 600 {
		t.Fatalf("b's frames have top %s after a returned to live", topText(got))
	}
	if got := f.followed(); got[600] != 1 || len(got) != 1 {
		t.Fatalf("the pane follows %v, want b's line 600 only", got)
	}
}

func TestAScrollFollowsItsTopAndItsMarks(t *testing.T) {
	ts := newTestServer(t)
	a := ts.interactive()
	_, p, f := scrolledSetup(t, ts, a)
	a.Send(proto.PaneScroll{Pane: p, Top: top(500), Marks: []proto.Pos{{Line: 510, Col: 4}, {Line: 505, Col: 0}}})
	testkit.Eventually(t, 5*time.Second, "three followed lines", func() bool { return len(f.followed()) == 3 })
	if got := sortedKeys(f.followed()); !slices.Equal(got, []int64{500, 505, 510}) {
		t.Fatalf("followed %v", got)
	}
	a.Send(proto.PaneScroll{Pane: p, Top: top(520), Marks: []proto.Pos{{Line: 530, Col: 1}}})
	testkit.Eventually(t, 5*time.Second, "the new lines", func() bool {
		return slices.Equal(sortedKeys(f.followed()), []int64{520, 530})
	})
	calls := f.followCalls()
	want := []string{"follow 500", "follow 510", "follow 505", "unfollow 500", "unfollow 510", "unfollow 505", "follow 520", "follow 530"}
	if !slices.Equal(calls, want) {
		t.Fatalf("Follow and Unfollow were called as\n%s\nwant\n%s", joined(calls), joined(want))
	}
	// Marks while live: the lines are followed, the view stays live.
	a.Send(proto.PaneScroll{Pane: p, Marks: []proto.Pos{{Line: 990, Col: 2}}})
	testkit.Eventually(t, 5*time.Second, "the mark alone", func() bool {
		return slices.Equal(sortedKeys(f.followed()), []int64{990})
	})
	waitTop(t, a, p, nil)
}

func TestReturningToLiveUnfollowsEveryLine(t *testing.T) {
	ts := newTestServer(t)
	a := ts.interactive()
	_, p, f := scrolledSetup(t, ts, a)
	scroll := func() {
		t.Helper()
		a.Send(proto.PaneScroll{Pane: p, Top: top(300), Marks: []proto.Pos{{Line: 310}, {Line: 320}}})
		testkit.Eventually(t, 5*time.Second, "three followed lines", func() bool { return len(f.followed()) == 3 })
	}
	none := func(what string) {
		t.Helper()
		testkit.Eventually(t, 5*time.Second, "no followed line "+what, func() bool { return len(f.followed()) == 0 })
	}
	scroll()
	a.Send(proto.PaneScroll{Pane: p})
	none("after a return to live")
	scroll()
	other, _, _ := ts.openDir(ts.oneshot(), mkdir(t, ts.dir+"/other"))
	ts.view(a, other, 80, 24)
	none("after a view change")
	s := ts.stateGet(a).Sessions
	for _, x := range s {
		if x.Dir == ts.dir {
			ts.view(a, x.ID, 80, 24)
		}
	}
	scroll()
	a.Close()
	none("after the end of the connection")
}

func TestAResizeMovesAScrolledConnectionAndItsMarksAndSendsReflow(t *testing.T) {
	ts := newTestServer(t)
	a := ts.interactive()
	_, p, f := scrolledSetup(t, ts, a)
	// The resize moves every line 100 up; the line 505 is gone.
	f.setRemap(func(abs int64) (int64, bool) {
		if abs == 505 {
			return 0, false
		}
		return abs - 100, true
	})
	a.Send(proto.PaneScroll{Pane: p, Top: top(500), Marks: []proto.Pos{{Line: 510, Col: 79}, {Line: 505, Col: 3}}})
	waitTop(t, a, p, top(500))
	testkit.Eventually(t, 5*time.Second, "three followed lines", func() bool { return len(f.followed()) == 3 })

	from := len(a.Received())
	a.Send(proto.ViewResize{Cols: 40, Rows: 24})
	m := a.WaitMessage(proto.TypeEventReflow, from)
	var r proto.Reflow
	m.Decode(&r)
	if r.Pane != p || r.Top == nil || *r.Top != 400 || len(r.Marks) != 2 ||
		r.Marks[0] != (proto.Pos{Line: 410, Col: 39}) || r.Marks[1] != (proto.Pos{Line: -1, Col: 3}) {
		t.Fatalf("event.reflow %+v (top %s); want top 400, marks 410:39 and -1", r, topText(r.Top))
	}
	testkit.Eventually(t, 5*time.Second, "a frame at the new width", func() bool {
		fs, _ := framesOf(t, a, from, p)
		return len(fs) > 0 && fs[len(fs)-1].Cols == 40
	})
	_, rat := messagesOf(a, from, proto.TypeEventReflow)
	fs, fat := framesOf(t, a, from, p)
	for k, fr := range fs {
		if fr.Cols == 40 {
			if fat[k] < rat[0] {
				t.Fatalf("the first frame after the resize (at %d) came before event.reflow (at %d)", fat[k], rat[0])
			}
			if fr.Top == nil || *fr.Top != 400 {
				t.Fatalf("the first frame after the resize has top %s, want 400", topText(fr.Top))
			}
			break
		}
	}
	// The pane follows the moved lines; a further scroll lets go of them.
	if got := sortedKeys(f.followed()); !slices.Equal(got, []int64{400, 410}) {
		t.Fatalf("after the reflow the pane follows %v", got)
	}
	a.Send(proto.PaneScroll{Pane: p})
	testkit.Eventually(t, 5*time.Second, "nothing followed", func() bool { return len(f.followed()) == 0 })
}

func TestAConnectionThatIsNotScrolledGetsNoReflow(t *testing.T) {
	ts := newTestServer(t)
	a, b := ts.interactive(), ts.interactive()
	_, p, f := scrolledSetup(t, ts, a, b)
	f.setRemap(func(abs int64) (int64, bool) { return abs + 7, true })
	a.Send(proto.PaneScroll{Pane: p, Top: top(200)})
	waitTop(t, a, p, top(200))
	fromA, fromB := len(a.Received()), len(b.Received())
	b.Key(p, "x", 0, "x") // b owns the size
	b.Send(proto.ViewResize{Cols: 70, Rows: 20})
	a.WaitMessage(proto.TypeEventReflow, fromA)
	testkit.Eventually(t, 5*time.Second, "b's frame at the new size", func() bool {
		fs, _ := framesOf(t, b, fromB, p)
		return len(fs) > 0 && fs[len(fs)-1].Cols == 70
	})
	ts.sync(b)
	if ms, _ := messagesOf(b, fromB, proto.TypeEventReflow); len(ms) != 0 {
		t.Fatalf("a connection that is not scrolled got %d event.reflow", len(ms))
	}
	waitTop(t, a, p, top(207))
}

func TestALineTheRemapCannotPlaceReturnsTheViewerToLive(t *testing.T) {
	ts := newTestServer(t)
	a := ts.interactive()
	_, p, f := scrolledSetup(t, ts, a)
	f.setRemap(func(abs int64) (int64, bool) {
		if abs == 500 {
			return 0, false
		}
		return abs - 100, true
	})
	a.Send(proto.PaneScroll{Pane: p, Top: top(500), Marks: []proto.Pos{{Line: 510, Col: 1}}})
	waitTop(t, a, p, top(500))
	from := len(a.Received())
	a.Send(proto.ViewResize{Cols: 60, Rows: 24})
	var r proto.Reflow
	a.WaitMessage(proto.TypeEventReflow, from).Decode(&r)
	if r.Top != nil || len(r.Marks) != 1 || r.Marks[0].Line != -1 {
		t.Fatalf("event.reflow %+v (top %s); want live and every mark gone", r, topText(r.Top))
	}
	waitTop(t, a, p, nil)
	testkit.Eventually(t, 5*time.Second, "the lines are let go", func() bool { return len(f.followed()) == 0 })
}

func TestPaneTextReplies(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn) // dormant: nobody displays it
	ts.display(s)
	f := ts.pane(tn.Pane)
	f.mu.Lock()
	f.text = func(from, to vt.Pos, block bool) (string, bool) {
		return fmt.Sprintf("%d:%d-%d:%d block=%t", from.Line, from.Col, to.Line, to.Col, block), false
	}
	f.mu.Unlock()
	var res proto.PaneTextResult
	ok(t, c, proto.PaneText{Pane: tn.Pane, From: proto.Pos{Line: 12000, Col: 3}, To: proto.Pos{Line: 12002, Col: 9}, Block: true}).Msg.Decode(&res)
	if res.Text != "12000:3-12002:9 block=true" || res.Cut {
		t.Fatalf("pane.text answered %+v", res)
	}
	// From an interactive connection too.
	i := ts.interactive()
	ok(t, i, proto.PaneText{Pane: tn.Pane}).Msg.Decode(&res)
	if res.Text != "0:0-0:0 block=false" {
		t.Fatalf("pane.text answered %+v", res)
	}
	// A pane that never ran has no text; a pane that does not exist is no-pane.
	ok(t, c, proto.PaneText{Pane: p}).Msg.Decode(&res)
	if res.Text != "" {
		t.Fatalf("a dormant pane's text is %q", res.Text)
	}
	if r := c.Request(proto.PaneText{Pane: "p999"}); r.OK || r.Code != proto.CodeNoPane {
		t.Fatalf("pane.text of a missing pane: %s %s", r.Msg.T, r.Code)
	}
}
