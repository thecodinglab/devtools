package fleet_test

import (
	"testing"

	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/proto"
)

// SW17, SEC11
func TestARoutedSwitchStaysOnTheMachineItCameFrom(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2), ss(3)))
	show(t, r, "M", 2)
	// a command in a pane of M asks for s3; the local machine has an s3
	// too, and the message cannot say which machine it means
	r.M("M").Push(proto.Encode(proto.UISwitch{Session: "s3"}))
	id, vs := nextViewSet(t, r, "M", 1)
	if vs.Session != "s3" {
		t.Fatalf("view.set %+v", vs)
	}
	if viewSets(r, "") != 1 {
		t.Fatal("the routed switch reached the local server")
	}
	r.M("M").Push(viewOK(id, 3))
	r.M("M").Push(frame("p3", 1, true, "x"))
	if u := r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Session == "s3" }); u.Target.Machine != "M" {
		t.Fatalf("shown %+v", u.Target)
	}
}

func TestARoutedSwitchByDirectoryOpensThere(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Push(proto.Encode(proto.UISwitch{Dir: dir(4), Name: "p4-main"}))
	_, vs := nextViewSet(t, r, "M", 1)
	if vs.Dir != dir(4) || vs.Name != "p4-main" {
		t.Fatalf("view.set %+v", vs)
	}
}

// SEC11
func TestRoutedRequestsOfABackgroundMachineAreDropped(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.M("M").Push(proto.Encode(proto.UISwitch{Session: "s2"}))
	r.M("M").Push(proto.Encode(proto.UIHub{Mode: proto.HubAgents}))
	// a routed copy never has an id: one with an id is not honoured either
	r.M("").Push(proto.Request(5, proto.UIHub{Mode: proto.HubAll}))
	r.None(fleet.OpenHub)
	if viewSets(r, "M") != 0 || viewSets(r, "") != 1 {
		t.Fatal("a routed request of a machine that is not shown was honoured")
	}
	mode(t, r, "showing", "", 1)
}

func TestARoutedHubOpensTheHubWithTheMachineOfTheLink(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	r.M("M").Push(proto.Encode(proto.UIHub{Mode: proto.HubAgents, Query: "fix\x1b]52;c;x\x07", Here: dir(2), Pane: "p2"}))
	u := r.Next(fleet.OpenHub)
	if u.Hub.Mode != hub.ModeAgents || u.Hub.Here.Machine != "M" || u.Hub.Here.Dir != dir(2) {
		t.Fatalf("hub request %+v", u.Hub)
	}
	if u.Hub.Query != "fix]52;c;x" {
		t.Fatalf("query %q is not sanitised", u.Hub.Query)
	}
	for m, want := range map[string]hub.Mode{proto.HubAll: hub.ModeAll, proto.HubOpen: hub.ModeSessions, proto.HubNew: hub.ModeNew} {
		r.M("M").Push(proto.Encode(proto.UIHub{Mode: m}))
		if u := r.Next(fleet.OpenHub); u.Hub.Mode != want {
			t.Fatalf("mode %q gave %v", m, u.Hub.Mode)
		}
	}
	r.M("M").Push(proto.Encode(proto.UIHub{Mode: "everything"}))
	r.None(fleet.OpenHub)
}

func TestRoutedRequestsOfAStaleViewAreNotHonoured(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	// a switch to the local machine is pending: M's link still shows the
	// view, so a routed hub from it is honoured, one from the local link not
	r.F.Switch(target("", 1))
	nextViewSet(t, r, "", 1)
	r.M("").Push(proto.Encode(proto.UIHub{Mode: proto.HubAll}))
	r.None(fleet.OpenHub)
	r.M("M").Push(proto.Encode(proto.UIHub{Mode: proto.HubAll}))
	r.Next(fleet.OpenHub)
}

// SEC15
func TestClipboardOfABackgroundMachineIsDropped(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	r.M("M").Push(proto.Encode(proto.Clipboard{Pane: "p2", Data: []byte("from M")}))
	r.M("M").Push(proto.Encode(proto.Bell{Pane: "p2"}))
	r.None(fleet.Clipboard)
	r.None(fleet.Bell)
	r.M("").Push(proto.Encode(proto.Clipboard{Pane: "p1", Data: []byte("from here")}))
	if u := r.Next(fleet.Clipboard); string(u.Data) != "from here" {
		t.Fatalf("clipboard %q", u.Data)
	}
	r.M("").Push(proto.Encode(proto.Clipboard{Pane: "p1", Data: nil}))
	r.None(fleet.Clipboard)
	r.M("").Push(proto.Encode(proto.Bell{Pane: "p1"}))
	u := r.Next(fleet.Bell)
	var b proto.Bell
	if u.Frame.T != proto.TypeEventBell || u.Frame.Decode(&b) != nil || b.Pane != "p1" {
		t.Fatalf("bell %+v: a program's bell names its pane", u)
	}
}

