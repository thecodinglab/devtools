package fleet_test

import (
	"fmt"
	"testing"
	"time"

	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// The switch state machine of 3.5.4 and the stale view of 3.5.5, through
// the rig: a scripted server per machine, a fake clock.

// dir is the directory of session N in these tests.
func dir(n int) string { return fmt.Sprintf("/home/florian/dev/p%d/main", n) }

// ss is session N of a snapshot of fleettest.
func ss(n int) fleettest.Sess {
	return fleettest.Sess{N: n, Dir: dir(n), Label: fmt.Sprintf("p%d-main", n)}
}

// target is session N of machine m.
func target(m string, n int) session.Target {
	return session.Target{Machine: m, Session: fmt.Sprintf("s%d", n)}
}

// startShowing is a rig whose local machine is online with session 1 and
// shows it, and whose other machines are the lines.
func startShowing(t *testing.T, lines ...string) *fleettest.Rig {
	t.Helper()
	r := fleettest.New(t, machines(lines...)...)
	r.M("").Online(fleettest.Snapshot(ss(1)))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "") == "online" })
	show(t, r, "", 1)
	return r
}

// viewSets is how many view.set the machine got.
func viewSets(r *fleettest.Rig, name string) int { return len(r.M(name).SentOf(proto.TypeViewSet)) }

// nextViewSet waits for view.set number n+1 to machine name and returns it.
func nextViewSet(t *testing.T, r *fleettest.Rig, name string, n int) (uint64, proto.ViewSet) {
	t.Helper()
	r.Eventually(fmt.Sprintf("view.set number %d to %q", n+1, name), func() bool { return viewSets(r, name) > n })
	m := r.M(name).SentOf(proto.TypeViewSet)[n]
	var vs proto.ViewSet
	if err := m.Decode(&vs); err != nil {
		t.Fatal(err)
	}
	return m.ID, vs
}

// viewOK is the view.ok of session N with its one pane, or with panes.
func viewOK(id uint64, n int, panes ...string) proto.Msg {
	if len(panes) == 0 {
		panes = []string{fmt.Sprintf("p%d", n)}
	}
	return proto.Request(id, proto.ViewOK{Session: fmt.Sprintf("s%d", n), Dir: dir(n), Tab: fmt.Sprintf("t%d", n), Panes: panes})
}

// frame is a frame of pane; text marks it.
func frame(pane string, seq uint64, full bool, text string) proto.Msg {
	return proto.Encode(proto.Frame{Pane: pane, Seq: seq, Cols: 80, Rows: 23, Full: full, Lines: []proto.Line{{Y: 0, S: text}}})
}

// frameText is the mark of a frame update.
func frameText(t *testing.T, u fleet.Update) string {
	t.Helper()
	var f proto.Frame
	if err := u.Frame.Decode(&f); err != nil || len(f.Lines) == 0 {
		t.Fatalf("not a frame: %+v %v", u.Frame, err)
	}
	return f.Lines[0].S
}

// isPicture reports whether u is a frame of a pane, and not the state of
// the displayed server, which also arrives as an update of kind Frame.
func isPicture(u fleet.Update) bool { return u.Frame.T == proto.TypeFrame }

// nextPicture is the next update of kind Frame that is a frame of a pane;
// it and the updates of kind Frame before it are taken out.
func nextPicture(r *fleettest.Rig) fleet.Update {
	r.T.Helper()
	return r.NextWhere(fleet.Frame, isPicture)
}

// pictures are the frames of panes among us.
func pictures(us []fleet.Update) []fleet.Update {
	var out []fleet.Update
	for _, u := range us {
		if isPicture(u) {
			out = append(out, u)
		}
	}
	return out
}

// stateOf is the snapshot an update of kind Frame carries; the test fails
// when it is not the state of the displayed server.
func stateOf(t *testing.T, u fleet.Update) proto.Snapshot {
	t.Helper()
	var s proto.Snapshot
	if u.Kind != fleet.Frame || u.Frame.T != proto.TypeStateSnapshot || u.Frame.Decode(&s) != nil {
		t.Fatalf("not the displayed server's state: %v %s %s", fleettest.KindName(u.Kind), u.Frame.T, u.Frame.Body)
	}
	return s
}

