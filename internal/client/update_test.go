package client_test

import (
	"image/color"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/clienttest"
	"devtools/internal/client/screen"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

func TestAnInvalidFrameDropsTheLinkAndShowsANotice(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"}, "good")
	d.Update(frame("p1", 2, 80, 23, false, "\x1b[31mnot the grammar"))
	st := client.ChromeStateOf(d.M)
	if want := ui.Notice("bad-frame", "desktop"); st.Notice != want {
		t.Fatalf("notice %q, want %q", st.Notice, want)
	}
	// the link of the shown target is dropped, once
	dr := d.F.CallsOf("Drop")
	if len(dr) != 1 || dr[0].Target.Machine != "" || dr[0].Target.Session != "s1" {
		t.Fatalf("Drop calls %+v, want one for the shown target", dr)
	}
	if sw := d.F.CallsOf("Switch"); len(sw) != 0 {
		t.Fatalf("Switch calls %+v, want none", sw)
	}
	// nothing more of the broken view is applied
	d.Update(frame("p1", 3, 80, 23, false, "\x1b[31mbad again"))
	d.Update(frame("p1", 3, 80, 23, false, "after"))
	if d.Screen().Contains("after") {
		t.Fatal("a frame after the invalid one was applied")
	}
	if dr := d.F.CallsOf("Drop"); len(dr) != 1 {
		t.Fatalf("Drop calls %+v, want still one", dr)
	}
	// the fleet turns the view stale, then shows it again after the
	// reconnect: the view that follows starts again from full frames
	d.Update(fleet.Update{Kind: fleet.Stale})
	s := snapshot("", proto.Pane{ID: "p1"})
	d.Update(shown("", s))
	d.Update(frame("p1", 1, 80, 23, true, "fresh"))
	if !d.Screen().Contains("fresh") {
		t.Fatal("the new view did not draw")
	}
}

func TestAFrameOfANewTabWaitsForTheState(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"}, "tab one")
	// another client selected tab t2: the frames of its pane arrive first
	s := snapshot("", proto.Pane{ID: "p1"})
	s.Sessions[0].Tabs = []string{"t1", "t2"}
	s.Sessions[0].Current = "t2"
	s.Tabs = append(s.Tabs, proto.Tab{ID: "t2", Session: "s1", Index: 2, Panes: []string{"p7"}, Focused: "p7"})
	s.Panes = append(s.Panes, proto.Pane{ID: "p7", Tab: "t2", Session: "s1", State: proto.PaneRunning})
	s.Rev = 2
	d.F.SetState(s)
	d.Update(frame("p7", 1, 80, 23, true, "tab two"))
	if !d.Screen().Contains("tab two") {
		t.Fatalf("the frame of the new tab was not drawn after the state was read:\n%s", d.Screen().Plain())
	}
	if st := client.ChromeStateOf(d.M); st.Notice != "" {
		t.Fatalf("notice %q, want none: the frame was valid", st.Notice)
	}
	d.Type("x")
	if in := d.F.Inputs(); len(in) != 1 || keyOf(t, in[0]).Pane != "p7" {
		t.Fatalf("inputs %v, want x to p7", in)
	}
}

func TestTypeaheadAfterNewTabGoesToTheNewTab(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	d.Key("c")
	req := lastRequest(t, d.F)
	if req.T != proto.TypeTabNew {
		t.Fatalf("request %s, want tab.new", req.T)
	}
	d.Type("ls")
	d.Key("enter")
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("typeahead reached a pane before the reply: %v", in)
	}
	// the server: delta (now in the state), then the reply (W2)
	s := snapshot("", proto.Pane{ID: "p1"})
	s.Sessions[0].Tabs = []string{"t1", "t2"}
	s.Sessions[0].Current = "t2"
	s.Tabs = append(s.Tabs, proto.Tab{ID: "t2", Session: "s1", Index: 2, Panes: []string{"p2"}, Focused: "p2"})
	s.Panes = append(s.Panes, proto.Pane{ID: "p2", Tab: "t2", Session: "s1", State: proto.PaneRunning})
	s.Rev = 2
	d.F.SetState(s)
	d.Reply(proto.OKReply(req.ID, proto.TabNewResult{Tab: "t2", Pane: "p2"}))
	in := d.F.Inputs()
	if len(in) != 3 {
		t.Fatalf("inputs %v, want l, s, enter", in)
	}
	for i, want := range []string{"l", "s", "enter"} {
		if k := keyOf(t, in[i]); k.Pane != "p2" || k.Key != want {
			t.Errorf("input %d = %+v, want %s to p2", i, k, want)
		}
	}
}

