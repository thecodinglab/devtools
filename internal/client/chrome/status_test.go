package chrome

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"devtools/internal/client/keys"
	"devtools/internal/client/screen"
	"devtools/internal/config"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"
)

// statusTokens are the five colour lines the systems config generates from
// palette.nix (5a.7.1): the truecolor tier of the goldens, dark values.
var statusTokens = []ui.Token{
	{Name: "strong", Light: 0x111214, Dark: 0xe6e7eb},
	{Name: "muted", Light: 0x55585f, Dark: 0xa6a8b0},
	{Name: "raised", Light: 0xe3e4e8, Dark: 0x30323a},
	{Name: "selection", Light: 0xd6dde9, Dark: 0x3a4150},
	{Name: "warning", Light: 0xb04a00, Dark: 0xec8a3f},
}

// statusThemes are the truecolor and the palette tier with the glyphs of
// utf8.
func statusThemes(t *testing.T, utf8 bool) (truecolor, palette ui.Theme) {
	t.Helper()
	tc, err := ui.NewTheme(ui.ThemeOptions{Tokens: statusTokens, Dark: true, Known: true, Colors: 1 << 24, UTF8: utf8})
	if err != nil {
		t.Fatal(err)
	}
	// 256 colours are not 24-bit: the palette tier, the tokens unused.
	pal, err := ui.NewTheme(ui.ThemeOptions{Tokens: statusTokens, Dark: true, Known: true, Colors: 256, UTF8: utf8})
	if err != nil {
		t.Fatal(err)
	}
	return tc, pal
}

// statusLook is the look with the default binding table.
func statusLook() *look {
	return NewLook(LookOptions{Chords: keys.Defaults().Chords}).(*look)
}

var statusNow = time.Date(2026, 11, 17, 14, 32, 0, 0, time.UTC)

// statusStandard is the state of chrome-single-80x24 with tab 2 current:
// a session of this machine, three tabs, an agent that needs the user in
// tab 2, macmini online.
func statusStandard() State {
	return State{
		Width: 80, Height: 24,
		WindowFocused: true,
		Mode:          "normal",
		View:          "showing",
		Session:       "devtools-main",
		Tabs: []Tab{
			{ID: "t1", Index: 1, Title: "zsh"},
			{ID: "t2", Index: 2, Title: "claude", Agent: "blocked", Current: true},
			{ID: "t3", Index: 3, Title: "nvim"},
		},
		Summary: Summary{
			Top:    "blocked",
			Counts: map[string]int{"blocked": 1, "done": 1, "working": 2},
			Machines: []Machine{
				{Name: "desktop", State: "online", Agents: map[string]int{"blocked": 1, "working": 2}},
				{Name: "macmini", State: "online", Agents: map[string]int{"done": 1}},
			},
		},
		Now: statusNow,
	}
}

// statusRemote is a session of macmini on screen, tab 2 current with a
// working agent.
func statusRemote() State {
	s := statusStandard()
	s.Machine, s.Session = "macmini", "notes-main"
	s.Tabs = []Tab{
		{ID: "t1", Index: 1, Title: "zsh"},
		{ID: "t2", Index: 2, Title: "claude", Agent: "working", Current: true},
	}
	return s
}

// statusStale is chrome-stale-80x24: the session of macmini on screen and
// macmini lost; its agents are not counted. macbook is configured and off
// with nothing busy, so it is not listed.
func statusStale() State {
	s := statusStandard()
	s.View, s.Machine, s.Session = "stale", "macmini", "notes-main"
	s.Tabs = []Tab{
		{ID: "t1", Index: 1, Title: "zsh", Current: true},
		{ID: "t2", Index: 2, Title: "claude"},
	}
	s.Summary = Summary{
		Top:    "blocked",
		Counts: map[string]int{"blocked": 1, "working": 1},
		Machines: []Machine{
			{Name: "desktop", State: "online", Agents: map[string]int{"blocked": 1, "working": 1}},
			{Name: "macmini", State: "retrying", Problem: "lost", Retry: "retrying"},
			{Name: "macbook", State: "off"},
		},
	}
	return s
}