// hasSession reports whether the snapshot has the session id.
func hasSession(s proto.Snapshot, id string) bool {
	for _, o := range s.Sessions {
		if o.ID == id {
			return true
		}
	}
	return false
}

// show switches to session N of machine m and answers like its server.
func show(t *testing.T, r *fleettest.Rig, m string, n int) fleet.Update {
	t.Helper()
	before := viewSets(r, m)
	r.F.Switch(target(m, n))
	id, _ := nextViewSet(t, r, m, before)
	r.M(m).Push(viewOK(id, n))
	r.M(m).Push(frame(fmt.Sprintf("p%d", n), 1, true, "first"))
	u := r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Machine == m && u.Target.Session == fmt.Sprintf("s%d", n) })
	st := r.NextWhere(fleet.Frame, func(f fleet.Update) bool { return !isPicture(f) && f.Target == u.Target })
	if s := stateOf(t, st); !hasSession(s, fmt.Sprintf("s%d", n)) {
		t.Fatalf("the state after Shown has no session s%d: %+v", n, s)
	}
	if got := frameText(t, nextPicture(r)); got != "first" {
		t.Fatalf("the first frame after Shown is %q", got)
	}
	r.Settle(func(s fleet.SummaryData) bool {
		return s.Mode == "showing" && s.Target.Machine == m && s.Target.Session == u.Target.Session
	})
	return u
}

// mode waits until the summary's mode and target are as wanted.
func mode(t *testing.T, r *fleettest.Rig, want string, m string, n int) fleet.SummaryData {
	t.Helper()
	return r.Settle(func(s fleet.SummaryData) bool {
		return s.Mode == want && s.Target.Machine == m && s.Target.Session == fmt.Sprintf("s%d", n)
	})
}

// notice waits for a notice with the text.
func notice(t *testing.T, r *fleettest.Rig, text string) {
	t.Helper()
	r.NextWhere(fleet.Notice, func(u fleet.Update) bool { return u.Notice == text })
}

// key is input.key of text to pane.
func key(pane, text string) proto.Msg {
	return proto.Encode(proto.Key{Pane: pane, Key: text, Text: text})
}

// keysOf is the text and pane of every input.key the machine got.
func keysOf(t *testing.T, r *fleettest.Rig, name string) []string {
	t.Helper()
	var out []string
	for _, m := range r.M(name).SentOf(proto.TypeInputKey) {
		var k proto.Key
		if err := m.Decode(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k.Pane+":"+k.Text)
	}
	return out
}

// clears is how many view.clear the machine got.
func clears(r *fleettest.Rig, name string) int { return len(r.M(name).SentOf(proto.TypeViewClear)) }

// SW1
func TestSwitchToARemoteSessionShowsItInTheSameWindow(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, vs := nextViewSet(t, r, "M", 0)
	if vs.Session != "s2" || vs.Cols != 80 || vs.Rows != 23 || vs.Dir != "" {
		t.Fatalf("view.set %+v", vs)
	}
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "M's picture"))
	u := r.Next(fleet.Shown)
	if u.Target != (session.Target{Machine: "M", Session: "s2", Tab: "t2"}) || u.View.Session != "s2" || len(u.View.Panes) != 1 {
		t.Fatalf("shown %+v", u)
	}
	if got := frameText(t, nextPicture(r)); got != "M's picture" {
		t.Fatalf("first frame %q", got)
	}
	r.Eventually("view.clear on the local link", func() bool { return clears(r, "") == 1 })
	if clears(r, "M") != 0 {
		t.Fatal("the target's link was cleared")
	}
	mode(t, r, "showing", "M", 2)
}

