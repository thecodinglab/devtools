package server

import (
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/testkit"
)

// group is a foreground process group of the fake table: the leader first,
// its pid the group's id.
func group(pgid int, names ...string) []platform.Proc {
	var out []platform.Proc
	for k, n := range names {
		out = append(out, platform.Proc{Pid: pgid + k, PPid: pgid + k - 1, Pgid: pgid, Name: n})
	}
	return out
}

func TestEditorIsTrueForAnyMatchingProcessOfTheForegroundGroup(t *testing.T) {
	for _, name := range []string{"nvim", "vim", "vimdiff", "view", "fzf", "lvim", "nvimdiff", "gvim", "vi"} {
		if !isEditor(group(10, name)) {
			t.Errorf("%s is not an editor", name)
		}
	}
	for _, name := range []string{"zsh", "claude", "nvimpager", "less", "git", "nvim-qt", ""} {
		if isEditor(group(10, name)) {
			t.Errorf("%s is an editor", name)
		}
	}
	if !isEditor(group(10, "git", "nvim")) {
		t.Error("git commit with nvim as its child is not an editor")
	}

	// Through the server: the pane's foreground group is git with nvim.
	ts := newTestServer(t, func(o *Options) { o.FgInterval = 10 * time.Millisecond })
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	g := fakePidBase + 5000
	setProcs(t, append(group(g, "git", "nvim"), group(f.Info().Shell, "zsh")...)...)
	f.setFg(g)
	st := a.WaitState("the pane runs an editor", func(st proto.State) bool { return st.Panes[p].Editor })
	if x := st.Panes[p]; !x.Busy || x.Program != "git" {
		t.Fatalf("pane %+v; want busy, program git", x)
	}
	f.setFg(f.Info().Shell)
	st = a.WaitState("the shell is back", func(st proto.State) bool { return st.Panes[p].Program == "zsh" })
	if x := st.Panes[p]; x.Busy || x.Program != "zsh" {
		t.Fatalf("pane %+v; want not busy, program zsh", x)
	}
}

func TestEditorIsCurrentWithin100msOfAForegroundGroupChange(t *testing.T) {
	ts := newTestServer(t) // FgInterval 100 ms, ProcInterval 1 s
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	g := fakePidBase + 6000
	setProcs(t, group(g, "nvim")...)
	time.Sleep(250 * time.Millisecond) // the first reading of the new pane is done
	start := time.Now()
	f.setFg(g)
	a.WaitState("the pane runs an editor", func(st proto.State) bool { return st.Panes[p].Editor })
	// 100 ms of the interval, a scan and a delta; far from the second of
	// the periodic scan.
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Fatalf("editor became true %v after the foreground group changed", d)
	}
}

func TestBusyIsTrueWhileAProgramOtherThanTheShellIsInTheForeground(t *testing.T) {
	ts := newTestServer(t, func(o *Options) {
		o.FgInterval = 10 * time.Millisecond
		o.ProcInterval = 100 * time.Millisecond
	})
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	if st := ts.stateGet(c); st.Panes[0].Busy {
		t.Fatal("a pane whose shell is in the foreground is busy")
	}
	g := fakePidBase + 7000
	setProcs(t, group(g, "make")...)
	f.setFg(g)
	a.WaitState("busy", func(st proto.State) bool { return st.Panes[p].Busy })
	f.setFg(f.Info().Shell)
	a.WaitState("not busy", func(st proto.State) bool { return !st.Panes[p].Busy })

	// A pane nobody looks at is read once per ProcInterval.
	other, _, q := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	ts.display(other)
	h := ts.pane(q)
	h.setFg(g + 1)
	a.WaitState("the hidden pane is busy", func(st proto.State) bool { return st.Panes[q].Busy })
}

func TestTheProcessScanRunsOnlyWhileAForegroundProgramRuns(t *testing.T) {
	const interval = 40 * time.Millisecond
	ts := newTestServer(t, func(o *Options) {
		o.FgInterval = 10 * time.Millisecond
		o.ProcInterval = interval
	})
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	f := ts.pane(p)
	// The first reading of a new pane's foreground group scans once.
	time.Sleep(5 * interval)
	idle := tableReads()
	time.Sleep(10 * interval)
	if n := tableReads() - idle; n != 0 {
		t.Fatalf("%d scans while only the shell ran", n)
	}
	g := fakePidBase + 8000
	setProcs(t, group(g, "cargo")...)
	f.setFg(g)
	testkit.Eventually(t, 5*time.Second, "periodic scans", func() bool { return tableReads()-idle >= 4 })
	f.setFg(f.Info().Shell)
	a.WaitState("not busy", func(st proto.State) bool { return !st.Panes[p].Busy })
	time.Sleep(3 * interval) // the scan of the change, and one that was running
	done := tableReads()
	time.Sleep(10 * interval)
	if n := tableReads() - done; n != 0 {
		t.Fatalf("%d scans after the program ended", n)
	}
}

func TestTheObserverGetsForegroundAndSeen(t *testing.T) {
	const interval = 50 * time.Millisecond
	ts := newTestServer(t, func(o *Options) {
		o.FgInterval = 10 * time.Millisecond
		o.ProcInterval = interval
	})
	rec := &recordingObserver{}
	ts.do(func(c *core) { c.observer = rec })
	c := ts.oneshot()
	s, _, p := ts.open(c)
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn) // the current tab: tn.Pane
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	testkit.Eventually(t, 5*time.Second, "the displayed pane is seen", func() bool { return rec.has("seen " + tn.Pane) })
	if rec.has("seen " + p) {
		t.Fatal("a pane of a tab that is not shown was seen")
	}
	f := ts.pane(tn.Pane)
	f.setTitle("claude")
	testkit.Eventually(t, 5*time.Second, "seen again with the title", func() bool { return rec.count("seen "+tn.Pane) == 2 })

	// Without a focused window nothing is seen.
	a.Send(proto.Focus{Focused: false})
	ts.sync(a)
	f.setTitle("claude 2")
	testkit.Eventually(t, 5*time.Second, "the title", func() bool { return rec.has("title " + tn.Pane + " claude 2") })
	if n := rec.count("seen " + tn.Pane); n != 2 {
		t.Fatalf("seen %d times; a title without a focused viewer is no sight", n)
	}
	a.Send(proto.Focus{Focused: true})
	testkit.Eventually(t, 5*time.Second, "seen when the window has the focus again", func() bool { return rec.count("seen "+tn.Pane) == 3 })

	// The foreground group reaches the observer, at most once per interval.
	g := fakePidBase + 9000
	setProcs(t, group(g, "claude", "node")...)
	start := time.Now()
	f.setFg(g)
	testkit.Eventually(t, 5*time.Second, "Foreground", func() bool { return rec.has("foreground " + tn.Pane) })
	time.Sleep(10 * interval)
	n := rec.count("foreground " + tn.Pane)
	if limit := int(time.Since(start)/interval) + 1; n > limit {
		t.Fatalf("Foreground %d times in %v, at most %d", n, time.Since(start), limit)
	}
	if n < 3 {
		t.Fatalf("Foreground only %d times while the program ran", n)
	}
}
