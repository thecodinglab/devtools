package lane

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"devtools/internal/pane"
	"devtools/internal/proto"
	"devtools/internal/vt"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a socket or a state directory reaches them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-lane-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{proto.EnvSocket, proto.EnvPane, proto.EnvTTY, "XDG_STATE_HOME", "DEVTOOLS_CONFIG", "DEVTOOLS_MULTIPLEXER"} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// fakeSource is a pane as the lane sees it. Snapshot returns the current
// screen; while scrolled it returns rows named after their absolute line.
type fakeSource struct {
	mu    sync.Mutex
	snap  pane.Snap
	calls []call
	block chan struct{} // when not nil, Snapshot waits for it to be closed
	enter chan struct{} // when not nil, Snapshot signals it before blocking
}

type call struct {
	top      int64
	scrolled bool
}

func newSource(cols int, lines ...string) *fakeSource {
	return &fakeSource{snap: pane.Snap{
		Cols: cols, Rows: len(lines),
		Lines:  append([]string(nil), lines...),
		Cursor: vt.Cursor{X: 0, Y: 0, Visible: true, Shape: "block"},
		First:  0, Base: 1000,
	}}
}

func (f *fakeSource) set(change func(*pane.Snap)) {
	f.mu.Lock()
	change(&f.snap)
	f.mu.Unlock()
}

func (f *fakeSource) Snapshot(top int64, scrolled bool) pane.Snap {
	f.mu.Lock()
	block, enter := f.block, f.enter
	f.calls = append(f.calls, call{top, scrolled})
	f.mu.Unlock()
	if enter != nil {
		select {
		case enter <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.snap
	s.Lines = append([]string(nil), f.snap.Lines...)
	if scrolled {
		s.Scrolled, s.Top = true, top
		for y := range s.Lines {
			s.Lines[y] = fmt.Sprintf("line %d", top+int64(y))
		}
	}
	return s
}

func (f *fakeSource) lastCall() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// peer is the client end of a connection: it reads every message the lane
// writes, once started.
type peer struct {
	conn net.Conn
	mu   sync.Mutex
	msgs []got
	done chan struct{}
}

type got struct {
	msg proto.Msg
	at  time.Time
}

func (p *peer) start() {
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		for {
			m, err := proto.ReadMsg(p.conn)
			if err != nil {
				return
			}
			p.mu.Lock()
			p.msgs = append(p.msgs, got{m, time.Now()})
			p.mu.Unlock()
		}
	}()
}

func (p *peer) all() []got {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]got(nil), p.msgs...)
}

func (p *peer) frames(t *testing.T) []proto.Frame {
	t.Helper()
	var out []proto.Frame
	for _, g := range p.all() {
		if g.msg.T == proto.TypeFrame {
			out = append(out, decodeFrame(t, g.msg))
		}
	}
	return out
}

func decodeFrame(t *testing.T, m proto.Msg) proto.Frame {
	t.Helper()
	var f proto.Frame
	if err := m.Decode(&f); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	return f
}

// fixture is a lane running on one end of a net.Pipe and a peer on the
// other. Inside a synctest bubble everything about time is the fake clock.
type fixture struct {
	l    *Lane
	p    *peer
	errc chan error
}

func run(t *testing.T, o Options, reading bool) *fixture {
	t.Helper()
	a, b := net.Pipe()
	fx := &fixture{l: New(o), p: &peer{conn: b}, errc: make(chan error, 1)}
	go func() { fx.errc <- fx.l.Run(a) }()
	if reading {
		fx.p.start()
	}
	t.Cleanup(func() {
		fx.l.Close(0)
		a.Close()
		b.Close()
		<-fx.errc
		if fx.p.done != nil {
			<-fx.p.done
		}
	})
	return fx
}

func ok(id uint64) proto.Msg { return proto.OKReply(id, nil) }

func show(t *testing.T, l *Lane, m *proto.Msg, panes map[string]Source) {
	t.Helper()
	if !l.Show(m, panes) {
		t.Fatal("Show returned false")
	}
}

func lineMap(f proto.Frame) map[int]string {
	m := map[int]string{}
	for _, ln := range f.Lines {
		m[ln.Y] = ln.S
	}
	return m
}

func TestControlMessagesKeepTheirOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "a", "b")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		for i := 1; i <= 500; i++ {
			if !fx.l.Send(ok(uint64(i))) {
				t.Fatalf("Send %d returned false", i)
			}
			if i%50 == 0 {
				src.set(func(s *pane.Snap) { s.Lines[0] = fmt.Sprint(i) })
				fx.l.Dirty("p1")
				time.Sleep(time.Millisecond)
			}
		}
		time.Sleep(time.Second)
		synctest.Wait()
		var ids []uint64
		frames := 0
		for _, g := range fx.p.all() {
			switch g.msg.T {
			case proto.TypeOK:
				ids = append(ids, g.msg.ID)
			case proto.TypeFrame:
				frames++
			}
		}
		if len(ids) != 500 {
			t.Fatalf("got %d control messages, want 500", len(ids))
		}
		for i, id := range ids {
			if id != uint64(i+1) {
				t.Fatalf("control message %d has id %d, want %d", i, id, i+1)
			}
		}
		if frames < 2 {
			t.Fatalf("got %d frames between the control messages, want several", frames)
		}
	})
}

func TestControlIsWrittenBeforePendingFrames(t *testing.T) {
	t.Run("queued while a frame is being written", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fx := run(t, Options{}, false)
			src := newSource(10, "one")
			show(t, fx.l, nil, map[string]Source{"p1": src})
			synctest.Wait() // Run is blocked writing frame 1: nobody reads
			src.set(func(s *pane.Snap) { s.Lines[0] = "two" })
			fx.l.Dirty("p1")
			fx.l.Send(ok(7))
			fx.p.start()
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
			var order []string
			for _, g := range fx.p.all() {
				if g.msg.T == proto.TypeFrame {
					f := decodeFrame(t, g.msg)
					order = append(order, fmt.Sprintf("frame %d %s", f.Seq, lineMap(f)[0]))
				} else {
					order = append(order, g.msg.T)
				}
			}
			want := []string{"frame 1 one", "ok", "frame 2 two"}
			if strings.Join(order, ", ") != strings.Join(want, ", ") {
				t.Fatalf("order %q, want %q", order, want)
			}
		})
	})
	t.Run("queued while a frame is being built", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fx := run(t, Options{}, true)
			src := newSource(10, "one")
			src.block = make(chan struct{})
			show(t, fx.l, nil, map[string]Source{"p1": src})
			synctest.Wait() // Run is inside Snapshot
			fx.l.Send(ok(7))
			close(src.block)
			synctest.Wait()
			all := fx.p.all()
			if len(all) != 2 || all[0].msg.T != proto.TypeOK || all[1].msg.T != proto.TypeFrame {
				var ts []string
				for _, g := range all {
					ts = append(ts, g.msg.T)
				}
				t.Fatalf("messages %q, want ok then frame", ts)
			}
		})
	})
}

func TestFirstFrameAfterShowIsFullWithSeqOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "a", "", "c")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()
		src.set(func(s *pane.Snap) { s.Lines[1] = "b" })
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		m := ok(1)
		show(t, fx.l, &m, map[string]Source{"p1": src})
		synctest.Wait()

		all := fx.p.all()
		if len(all) != 4 {
			t.Fatalf("got %d messages, want 4", len(all))
		}
		first := decodeFrame(t, all[0].msg)
		if first.Seq != 1 || !first.Full || first.Pane != "p1" || first.Cols != 10 || first.Rows != 3 {
			t.Fatalf("first frame %+v, want seq 1, full, p1 10x3", first)
		}
		if len(first.Lines) != 3 || first.Lines[0] != (proto.Line{Y: 0, S: "a"}) ||
			first.Lines[1] != (proto.Line{Y: 1, S: ""}) || first.Lines[2] != (proto.Line{Y: 2, S: "c"}) {
			t.Fatalf("first frame lines %+v, want every row including the blank one", first.Lines)
		}
		second := decodeFrame(t, all[1].msg)
		if second.Seq != 2 || second.Full {
			t.Fatalf("second frame seq %d full %v, want 2 and partial", second.Seq, second.Full)
		}
		if all[2].msg.T != proto.TypeOK {
			t.Fatalf("third message %s, want the message of Show", all[2].msg.T)
		}
		again := decodeFrame(t, all[3].msg)
		if again.Seq != 1 || !again.Full || len(again.Lines) != 3 {
			t.Fatalf("frame after the second Show %+v, want seq 1 and full", again)
		}
	})
}

func TestOnlyChangedRowsAreSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(20, "r0", "r1", "r2", "r3", "r4")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()
		src.set(func(s *pane.Snap) {
			s.Lines[1] = "\x1b[0;1mbold\x1b[0m"
			s.Lines[3] = ""
		})
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		fs := fx.p.frames(t)
		if len(fs) != 2 {
			t.Fatalf("got %d frames, want 2", len(fs))
		}
		f := fs[1]
		want := []proto.Line{{Y: 1, S: "\x1b[0;1mbold\x1b[0m"}, {Y: 3, S: ""}}
		if f.Full || len(f.Lines) != 2 || f.Lines[0] != want[0] || f.Lines[1] != want[1] {
			t.Fatalf("frame full %v lines %+v, want partial %+v", f.Full, f.Lines, want)
		}
	})
}

func TestNothingIsSentWhenNothingChanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "same")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		for range 20 {
			fx.l.Dirty("p1")
			time.Sleep(10 * time.Millisecond)
		}
		synctest.Wait()
		if all := fx.p.all(); len(all) != 1 {
			t.Fatalf("got %d messages, want only the first frame", len(all))
		}
		// A change of a mode that is not on the wire is no change either.
		src.set(func(s *pane.Snap) { s.Modes.MouseSGR = true; s.Modes.Paste = true })
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if all := fx.p.all(); len(all) != 1 {
			t.Fatalf("got %d messages after a change of a mode the wire does not carry, want 1", len(all))
		}
	})
}

func TestCursorOrModeChangeAloneIsAFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "x", "y")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()

		src.set(func(s *pane.Snap) { s.Cursor.X, s.Cursor.Y = 3, 1 })
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		src.set(func(s *pane.Snap) { s.Modes.Alt = true; s.Modes.Mouse = "drag"; s.Modes.Kitty = 3 })
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()

		fs := fx.p.frames(t)
		if len(fs) != 3 {
			t.Fatalf("got %d frames, want 3", len(fs))
		}
		c := fs[1]
		if c.Full || len(c.Lines) != 0 || c.Cursor.X != 3 || c.Cursor.Y != 1 || !c.Cursor.Visible {
			t.Fatalf("cursor frame %+v, want no rows and the cursor at 3,1", c)
		}
		m := fs[2]
		if m.Full || len(m.Lines) != 0 || m.Modes != (proto.Modes{Alt: true, Mouse: "drag", Keys: 3}) {
			t.Fatalf("mode frame %+v, want no rows and the new modes", m)
		}
		// The empty list is on the wire as [], not null.
		for _, g := range fx.p.all()[1:] {
			if !strings.Contains(string(g.msg.Body), `"lines":[]`) {
				t.Fatalf("frame body %s has no empty lines list", g.msg.Body)
			}
		}
	})
}

func TestAtMostOneFramePerPanePerInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 16 * time.Millisecond
		fx := run(t, Options{FrameInterval: interval}, true)
		src := newSource(10, "0")
		start := time.Now()
		show(t, fx.l, nil, map[string]Source{"p1": src})
		for i := 1; i <= 200; i++ {
			src.set(func(s *pane.Snap) { s.Lines[0] = fmt.Sprint(i) })
			fx.l.Dirty("p1")
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()

		all := fx.p.all()
		if !all[0].at.Equal(start) {
			t.Fatalf("first frame at %v, want at once", all[0].at.Sub(start))
		}
		for i := 1; i < len(all); i++ {
			if gap := all[i].at.Sub(all[i-1].at); gap < interval {
				t.Fatalf("frames %d and %d are %v apart, less than %v", i-1, i, gap, interval)
			}
		}
		if n := len(all); n > 200/16+2 {
			t.Fatalf("got %d frames in 200 ms, want at most %d", n, 200/16+2)
		}
		last := decodeFrame(t, all[len(all)-1].msg)
		if lineMap(last)[0] != "200" {
			t.Fatalf("last frame shows %q, want the newest screen 200", lineMap(last)[0])
		}

		// A pane whose last frame is older than the interval gets its next
		// frame at once: a typed character waits for no timer.
		before := len(all)
		at := time.Now()
		src.set(func(s *pane.Snap) { s.Lines[0] = "typed" })
		fx.l.Dirty("p1")
		synctest.Wait()
		all = fx.p.all()
		if len(all) != before+1 || !all[len(all)-1].at.Equal(at) {
			t.Fatalf("after an idle interval the frame came at %v, want at once", all[len(all)-1].at.Sub(at))
		}
	})
}

