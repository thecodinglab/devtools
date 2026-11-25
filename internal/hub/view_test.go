package hub

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"devtools/internal/client/screen"
	"devtools/internal/session"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The tests of the look of the hub (P4-04). Goldens are the mockups of
// 5a.9.2 in three layers (5a.17): the text is typed from the mockup, the two
// style layers are written once by -update and read against the Styles
// paragraphs. A whole window is compared without its last row, the status
// row, which the hub does not draw; the four screens of the standalone host
// have no status row and are compared whole.

// viewTruecolor is the truecolor tier with the dark values of 5a.7.1, the
// five tokens the systems config generates from palette.nix.
func viewTruecolor(t *testing.T) ui.Theme {
	t.Helper()
	th, err := ui.NewTheme(ui.ThemeOptions{
		Tokens: []ui.Token{
			{Name: "strong", Light: 0x111214, Dark: 0xe6e7eb},
			{Name: "muted", Light: 0x55585f, Dark: 0xa6a8b0},
			{Name: "raised", Light: 0xe3e4e8, Dark: 0x30323a},
			{Name: "selection", Light: 0xd6dde9, Dark: 0x3a4150},
			{Name: "warning", Light: 0xb04a00, Dark: 0xec8a3f},
		},
		Dark: true, Known: true, Colors: 1 << 24, UTF8: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// tiers is both tiers: the truecolor one, then the palette tier with UTF-8
// glyphs, which is the zero theme.
func tiers(t *testing.T) [2]ui.Theme { return [2]ui.Theme{viewTruecolor(t), {}} }

// desktop is this machine's name in the example of 5a.9, set as
// ModelOptions.Name the way the overlay host sets it.
const desktop = "desktop"

// The example state of 5a.9: this machine (desktop) with devtools, systems
// and notes, macmini with devtools and blog, macbook. Paths of macmini are
// under its own home.

func localWorktree(project, name string) Item { return worktree(project, name) }

func miniWorktree(project, name string) Item {
	item := worktree(project, name)
	item.Root = "/Users/florian/dev"
	item.Path = item.Root + "/" + project + "/" + name
	return item
}

func sessionOf(item Item, id string) Item {
	return Item{Label: item.Session, Session: item.Session, SessionID: id, Path: item.Path}
}

func agentOf(item Item, id, tab, pane string, tabIndex int, kind, status, title string) Item {
	return Item{
		Kind: KindAgent, Session: item.Session, SessionID: id, Tab: tab, Pane: pane,
		TabIndex: tabIndex, Agent: kind, Status: status, State: ui.Session{Title: title},
	}
}

var (
	dtMain    = localWorktree("devtools", "main")
	dtReplace = localWorktree("devtools", "replace-herdr")
	dtFooter  = localWorktree("devtools", "fix-footer")
	sysMain   = localWorktree("systems", "main")
	notesMain = localWorktree("notes", "main")
	miniDev   = miniWorktree("devtools", "main")
	miniBlog  = miniWorktree("blog", "main")
)

// localEvents is everything this machine reports: its rows, their git
// state, the end of the scan, its sessions and its agents.
func localEvents() []Event {
	return []Event{
		{Kind: Online},
		on("", dtMain, dtReplace, dtFooter, sysMain, notesMain),
		{Kind: Git, Path: dtMain.Path, Git: ui.Git{Changes: 3, Ahead: 2, HasUpstream: true}},
		{Kind: Git, Path: dtFooter.Path, Git: ui.Git{Changes: 1, HasUpstream: true}},
		{Kind: Git, Path: sysMain.Path, Git: ui.Git{Changes: 12, Behind: 1, HasUpstream: true}},
		{Kind: Complete},
		{Kind: Sessions, Items: []Item{
			sessionOf(dtMain, "s1"), sessionOf(dtReplace, "s2"), sessionOf(dtFooter, "s3"), sessionOf(sysMain, "s4"),
		}},
		{Kind: Agents, Items: []Item{
			agentOf(dtMain, "s1", "t2", "p2", 2, "claude", "blocked", "Fix the hub footer"),
			agentOf(dtReplace, "s2", "t5", "p5", 2, "claude", "working", "Plan part 4"),
			agentOf(sysMain, "s4", "t9", "p9", 3, "claude", "idle", ""),
		}},
	}
}

// miniEvents is what macmini reports; without the end of the scan it is
// still listing.
func miniEvents(complete bool) []Event {
	evs := []Event{
		{Machine: "macmini", Kind: Connecting},
		{Machine: "macmini", Kind: Online},
		on("macmini", miniDev, miniBlog),
		{Machine: "macmini", Kind: Sessions, Items: []Item{sessionOf(miniDev, "m1"), sessionOf(miniBlog, "m2")}},
		{Machine: "macmini", Kind: Agents, Items: []Item{
			agentOf(miniDev, "m1", "u1", "q1", 1, "claude", "done", "Review PR 212"),
			agentOf(miniBlog, "m2", "u2", "q2", 1, "codex", "working", "Draft the post"),
		}},
	}
	if complete {
		evs = append(evs, Event{Machine: "macmini", Kind: Complete})
	}
	return evs
}

// here is the pane of the mockups: the claude in devtools-main.
var here = session.Target{Dir: dtMain.Path, Pane: "p2"}

// exampleHub is the hub of the example with theme th, features f, opened
// in mode, after events.
func exampleHub(th ui.Theme, f Features, mode Mode, events ...Event) *Model {
	m := New(ModelOptions{Mode: mode, Here: here, Name: desktop, Actions: &fakeBackend{}, Theme: th, Features: f})
	feed(m, events...)
	return m
}

func concat(lists ...[]Event) []Event {
	var out []Event
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// screenOf is the hub drawn by the overlay's path, Draw onto a canvas, at
// w by h with the cursor where Cursor puts it.
func screenOf(m *Model, w, h int) golden.Screen {
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	c := screen.New(w, h)
	m.Draw(c)
	x, y, visible := m.Cursor()
	if !visible {
		x, y = 0, 0
	}
	return golden.Screen{Cols: w, Rows: h, Lines: c.Lines(), CursorX: x, CursorY: y, CursorVisible: visible}
}

// rowsOf is rows from to from+n of s as a screen of their own, the cursor
// hidden: the goldens of single rows.
func rowsOf(s golden.Screen, from, n int) golden.Screen {
	return golden.Screen{Cols: s.Cols, Rows: n, Lines: append([]string(nil), s.Lines[from:from+n]...)}
}

// look asserts the three layers of id; build makes the hub in a tier and
// returns its screen.
func look(t *testing.T, id string, build func(th ui.Theme) golden.Screen) {
	t.Helper()
	th := tiers(t)
	golden.Look(t, id, build(th[0]), build(th[1]))
}

// lines is the text of the hub at w by h, one string per row.
func plainRows(m *Model, w, h int) []string {
	return strings.Split(ansi.Strip(m.View(w, h)), "\n")
}

func TestHubOnTheTmuxBackend(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 10}} {
		id := fmt.Sprintf("hub-noagents-%dx%d", size[0], size[1])
		look(t, id, func(th ui.Theme) golden.Screen {
			m := exampleHub(th, Features{}, ModeAll, concat(localEvents(), miniEvents(false))...)
			return screenOf(m, size[0], size[1])
		})
	}
}

func TestHubOnTheHerdrBackend(t *testing.T) {
	look(t, "hub-flat-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, Features{Agents: true}, ModeAll, concat(localEvents(), miniEvents(false))...)
		return screenOf(m, 80, 24)
	})
	look(t, "hub-flat-agents-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, Features{Agents: true}, ModeAgents, concat(localEvents(), miniEvents(false))...)
		return screenOf(m, 80, 24)
	})
}