// SW2
func TestSwitchBackToLocal(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	show(t, r, "", 1)
	r.Eventually("view.clear on M's link", func() bool { return clears(r, "M") == 1 })
	if clears(r, "") != 1 {
		t.Fatalf("the local link got %d view.clear, want the one of the first switch", clears(r, ""))
	}
}

// SW3
func TestSwitchBetweenTwoRemoteMachines(t *testing.T) {
	r := startShowing(t, "M connect", "K connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	online(t, r, "K", fleettest.Snapshot(ss(3)))
	show(t, r, "M", 2)
	show(t, r, "K", 3)
	r.Eventually("view.clear on M's link", func() bool { return clears(r, "M") == 1 })
	if clears(r, "K") != 0 {
		t.Fatal("K was cleared")
	}
}

// SW4
func TestSwitchToAProjectOpensItsSessionThere(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot())
	r.F.Switch(session.Target{Machine: "M", Dir: dir(7), Name: "p7-main"})
	id, vs := nextViewSet(t, r, "M", 0)
	if vs.Dir != dir(7) || vs.Name != "p7-main" || vs.Session != "" {
		t.Fatalf("view.set %+v, want the directory and its name", vs)
	}
	r.M("M").Push(viewOK(id, 7))
	r.M("M").Push(frame("p7", 1, true, "x"))
	if u := r.Next(fleet.Shown); u.Target.Session != "s7" || u.View.Dir != dir(7) {
		t.Fatalf("shown %+v", u)
	}
}

// SW5
func TestSwitchRollsBackWhenTheMachineIsUnreachable(t *testing.T) {
	r := startShowing(t, "K")
	r.F.Switch(target("K", 2))
	mode(t, r, "switching", "K", 2)
	r.M("K").Fail(remote.Unreachable)
	notice(t, r, "K not reachable.")
	mode(t, r, "showing", "", 1)
	r.None(fleet.Shown)
	r.None(fleet.OpenHub)
}

// SW6
func TestSwitchRollsBackWhenRefused(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 9))
	id, _ := nextViewSet(t, r, "M", 0)
	r.M("M").Push(proto.ErrReply(id, proto.CodeNoSession, "no session s9\x1b[2J"))
	notice(t, r, "M: no session s9[2J")
	mode(t, r, "showing", "", 1)
	if clears(r, "M") != 0 || clears(r, "") != 0 {
		t.Fatal("a refused switch cleared a link: the server changed nothing")
	}
}

// SW7
func TestSwitchRollsBackWithoutFrames(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.M("M").Push(viewOK(id, 2))
	r.Clock.WaitTimer(t, 5*time.Second)
	r.Advance(5 * time.Second)
	notice(t, r, "M did not answer")
	r.Eventually("view.clear on M's link", func() bool { return clears(r, "M") == 1 })
	mode(t, r, "showing", "", 1)
	if clears(r, "") != 0 {
		t.Fatal("the shown view's link was cleared")
	}
	r.None(fleet.Shown)
}

// SW7, the same link
func TestRollbackOnTheSameLinkRestoresTheView(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2), ss(3)))
	show(t, r, "M", 2)
	r.F.Switch(target("M", 3))
	id, _ := nextViewSet(t, r, "M", 1)
	r.M("M").Push(viewOK(id, 3))
	r.M("M").Push(frame("p2", 9, false, "old view after the reply: not shown"))
	r.Clock.WaitTimer(t, 5*time.Second)
	r.Advance(5 * time.Second)
	notice(t, r, "M did not answer")
	again, vs := nextViewSet(t, r, "M", 2)
	if vs.Session != "s2" {
		t.Fatalf("view.set %+v, want the shown session again", vs)
	}
	r.M("M").Push(viewOK(again, 2))
	r.M("M").Push(frame("p2", 1, true, "s2 again"))
	if u := r.Next(fleet.Shown); u.Target.Session != "s2" {
		t.Fatalf("shown %+v", u)
	}
	for _, u := range pictures(r.Take(fleet.Frame)) {
		if frameText(t, u) == "old view after the reply: not shown" {
			t.Fatal("a frame after the target's view.ok reached the client as the old view's")
		}
	}
	if clears(r, "M") != 0 {
		t.Fatal("the shared link was cleared")
	}
	mode(t, r, "showing", "M", 2)
}

