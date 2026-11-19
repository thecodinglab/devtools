package server

import (
	"bytes"
	"errors"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

// TestMain keeps the tests hermetic: HOME is a temporary directory, nothing
// of the developer's environment that names a socket, a state directory or a
// multiplexer reaches the server, panes are fakes whose environment is not
// built by pane.Env, and the process table is the fake of the tests.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-server-test")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "XDG_") ||
			strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "TMUX") {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	paneEnv = func(server []string, hello map[string]string, id, socket string) []string {
		return []string{"DEVTOOLS_PANE=" + id, "DEVTOOLS_SOCKET=" + socket}
	}
	procTable = readFakeTable
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// The extension of these tests is registered once, from init, as cross.go
// registers its own: registries are process-wide, and a test that runs
// again (-count) must not register twice. Its handler is whatever the
// running test sets.
const (
	testExtType = "test.core.ext"
	testExtCap  = "testext"
)

var testExt struct {
	mu sync.Mutex
	h  Handler
}

func init() {
	Extend(testExtType, testExtCap, func(core Core, c Conn, m proto.Msg) {
		testExt.mu.Lock()
		h := testExt.h
		testExt.mu.Unlock()
		if h != nil {
			h(core, c, m)
		}
	})
}

func setTestExt(t *testing.T, h Handler) {
	testExt.mu.Lock()
	testExt.h = h
	testExt.mu.Unlock()
	t.Cleanup(func() {
		testExt.mu.Lock()
		testExt.h = nil
		testExt.mu.Unlock()
	})
}

// goid is the id of the calling goroutine, from its stack header.
func goid() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	b = b[:bytes.IndexByte(b, ' ')]
	n, _ := strconv.ParseUint(string(b), 10, 64)
	return n
}

func TestAnExtensionHandlerRunsOnTheLoop(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, _ := ts.open(c)

	var loop uint64
	ts.do(func(*core) { loop = goid() })
	type seen struct {
		goroutine uint64
		kind      string
		sessions  int
	}
	got := make(chan seen, 1)
	setTestExt(t, func(core Core, cn Conn, m proto.Msg) {
		got <- seen{goid(), cn.Kind(), len(core.State().Sessions)}
		core.Reply(cn, m.ID, proto.SessionOpenResult{Session: s})
	})
	r := c.Request(proto.Msg{T: testExtType})
	if !r.OK {
		t.Fatalf("the extension's request was answered %s: %s", r.Code, r.Text)
	}
	var res proto.SessionOpenResult
	r.Msg.Decode(&res)
	if res.Session != s {
		t.Fatalf("the reply of the handler carries %+v", res)
	}
	h := <-got
	if h.goroutine != loop {
		t.Fatalf("the handler ran on goroutine %d, the loop is %d", h.goroutine, loop)
	}
	if h.kind != proto.KindOneshot || h.sessions != 1 {
		t.Fatalf("the handler saw a %s connection and %d sessions", h.kind, h.sessions)
	}
}

func TestAnExtensionCapabilityIsInWelcome(t *testing.T) {
	ts := newTestServer(t)
	for _, c := range [][]string{ts.interactive().Welcome().Caps, ts.oneshot().Welcome().Caps} {
		has := map[string]int{}
		for _, x := range c {
			has[x]++
		}
		if has[testExtCap] != 1 || has[proto.CapStateGet] != 1 {
			t.Fatalf("welcome.caps %v: want %s and %s once each", c, testExtCap, proto.CapStateGet)
		}
	}
}

// recordingObserver records the calls of the Observer.
type recordingObserver struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingObserver) add(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recordingObserver) has(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == s {
			return true
		}
	}
	return false
}

func (r *recordingObserver) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (r *recordingObserver) Title(_ Core, pane, title string) { r.add("title " + pane + " " + title) }
func (r *recordingObserver) Report(_ Core, pane string, a vt.AgentReport) {
	r.add("report " + pane + " " + a.Kind + " " + a.State)
}
func (r *recordingObserver) Foreground(_ Core, pane string, _ []platform.Proc) {
	r.add("foreground " + pane)
}
func (r *recordingObserver) Seen(_ Core, pane string) { r.add("seen " + pane) }
func (r *recordingObserver) Gone(_ Core, pane string) { r.add("gone " + pane) }

