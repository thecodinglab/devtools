package fleet

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"devtools/internal/client/panes"
	"devtools/internal/config"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// The frames a switch buffers (3.5.4 step 6) against a hostile or merely
// busy link: only the panes of view.ok, a sequence the client's receiver
// accepts, and a bound on what is kept whether the frames are full or not
// (SEC12). The loop runs on the test goroutine, so every step is exact.

// bench is a fleet whose three machines are online with sessions s1..s3,
// each of one tab t1, on links of the test.
type bench struct {
	t     *testing.T
	f     *Fleet
	clock *stepClock
	links map[string]*testLink
}

func newBench(t *testing.T) *bench {
	t.Helper()
	b := &bench{t: t, clock: &stepClock{now: time.Unix(1795000000, 0)}, links: map[string]*testLink{}}
	b.f = New(Options{
		Machines: []config.Machine{{Name: "M", Connect: true}, {Name: "K", Connect: true}},
		Dialer:   waitDialer{},
		Clock:    b.clock,
		Cols:     80, Rows: 23,
	})
	b.f.updates.close()
	b.f.updates = &pipe[Update]{wake: make(chan struct{}, 1), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	b.f.ctx = ctx
	t.Cleanup(func() {
		cancel()
		for _, l := range b.links {
			l.end()
		}
		close(b.f.stop)
	})
	for _, mc := range b.f.machines {
		b.f.dial(mc)
	}
	b.run()
	for _, name := range []string{"", "M", "K"} {
		mc := b.f.byName[name]
		l := &testLink{
			welcome: proto.Welcome{Gen: proto.Gen, Boot: "boot-" + name, Host: "h" + name},
			recv:    make(chan proto.Msg, 1),
		}
		b.links[name] = l
		b.f.dialed(mc, mc.gen, l, nil)
		var ss []proto.Session
		for n := 1; n <= 3; n++ {
			ss = append(ss, proto.Session{ID: fmt.Sprintf("s%d", n), Dir: fmt.Sprintf("/d/%d", n), Label: "l", Tabs: []string{"t1"}, Current: "t1"})
		}
		snap := proto.Snapshot{Rev: 1, Sessions: ss, Tabs: []proto.Tab{}, Panes: []proto.Pane{}, Agents: []proto.Agent{}}
		b.f.linkMessage(linkMsg{mc: mc, gen: mc.gen, msg: proto.Encode(snap)})
		b.run()
		if mc.link != l || !mc.online() {
			t.Fatalf("machine %q is not online", name)
		}
	}
	b.take()
	return b
}

func (b *bench) run() {
	b.f.drain()
	b.f.settle()
}

// deliver hands m to the fleet as a message of name's link.
func (b *bench) deliver(name string, m proto.Msg) {
	mc := b.f.byName[name]
	b.f.linkMessage(linkMsg{mc: mc, gen: mc.gen, msg: m})
	b.run()
}

// sentOf is what the fleet sent of type typ on name's link.
func (b *bench) sentOf(name, typ string) []proto.Msg {
	var out []proto.Msg
	for _, m := range b.links[name].messages() {
		if m.T == typ {
			out = append(out, m)
		}
	}
	return out
}

// pick switches to target and returns the view.set it sent.
func (b *bench) pick(t session.Target) (uint64, proto.ViewSet) {
	b.t.Helper()
	before := len(b.sentOf(t.Machine, proto.TypeViewSet))
	b.f.Switch(t)
	b.run()
	sets := b.sentOf(t.Machine, proto.TypeViewSet)
	if len(sets) != before+1 {
		b.t.Fatalf("no view.set to %q for %+v", t.Machine, t)
	}
	var vs proto.ViewSet
	if err := sets[before].Decode(&vs); err != nil {
		b.t.Fatal(err)
	}
	return sets[before].ID, vs
}

// take is the updates since the last take.
func (b *bench) take() []Update {
	b.f.updates.mu.Lock()
	defer b.f.updates.mu.Unlock()
	us := b.f.updates.q
	b.f.updates.q = nil
	return us
}

func benchOK(id uint64, session string, ps ...string) proto.Msg {
	return proto.Request(id, proto.ViewOK{Session: session, Dir: "/d/1", Tab: "t1", Panes: ps})
}

func benchFrame(pane string, seq uint64, cols int, full, more bool, text string) proto.Msg {
	return proto.Encode(proto.Frame{Pane: pane, Seq: seq, Cols: cols, Rows: 23, Full: full, More: more, Lines: []proto.Line{{Y: 0, S: text}}})
}

// receive applies the updates to a receiver as the client does: Shown
// starts a new view, a frame is applied. It fails the test where the
// client would drop the link.
func receive(t *testing.T, set *panes.Set, us []Update) (shown int) {
	t.Helper()
	for _, u := range us {
		switch u.Kind {
		case Shown:
			set.Reset(u.View)
			shown++
		case Frame:
			if u.Frame.T != proto.TypeFrame {
				continue
			}
			var fr proto.Frame
			if err := u.Frame.Decode(&fr); err != nil {
				t.Fatal(err)
			}
			if err := set.Apply(fr); err != nil {
				t.Fatalf("the client refuses a frame of the fleet: %v", err)
			}
		}
	}
	return shown
}

// W2-openai-sol-W2-01
func TestFramesOfPanesOutsideTheViewAreNotBuffered(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1"))
	for i := range 1024 {
		b.deliver("M", benchFrame(fmt.Sprintf("p%d", 1000+i), 1, 80, true, false, "not of the view"))
	}
	p := switcherOf(b.f).pend
	if p == nil {
		t.Fatal("the switch ended")
	}
	if len(p.frames) != 0 || p.bytes != 0 {
		t.Fatalf("kept %d frames (%d bytes) of panes the view.ok does not list", len(p.frames), p.bytes)
	}
	b.deliver("M", benchFrame("p1", 1, 80, true, false, "p1"))
	var set panes.Set
	us := b.take()
	if receive(t, &set, us) != 1 {
		t.Fatal("not shown after the full frame of its one pane")
	}
	for _, u := range us {
		if u.Kind == Frame && strings.Contains(string(u.Frame.Body), "not of the view") {
			t.Fatal("a frame of a pane outside the view reached the client")
		}
	}
}

// W2-openai-sol-W2-01: full frames that start a pane again replace what it
// had, so they never pile up.
func TestRepeatedFullFramesOfAPaneKeepOneCopy(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1", "p2"))
	for range 1024 {
		b.deliver("M", benchFrame("p1", 1, 80, true, false, "again"))
	}
	p := switcherOf(b.f).pend
	if p == nil {
		t.Fatal("the switch ended")
	}
	if len(p.frames) != 1 {
		t.Fatalf("kept %d frames of one pane that started again 1024 times", len(p.frames))
	}
}

// W2-openai-sol-W2-01: the pieces of a full frame that never ends count:
// beyond the 512 partial frames and one full frame per pane the switch
// rolls back.
func TestAFullFrameThatNeverEndsRollsBack(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1"))
	for i := range maxFrames + 1 {
		if switcherOf(b.f).pend == nil {
			t.Fatalf("rolled back after %d pieces", i)
		}
		b.deliver("M", benchFrame("p1", 1, 80, true, true, "piece"))
	}
	if switcherOf(b.f).pend != nil {
		t.Fatalf("still switching with %d pieces of one unfinished full frame", len(switcherOf(b.f).pend.frames))
	}
	if n := len(b.sentOf("M", proto.TypeViewClear)); n != 1 {
		t.Fatalf("%d view.clear on M, want 1", n)
	}
	noticed := false
	for _, u := range b.take() {
		noticed = noticed || (u.Kind == Notice && strings.Contains(u.Notice, "did not answer"))
		if u.Kind == Shown || u.Kind == Frame {
			t.Fatalf("update %v of a switch that rolled back", u.Kind)
		}
	}
	if !noticed {
		t.Fatal("no notice")
	}
}

// W2-openai-sol-W2-01: what is kept is bounded in bytes too.
func TestBufferedFramesAreBoundedInBytes(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1"))
	big := benchFrame("p1", 1, 80, true, true, strings.Repeat("x", 1<<20))
	n := 0
	for switcherOf(b.f).pend != nil {
		if n > maxBufBytes>>20+1 {
			t.Fatalf("still switching with %d bytes buffered", switcherOf(b.f).pend.bytes)
		}
		b.deliver("M", big) // the same body: the test's memory stays small
		n++
	}
	if n >= maxFrames {
		t.Fatalf("the rollback came after %d pieces, by their count", n)
	}
}

// W2-openai-sol-W2-01: a frame that does not follow the pane's sequence is
// not kept.
func TestFramesOutOfSequenceAreNotBuffered(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1", "p2"))
	b.deliver("M", benchFrame("p1", 2, 80, false, false, "partial before any full"))
	b.deliver("M", benchFrame("p1", 1, 80, true, false, "p1 full"))
	b.deliver("M", benchFrame("p1", 5, 80, false, false, "a gap"))
	b.deliver("M", benchFrame("p1", 2, 90, false, false, "a new size without full"))
	b.deliver("M", benchFrame("p1", 2, 80, true, true, "piece 1"))
	b.deliver("M", benchFrame("p1", 3, 80, true, false, "a piece of another seq"))
	b.deliver("M", benchFrame("p1", 2, 80, false, false, "a piece that is not full"))
	b.deliver("M", benchFrame("p1", 2, 80, true, false, "piece 2"))
	b.deliver("M", benchFrame("p2", 1, 80, true, false, "p2 full"))
	var got []string
	var set panes.Set
	us := b.take()
	if receive(t, &set, us) != 1 {
		t.Fatal("not shown")
	}
	for _, u := range us {
		if u.Kind == Frame && u.Frame.T == proto.TypeFrame {
			var fr proto.Frame
			_ = u.Frame.Decode(&fr)
			got = append(got, fr.Lines[0].S)
		}
	}
	want := []string{"p1 full", "piece 1", "piece 2", "p2 full"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("frames after Shown %q, want %q", got, want)
	}
}

// W2-openai-sol-W2-02
func TestBufferedFramesReachTheReceiverInSequence(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1", "p2"))
	b.deliver("M", benchFrame("p1", 1, 80, true, false, "p1 at 80"))
	b.deliver("M", benchFrame("p1", 2, 100, true, false, "p1 at 100"))
	b.deliver("M", benchFrame("p1", 3, 100, false, false, "p1 changed"))
	b.deliver("M", benchFrame("p2", 1, 80, true, false, "p2"))
	var set panes.Set
	if receive(t, &set, b.take()) != 1 {
		t.Fatal("not shown")
	}
	if sc := set.Screen("p1"); sc == nil {
		t.Fatal("no screen of p1")
	}
	// the frames that follow the commit continue the sequence
	b.deliver("M", benchFrame("p1", 4, 100, false, false, "p1 live"))
	b.deliver("M", benchFrame("p2", 2, 80, false, false, "p2 live"))
	receive(t, &set, b.take())
}

// W2-openai-sol-W2-02: a pane that starts again at seq 1 before the commit
// (its displayed set changed) reaches the receiver from that frame on.
func TestAPaneThatStartsAgainIsForwardedFromItsNewFirstFrame(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "M", Session: "s1"})
	b.deliver("M", benchOK(id, "s1", "p1", "p2"))
	b.deliver("M", benchFrame("p1", 1, 80, true, false, "p1 first"))
	b.deliver("M", benchFrame("p1", 2, 80, false, false, "p1 partial"))
	b.deliver("M", benchFrame("p1", 1, 60, true, false, "p1 again"))
	b.deliver("M", benchFrame("p2", 1, 80, true, false, "p2"))
	var set panes.Set
	us := b.take()
	if receive(t, &set, us) != 1 {
		t.Fatal("not shown")
	}
	for _, u := range us {
		if u.Kind == Frame && strings.Contains(string(u.Frame.Body), "p1 first") {
			t.Fatal("the frames before the pane started again were forwarded")
		}
	}
}