// SW8
func TestASwitchWaitsTenSecondsWhileTheDialMayTakeThirty(t *testing.T) {
	r := startShowing(t, "K")
	answer := r.M("K").Later()
	r.F.Switch(target("K", 2))
	r.Eventually("the dial of K", func() bool { return r.M("K").Dials() == 1 })
	r.Clock.WaitTimer(t, 10*time.Second)
	r.Advance(10 * time.Second)
	notice(t, r, "still connecting to K")
	mode(t, r, "showing", "", 1)
	r.Advance(2 * time.Second)
	answer(fleettest.Snapshot(ss(2)))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, "K") == "online" })
	if r.M("K").Dials() != 1 {
		t.Fatalf("K was dialled %d times, want the one attempt", r.M("K").Dials())
	}
	r.F.Switch(target("K", 2))
	id, _ := nextViewSet(t, r, "K", 0)
	r.M("K").Push(viewOK(id, 2))
	r.M("K").Push(frame("p2", 1, true, "x"))
	if u := r.Next(fleet.Shown); u.Target.Machine != "K" {
		t.Fatalf("shown %+v", u)
	}
}

// SW9
func TestASecondPickReplacesTheFirst(t *testing.T) {
	r := startShowing(t, "M connect", "K connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	online(t, r, "K", fleettest.Snapshot(ss(3)))
	r.F.Switch(target("M", 2))
	nextViewSet(t, r, "M", 0)
	r.F.Switch(target("K", 3))
	id, _ := nextViewSet(t, r, "K", 0)
	r.Eventually("view.clear on M's link", func() bool { return clears(r, "M") == 1 })
	mode(t, r, "switching", "K", 3)
	r.M("K").Push(viewOK(id, 3))
	r.M("K").Push(frame("p3", 1, true, "K"))
	if u := r.Next(fleet.Shown); u.Target.Machine != "K" {
		t.Fatalf("shown %+v", u)
	}
	if clears(r, "") != 1 || clears(r, "K") != 0 {
		t.Fatalf("view.clear: local %d, K %d", clears(r, ""), clears(r, "K"))
	}
}

// SW9
func TestALateReplyOfAReplacedSwitchIsIgnored(t *testing.T) {
	r := startShowing(t, "M connect", "K connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	online(t, r, "K", fleettest.Snapshot(ss(3)))
	r.F.Switch(target("M", 2))
	idM, _ := nextViewSet(t, r, "M", 0)
	r.F.Switch(target("K", 3))
	idK, _ := nextViewSet(t, r, "K", 0)
	r.M("M").Push(viewOK(idM, 2))
	r.M("M").Push(frame("p2", 1, true, "late M"))
	r.None(fleet.Shown)
	mode(t, r, "switching", "K", 3)
	r.M("K").Push(viewOK(idK, 3))
	r.M("K").Push(frame("p3", 1, true, "K"))
	if u := r.Next(fleet.Shown); u.Target.Machine != "K" {
		t.Fatalf("shown %+v", u)
	}
	r.M("M").Push(frame("p2", 2, false, "later M"))
	time.Sleep(fleettest.Quiet)
	for _, u := range pictures(r.Take(fleet.Frame)) {
		if s := frameText(t, u); s != "K" {
			t.Fatalf("frame %q of the replaced switch reached the client", s)
		}
	}
	if n := r.Count(fleet.Shown); n != 2 {
		t.Fatalf("%d Shown, want the local one and K", n)
	}
}

// SW10
func TestKeysTypedWhileSwitchingGoToTheTarget(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.F.Input(key("p1", "a"))
	r.F.Input(key("p1", "b"))
	r.F.Input(proto.Encode(proto.Paste{Pane: "p1", Text: "pasted"}))
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "x"))
	r.Next(fleet.Shown)
	r.Eventually("the held keys on M", func() bool { return len(keysOf(t, r, "M")) == 2 })
	if got := keysOf(t, r, "M"); got[0] != "p2:a" || got[1] != "p2:b" {
		t.Fatalf("keys on M %q, want a then b to the target's pane", got)
	}
	pastes := r.M("M").SentOf(proto.TypeInputPaste)
	if len(pastes) != 1 {
		t.Fatalf("%d pastes on M", len(pastes))
	}
	if got := keysOf(t, r, ""); len(got) != 0 {
		t.Fatalf("the old view got keys %q", got)
	}
	r.None(fleet.Notice)
}

