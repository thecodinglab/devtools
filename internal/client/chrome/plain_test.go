package chrome

import (
	"os"
	"strings"
	"testing"
	"time"

	"devtools/internal/client/screen"
	"devtools/internal/testkit/golden"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket or a multiplexer reaches them. The chrome
// reads no environment; this is the convention of 10.1.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-chrome-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// screenOf is the golden screen of a canvas.
func screenOf(c *screen.Canvas) golden.Screen {
	w, h := c.Size()
	return golden.Screen{Cols: w, Rows: h, Lines: c.Lines()}
}

// plainState is a window on this machine with three tabs, one agent that
// needs the user, and two other machines.
func plainState(w, h int) State {
	return State{
		Width: w, Height: h,
		WindowFocused: true,
		Mode:          "normal",
		View:          "showing",
		Session:       "devtools/main",
		Tabs: []Tab{
			{ID: "t1", Index: 1, Title: "zsh", Current: true},
			{ID: "t2", Index: 2, Title: "nvim"},
			{ID: "t3", Index: 3, Title: "claude", Agent: "blocked", Bell: true},
		},
		Summary: Summary{
			Top:    "blocked",
			Counts: map[string]int{"blocked": 1, "done": 2, "working": 1},
			Machines: []Machine{
				{Name: "desktop", State: "online"},
				{Name: "macmini", State: "online"},
				{Name: "laptop", State: "attention", Problem: "host-key"},
			},
		},
		Now: time.Date(2026, 11, 17, 14, 32, 0, 0, time.UTC),
	}
}

func TestPlainStatusRow(t *testing.T) {
	cases := []struct {
		name string
		w    int
		edit func(*State)
	}{
		{"normal-80", 80, func(*State) {}},
		{"remote-80", 80, func(s *State) { s.Machine = "macmini" }},
		{"notice-80", 80, func(s *State) { s.Notice = "No binding for ctrl+q" }},
		{"prefix-80", 80, func(s *State) { s.Mode = "prefix" }},
		{"prefix-hints-80", 80, func(s *State) { s.Mode, s.Hints = "prefix", true }},
		{"switching-80", 80, func(s *State) {
			s.Mode, s.Target = "switching", Target{Machine: "macmini", Session: "api/main"}
		}},
		{"copy-80", 80, func(s *State) {
			s.Mode, s.Copy = "copy", &Copy{Line: 7412, Total: 10000, Selecting: "line"}
		}},
		{"scrolled-80", 80, func(s *State) { s.Mode, s.Copy = "scrolled", &Copy{Line: 12, Total: 340} }},
		{"confirm-80", 80, func(s *State) { s.Mode, s.Confirm = "confirm", &Confirm{Program: "nvim"} }},
		{"normal-120", 120, func(*State) {}},
		{"normal-40", 40, func(*State) {}},
		{"normal-20", 20, func(*State) {}},
		{"empty-80", 80, func(s *State) {
			s.Session, s.Tabs, s.Summary, s.Now = "", nil, Summary{}, time.Time{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := plainState(tc.w, 24)
			tc.edit(&s)
			c := screen.New(tc.w, 1)
			Plain{}.Status(c, s)
			golden.Assert(t, "plain-status-"+tc.name, screenOf(c).Runs())
		})
	}
}

func TestPlainStatusDrawsOnlyItsRow(t *testing.T) {
	c := screen.New(30, 3)
	c.Text(0, 0, "above", screen.Style{}, 30)
	Plain{}.Status(c.Sub(screen.Rect{X: 0, Y: 2, W: 30, H: 1}), plainState(30, 3))
	if got := c.Lines()[0]; got != "above" {
		t.Errorf("row 0 = %q", got)
	}
	if got := c.Lines()[2]; got == "" {
		t.Errorf("the status row is empty")
	}
}

func TestPlainTooSmall(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{{"19x6", 19, 6}, {"40x5", 40, 5}, {"10x3", 10, 3}, {"30x1", 30, 1}} {
		t.Run(size.name, func(t *testing.T) {
			c := screen.New(size.w, size.h)
			c.Fill(screen.Rect{X: 0, Y: 0, W: size.w, H: size.h}, screen.Cell{S: "#", W: 1})
			Plain{}.TooSmall(c, plainState(size.w, size.h))
			golden.Assert(t, "plain-toosmall-"+size.name, screenOf(c).Plain())
		})
	}
}

func TestPlainBannerCoversOneRow(t *testing.T) {
	c := screen.New(60, 4)
	for y := range 4 {
		c.Text(0, y, "pane content", screen.Style{}, 60)
	}
	s := plainState(60, 5)
	if rows := (Plain{}).Banner(c, s); rows != 0 {
		t.Fatalf("Banner of a showing view = %d rows, want 0", rows)
	}
	if got := c.Lines()[0]; got != "pane content" {
		t.Fatalf("Banner of a showing view drew %q", got)
	}

	s.View, s.Machine = "stale", "macmini"
	s.Summary.Machines[1] = Machine{Name: "macmini", State: "retrying", Problem: "lost", Retry: "retrying"}
	if rows := (Plain{}).Banner(c, s); rows != 1 {
		t.Fatalf("Banner of a stale view = %d rows, want 1", rows)
	}
	lines := c.Lines()
	want := " macmini  not reachable. Retrying. Typing is paused."
	if got := golden.PlainRow(lines[0]); got != want {
		t.Errorf("banner = %q, want %q", got, want)
	}
	for y := 1; y < 4; y++ {
		if lines[y] != "pane content" {
			t.Errorf("row %d = %q: the banner covers more than its row", y, lines[y])
		}
	}
	for x := range 60 {
		if c.At(x, 0).Style.Attrs&screen.Reverse == 0 {
			t.Fatalf("banner cell %d is not reverse: it does not cover the whole width", x)
		}
	}

	// narrower: the sentence leaves first, then the phrase is cut
	for _, tc := range []struct {
		w    int
		want string
	}{
		{40, " macmini  not reachable. Retrying."},
		{25, " macmini  not reachab..."},
		{6, " m..."},
		{3, " m"},
	} {
		c := screen.New(tc.w, 2)
		if rows := (Plain{}).Banner(c, s); rows != 1 {
			t.Errorf("%d columns: rows = %d", tc.w, rows)
		}
		if got := golden.PlainRow(c.Lines()[0]); got != tc.want {
			t.Errorf("%d columns: banner = %q, want %q", tc.w, got, tc.want)
		}
	}
}

func TestPlainPaneMessage(t *testing.T) {
	c := screen.New(40, 4)
	for y := range 4 {
		c.Text(0, y, strings.Repeat("x", 40), screen.Style{}, 40)
	}
	Plain{}.PaneMessage(c, plainState(40, 5), "nvim ended with\x1b[31m status 1.")
	lines := c.Lines()
	want := []string{"nvim ended with[31m status 1.", "Press enter to start it again.", strings.Repeat("x", 40), strings.Repeat("x", 40)}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, lines[i], want[i])
		}
	}
	// a pane of one row gets the first line only, and nothing panics
	c = screen.New(10, 1)
	Plain{}.PaneMessage(c, plainState(10, 2), "ended")
	if got := c.Lines()[0]; got != "ended" {
		t.Errorf("one row = %q", got)
	}
}

