package fleet

import (
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// The state of the displayed server as the client gets it (5.3.8, contract
// change 14): an update of kind Frame whose Frame is a state.snapshot of the
// machine's cleaned state, right after each Shown and before the frames
// that follow it, and after every change of that state while the view is
// shown and not stale, in the order the link delivered it.

// shownOn commits session s1 of machine name with its one pane p1 and
// returns the updates of the commit.
func (b *bench) shownOn(name string) []Update {
	b.t.Helper()
	id, _ := b.pick(session.Target{Machine: name, Session: "s1"})
	b.deliver(name, benchOK(id, "s1", "p1"))
	b.deliver(name, benchFrame("p1", 1, 80, true, false, "p1 first"))
	us := b.take()
	if mode, _, _ := switcherOf(b.f).state(); mode != "showing" {
		b.t.Fatalf("mode %q after the full frame", mode)
	}
	return us
}

// stateUpdate is the snapshot u carries when u is the displayed server's
// state.
func stateUpdate(u Update) (proto.Snapshot, bool) {
	var s proto.Snapshot
	if u.Kind != Frame || u.Frame.T != proto.TypeStateSnapshot || u.Frame.Decode(&s) != nil {
		return proto.Snapshot{}, false
	}
	return s, true
}

// states are the snapshots among us.
func states(us []Update) []proto.Snapshot {
	var out []proto.Snapshot
	for _, u := range us {
		if s, ok := stateUpdate(u); ok {
			out = append(out, s)
		}
	}
	return out
}

// paneOf is pane id of the snapshot.
func paneOf(s proto.Snapshot, id string) (proto.Pane, bool) {
	for _, p := range s.Panes {
		if p.ID == id {
			return p, true
		}
	}
	return proto.Pane{}, false
}

// order names the updates: Shown, state@rev, frame:text, and the kind of
// anything else.
func order(us []Update) string {
	var out []string
	for _, u := range us {
		switch {
		case u.Kind == Shown:
			out = append(out, "Shown")
		case u.Kind == Frame && u.Frame.T == proto.TypeStateSnapshot:
			s, _ := stateUpdate(u)
			out = append(out, "state@"+itoa(int(s.Rev)))
		case u.Kind == Frame && u.Frame.T == proto.TypeFrame:
			var fr proto.Frame
			_ = u.Frame.Decode(&fr)
			out = append(out, "frame:"+fr.Lines[0].S)
		case u.Kind == Summary:
			// the status row; not part of the order of the view
		default:
			out = append(out, "kind"+itoa(int(u.Kind)))
		}
	}
	return strings.Join(out, " ")
}

// editorDelta is a state.delta of revision rev that puts tab t1 of s1 with
// pane p1, whose program is program, in an editor or not, busy or not.
func editorDelta(rev uint64, program string, editor bool) proto.Msg {
	return proto.Encode(proto.Delta{Rev: rev, Changes: []proto.Change{
		proto.Put(proto.Tab{ID: "t1", Session: "s1", Index: 1, Panes: []string{"p1"}, Focused: "p1"}),
		proto.Put(proto.Pane{ID: "p1", Tab: "t1", Session: "s1", State: proto.PaneRunning, Program: program, Editor: editor, Busy: editor}),
	}})
}

// W3-openai-sol-W3-01
func TestTheDisplayedStateFollowsShownBeforeTheBufferedFrames(t *testing.T) {
	b := newBench(t)
	us := b.shownOn("M")
	if got, want := order(us), "Shown state@1 frame:p1 first"; got != want {
		t.Fatalf("the commit hands on %q, want %q", got, want)
	}
	for _, u := range us {
		if s, ok := stateUpdate(u); ok {
			if u.Target != (session.Target{Machine: "M", Session: "s1", Tab: "t1"}) {
				t.Fatalf("the state is addressed to %+v", u.Target)
			}
			if len(s.Sessions) != 3 || s.Sessions[0].ID != "s1" || s.Sessions[2].ID != "s3" {
				t.Fatalf("the state is not M's: %+v", s.Sessions)
			}
		}
	}
}

// W3-openai-sol-W3-01: a session shown again (a tab selected in it, 3.5.4
// step 2) is a Shown, and its state follows it too.
func TestShowingTheSameSessionAgainHandsOnItsState(t *testing.T) {
	b := newBench(t)
	b.shownOn("M")
	b.f.Switch(session.Target{Machine: "M", Session: "s1", Tab: "t1"})
	b.run()
	sets := b.sentOf("M", proto.TypeViewSet)
	again := sets[len(sets)-1].ID
	b.deliver("M", benchOK(again, "s1", "p1"))
	if got, want := order(b.take()), "Shown state@1"; got != want {
		t.Fatalf("showing the session again hands on %q, want %q", got, want)
	}
}

// W3-openai-sol-W3-01: a change of the editor, busy and program fields that
// no frame follows (a program that starts without printing) still reaches
// the client, at the latest when the summary's 50 ms end.
func TestAChangeOfTheDisplayedStateWithoutFramesReachesTheClient(t *testing.T) {
	b := newBench(t)
	b.shownOn("M")
	b.clock.advance(time.Second, b.f)
	b.take()

	// no summary is being held back: the state is handed on in the same step
	b.deliver("M", editorDelta(2, "nvim", true))
	got := states(b.take())
	if len(got) != 1 || got[0].Rev != 2 {
		t.Fatalf("states %+v after a delta of the displayed machine, want revision 2", got)
	}
	if p, ok := paneOf(got[0], "p1"); !ok || !p.Editor || !p.Busy || p.Program != "nvim" {
		t.Fatalf("pane p1 in the state handed on: %+v", got[0].Panes)
	}

	// a summary is being held back by its rate limit: the state follows when
	// it ends, with nothing of the link in between
	agent := func(rev uint64, status string) proto.Msg {
		return proto.Encode(proto.Delta{Rev: rev, Changes: []proto.Change{
			proto.Put(proto.Agent{Pane: "p1", Tab: "t1", Session: "s1", Kind: proto.AgentClaude, Status: status}),
		}})
	}
	b.deliver("M", agent(3, proto.StatusWorking)) // a summary goes out
	b.deliver("M", agent(4, proto.StatusBlocked)) // another one is held back
	if !b.f.sum.timer.active() {
		t.Fatal("the summary is not held back; the test does not test what it says")
	}
	b.deliver("M", editorDelta(5, "zsh", false))
	b.clock.advance(summaryEvery, b.f)
	got = states(b.take())
	if len(got) == 0 || got[len(got)-1].Rev != 5 {
		t.Fatalf("states %+v within 50 ms of the change, want revision 5 last", got)
	}
	if p, ok := paneOf(got[len(got)-1], "p1"); !ok || p.Editor || p.Busy || p.Program != "zsh" {
		t.Fatalf("pane p1 in the state handed on: %+v", got[len(got)-1].Panes)
	}
	b.clock.advance(time.Second, b.f)
	if got := states(b.take()); len(got) != 0 {
		t.Fatalf("the same state handed on again: %+v", got)
	}
}

// W3-openai-sol-W3-01 (W4): the state of a tab change precedes the frames
// the link sent after it, whether or not the summary is held back.
func TestAChangeOfTheDisplayedStateComesBeforeTheFramesAfterIt(t *testing.T) {
	b := newBench(t)
	b.shownOn("M")
	b.deliver("M", proto.Encode(proto.Delta{Rev: 2, Changes: []proto.Change{
		proto.Put(proto.Session{ID: "s1", Dir: "/d/1", Label: "l", Tabs: []string{"t1", "t2"}, Current: "t2"}),
		proto.Put(proto.Tab{ID: "t2", Session: "s1", Index: 2, Panes: []string{"p2"}, Focused: "p2"}),
		proto.Put(proto.Pane{ID: "p2", Tab: "t2", Session: "s1", State: proto.PaneRunning, Program: "zsh"}),
	}}))
	b.deliver("M", benchFrame("p2", 1, 80, true, false, "p2 first"))
	b.deliver("M", benchFrame("p1", 2, 80, false, false, "p1 second"))
	b.deliver("M", editorDelta(3, "vim", true))
	b.deliver("M", benchFrame("p1", 3, 80, false, false, "p1 third"))
	b.clock.advance(time.Second, b.f)
	got := order(b.take())
	want := "state@2 frame:p2 first frame:p1 second state@3 frame:p1 third"
	if got != want {
		t.Fatalf("handed on %q, want %q", got, want)
	}
}

// W3-openai-sol-W3-01: only the machine of the committed view, while it is
// shown and not stale, hands on its state.
func TestTheStateOfAnotherOrAStaleMachineIsNotHandedOn(t *testing.T) {
	b := newBench(t)
	b.shownOn("M")
	b.clock.advance(time.Second, b.f)
	b.take()
	b.deliver("K", editorDelta(2, "nvim", true))
	b.deliver("", editorDelta(2, "nvim", true))
	b.clock.advance(time.Second, b.f)
	if got := states(b.take()); len(got) != 0 {
		t.Fatalf("the state of a machine not shown was handed on: %+v", got)
	}

	// while a switch to K runs, M is still displayed
	id, _ := b.pick(session.Target{Machine: "K", Session: "s2"})
	b.deliver("M", editorDelta(2, "nvim", true))
	if got := states(b.take()); len(got) != 1 || got[0].Rev != 2 {
		t.Fatalf("states %+v of the displayed machine while a switch runs", got)
	}
	b.deliver("K", benchOK(id, "s2", "p1"))
	b.deliver("K", editorDelta(3, "vim", true)) // before the commit: K is not displayed yet
	if got := states(b.take()); len(got) != 0 {
		t.Fatalf("the state of the target was handed on before the commit: %+v", got)
	}
	b.deliver("K", benchFrame("p1", 1, 80, true, false, "K"))
	if got, want := order(b.take()), "Shown state@3 frame:K"; got != want {
		t.Fatalf("the commit on K hands on %q, want %q", got, want)
	}
	b.deliver("M", editorDelta(3, "zsh", false))
	b.clock.advance(time.Second, b.f)
	if got := states(b.take()); len(got) != 0 {
		t.Fatalf("the state of the machine shown before was handed on: %+v", got)
	}

	// K is lost: its view is stale, and what K had is not handed on again
	mc := b.f.byName["K"]
	b.f.lost(mc, &remote.Failure{Reason: remote.Unreachable, Text: "gone"})
	b.run()
	b.clock.advance(time.Second, b.f)
	if got := states(b.take()); len(got) != 0 {
		t.Fatalf("a state was handed on for a stale view: %+v", got)
	}
}
