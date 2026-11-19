package prototest

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/testkit/golden"
)

func TestMain(m *testing.M) {
	// Hermetic: no variable of the developer's session reaches a test.
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "HERDR_") || (strings.HasPrefix(name, "DEVTOOLS_") && name != "DEVTOOLS_TEST_NO_PTY") ||
			name == "TMUX" || name == "TMUX_PANE" {
			os.Unsetenv(name)
		}
	}
	Timeout = 5 * time.Second
	os.Exit(m.Run())
}

// connect dials s with hello while the test's script accepts the
// connection and welcomes it with snap.
func connect(t *testing.T, tb testing.TB, s *Server, hello proto.Hello, snap proto.Snapshot) (*Client, *Peer) {
	t.Helper()
	peers := make(chan *Peer, 1)
	go func() {
		p := s.Accept()
		p.Welcome(snap)
		peers <- p
	}()
	c := dial(tb, s.Socket(), hello)
	return c, <-peers
}

func frame(seq uint64, cols, rows int, full, more bool, lines ...proto.Line) proto.Frame {
	return proto.Frame{
		Pane: "p1", Seq: seq, Cols: cols, Rows: rows, Full: full, More: more, Lines: lines,
		Cursor: proto.Cursor{X: int(seq), Y: 0, Visible: true, Shape: "block"},
	}
}

func TestPrototestClientAppliesFramesInOrder(t *testing.T) {
	s := NewServer(t)
	snap := proto.Snapshot{Rev: 1, Sessions: []proto.Session{{ID: "s1", Dir: "/x", Label: "x", Tabs: []string{"t1"}, Current: "t1"}}}
	c, p := connect(t, t, s, Interactive(), snap)
	if p.Hello.Kind != proto.KindInteractive || p.Hello.Gen != proto.Gen || p.Hello.Colors == nil {
		t.Fatalf("hello %+v", p.Hello)
	}
	if c.Welcome().Host != "prototest" || c.State().Rev != 1 {
		t.Fatalf("welcome %+v, state %+v", c.Welcome(), c.State())
	}

	// A view: the script answers the request with view.ok.
	go func() {
		var v proto.ViewSet
		m := p.Expect(proto.TypeViewSet, &v)
		p.Send(proto.Request(m.ID, proto.ViewOK{Session: v.Session, Dir: "/x", Tab: "t1", Panes: []string{"p1"}}))
	}()
	ok := c.View(proto.ViewSet{Session: "s1", Cols: 80, Rows: 24})
	if ok.Tab != "t1" || len(ok.Panes) != 1 {
		t.Fatalf("view.ok %+v", ok)
	}

	p.Send(frame(1, 10, 3, true, false, proto.Line{Y: 0, S: "a"}, proto.Line{Y: 1, S: "\x1b[0;1mb"}, proto.Line{Y: 2, S: "c"}))
	p.Send(frame(2, 10, 3, false, false, proto.Line{Y: 1, S: "B"}))
	p.Send(proto.Delta{Rev: 2, Changes: []proto.Change{proto.Put(proto.Pane{ID: "p1", Tab: "t1", Session: "s1", State: proto.PaneRunning})}})
	// Frame 3 in two pieces, and at another size, so it is full.
	p.Send(frame(3, 12, 4, true, true, proto.Line{Y: 0, S: "A"}, proto.Line{Y: 1, S: "B"}))
	p.Send(frame(3, 12, 4, true, false, proto.Line{Y: 2, S: "C"}, proto.Line{Y: 3, S: "D"}))
	p.Send(frame(4, 12, 4, false, false, proto.Line{Y: 0, S: "AA"}))

	got := c.WaitScreen("p1", "frame 4", func(s golden.Screen) bool { return s.Row(0) == "AA" })
	want := "size 12x4 cursor 4,0 visible\nAA|\nB|\nC|\nD|\n"
	if got.Plain() != want {
		t.Fatalf("screen\n%s\nwant\n%s", got.Plain(), want)
	}
	if c.Frames("p1") != 4 {
		t.Fatalf("%d frames applied, want 4 (pieces count as one)", c.Frames("p1"))
	}
	st := c.WaitState("pane p1 running", func(s proto.State) bool { return s.Panes["p1"].State == proto.PaneRunning })
	if st.Rev != 2 {
		t.Fatalf("state at revision %d", st.Rev)
	}
	wantTypes := "welcome state.snapshot view.ok frame frame state.delta frame frame frame"
	if got := strings.Join(c.Types(), " "); got != wantTypes {
		t.Fatalf("received %s\nwant     %s", got, wantTypes)
	}

	// After frame 2 and before the pieces the screen was the one of frame 2:
	// a second client sees the pieces only after the last.
	c2, p2 := connect(t, t, s, Interactive(), snap)
	p2.Send(frame(1, 10, 2, true, false, proto.Line{Y: 0, S: "old"}))
	c2.WaitFor("p1", "old")
	p2.Send(frame(2, 10, 2, true, true, proto.Line{Y: 0, S: "new"}))
	p2.Send(proto.Bell{Pane: "p1"})
	if m := c2.WaitMessage(proto.TypeEventBell, 0); m.T != proto.TypeEventBell {
		t.Fatalf("WaitMessage returned %s", m.T)
	}
	if row := c2.Screen("p1").Row(0); row != "old" {
		t.Fatalf("a piece with more was presented: row 0 is %q", row)
	}

	// Requests, input and a reply with err.
	script := make(chan struct{})
	go func() {
		defer close(script)
		m := p.Expect(proto.TypeTabClose, nil)
		p.Fail(m.ID, proto.CodeNoTab, "no tab t9")
		var k proto.Key
		p.Expect(proto.TypeInputKey, &k)
		p.Expect(proto.TypeInputKey, nil)
		p.Expect(proto.TypeInputKey, nil)
	}()
	r := c.Request(proto.TabClose{Tab: "t9"})
	if r.OK || r.Code != proto.CodeNoTab {
		t.Fatalf("reply %+v", r)
	}
	c.Type("p1", "aB\r")
	select {
	case <-script:
	case <-time.After(Timeout):
		t.Fatal("the script did not receive the three keys")
	}
	if k := KeyOf("p1", 'B'); k.Key != "b" || k.Mod != proto.ModShift || k.Text != "B" {
		t.Fatalf("key of B: %+v", k)
	}
	if k := KeyOf("p1", '\x03'); k.Key != "c" || k.Mod != proto.ModCtrl {
		t.Fatalf("key of ctrl+c: %+v", k)
	}
	for _, r := range "a /ä\r\t\x1b\x7f\x01" {
		if k := KeyOf("p1", r); !proto.ValidKey(k.Key) {
			t.Fatalf("Type sends the key %q for %q, which is not a key of 4.2.6", k.Key, r)
		}
	}
}

