package client_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/clienttest"
	"devtools/internal/client/keys"
	"devtools/internal/client/screen"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"
)

// fakeOverlay is a hub that records what it gets; enter picks pick (or
// closes without a choice when pick is nil), esc closes.
type fakeOverlay struct {
	req  fleet.HubRequest
	got  []tea.Msg
	pick *session.Target
	w, h int
}

func (o *fakeOverlay) Init() tea.Cmd { return nil }

func (o *fakeOverlay) Update(msg tea.Msg) (tea.Cmd, *chrome.Outcome) {
	o.got = append(o.got, msg)
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		o.w, o.h = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.Code {
		case tea.KeyEnter:
			return nil, &chrome.Outcome{Switch: o.pick}
		case tea.KeyEscape:
			return nil, &chrome.Outcome{}
		}
	}
	return nil, nil
}

func (o *fakeOverlay) Bounds(w, h int) screen.Rect { return screen.Rect{W: w, H: h} }

func (o *fakeOverlay) Draw(c *screen.Canvas) {
	c.Text(0, 0, "HUB "+modeName(o.req.Mode), screen.Style{}, 40)
}

func (o *fakeOverlay) Cursor() (x, y int, visible bool) { return 4, 0, true }

func modeName(m hub.Mode) string {
	switch m {
	case hub.ModeSessions:
		return "sessions"
	case hub.ModeAgents:
		return "agents"
	case hub.ModeNew:
		return "new"
	}
	return "all"
}

// fakeHub is an OpenHub that makes fakeOverlays and keeps them in *opened.
func fakeHub(opened *[]*fakeOverlay) func(req fleet.HubRequest, events <-chan hub.Event, theme ui.Theme) chrome.Overlay {
	return func(req fleet.HubRequest, events <-chan hub.Event, theme ui.Theme) chrome.Overlay {
		o := &fakeOverlay{req: req}
		if opened != nil {
			*opened = append(*opened, o)
		}
		return o
	}
}

// lastRequest is the newest request of the client but state.get.
func lastRequest(t *testing.T, f *clienttest.Fleet) proto.Msg {
	t.Helper()
	rs := f.Requests()
	if len(rs) == 0 {
		t.Fatal("no request was sent")
	}
	return rs[len(rs)-1]
}

