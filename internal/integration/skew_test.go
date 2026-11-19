package integration

// Skew and limits (4.1): a client of a later version, a client without a
// capability, the message cap in both directions, a hundred idle panes, the
// server's memory with full histories (T10 of 10.4), and the time from a
// key to the screen.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/e2e"
	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
)

// readFramed reads one message as it is on the wire: the length of its
// body and the body, whatever the length says. A length above the cap is
// read in full too, so that the test sees what was sent.
func readFramed(nc net.Conn, d time.Duration) (int, []byte, error) {
	nc.SetReadDeadline(time.Now().Add(d))
	var prefix [4]byte
	if _, err := io.ReadFull(nc, prefix[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(prefix[:]))
	body := make([]byte, n)
	if _, err := io.ReadFull(nc, body); err != nil {
		return n, nil, err
	}
	return n, body, nil
}

// head is the envelope of a message body: its type, id and the fields of a
// frame that say which piece it is.
type head struct {
	T    string `json:"t"`
	ID   uint64 `json:"id"`
	Pane string `json:"pane"`
	Full bool   `json:"full"`
	More bool   `json:"more"`
}

func TestAClientWithUnknownCapabilitiesAndFieldsWorks(t *testing.T) {
	home, socket := startServer(t)
	dir := work(t, home)
	r := dialRaw(t, socket)
	// A hello of a later client: a capability this server does not know,
	// fields it does not know, at the top and inside colors.
	r.send(helloJSON(proto.KindInteractive,
		`"caps":["telepathy","catalog","agents","route","stateget"]`,
		`"later":{"nested":[1,2,3]}`,
		`"colors":{"fg":"#d0d0d0","bg":"#000000","dark":true,"contrast":"high"}`))
	m := r.next()
	var w proto.Welcome
	if m.T != proto.TypeWelcome || m.Decode(&w) != nil {
		t.Fatalf("the hello was answered %s", m.Body)
	}
	if slices.Contains(w.Caps, "telepathy") {
		t.Fatalf("welcome.caps %v claims a capability the server does not have", w.Caps)
	}
	for _, c := range []string{proto.CapCatalog, proto.CapAgents, proto.CapRoute, proto.CapStateGet} {
		if !slices.Contains(w.Caps, c) {
			t.Fatalf("welcome.caps %v lacks %s", w.Caps, c)
		}
	}
	// A request with a field of a later version works; a notification of a
	// type the server does not know changes nothing.
	r.send(`{"t":"view.later","hint":"ignored"}`)
	r.send(fmt.Sprintf(`{"t":"view.set","id":1,"dir":%q,"name":"work","cols":80,"rows":24,"zoom":2}`, dir))
	// view.ok and the pane's first frames, in the order the server sends
	// them, until the program's first line arrived.
	var v proto.ViewOK
	until(t, r, "view.ok and the program's first line", func(m proto.Msg) bool {
		if m.ID == 1 {
			if m.T != proto.TypeViewOK || m.Decode(&v) != nil || len(v.Panes) != 1 {
				t.Fatalf("view.set with an unknown field was answered %s", m.Body)
			}
		}
		return v.Session != "" && m.T == proto.TypeFrame && bytes.Contains(m.Body, []byte("ready"))
	})
	// A key with a field of a later version reaches the program.
	r.send(fmt.Sprintf(`{"t":"input.key","pane":%q,"key":"x","mod":0,"text":"x","pressure":0.5}`, v.Panes[0]))
	until(t, r, "the program's hex of x", func(m proto.Msg) bool {
		return m.T == proto.TypeFrame && bytes.Contains(m.Body, []byte("78"))
	})
}

// until reads messages of r, answering pings, until done holds for one of
// them; done sees every message on the way. The test fails after
// prototest.Timeout.
func until(t *testing.T, r *rawConn, what string, done func(proto.Msg) bool) {
	t.Helper()
	deadline := time.Now().Add(prototest.Timeout)
	for time.Now().Before(deadline) {
		m := r.next()
		if m.T == proto.TypePing {
			r.send(`{"t":"pong"}`)
			continue
		}
		if done(m) {
			return
		}
	}
	t.Fatalf("no %s within %v", what, prototest.Timeout)
}

func TestAClientWithoutACapabilityNeverReceivesItsMessages(t *testing.T) {
	// The pane runs a program named claude that reports working and
	// blocked in turn (OSC 7501): every blocked is an event.agent.
	home, socket := startServer(t, servertest.WithHelper("fg", "claude", "agentfake"),
		servertest.WithOptions(func(o *server.Options) { o.ProcInterval = 50 * time.Millisecond }))
	dir := work(t, home)
	withAgents := prototest.Interactive()
	withAgents.Caps = []string{proto.CapAgents, proto.CapStateGet}
	a := prototest.Dial(t, socket, withAgents)
	plain := prototest.Interactive()
	plain.Caps = []string{proto.CapStateGet}
	b := prototest.Dial(t, socket, plain)
	v := a.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	b.View(proto.ViewSet{Session: v.Session, Cols: 80, Rows: 24})

	// The client with the capability receives event.agent twice, so the
	// one without it had the time to receive one too.
	from := len(a.Received())
	from = indexAfter(a.Received(), a.WaitMessage(proto.TypeEventAgent, from))
	a.WaitMessage(proto.TypeEventAgent, from)
	b.Request(proto.StateGet{}) // everything sent to b before its reply has arrived
	for _, m := range b.Received() {
		if m.T == proto.TypeEventAgent {
			t.Fatalf("a client without the capability %s received %s", proto.CapAgents, m.Body)
		}
	}
}

// indexAfter returns the index after the message m among msgs.
func indexAfter(msgs []proto.Msg, m proto.Msg) int {
	for i := range msgs {
		if bytes.Equal(msgs[i].Body, m.Body) {
			return i + 1
		}
	}
	return len(msgs)
}

func TestAFrameAboveTheMessageCapIsNeverSent(t *testing.T) {
	longer(t, time.Minute)
	// The program fills a pane of 1000 x 500 with cells of distinct
	// colours: its full frame is far above the cap of 4 MiB.
	const cols, rows = 1000, 500
	home, socket := startServer(t, servertest.WithHelper("paint", strconv.Itoa(cols)))
	dir := work(t, home)
	painter := prototest.Dial(t, socket, prototest.Interactive())
	v := painter.View(proto.ViewSet{Dir: dir, Name: "work", Cols: cols, Rows: rows})
	painter.WaitScreen(v.Panes[0], "the painted pane", func(s golden.Screen) bool {
		return strings.HasPrefix(golden.PlainRow(s.Lines[rows/2]), "painted")
	})

	// A new viewer gets the whole screen as one frame in pieces.
	r := dialRaw(t, socket)
	r.send(helloJSON(proto.KindInteractive))
	r.send(fmt.Sprintf(`{"t":"view.set","id":1,"session":%q,"cols":%d,"rows":%d}`, v.Session, cols, rows))
	total, pieces, largest := 0, 0, 0
	for {
		n, body, err := readFramed(r.nc, prototest.Timeout)
		if err != nil {
			t.Fatalf("after %d pieces of %d bytes: %v", pieces, total, err)
		}
		largest = max(largest, n)
		if n > proto.MaxMessage {
			t.Fatalf("a message of %d bytes was sent, above the cap of %d", n, proto.MaxMessage)
		}
		var h head
		if err := json.Unmarshal(body, &h); err != nil {
			t.Fatal(err)
		}
		if h.T != proto.TypeFrame || h.Pane != v.Panes[0] {
			continue
		}
		if pieces == 0 && !h.Full {
			t.Fatal("the first frame of a new viewer is not full")
		}
		total += n
		pieces++
		if !h.More {
			break
		}
	}
	t.Logf("the full frame of %d x %d took %d pieces, %d bytes, the largest %d bytes", cols, rows, pieces, total, largest)
	if total <= proto.MaxMessage || pieces < 2 {
		t.Fatalf("the frame was %d bytes in %d pieces: the test did not exceed the cap", total, pieces)
	}
}

func TestAnOversizedMessageFromAClientClosesIt(t *testing.T) {
	home, socket := startServer(t)
	other := prototest.Dial(t, socket, prototest.Interactive())
	v := other.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	for _, c := range []struct {
		what string
		n    uint32
	}{
		{"one byte above the cap", proto.MaxMessage + 1},
		{"the largest length", ^uint32(0)},
		{"length zero", 0},
	} {
		t.Run(c.what, func(t *testing.T) {
			r := dialRaw(t, socket)
			r.send(helloJSON(proto.KindInteractive))
			if m := r.next(); m.T != proto.TypeWelcome {
				t.Fatalf("the hello was answered %s", m.Body)
			}
			var prefix [4]byte
			binary.BigEndian.PutUint32(prefix[:], c.n)
			r.nc.SetWriteDeadline(time.Now().Add(prototest.Timeout))
			if _, err := r.nc.Write(append(prefix[:], `{"t":"ping"}`...)); err != nil {
				t.Fatal(err)
			}
			if !waitClosedByPeer(r.nc, prototest.Timeout) {
				t.Fatalf("the connection is still open %v after a length of %d", prototest.Timeout, c.n)
			}
		})
	}
	// The other connection did not notice.
	other.Type(v.Panes[0], "k")
	other.WaitScreen(v.Panes[0], "k typed after the oversized messages", func(s golden.Screen) bool {
		return strings.Contains(strings.Join(plainRows(s), "\n"), "6b")
	})
}

// cpuTime returns the processor time a process has used, user and system,
// all its threads.
func cpuTime(pid int) (time.Duration, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return 0, err
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			return 0, errors.New("no ) in stat")
		}
		f := strings.Fields(s[i+1:])
		// After the name: state is field 3 of stat, utime 14, stime 15.
		if len(f) < 13 {
			return 0, errors.New("short stat")
		}
		utime, err1 := strconv.ParseInt(f[11], 10, 64)
		stime, err2 := strconv.ParseInt(f[12], 10, 64)
		if err1 != nil || err2 != nil {
			return 0, errors.New("bad stat")
		}
		// USER_HZ is 100 on every Linux the plan supports.
		return time.Duration(utime+stime) * 10 * time.Millisecond, nil
	}
	out, err := exec.Command("/bin/ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return parsePsTime(strings.TrimSpace(string(out)))
}