// recordingT stands in for the test where the client must fail it.
type recordingT struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recordingT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recordingT) errors() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.errs...)
}

func TestPrototestClientFailsOnASequenceGap(t *testing.T) {
	for _, c := range []struct {
		name   string
		frames []any
		want   string
	}{
		{"a gap", []any{frame(1, 4, 2, true, false), frame(3, 4, 2, false, false)}, "frame seq 3 after 1"},
		// W5: a full frame is no licence to start again at 1 while the pane
		// stays displayed.
		{"a restart without a change of the view", []any{frame(1, 4, 2, true, false), frame(2, 4, 2, true, false), frame(1, 4, 2, true, false)}, "frame seq 1 after 2"},
		{"a first frame that is not 1", []any{frame(2, 4, 2, true, false)}, "seq 2, not 1"},
		{"a first frame that is not full", []any{frame(1, 4, 2, false, false)}, "frame 1 is not full"},
		{"a resize that is not full", []any{frame(1, 4, 2, true, false), frame(2, 5, 2, false, false)}, "not full"},
		{"a revision gap", []any{proto.Delta{Rev: 3}}, "revision gap"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewServer(t)
			rec := &recordingT{TB: t}
			cl, p := connect(t, rec, s, Interactive(), proto.Snapshot{Rev: 1})
			for _, f := range c.frames {
				p.Send(f)
			}
			deadline := time.Now().Add(Timeout)
			for !cl.Closed() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !cl.Closed() {
				t.Fatal("the client did not close the connection")
			}
			errs := rec.errors()
			if len(errs) != 1 || !strings.Contains(errs[0], c.want) {
				t.Fatalf("the client reported %q, want one error with %q", errs, c.want)
			}
			if cl.Err() == nil {
				t.Fatal("Err is nil")
			}
			p.Ended()
		})
	}
}