// statusNone is a plain terminal that started in the hub: no session is
// displayed, the two other machines are connecting.
func statusNone() State {
	s := statusStandard()
	s.View, s.Session, s.Tabs = "none", "", nil
	s.Summary = Summary{Machines: []Machine{
		{Name: "desktop", State: "online"},
		{Name: "macmini", State: "connecting"},
		{Name: "macbook", State: "connecting"},
	}}
	return s
}

// statusScreens draws s at width cells in the truecolor and the palette
// tier.
func statusScreens(t *testing.T, l *look, s State, width int, utf8 bool) (truecolor, palette golden.Screen) {
	t.Helper()
	tc, pal := statusThemes(t, utf8)
	draw := func(th ui.Theme) golden.Screen {
		s.Theme, s.Width = th, width
		c := screen.New(width, 1)
		l.Status(c, s)
		return golden.Screen{Cols: width, Rows: 1, Lines: c.Lines()}
	}
	return draw(tc), draw(pal)
}

// statusGolden asserts the three layers of the golden id.
func statusGolden(t *testing.T, id string, s State, width int) {
	t.Helper()
	statusGoldenWith(t, statusLook(), id, s, width, true)
}

func statusGoldenWith(t *testing.T, l *look, id string, s State, width int, utf8 bool) {
	t.Helper()
	tc, pal := statusScreens(t, l, s, width, utf8)
	golden.Look(t, id, tc, pal)
}

// statusText is the row of s at width cells in the truecolor tier, without
// styles.
func statusText(t *testing.T, l *look, s State, width int) string {
	t.Helper()
	tc, _ := statusScreens(t, l, s, width, true)
	return tc.Row(0)
}

func TestStatusRowAtEveryWidth(t *testing.T) {
	for _, w := range []int{135, 80, 60, 40, 20} {
		statusGolden(t, fmt.Sprintf("status-standard-%d", w), statusStandard(), w)
	}
}

func TestStatusRowTabMarks(t *testing.T) {
	s := statusStandard()
	s.Tabs = []Tab{
		{ID: "t1", Index: 1, Title: "zsh", Bell: true, Current: true},
		{ID: "t2", Index: 2, Title: "claude", Agent: "blocked"},
		{ID: "t3", Index: 3, Title: "claude", Agent: "done"},
		{ID: "t4", Index: 4, Title: "codex", Agent: "working"},
		{ID: "t5", Index: 5, Title: "nvim"},
	}
	statusGolden(t, "status-tabs-135", s, 135)
}

func TestStatusRowFleetStates(t *testing.T) {
	desktop := Machine{Name: "desktop", State: "online"}
	notCounted := map[string]int{"blocked": 1, "working": 1}
	cases := []struct {
		id       string
		machines []Machine
		counts   map[string]int
	}{
		{"status-fleet-connecting-135", []Machine{desktop,
			{Name: "macmini", State: "online"}, {Name: "macbook", State: "connecting"}}, nil},
		{"status-fleet-retrying-135", []Machine{desktop,
			{Name: "macmini", State: "retrying", Problem: "unreachable", Retry: "retrying"}}, notCounted},
		{"status-fleet-attention-135", []Machine{desktop,
			{Name: "macmini", State: "attention", Problem: "auth", Reason: "Permission denied (publickey)."}}, notCounted},
		{"status-fleet-two-135", []Machine{desktop,
			{Name: "macmini", State: "attention", Problem: "host-key"}, {Name: "macbook", State: "connecting"}}, notCounted},
		{"status-fleet-off-135", []Machine{desktop,
			{Name: "macmini", State: "online"}, {Name: "macbook", State: "off", Busy: 1}}, nil},
	}
	for _, tc := range cases {
		s := statusStandard()
		s.Summary.Machines = tc.machines
		if tc.counts != nil {
			s.Summary.Counts = tc.counts
		}
		statusGolden(t, tc.id, s, 135)
	}
}

