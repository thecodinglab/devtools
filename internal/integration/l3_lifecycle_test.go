package integration

// Level 3: the pane lifecycle of 3.3 and the stop of 3.9, with real
// programs: a signal leaves an exited pane, Enter starts it again, a normal
// exit closes it, a closed pane's group gets SIGHUP, and a stopped server
// leaves no process behind.

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit"
)

func TestASignalledProgramLeavesAnExitedPaneAndEnterRespawnsIt(t *testing.T) {
	scratch := testkit.ShortTempDir(t)
	marker := filepath.Join(scratch, "killed")
	home, socket := startServer(t, servertest.WithHelper("killonce", marker))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]

	st := c.WaitState("the pane exited", func(s proto.State) bool { return s.Panes[p].State == proto.PaneExited })
	pane := st.Panes[p]
	// exit.text is for a program that could not be started (3.3).
	if pane.Exit == nil || pane.Exit.Signal != "SIGTERM" || pane.Exit.Code != -1 {
		t.Fatalf("the exited pane carries %+v, not the signal SIGTERM", pane.Exit)
	}
	// The pane stays in its tab, and its session stays.
	if _, ok := st.Sessions[v.Session]; !ok || len(st.Tabs[pane.Tab].Panes) != 1 {
		t.Fatalf("after the exit the state is %+v", st)
	}

	// Another key does nothing; Enter starts the program again, with the
	// same id, in the same tab.
	c.Type(p, "x")
	c.Key(p, "enter", 0, "")
	st = c.WaitState("the pane runs again", func(s proto.State) bool { return s.Panes[p].State == proto.PaneRunning })
	if st.Panes[p].Exit != nil || st.Panes[p].Tab != pane.Tab {
		t.Fatalf("the respawned pane is %+v", st.Panes[p])
	}
	c.WaitFor(p, "respawned")
}

func TestANormalExitClosesPaneTabAndSession(t *testing.T) {
	home, socket := startServer(t)
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	first := v.Panes[0]
	r := c.Request(proto.PaneSplit{Pane: first, Direction: proto.SplitRight})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	var sp proto.PaneSplitResult
	r.Msg.Decode(&sp)
	c.WaitFor(sp.Pane, "ready")
	c.WaitFor(first, "ready")

	// The hex helper exits 0 after three q: the pane goes, its tab stays
	// with the other one.
	c.Type(sp.Pane, "qqq")
	st := c.WaitState("the second pane is gone", func(s proto.State) bool {
		_, ok := s.Panes[sp.Pane]
		return !ok
	})
	tab := st.Tabs[st.Panes[first].Tab]
	if len(tab.Panes) != 1 || tab.Panes[0] != first || tab.Split != "" || tab.Focused != first {
		t.Fatalf("the tab after the exit: %+v", tab)
	}

	// The last pane exits: its tab and its session go, and the viewer gets
	// view.ended after the delta that deleted the session.
	from := len(c.Received())
	c.Type(first, "qqq")
	ended := c.WaitMessage(proto.TypeViewEnded, from)
	var e proto.ViewEnded
	ended.Decode(&e)
	if e.Session != v.Session {
		t.Fatalf("view.ended names %+v", e)
	}
	st = c.State()
	if len(st.Sessions) != 0 || len(st.Tabs) != 0 || len(st.Panes) != 0 {
		t.Fatalf("after the last exit the state holds %+v", st)
	}
}