func TestPrototestStopReadingStallsTheConnection(t *testing.T) {
	s := NewServer(t)
	c, p := connect(t, t, s, Interactive(), proto.Snapshot{Rev: 1})
	row := strings.Repeat("x", 60<<10)
	write := func(seq uint64) error {
		f := frame(seq, 80, 2, true, false, proto.Line{Y: 0, S: row})
		p.Conn().SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		return proto.WriteMsg(p.Conn(), proto.Encode(f))
	}
	// A reading client takes 8 MiB without a stall.
	seq := uint64(0)
	for range 8 << 20 / len(row) {
		seq++
		if err := write(seq); err != nil {
			t.Fatalf("frame %d to a reading client: %v", seq, err)
		}
	}
	last := seq
	c.WaitScreen("p1", "the last frame", func(golden.Screen) bool { return c.Frames("p1") == int(last) })

	c.StopReading()
	var err error
	written := 0
	for written < 64<<20 {
		seq++
		if err = write(seq); err != nil {
			break
		}
		written += len(row)
	}
	var ne net.Error
	if err == nil || !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("after StopReading %d bytes were written and the write ended with %v, want a timeout", written, err)
	}
	if c.Closed() {
		t.Fatal("a stalled client closed the connection")
	}
}

// TestPrototestWaitClosedSeesThePeerHangUp is run with -race (CHECK): the
// wait asks its condition while the reader marks the connection closed.
func TestPrototestWaitClosedSeesThePeerHangUp(t *testing.T) {
	s := NewServer(t)
	c, p := connect(t, t, s, Interactive(), proto.Snapshot{Rev: 1})
	hungUp := make(chan struct{})
	go func() {
		defer close(hungUp)
		time.Sleep(time.Millisecond)
		p.Close()
	}()
	c.WaitClosed()
	<-hungUp
	if !c.Closed() {
		t.Fatal("WaitClosed returned on an open connection")
	}
	if err := c.Err(); err != nil {
		t.Fatalf("an ordinary hang-up is reported as a protocol error: %v", err)
	}
}

// Steps of TestPrototestClientRestartsASequenceOnlyWhenThePaneIsDisplayedAgain
// besides the messages the peer sends as they are.
type (
	viewStep    proto.ViewOK // the client sets a view, the peer answers with view.ok
	refusedView struct{}     // the client sets a view, the peer answers with err
)

func paneFrame(pane string, seq uint64, full bool, row string) proto.Frame {
	return proto.Frame{
		Pane: pane, Seq: seq, Cols: 4, Rows: 2, Full: full, Lines: []proto.Line{{Y: 0, S: row}},
		Cursor: proto.Cursor{Visible: true, Shape: "block"},
	}
}