func TestRollupNamesTheMostUrgentAgentWhenThereIsRoom(t *testing.T) {
	s := statusStandard()
	s.Summary.Urgent = Target{Machine: "macmini", Session: "devtools-main"}
	statusGolden(t, "status-urgent-160", s, 160)

	// At 135 columns the place is gone and nothing else changed.
	l := statusLook()
	got, gotPal := statusScreens(t, l, s, 135, true)
	want, wantPal := statusScreens(t, l, statusStandard(), 135, true)
	if got.Runs() != want.Runs() || gotPal.Runs() != wantPal.Runs() {
		t.Errorf("at 135 columns the row with the most urgent agent is\n%s\nwant the standard row\n%s", got.Runs(), want.Runs())
	}
	if p := reduce(withTheme(t, s), 135); p.urgent || p.steps != 0 {
		t.Errorf("plan at 135 = %+v, want only the place left out", p)
	}

	// More than one done agent: "first in".
	s.Summary.Counts["done"] = 2
	if row := statusText(t, l, s, 200); !strings.Contains(row, "✓ 2 done first in macmini devtools-main") {
		t.Errorf("two done agents: %q", row)
	}
	// The agent on this machine: the machine is left out.
	s.Summary.Urgent = Target{Machine: "desktop", Session: "api-main"}
	if row := statusText(t, l, s, 200); !strings.Contains(row, "✓ 2 done first in api-main ") {
		t.Errorf("an agent of this machine: %q", row)
	}
	// A blocked agent that is not on screen is the most urgent one.
	s.Summary.Counts["blocked"] = 2
	if row := statusText(t, l, s, 200); !strings.Contains(row, "◆ 2 need you first in api-main ") {
		t.Errorf("a blocked agent elsewhere: %q", row)
	}
}

// withTheme gives s the truecolor theme.
func withTheme(t *testing.T, s State) State {
	t.Helper()
	s.Theme, _ = statusThemes(t, true)
	return s
}

func TestStatusRowShowsARemoteSessionAsAChip(t *testing.T) {
	for _, w := range []int{80, 40, 20} {
		statusGolden(t, fmt.Sprintf("status-remote-%d", w), statusRemote(), w)
	}
	// The chip starts at column 0 and takes the raised ground in the
	// truecolor tier, the brackets in the palette tier.
	tc, pal := statusScreens(t, statusLook(), statusRemote(), 80, true)
	if !strings.HasPrefix(tc.Row(0), " macmini  notes-main") || !strings.HasPrefix(pal.Row(0), "[macmini] notes-main") {
		t.Errorf("chip: %q and %q", tc.Row(0), pal.Row(0))
	}
}

func TestStatusRowWithNoSession(t *testing.T) {
	statusGolden(t, "status-none-80", statusNone(), 80)
}

func TestStatusRowInASCII(t *testing.T) {
	statusGoldenWith(t, statusLook(), "status-ascii-80", statusStandard(), 80, false)
}

func TestPrefixBlockKeepsTabsInPlace(t *testing.T) {
	cases := []struct {
		id string
		s  State
	}{
		{"status-prefix-80", statusStandard()},
		{"status-prefix-stale-80", statusStale()},
		{"status-prefix-none-80", statusNone()},
	}
	l := statusLook()
	for _, tc := range cases {
		prefix := tc.s
		prefix.Mode = "prefix"
		statusGolden(t, tc.id, prefix, 80)

		for _, utf8 := range []bool{true, false} {
			nTC, nPal := statusScreens(t, l, tc.s, 80, utf8)
			pTC, pPal := statusScreens(t, l, prefix, 80, utf8)
			for tier, pair := range map[string][2]golden.Screen{"truecolor": {nTC, pTC}, "palette": {nPal, pPal}} {
				normal, pre := drawn(pair[0]), drawn(pair[1])
				block := 0
				for block < len(pre) && pre[block].Style.Attrs&screen.Reverse != 0 {
					block++
				}
				address := addressWidth(withTheme(t, tc.s), 80)
				if block != max(address, 8) {
					t.Errorf("%s %s: the block is %d cells, the address %d", tc.id, tier, block, address)
				}
				for x := block; x < 80; x++ {
					if normal[x] != pre[x] {
						t.Errorf("%s %s: cell %d is %+v with the prefix, %+v without", tc.id, tier, x, pre[x], normal[x])
						break
					}
				}
			}
		}
	}
}