func TestTheObserverSeesTitleReportAndGone(t *testing.T) {
	ts := newTestServer(t)
	rec := &recordingObserver{}
	ts.do(func(c *core) { c.observer = rec })
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ts.display(s)
	f := ts.panes.get(p)
	if f == nil {
		t.Fatal("displaying the session spawned no pane")
	}

	f.setTitle("make test")
	testkit.Eventually(t, 5*time.Second, "the observer sees the title", func() bool { return rec.has("title " + p + " make test") })
	f.setTitle("make test") // the same title again is no news
	f.report(vt.AgentReport{Kind: "claude", State: "working"})
	testkit.Eventually(t, 5*time.Second, "the observer sees the report", func() bool { return rec.has("report " + p + " claude working") })
	if n := rec.count("title "); n != 1 {
		t.Fatalf("the observer saw %d titles, want 1", n)
	}

	var res proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitRight}).Msg.Decode(&res)
	ok(t, c, proto.PaneClose{Pane: p})
	testkit.Eventually(t, 5*time.Second, "the observer sees the pane go", func() bool { return rec.has("gone " + p) })
	if rec.has("gone " + res.Pane) {
		t.Fatal("the pane that stayed was reported gone")
	}
	// A pane that never ran goes too: a new tab of a session not displayed.
	s2, _, p2 := ts.openDir(c, mkdir(t, ts.dir+"/other"))
	ok(t, c, proto.SessionClose{Session: s2})
	testkit.Eventually(t, 5*time.Second, "the observer sees the dormant pane go", func() bool { return rec.has("gone " + p2) })
}

func TestPutAgentReachesTheNextDelta(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.CosmeticDelay = time.Hour })
	i := ts.interactive()
	c := ts.oneshot()
	s, tab, p := ts.open(c)

	from := len(i.Received())
	ts.do(func(c *core) {
		c.PutAgent(p, proto.Agent{Kind: proto.AgentClaude, Status: proto.StatusBlocked, Title: "fix the tests", Since: 1795000000000})
	})
	var tr proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tr)
	st := i.WaitState("the new tab", func(st proto.State) bool { _, ok := st.Tabs[tr.Tab]; return ok })
	a, has := st.Agents[p]
	if !has {
		t.Fatal("the agent was not in a delta before the next structural one")
	}
	if a.Session != s || a.Tab != tab || a.Kind != proto.AgentClaude || a.Status != proto.StatusBlocked || a.Title != "fix the tests" {
		t.Fatalf("agent %+v", a)
	}
	ds := deltasOf(t, i, from)
	if len(ds) != 2 || !touches(ds[0], proto.KindAgent, p) || !touches(ds[1], proto.KindTab, tr.Tab) {
		t.Fatalf("want the agent's delta, then the tab's; got %+v", ds)
	}

	ts.do(func(c *core) { c.DelAgent(p) })
	ok(t, c, proto.TabSelect{Session: s, Index: 1})
	i.WaitState("the agent is gone", func(st proto.State) bool { _, ok := st.Agents[p]; return !ok })
	if snap := ts.stateGet(c); len(snap.Agents) != 0 {
		t.Fatalf("state.get still has agents %+v", snap.Agents)
	}
}

// flakyListener fails Accept a number of times before it accepts.
type flakyListener struct {
	net.Listener
	fails atomic.Int32
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.fails.Add(-1) >= 0 {
		return nil, errors.New("accept: too many open files")
	}
	return l.Listener.Accept()
}

func TestAcceptSurvivesATransientError(t *testing.T) {
	tmp := testkit.ShortTempDir(t)
	ts := &testServer{t: t, socket: tmp + "/s.sock", panes: &fakePanes{}, served: make(chan error, 1)}
	o := Defaults()
	o.NewPane = ts.panes.start
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ts.s = s
	l, err := net.Listen("unix", ts.socket)
	if err != nil {
		t.Fatal(err)
	}
	fl := &flakyListener{Listener: l}
	fl.fails.Store(3)
	start := time.Now()
	ts.serve(fl)
	w := ts.oneshot().Welcome()
	if w.Gen != proto.Gen {
		t.Fatalf("welcome %+v", w)
	}
	if fl.fails.Load() >= 0 {
		t.Fatal("the listener did not fail")
	}
	if d := time.Since(start); d < 3*acceptRetry {
		t.Fatalf("three failed accepts took %v: they were not retried after %v", d, acceptRetry)
	}
	select {
	case err := <-ts.served:
		t.Fatalf("Serve returned %v", err)
	default:
	}
}

func TestAConnectionThatStopsReadingDoesNotStopTheLoop(t *testing.T) {
	ts := newTestServer(t, func(o *Options) {
		o.StallTimeout = 300 * time.Millisecond
		o.PingInterval = time.Minute
	})
	stalled := ts.interactive()
	stalled.StopReading()
	c := ts.oneshot()
	s, _, _ := ts.open(c)

	dropped := func() bool {
		n := -1
		ts.do(func(c *core) { n = c.reg.interactive() })
		return n == 0
	}
	var slowest time.Duration
	for n := 0; n < 5000 && !dropped(); n++ {
		start := time.Now()
		ok(t, c, proto.TabNew{Session: s})
		slowest = max(slowest, time.Since(start))
	}
	testkit.Eventually(t, 10*time.Second, "the stalled connection is closed", dropped)
	if slowest > 2*time.Second {
		t.Fatalf("a request took %v while a connection did not read", slowest)
	}
	// The loop goes on.
	ok(t, c, proto.TabSelect{Session: s, Index: 1})
}