func TestKeysGoToTheFocusedPane(t *testing.T) {
	d := clienttest.New(t, client.Options{})
	s := snapshot(proto.SplitRight, proto.Pane{ID: "p1"}, proto.Pane{ID: "p2"})
	s.Tabs[0].Focused = "p2"
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frame("p1", 1, 40, 23, true))
	d.Update(frame("p2", 1, 39, 23, true))
	d.Type("aB")
	d.Key("enter")
	d.Key("alt+b")
	in := d.F.Inputs()
	if len(in) != 4 {
		t.Fatalf("inputs %v, want four keys", in)
	}
	for i, want := range []proto.Key{
		{Pane: "p2", Key: "a", Text: "a"},
		{Pane: "p2", Key: "b", Mod: proto.ModShift, Text: "B", Shifted: "B"},
		{Pane: "p2", Key: "enter"},
		{Pane: "p2", Key: "b", Mod: proto.ModAlt},
	} {
		if got := keyOf(t, in[i]); got != want {
			t.Errorf("input %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestEveryRequestActionSendsItsRequest(t *testing.T) {
	cases := []struct {
		keys []string
		want proto.Typed
	}{
		{[]string{"c"}, proto.TabNew{Session: "s1"}},
		{[]string{"n"}, proto.TabSelect{Session: "s1", Step: 1}},
		{[]string{"p"}, proto.TabSelect{Session: "s1", Step: -1}},
		{[]string{"3"}, proto.TabSelect{Session: "s1", Index: 3}},
		{[]string{"%"}, proto.PaneSplit{Pane: "p1", Direction: "right"}},
		{[]string{"v"}, proto.PaneSplit{Pane: "p1", Direction: "right"}},
		{[]string{"\""}, proto.PaneSplit{Pane: "p1", Direction: "down"}},
		{[]string{"-"}, proto.PaneSplit{Pane: "p1", Direction: "down"}},
		{[]string{"x"}, proto.PaneClose{Pane: "p1"}},
		{[]string{"L"}, proto.PaneResize{Pane: "p1", Direction: "right", Cells: 5}},
		{[]string{"H"}, proto.PaneResize{Pane: "p1", Direction: "left", Cells: 5}},
		{[]string{"J"}, proto.PaneResize{Pane: "p1", Direction: "down", Cells: 5}},
		{[]string{"K"}, proto.PaneResize{Pane: "p1", Direction: "up", Cells: 5}},
		{[]string{"ctrl+left"}, proto.PaneResize{Pane: "p1", Direction: "left", Cells: 1}},
		{[]string{"ctrl+up"}, proto.PaneResize{Pane: "p1", Direction: "up", Cells: 1}},
	}
	for _, c := range cases {
		t.Run(c.want.Type()+" "+c.keys[0], func(t *testing.T) {
			d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
			d.Key("ctrl+space")
			for _, k := range c.keys {
				d.Key(k)
			}
			got := lastRequest(t, d.F)
			want := proto.Request(got.ID, c.want)
			if got.T != want.T || string(got.Body) != string(want.Body) {
				t.Fatalf("request %s, want %s", got.Body, want.Body)
			}
		})
	}
	for _, c := range []struct{ key, dir string }{{"ctrl+h", "left"}, {"ctrl+j", "down"}, {"ctrl+k", "up"}, {"ctrl+l", "right"}} {
		t.Run("pane.focus "+c.dir, func(t *testing.T) {
			d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
			d.Key(c.key)
			got := lastRequest(t, d.F)
			want := proto.Request(got.ID, proto.PaneFocus{Pane: "p1", Direction: c.dir})
			if string(got.Body) != string(want.Body) {
				t.Fatalf("request %s, want %s", got.Body, want.Body)
			}
			if in := d.F.Inputs(); len(in) != 0 {
				t.Fatalf("the key also reached the pane: %v", in)
			}
		})
	}
}

func TestThePrefixTwiceSendsThePrefixKey(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("the prefix reached the pane: %v", in)
	}
	d.Key("ctrl+space")
	in := d.F.Inputs()
	if len(in) != 1 {
		t.Fatalf("inputs %v, want the prefix chord once", in)
	}
	if k := keyOf(t, in[0]); k.Key != "space" || k.Mod != proto.ModCtrl || k.Pane != "p1" {
		t.Fatalf("input %+v, want ctrl+space to p1", k)
	}
	if st := client.ChromeStateOf(d.M); st.Mode != "normal" {
		t.Fatalf("mode %q after the prefix twice, want normal", st.Mode)
	}
}

func TestVimAwareFocusForwardsTheKeyToVim(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "nvim", Editor: true})
	d.Key("ctrl+h")
	in := d.F.Inputs()
	if len(in) != 1 {
		t.Fatalf("inputs %v, want ctrl+h to reach nvim", in)
	}
	if k := keyOf(t, in[0]); k.Key != "h" || k.Mod != proto.ModCtrl {
		t.Fatalf("input %+v, want ctrl+h", k)
	}
	if rs := d.F.Requests(); len(rs) != 0 {
		t.Fatalf("requests %v, want none: the editor moves its own focus", rs)
	}
}

func TestReleasesAreForwardedOnlyToPanesThatAskedForThem(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("a")
	d.Release("a")
	d.Send(tea.KeyPressMsg{Code: 'a', Text: "a", IsRepeat: true})
	in := d.F.Inputs()
	if len(in) != 2 {
		t.Fatalf("legacy pane: inputs %v, want the press and the repeat as a press", in)
	}
	if k := keyOf(t, in[1]); k.Repeat || k.Release {
		t.Fatalf("legacy pane got %+v, want neither repeat nor release", k)
	}
	d.F.Reset()
	d.Update(frameWith("p1", 2, 80, 23, proto.Cursor{}, proto.Modes{Keys: 1 | 2}))
	d.Key("a")
	d.Send(tea.KeyPressMsg{Code: 'a', Text: "a", IsRepeat: true})
	d.Release("a")
	in = d.F.Inputs()
	if len(in) != 3 {
		t.Fatalf("pane with flag 2: inputs %v, want press, repeat and release", in)
	}
	if k := keyOf(t, in[1]); !k.Repeat {
		t.Errorf("second input %+v, want the repeat", k)
	}
	if k := keyOf(t, in[2]); !k.Release {
		t.Errorf("third input %+v, want the release", k)
	}
}

func TestPasteIsCutAtRuneBoundaries(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	text := "x" + strings.Repeat("€", proto.MaxPaste/3+10) // the euro sign is 3 bytes; the first cut falls inside one
	d.Send(tea.PasteMsg{Content: text})
	in := d.F.Inputs()
	if len(in) != 2 {
		t.Fatalf("%d messages, want 2 chunks", len(in))
	}
	var joined strings.Builder
	for i, m := range in {
		var p proto.Paste
		if m.T != proto.TypeInputPaste || m.Decode(&p) != nil {
			t.Fatalf("message %d: %s", i, m.Body)
		}
		if len(p.Text) > proto.MaxPaste || !utf8.ValidString(p.Text) {
			t.Fatalf("chunk %d: %d bytes, valid UTF-8 %v", i, len(p.Text), utf8.ValidString(p.Text))
		}
		if p.More != (i == 0) || p.Pane != "p1" {
			t.Fatalf("chunk %d: more %v pane %q", i, p.More, p.Pane)
		}
		joined.WriteString(p.Text)
	}
	if joined.String() != text {
		t.Fatal("the chunks do not join to the paste")
	}
}