// drawn is the cells of row 0 of a screen, read back through a canvas.
func drawn(sc golden.Screen) []screen.Cell {
	c := screen.New(sc.Cols, 1)
	c.Styled(0, 0, sc.Lines[0], sc.Cols)
	out := make([]screen.Cell, sc.Cols)
	for x := range out {
		out[x] = c.At(x, 0)
	}
	return out
}

// addressWidth is the cells of the margin and the address of the normal
// row of s, as reduced at width.
func addressWidth(s State, width int) int {
	s.Mode = "normal"
	p := reduce(s, width)
	pieces, _ := addressRow(s, p, width)
	end := 0
	for _, pc := range pieces {
		if pc.text == s.Session || s.Machine != "" && strings.Trim(pc.text, " []") == s.Machine || pc.text == thisMachine(s) {
			end = max(end, pc.x+ui.Width(pc.text))
		}
	}
	return end
}

func TestHintStripPages(t *testing.T) {
	l := statusLook()
	s := statusStandard()
	s.Mode, s.Hints = "prefix", true
	statusGolden(t, "status-hints-80", s, 80)
	s.HintPage = 1
	statusGolden(t, "status-hints-more-80", s, 80)

	for _, w := range []int{20, 40, 60, 80, 120} {
		pages := hintPages(l.hints(), w-rowMargin-hintStart)
		if len(pages) < 2 {
			t.Fatalf("%d columns: %d pages", w, len(pages))
		}
		var seen []hint
		for n := range pages {
			s.HintPage = n
			pieces, _ := l.row(withTheme(t, s), w)
			keys := keyPieces(pieces)
			if last := keys[len(keys)-1]; last.text != "?" {
				t.Errorf("%d columns, page %d does not end with ? more: %q", w, n, statusText(t, l, s, w))
			}
			for _, k := range keys[:len(keys)-1] {
				seen = append(seen, hintOf(l, k.text))
			}
		}
		if fmt.Sprint(seen) != fmt.Sprint(l.hints()) {
			t.Errorf("%d columns: the pages hold %v, want every hint once: %v", w, seen, l.hints())
		}
		s.HintPage = len(pages)
		again := statusText(t, l, s, w)
		s.HintPage = 0
		if first := statusText(t, l, s, w); again != first {
			t.Errorf("%d columns: after the last page %q, want the first %q", w, again, first)
		}
	}
}

// keyPieces is the pieces of a row that are keys.
func keyPieces(pieces []piece) []piece {
	var out []piece
	for _, p := range pieces {
		if p.key {
			out = append(out, p)
		}
	}
	return out
}

// hintOf is the hint of the strip whose key is key.
func hintOf(l *look, key string) hint {
	for _, h := range l.hints() {
		if h.key == key {
			return h
		}
	}
	return hint{key: key}
}

func TestSwitchingIndicator(t *testing.T) {
	s := statusStandard()
	s.Mode, s.Target = "switching", Target{Machine: "macmini", Session: "notes-main"}
	statusGolden(t, "status-switching-80", s, 80)
	statusGolden(t, "status-switching-40", s, 40)

	stale := statusStale()
	stale.Mode, stale.Target = "switching", Target{Machine: "macbook", Session: "blog-main"}
	statusGolden(t, "status-switching-stale-80", stale, 80)
}

func TestNoticeReplacesTabs(t *testing.T) {
	s := statusStandard()
	s.Notice = ui.Notice("no-answer", "macmini")
	statusGolden(t, "status-notice-80", s, 80)
	s.Notice = ui.Notice("older-server")
	statusGolden(t, "status-skew-80", s, 80)

	// Below that the text is cut before the address gives way.
	if row := statusText(t, statusLook(), s, 60); !strings.HasPrefix(row, " desktop  devtools-main   Server is") || !strings.HasSuffix(row, "…   ◆ 1") {
		t.Errorf("a long notice at 60 columns: %q", row)
	}
	// A notice waits in the prefix.
	s.Mode = "prefix"
	if row := statusText(t, statusLook(), s, 80); strings.Contains(row, "Server") || !strings.Contains(row, "2 claude ◆") {
		t.Errorf("a notice in the prefix: %q", row)
	}
}

