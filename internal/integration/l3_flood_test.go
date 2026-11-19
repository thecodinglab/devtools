package integration

// Level 3: a program that writes as fast as it can. One stalled client
// holds nobody else (W7, 3.2.3), and the server's memory does not grow with
// what the program writes.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
)

// floodMiB is what the helper flood writes in these tests.
const floodMiB = 64

// stall connects a client that sets a view of the session and then never
// reads: hello and view.set are written, nothing is read. The view is large,
// so that frames fill the socket at once, and it is set last, so that this
// client owns the session's size and the frames of every viewer have it.
func stall(t *testing.T, socket, session string, cols, rows int) net.Conn {
	t.Helper()
	r := dialRaw(t, socket)
	r.send(helloJSON(proto.KindInteractive))
	r.send(fmt.Sprintf(`{"t":"view.set","id":1,"session":%q,"cols":%d,"rows":%d}`, session, cols, rows))
	return r.nc
}

// waitClosedByPeer reads and drops what the server sent on nc until the
// server closes it; false when it is still open after d. Reading drains the
// connection, so it is called only once the server has closed it.
func waitClosedByPeer(nc net.Conn, d time.Duration) bool {
	nc.SetReadDeadline(time.Now().Add(d))
	_, err := io.Copy(io.Discard, nc)
	var ne net.Error
	return !(errors.As(err, &ne) && ne.Timeout())
}

// interactiveClients asks the server how many interactive connections it
// has, not counting the one that asks: welcome.clients of a new connection
// that is closed again at once.
func interactiveClients(t *testing.T, socket string) int {
	t.Helper()
	nc, err := net.DialTimeout("unix", socket, prototest.Timeout)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	r := &rawConn{t: t, nc: nc}
	r.send(helloJSON(proto.KindInteractive))
	m := r.next()
	var w proto.Welcome
	if m.T != proto.TypeWelcome || m.Decode(&w) != nil {
		t.Fatalf("the hello was answered %s", m.Body)
	}
	return w.Clients - 1
}

func TestAFloodWithAStalledClientDoesNotStallTheOthers(t *testing.T) {
	longer(t, 2*time.Minute)
	const stallTimeout = 2 * time.Second
	home, socket := startServer(t, servertest.WithHelper("flood", strconv.Itoa(floodMiB)),
		servertest.WithOptions(func(o *server.Options) {
			o.StallTimeout = stallTimeout
			// Liveness must not be what closes the stalled client.
			o.PingInterval = 5 * time.Minute
		}))
	reader := prototest.Dial(t, socket, prototest.Interactive())
	v := reader.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	stalled := stall(t, socket, v.Session, 300, 200)
	start := time.Now()

	// The reading client follows the flood to its end.
	reader.WaitScreen(p, "the flood's last line, done", func(s golden.Screen) bool {
		rows := plainRows(s)
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i] != "" {
				return rows[i] == "done"
			}
		}
		return false
	})
	t.Logf("%d MiB reached the reading client in %v", floodMiB, time.Since(start))
	if reader.Closed() {
		t.Fatalf("the reading client was closed: %v", reader.Err())
	}

	// The stalled one is closed once its writes were blocked for
	// StallTimeout; the liveness timeout (two ping intervals) is far away.
	testkit.Eventually(t, 10*stallTimeout, "the server closes the stalled client", func() bool {
		return interactiveClients(t, socket) == 1
	})
	t.Logf("the stalled client was closed %v after the flood began", time.Since(start))
	if !waitClosedByPeer(stalled, 10*time.Second) {
		t.Fatal("the server counts the stalled client as gone, but its connection is open")
	}
	// The reading client still works.
	reader.Type(p, "x")
	reader.WaitScreen(p, "the echo of x after done", func(s golden.Screen) bool {
		return slices.Contains(plainRows(s), "x")
	})
	if r := reader.Request(proto.StateGet{}); !r.OK {
		t.Fatalf("state.get after the flood: %+v", r)
	}
}

// rssKiB reads the resident set size of a process. Engine memory is outside
// the Go heap, so memory tests read the resident set and never
// runtime.MemStats (10.1.9).
func rssKiB(pid int) (int, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
				return strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), " kB"))
			}
		}
		return 0, errors.New("no VmRSS line")
	}
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// floodGrowthMiB bounds what the server's resident set may grow by while
// floodMiB pass through one pane of 200 x 50 to a reading and a stalled
// client: the engine's full history of that pane (18 to 19.5 MiB, E10 item
// 13), the frames of two connections and the runtime's slack, but never a
// copy of what passed through, so the bound is the size of the flood.
// Measured on the desktop: about 27 MiB, about 43 MiB under -race (whose
// shadow memory grows with the heap). T10 of 10.4 (P1-19) is the bound of
// record for whole histories.
const floodGrowthMiB = floodMiB

func TestAFloodKeepsServerMemoryBounded(t *testing.T) {
	longer(t, 2*time.Minute)
	home := testkit.ShortTempDir(t)
	dir := work(t, home)
	b := startBoot(t, home, "flood "+strconv.Itoa(floodMiB))
	base, err := rssKiB(b.pid())
	if err != nil {
		t.Fatal(err)
	}
	reader := prototest.Dial(t, b.socket(), prototest.Interactive())
	v := reader.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 200, Rows: 50})
	p := v.Panes[0]
	stall(t, b.socket(), v.Session, 200, 50)

	peak := base
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			if kib, err := rssKiB(b.pid()); err == nil && kib > peak {
				peak = kib
			}
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	reader.WaitScreen(p, "the flood's last line, done", func(s golden.Screen) bool {
		for _, r := range plainRows(s) {
			if r == "done" {
				return true
			}
		}
		return false
	})
	close(done)
	<-sampled
	growth := float64(peak-base) / 1024
	t.Logf("resident set of the server: %d KiB before, %d KiB at the peak (+%.1f MiB) for %d MiB of output", base, peak, growth, floodMiB)
	if growth > floodGrowthMiB {
		t.Fatalf("the server grew by %.1f MiB while %d MiB passed through, more than %d MiB", growth, floodMiB, floodGrowthMiB)
	}
}