func TestSessionNextCallsFleetStep(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	d.Key(")")
	d.Key("ctrl+space")
	d.Key("(")
	steps := d.F.CallsOf("Step")
	if len(steps) != 2 || steps[0].Delta != 1 || steps[1].Delta != -1 {
		t.Fatalf("Step calls %+v, want +1 then -1", steps)
	}
}

func TestDetachEndsTheProgram(t *testing.T) {
	for _, k := range []string{"d", "q"} {
		d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
		d.Key("ctrl+space")
		if d.Quit() {
			t.Fatal("the prefix alone ended the program")
		}
		d.Key(k)
		if !d.Quit() {
			t.Fatalf("prefix+%s did not end the program", k)
		}
	}
}

func TestHintsAreSetAfterTheDelayAndClearedWithThePrefix(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	if st := client.ChromeStateOf(d.M); st.Mode != "prefix" || st.Hints {
		t.Fatalf("at once: mode %q hints %v, want prefix without hints", st.Mode, st.Hints)
	}
	d.Advance(ui.HintDelay - time.Millisecond)
	if client.ChromeStateOf(d.M).Hints {
		t.Fatal("hints before the delay")
	}
	d.Advance(time.Millisecond)
	if !client.ChromeStateOf(d.M).Hints {
		t.Fatal("no hints after the delay")
	}
	d.Key("esc")
	if st := client.ChromeStateOf(d.M); st.Mode != "normal" || st.Hints || st.HintPage != 0 {
		t.Fatalf("after esc: mode %q hints %v page %d", st.Mode, st.Hints, st.HintPage)
	}
	// a new prefix starts without hints, and the old timer does nothing
	d.Key("ctrl+space")
	d.Advance(ui.HintDelay / 2)
	if client.ChromeStateOf(d.M).Hints {
		t.Fatal("hints from the timer of the earlier prefix")
	}
}

func TestQuestionMarkShowsHintsAtOnceAndThenTheNextPage(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	d.Key("?")
	if st := client.ChromeStateOf(d.M); st.Mode != "prefix" || !st.Hints || st.HintPage != 0 {
		t.Fatalf("after ?: mode %q hints %v page %d, want the first page at once", st.Mode, st.Hints, st.HintPage)
	}
	d.Key("?")
	d.Key("?")
	if st := client.ChromeStateOf(d.M); st.Mode != "prefix" || st.HintPage != 2 {
		t.Fatalf("after ? ? ?: mode %q page %d, want page 2 with the prefix waiting", st.Mode, st.HintPage)
	}
	d.Key("ctrl+space")
	d.Key("ctrl+space")
	if st := client.ChromeStateOf(d.M); st.HintPage != 0 || st.Hints {
		t.Fatalf("after the prefix ended: hints %v page %d, want both reset", st.Hints, st.HintPage)
	}
}

func TestAnUnboundKeyAfterThePrefixLeavesTheNotice(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"})
	d.Key("ctrl+space")
	d.Key("z")
	st := client.ChromeStateOf(d.M)
	if st.Mode != "normal" || st.Notice != "No binding for z" {
		t.Fatalf("mode %q notice %q, want normal with the notice", st.Mode, st.Notice)
	}
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("the key reached the pane: %v", in)
	}
	if !d.Screen().Contains("No binding for z") {
		t.Fatal("the status row does not show the notice")
	}
}

