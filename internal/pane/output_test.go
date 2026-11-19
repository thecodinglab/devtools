package pane

import (
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// unhex reads a line the helper query printed ("1b 5b 3f ...").
func unhex(line string) (string, bool) {
	var b strings.Builder
	for _, f := range strings.Fields(line) {
		v, err := strconv.ParseUint(f, 16, 8)
		if err != nil || len(f) != 2 {
			return "", false
		}
		b.WriteByte(byte(v))
	}
	return b.String(), b.Len() > 0
}

func TestQueriesAreAnsweredThroughThePTY(t *testing.T) {
	p, _ := startHelper(t, "query")
	out := waitScreen(t, p, "\nend")
	var replies []string
	for _, line := range strings.Split(out, "\n") {
		if r, ok := unhex(line); ok {
			replies = append(replies, r)
		}
	}
	want := []func(string) bool{
		func(r string) bool { return strings.HasPrefix(r, "\x1b[?") && strings.HasSuffix(r, "c") }, // DA1
		func(r string) bool { return r == "\x1bP>|devtools "+testVersion+" "+testHost+"\x1b\\" },   // XTVERSION
		func(r string) bool { return strings.HasPrefix(r, "\x1b]10;rgb:1111/2222/3333") },          // OSC 10
		func(r string) bool { return strings.HasPrefix(r, "\x1b]11;rgb:4444/5555/6666") },          // OSC 11
	}
	names := []string{"DA1", "XTVERSION", "OSC 10", "OSC 11"}
	for i, ok := range want {
		found := false
		for _, r := range replies {
			found = found || ok(r)
		}
		if !found {
			t.Errorf("no %s reply among %q", names[i], replies)
		}
	}
	// A colour change at run time is answered with the new colours.
	p.SetColors(vt.Colors{Fg: "#ffffff", Bg: "#000000"})
	probe(p, func(a *actor) {
		a.term.Write([]byte("\x1b]11;?\x07"))
		if r := string(a.term.Replies()); r != "\x1b]11;rgb:0000/0000/0000\x07" {
			t.Errorf("after SetColors OSC 11 answers %q", r)
		}
	})
}

func TestRepliesCausedByAResizeAreWritten(t *testing.T) {
	p, _ := startHelper(t, "modes", `\e[?2048h`)
	waitScreen(t, p, "ready")
	p.Resize(100, 30)
	// In-band resize notification (mode 2048): CSI 48 ; rows ; cols ; ... t
	waitScreen(t, p, hex("\x1b[48;30;100;"))
}

func TestFloodEndsWithDoneAndBoundedMemory(t *testing.T) {
	o, _ := options(t, "flood", "64")
	o.Cols, o.Rows = 80, 24
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base, peak := ms.HeapAlloc, ms.HeapAlloc
	p := start(t, o)
	testkit.Eventually(t, 5*time.Minute, `"done" after 64 MiB`, func() bool {
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapAlloc)
		for _, l := range p.Snapshot(0, false).Lines {
			if l == "done" {
				return true
			}
		}
		return false
	})
	if grew := int64(peak) - int64(base); grew >= 32<<20 {
		t.Errorf("the heap grew by %d MiB during a flood of 64 MiB, want below 32", grew>>20)
	}
	if n := p.a.parsed.Load(); n < 64<<20/readSize {
		t.Errorf("%d chunks parsed for 64 MiB of %d bytes at most", n, readSize)
	}
	s := p.Snapshot(0, false)
	if s.Base-s.First != 1000 {
		t.Errorf("history holds %d lines, want 1000", s.Base-s.First)
	}
}

func TestSnapshotWaitsForSynchronizedOutput(t *testing.T) {
	o, _ := options(t, "sync", "300")
	o.SyncHold = 10 * time.Second
	p := start(t, o)
	waitSyncOpen(t, p)
	s := p.Snapshot(0, false)
	text := strings.Join(s.Lines, "\n")
	if !strings.Contains(text, "sync 00 top") || !strings.Contains(text, "sync 23 bottom") {
		t.Fatalf("a snapshot while synchronized output was open is torn:\n%s", text)
	}
}

func TestSnapshotGivesUpAfterSyncHold(t *testing.T) {
	o, _ := options(t, "sync", "60000")
	o.SyncHold = 150 * time.Millisecond
	p := start(t, o)
	waitSyncOpen(t, p)
	began := time.Now()
	s := p.Snapshot(0, false)
	took := time.Since(began)
	text := strings.Join(s.Lines, "\n")
	if !strings.Contains(text, "sync 00 top") || strings.Contains(text, "bottom") {
		t.Errorf("the snapshot after the hold shows:\n%s", text)
	}
	if took > 5*time.Second {
		t.Errorf("Snapshot took %v with a SyncHold of 150 ms", took)
	}
	// Once the hold passed, a snapshot does not wait again.
	began = time.Now()
	p.Snapshot(0, false)
	if took := time.Since(began); took > 100*time.Millisecond {
		t.Errorf("a second snapshot took %v", took)
	}
}