func TestTheTypeaheadGateGivesUpAfterTwoSeconds(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	d.Key("%")
	d.Type("ab")
	d.Advance(2*time.Second - time.Millisecond)
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("keys left the gate before 2 s: %v", in)
	}
	d.Advance(time.Millisecond)
	in := d.F.Inputs()
	if len(in) != 2 || keyOf(t, in[0]).Key != "a" || keyOf(t, in[1]).Pane != "p1" {
		t.Fatalf("inputs %v, want a and b to p1 after 2 s", in)
	}
	// the late reply opens nothing and sends nothing again
	d.Reply(proto.OKReply(lastRequest(t, d.F).ID, proto.PaneSplitResult{Pane: "p2"}))
	if got := len(d.F.Inputs()); got != 2 {
		t.Fatalf("%d inputs after the late reply, want 2", got)
	}
	d.Type("c")
	if got := len(d.F.Inputs()); got != 3 {
		t.Fatalf("typing after the gate: %d inputs, want 3", got)
	}
}

func TestFocusAndBlurReachTheFleet(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Send(tea.BlurMsg{})
	d.Send(tea.FocusMsg{})
	calls := d.F.CallsOf("Focus")
	if len(calls) != 2 || calls[0].Focused || !calls[1].Focused {
		t.Fatalf("Focus calls %+v, want false then true", calls)
	}
	if st := client.ChromeStateOf(d.M); !st.WindowFocused {
		t.Fatal("the chrome does not see the focus")
	}
	d.Send(tea.BlurMsg{})
	if st := client.ChromeStateOf(d.M); st.WindowFocused {
		t.Fatal("the chrome does not see the blur")
	}
}

func TestAColourAnswerReachesTheFleetAndSetsDark(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Send(tea.ForegroundColorMsg{Color: color.RGBA{0x1a, 0x1b, 0x26, 0xff}})
	d.Send(tea.BackgroundColorMsg{Color: color.RGBA{0xe1, 0xe2, 0xe7, 0xff}})
	calls := d.F.CallsOf("Colors")
	if len(calls) != 1 {
		t.Fatalf("Colors calls %+v, want one", calls)
	}
	var c proto.ViewColors
	if calls[0].Msg.T != proto.TypeViewColors || calls[0].Msg.Decode(&c) != nil {
		t.Fatalf("Colors got %s", calls[0].Msg.Body)
	}
	if c != (proto.ViewColors{Fg: "#1a1b26", Bg: "#e1e2e7", Dark: false}) {
		t.Fatalf("view.colors %+v", c)
	}
	light, _ := ui.NewTheme(ui.ThemeOptions{Known: true, Dark: false, Colors: 16, UTF8: true})
	if st := client.ChromeStateOf(d.M); st.Theme != light {
		t.Fatalf("theme %+v, want the light theme of a known background", st.Theme)
	}
	// the same answer again (a focus gain) sends nothing
	d.Send(tea.BackgroundColorMsg{Color: color.RGBA{0xe1, 0xe2, 0xe7, 0xff}})
	if got := len(d.F.CallsOf("Colors")); got != 1 {
		t.Fatalf("%d Colors calls after the same answer, want 1", got)
	}
	d.Send(tea.BackgroundColorMsg{Color: color.RGBA{0x1a, 0x1b, 0x26, 0xff}})
	calls = d.F.CallsOf("Colors")
	if len(calls) != 2 || calls[1].Msg.Decode(&c) != nil || !c.Dark || c.Bg != "#1a1b26" {
		t.Fatalf("after a dark answer: %+v", calls)
	}
	dark, _ := ui.NewTheme(ui.ThemeOptions{Known: true, Dark: true, Colors: 16, UTF8: true})
	if st := client.ChromeStateOf(d.M); st.Theme != dark {
		t.Fatal("the theme did not turn dark")
	}
}