func TestPaneChildrenReceiveSighup(t *testing.T) {
	home := testkit.ShortTempDir(t)
	dir := work(t, home)
	marks := testkit.ShortTempDir(t)
	// The server is started the way nohup starts a program, with SIGHUP
	// ignored. Its start step 8 handles SIGHUP instead of ignoring it, so
	// the programs of its panes do not inherit an ignored SIGHUP (3.9).
	signal.Ignore(syscall.SIGHUP)
	b := startBoot(t, home, "hupcheck", envTestDir+"="+marks)
	signal.Reset(syscall.SIGHUP)

	c := prototest.Dial(t, b.socket(), prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	r := c.Request(proto.PaneSplit{Pane: v.Panes[0], Direction: proto.SplitRight})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	var sp proto.PaneSplitResult
	r.Msg.Decode(&sp)
	pids := map[string]int{}
	for _, p := range []string{v.Panes[0], sp.Pane} {
		s := c.WaitFor(p, "sighup-ignored=")
		line, _ := field(s, "sighup-ignored")
		var ignored bool
		var pid int
		if _, err := fmt.Sscanf(line, "%t pid=%d", &ignored, &pid); err != nil {
			t.Fatalf("pane %s printed %q: %v", p, line, err)
		}
		if ignored {
			t.Errorf("the program of pane %s was started with SIGHUP ignored", p)
		}
		pids[p] = pid
	}

	// Closing a pane sends SIGHUP to its process group first (3.2.1).
	if r := c.Request(proto.PaneClose{Pane: sp.Pane}); !r.OK {
		t.Fatalf("pane.close: %s: %s", r.Code, r.Text)
	}
	got := filepath.Join(marks, strconv.Itoa(pids[sp.Pane])+".SIGHUP")
	testkit.Eventually(t, 5*time.Second, "the closed pane's program records SIGHUP", func() bool {
		_, err := os.Stat(got)
		return err == nil
	})

	// The stop closes the remaining pane the same way.
	if code := b.stop(); code != 0 {
		t.Fatalf("the server exited %d: %s", code, b.log.String())
	}
	got = filepath.Join(marks, strconv.Itoa(pids[v.Panes[0]])+".SIGHUP")
	testkit.Eventually(t, 5*time.Second, "the last pane's program records SIGHUP", func() bool {
		_, err := os.Stat(got)
		return err == nil
	})
	if left, _ := filepath.Glob(filepath.Join(marks, "*.SIGTERM")); len(left) != 0 {
		t.Fatalf("a program needed SIGTERM: %v", left)
	}
}

func TestShutdownLeavesNoProcessBehind(t *testing.T) {
	home := testkit.ShortTempDir(t)
	dir := work(t, home)
	b := startBoot(t, home, "pidwait")
	c := prototest.Dial(t, b.socket(), prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: dir, Name: "work", Cols: 80, Rows: 24})
	panes := []string{v.Panes[0]}
	r := c.Request(proto.PaneSplit{Pane: v.Panes[0], Direction: proto.SplitDown})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	var sp proto.PaneSplitResult
	r.Msg.Decode(&sp)
	panes = append(panes, sp.Pane)

	var pids []int
	collect := func(c *prototest.Client, p string) {
		s := c.WaitFor(p, "pids ")
		line, _ := field(s, "pids")
		var own, child int
		if _, err := fmt.Sscanf(line, "%d %d", &own, &child); err != nil {
			t.Fatalf("pane %s printed %q: %v", p, line, err)
		}
		pids = append(pids, own, child)
	}
	// The panes of the first tab, before a new tab hides them; they keep
	// running.
	for _, p := range panes {
		collect(c, p)
	}
	r = c.Request(proto.TabNew{Session: v.Session})
	if !r.OK {
		t.Fatalf("tab.new: %s: %s", r.Code, r.Text)
	}
	var tn proto.TabNewResult
	r.Msg.Decode(&tn)
	collect(c, tn.Pane)
	// A second session, displayed by a second client.
	other := prototest.Dial(t, b.socket(), prototest.Interactive())
	ov := other.View(proto.ViewSet{Dir: work(t, home, "other"), Name: "other", Cols: 80, Rows: 24})

	collect(other, ov.Panes[0])
	for _, pid := range pids {
		if !alive(pid) {
			t.Fatalf("process %d is not running before the stop", pid)
		}
	}

	if code := b.stop(); code != 0 {
		t.Fatalf("the server exited %d: %s", code, b.log.String())
	}
	if _, err := os.Stat(b.socket()); !os.IsNotExist(err) {
		t.Fatalf("the socket is still there: %v", err)
	}
	testkit.Eventually(t, 5*time.Second, fmt.Sprintf("the pane programs and their children %v are gone", pids), func() bool {
		for _, pid := range pids {
			if alive(pid) {
				return false
			}
		}
		return true
	})
	c.WaitClosed()
	other.WaitClosed()
}