// allEvents is the state of hub-all-80x24: macmini still listing, macbook
// connecting.
func allEvents() []Event {
	return concat(localEvents(), miniEvents(false), []Event{{Machine: "macbook", Kind: Connecting}})
}

func TestHubAllScope(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 10}} {
		id := fmt.Sprintf("hub-all-%dx%d", size[0], size[1])
		look(t, id, func(th ui.Theme) golden.Screen {
			return screenOf(exampleHub(th, every, ModeAll, allEvents()...), size[0], size[1]-1)
		})
	}
}

func TestHubSessionsScope(t *testing.T) {
	look(t, "hub-sessions-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, every, ModeSessions, concat(localEvents(), miniEvents(true), []Event{
			{Machine: "macbook", Kind: Failed, Err: Problem{Reason: "unreachable", Retry: "next-open"}},
		})...)
		return screenOf(m, 80, 23)
	})
}

func TestHubNewFeature(t *testing.T) {
	look(t, "hub-new-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, every, ModeNew, concat(localEvents(), miniEvents(true), []Event{
			{Machine: "macbook", Kind: Online}, {Machine: "macbook", Kind: Complete},
		})...)
		typeInto(m, "fix-status")
		return screenOf(m, 80, 23)
	})
	// before anything is typed: the placeholder of a new feature, and row 2
	// asks for a name
	m := exampleHub(ui.Theme{}, every, ModeNew, localEvents()...)
	rows := plainRows(m, 80, 23)
	if !strings.HasPrefix(rows[0], "  › Name the new feature") || rows[2] != "  Type a name for the feature first." {
		t.Fatalf("new feature without a name:\n%s", strings.Join(rows, "\n"))
	}
}