// resizesAfter is the view.resize the fleet sent on name's link after the
// view.set with id, as cols×rows.
func (b *bench) resizesAfter(name string, id uint64) []string {
	var out []string
	after := false
	for _, m := range b.links[name].messages() {
		if m.T == proto.TypeViewSet && m.ID == id {
			after = true
			continue
		}
		if after && m.T == proto.TypeViewResize {
			var vr proto.ViewResize
			_ = m.Decode(&vr)
			out = append(out, fmt.Sprintf("%d×%d", vr.Cols, vr.Rows))
		}
	}
	return out
}

// W2-openai-sol-W2-03, from no view
func TestAResizeBeforeViewOKReachesTheTargetFromNoView(t *testing.T) {
	b := newBench(t)
	id, vs := b.pick(session.Target{Machine: "M", Session: "s1"})
	if vs.Cols != 80 || vs.Rows != 23 {
		t.Fatalf("view.set %+v", vs)
	}
	b.f.Resize(120, 40)
	b.run()
	if got := b.resizesAfter("M", id); len(got) != 0 {
		t.Fatalf("view.resize %q before the view.ok", got)
	}
	b.deliver("M", benchOK(id, "s1", "p1"))
	if got := b.resizesAfter("M", id); len(got) != 1 || got[0] != "120×40" {
		t.Fatalf("view.resize after the view.ok %q, want 120×40", got)
	}
	b.deliver("M", benchFrame("p1", 1, 120, true, false, "p1"))
	if mode, _, _ := switcherOf(b.f).state(); mode != "showing" {
		t.Fatalf("mode %q", mode)
	}
}

