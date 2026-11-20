package server

import (
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
)

func TestSnapshotIsTheFirstMessageAfterWelcome(t *testing.T) {
	ts := newTestServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	ok(t, c, proto.TabNew{Session: s})

	i := ts.interactive() // Dial itself fails unless state.snapshot follows welcome
	got := i.Received()
	if len(got) < 2 || got[0].T != proto.TypeWelcome || got[1].T != proto.TypeStateSnapshot || got[1].ID != 0 {
		t.Fatalf("the first messages are %v", i.Types())
	}
	var snap proto.Snapshot
	got[1].Decode(&snap)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != s || len(snap.Tabs) != 2 || len(snap.Panes) != 2 || snap.Panes[0].ID != p {
		t.Fatalf("the snapshot does not hold the state: %+v", snap)
	}
	if want := ts.stateGet(c).Rev; snap.Rev != want {
		t.Fatalf("the snapshot is at revision %d, the server at %d", snap.Rev, want)
	}
}

func TestHelloTimeoutClosesTheConnection(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.HelloTimeout = 150 * time.Millisecond })
	nc := prototest.DialRaw(t, ts.socket)
	start := time.Now()
	nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := proto.ReadMsg(nc)
	if err != io.EOF {
		t.Fatalf("a connection without hello gave %v, not the end", err)
	}
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("closed after %v, before the hello timeout", d)
	}
	// A first message that is not hello is refused at once.
	nc2 := prototest.DialRaw(t, ts.socket)
	nc2.SetDeadline(time.Now().Add(5 * time.Second))
	proto.WriteMsg(nc2, proto.Encode(proto.StateGet{}))
	m, err := proto.ReadMsg(nc2)
	if err != nil || m.T != proto.TypeRefuse {
		t.Fatalf("a first message that is not hello was answered %s %v", m.T, err)
	}
}

// The refuse of a first message that is not hello repeats its type cleaned
// and cut (4.1: the sender cleans and cuts every text; SEC2-W2-02).
func TestTheRefuseOfANonHelloFirstMessageIsCleanedAndCut(t *testing.T) {
	ts := newTestServer(t)
	for _, typ := range []string{
		"state.get\x1b[31m\u202eevil\a",
		strings.Repeat("x", 100_000),
	} {
		nc := prototest.DialRaw(t, ts.socket)
		nc.SetDeadline(time.Now().Add(5 * time.Second))
		if err := proto.WriteMsg(nc, proto.Msg{T: typ}); err != nil {
			t.Fatal(err)
		}
		m, err := proto.ReadMsg(nc)
		if err != nil || m.T != proto.TypeRefuse {
			t.Fatalf("a first message of type %.40q was answered %s %v", typ, m.T, err)
		}
		var r proto.Refuse
		if err := m.Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.Code != proto.CodeInvalid {
			t.Fatalf("code %q, want %q", r.Code, proto.CodeInvalid)
		}
		if strings.ContainsAny(r.Text, "\x1b\a\u202e") {
			t.Fatalf("the refuse text %q holds control or bidi characters", r.Text)
		}
		if n := len([]rune(r.Text)); n > 200 {
			t.Fatalf("the refuse text has %d runes; the type was not cut", n)
		}
		if !strings.Contains(r.Text, typ[:5]) {
			t.Fatalf("the refuse text %q does not name the type", r.Text)
		}
	}
}

func TestRefuseNamesBothGenerations(t *testing.T) {
	ts := newTestServer(t)
	h := prototest.Interactive()
	h.Gen = 3
	text := refused(t, ts.socket, h, proto.CodeGeneration)
	if !strings.Contains(text, "protocol 1") || !strings.Contains(text, "protocol 3") {
		t.Fatalf("refuse text %q does not name both generations", text)
	}
	h.Gen, h.Kind = proto.Gen, "window"
	refused(t, ts.socket, h, proto.CodeInvalid)
}

// A client of generation 2 (the builds that named the tree layout so) is
// welcomed in its own generation, which is what it checks for, and finds
// the tree layout among the caps.
func TestAClientOfGenerationTwoIsWelcomedInItsOwn(t *testing.T) {
	ts := newTestServer(t)
	h := prototest.Interactive()
	h.Gen = 2
	w := prototest.Dial(t, ts.socket, h).Welcome()
	if w.Gen != 2 || !slices.Contains(w.Caps, proto.CapTree) || !slices.Contains(w.Caps, proto.CapStateGet) {
		t.Fatalf("welcome %+v, want generation 2 with tree and stateget", w)
	}
	if w := ts.oneshot().Welcome(); w.Gen != proto.Gen {
		t.Fatalf("a generation %d hello was welcomed with %d", proto.Gen, w.Gen)
	}
}

