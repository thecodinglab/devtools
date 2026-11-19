package integration

// The harness of level 5 (10.1.1): three processes, the server (a boot
// server, main_test.go), the client (the helper attach, on a PTY) and the
// test as the outer terminal (termtest, a vt.Term on the PTY master).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/testkit/termtest"
)

// The bytes an outer terminal sends for the keys the tests press. bubbletea
// asks the outer terminal for key disambiguation (3.2.4), so ctrl+space
// arrives as its kitty form.
const (
	keyPrefix = "\x1b[32;5u" // ctrl+space
	keyCtrlH  = "\x08"
	keyCtrlL  = "\x0c"
)

// l5 is one scenario of level 5: a home directory, the directory of the
// session, the directory helpers leave files in, and the boot server.
type l5 struct {
	t       *testing.T
	home    string
	dir     string // the session's directory, home/work
	testDir string
	srv     *bootServer
}

// newL5 starts a boot server whose panes run paneSpec, in a new home.
func newL5(t *testing.T, paneSpec string, extra ...string) *l5 {
	t.Helper()
	testkit.RequirePTY(t)
	home := testkit.ShortTempDir(t)
	s := &l5{t: t, home: home, dir: work(t, home), testDir: filepath.Join(home, "test")}
	if err := os.MkdirAll(s.testDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if paneSpec != "" {
		s.srv = startBoot(t, home, paneSpec, append([]string{envTestDir + "=" + s.testDir}, extra...)...)
	}
	return s
}

// attachEnv is the environment of the attach helper in this home, with the
// further variables extra.
func (s *l5) attachEnv(extra ...string) []string {
	_, helper := testkit.HelperCommand("attach")
	return testkit.CleanEnv(s.home, append(append(helper, "GORACE=atexit_sleep_ms=0", "TERM=xterm-256color"), extra...)...)
}

// attach starts the client on a new outer terminal of cols x rows that shows
// the session of s.dir.
func (s *l5) attach(cols, rows int, extra ...string) *termtest.Terminal {
	s.t.Helper()
	return s.attachWith(termtest.Options{Cols: cols, Rows: rows}, extra...)
}

// attachWith starts the client on an outer terminal of the options o; its
// environment is attachEnv with extra.
func (s *l5) attachWith(o termtest.Options, extra ...string) *termtest.Terminal {
	s.t.Helper()
	argv, _ := testkit.HelperCommand("attach", s.dir, "work")
	o.Env = s.attachEnv(extra...)
	return termtest.Start(s.t, argv, o)
}

// longerScreens raises termtest.Timeout for the rest of the test.
func longerScreens(t *testing.T, d time.Duration) {
	old := termtest.Timeout
	termtest.Timeout = d
	t.Cleanup(func() { termtest.Timeout = old })
}

// statusRow is the last row of a screen: the status row of chrome.Plain.
func statusRow(s golden.Screen) string {
	if len(s.Lines) == 0 {
		return ""
	}
	return golden.PlainRow(s.Lines[len(s.Lines)-1])
}

// shows waits until the client draws its status row with the session's label
// and the program's first line ("ready" of hex) above it.
func shows(x *termtest.Terminal, text string) golden.Screen {
	return x.WaitScreen("the session with "+text+" and its status row", func(s golden.Screen) bool {
		return strings.Contains(statusRow(s), "work") && paneHas(s, text)
	})
}

// paneHas reports whether a row of the pane area (every row but the status
// row) contains text.
func paneHas(s golden.Screen, text string) bool {
	rows := plainRows(s)
	for _, r := range rows[:max(len(rows)-1, 0)] {
		if strings.Contains(r, text) {
			return true
		}
	}
	return false
}

// hexIn returns what the helper hex printed in the columns [from, to) of the
// pane area, after its line "ready": the hex digits without the blanks, so
// that the wrap of a row does not matter.
func hexIn(s golden.Screen, from, to int) string {
	rows := plainRows(s)
	var b strings.Builder
	started := false
	for _, r := range rows[:max(len(rows)-1, 0)] {
		cells := []rune(r)
		lo, hi := min(from, len(cells)), min(to, len(cells))
		part := strings.TrimSpace(string(cells[lo:hi]))
		if !started {
			// The line discipline may echo keys typed before hex switched
			// its terminal to raw mode in front of "ready".
			if strings.Contains(part, "ready") {
				started = true
			}
			continue
		}
		b.WriteString(strings.ReplaceAll(part, " ", ""))
	}
	return b.String()
}

// hexOf is hexIn over the whole width.
func hexOf(s golden.Screen) string { return hexIn(s, 0, s.Cols) }

// hexBytes is what hex prints for b, without blanks.
func hexBytes(b string) string { return fmt.Sprintf("%x", b) }

// waitHex waits until hex printed want (without blanks) in the columns
// [from, to) and returns the screen.
func waitHex(x *termtest.Terminal, what string, from, to int, want string) golden.Screen {
	return x.WaitScreen(what, func(s golden.Screen) bool { return hexIn(s, from, to) == want })
}

// oneshot is a one-shot connection without a pane, for state.get.
func oneshot(t *testing.T, socket string) *prototest.Client {
	t.Helper()
	return prototest.Dial(t, socket, prototest.Oneshot(""))
}

// stateGet asks the server for its state.
func stateGet(t *testing.T, o *prototest.Client) proto.Snapshot {
	t.Helper()
	r := o.Request(proto.StateGet{})
	if !r.OK || r.Msg.T != proto.TypeStateSnapshot {
		t.Fatalf("state.get answered %+v", r)
	}
	var snap proto.Snapshot
	if err := r.Msg.Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// paneOf returns the pane of a snapshot.
func paneOf(snap proto.Snapshot, id string) proto.Pane {
	for _, p := range snap.Panes {
		if p.ID == id {
			return p
		}
	}
	return proto.Pane{}
}

// waitEditor waits until the server's process scan found the one pane of
// the state to be an editor, and returns its id.
func waitEditor(t *testing.T, o *prototest.Client) string {
	t.Helper()
	var id string
	testkit.Eventually(t, 15*time.Second, "the scan finds the editor", func() bool {
		snap := stateGet(t, o)
		if len(snap.Panes) == 0 {
			return false
		}
		id = snap.Panes[0].ID
		return snap.Panes[0].Editor
	})
	return id
}

// press types the prefix, then key.
func press(x *termtest.Terminal, key string) {
	x.Type(keyPrefix)
	x.Type(key)
}