// SW10
func TestHeldKeysAreDroppedOnRollback(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.F.Input(key("p1", "a"))
	r.M("M").Push(proto.ErrReply(id, proto.CodeNoSession, "gone"))
	notice(t, r, "M: gone")
	mode(t, r, "showing", "", 1)
	r.F.Input(key("p1", "c"))
	r.Eventually("the key typed after the rollback", func() bool { return len(keysOf(t, r, "")) == 1 })
	if got := keysOf(t, r, ""); got[0] != "p1:c" {
		t.Fatalf("keys on the local link %q, want only the one after the rollback", got)
	}
	if got := keysOf(t, r, "M"); len(got) != 0 {
		t.Fatalf("keys on M %q", got)
	}
}

func TestKeysAreHeldForTwoSecondsThenDropped(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.F.Input(key("p1", "a"))
	r.Clock.WaitTimer(t, 2*time.Second)
	r.Advance(2 * time.Second)
	r.F.Input(key("p1", "b"))
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "x"))
	r.Next(fleet.Shown)
	notice(t, r, "Some keys typed during the switch were dropped")
	if got := keysOf(t, r, "M"); len(got) != 0 {
		t.Fatalf("keys on M %q: nothing typed at the old picture arrives seconds later", got)
	}
	if got := keysOf(t, r, ""); len(got) != 0 {
		t.Fatalf("keys on the old view %q", got)
	}
}

func TestMoreThanTwoHundredFiftySixHeldKeysAreCut(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	for i := range 300 {
		r.F.Input(key("p1", fmt.Sprint(i%10)))
	}
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "x"))
	r.Next(fleet.Shown)
	notice(t, r, "Some keys typed during the switch were dropped")
	r.Eventually("256 keys on M", func() bool { return len(keysOf(t, r, "M")) == 256 })
}

func TestTheSummaryIsSwitchingFromTheFirstInstant(t *testing.T) {
	r := startShowing(t, "K")
	r.M("K").Hang()
	r.F.Switch(target("K", 2))
	s := r.Settle(func(s fleet.SummaryData) bool { return s.Mode != "showing" })
	if s.Mode != "switching" || s.Target != target("K", 2) || s.Stale {
		t.Fatalf("the first summary after the pick is %+v, want switching to K", s)
	}
	if fleettest.StateOf(s, "K") != "connecting" {
		t.Fatalf("K is %q", fleettest.StateOf(s, "K"))
	}
}

func TestFramesOfAnUncommittedViewNeverReachTheClient(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.Take(fleet.Frame)
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.M("M").Push(frame("p2", 1, true, "before view.ok"))
	r.M("M").Push(viewOK(id, 2, "p2", "p5"))
	r.M("M").Push(frame("p2", 1, true, "p2 full"))
	r.M("M").Push(frame("p2", 2, false, "p2 partial"))
	r.None(fleet.Frame)
	r.None(fleet.Shown)
	r.M("M").Push(frame("p5", 1, true, "p5 full"))
	u := r.Next(fleet.Shown)
	if len(u.View.Panes) != 2 {
		t.Fatalf("shown %+v", u)
	}
	var got []string
	for range 3 {
		got = append(got, frameText(t, nextPicture(r)))
	}
	want := []string{"p2 full", "p2 partial", "p5 full"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frames after Shown %q, want %q", got, want)
		}
	}
	r.None(fleet.Frame)
}