// waitSyncOpen waits until the pane parsed the opening of synchronized
// output.
func waitSyncOpen(t *testing.T, p *Pane) {
	t.Helper()
	testkit.Eventually(t, 20*time.Second, "synchronized output is open", func() bool {
		open := false
		probe(p, func(a *actor) { open = a.term.SyncActive() })
		return open
	})
}

func TestTitleCwdBellClipboardAndReportEvents(t *testing.T) {
	p, r := startHelper(t, "modes",
		`\e]0;my title\e\`,
		`\e]7;file:///tmp/work\e\`,
		"\a",
		`\e]52;c;aGk=\e\`,
		`\e]7501;state=working\e\`,
		`\e]52;c;?\e\`, // a read: ignored, no event
	)
	waitScreen(t, p, "ready")
	testkit.Eventually(t, 20*time.Second, "the agent report", func() bool { return r.count(AgentReport) > 0 })
	var kinds []string
	for _, e := range r.all() {
		if e.Pane != "p1" {
			t.Errorf("event %v carries pane %q", e.Kind, e.Pane)
		}
		switch e.Kind {
		case InfoChanged:
			kinds = append(kinds, "info")
		case Bell:
			kinds = append(kinds, "bell")
		case Clipboard:
			kinds = append(kinds, "clipboard:"+string(e.Clipboard))
		case AgentReport:
			kinds = append(kinds, "agent:"+e.Agent.State)
		}
	}
	got := strings.Join(kinds, " ")
	if !strings.Contains(got, "bell clipboard:hi agent:working") || strings.Count(got, "clipboard") != 1 {
		t.Errorf("events %q", got)
	}
	if !strings.HasPrefix(got, "info") {
		t.Errorf("events %q do not start with the title and directory", got)
	}
	info := p.Info()
	if info.Title != "my title" || info.Cwd != "/tmp/work" {
		t.Errorf("Info title %q cwd %q", info.Title, info.Cwd)
	}
}

func TestForegroundGroupChangeIsAnEvent(t *testing.T) {
	old := fgInterval
	fgInterval = 20 * time.Millisecond
	t.Cleanup(func() { fgInterval = old })
	p, r := startHelper(t, "fg")
	out := waitScreen(t, p, "child ")
	_, rest, _ := strings.Cut(out, "child ")
	child, err := strconv.Atoi(strings.Fields(rest)[0])
	if err != nil {
		t.Fatalf("the helper printed %q", out)
	}
	// The pane's own reading reports the change, without Info being asked.
	testkit.Eventually(t, 20*time.Second, "InfoChanged for the new foreground group", func() bool {
		return r.count(InfoChanged) > 0
	})
	info := p.Info()
	if info.Fg != child || info.Shell == child || info.Shell != info.Pid {
		t.Errorf("Info: fg %d shell %d pid %d; the child in the foreground is %d", info.Fg, info.Shell, info.Pid, child)
	}
}

func TestAReflowingResizeCallsOnReflowBeforeTheNextSnapshot(t *testing.T) {
	o, _ := options(t, "lines", "300")
	o.Cols = 80
	var (
		mu                sync.Mutex
		calls             int
		now               int64
		okFollowed, okNot bool
		followed, other   = int64(-1), int64(-1)
	)
	o.OnReflow = func(remap func(abs int64) (int64, bool)) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		now, okFollowed = remap(followed)
		_, okNot = remap(other)
	}
	p := start(t, o)
	waitScreen(t, p, "line 300")
	f, n := findLine(t, p, "line 150"), findLine(t, p, "line 151")
	mu.Lock()
	followed, other = f, n
	mu.Unlock()
	p.Follow(f)
	p.Resize(4, 24)
	s := p.Snapshot(0, false)
	mu.Lock()
	defer mu.Unlock()
	if s.Cols != 4 {
		t.Fatalf("the snapshot after Resize is %d columns wide", s.Cols)
	}
	if calls != 1 {
		t.Fatalf("OnReflow ran %d times before the next snapshot, want 1", calls)
	}
	if !okFollowed {
		t.Fatalf("the remap does not place the followed line")
	}
	if okNot {
		t.Errorf("the remap places a line that was not followed")
	}
	if got, _ := p.Text(vt.Pos{Line: now}, vt.Pos{Line: now + 1, Col: 3}, false); got != "line 150" {
		t.Errorf("the followed line is now %d with the text %q", now, got)
	}
}

func TestFollowAndUnfollowRunOnTheActor(t *testing.T) {
	o, _ := options(t, "lines", "300")
	o.Cols = 80
	var (
		mu  sync.Mutex
		res = map[int64]bool{}
	)
	var lines []int64
	o.OnReflow = func(remap func(abs int64) (int64, bool)) {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			_, ok := remap(l)
			res[l] = ok
		}
	}
	p := start(t, o)
	waitScreen(t, p, "line 300")
	a, b := findLine(t, p, "line 100"), findLine(t, p, "line 200")
	mu.Lock()
	lines = []int64{a, b}
	mu.Unlock()
	var wg sync.WaitGroup
	for _, l := range []int64{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				p.Follow(l)
				p.Unfollow(l)
			}
		}()
	}
	wg.Wait()
	// a twice, then one Unfollow: still followed. b: no longer.
	p.Follow(a)
	p.Follow(a)
	p.Unfollow(a)
	p.Resize(4, 24)
	p.Snapshot(0, false)
	mu.Lock()
	defer mu.Unlock()
	if !res[a] {
		t.Errorf("a line followed twice and unfollowed once is not remapped")
	}
	if res[b] {
		t.Errorf("a line followed and unfollowed as often is still remapped")
	}
}

func TestScrolledSnapshotStartsAtTop(t *testing.T) {
	o, _ := options(t, "lines", "100")
	o.Cols, o.Rows = 80, 10
	p := start(t, o)
	waitScreen(t, p, "line 100")
	top := findLine(t, p, "line 20")
	s := p.Snapshot(top, true)
	if !s.Scrolled || s.Top != top || s.Lines[0] != "line 20" || s.Lines[1] != "line 21" || len(s.Lines) != 10 {
		t.Fatalf("Snapshot(%d, true) = top %d scrolled %v lines %q", top, s.Top, s.Scrolled, s.Lines)
	}
	if s := p.Snapshot(-50, true); s.Top != s.First || s.Lines[0] != "line 1" {
		t.Errorf("a top before the history: Top %d (first %d), row 0 %q", s.Top, s.First, s.Lines[0])
	}
	live := p.Snapshot(0, false)
	if s := p.Snapshot(1<<40, true); s.Top != s.Base || strings.Join(s.Lines, "|") != strings.Join(live.Lines, "|") {
		t.Errorf("a top after the screen: Top %d (base %d), lines %q, live %q", s.Top, s.Base, s.Lines, live.Lines)
	}
}

func TestTextReachesHistory(t *testing.T) {
	o, _ := options(t, "lines", "100")
	o.Cols, o.Rows = 80, 10
	p := start(t, o)
	waitScreen(t, p, "line 100")
	l := findLine(t, p, "line 1")
	if s := p.Snapshot(0, false); l >= s.Base {
		t.Fatalf("line 1 at %d is not in the history (base %d)", l, s.Base)
	}
	text, cut := p.Text(vt.Pos{Line: l}, vt.Pos{Line: l + 2, Col: 79}, false)
	if text != "line 1\nline 2\nline 3" || cut {
		t.Errorf("Text = %q, cut %v", text, cut)
	}
	if text, _ := p.Text(vt.Pos{Line: l, Col: 1}, vt.Pos{Line: l + 1, Col: 2}, true); text != "in\nin" {
		t.Errorf("block Text = %q", text)
	}
}

func TestWatchWakesOncePerChunkAndNeverBlocks(t *testing.T) {
	o, _ := options(t, "flood", "4")
	o.Cols = 80
	p := start(t, o)
	release := make(chan struct{})
	var stuck atomic.Int32
	p.Watch(func() {
		stuck.Add(1)
		<-release // a watcher that never returns
	})
	var wakes atomic.Int64
	p.Watch(func() { wakes.Add(1) })
	t.Cleanup(func() { close(release) })
	testkit.Eventually(t, 2*time.Minute, `"done" while a watcher is stuck`, func() bool {
		for _, l := range p.Snapshot(0, false).Lines {
			if l == "done" {
				return true
			}
		}
		return false
	})
	parsed := p.a.parsed.Load()
	if w := wakes.Load(); w == 0 || w > parsed {
		t.Errorf("%d wakes for %d parsed chunks", w, parsed)
	}
	if n := stuck.Load(); n != 1 {
		t.Errorf("the stuck watcher was called %d times", n)
	}
	// A cancelled watcher is not woken again.
	var after atomic.Int64
	cancel := p.Watch(func() { after.Add(1) })
	cancel()
	cancel()
	before := p.a.parsed.Load()
	p.Key(vt.Key{Name: "z", Text: "z"}) // echoed: one more chunk of output
	testkit.Eventually(t, 20*time.Second, "the echo is parsed", func() bool { return p.a.parsed.Load() > before })
	if n := after.Load(); n != 0 {
		t.Errorf("a cancelled watcher was woken %d times", n)
	}
}
