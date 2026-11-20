package e2e

// The scenarios of one machine (S in the table of P3-05b): sessions, tabs
// and splits across a detach (L2), a server restart (L8), no server (L1,
// F45), and the hub's first local row (T1).

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/ui"
)

func TestAttachCreateTabsSplitDetachReattach(t *testing.T) {
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 80, 24), "alpha")

	// a second tab, split once; the right pane prints a line of its own
	x.Key("prefix+c")
	eventually(t, 10*time.Second, "a second tab", func() bool {
		ss, _ := sessionOf(snapshot(t, m), "alpha")
		return len(ss.Tabs) == 2
	})
	x.Key("prefix+%")
	waitText(t, x, 10*time.Second, glyph(ui.GlyphDividerV))
	eventually(t, 10*time.Second, "the second tab is split", func() bool {
		return strings.Contains(layout(snapshot(t, m), "alpha"), "tab 2 right [")
	})
	x.Type("echo right-$((2+3))-pane\r")
	waitText(t, x, 10*time.Second, "right-5-pane")
	right := shellPid(t, x, "before")
	before := layout(snapshot(t, m), "alpha")

	x.Key("prefix+d")
	if code, out := x.Exit(); code != 0 {
		t.Fatalf("detach: exit %d; output:\n%s", code, out)
	}
	eventually(t, 10*time.Second, "no client after the detach", func() bool { return m.Clients() == 0 })
	if got := layout(snapshot(t, m), "alpha"); got != before {
		t.Errorf("the layout changed with the detach:\n%s\nwant\n%s", got, before)
	}

	// provisional: section 4.5 says `devtools attach` in a plain terminal
	// shows the session most recently shown, else the hub; the client of
	// this tree starts on neither (it waits for a pick, reported by
	// P3-05b), so the scenario attaches again by the session's name, the
	// other command that shows a session in this terminal.
	y := Attach(t, m, plain(m, 80, 24), "switch", "alpha")
	waitUntil(t, y, 15*time.Second, "the same tab, split, with the right pane's line", func(rows []string) bool {
		return shows(rows, "", "alpha") && strings.Contains(strings.Join(rows, "\n"), "right-5-pane") &&
			strings.Contains(strings.Join(rows, "\n"), glyph(ui.GlyphDividerV))
	})
	if got := layout(snapshot(t, m), "alpha"); got != before {
		t.Errorf("the layout after attaching again:\n%s\nwant\n%s", got, before)
	}
	if got := shellPid(t, y, "after"); got != right {
		t.Errorf("the right pane's shell is %d after attaching again, was %d: the shells must be the same", got, right)
	}
	if n := attachProcs(t, m, y); n != 1 {
		t.Errorf("%d client processes, want 1", n)
	}
}

func TestServerRestartRestoresLayout(t *testing.T) {
	m := StartMachine(t, "desktop")
	dir := project(t, m, "alpha")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	x.Key("prefix+c")
	x.Key("prefix+%")
	eventually(t, 10*time.Second, "a second tab, split", func() bool {
		return strings.Contains(layout(snapshot(t, m), "alpha"), "tab 2 right [")
	})
	waitText(t, x, 10*time.Second, glyph(ui.GlyphDividerV))
	// The server learns a pane's directory from OSC 7, which the shells of
	// the user send at every prompt (3.2.2); /bin/sh does not, so the pane
	// sends it once by hand.
	x.Type("cd sub && printf '\\033]7;file://localhost%s\\033\\\\' \"$PWD\"\r")
	eventually(t, 10*time.Second, "the right pane's directory is sub", func() bool {
		return strings.Contains(layout(snapshot(t, m), "alpha"), " "+sub+"]")
	})
	oldPid := shellPid(t, x, "before")
	before := layout(snapshot(t, m), "alpha")
	host := m.Welcome().Host

	// The restart of L8 is the service manager's (devtools server restart
	// asks it, F17), which a test does not touch: the server is ended with
	// SIGTERM and started again, which is what the unit does.
	m.Stop()
	m.Start()
	waitText(t, x, 20*time.Second, ui.Notice("restarted", host))
	if got := layout(snapshot(t, m), "alpha"); got != before {
		t.Errorf("the layout after the restart:\n%s\nwant\n%s", got, before)
	}
	waitUntil(t, x, 10*time.Second, "the split tab is shown again", func(rows []string) bool {
		return shows(rows, "", "alpha") && strings.Contains(strings.Join(rows, "\n"), glyph(ui.GlyphDividerV))
	})
	newPid := shellPid(t, x, "after")
	if newPid == oldPid {
		t.Errorf("the shell of the right pane is still %d: the shells must be new", oldPid)
	}
	x.Type("pwd\r")
	waitText(t, x, 10*time.Second, sub)
}