func TestTheFirstResizeIsSentAtOnceThenDebounced(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	rs := d.F.CallsOf("Resize")
	if len(rs) != 1 || rs[0].Cols != 80 || rs[0].Rows != 23 {
		t.Fatalf("Resize calls %+v, want 80 by 23 at once", rs)
	}
	d.Advance(10 * time.Millisecond)
	d.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.Advance(10 * time.Millisecond)
	d.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
	if rs := d.F.CallsOf("Resize"); len(rs) != 1 {
		t.Fatalf("Resize calls %+v inside 50 ms, want still one", rs)
	}
	d.Advance(30 * time.Millisecond)
	rs = d.F.CallsOf("Resize")
	if len(rs) != 2 || rs[1].Cols != 120 || rs[1].Rows != 39 {
		t.Fatalf("Resize calls %+v, want the newest size once the 50 ms passed", rs)
	}
	d.Advance(time.Second)
	d.Send(tea.WindowSizeMsg{Width: 90, Height: 20})
	if rs := d.F.CallsOf("Resize"); len(rs) != 3 || rs[2].Cols != 90 || rs[2].Rows != 19 {
		t.Fatalf("a change after a quiet second: %+v, want it at once", rs)
	}
}

func TestANoticeDisappearsAfterItsTime(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Update(fleet.Update{Kind: fleet.Notice, Notice: "macmini restarted. Its shells are new."})
	if !d.Screen().Contains("macmini restarted.") {
		t.Fatalf("the notice is not shown:\n%s", d.Screen().Plain())
	}
	d.Advance(ui.NoticeLife - time.Millisecond)
	if !d.Screen().Contains("macmini restarted.") {
		t.Fatal("the notice left before its time")
	}
	d.Advance(time.Millisecond)
	if d.Screen().Contains("macmini restarted.") {
		t.Fatal("the notice is still shown after its time")
	}
}

func TestABellIsWrittenOnce(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Update(fleet.Update{Kind: fleet.Bell, Frame: proto.Encode(proto.Bell{Pane: "p1"})})
	d.Screen()
	d.Update(frame("p1", 2, 80, 23, false, "more"))
	d.Screen()
	if raw := d.Raw(); len(raw) != 1 || raw[0] != "\a" {
		t.Fatalf("written past the renderer: %q, want one BEL", raw)
	}
	// an agent's bell rings as well
	d.Update(fleet.Update{Kind: fleet.Bell})
	if raw := d.Raw(); len(raw) != 2 {
		t.Fatalf("written: %q, want a second BEL for the agent", raw)
	}
	// a program's bell in a pane that is not on screen marks its tab
	s := snapshot("", proto.Pane{ID: "p1"})
	s.Sessions[0].Tabs = []string{"t1", "t2"}
	s.Tabs = append(s.Tabs, proto.Tab{ID: "t2", Session: "s1", Index: 2, Panes: []string{"p9"}, Focused: "p9"})
	s.Panes = append(s.Panes, proto.Pane{ID: "p9", Tab: "t2", Session: "s1", State: proto.PaneRunning})
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(s)})
	d.Update(fleet.Update{Kind: fleet.Bell, Frame: proto.Encode(proto.Bell{Pane: "p9"})})
	if raw := d.Raw(); len(raw) != 2 {
		t.Fatalf("written: %q, want no BEL for a pane that is not on screen", raw)
	}
	tabs := client.ChromeStateOf(d.M).Tabs
	if len(tabs) != 2 || tabs[0].Bell || !tabs[1].Bell {
		t.Fatalf("tabs %+v, want the bell on tab 2", tabs)
	}
}

