package server

import (
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/testkit"
)

// focusIs waits until the newest focus report to the pane's program is want.
func focusIs(t *testing.T, f *fakePane, want bool) {
	t.Helper()
	testkit.Eventually(t, 5*time.Second, "the focus report of "+f.o.ID, func() bool {
		calls := f.focusCalls()
		if !want && len(calls) == 0 {
			return true
		}
		return len(calls) > 0 && calls[len(calls)-1] == want
	})
}

func TestFocusReportsFollowWindowFocusAndPaneFocus(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p1 := ts.open(c)
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p1, Direction: proto.SplitRight}).Msg.Decode(&sp)
	p2 := sp.Pane

	a := ts.interactive()
	ts.view(a, s, 80, 24) // a window counts as focused until it says otherwise
	f1, f2 := ts.pane(p1), ts.pane(p2)
	focusIs(t, f2, true)
	if len(f1.focusCalls()) != 0 {
		t.Fatalf("the pane without the focus was told %v", f1.focusCalls())
	}

	ok(t, c, proto.PaneFocus{Pane: p1})
	focusIs(t, f1, true)
	focusIs(t, f2, false)

	a.Send(proto.Focus{Focused: false})
	focusIs(t, f1, false)

	b := ts.interactive()
	ts.view(b, s, 80, 24)
	focusIs(t, f1, true) // one focused window that displays the session is enough
	b.Send(proto.Focus{Focused: false})
	focusIs(t, f1, false)
	a.Send(proto.Focus{Focused: true})
	focusIs(t, f1, true)

	// Another tab becomes current: its focused pane has the focus.
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	f3 := ts.pane(tn.Pane)
	focusIs(t, f3, true)
	focusIs(t, f1, false)
	// The window leaves the session.
	a.Send(proto.ViewClear{})
	focusIs(t, f3, false)
	if calls := f2.focusCalls(); len(calls) != 2 {
		t.Fatalf("the pane that lost the focus once was told %v", calls)
	}
}