// parsePsTime reads the time column of ps: [[dd-]hh:]mm:ss.cc.
func parsePsTime(s string) (time.Duration, error) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		days, _ = strconv.Atoi(d)
		s = rest
	}
	parts := strings.Split(s, ":")
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, fmt.Errorf("ps time %q: %v", s, err)
		}
		total = total*60 + v
	}
	return time.Duration((total + float64(days)*86400) * float64(time.Second)), nil
}

// idleCPULimit is the bound of TestOneHundredPanesIdleWithoutBusyLoops: CPU
// time of the server over idleWindow.
const (
	idleCPULimit = 150 * time.Millisecond
	idleWindow   = 3 * time.Second
)

// openPanes makes the session of dir hold n panes through c: tabs of two
// panes each, the first of them the session's own. It returns the pane ids.
func openPanes(t *testing.T, c *prototest.Client, dir string, n, cols, rows int) []string {
	t.Helper()
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: cols, Rows: rows})
	ids := []string{v.Panes[0]}
	split := func(pane string) {
		r := c.Request(proto.PaneSplit{Pane: pane, Direction: proto.SplitRight})
		var res proto.PaneSplitResult
		if !r.OK || r.Msg.Decode(&res) != nil {
			t.Fatalf("pane.split: %+v", r)
		}
		ids = append(ids, res.Pane)
	}
	split(v.Panes[0])
	for len(ids) < n {
		r := c.Request(proto.TabNew{Session: v.Session})
		var res proto.TabNewResult
		if !r.OK || r.Msg.Decode(&res) != nil {
			t.Fatalf("tab.new: %+v", r)
		}
		ids = append(ids, res.Pane)
		if len(ids) < n {
			split(res.Pane)
		}
	}
	return ids
}