func TestASlowReaderGetsTheNewestScreenNotABacklog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, false)
		src := newSource(10, "start")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait() // frame 1 is stuck in the pipe
		for i := 1; i <= 1000; i++ {
			src.set(func(s *pane.Snap) { s.Lines[0] = fmt.Sprint(i) })
			fx.l.Dirty("p1")
			time.Sleep(time.Millisecond)
		}
		fx.p.start()
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		fs := fx.p.frames(t)
		if len(fs) != 2 {
			t.Fatalf("got %d frames, want the stuck one and the newest", len(fs))
		}
		if got := lineMap(fs[1])[0]; got != "1000" || fs[1].Seq != 2 {
			t.Fatalf("second frame seq %d shows %q, want seq 2 with the newest screen 1000", fs[1].Seq, got)
		}
	})
}

func TestFrameIsSplitAtOneMiBWithMore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		const rows, width = 100, 40000
		lines := make([]string, rows)
		for y := range lines {
			lines[y] = strings.Repeat(string(rune('a'+y%26)), width)
		}
		src := newSource(width, lines...)
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()
		all := fx.p.all()
		if len(all) < 4 {
			t.Fatalf("got %d pieces for %d bytes of rows, want at least 4", len(all), rows*width)
		}
		next := 0
		for i, g := range all {
			if len(g.msg.Body) > proto.FramePiece+width+pieceBase {
				t.Fatalf("piece %d has %d bytes, above the 1 MiB mark plus one row", i, len(g.msg.Body))
			}
			f := decodeFrame(t, g.msg)
			if f.Seq != 1 || !f.Full || f.Rows != rows {
				t.Fatalf("piece %d: seq %d full %v rows %d, want seq 1, full, %d rows", i, f.Seq, f.Full, f.Rows, rows)
			}
			if f.More != (i < len(all)-1) {
				t.Fatalf("piece %d of %d has more %v", i, len(all), f.More)
			}
			for _, ln := range f.Lines {
				if ln.Y != next || ln.S != lines[next] {
					t.Fatalf("piece %d has row %d, want row %d", i, ln.Y, next)
				}
				next++
			}
		}
		if next != rows {
			t.Fatalf("the pieces hold %d rows, want %d", next, rows)
		}
	})
}

func TestSizeChangeForcesAFullFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "a", "b", "c")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()
		src.set(func(s *pane.Snap) { s.Rows = 4; s.Lines = append(s.Lines, "d") })
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		src.set(func(s *pane.Snap) { s.Cols = 12 })
		fx.l.Dirty("p1")
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		fs := fx.p.frames(t)
		if len(fs) != 3 {
			t.Fatalf("got %d frames, want 3", len(fs))
		}
		if f := fs[1]; !f.Full || f.Seq != 2 || f.Rows != 4 || len(f.Lines) != 4 {
			t.Fatalf("frame after a row change %+v, want full with 4 rows", f)
		}
		if f := fs[2]; !f.Full || f.Seq != 3 || f.Cols != 12 || len(f.Lines) != 4 {
			t.Fatalf("frame after a column change %+v, want full with 4 rows", f)
		}
	})
}