// W2-openai-sol-W2-03, between two machines
func TestAResizeBeforeViewOKReachesTheTargetOnAnotherMachine(t *testing.T) {
	b := newBench(t)
	id, _ := b.pick(session.Target{Machine: "", Session: "s1"})
	b.deliver("", benchOK(id, "s1", "p1"))
	b.deliver("", benchFrame("p1", 1, 80, true, false, "here"))
	if mode, _, _ := switcherOf(b.f).state(); mode != "showing" {
		t.Fatalf("mode %q", mode)
	}
	id, _ = b.pick(session.Target{Machine: "K", Session: "s2"})
	b.f.Resize(120, 40)
	b.run()
	if n := len(b.sentOf("", proto.TypeViewResize)); n != 1 {
		t.Fatalf("%d view.resize to the shown machine, want 1", n)
	}
	b.deliver("K", benchOK(id, "s2", "p2"))
	if got := b.resizesAfter("K", id); len(got) != 1 || got[0] != "120×40" {
		t.Fatalf("view.resize to K after its view.ok %q, want 120×40", got)
	}
	// a resize after the view.ok reaches K at once, and only once
	b.f.Resize(100, 30)
	b.run()
	if got := b.resizesAfter("K", id); len(got) != 2 || got[1] != "100×30" {
		t.Fatalf("view.resize to K %q, want 120×40 then 100×30", got)
	}
	b.deliver("K", benchFrame("p2", 1, 100, true, false, "K"))
	if got := b.resizesAfter("K", id); len(got) != 2 {
		t.Fatalf("view.resize to K after the commit %q", got)
	}
}