func TestManyPartialFramesBeforeTheLastFullOneRollBack(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.M("M").Push(viewOK(id, 2, "p2", "p5"))
	r.M("M").Push(frame("p2", 1, true, "full"))
	for i := range 513 {
		r.M("M").Push(frame("p2", uint64(i+2), false, "partial"))
	}
	notice(t, r, "M did not answer")
	r.Eventually("view.clear on M", func() bool { return clears(r, "M") == 1 })
	mode(t, r, "showing", "", 1)
}

// AG2
func TestPickingAnAgentShowsItsPane(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(session.Target{Machine: "M", Session: "s2", Tab: "t2", Pane: "p2"})
	id, vs := nextViewSet(t, r, "M", 0)
	if vs.Session != "s2" || vs.Tab != "t2" || vs.Pane != "p2" {
		t.Fatalf("view.set %+v, want session, tab and pane", vs)
	}
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "x"))
	if u := r.Next(fleet.Shown); u.Target != (session.Target{Machine: "M", Session: "s2", Tab: "t2", Pane: "p2"}) {
		t.Fatalf("shown %+v", u.Target)
	}
}

func TestPickingTheShownSessionWithAPaneSelectsIt(t *testing.T) {
	r := startShowing(t)
	r.F.Switch(session.Target{Session: "s1"})
	r.F.Switch(session.Target{Session: "s1", Tab: "t1", Pane: "p1"})
	id, vs := nextViewSet(t, r, "", 1)
	if vs.Session != "s1" || vs.Pane != "p1" {
		t.Fatalf("view.set %+v", vs)
	}
	if viewSets(r, "") != 2 {
		t.Fatalf("%d view.set, want none for the plain pick of the shown session", viewSets(r, "")-1)
	}
	r.M("").Push(viewOK(id, 1))
	if u := r.Next(fleet.Shown); u.Target.Pane != "p1" {
		t.Fatalf("shown %+v", u.Target)
	}
}

func TestAnInvalidTargetIsANoticeAndNothingElse(t *testing.T) {
	r := startShowing(t, "M connect")
	for _, tg := range []session.Target{
		{Machine: "nowhere", Session: "s2"},
		{Session: "x2"},
		{Session: "s2", Dir: "/a"},
		{},
		{Dir: "relative"},
		{Session: "s2", Pane: "t4"},
	} {
		r.F.Switch(tg)
		r.Next(fleet.Notice)
	}
	mode(t, r, "showing", "", 1)
	if viewSets(r, "M") != 0 || viewSets(r, "") != 1 || r.M("M").Dials() != 1 {
		t.Fatalf("an invalid target was acted on")
	}
}

// SW11
func TestALostMachineLeavesAStaleViewAndAUsableHub(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Drop()
	u := r.Next(fleet.Stale)
	if u.Target.Machine != "M" || u.Target.Session != "s2" || u.Problem.Reason != "lost" || u.Problem.Retry != "retrying" {
		t.Fatalf("stale %+v", u)
	}
	s := mode(t, r, "stale", "M", 2)
	if !s.Stale {
		t.Fatal("the summary does not say stale")
	}
	r.F.Input(key("p2", "x"))
	feed := r.OpenHub()
	feed.Next("", hub.Online)
	feed.Next("", hub.Sessions)
	r.F.HubClosed()
	r.F.Switch(target("", 1))
	s = r.Settle(func(s fleet.SummaryData) bool { return s.Mode == "switching" })
	if !s.Stale {
		t.Fatal("switching away from a stale view does not say stale")
	}
	id, _ := nextViewSet(t, r, "", 1)
	r.M("").Push(viewOK(id, 1))
	r.M("").Push(frame("p1", 1, true, "x"))
	r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Machine == "" })
	if s := mode(t, r, "showing", "", 1); s.Stale {
		t.Fatal("still stale after a local pick")
	}
	if got := keysOf(t, r, "M"); len(got) != 0 {
		t.Fatalf("keys reached M: %q", got)
	}
}