func TestARequestGoesToTheShownServerAndItsReplyComesBack(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	reply := r.F.Request(proto.Request(41, proto.TabNew{Session: "s2"}))
	r.Eventually("tab.new on M", func() bool { return len(r.M("M").SentOf(proto.TypeTabNew)) == 1 })
	sent := r.M("M").SentOf(proto.TypeTabNew)[0]
	var tn proto.TabNew
	if err := sent.Decode(&tn); err != nil || tn.Session != "s2" || sent.ID == 0 {
		t.Fatalf("tab.new %+v %v", sent, err)
	}
	if len(r.M("").SentOf(proto.TypeTabNew)) != 0 {
		t.Fatal("the request reached the local server")
	}
	r.M("M").Push(proto.OKReply(sent.ID, proto.TabNewResult{Tab: "t9", Pane: "p9"}))
	got := <-reply
	var res proto.TabNewResult
	if got.T != proto.TypeOK || got.ID != 41 || got.Decode(&res) != nil || res.Pane != "p9" {
		t.Fatalf("reply %+v", got)
	}
}

func TestAnErrReplyIsSanitised(t *testing.T) {
	r := startShowing(t)
	reply := r.F.Request(proto.Request(3, proto.PaneClose{Pane: "p1"}))
	r.Eventually("pane.close", func() bool { return len(r.M("").SentOf(proto.TypePaneClose)) == 1 })
	id := r.M("").SentOf(proto.TypePaneClose)[0].ID
	r.M("").Push(proto.ErrReply(id, proto.CodeNoPane, "no\x1b[31m pane"))
	rep, ok := proto.AsReply(<-reply)
	if !ok || rep.OK || rep.Code != proto.CodeNoPane || rep.Text != "no[31m pane" || rep.Msg.ID != 3 {
		t.Fatalf("reply %+v", rep)
	}
}

func TestRequestsInFlightAreAnsweredLostAndNotResent(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2)))
	show(t, r, "M", 2)
	reply := r.F.Request(proto.Request(7, proto.TabNew{Session: "s2"}))
	r.Eventually("tab.new on M", func() bool { return len(r.M("M").SentOf(proto.TypeTabNew)) == 1 })
	r.M("M").Drop()
	rep, ok := proto.AsReply(<-reply)
	if !ok || rep.OK || rep.Code != proto.CodeLost || rep.Msg.ID != 7 {
		t.Fatalf("reply %+v, want err lost with the request's id", rep)
	}
	reconnect(t, r, "M", fleettest.Snapshot(ss(2)))
	nextViewSet(t, r, "M", 1)
	if n := len(r.M("M").SentOf(proto.TypeTabNew)); n != 1 {
		t.Fatalf("tab.new was sent %d times", n)
	}
	// a request while nothing is shown, and one while stale, are lost at once
	r.M("M").Drop()
	r.Next(fleet.Stale)
	rep, _ = proto.AsReply(<-r.F.Request(proto.Request(8, proto.TabNew{Session: "s2"})))
	if rep.OK || rep.Code != proto.CodeLost || rep.Msg.ID != 8 {
		t.Fatalf("reply %+v", rep)
	}
}

func TestTheWindowsOwnMessagesAreNoRequests(t *testing.T) {
	r := startShowing(t)
	for _, m := range []proto.Msg{
		proto.Request(1, proto.ViewSet{Session: "s1"}),
		proto.Encode(proto.Key{Pane: "p1", Key: "a"}),
		proto.Request(2, proto.UISwitch{Session: "s1"}),
	} {
		rep, _ := proto.AsReply(<-r.F.Request(m))
		if rep.OK || rep.Code != proto.CodeInvalid {
			t.Fatalf("%s: reply %+v", m.T, rep)
		}
	}
	if viewSets(r, "") != 1 {
		t.Fatal("a view.set went out through Request")
	}
}

func TestSteppingStaysOnTheDisplayedMachine(t *testing.T) {
	r := startShowing(t, "M connect")
	online(t, r, "M", fleettest.Snapshot(ss(2), ss(3)))
	show(t, r, "M", 3)
	r.F.Step(+1)
	id, vs := nextViewSet(t, r, "M", 1)
	if vs.Session != "s2" {
		t.Fatalf("session-next from the last session of M went to %+v, want M's first", vs)
	}
	r.M("M").Push(viewOK(id, 2))
	r.M("M").Push(frame("p2", 1, true, "x"))
	r.NextWhere(fleet.Shown, func(u fleet.Update) bool { return u.Target.Session == "s2" })
	r.F.Step(-1)
	_, vs = nextViewSet(t, r, "M", 2)
	if vs.Session != "s3" {
		t.Fatalf("session-prev went to %+v", vs)
	}
	if viewSets(r, "") != 1 {
		t.Fatal("stepping reached another machine")
	}
	// the step is a switch like any other, to M's other session
	mode(t, r, "switching", "M", 3)
	// a second step while it runs drops it, which sets the shown view
	// again on the shared link, and steps from the shown view
	r.F.Step(+1)
	_, vs = nextViewSet(t, r, "M", 3)
	if vs.Session != "s2" {
		t.Fatalf("view.set %+v, want the shown view set again", vs)
	}
	_, vs = nextViewSet(t, r, "M", 4)
	if vs.Session != "s3" {
		t.Fatalf("session-next from s2 while a switch ran went to %+v", vs)
	}
}

func TestSteppingWithOneSessionDoesNothing(t *testing.T) {
	r := startShowing(t)
	r.F.Step(1)
	r.None(fleet.Notice)
	if viewSets(r, "") != 1 {
		t.Fatal("a step with one session sent a view.set")
	}
}