func TestWelcomeCarriesBootHostPidAndClientCount(t *testing.T) {
	ts := newTestServer(t)
	w1 := ts.interactive().Welcome()
	w2 := ts.interactive().Welcome()
	w3 := ts.oneshot().Welcome()
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(w1.Boot) || w2.Boot != w1.Boot || w3.Boot != w1.Boot {
		t.Fatalf("boot %q %q %q", w1.Boot, w2.Boot, w3.Boot)
	}
	id := regexp.MustCompile(`^c[0-9]+$`)
	for _, w := range []proto.Welcome{w1, w2, w3} {
		if w.Host != "testhost" || w.Pid != os.Getpid() || w.Gen != proto.Gen || w.Version != "0.0.0-test" || !id.MatchString(w.Client) {
			t.Fatalf("welcome %+v", w)
		}
	}
	if w1.Client == w2.Client || w2.Client == w3.Client {
		t.Fatalf("client ids %s %s %s", w1.Client, w2.Client, w3.Client)
	}
	if w1.Clients != 1 || w2.Clients != 2 || w3.Clients != 2 {
		t.Fatalf("clients %d %d %d, want 1 2 2 (a one-shot is not counted)", w1.Clients, w2.Clients, w3.Clients)
	}
	if other := newTestServer(t).oneshot().Welcome().Boot; other == w1.Boot {
		t.Fatal("two servers have the same boot id")
	}
}

func TestAOneShotGetsRepliesButNoStateStream(t *testing.T) {
	ts := newTestServer(t)
	i := ts.interactive()
	c := ts.oneshot()
	s, _, _ := ts.open(c)
	ok(t, ts.oneshot(), proto.TabNew{Session: s})
	i.WaitState("two tabs", func(st proto.State) bool { return len(st.Tabs) == 2 })
	for _, m := range c.Received() {
		switch m.T {
		case proto.TypeWelcome, proto.TypeOK, proto.TypeStateSnapshot:
			if m.T != proto.TypeWelcome && m.ID == 0 {
				t.Fatalf("the one-shot received %s without an id", m.T)
			}
		default:
			t.Fatalf("the one-shot received %s; it gets replies only (%v)", m.T, c.Types())
		}
	}
	// view.* and input.* are not for one-shots (4.2.1 rule 4).
	r := c.Request(proto.ViewSet{Session: s, Cols: 80, Rows: 24})
	if r.OK || r.Code != proto.CodeInvalid {
		t.Fatalf("view.set from a one-shot: %+v", r)
	}
	c.Send(proto.Key{Pane: "p1", Key: "a", Text: "a"})
	ok(t, c, proto.StateGet{})
}

// running opens a session and displays it, so that its pane has a (fake)
// program, and returns the pane and its fake.
func running(t *testing.T, ts *testServer) (string, *fakePane) {
	t.Helper()
	s, _, p := ts.open(ts.oneshot())
	ts.display(s)
	f := ts.panes.get(p)
	if f == nil {
		t.Fatal("no pane was spawned")
	}
	return p, f
}

func TestAOneShotCarriesItsPane(t *testing.T) {
	ts := newTestServer(t)
	p, f := running(t, ts)
	h := prototest.Oneshot(p)
	h.Ancestors = []int{os.Getpid(), f.Info().Pid, 1}
	c := prototest.Dial(t, ts.socket, h)
	id := c.Welcome().Client
	var pane string
	ts.do(func(c *core) {
		for _, cn := range c.Conns() {
			if cn.ID() == id {
				pane = cn.Pane()
			}
		}
	})
	if pane != p {
		t.Fatalf("the connection carries pane %q, want %s", pane, p)
	}
	ok(t, c, proto.StateGet{})
}

func TestAOneShotWithAStalePaneIsAnsweredNoPane(t *testing.T) {
	ts := newTestServer(t)
	p, f := running(t, ts)
	stale := prototest.Oneshot("p99")
	stale.Ancestors = []int{os.Getpid(), f.Info().Pid, 1}
	if text := refused(t, ts.socket, stale, proto.CodeNoPane); !strings.Contains(text, "p99") {
		t.Fatalf("refuse text %q does not name the pane", text)
	}
	// A pane that exists, but is not an ancestor of the caller.
	other := prototest.Oneshot(p)
	other.Ancestors = []int{os.Getpid(), 1}
	refused(t, ts.socket, other, proto.CodeNoPane)
	// A pane that exists and has no program: a session never displayed.
	_, _, dormant := ts.openDir(ts.oneshot(), mkdir(t, ts.dir+"/never"))
	if ts.panes.get(dormant) != nil {
		t.Fatal("a pane of a session nobody displayed was spawned")
	}
	h := prototest.Oneshot(dormant)
	h.Ancestors = []int{os.Getpid(), f.Info().Pid, 1}
	refused(t, ts.socket, h, proto.CodeNoPane)
}

func TestHelloFromAPanesOwnTerminalIsRefused(t *testing.T) {
	ts := newTestServer(t)
	_, f := running(t, ts)
	h := prototest.Interactive()
	h.TTY = f.Info().TTY
	if text := refused(t, ts.socket, h, proto.CodeNested); !strings.Contains(text, "testhost") {
		t.Fatalf("refuse text %q does not name the host", text)
	}
	// Through a bridge the terminal is another machine's: ignored.
	h.Remote = true
	welcomed(t, ts.socket, h)
	h.Remote, h.TTY = false, "/dev/pts/elsewhere"
	welcomed(t, ts.socket, h)
}

func TestHelloFromADescendantOfAPaneIsRefused(t *testing.T) {
	ts := newTestServer(t)
	_, f := running(t, ts)
	h := prototest.Interactive()
	h.TTY = "/dev/pts/elsewhere"
	h.Ancestors = []int{os.Getpid(), f.Info().Pid, 1}
	refused(t, ts.socket, h, proto.CodeNested)
	// A one-shot inside a pane is what panes are for.
	o := prototest.Oneshot("")
	o.Ancestors = h.Ancestors
	welcomed(t, ts.socket, o)
}
