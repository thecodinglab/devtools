package server

import (
	"bytes"
	"testing"
	"time"

	"devtools/internal/pane"
	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
)

func TestClipboardAndBellGoToTheViewersOfThePane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	other, _, _ := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	a, b, d := ts.interactive(), ts.interactive(), ts.interactive()
	ts.view(a, s, 80, 24)
	ts.view(b, other, 80, 24)
	f := ts.pane(p)
	from := []int{len(a.Received()), len(b.Received()), len(d.Received()), len(c.Received())}

	data := []byte("copied text")
	f.o.Notify(pane.Event{Pane: p, Kind: pane.Clipboard, Clipboard: data})
	f.o.Notify(pane.Event{Pane: p, Kind: pane.Clipboard}) // never empty
	f.o.Notify(pane.Event{Pane: p, Kind: pane.Bell})
	for k, cl := range []*prototest.Client{a, b, d} {
		var e proto.Bell
		cl.WaitMessage(proto.TypeEventBell, from[k]).Decode(&e)
		if e.Pane != p {
			t.Fatalf("event.bell %+v", e)
		}
	}
	m := a.WaitMessage(proto.TypeEventClipboard, from[0])
	var e proto.Clipboard
	m.Decode(&e)
	if e.Pane != p || !bytes.Equal(e.Data, data) {
		t.Fatalf("event.clipboard %+v", e)
	}
	ts.sync(c)
	if ms, _ := messagesOf(a, from[0], proto.TypeEventClipboard); len(ms) != 1 {
		t.Fatalf("the viewer got %d event.clipboard, want 1", len(ms))
	}
	for k, cl := range []*prototest.Client{b, d, c} {
		for _, m := range cl.Received()[from[k+1]:] {
			if m.T == proto.TypeEventClipboard {
				t.Fatalf("connection %d, which does not display the pane, got event.clipboard", k+1)
			}
		}
	}
	for _, m := range c.Received()[from[3]:] {
		if m.T == proto.TypeEventBell {
			t.Fatal("a one-shot got event.bell")
		}
	}
}

func TestASignalledProgramLeavesAnExitedPane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, tab, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	f.exit(0, "SIGKILL")
	st := a.WaitState("the pane is exited", func(st proto.State) bool { return st.Panes[p].State == proto.PaneExited })
	if x := st.Panes[p].Exit; x == nil || x.Signal != "SIGKILL" {
		t.Fatalf("exit %+v", x)
	}
	if tb := st.Tabs[tab]; len(tb.Panes) != 1 || tb.Panes[0] != p {
		t.Fatalf("the exited pane left its tab: %+v", tb)
	}
	// A program that could not be started is exited too, with its reason.
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	g := ts.pane(tn.Pane)
	g.mu.Lock()
	g.info.State = proto.PaneExited
	g.info.Exit = &pane.Exit{Code: 127, Text: "exec /bin/nope: no such file or directory"}
	g.mu.Unlock()
	g.o.Notify(pane.Event{Pane: tn.Pane, Kind: pane.Exited})
	st = a.WaitState("the second pane is exited", func(st proto.State) bool { return st.Panes[tn.Pane].State == proto.PaneExited })
	if x := st.Panes[tn.Pane].Exit; x == nil || x.Text == "" || x.Code != 127 {
		t.Fatalf("exit %+v", x)
	}
	// An exit the server caused changes nothing.
	ok(t, c, proto.TabSelect{Session: s, Tab: tab})
	f2 := ts.pane(p)
	f2.mu.Lock()
	f2.info.Exit = &pane.Exit{Signal: "SIGHUP", ByServer: true}
	f2.mu.Unlock()
	f2.o.Notify(pane.Event{Pane: p, Kind: pane.Exited})
	ts.sync(c)
	if st := ts.stateGet(c); len(st.Panes) != 2 {
		t.Fatalf("an exit the server caused changed the state: %+v", st.Panes)
	}
}

func TestANormalExitClosesThePane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, tab, p := ts.open(c)
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitDown}).Msg.Decode(&sp)
	// The state is watched by a connection that displays nothing: a viewer
	// of the tab gets its panes again from seq 1 when one goes, which
	// prototest does not apply (see TestASplitOfTheDisplayedTabStartsEveryPaneAgain).
	w := ts.interactive()
	ts.display(s)
	ts.pane(sp.Pane).exit(3, "") // any code
	st := w.WaitState("the pane is gone", func(st proto.State) bool { _, ok := st.Panes[sp.Pane]; return !ok })
	if tb := st.Tabs[tab]; len(tb.Panes) != 1 || tb.Panes[0] != p || tb.Split != proto.SplitNone || tb.Focused != p {
		t.Fatalf("after the exit the tab is %+v", tb)
	}
	// The last pane of the last tab: the session ends.
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	from := len(a.Received())
	ts.pane(p).exit(0, "")
	a.WaitMessage(proto.TypeViewEnded, from)
	if st := ts.stateGet(c); len(st.Sessions) != 0 {
		t.Fatalf("the session outlived its last pane: %+v", st.Sessions)
	}
}

func TestEnterRespawnsAnExitedPane(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	f.write(0, "first run")
	a.WaitFor(p, "first run")
	f.exit(0, "SIGTERM")
	a.WaitState("the pane is exited", func(st proto.State) bool { return st.Panes[p].State == proto.PaneExited })

	a.Key(p, "x", 0, "x")    // another key does not start it
	a.Key(p, "enter", 4, "") // nor ctrl+enter
	ts.sync(a)
	if n := ts.panes.started(); n != 1 {
		t.Fatalf("%d pane objects after keys other than Enter", n)
	}
	a.Key(p, "enter", 0, "")
	st := a.WaitState("the pane runs again", func(st proto.State) bool { return st.Panes[p].State == proto.PaneRunning })
	if st.Panes[p].Exit != nil {
		t.Fatalf("a running pane keeps its exit %+v", st.Panes[p].Exit)
	}
	g := ts.pane(p)
	testkit.Eventually(t, 5*time.Second, "the new pane object", func() bool { g = ts.panes.get(p); return g != f })
	if g.o.ID != p || g.o.Dir != f.o.Dir || g.o.Cols != 80 || g.o.Rows != 24 {
		t.Fatalf("the respawned pane: id %s dir %s %dx%d", g.o.ID, g.o.Dir, g.o.Cols, g.o.Rows)
	}
	testkit.Eventually(t, 5*time.Second, "the old object is closed", f.isClosed)
	// The viewer's frames go on from the new object, in sequence.
	g.write(0, "second run")
	a.WaitFor(p, "second run")
	if g.hasInput("key enter") || f.hasInput("key enter") {
		t.Fatal("the Enter that respawned the pane reached a program")
	}
}