func TestHubAgentsScope(t *testing.T) {
	look(t, "hub-agents-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, every, ModeAgents, concat(localEvents(), miniEvents(true), []Event{
			{Machine: "macbook", Kind: Connecting},
		})...)
		return screenOf(m, 80, 23)
	})
}

func TestHubLoadingSaysNothingBefore150ms(t *testing.T) {
	open := func(th ui.Theme) *Model {
		return exampleHub(th, every, ModeAll,
			Event{Machine: "macmini", Kind: Connecting},
			Event{Machine: "macbook", Kind: Connecting},
		)
	}
	before := plainRows(open(ui.Theme{}), 80, 23)
	if strings.TrimSpace(before[2]) != "" || !strings.HasPrefix(before[4], "  macmini   connecting…") || !strings.HasPrefix(before[6], "  macbook ") {
		t.Fatalf("before 150 ms:\n%s", strings.Join(before, "\n"))
	}
	look(t, "hub-loading-80x24", func(th ui.Theme) golden.Screen {
		m := open(th)
		m.Update(slowMsg{})
		return screenOf(m, 80, 23)
	})
}

func TestHubWithALostAndAnOffMachine(t *testing.T) {
	look(t, "hub-stale-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, every, ModeAll, concat(localEvents(), miniEvents(true), []Event{
			{Machine: "macbook", Kind: Connecting},
			{Machine: "macbook", Kind: Closed},
			{Machine: "macmini", Kind: Failed, Err: Problem{Reason: "lost", Retry: "retrying"}},
		})...)
		return screenOf(m, 80, 23)
	})
}

// selectFooter puts the cursor on fix-footer of this machine.
func selectFooter(t *testing.T, m *Model) {
	t.Helper()
	selectWhere(t, m, func(i Item) bool { return i.Path == dtFooter.Path && i.Machine == "" })
}

func TestHubMergeConfirm(t *testing.T) {
	look(t, "hub-confirm-80x24", func(th ui.Theme) golden.Screen {
		m := exampleHub(th, every, ModeAll, allEvents()...)
		selectFooter(t, m)
		m.Update(ctrlG)
		return screenOf(m, 80, 23)
	})
}

func TestHubHeadingForEveryStateAndReason(t *testing.T) {
	look(t, "hub-headings-80", func(th ui.Theme) golden.Screen {
		m := New(ModelOptions{Theme: th, Features: every})
		mc := func(name, state string, p Problem, complete bool) line {
			return m.machineHeading(&machine{name: name, state: state, problem: p, complete: complete}, 80)
		}
		attention := func(reason, text string) line {
			return mc("macmini", "attention", Problem{Reason: reason, Text: text, Retry: "in-30s"}, false)
		}
		heads := []line{
			mc("macmini", "online", Problem{}, true),
			mc("macmini", "online", Problem{}, false),
			mc("macmini", "connecting", Problem{}, false),
			mc("macbook", "off", Problem{}, false),
			mc("macmini", "retrying", Problem{Reason: "unreachable", Retry: "retrying"}, false),
			mc("macbook", "off", Problem{Reason: "unreachable", Retry: "next-open"}, false),
			attention("auth", ""),
			attention("host-key", ""),
			attention("no-devtools", ""),
			attention("no-server", ""),
			attention("incompatible", ""),
			mc("macmini", "retrying", Problem{Reason: "protocol", Text: "bash: devtools: Permission denied", Retry: "retrying"}, false),
			m.headingLine("macmini", false, ui.ListPhrase("cut"), false, 80),
			m.headingLine("macmini", false, ui.ListPhrase("incomplete"), false, 80),
			m.headingLine("desktop", true, ui.LocalPhrase("older"), false, 80),
		}
		c := screen.New(80, len(heads))
		for y, h := range heads {
			c.Styled(0, y, m.render(h, 80), 80)
		}
		return golden.Screen{Cols: 80, Rows: len(heads), Lines: c.Lines()}
	})
}