func TestConfirmTakesTheRow(t *testing.T) {
	s := statusStandard()
	s.Mode, s.Confirm = "confirm", &Confirm{Program: "nvim"}
	for _, w := range []int{80, 40, 20} {
		statusGolden(t, fmt.Sprintf("status-confirm-%d", w), s, w)
	}
}

func TestCopyModeRow(t *testing.T) {
	s := statusStandard()
	s.Mode, s.Copy = "copy", &Copy{Line: 7412, Total: 8113, Selecting: "line"}
	statusGolden(t, "status-copy-80", s, 80)
	statusGolden(t, "status-copy-40", s, 40)
	s.Mode, s.Copy = "scrolled", &Copy{Line: 7412, Total: 8113}
	statusGolden(t, "status-scrolled-80", s, 80)
	statusGolden(t, "status-scrolled-40", s, 40)
}

// statusChip64 is a session of a machine whose alias is 64 characters long
// (4.6).
func statusChip64() State {
	s := statusRemote()
	s.Machine = "m" + strings.Repeat("acmini-", 9)
	s.Summary.Machines[1].Name = s.Machine
	return s
}

// statusCase is one state of the status row: a mode in a view it can hold
// with (5a.10.1).
type statusCase struct {
	name string
	s    State
}

// statusCases is every mode with every view it can hold with, on this
// machine and on another one.
func statusCases() []statusCase {
	var out []statusCase
	bases := []statusCase{
		{"standard", statusStandard()}, {"remote", statusRemote()}, {"chip64", statusChip64()},
		{"stale", statusStale()}, {"none", statusNone()},
	}
	long := ui.Notice("older-server")
	for _, b := range bases {
		modes := []string{"normal", "notice", "prefix", "hints", "switching", "copy", "scrolled", "scrolled-notice", "confirm"}
		switch b.s.View {
		case "stale":
			modes = []string{"normal", "notice", "prefix", "hints", "switching"}
		case "none":
			modes = []string{"normal", "notice", "prefix", "hints"}
		}
		for _, m := range modes {
			s := b.s
			switch m {
			case "notice":
				s.Notice = long
			case "hints":
				s.Mode, s.Hints = "prefix", true
			case "switching":
				s.Mode, s.Target = m, Target{Machine: "macbook", Session: "a-session-label-of-forty-characters-long"}
			case "copy":
				s.Mode, s.Copy = m, &Copy{Line: 7412, Total: 8113, Selecting: "block"}
			case "scrolled", "scrolled-notice":
				s.Mode, s.Copy = "scrolled", &Copy{Line: 7412, Total: 8113}
				if m == "scrolled-notice" {
					s.Notice = long
				}
			case "confirm":
				s.Mode, s.Confirm = m, &Confirm{Program: "a-program-with-a-long-name"}
			default:
				s.Mode = m
			}
			out = append(out, statusCase{b.name + "-" + m, s})
		}
	}
	return out
}

func TestStatusRowNeverExceedsTheWidth(t *testing.T) {
	l := statusLook()
	for _, utf8 := range []bool{true, false} {
		tc, pal := statusThemes(t, utf8)
		for _, th := range []ui.Theme{tc, pal} {
			for _, c := range statusCases() {
				s := c.s
				s.Theme = th
				for w := 20; w <= 300; w++ {
					for page := range 4 {
						s.HintPage = page
						pieces, need := l.row(s, w)
						if need > w {
							t.Errorf("%s utf8=%v, %d columns: the row needs %d", c.name, utf8, w, need)
						}
						for _, p := range pieces {
							if p.x < 0 || p.x+ui.Width(p.text) > w-rowMargin {
								t.Errorf("%s utf8=%v, %d columns: %q at %d leaves no margin", c.name, utf8, w, p.text, p.x)
							}
						}
						if s.Mode != "prefix" || !s.Hints {
							break
						}
					}
				}
			}
		}
	}
}