func TestARunningClientKeepsTheLocalLinkRetryingWhenItsServerGoesAway(t *testing.T) {
	var opened []*fakeOverlay
	d := showOne(t, client.Options{OpenHub: fakeHub(&opened)}, proto.Pane{ID: "p1"}, "", "work")
	d.Update(fleet.Update{Kind: fleet.Stale, Target: session.Target{Session: "s1"}, Problem: hub.Problem{Reason: "no-server", Retry: "retrying"}})
	retrying := online(fleet.MachineState{Name: "macmini", Host: "macmini", State: "online"})
	retrying.Summary.Machines[0].State = "retrying"
	retrying.Summary.Machines[0].Problem = hub.Problem{Reason: "no-server", Retry: "retrying"}
	retrying.Summary.Mode, retrying.Summary.Stale = "stale", true
	d.Update(retrying)
	if d.Quit() {
		t.Fatal("the client ended when its server went away")
	}
	if st := client.ChromeStateOf(d.M); st.View != "stale" {
		t.Fatalf("view %q, want stale", st.View)
	}
	sc := d.Screen()
	if !sc.Contains("work") {
		t.Fatal("the last picture is gone")
	}
	// typing is dropped
	d.Type("x")
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("typing reached the lost machine: %v", in)
	}
	// the hub opens and the other machines stay usable
	d.Update(fleet.Update{Kind: fleet.OpenHub, Hub: fleet.HubRequest{Mode: hub.ModeAll}})
	if len(opened) != 1 || len(d.F.CallsOf("HubOpened")) != 1 {
		t.Fatalf("hubs %d, HubOpened calls %d; want the hub open", len(opened), len(d.F.CallsOf("HubOpened")))
	}
	if !d.Screen().Contains("HUB all") {
		t.Fatalf("the hub is not drawn:\n%s", d.Screen().Plain())
	}
	other := session.Target{Machine: "macmini", Session: "s3"}
	opened[0].pick = &other
	d.Key("enter")
	sw := d.F.CallsOf("Switch")
	if len(sw) != 1 || sw[0].Target != other {
		t.Fatalf("Switch calls %+v, want the pick on macmini", sw)
	}
	if len(d.F.CallsOf("HubClosed")) != 1 || d.Quit() {
		t.Fatal("the hub did not close, or the client ended")
	}
}

func TestTheHubGetsKeysAndItsSizeAndClosingItWithNothingShownEnds(t *testing.T) {
	var opened []*fakeOverlay
	d := clienttest.New(t, client.Options{OpenHub: fakeHub(&opened), Hub: &fleet.HubRequest{Mode: hub.ModeSessions}})
	if len(opened) != 1 || opened[0].req.Mode != hub.ModeSessions {
		t.Fatalf("hubs %+v, want the sessions hub at start", opened)
	}
	if o := opened[0]; o.w != 80 || o.h != 23 {
		t.Fatalf("hub size %dx%d, want the pane area 80x23", o.w, o.h)
	}
	d.Type("dev")
	d.Key("ctrl+j")
	keysGot := 0
	for _, m := range opened[0].got {
		if _, ok := m.(tea.KeyPressMsg); ok {
			keysGot++
		}
	}
	if keysGot != 4 || len(d.F.Requests()) != 0 || len(d.F.Inputs()) != 0 {
		t.Fatalf("the hub got %d keys (want 4, ctrl+j included); requests %v inputs %v", keysGot, d.F.Requests(), d.F.Inputs())
	}
	if sc := d.Screen(); !sc.CursorVisible || sc.CursorX != 4 || sc.CursorY != 0 {
		t.Fatalf("cursor %d,%d visible %v, want the hub's", sc.CursorX, sc.CursorY, sc.CursorVisible)
	}
	d.Key("esc")
	if !d.Quit() {
		t.Fatal("closing the hub with no session shown did not end the client")
	}
}

// --- stale and switching display (P1-17b) ---

// switching is the summary of a switch to s9 on macmini.
func switching() fleet.Update {
	u := online(fleet.MachineState{Name: "macmini", Host: "macmini", State: "connecting"})
	u.Summary.Mode = "switching"
	u.Summary.Target = session.Target{Machine: "macmini", Session: "s9", Name: "x-main"}
	return u
}