// TestShowQueuesItsMessageAndResetsSlotsInOneStep races Show against a lane
// that builds frames as fast as it can. After the message of view i the peer
// must see frames of view i's panes only, each pane starting at seq 1, full,
// and counting up by one (W4, W5, W8).
func TestShowQueuesItsMessageAndResetsSlotsInOneStep(t *testing.T) {
	const views = 1000
	a, b := net.Pipe()
	l := New(Options{FrameInterval: time.Microsecond})
	errc := make(chan error, 1)
	go func() { errc <- l.Run(a) }()

	type check struct {
		err    error
		frames int
	}
	checked := make(chan check, 1)
	go func() {
		var c check
		view := uint64(0)
		seq := map[string]uint64{}
		for {
			m, err := proto.ReadMsg(b)
			if err != nil {
				checked <- c
				return
			}
			if c.err != nil {
				continue
			}
			switch m.T {
			case proto.TypeViewOK:
				if m.ID != view+1 {
					c.err = fmt.Errorf("view %d follows view %d", m.ID, view)
				}
				view = m.ID
				seq = map[string]uint64{}
			case proto.TypeFrame:
				var f proto.Frame
				if err := m.Decode(&f); err != nil {
					c.err = err
					continue
				}
				c.frames++
				if f.Pane != "shared" && f.Pane != fmt.Sprintf("v%d", view) {
					c.err = fmt.Errorf("frame of pane %s after the message of view %d", f.Pane, view)
					continue
				}
				if f.Seq != seq[f.Pane]+1 {
					c.err = fmt.Errorf("view %d pane %s: seq %d after %d", view, f.Pane, f.Seq, seq[f.Pane])
				}
				if f.Seq == 1 && !f.Full {
					c.err = fmt.Errorf("view %d pane %s: first frame is not full", view, f.Pane)
				}
				seq[f.Pane] = f.Seq
			}
		}
	}()

	var current atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			l.Dirty("shared")
			l.Dirty(fmt.Sprintf("v%d", current.Load()))
			l.Dirty(fmt.Sprintf("v%d", current.Load()-1))
		}
	}()
	shared := &counting{}
	for i := uint64(1); i <= views; i++ {
		m := proto.Request(i, proto.ViewOK{})
		current.Store(i)
		if !l.Show(&m, map[string]Source{"shared": shared, fmt.Sprintf("v%d", i): &counting{}}) {
			t.Fatalf("Show %d returned false", i)
		}
		// Vary the moment of the next Show, so that it lands before, during
		// and after the building and the writing of frames.
		time.Sleep(time.Duration(i%5) * 20 * time.Microsecond)
	}
	close(stop)
	wg.Wait()
	l.Close(time.Second)
	a.Close()
	if err := <-errc; err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := <-checked
	b.Close()
	if c.err != nil {
		t.Fatal(c.err)
	}
	t.Logf("%d frames during %d views", c.frames, views)
	if c.frames < views/10 {
		t.Fatalf("only %d frames were sent during %d views; the race was not exercised", c.frames, views)
	}
}

// counting is a source whose screen changes on every Snapshot.
type counting struct{ n atomic.Int64 }

func (c *counting) Snapshot(int64, bool) pane.Snap {
	return pane.Snap{Cols: 5, Rows: 2, Lines: []string{fmt.Sprint(c.n.Add(1)), ""}}
}

func TestScrolledViewSendsRowsFromTopAndHidesTheCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "live0", "live1")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()
		fx.l.Scroll("p1", 100, true)
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if c := src.lastCall(); c != (call{100, true}) {
			t.Fatalf("Snapshot called with %+v, want top 100 scrolled", c)
		}
		fx.l.Scroll("p1", 0, false)
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if c := src.lastCall(); c.scrolled {
			t.Fatalf("Snapshot called with %+v after returning to live", c)
		}

		fs := fx.p.frames(t)
		if len(fs) != 3 {
			t.Fatalf("got %d frames, want 3", len(fs))
		}
		s := fs[1]
		if s.Top == nil || *s.Top != 100 || s.Cursor.Visible {
			t.Fatalf("scrolled frame top %v cursor %+v, want top 100 and a hidden cursor", s.Top, s.Cursor)
		}
		if got := lineMap(s); got[0] != "line 100" || got[1] != "line 101" {
			t.Fatalf("scrolled frame rows %v, want the lines from 100", got)
		}
		live := fs[2]
		if live.Top != nil || !live.Cursor.Visible || lineMap(live)[0] != "live0" {
			t.Fatalf("live frame %+v, want no top, a visible cursor and the live rows", live)
		}
		if strings.Contains(string(fx.p.all()[2].msg.Body), `"top"`) {
			t.Fatal(`a live frame carries "top"`)
		}
	})
}