func TestPlainDividerPointsAtThePaneWithTheKeys(t *testing.T) {
	s := plainState(80, 24)
	c := screen.New(1, 5)
	Plain{}.Divider(c, s, true, middle(c, true, true))
	if got := strings.Join(c.Lines(), ""); got != "||\x1b[0;1m<\x1b[0m||" {
		t.Errorf("vertical divider = %q", got)
	}
	c = screen.New(5, 1)
	Plain{}.Divider(c, s, false, middle(c, false, false))
	if got := golden.PlainRow(c.Lines()[0]); got != "--v--" {
		t.Errorf("horizontal divider = %q", got)
	}
	s.View = "stale"
	Plain{}.Divider(c, s, false, middle(c, false, true))
	if got := c.Lines()[0]; got != "-----" {
		t.Errorf("divider of a stale view = %q, want no pointer", got)
	}
	s.View, s.WindowFocused = "showing", false
	Plain{}.Divider(c, s, false, middle(c, false, true))
	if got := c.Lines()[0]; got != "-----" {
		t.Errorf("divider of an unfocused window = %q, want no pointer", got)
	}
}

func TestPlainStyles(t *testing.T) {
	p := Plain{}
	s := plainState(80, 24)
	if c, r := p.MinSize(); c != 20 || r != 6 {
		t.Errorf("MinSize = %d, %d", c, r)
	}
	if p.StatusRows() != 1 {
		t.Errorf("StatusRows = %d", p.StatusRows())
	}
	red := screen.Style{Fg: screen.Color{Kind: 1, V: 1}, Attrs: screen.Bold, Link: "https://x"}
	if got := p.Selection(s, red); got.Attrs&screen.Reverse == 0 || got.Fg != red.Fg {
		t.Errorf("Selection = %+v", got)
	}
	if got := p.CopyCursor(s, red); got.Attrs&screen.Reverse == 0 || got.Underline == 0 {
		t.Errorf("CopyCursor = %+v", got)
	}
	if got := p.Inactive(s, red); got != (screen.Style{Attrs: screen.Faint}) {
		t.Errorf("Inactive = %+v", got)
	}
	c := screen.New(3, 2)
	p.Pad(c, s)
	if got := c.Lines(); got[0] != "..." || got[1] != "..." {
		t.Errorf("Pad = %q", got)
	}
}