func TestTheOldViewIsDrawnInactiveWhileASwitchIsPending(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"}, "\x1b[0;1mbold\x1b[0m text", "row 1")
	d.Screen()
	if st := client.CanvasOf(d.M).At(0, 0).Style; st.Attrs&screen.Bold == 0 {
		t.Fatalf("cell 0,0 is %+v before the switch, want bold", st)
	}
	d.Update(switching())
	sc := d.Screen()
	inactive := chrome.Plain{}.Inactive(client.ChromeStateOf(d.M), screen.Style{Attrs: screen.Bold})
	if st := client.CanvasOf(d.M).At(0, 0).Style; st != inactive {
		t.Errorf("cell 0,0 is %+v while the switch is pending, want the inactive style %+v", st, inactive)
	}
	if sc.Row(0) != "bold text" || sc.Row(1) != "row 1" {
		t.Errorf("rows %q, %q: the old view stays on screen, without a banner", sc.Row(0), sc.Row(1))
	}
	if v := d.View(); v.Cursor != nil {
		t.Error("the cursor of the old view is shown while a switch is pending")
	}
	// a frame of the old view keeps updating it, inactive
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(proto.Frame{Pane: "p1", Seq: 2, Cols: 80, Rows: 23,
		Lines: []proto.Line{{Y: 1, S: "row 1 new"}}, Cursor: proto.Cursor{Visible: true}})})
	if sc := d.Screen(); sc.Row(1) != "row 1 new" {
		t.Errorf("row 1 %q, want the old view's new output", sc.Row(1))
	}
	if st := client.CanvasOf(d.M).At(0, 1).Style; st != (chrome.Plain{}).Inactive(client.ChromeStateOf(d.M), screen.Style{}) {
		t.Errorf("the updated row is drawn %+v, want inactive", st)
	}
	// a rollback: the view is active again
	d.Update(online())
	d.Screen()
	if st := client.CanvasOf(d.M).At(0, 0).Style; st.Attrs&screen.Bold == 0 {
		t.Errorf("cell 0,0 is %+v after the rollback, want bold again", st)
	}
}

func TestTheStaleBannerCoversTheTopRowWithoutAResize(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"}, "row 0", "row 1", "row 2")
	d.F.Reset()
	d.Update(fleet.Update{Kind: fleet.Stale, Problem: hub.Problem{Reason: "lost"}})
	sc := d.Screen()
	if !sc.Contains(ui.BannerPaused) || sc.Contains("row 0") {
		t.Errorf("row 0 %q, want the banner over it", sc.Row(0))
	}
	if sc.Row(1) != "row 1" || sc.Row(2) != "row 2" {
		t.Errorf("rows %q, %q; want the panes where they were", sc.Row(1), sc.Row(2))
	}
	if rs := d.F.CallsOf("Resize"); len(rs) != 0 {
		t.Errorf("resizes %v, want none: the banner never resizes a pane", rs)
	}
	if st := client.ChromeStateOf(d.M); st.View != "stale" {
		t.Errorf("view %q, want stale", st.View)
	}
	d.Type("x")
	if in := d.F.Inputs(); len(in) != 0 {
		t.Errorf("inputs %v, want none: typing is paused", in)
	}
}

func TestKeysTypedWhileSwitchingGoToTheFleet(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
	d.Update(switching())
	d.F.Reset()
	d.Type("ls")
	d.Send(tea.PasteMsg{Content: "x"})
	in := d.F.Inputs()
	if len(in) != 3 || keyOf(t, in[0]).Key != "l" || keyOf(t, in[1]).Key != "s" || in[2].T != proto.TypeInputPaste {
		t.Fatalf("inputs %v, want l, s and the paste handed to the fleet", in)
	}

	t.Run("with nothing displayed yet", func(t *testing.T) {
		d := clienttest.New(t, client.Options{})
		d.Update(switching())
		d.Type("a")
		in := d.F.Inputs()
		if len(in) != 1 || keyOf(t, in[0]).Key != "a" {
			t.Fatalf("inputs %v, want the key handed to the fleet", in)
		}
	})
}