func TestNoKeyIsEverCut(t *testing.T) {
	l := statusLook()
	whole := map[string]bool{"esc": true, "?": true, "v": true, "y": true, "q": true}
	for _, h := range l.hints() {
		whole[h.key] = true
	}
	for _, utf8 := range []bool{true, false} {
		tc, pal := statusThemes(t, utf8)
		whole[tc.Glyph(ui.GlyphEnter)] = true
		for _, th := range []ui.Theme{tc, pal} {
			for _, c := range statusCases() {
				s := c.s
				s.Theme = th
				for w := 20; w <= 300; w++ {
					for page := range 4 {
						s.HintPage = page
						pieces, _ := l.row(s, w)
						canvas := screen.New(w, 1)
						l.Status(canvas, s)
						row := golden.PlainRow(canvas.Lines()[0])
						for _, p := range keyPieces(pieces) {
							if !whole[p.text] {
								t.Errorf("%s, %d columns: key %q is not a whole key", c.name, w, p.text)
							}
							if got := cellsAt(canvas, p.x, ui.Width(p.text)); got != p.text {
								t.Errorf("%s, %d columns: key %q drawn as %q in %q", c.name, w, p.text, got, row)
							}
						}
						if s.Mode == "confirm" && len(keyPieces(pieces)) != 2 {
							t.Errorf("%s, %d columns: the confirm names %d keys: %q", c.name, w, len(keyPieces(pieces)), row)
						}
						if s.Mode != "prefix" || !s.Hints {
							break
						}
					}
				}
			}
		}
	}
}

// cellsAt is the text of n cells of row 0 from x.
func cellsAt(c *screen.Canvas, x, n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(c.At(x+i, 0).S)
	}
	return b.String()
}

func TestCurrentTabAndBlockedCountSurviveEveryWidth(t *testing.T) {
	l := statusLook()
	for _, base := range []statusCase{{"standard", statusStandard()}, {"remote", statusRemote()}, {"chip64", statusChip64()}} {
		for _, mode := range []string{"normal", "prefix"} {
			for _, utf8 := range []bool{true, false} {
				tc, pal := statusThemes(t, utf8)
				for _, th := range []ui.Theme{tc, pal} {
					s := base.s
					s.Mode, s.Theme = mode, th
					cur := s.Tabs[1]
					mark, _, _ := agentMarkOf(th, cur.Agent)
					blocked, _, _ := agentMarkOf(th, "blocked")
					accent := withBold(roleFace(th, ui.RoleAccent))
					for w := 20; w <= 300; w++ {
						pieces, _ := l.row(s, w)
						tab, markOK, count := -1, false, false
						for i, p := range pieces {
							if p.face == accent && strings.HasPrefix(p.text, "2") {
								tab = i
							}
							if tab >= 0 && i == tab+1 && p.text == mark && p.x == pieces[tab].x+ui.Width(pieces[tab].text)+1 {
								markOK = true
							}
							if p.text == blocked+" 1" || strings.HasPrefix(p.text, blocked+" 1 ") {
								count = true
							}
						}
						if tab < 0 || !markOK || !count {
							t.Errorf("%s %s utf8=%v, %d columns: tab %v, mark %v, blocked count %v: %q",
								base.name, mode, utf8, w, tab >= 0, markOK, count, statusText(t, l, s, w))
						}
					}
				}
			}
		}
	}
}

func TestAChipWithASixtyFourCharacterNameFitsTwentyColumns(t *testing.T) {
	s := statusChip64()
	if n := ui.Width(s.Machine); n != 64 {
		t.Fatalf("the alias is %d characters", n)
	}
	tc, pal := statusScreens(t, statusLook(), s, 20, true)
	for tier, sc := range map[string]golden.Screen{"truecolor": tc, "palette": pal} {
		row := sc.Row(0)
		if ui.Width(row) > 19 {
			t.Errorf("%s: %q is wider than 19 cells", tier, row)
		}
		if len(row) < 2 || row[1] != 'm' || !strings.HasSuffix(row, "◆ 1") || !strings.Contains(row, "2 ●") {
			t.Errorf("%s: %q does not keep the chip's first letter, the current tab and the blocked count", tier, row)
		}
	}
}