// waitRunning waits until every pane of the server runs.
func waitRunning(t *testing.T, o *prototest.Client, n int) {
	t.Helper()
	testkit.Eventually(t, 60*time.Second, fmt.Sprintf("%d panes run", n), func() bool {
		snap := stateGet(t, o)
		running := 0
		for _, p := range snap.Panes {
			if p.State == proto.PaneRunning {
				running++
			}
		}
		return running == n
	})
}

func TestOneHundredPanesIdleWithoutBusyLoops(t *testing.T) {
	longer(t, time.Minute)
	home := testkit.ShortTempDir(t)
	b := startBoot(t, home, "hex")
	c := prototest.Dial(t, b.socket(), prototest.Interactive())
	c.KeepAlive(time.Second)
	openPanes(t, c, work(t, home), 100, 80, 24)
	waitRunning(t, oneshot(t, b.socket()), 100)
	// Let the starts settle: the first scans, the first frames, the save.
	time.Sleep(2 * time.Second)
	before, err := cpuTime(b.pid())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(idleWindow)
	after, err := cpuTime(b.pid())
	if err != nil {
		t.Fatal(err)
	}
	used := after - before
	t.Logf("the server used %v of processor time in %v with 100 idle panes", used, idleWindow)
	if used >= idleCPULimit {
		t.Fatalf("the server used %v in %v with 100 idle panes, not below %v", used, idleWindow, idleCPULimit)
	}
}