// reconnect brings M back after a drop: its retry is due in 0.5 s.
func reconnect(t *testing.T, r *fleettest.Rig, name string, snap proto.Snapshot) {
	t.Helper()
	r.M(name).Online(snap)
	r.Clock.WaitTimer(t, 500*time.Millisecond)
	r.Advance(500 * time.Millisecond)
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.StateOf(s, name) == "online" })
}

// SW12
func TestReconnectWithTheSameBootResumes(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Drop()
	r.Next(fleet.Stale)
	r.F.Input(key("p2", "typed while stale"))
	reconnect(t, r, "M", fleettest.Snapshot(ss(2)))
	id, vs := nextViewSet(t, r, "M", 1)
	if vs.Session != "s2" {
		t.Fatalf("view.set %+v", vs)
	}
	mode(t, r, "stale", "M", 2)
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "back"))
	r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Machine == "M" })
	if s := mode(t, r, "showing", "M", 2); s.Stale {
		t.Fatal("still stale")
	}
	if got := keysOf(t, r, "M"); len(got) != 0 {
		t.Fatalf("keys typed while stale were sent: %q", got)
	}
	r.None(fleet.Notice)
}

// SW13
func TestReconnectAfterARestartReopensByDirectory(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Drop()
	r.Next(fleet.Stale)
	w := r.M("M").Welcome()
	w.Boot = "boot-M-2"
	r.M("M").SetWelcome(w)
	reconnect(t, r, "M", fleettest.Snapshot(fleettest.Sess{N: 8, Dir: dir(2), Label: "p2-main"}))
	id, vs := nextViewSet(t, r, "M", 1)
	if vs.Dir != dir(2) || vs.Session != "" || vs.Name != "p2-main" {
		t.Fatalf("view.set %+v, want the directory", vs)
	}
	r.M("M").Push(viewOK(id, 8))
	r.M("M").Push(frame("p8", 1, true, "new shell"))
	if u := r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Machine == "M" }); u.Target.Session != "s8" {
		t.Fatalf("shown %+v", u.Target)
	}
	notice(t, r, "M restarted. Its shells are new.")
}

// SW14
func TestReconnectWithoutTheSessionFallsBack(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Drop()
	r.Next(fleet.Stale)
	reconnect(t, r, "M", fleettest.Snapshot(ss(3)))
	id, vs := nextViewSet(t, r, "", 1)
	if vs.Session != "s1" {
		t.Fatalf("view.set %+v, want the local session from the history", vs)
	}
	r.M("").Push(viewOK(id, 1))
	r.M("").Push(frame("p1", 1, true, "x"))
	r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Machine == "" })
	mode(t, r, "showing", "", 1)
	if viewSets(r, "M") != 1 {
		t.Fatal("M got a view.set for a session that is gone")
	}
}

func TestReconnectWithoutTheSessionAndNoHistoryOpensTheHub(t *testing.T) {
	r := fleettest.New(t, machines("M connect")...)
	r.M("").Online(fleettest.Snapshot())
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Drop()
	r.Next(fleet.Stale)
	reconnect(t, r, "M", fleettest.Snapshot())
	r.Next(fleet.OpenHub)
	r.Settle(func(s fleet.SummaryData) bool { return s.Mode == "none" && !s.Stale })
}

// SW15
func TestALocalServerCrashIsHandledLikeARemoteDrop(t *testing.T) {
	r := startShowing(t)
	r.M("").Drop()
	if u := r.Next(fleet.Stale); u.Target.Machine != "" || u.Target.Session != "s1" {
		t.Fatalf("stale %+v", u)
	}
	mode(t, r, "stale", "", 1)
	reconnect(t, r, "", fleettest.Snapshot(ss(1)))
	id, vs := nextViewSet(t, r, "", 1)
	if vs.Session != "s1" {
		t.Fatalf("view.set %+v", vs)
	}
	r.M("").Push(viewOK(id, 1))
	r.M("").Push(frame("p1", 1, true, "x"))
	r.Next(fleet.Shown)
	mode(t, r, "showing", "", 1)
}

