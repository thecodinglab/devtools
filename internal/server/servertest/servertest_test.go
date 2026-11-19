package servertest_test

import (
	"os"
	"path/filepath"
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

// TestMain makes the test binary the panes' program (testkit.RunHelper) and
// keeps the developer's environment away from the servers of the tests.
func TestMain(m *testing.M) {
	testkit.RunHelper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "XDG_") ||
			strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "TMUX") {
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}

func TestStartRunsARealServerWhosePanesRunTheHexHelper(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir)
	if socket != filepath.Join(dir, servertest.SocketName) {
		t.Fatalf("the socket is %s", socket)
	}
	c := prototest.Dial(t, socket, prototest.Interactive())
	if w := c.Welcome(); w.Version != servertest.Version || w.Host != servertest.Host {
		t.Fatalf("welcome %+v", w)
	}
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitFor(p, "ready")
	c.Type(p, "ab")
	c.WaitFor(p, "61 62")
}

func TestWithHelperChoosesThePanesProgram(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithHelper("lines", "3"))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	c.WaitScreen(v.Panes[0], "three numbered lines", func(s golden.Screen) bool {
		return s.Contains("line 1") && s.Contains("line 3")
	})
}

func TestWithOptionsChangesTheServersOptions(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	socket := servertest.Start(t, dir, servertest.WithOptions(func(o *server.Options) {
		o.Version = "9.9.9-options"
		o.SaveDelay = 50 * time.Millisecond
	}))
	c := prototest.Dial(t, socket, prototest.Interactive())
	if v := c.Welcome().Version; v != "9.9.9-options" {
		t.Fatalf("welcome.version %q", v)
	}
	c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	testkit.Eventually(t, 5*time.Second, "state.json is written after the short save delay", func() bool {
		_, err := os.Stat(filepath.Join(dir, "state.json"))
		return err == nil
	})
}

func TestTheServerStopsWhenTheTestEnds(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	var socket string
	t.Run("server", func(t *testing.T) {
		socket = servertest.Start(t, dir)
		c := prototest.Dial(t, socket, prototest.Interactive())
		c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	})
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("the socket is still there after the subtest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatalf("the stop did not save the state: %v", err)
	}
}