func TestTheSwitchingModeShowsOnlyAfterTheSlowDelay(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
	d.Update(switching())
	if modeOf(d) != "normal" {
		t.Fatalf("mode %q at once, want normal", modeOf(d))
	}
	d.Advance(ui.Slow - time.Millisecond)
	if modeOf(d) != "normal" {
		t.Fatalf("mode %q before %v, want normal", modeOf(d), ui.Slow)
	}
	d.Advance(time.Millisecond)
	st := client.ChromeStateOf(d.M)
	if st.Mode != "switching" || st.Target != (chrome.Target{Machine: "macmini", Session: "x-main"}) {
		t.Fatalf("mode %q target %+v, want switching to macmini x-main", st.Mode, st.Target)
	}
	if sc := d.Screen(); !sc.Contains("switching") {
		t.Errorf("status %q, want the switching row", sc.Row(23))
	}
	// a switch that ends before the delay never shows it
	d = showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
	d.Update(switching())
	d.Advance(100 * time.Millisecond)
	d.Update(online())
	d.Advance(time.Second)
	if modeOf(d) != "normal" {
		t.Errorf("mode %q, want normal: the switch ended before %v", modeOf(d), ui.Slow)
	}
}

// stateGets are the state.get requests the fake fleet saw.
func stateGets(f *clienttest.Fleet) []proto.Msg {
	var out []proto.Msg
	for _, c := range f.Calls() {
		if c.Name == "Request" && c.Msg.T == proto.TypeStateGet {
			out = append(out, c.Msg)
		}
	}
	return out
}

// Two servers count their ids independently, so machine A and machine B
// can both have s1, t1, p1 and p2. A state read asked while A was shown
// and answered after B's view committed is A's state: it must not replace
// B's, or keys typed on B would go to the pane A focuses.
func TestAStateReadOfThePreviousMachineIsIgnored(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	a := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	a.Sessions[0].Label = "machine-A"
	d.Update(online())
	d.Update(shown("", a))
	gets := stateGets(d.F)
	if len(gets) != 1 {
		t.Fatalf("%d state reads after the first view, want 1", len(gets))
	}
	readA := gets[0]

	b := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	b.Tabs[0].Focused = "p2"
	b.Sessions[0].Label = "machine-B"
	d.Update(shown("macmini", b))
	if got := len(stateGets(d.F)); got != 2 {
		t.Fatalf("%d state reads after B's view committed, want 2: B reads its own state without waiting for A's", got)
	}
	readB := stateGets(d.F)[1]
	if !d.F.Answer(proto.Request(readB.ID, b)) {
		t.Fatal("B's state read does not wait")
	}
	d.Send(nil)
	d.Update(frame("p1", 1, 40, 23, true))
	d.Update(frame("p2", 1, 39, 23, true))
	d.Type("x")
	in := d.F.Inputs()
	if len(in) == 0 || keyOf(t, in[len(in)-1]).Pane != "p2" {
		t.Fatal("setup: the key on B does not reach p2")
	}

	d.F.Reset()
	if !d.F.Answer(proto.Request(readA.ID, a)) {
		t.Fatal("A's state read no longer waits")
	}
	d.Send(nil)
	d.Type("y")
	in = d.F.Inputs()
	if len(in) == 0 {
		t.Fatal("the key was dropped")
	}
	if got := keyOf(t, in[len(in)-1]).Pane; got != "p2" {
		t.Fatalf("after A's late state the key on B went to %s, want p2", got)
	}
	if got := d.View().WindowTitle; got != "macmini: machine-B" {
		t.Fatalf("title %q, want B's session", got)
	}
}

// A state.delta forwarded while a state.get runs makes the displayed state
// newer than the snapshot the read then returns; the older snapshot does
// not replace it.
func TestAnOlderStateReadDoesNotReplaceANewerState(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	old := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	d.Update(online())
	d.Update(shown("", old))
	gets := stateGets(d.F)
	if len(gets) != 1 {
		t.Fatalf("%d state reads, want 1", len(gets))
	}
	newer := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	newer.Rev = 2
	newer.Tabs[0].Focused = "p2"
	d.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(newer)})
	d.Update(frame("p1", 1, 40, 23, true))
	d.Update(frame("p2", 1, 39, 23, true))
	if !d.F.Answer(proto.Request(gets[0].ID, old)) {
		t.Fatal("the state read does not wait")
	}
	d.Send(nil)
	d.F.Reset()
	d.Type("y")
	in := d.F.Inputs()
	if len(in) == 0 {
		t.Fatal("the key was dropped")
	}
	if got := keyOf(t, in[len(in)-1]).Pane; got != "p2" {
		t.Fatalf("the key went to %s after an older snapshot arrived, want p2 of revision 2", got)
	}
}