// SW16
func TestTheDisplayedSessionEndingFallsBack(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Push(proto.Encode(proto.ViewEnded{Session: "s2", Dir: dir(2)}))
	id, vs := nextViewSet(t, r, "", 1)
	if vs.Session != "s1" {
		t.Fatalf("view.set %+v", vs)
	}
	r.M("").Push(viewOK(id, 1))
	r.M("").Push(frame("p1", 1, true, "x"))
	r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Machine == "" })
	mode(t, r, "showing", "", 1)
	// the local session ends too: nothing is left, the hub opens
	r.M("").Push(proto.Encode(proto.ViewEnded{Session: "s1", Dir: dir(1)}))
	r.Next(fleet.OpenHub)
	r.Settle(func(s fleet.SummaryData) bool { return s.Mode == "none" })
}

func TestAViewEndedOfAnotherSessionChangesNothing(t *testing.T) {
	r := startShowing(t)
	r.M("").Push(proto.Encode(proto.ViewEnded{Session: "s7", Dir: dir(7)}))
	r.None(fleet.OpenHub)
	mode(t, r, "showing", "", 1)
}

func TestARollbackFromNothingShownOpensTheHub(t *testing.T) {
	r := fleettest.New(t, machines("K")...)
	r.M("").Online(fleettest.Snapshot())
	r.F.Switch(target("K", 2))
	r.M("K").Fail(remote.Auth)
	r.Next(fleet.OpenHub)
	r.Settle(func(s fleet.SummaryData) bool { return s.Mode == "none" })
}

func TestCancelKeepsTheViewAndClearsTheTarget(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.F.Switch(target("M", 2))
	id, _ := nextViewSet(t, r, "M", 0)
	r.F.Input(key("p1", "a"))
	r.F.Cancel()
	mode(t, r, "showing", "", 1)
	r.Eventually("view.clear on M", func() bool { return clears(r, "M") == 1 })
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "x"))
	r.None(fleet.Shown)
	r.None(fleet.Notice)
	if got := keysOf(t, r, "M"); len(got) != 0 {
		t.Fatalf("keys on M %q", got)
	}
}

func TestTheWindowsMachineIsNotClosedWhenIdle(t *testing.T) {
	r := startShowing(t, "K")
	r.M("K").Online(fleettest.Snapshot(ss(2)))
	show(t, r, "K", 2)
	r.Advance(11 * time.Minute)
	time.Sleep(fleettest.Quiet)
	if !r.M("K").Live() {
		t.Fatal("the link of the shown machine was closed")
	}
	show(t, r, "", 1)
	r.Advance(10 * time.Minute)
	r.Eventually("K closed", func() bool { return !r.M("K").Live() })
}

func TestAResizeReachesTheShownServer(t *testing.T) {
	r := startShowing(t)
	r.F.Resize(120, 40)
	r.Eventually("view.resize", func() bool { return len(r.M("").SentOf(proto.TypeViewResize)) == 1 })
	var vr proto.ViewResize
	if err := r.M("").SentOf(proto.TypeViewResize)[0].Decode(&vr); err != nil || vr.Cols != 120 || vr.Rows != 40 {
		t.Fatalf("view.resize %+v %v", vr, err)
	}
}

func TestInputReachesTheShownPane(t *testing.T) {
	r := startShowing(t)
	r.F.Input(key("p1", "z"))
	r.Eventually("the key", func() bool { return len(keysOf(t, r, "")) == 1 })
	r.F.Input(proto.Encode(proto.Ping{}))
	time.Sleep(fleettest.Quiet)
	if n := len(r.M("").SentOf(proto.TypePing)); n != 0 {
		t.Fatal("Input sent something that is not input")
	}
}