func TestServerMemoryWithFullHistories(t *testing.T) {
	// T10 of 10.4; both bounds are ESTIMATES, read from e2e's table.
	full, idle := e2e.Limit("T10", "full"), e2e.Limit("T10", "idle")

	t.Run("twenty panes of 200 x 50 with 10,000 lines of history each", func(t *testing.T) {
		longer(t, 2*time.Minute)
		home := testkit.ShortTempDir(t)
		b := startBoot(t, home, "wide 11000 200")
		c := prototest.Dial(t, b.socket(), prototest.Interactive())
		c.KeepAlive(time.Second)
		// Each pane in a session of its own, displayed once at 200 x 50
		// until its program printed everything.
		for i := range 20 {
			dir := work(t, home, strconv.Itoa(i))
			v := c.View(proto.ViewSet{Dir: dir, Name: filepath.Base(dir), Cols: 200, Rows: 50})
			c.WaitFor(v.Panes[0], "done")
		}
		measureRSS(t, b.pid(), full, "20 panes of 200 x 50 with full histories")
	})
	t.Run("ten idle empty panes", func(t *testing.T) {
		home := testkit.ShortTempDir(t)
		b := startBoot(t, home, "hex")
		c := prototest.Dial(t, b.socket(), prototest.Interactive())
		c.KeepAlive(time.Second)
		openPanes(t, c, work(t, home), 10, 80, 24)
		waitRunning(t, oneshot(t, b.socket()), 10)
		time.Sleep(time.Second)
		measureRSS(t, b.pid(), idle, "10 idle empty panes")
	})
}

// measureRSS reads the resident set of the server and holds it against the
// bound.
func measureRSS(t *testing.T, pid int, bound e2e.Bound, what string) {
	t.Helper()
	kib, err := rssKiB(pid)
	if err != nil {
		t.Fatal(err)
	}
	got := float64(kib) / 1024
	t.Logf("T10 %s: resident set of the server %.1f MiB, at most %.0f MiB", what, got, bound.Value)
	if !bound.Met(got) {
		t.Fatalf("T10 %s: the server holds %.1f MiB, more than %.0f MiB", what, got, bound.Value)
	}
}