func TestEveryHintFitsAtTwoHundredColumns(t *testing.T) {
	l := statusLook()
	s := statusStandard()
	s.Mode, s.Hints = "prefix", true
	// status-hints-200, an illustration of 5a.9.1
	want := ` prefix   esc cancel   o hub   s sessions   a agents   c new tab   n next tab   p previous tab   1-9 tab   % split right   " split down   x close pane   [ copy   ( ) session   d detach`
	if got := statusText(t, l, s, 200); got != want {
		t.Errorf("at 200 columns:\n got %q\nwant %q", got, want)
	}
	if n := len(hintPages(l.hints(), 200-rowMargin-hintStart)); n != 1 {
		t.Errorf("%d pages at 200 columns", n)
	}
}

func TestHintsFollowTheBindingTable(t *testing.T) {
	table, err := keys.New(
		[]config.Bind{{Chord: "prefix+t", Action: "tab-new", Line: 1}, {Chord: "prefix+f", Action: "hub", Arg: "new", Line: 2}},
		[]config.Unbind{{Chord: "prefix+c", Line: 3}, {Chord: "prefix+o", Line: 4}, {Chord: "prefix+(", Line: 5}},
	)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLook(LookOptions{Chords: table.Chords}).(*look)
	got := fmt.Sprint(l.hints())
	for _, want := range []string{"{t new tab}", "{f new feature}", "{) session}", "{1-9 tab}"} {
		if !strings.Contains(got, want) {
			t.Errorf("hints %s do not hold %s", got, want)
		}
	}
	for _, gone := range []string{"{c ", "{o hub}"} {
		if strings.Contains(got, gone) {
			t.Errorf("hints %s still hold %s", got, gone)
		}
	}
	s := statusStandard()
	s.Mode, s.Hints = "prefix", true
	if row := statusText(t, l, s, 200); !strings.Contains(row, "   f new feature   t new tab   ") || strings.Contains(row, " hub ") {
		t.Errorf("the strip at 200 columns: %q", row)
	}
	// Without a binding table there is no strip: the prefix keeps the tabs.
	plain := NewLook(LookOptions{}).(*look)
	if row := statusText(t, plain, s, 80); !strings.Contains(row, "2 claude ◆") || strings.Contains(row, "cancel") {
		t.Errorf("without chords: %q", row)
	}
	if k := digitRange([]string{"1", "2", "4"}); k != "1" {
		t.Errorf("digits with a hole: %q", k)
	}
}

func TestAnOffMachineIsListedOnlyWhileItHadBusyAgents(t *testing.T) {
	l := statusLook()
	s := statusStandard()
	s.Summary.Machines = append(s.Summary.Machines, Machine{Name: "macbook", State: "off"})
	if row := statusText(t, l, s, 200); strings.Contains(row, "macbook") {
		t.Errorf("an off machine without busy agents is listed: %q", row)
	}
	s.Summary.Machines[2].Busy = 2
	if row := statusText(t, l, s, 200); !strings.Contains(row, "macmini  macbook off   ◆ 1") {
		t.Errorf("an off machine whose agents were busy: %q", row)
	}
}

func TestStatusDrawsOneRow(t *testing.T) {
	l := statusLook()
	if l.StatusRows() != 1 {
		t.Errorf("StatusRows = %d", l.StatusRows())
	}
	c := screen.New(80, 3)
	c.Text(0, 0, "above", screen.Style{}, 80)
	c.Text(0, 2, strings.Repeat("x", 80), screen.Style{}, 80)
	l.Status(c.Sub(screen.Rect{X: 0, Y: 2, W: 80, H: 1}), withTheme(t, statusStandard()))
	if got := c.Lines()[0]; got != "above" {
		t.Errorf("row 0 = %q", got)
	}
	if got := golden.PlainRow(c.Lines()[2]); got != " desktop  devtools-main   1 zsh  2 claude ◆  3 nvim       ◆ 1  ✓ 1  ● 2   14:32" {
		t.Errorf("status row = %q", got)
	}
	l.Status(screen.New(0, 0), withTheme(t, statusStandard())) // no panic
}