func TestNoServerNamesStartCommand(t *testing.T) {
	m := newMachine(t, "desktop")
	// The words name the start command of this platform; the command line
	// sets ui.StartHint from platform.StartHint() before it prints them, and
	// the environment of the machine names no service, as the test's own
	// has none (TestMain removes DEVTOOLS_SERVICE).
	words := ui.Message("noserver")
	if _, err := lookPath(m.Env(), "tmux"); err == nil {
		words = ui.Message("noserver", "tmux")
	}
	want := strings.Replace(words, ui.StartHint, platform.StartHint(), 1)
	for _, run := range []struct {
		name   string
		config []string
	}{
		{"without a machine line", nil},
		{"with a machine line", []string{"machine macmini"}},
	} {
		t.Run(run.name, func(t *testing.T) {
			if run.config != nil {
				m.Config(run.config...)
			}
			x := Attach(t, m, plain(m, 80, 24))
			code, out := x.Exit()
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			if got := strings.TrimSpace(lines(out)); got != want {
				t.Errorf("output %q, want the noserver words %q", got, want)
			}
			if strings.Contains(rawOutput(x), altScreen) {
				t.Errorf("the output enters the alternate screen: a client started")
			}
			if n := serverProcs(t, m); n != 0 {
				t.Errorf("%d server processes appeared", n)
			}
			if _, err := os.Stat(m.Socket()); err == nil {
				t.Errorf("a socket appeared at %s", m.Socket())
			}
			if n := m.Clients(); n != 0 {
				t.Errorf("%d clients", n)
			}
		})
	}
	if got := m.SSHLog(); len(got) != 0 {
		t.Errorf("ssh ran: %q", got)
	}
}

func TestHubFirstLocalRow(t *testing.T) {
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	project(t, m, "omega")
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	firstRow := func(rows []string) bool { return hubRow(rows, "alpha") || hubRow(rows, "omega") }
	measureP95(t, "T1", "first local row", SamplesP95, func(int) time.Duration {
		start := time.Now()
		x.Key("prefix+o")
		waitUntil(t, x, 5*time.Second, "a local row of the hub", func(rows []string) bool {
			return hubOpen(rows) && firstRow(rows)
		})
		took := time.Since(start)
		closeHub(t, x)
		return took
	})
}

// A tab of three panes: split right, then the right pane split down; the
// bottom right pane grows to the left twice, the second time without the
// prefix (the repeat), and the server restores the grid with its sizes.
func TestAGridOfPanesResizesAndSurvivesARestart(t *testing.T) {
	m := StartMachine(t, "desktop")
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 80, 24), "alpha")
	tabOf := func() proto.Tab {
		s := snapshot(t, m)
		ss, _ := sessionOf(s, "alpha")
		for _, tb := range s.Tabs {
			if tb.ID == ss.Current {
				return tb
			}
		}
		return proto.Tab{}
	}
	x.Key("prefix+%")
	eventually(t, 10*time.Second, "two panes", func() bool { return len(tabOf().Panes) == 2 })
	x.Key("prefix+-")
	eventually(t, 10*time.Second, "three panes", func() bool { return len(tabOf().Panes) == 3 })
	// 80 columns: 40, the divider, 39; 23 rows on the right: 11, the
	// divider, 11
	dividerAt := func(rows []string) int {
		if len(rows) == 0 {
			return -1
		}
		return strings.Index(string([]rune(rows[0])), glyph(ui.GlyphDividerV))
	}
	runeCol := func(rows []string) int {
		i := dividerAt(rows)
		if i < 0 {
			return -1
		}
		return len([]rune(rows[0][:i]))
	}
	waitUntil(t, x, 10*time.Second, "a grid of three panes", func(rows []string) bool {
		return runeCol(rows) == 40 && strings.Contains(strings.Join(rows, "\n"), glyph(ui.GlyphDividerH))
	})

	x.Key("prefix+H")
	x.Key("H")
	waitUntil(t, x, 10*time.Second, "the right panes 10 columns wider", func(rows []string) bool { return runeCol(rows) == 30 })
	before := tabOf()
	if l := before.Layout; l.Split != proto.SplitRight || len(l.Nodes) != 2 || l.Nodes[1].Split != proto.SplitDown {
		t.Fatalf("layout %+v, want p1 | (p2 / p3)", l)
	}

	// After the repeat time H is the pane's again.
	time.Sleep(1500 * time.Millisecond)
	x.Type("echo H-is-typed\r")
	waitText(t, x, 10*time.Second, "H-is-typed")

	m.Stop()
	m.Start()
	waitText(t, x, 20*time.Second, ui.Notice("restarted", m.Welcome().Host))
	if after := tabOf(); !reflect.DeepEqual(after.Layout, before.Layout) {
		t.Fatalf("the layout after the restart\n%+v\nwant\n%+v", after.Layout, before.Layout)
	}
	waitUntil(t, x, 10*time.Second, "the grid is shown again", func(rows []string) bool { return runeCol(rows) == 30 })
}