func TestPaneCloseAsksWhenAProgramRunsAndClosesOnEnter(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "nvim", Busy: true})
	d.Key("ctrl+space")
	d.Key("x")
	st := client.ChromeStateOf(d.M)
	if st.Mode != "confirm" || st.Confirm == nil || st.Confirm.Program != "nvim" {
		t.Fatalf("mode %q confirm %+v, want the confirm of nvim", st.Mode, st.Confirm)
	}
	if rs := d.F.Requests(); len(rs) != 0 {
		t.Fatalf("requests %v before the answer", rs)
	}
	d.Type("y")
	d.Key("esc")
	if st := client.ChromeStateOf(d.M); st.Mode != "normal" || st.Confirm != nil {
		t.Fatalf("after esc: mode %q, want normal", st.Mode)
	}
	if rs, in := d.F.Requests(), d.F.Inputs(); len(rs) != 0 || len(in) != 0 {
		t.Fatalf("esc kept the pane, but requests %v inputs %v", rs, in)
	}
	d.Key("ctrl+space")
	d.Key("x")
	d.Key("enter")
	got := lastRequest(t, d.F)
	if want := proto.Request(got.ID, proto.PaneClose{Pane: "p1"}); string(got.Body) != string(want.Body) {
		t.Fatalf("request %s, want %s", got.Body, want.Body)
	}
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("the enter of the confirm reached the pane: %v", in)
	}

	// a shell without a running program closes at once
	d2 := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
	d2.Key("ctrl+space")
	d2.Key("x")
	if st := client.ChromeStateOf(d2.M); st.Mode == "confirm" {
		t.Fatal("asked to close an idle shell")
	}
	if rs := d2.F.Requests(); len(rs) != 1 || rs[0].T != proto.TypePaneClose {
		t.Fatalf("requests %v, want pane.close", rs)
	}
}

func TestATooSmallWindowSendsNoResizeAndStillDetaches(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1"}, "hello")
	d.F.Reset()
	d.Send(tea.WindowSizeMsg{Width: 19, Height: 10})
	d.Advance(time.Second)
	if rs := d.F.CallsOf("Resize"); len(rs) != 0 {
		t.Fatalf("resizes %+v while too small", rs)
	}
	sc := d.Screen()
	if !sc.Contains(ui.TooSmall) || sc.Contains("hello") {
		t.Fatalf("too small: screen\n%s", sc.Plain())
	}
	d.Type("ab")
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("keys reached the pane while too small: %v", in)
	}
	d.Key("ctrl+space")
	d.Key("c")
	if rs := d.F.Requests(); len(rs) != 0 {
		t.Fatalf("prefix+c acted while too small: %v", rs)
	}
	d.Key("ctrl+space")
	d.Key("d")
	if !d.Quit() {
		t.Fatal("detach did not act while too small")
	}
}