func TestPrototestClientRestartsASequenceOnlyWhenThePaneIsDisplayedAgain(t *testing.T) {
	session := func(id, current string, tabs ...string) proto.Session {
		return proto.Session{ID: id, Dir: "/" + id, Label: id, Tabs: tabs, Current: current}
	}
	tab := func(id, session string, index int, panes ...string) proto.Tab {
		return proto.Tab{ID: id, Session: session, Index: index, Panes: panes, Split: proto.SplitRight, Focused: panes[0]}
	}
	snap := proto.Snapshot{
		Rev:      1,
		Sessions: []proto.Session{session("s1", "t1", "t1", "t2"), session("s2", "t3", "t3", "t4")},
		Tabs: []proto.Tab{
			tab("t1", "s1", 1, "p1", "p4"), tab("t2", "s1", 2, "p2"),
			tab("t3", "s2", 1, "p3"), tab("t4", "s2", 2, "p5"),
		},
	}
	delta := func(rev uint64, v any) proto.Delta {
		return proto.Delta{Rev: rev, Changes: []proto.Change{proto.Put(v)}}
	}
	view := viewStep{Session: "s1", Dir: "/s1", Tab: "t1", Panes: []string{"p1", "p4"}}
	shown := []any{view, paneFrame("p1", 1, true, "a"), paneFrame("p1", 2, false, "b")}
	steps := func(more ...any) []any { return append(append([]any(nil), shown...), more...) }

	for _, c := range []struct {
		name  string
		steps []any
		want  string // the error; "" when every frame is legal
		row   string // row 0 of p1 at the end of a legal run
	}{
		{"after the view.ok of a new view", steps(
			view, paneFrame("p1", 1, true, "c"), paneFrame("p1", 2, false, "d"),
		), "", "d"},
		{"after a change of the current tab and back", steps(
			delta(2, session("s1", "t2", "t1", "t2")), paneFrame("p2", 1, true, "x"),
			delta(3, session("s1", "t1", "t1", "t2")), paneFrame("p1", 1, true, "c"), paneFrame("p1", 2, false, "d"),
		), "", "d"},
		{"after view.ended and a new view", steps(
			proto.ViewEnded{Session: "s1", Dir: "/s1"}, view, paneFrame("p1", 1, true, "c"),
		), "", "c"},
		{"after the pane left its tab and came back, for every pane of the tab", steps(
			paneFrame("p4", 1, true, "q"),
			delta(2, tab("t1", "s1", 1, "p4")), delta(3, tab("t1", "s1", 1, "p1", "p4")),
			paneFrame("p1", 1, true, "c"), paneFrame("p4", 1, true, "r"), paneFrame("p1", 2, false, "d"),
		), "", "d"},
		{"of the pane that stayed when the other left", steps(
			paneFrame("p4", 1, true, "q"), delta(2, tab("t1", "s1", 1, "p1")), paneFrame("p1", 1, true, "c"),
		), "", "c"},

		{"a restart after a delta that changes nothing on the screen", steps(
			delta(2, proto.Pane{ID: "p1", Tab: "t1", Session: "s1", State: proto.PaneRunning}), paneFrame("p1", 1, true, "c"),
		), "frame seq 1 after 2", ""},
		{"a restart after a tab change of another session", steps(
			delta(2, session("s2", "t4", "t3", "t4")), paneFrame("p1", 1, true, "c"),
		), "frame seq 1 after 2", ""},
		{"a restart after a refused view.set", steps(
			refusedView{}, paneFrame("p1", 1, true, "c"),
		), "frame seq 1 after 2", ""},
		{"a sequence that goes on for the pane that stayed when the other left", steps(
			paneFrame("p4", 1, true, "q"), delta(2, tab("t1", "s1", 1, "p1")), paneFrame("p1", 3, false, "c"),
		), "seq 3, not 1", ""},
		{"a sequence that goes on after a change of the current tab", steps(
			delta(2, session("s1", "t2", "t1", "t2")), delta(3, session("s1", "t1", "t1", "t2")), paneFrame("p1", 3, false, "c"),
		), "seq 3, not 1", ""},
		{"a frame 1 that is not full after a new view", steps(
			view, paneFrame("p1", 1, false, "c"),
		), "frame 1 is not full", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewServer(t)
			rec := &recordingT{TB: t}
			cl, p := connect(t, rec, s, Interactive(), snap)
			for _, st := range c.steps {
				switch v := st.(type) {
				case viewStep:
					go func() {
						var set proto.ViewSet
						m := p.Expect(proto.TypeViewSet, &set)
						p.Send(proto.Request(m.ID, proto.ViewOK(v)))
					}()
					cl.View(proto.ViewSet{Session: v.Session, Cols: 80, Rows: 24})
				case refusedView:
					go func() {
						m := p.Expect(proto.TypeViewSet, nil)
						p.Fail(m.ID, proto.CodeNoSession, "no session s9")
					}()
					if r := cl.Request(proto.ViewSet{Session: "s9", Cols: 80, Rows: 24}); r.OK {
						t.Fatalf("view.set answered %+v", r)
					}
				default:
					p.Send(v)
				}
			}
			if c.want == "" {
				p.Send(proto.Bell{Pane: "p1"})
				cl.WaitMessage(proto.TypeEventBell, 0)
				if errs := rec.errors(); len(errs) != 0 || cl.Closed() {
					t.Fatalf("a legal redisplay was refused: %q", errs)
				}
				if row := cl.Screen("p1").Row(0); row != c.row {
					t.Fatalf("row 0 of p1 is %q, want %q", row, c.row)
				}
				return
			}
			deadline := time.Now().Add(Timeout)
			for !cl.Closed() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			errs := rec.errors()
			if !cl.Closed() || len(errs) != 1 || !strings.Contains(errs[0], c.want) {
				t.Fatalf("closed %v, the client reported %q, want one error with %q", cl.Closed(), errs, c.want)
			}
			p.Ended()
		})
	}
}