func TestControlOverflowReturnsFalse(t *testing.T) {
	big := proto.OKReply(1, map[string]string{"x": strings.Repeat("a", 1000)})
	size := 4 + len(big.Body) // the length prefix and the body
	l := New(Options{ControlMax: 4*size + size/2})
	sent := 0
	for l.Send(big) {
		sent++
		if sent > 10 {
			t.Fatal("Send kept returning true above ControlMax")
		}
	}
	if sent != 4 {
		t.Fatalf("%d messages fit into a queue of four and a half, want 4", sent)
	}
	if l.Send(ok(2)) {
		t.Fatal("a small Send after the overflow returned true: a message would be lost in between")
	}
	m := ok(3)
	if l.Show(&m, nil) {
		t.Fatal("Show with a message after the overflow returned true")
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if err := l.Run(a); !errors.Is(err, errControlFull) {
		t.Fatalf("Run returned %v, want the overflow error", err)
	}
}

func socketPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	conn := func(fd int) net.Conn {
		f := os.NewFile(uintptr(fd), "lane-test")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatalf("FileConn: %v", err)
		}
		return c
	}
	return conn(fds[0]), conn(fds[1])
}

func TestStalledPeerEndsRunAfterStallTimeout(t *testing.T) {
	a, b := socketPair(t)
	defer a.Close()
	defer b.Close()
	const stall = 200 * time.Millisecond
	l := New(Options{StallTimeout: stall})
	body := strings.Repeat("a", 512<<10)
	for i := 1; i <= 8; i++ { // 4 MiB, far more than a socket buffer
		if !l.Send(proto.OKReply(uint64(i), map[string]string{"x": body})) {
			t.Fatalf("Send %d returned false", i)
		}
	}
	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- l.Run(a) }()
	select {
	case err := <-errc:
		elapsed := time.Since(start)
		if !errors.Is(err, errStalled) {
			t.Fatalf("Run returned %v, want the stall error", err)
		}
		if elapsed < stall {
			t.Fatalf("Run ended after %v, before the stall timeout of %v", elapsed, stall)
		}
		if elapsed > 10*stall {
			t.Fatalf("Run ended after %v, long after the stall timeout of %v", elapsed, stall)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end although the peer reads nothing")
	}
}

func TestPingIsSentAfterAnIdleInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const every = 5 * time.Second
		fx := run(t, Options{PingInterval: every}, true)
		pings := func() int {
			n := 0
			for _, g := range fx.p.all() {
				if g.msg.T == proto.TypePing {
					n++
				}
			}
			return n
		}
		time.Sleep(every - time.Millisecond)
		synctest.Wait()
		if n := pings(); n != 0 {
			t.Fatalf("%d pings before the interval passed", n)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if n := pings(); n != 1 {
			t.Fatalf("%d pings after an idle interval, want 1", n)
		}
		// Anything written restarts the interval.
		time.Sleep(2 * time.Second)
		fx.l.Send(ok(1))
		time.Sleep(every - time.Millisecond)
		synctest.Wait()
		if n := pings(); n != 1 {
			t.Fatalf("%d pings although a message was written %v ago", n, every-time.Millisecond)
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if n := pings(); n != 2 {
			t.Fatalf("%d pings, want 2", n)
		}
	})
}