func TestHubEmptyStates(t *testing.T) {
	// row 2 of the hub, or the row under a machine's heading
	cases := []struct {
		id    string
		build func(th ui.Theme) *Model
		row   func(m *Model) int
	}{
		{"hub-empty-projects-80", func(th ui.Theme) *Model {
			return exampleHub(th, every, ModeAll, Event{Kind: Online}, Event{Kind: Complete})
		}, nil},
		{"hub-empty-sessions-80", func(th ui.Theme) *Model {
			return exampleHub(th, every, ModeSessions, Event{Kind: Online}, on("", dtMain, notesMain), Event{Kind: Complete}, Event{Kind: Sessions})
		}, nil},
		{"hub-empty-agents-80", func(th ui.Theme) *Model {
			return exampleHub(th, every, ModeAgents, Event{Kind: Online}, on("", dtMain), Event{Kind: Complete}, Event{Kind: Agents})
		}, nil},
		{"hub-empty-machine-80", func(th ui.Theme) *Model {
			return exampleHub(th, every, ModeAll, Event{Kind: Online}, on("", dtMain), Event{Kind: Complete},
				Event{Machine: "macmini", Kind: Online}, Event{Machine: "macmini", Kind: Complete})
		}, func(m *Model) int {
			for i, r := range plainRows(m, 80, 23) {
				if strings.Contains(r, "macmini") && !strings.Contains(r, "Nothing") {
					return i + 1
				}
			}
			return -1
		}},
		{"hub-empty-query-80", func(th ui.Theme) *Model {
			m := New(ModelOptions{Here: here, Name: desktop, Theme: th, Features: every})
			feed(m, Event{Kind: Online}, on("", dtMain, dtFooter), Event{Kind: Complete})
			typeInto(m, "fix-stat")
			return m
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			look(t, c.id, func(th ui.Theme) golden.Screen {
				m := c.build(th)
				row := 2
				if c.row != nil {
					row = c.row(m)
				}
				if row < 0 {
					t.Fatalf("no row for %s", c.id)
				}
				return rowsOf(screenOf(m, 80, 23), row, 1)
			})
		})
	}
}

func TestHubFooterStates(t *testing.T) {
	selectMini := func(t *testing.T, m *Model) {
		selectWhere(t, m, func(i Item) bool { return i.Machine == "macmini" && i.Path == miniDev.Path })
	}
	cases := []struct {
		id   string
		rows int
		set  func(t *testing.T, m *Model)
	}{
		{"hub-footer-confirm-80", 2, func(t *testing.T, m *Model) { selectFooter(t, m); m.Update(ctrlG) }},
		{"hub-footer-feature-80", 1, selectFooter},
		{"hub-footer-query-80", 1, func(t *testing.T, m *Model) { typeInto(m, "sys") }},
		{"hub-footer-busy-80", 1, func(t *testing.T, m *Model) { m.busy = ui.Busy("open-remote", "notes-main", "macmini") }},
		{"hub-footer-error-80", 1, func(t *testing.T, m *Model) {
			m.fail(errors.New("git merge failed: conflict in internal/hub/view.go"))
		}},
		{"hub-footer-remote-create-80", 1, func(t *testing.T, m *Model) { selectMini(t, m); m.Update(ctrlN) }},
		{"hub-footer-remote-merge-80", 1, func(t *testing.T, m *Model) { selectMini(t, m); m.Update(ctrlG) }},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			look(t, c.id, func(th ui.Theme) golden.Screen {
				m := exampleHub(th, every, ModeAll, concat(localEvents(), miniEvents(true))...)
				c.set(t, m)
				// the footer stands above the closing rule, the hub's last row
				return rowsOf(screenOf(m, 80, 23), 22-c.rows, c.rows)
			})
		})
	}
}

// everyScope is the example in each scope the features have.
func everyScope(th ui.Theme, f Features) []*Model {
	var hubs []*Model
	for _, mode := range []Mode{ModeAll, ModeSessions, ModeAgents, ModeNew} {
		m := exampleHub(th, f, mode, concat(allEvents(), []Event{
			{Machine: "macbook", Kind: Failed, Err: Problem{Reason: "auth", Retry: "in-30s"}},
		})...)
		hubs = append(hubs, m)
	}
	return hubs
}

func TestEveryLineFitsTheWidth(t *testing.T) {
	check := func(m *Model, w, h int) {
		t.Helper()
		lines := strings.Split(m.View(w, h), "\n")
		if len(lines) != h {
			t.Fatalf("%dx%d scope %s: %d lines", w, h, m.currentScope(), len(lines))
		}
		for _, l := range lines {
			if got := ui.Width(l); got > w {
				t.Fatalf("%dx%d scope %s: %q is %d wide", w, h, m.currentScope(), ansi.Strip(l), got)
			}
		}
	}
	for _, th := range tiers(t) {
		for _, f := range []Features{every, {Agents: true}, {}} {
			for _, m := range everyScope(th, f) {
				for w := 20; w <= 300; w++ {
					check(m, w, 24)
				}
				for h := 5; h <= 60; h++ {
					for _, w := range []int{20, 40, 80, 135, 300} {
						check(m, w, h)
					}
				}
			}
		}
	}
}