// TestThePrefixWorksWhileTheHubIsOpen: the prefix chord and the key after
// it are the prefix machine's while the hub is open (5a.10.1: the prefix
// holds with every view, and with view none the hub is always open), and
// every other key is the hub's. Drawn with no session displayed, the
// status row is the golden status-prefix-none-80 of the chrome package.
func TestThePrefixWorksWhileTheHubIsOpen(t *testing.T) {
	connecting := func(name string) fleet.MachineState { return fleet.MachineState{Name: name, State: "connecting"} }
	start := func(t *testing.T) (*clienttest.Driver, *fakeOverlay) {
		t.Helper()
		var opened []*fakeOverlay
		d := clienttest.New(t, client.Options{Hub: &fleet.HubRequest{}, OpenHub: fakeHub(&opened),
			Chrome: chrome.NewLook(chrome.LookOptions{Chords: keys.Defaults().Chords})})
		d.Update(online(connecting("macmini"), connecting("macbook")))
		if len(opened) != 1 {
			t.Fatalf("%d hubs opened at the start, want 1", len(opened))
		}
		return d, opened[0]
	}
	hubKeys := func(o *fakeOverlay) int {
		n := 0
		for _, msg := range o.got {
			if _, ok := msg.(tea.KeyPressMsg); ok {
				n++
			}
		}
		return n
	}

	t.Run("the status row is status-prefix-none-80", func(t *testing.T) {
		d, o := start(t)
		d.Key("ctrl+space")
		if st := client.ChromeStateOf(d.M); st.Mode != "prefix" || st.View != "none" {
			t.Fatalf("mode %q view %q after the prefix with the hub open, want prefix and none", st.Mode, st.View)
		}
		if n := hubKeys(o); n != 0 {
			t.Errorf("the hub got %d keys, want none: the prefix chord is the prefix machine's", n)
		}
		want, err := os.ReadFile(filepath.Join("chrome", golden.Dir, "status-prefix-none-80.txt"))
		if err != nil {
			t.Fatal(err)
		}
		sc := d.Screen()
		row := golden.Screen{Cols: 80, Rows: 1, Lines: sc.Lines[len(sc.Lines)-1:]}
		if msg, ok := golden.Diff(string(want), row.Plain()); !ok {
			t.Errorf("the status row with the prefix and the hub open: %s", msg)
		}
		// esc cancels the prefix and does not close the hub
		d.Key("esc")
		if st := client.ChromeStateOf(d.M); st.Mode != "normal" || d.Quit() || hubKeys(o) != 0 {
			t.Errorf("after esc: mode %q, quit %v, hub keys %d; want normal, the hub open, no key in it", st.Mode, d.Quit(), hubKeys(o))
		}
		// the next key is the hub's again
		d.Key("x")
		if n := hubKeys(o); n != 1 {
			t.Errorf("the hub got %d keys after the prefix ended, want 1", n)
		}
	})

	t.Run("prefix+d detaches", func(t *testing.T) {
		d, o := start(t)
		d.Key("ctrl+space")
		d.Key("d")
		if !d.Quit() {
			t.Fatal("prefix+d with the hub open did not end the client")
		}
		if n := hubKeys(o); n != 0 {
			t.Errorf("the hub got %d keys, want none", n)
		}
	})

	t.Run("prefix+s replaces the hub", func(t *testing.T) {
		var opened []*fakeOverlay
		d := showOne(t, client.Options{OpenHub: fakeHub(&opened)}, proto.Pane{ID: "p1", Program: "zsh"})
		d.Key("ctrl+space")
		d.Key("o")
		d.Key("ctrl+space")
		d.Key("s")
		if len(opened) != 2 || opened[1].req.Mode != hub.ModeSessions {
			t.Fatalf("opened %+v, want the hub and then the sessions hub", opened)
		}
		if n := hubKeys(opened[0]); n != 0 {
			t.Errorf("the first hub got %d keys, want none", n)
		}
		if in, rs := d.F.Inputs(), d.F.Requests(); len(in) != 0 || len(rs) != 0 {
			t.Errorf("inputs %v, requests %v; want none", in, rs)
		}
	})

	t.Run("other bindings and unbound keys act on nothing under the hub", func(t *testing.T) {
		var opened []*fakeOverlay
		d := showOne(t, client.Options{OpenHub: fakeHub(&opened)}, proto.Pane{ID: "p1", Program: "zsh"})
		d.Key("ctrl+space")
		d.Key("o")
		d.F.Reset()
		d.Key("ctrl+space")
		d.Key("c")
		d.Key("ctrl+space")
		d.Key("ctrl+space")
		d.Key("ctrl+space")
		d.Key("z")
		if in, rs := d.F.Inputs(), d.F.Requests(); len(in) != 0 || len(rs) != 0 {
			t.Errorf("inputs %v, requests %v under the hub; want none", in, rs)
		}
		if n := hubKeys(opened[0]); n != 0 {
			t.Errorf("the hub got %d keys, want none: each was the prefix machine's", n)
		}
		if st := client.ChromeStateOf(d.M); st.Mode != "normal" || d.Quit() {
			t.Errorf("mode %q, quit %v; want normal with the hub open", st.Mode, d.Quit())
		}
		// a direct binding is the hub's key (ctrl+j: next row)
		d.Key("ctrl+j")
		if n := hubKeys(opened[0]); n != 1 {
			t.Errorf("the hub got %d keys, want ctrl+j", n)
		}
		if rs := d.F.Requests(); len(rs) != 0 {
			t.Errorf("requests %v; ctrl+j moved the pane focus under the hub", rs)
		}
	})
}

// A resize after the prefix repeats without it while the keys come within
// keys.RepeatTime of each other; after that the key is the pane's again.
func TestAResizeRepeatsUntilItsTimeEnds(t *testing.T) {
	d := showOne(t, client.Options{}, proto.Pane{ID: "p1", Program: "zsh"})
	d.Key("ctrl+space")
	d.Key("L")
	d.Key("L")
	d.Key("ctrl+left")
	rs := d.F.Requests()
	if len(rs) != 3 {
		t.Fatalf("requests %v, want three resizes", rs)
	}
	for i, want := range []proto.PaneResize{
		{Pane: "p1", Direction: "right", Cells: 5},
		{Pane: "p1", Direction: "right", Cells: 5},
		{Pane: "p1", Direction: "left", Cells: 1},
	} {
		w := proto.Request(rs[i].ID, want)
		if string(rs[i].Body) != string(w.Body) {
			t.Fatalf("request %d %s, want %s", i, rs[i].Body, w.Body)
		}
	}
	if in := d.F.Inputs(); len(in) != 0 {
		t.Fatalf("a resize key reached the pane: %v", in)
	}
	d.Advance(keys.RepeatTime)
	d.Key("L")
	if in := d.F.Inputs(); len(in) != 1 {
		t.Fatalf("inputs %v after the repeat time, want L in the pane", in)
	}
	if n := len(d.F.Requests()); n != 3 {
		t.Fatalf("%d requests after the repeat time, want 3", n)
	}
}