func TestCloseDrainsQueuedControlMessages(t *testing.T) {
	t.Run("a reading peer gets everything", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fx := run(t, Options{}, false)
			src := newSource(10, "x")
			show(t, fx.l, nil, map[string]Source{"p1": src})
			synctest.Wait() // frame 1 is stuck in the pipe
			for i := 1; i <= 100; i++ {
				fx.l.Send(ok(uint64(i)))
			}
			src.set(func(s *pane.Snap) { s.Lines[0] = "y" })
			fx.l.Dirty("p1")
			fx.p.start()
			fx.l.Close(500 * time.Millisecond)
			if err := <-fx.errc; err != nil {
				t.Fatalf("Run returned %v after Close, want nil", err)
			}
			fx.errc <- nil // for the cleanup
			synctest.Wait()
			var ids []uint64
			frames := 0
			for _, g := range fx.p.all() {
				if g.msg.T == proto.TypeOK {
					ids = append(ids, g.msg.ID)
				} else {
					frames++
				}
			}
			if len(ids) != 100 || ids[0] != 1 || ids[99] != 100 {
				t.Fatalf("got %d control messages after Close, want all 100 in order", len(ids))
			}
			if frames != 1 {
				t.Fatalf("got %d frames, want only the one written before Close", frames)
			}
		})
	})
	t.Run("a peer that reads nothing holds Close for the drain only", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fx := run(t, Options{}, false)
			fx.l.Send(ok(1))
			synctest.Wait()
			start := time.Now()
			fx.l.Close(500 * time.Millisecond)
			if d := time.Since(start); d != 500*time.Millisecond {
				t.Fatalf("Close took %v, want the drain of 500ms", d)
			}
			if err := <-fx.errc; err != nil {
				t.Fatalf("Run returned %v after Close, want nil", err)
			}
			fx.errc <- nil
		})
	})
}

func TestDirtyNeverBlocks(t *testing.T) {
	hammer := func(t *testing.T, l *Lane, what string) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 10000 {
				l.Dirty("p1")
				l.Dirty("p2")
				l.Dirty("gone")
			}
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("10,000 Dirty calls did not return while %s", what)
		}
	}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	l := New(Options{FrameInterval: time.Microsecond})
	errc := make(chan error, 1)
	go func() { errc <- l.Run(a) }()

	// Run is inside a Snapshot that does not return.
	stuck := newSource(5, "x")
	stuck.block = make(chan struct{})
	stuck.enter = make(chan struct{}, 1)
	show(t, l, nil, map[string]Source{"p1": stuck})
	<-stuck.enter
	hammer(t, l, "a Snapshot blocks")

	// Run is inside a Write the peer does not read.
	close(stuck.block)
	show(t, l, nil, map[string]Source{"p1": newSource(5, "a"), "p2": newSource(5, "b")})
	hammer(t, l, "the peer reads nothing")

	l.Close(0)
	a.Close()
	if err := <-errc; err != nil {
		t.Fatalf("Run returned %v after Close, want nil", err)
	}
}

func TestHideStopsFramesAndDirtyOfAHiddenPaneIsIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := run(t, Options{}, true)
		src := newSource(10, "a")
		show(t, fx.l, nil, map[string]Source{"p1": src})
		synctest.Wait()
		fx.l.Hide()
		src.set(func(s *pane.Snap) { s.Lines[0] = "b" })
		fx.l.Dirty("p1")
		fx.l.Scroll("p1", 5, true)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if n := len(fx.p.all()); n != 1 {
			t.Fatalf("got %d messages, want only the frame before Hide", n)
		}
	})
}

func TestRunEndsWithTheErrorOfTheConnection(t *testing.T) {
	a, b := net.Pipe()
	b.Close()
	l := New(Options{})
	l.Send(ok(1))
	if err := l.Run(a); err == nil || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Run returned %v, want the closed pipe", err)
	}
	if l.Send(ok(2)) {
		t.Fatal("Send after Run failed returned true")
	}
}

func BenchmarkFrameBuild200x50(b *testing.B) {
	const cols, rows = 200, 50
	lines := make([]string, rows)
	for y := range lines {
		var sb strings.Builder
		for x := 0; x < cols/20; x++ {
			fmt.Fprintf(&sb, "\x1b[0;1;38;5;%dmword%02d\x1b[0m <tag> & ", (x*7+y)%256, x)
		}
		lines[y] = sb.String()
	}
	snap := pane.Snap{Cols: cols, Rows: rows, Lines: lines,
		Cursor: vt.Cursor{X: 3, Y: 4, Visible: true, Shape: "bar"}, First: 0, Base: 10000}
	b.ReportAllocs()
	for b.Loop() {
		s := &slot{pane: "p1"}
		p, _, err := build(s, snap)
		if err != nil || len(p) != 1 {
			b.Fatalf("build: %d pieces, %v", len(p), err)
		}
	}
}