func TestHubAtTwentyByFive(t *testing.T) {
	m := exampleHub(ui.Theme{}, every, ModeAll, allEvents()...)
	rows := plainRows(m, 20, 5)
	if len(rows) != 5 {
		t.Fatalf("%d rows", len(rows))
	}
	if !strings.HasPrefix(rows[0], "  › ") {
		t.Fatalf("row 0 = %q, want the input", rows[0])
	}
	// the name is cut at the end; the state stays (5a.6.7)
	if !strings.Contains(strings.Join(rows[1:4], "\n"), "▌ dev…   ◆ needs you") {
		t.Fatalf("the selected row is not in sight:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[4], "esc close") {
		t.Fatalf("row 4 = %q, want the footer", rows[4])
	}
	for _, r := range rows {
		if ui.Width(r) > 20 {
			t.Fatalf("%q is wider than 20", r)
		}
	}
	// with a create row the list gives way and the create row stays
	typeInto(m, "fresh")
	m.Update(down)
	m.Update(down)
	rows = plainRows(m, 20, 5)
	if !strings.Contains(strings.Join(rows, "\n"), "+ New feature") || !strings.Contains(rows[4], "esc clear") {
		t.Fatalf("with a create row:\n%s", strings.Join(rows, "\n"))
	}
}

// runLine is one parsed line of the styles of golden.Screen.Runs.
type runLine struct {
	row, col, width int
	style           string
}

func runsOf(t *testing.T, s golden.Screen) []runLine {
	t.Helper()
	text := s.Runs()
	_, styles, ok := strings.Cut(text, "\nstyles\n")
	if !ok {
		t.Fatalf("no styles in:\n%s", text)
	}
	var runs []runLine
	for _, l := range strings.Split(strings.TrimSuffix(styles, "\n"), "\n") {
		if l == "" {
			continue
		}
		var r runLine
		var rest string
		if _, err := fmt.Sscanf(l, "%d %d %d", &r.row, &r.col, &r.width); err != nil {
			t.Fatalf("run %q: %v", l, err)
		}
		parts := strings.SplitN(l, " ", 4)
		if len(parts) == 4 {
			rest = parts[3]
		}
		r.style = rest
		runs = append(runs, r)
	}
	return runs
}

// rowOf finds the row whose text contains text.
func rowOf(t *testing.T, s golden.Screen, text string) int {
	t.Helper()
	for y := range s.Rows {
		if strings.Contains(s.Row(y), text) {
			return y
		}
	}
	t.Fatalf("no row contains %q:\n%s", text, s.Plain())
	return -1
}

func TestTheHubColumnIsCentredAndAHundredWide(t *testing.T) {
	for _, w := range []int{100, 135, 200, 280} {
		m := exampleHub(viewTruecolor(t), every, ModeAll, allEvents()...)
		s := screenOf(m, w, 49)
		x0 := (w - 100) / 2
		if first := s.Row(0); !strings.HasPrefix(first, strings.Repeat(" ", x0)+"  › ") {
			t.Fatalf("%d: row 0 = %q, want the column at %d", w, first, x0)
		}
		if x, y, ok := m.Cursor(); !ok || y != 0 || x != x0+4 {
			t.Fatalf("%d: cursor %d,%d %v, want %d,0", w, x, y, ok, x0+4)
		}
		for y := range s.Rows {
			row := s.Row(y)
			if strings.TrimSpace(row) == "" {
				continue
			}
			if lead := len(row) - len(strings.TrimLeft(row, " ")); lead < x0 || ui.Width(row) > x0+100 {
				t.Fatalf("%d: row %d %q is outside the column %d to %d", w, y, row, x0, x0+100)
			}
		}
		// the ground of the selected row runs across the column
		sel := rowOf(t, s, "▌ devtools")
		covered := 0
		for _, r := range runsOf(t, s) {
			if r.row == sel && strings.Contains(r.style, "on #30323a") {
				if r.col < x0 || r.col+r.width > x0+100 {
					t.Fatalf("%d: ground at %d+%d is outside the column", w, r.col, r.width)
				}
				covered += r.width
			}
		}
		if covered != 100 {
			t.Fatalf("%d: the selected row has %d cells of ground, want 100", w, covered)
		}
	}
}

func TestSelectedRowFillsTheMeasure(t *testing.T) {
	for _, w := range []int{40, 80, 100} {
		m := exampleHub(viewTruecolor(t), every, ModeAll, allEvents()...)
		s := screenOf(m, w, 23)
		sel := rowOf(t, s, "▌ devtools")
		cells := make([]bool, w)
		for _, r := range runsOf(t, s) {
			if r.row == sel && strings.Contains(r.style, "on #30323a") {
				for x := r.col; x < r.col+r.width; x++ {
					cells[x] = true
				}
			}
		}
		for x, ok := range cells {
			if !ok {
				t.Fatalf("%d: cell %d of the selected row has no ground", w, x)
			}
		}
		// the name of the row you are in is bold in the accent
		for _, r := range runsOf(t, s) {
			if r.row == sel && r.col == 4 && !strings.HasPrefix(r.style, "bold p4 on #30323a") {
				t.Fatalf("%d: the name is drawn %q", w, r.style)
			}
		}
	}
}

func TestSelectedRowInThePaletteTierHasTheBarAndNoGround(t *testing.T) {
	m := exampleHub(ui.Theme{}, every, ModeAll, allEvents()...)
	s := screenOf(m, 80, 23)
	sel := rowOf(t, s, "▌ devtools")
	for _, r := range runsOf(t, s) {
		if r.row != sel {
			continue
		}
		if strings.Contains(r.style, " on ") || strings.HasPrefix(r.style, "on ") || strings.Contains(r.style, "reverse") {
			t.Fatalf("the selected row has a ground: %+v", r)
		}
		if r.col == 4 && r.style != "bold p4" {
			t.Fatalf("the name of the selected row you are in is %q, want bold in the accent", r.style)
		}
	}
	// the marks keep their colours
	found := false
	for _, r := range runsOf(t, s) {
		if r.row == sel && r.style == "p3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the blocked mark lost its colour on the selected row:\n%s", s.Runs())
	}
}

func TestMergeHintOnlyOnAFeatureWorktreeOfThisMachine(t *testing.T) {
	m := exampleHub(ui.Theme{}, every, ModeAll, concat(localEvents(), miniEvents(true))...)
	footer := func() string { return ansi.Strip(m.footer(80)) }
	merge := "⌃G merge into main"
	for _, c := range []struct {
		name  string
		which func(Item) bool
		want  bool
	}{
		{"a feature of this machine", func(i Item) bool { return i.Path == dtFooter.Path && i.Machine == "" }, true},
		{"another feature of this machine", func(i Item) bool { return i.Path == dtReplace.Path && i.Machine == "" }, true},
		{"main of this machine", func(i Item) bool { return i.Path == dtMain.Path && i.Machine == "" }, false},
		{"a worktree of macmini", func(i Item) bool { return i.Machine == "macmini" }, false},
	} {
		selectWhere(t, m, c.which)
		if got := strings.Contains(footer(), merge); got != c.want {
			t.Fatalf("%s: footer %q, merge offered %v, want %v", c.name, footer(), got, c.want)
		}
	}
	m.Update(tab)
	m.Update(tab)
	if strings.Contains(footer(), merge) {
		t.Fatalf("agents scope: footer %q offers a merge", footer())
	}
}

// columnOf is the column at which text starts in the row that contains
// it.
func columnOf(t *testing.T, rows []string, text string) int {
	t.Helper()
	for _, r := range rows {
		if i := strings.Index(r, text); i >= 0 {
			return ui.Width(r[:i])
		}
	}
	t.Fatalf("no row contains %q:\n%s", text, strings.Join(rows, "\n"))
	return -1
}

func TestColumnsDoNotMoveWhileTyping(t *testing.T) {
	m := exampleHub(ui.Theme{}, every, ModeAll, concat(localEvents(), miniEvents(true))...)
	before := plainRows(m, 80, 23)
	state := columnOf(t, before, "○ idle")
	changes := columnOf(t, before, "12 changed")
	// systems/main alone matches; the widest name (replace-herdr) and the
	// other rows are filtered out
	typeInto(m, "systems")
	after := plainRows(m, 80, 23)
	if got := columnOf(t, after, "○ idle"); got != state {
		t.Fatalf("the state column moved from %d to %d:\n%s", state, got, strings.Join(after, "\n"))
	}
	if got := columnOf(t, after, "12 changed"); got != changes {
		t.Fatalf("the changes column moved from %d to %d", changes, got)
	}
	if strings.Contains(strings.Join(after, "\n"), "replace-herdr") {
		t.Fatalf("the filter kept replace-herdr:\n%s", strings.Join(after, "\n"))
	}
}

func TestRowsKeepTheirPlaceWhileGitArrives(t *testing.T) {
	m := exampleHub(ui.Theme{}, every, ModeAll, allEvents()...)
	names := func(rows []string) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			if f := strings.Fields(strings.TrimLeft(r, "▎▌ ")); len(f) > 0 {
				out[i] = f[0] + fmt.Sprintf("@%d", ui.Width(r)-ui.Width(strings.TrimLeft(r, "▎▌ ")))
			}
		}
		return out
	}
	before := names(plainRows(m, 80, 23))
	feed(m,
		Event{Machine: "macmini", Kind: Git, Path: miniDev.Path, Git: ui.Git{Changes: 4, Ahead: 1, HasUpstream: true}},
		Event{Machine: "macmini", Kind: Git, Path: miniBlog.Path, Git: ui.Git{Changes: 2}},
	)
	afterRows := plainRows(m, 80, 23)
	after := names(afterRows)
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("row %d was %q and is %q after git arrived:\n%s", i, before[i], after[i], strings.Join(afterRows, "\n"))
		}
	}
	if !strings.Contains(strings.Join(afterRows, "\n"), "4 changed") {
		t.Fatalf("the git cells of macmini did not fill in:\n%s", strings.Join(afterRows, "\n"))
	}
}

func TestHeadingOfAFailedMachineStaysUnderAFilter(t *testing.T) {
	m := exampleHub(ui.Theme{}, every, ModeAll, concat(localEvents(), miniEvents(true), []Event{
		{Machine: "macbook", Kind: Failed, Err: Problem{Reason: "auth", Retry: "in-30s"}},
	})...)
	for _, query := range []string{"devtools", "zzzz", "blog"} {
		m.Update(esc)
		typeInto(m, query)
		view := strings.Join(plainRows(m, 80, 23), "\n")
		if !strings.Contains(view, "\n  macbook   ✗ ssh could not log in. Open the hub to try again.") {
			t.Fatalf("%s: the heading of the failed machine is gone:\n%s", query, view)
		}
		if query == "zzzz" && strings.Contains(view, "\n  macmini ") {
			t.Fatalf("%s: the heading of an online machine without a match stayed:\n%s", query, view)
		}
	}
}

func TestStaleRowsAreItalicAndComment(t *testing.T) {
	for i, th := range tiers(t) {
		m := exampleHub(th, every, ModeAll, concat(localEvents(), miniEvents(true), []Event{
			{Machine: "macmini", Kind: Failed, Err: Problem{Reason: "lost", Retry: "retrying"}},
		})...)
		s := screenOf(m, 80, 23)
		heading := rowOf(t, s, "macmini")
		stale := map[int]bool{}
		for y := heading + 1; y < heading+3; y++ {
			stale[y] = true
		}
		seen := map[int]bool{}
		for _, r := range runsOf(t, s) {
			if !stale[r.row] {
				continue
			}
			seen[r.row] = true
			if r.style != "italic p8" {
				t.Fatalf("tier %d: stale row %d has a run %+v, want italic p8", i, r.row, r)
			}
		}
		if len(seen) != 2 {
			t.Fatalf("tier %d: styled stale rows %v, want the two rows of macmini:\n%s", i, seen, s.Runs())
		}
	}
}

// A hub drawn at another height than the last WindowSizeMsg said keeps the
// selected row in sight: the list's height follows the height drawn, with
// or without the closing rule.
func TestSelectedRowStaysInSightAtAnyDrawnHeight(t *testing.T) {
	var items []Item
	for i := range 30 {
		items = append(items, worktree("devtools", fmt.Sprintf("feature-%02d", i)))
	}
	for _, sizes := range [][2]int{{15, 16}, {16, 15}, {12, 30}, {30, 12}} {
		m := exampleHub(ui.Theme{}, Features{}, ModeAll, on("", items...))
		m.Update(tea.WindowSizeMsg{Width: 80, Height: sizes[0]})
		for range 40 {
			m.Update(down)
		}
		rows := plainRows(m, 80, sizes[1])
		if len(rows) != sizes[1] {
			t.Fatalf("%v: %d rows", sizes, len(rows))
		}
		if !strings.Contains(strings.Join(rows, "\n"), "▌ └ feature-29") {
			t.Errorf("sized %d, drawn %d: the selected row is not in sight:\n%s", sizes[0], sizes[1], strings.Join(rows, "\n"))
		}
	}
}

// A project is one row, named by the project: its features branch off it
// with a tee and, on the last one, an elbow, in comment; the ASCII set
// draws them with | and `.
func TestFeaturesBranchOffTheirProject(t *testing.T) {
	ascii, err := ui.NewTheme(ui.ThemeOptions{UTF8: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		th         ui.Theme
		tee, elbow string
	}{{ui.Theme{}, "├", "└"}, {ascii, "|", "`"}} {
		m := exampleHub(c.th, every, ModeAll, localEvents()...)
		rows := plainRows(m, 80, 23)
		for _, want := range []string{
			" devtools ",
			"  " + c.tee + " replace-herdr ",
			"  " + c.elbow + " fix-footer ",
			"  systems ",
		} {
			if !strings.Contains(strings.Join(rows, "\n"), want) {
				t.Fatalf("no row %q:\n%s", want, strings.Join(rows, "\n"))
			}
		}
		if strings.Contains(strings.Join(rows, "\n"), " main ") {
			t.Fatalf("a main worktree is drawn by its own name:\n%s", strings.Join(rows, "\n"))
		}
	}
	s := screenOf(exampleHub(viewTruecolor(t), every, ModeAll, localEvents()...), 80, 23)
	branch := rowOf(t, s, "├ replace-herdr")
	for _, r := range runsOf(t, s) {
		if r.row == branch && r.col == 2 && r.style != "p8" {
			t.Fatalf("the branch is drawn %q, want comment", r.style)
		}
	}
}

// A project whose main worktree is filtered out keeps its name above its
// features, in comment: context, not a row to go to.
func TestAProjectWithoutItsMainRowIsContext(t *testing.T) {
	m := exampleHub(viewTruecolor(t), every, ModeAll, localEvents()...)
	typeInto(m, "footer")
	s := screenOf(m, 80, 23)
	heading := rowOf(t, s, "  devtools")
	if got := s.Row(heading + 1); !strings.HasPrefix(got, "▌ └ fix-footer") {
		t.Fatalf("row under the project is %q, want the selected feature", got)
	}
	for _, r := range runsOf(t, s) {
		if r.row == heading && r.col == 2 && r.style != "p8" {
			t.Fatalf("the project's name is drawn %q, want comment", r.style)
		}
	}
	if item, ok := m.selected(); !ok || item.Path != dtFooter.Path {
		t.Fatalf("selected %+v, want fix-footer", item)
	}
}

// Scrolling up onto a feature brings its project's row into sight with
// it, so a branch is not drawn without its trunk.
func TestScrollingUpOntoAFeatureShowsItsProject(t *testing.T) {
	var items []Item
	for i := range 12 {
		items = append(items, worktree(fmt.Sprintf("a%02d", i), "main"))
	}
	items = append(items, worktree("devtools", "main"), worktree("devtools", "one"), worktree("devtools", "two"), worktree("devtools", "three"))
	for i := range 12 {
		items = append(items, worktree(fmt.Sprintf("z%02d", i), "main"))
	}
	m := exampleHub(ui.Theme{}, Features{}, ModeAll, on("", items...))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for range len(items) {
		m.Update(down)
	}
	for {
		item, ok := m.selected()
		if !ok {
			t.Fatal("nothing selected")
		}
		if item.Project == "devtools" && item.Worktree == "three" {
			break
		}
		m.Update(up)
	}
	rows := plainRows(m, 80, 12)
	if !strings.HasSuffix(strings.TrimRight(rows[2], " "), " devtools") || !strings.HasPrefix(rows[5], "▌ └ three") {
		t.Fatalf("the project is not above its selected feature:\n%s", strings.Join(rows, "\n"))
	}
}

// The heading of another machine names the version of its server when it
// is another than this machine's, and an update installed there that waits
// for a restart; an unknown version of this machine shows none.
func TestHubHeadingNamesTheBuildOfAnotherMachine(t *testing.T) {
	look(t, "hub-headings-build-80", func(th ui.Theme) golden.Screen {
		m := New(ModelOptions{Theme: th, Features: every})
		feed(m, Event{Kind: Online, Version: "0.1.0+04c5fef"})
		mc := func(version, installed string, complete bool) line {
			return m.machineHeading(&machine{name: "macmini", state: "online", complete: complete, version: version, installed: installed}, 80)
		}
		heads := []line{
			mc("0.1.0+04c5fef", "", true),              // the same build: nothing
			mc("0.1.0+6ee3170", "", true),              // another build
			mc("0.1.0+6ee3170", "", false),             // another build, still listing
			mc("0.1.0+04c5fef", "0.1.0+abcdef0", true), // the same build, an update waits
			mc("0.1.0+6ee3170", "unknown", true),       // both
		}
		c := screen.New(80, len(heads))
		for y, h := range heads {
			c.Styled(0, y, m.render(h, 80), 80)
		}
		return golden.Screen{Cols: 80, Rows: len(heads), Lines: c.Lines()}
	})

	m := New(ModelOptions{Features: every})
	if got := m.otherVersion(&machine{name: "macmini", version: "0.1.0+6ee3170"}); got != "" {
		t.Fatalf("without this machine's version the heading names %q", got)
	}
	feed(m, Event{Kind: Online, Version: "0.1.0+04c5fef"}, Event{Machine: "macmini", Kind: Online, Version: "0.1.0+6ee3170"},
		Event{Machine: "macmini", Kind: Build, Installed: "0.1.0+abcdef0"})
	mm := m.machines[len(m.machines)-1]
	if mm.version != "0.1.0+6ee3170" || mm.installed != "0.1.0+abcdef0" || m.otherVersion(mm) != "0.1.0+6ee3170" {
		t.Fatalf("macmini holds %q %q", mm.version, mm.installed)
	}
	feed(m, Event{Machine: "macmini", Kind: Build})
	if mm.installed != "" || mm.version != "0.1.0+6ee3170" {
		t.Fatalf("after the restart macmini holds %q %q", mm.version, mm.installed)
	}
}
