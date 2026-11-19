package integration

// The whole screens of 5a.9 through the real client (P4-05): the model of
// internal/client driven by clienttest.Driver, with the look of
// internal/client/chrome, the real hub of internal/hub as its overlay, and
// the example state of 5a.9 fed as the fleet feeds it, as fleet.Updates and
// hub.Events. The eight goldens are the screens where regions compose (two
// panes, a banner over panes, a mode in a stale view, the hub over the
// chrome); every single region already has its golden in P4-02 to P4-04.
// Each golden has three layers (5a.17): the text, typed from the mockup and
// never written by -update, and the truecolor and palette style layers.
//
// TestGallery prints every screen and illustration of 5a.9 with its escape
// sequences, for a person to look at in a real terminal (K4).

import (
	"fmt"
	"image/color"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"devtools/internal/agent"
	"devtools/internal/client"
	"devtools/internal/client/chrome"
	"devtools/internal/client/clienttest"
	"devtools/internal/client/keys"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"
)

// galleryMode is DEVTOOLS_GALLERY as the test binary was started with. It
// is read when the package is initialised, because TestMain (slice_test.go)
// removes every DEVTOOLS_ variable before the tests run.
var galleryMode = os.Getenv("DEVTOOLS_GALLERY")

// lookColors are the five `color` lines the systems config generates from
// palette.nix (5a.7.1); the truecolor tier draws their dark values.
var lookColors = []config.Color{
	{Token: "strong", Light: "#111214", Dark: "#e6e7eb", Line: 1},
	{Token: "muted", Light: "#55585f", Dark: "#a6a8b0", Line: 2},
	{Token: "raised", Light: "#e3e4e8", Dark: "#30323a", Line: 3},
	{Token: "selection", Light: "#d6dde9", Dark: "#3a4150", Line: 4},
	{Token: "warning", Light: "#b04a00", Dark: "#ec8a3f", Line: 5},
}

// tier is a colour tier of the outer terminal: what bubbletea reports as its
// profile. 256 colours are not 24-bit, so ANSI256 is the palette tier with
// the tokens unused (5a.16.1).
type tier struct {
	name    string
	profile colorprofile.Profile
}

var (
	truecolorTier = tier{"truecolor", colorprofile.TrueColor}
	paletteTier   = tier{"palette", colorprofile.ANSI256}
	bothTiers     = []tier{truecolorTier, paletteTier}
)

// darkBackground and lightBackground are what a terminal answers when
// asked for its background colour.
var (
	darkBackground  = color.RGBA{0x1a, 0x1b, 0x26, 0xff}
	lightBackground = color.RGBA{0xfa, 0xfa, 0xfb, 0xff}
)

// --- the example state of 5a.9 ---

// agentStatus is the status of an agent in the fleet's summary.
type agentStatus = agent.Status

// The machines of the example: this one, desktop, and two of the config,
// macmini and macbook.
const (
	desktop = "desktop"
	macmini = "macmini"
	macbook = "macbook"
)

// world is the example state of 5a.9: three machines and five agents. The
// desktop server has devtools-main (three tabs: a shell, the claude that
// needs the user, an editor), devtools-replace-herdr with a working claude,
// devtools-fix-footer, and systems-main with an idle claude. macmini has
// devtools-main with a claude that is done, blog-main with a working codex,
// and notes-main, the session of the stale mockups. macbook is configured
// and off. The sessions of macmini in the hub feed are those of its catalog
// rows, as in the hub mockups.
type world struct {
	machines []fleet.MachineState // desktop, macmini, macbook: all well
	counts   map[string]int       // the roll-up over the online machines
	local    []hub.Event          // what desktop reports to the hub
	mini     []hub.Event          // what macmini reports, without the end of its scan
}

// Paths of the catalog rows, as the hub's own fixtures have them.
const (
	localRoot = "/dev"
	miniRoot  = "/Users/florian/dev"
	dtMainDir = localRoot + "/devtools/main"
	notesDir  = "/Users/florian/notes"
)

func worktreeItem(machine, root, project, name string) hub.Item {
	return hub.Item{
		Kind: hub.KindWorktree, Machine: machine, Project: project, Worktree: name,
		Label: project + "/" + name, Path: root + "/" + project + "/" + name, Root: root,
		Session: project + "-" + name,
	}
}

func sessionItem(it hub.Item, id string) hub.Item {
	return hub.Item{Label: it.Session, Session: it.Session, SessionID: id, Path: it.Path}
}

func agentItem(it hub.Item, id, tab, pane string, index int, kind, status, title string) hub.Item {
	return hub.Item{
		Kind: hub.KindAgent, Session: it.Session, SessionID: id, Tab: tab, Pane: pane,
		TabIndex: index, Agent: kind, Status: status, State: ui.Session{Title: title},
	}
}

// example builds the world. miniStatus is the status of macmini's claude in
// devtools-main: "done" in the mockups, "blocked" when it waits for the user
// (5a.10.5).
func example(miniStatus string) world {
	dtMain := worktreeItem("", localRoot, "devtools", "main")
	dtReplace := worktreeItem("", localRoot, "devtools", "replace-herdr")
	dtFooter := worktreeItem("", localRoot, "devtools", "fix-footer")
	sysMain := worktreeItem("", localRoot, "systems", "main")
	notesMain := worktreeItem("", localRoot, "notes", "main")
	miniDev := worktreeItem(macmini, miniRoot, "devtools", "main")
	miniBlog := worktreeItem(macmini, miniRoot, "blog", "main")
	strip := func(it hub.Item) hub.Item { it.Machine = ""; return it }

	w := world{
		machines: []fleet.MachineState{
			{Host: desktop, State: "online", Agents: agentCounts("blocked", 1, "working", 1, "idle", 1)},
			{Name: macmini, Host: macmini, State: "online", Agents: agentCounts(miniStatus, 1, "working", 1)},
			{Name: macbook, State: "off"},
		},
		counts: map[string]int{"blocked": 1, "working": 2},
		local: []hub.Event{
			{Kind: hub.Online},
			{Kind: hub.Items, Items: []hub.Item{dtMain, dtReplace, dtFooter, sysMain, notesMain}},
			{Kind: hub.Git, Path: dtMain.Path, Git: ui.Git{Changes: 3, Ahead: 2, HasUpstream: true}},
			{Kind: hub.Git, Path: dtFooter.Path, Git: ui.Git{Changes: 1, HasUpstream: true}},
			{Kind: hub.Git, Path: sysMain.Path, Git: ui.Git{Changes: 12, Behind: 1, HasUpstream: true}},
			{Kind: hub.Complete},
			{Kind: hub.Sessions, Items: []hub.Item{
				sessionItem(dtMain, "s1"), sessionItem(dtReplace, "s2"), sessionItem(dtFooter, "s3"), sessionItem(sysMain, "s4"),
			}},
			{Kind: hub.Agents, Items: []hub.Item{
				agentItem(dtMain, "s1", "t2", "p3", 2, proto.AgentClaude, "blocked", "Fix the hub footer"),
				agentItem(dtReplace, "s2", "t5", "p6", 2, proto.AgentClaude, "working", "Plan part 4"),
				agentItem(sysMain, "s4", "t9", "p10", 3, proto.AgentClaude, "idle", ""),
			}},
		},
		mini: []hub.Event{
			{Machine: macmini, Kind: hub.Connecting},
			{Machine: macmini, Kind: hub.Online},
			{Machine: macmini, Kind: hub.Items, Items: []hub.Item{strip(miniDev), strip(miniBlog)}},
			{Machine: macmini, Kind: hub.Sessions, Items: []hub.Item{sessionItem(miniDev, "m1"), sessionItem(miniBlog, "m2")}},
			{Machine: macmini, Kind: hub.Agents, Items: []hub.Item{
				agentItem(miniDev, "m1", "u1", "q1", 1, proto.AgentClaude, miniStatus, "Review PR 212"),
				agentItem(miniBlog, "m2", "u2", "q2", 1, proto.AgentCodex, "working", "Draft the post"),
			}},
		},
	}
	w.counts[miniStatus]++
	return w
}

func agentCounts(kv ...any) map[agentStatus]int {
	out := map[agentStatus]int{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[agentStatus(kv[i].(string))] += kv[i+1].(int)
	}
	return out
}

// summary is the fleet's summary in mode with machines in the given states
// and the roll-up counts; Top is the most urgent status counted.
func (w world) summary(mode string, machines []fleet.MachineState, counts map[string]int) fleet.Update {
	s := fleet.SummaryData{Mode: mode, Machines: machines, Counts: map[agentStatus]int{}}
	for k, v := range counts {
		s.Counts[agentStatus(k)] = v
	}
	for _, st := range []string{"blocked", "done", "working"} {
		if s.Counts[agentStatus(st)] > 0 {
			s.Top = agentStatus(st)
			break
		}
	}
	return fleet.Update{Kind: fleet.Summary, Summary: s}
}

// well is the summary while every machine is well: macmini online, macbook
// off with nothing busy, so the fleet strip lists neither.
func (w world) well() fleet.Update { return w.summary("showing", w.machines, w.counts) }

// macbookConnecting is the summary of hub-all-80x24: macmini still lists
// and macbook is being dialled.
func (w world) macbookConnecting() fleet.Update {
	ms := append([]fleet.MachineState(nil), w.machines...)
	ms[2] = fleet.MachineState{Name: macbook, State: "connecting"}
	return w.summary("showing", ms, w.counts)
}

// lostMini is the summary after macmini was lost: it retries, its agents are
// not counted, and the roll-up counts this machine only.
func (w world) lostMini(mode string) fleet.Update {
	ms := append([]fleet.MachineState(nil), w.machines...)
	ms[1] = fleet.MachineState{Name: macmini, Host: macmini, State: "retrying", Problem: hub.Problem{Reason: "lost", Retry: "retrying"}}
	u := w.summary(mode, ms, map[string]int{"blocked": 1, "working": 1})
	u.Summary.Stale = true
	return u
}

// hubAll is the hub feed of hub-all-80x24: macmini still listing, macbook
// connecting.
func (w world) hubAll() []hub.Event {
	return concatEvents(w.local, w.mini, []hub.Event{{Machine: macbook, Kind: hub.Connecting}})
}

// hubStale is the hub feed of hub-stale-80x24: macmini listed and then
// lost, macbook dialled and closed with nothing busy.
func (w world) hubStale() []hub.Event {
	return concatEvents(w.local, w.mini, []hub.Event{
		{Machine: macmini, Kind: hub.Complete},
		{Machine: macbook, Kind: hub.Connecting},
		{Machine: macbook, Kind: hub.Closed},
		{Machine: macmini, Kind: hub.Failed, Err: hub.Problem{Reason: "lost", Retry: "retrying"}},
	})
}

// hubAgents is the hub feed of hub-agents-80x24: macmini listed completely.
func (w world) hubAgents() []hub.Event {
	return concatEvents(w.local, w.mini, []hub.Event{
		{Machine: macmini, Kind: hub.Complete},
		{Machine: macbook, Kind: hub.Connecting},
	})
}

func concatEvents(lists ...[]hub.Event) []hub.Event {
	var out []hub.Event
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// --- the servers' states ---

// tab1 is the first tab of a session: its panes' programs, how they are
// split, and which of them has the keys.
type tab1 struct {
	programs []string
	split    string
	focus    int
}

func onePane(program string) tab1 { return tab1{programs: []string{program}} }

// desktopState is the state of the desktop server: devtools-main with tab 1
// as given, tab 2 the claude that needs the user and tab 3 running tab3;
// the other three sessions of the example and their agents.
func desktopState(t1 tab1, tab3 string) proto.Snapshot {
	s := proto.Snapshot{Rev: 1}
	addSession(&s, proto.Session{ID: "s1", Dir: dtMainDir, Label: "devtools-main"}, []tabSpec{
		{id: "t1", t1: t1, ids: []string{"p1", "p2"}},
		{id: "t2", t1: onePane("claude"), ids: []string{"p3"}},
		{id: "t3", t1: onePane(tab3), ids: []string{"p4"}},
	})
	addSession(&s, proto.Session{ID: "s2", Dir: localRoot + "/devtools/replace-herdr", Label: "devtools-replace-herdr"}, []tabSpec{
		{id: "t4", t1: onePane("zsh"), ids: []string{"p5"}},
		{id: "t5", t1: onePane("claude"), ids: []string{"p6"}},
	})
	addSession(&s, proto.Session{ID: "s3", Dir: localRoot + "/devtools/fix-footer", Label: "devtools-fix-footer"}, []tabSpec{
		{id: "t6", t1: onePane("zsh"), ids: []string{"p7"}},
	})
	addSession(&s, proto.Session{ID: "s4", Dir: localRoot + "/systems/main", Label: "systems-main"}, []tabSpec{
		{id: "t7", t1: onePane("zsh"), ids: []string{"p8"}},
		{id: "t8", t1: onePane("nvim"), ids: []string{"p9"}},
		{id: "t9", t1: onePane("claude"), ids: []string{"p10"}},
	})
	s.Agents = []proto.Agent{
		{Pane: "p3", Tab: "t2", Session: "s1", Kind: proto.AgentClaude, Status: proto.StatusBlocked, Title: "Fix the hub footer"},
		{Pane: "p6", Tab: "t5", Session: "s2", Kind: proto.AgentClaude, Status: proto.StatusWorking, Title: "Plan part 4"},
		{Pane: "p10", Tab: "t9", Session: "s4", Kind: proto.AgentClaude, Status: proto.StatusIdle},
	}
	return s
}

// miniState is the state of the macmini server: notes-main with tab 1 as
// given and tab 2 a claude of status notesAgent, and the two sessions of its
// catalog with their agents.
func miniState(t1 tab1, notesAgent string) proto.Snapshot {
	s := proto.Snapshot{Rev: 1}
	addSession(&s, proto.Session{ID: "m3", Dir: notesDir, Label: "notes-main"}, []tabSpec{
		{id: "u3", t1: t1, ids: []string{"q3", "q4"}},
		{id: "u4", t1: onePane("claude"), ids: []string{"q5"}},
	})
	addSession(&s, proto.Session{ID: "m1", Dir: miniRoot + "/devtools/main", Label: "devtools-main"}, []tabSpec{
		{id: "u1", t1: onePane("claude"), ids: []string{"q1"}},
	})
	addSession(&s, proto.Session{ID: "m2", Dir: miniRoot + "/blog/main", Label: "blog-main"}, []tabSpec{
		{id: "u2", t1: onePane("codex"), ids: []string{"q2"}},
	})
	s.Agents = []proto.Agent{
		{Pane: "q5", Tab: "u4", Session: "m3", Kind: proto.AgentClaude, Status: notesAgent},
		{Pane: "q1", Tab: "u1", Session: "m1", Kind: proto.AgentClaude, Status: proto.StatusDone, Title: "Review PR 212"},
		{Pane: "q2", Tab: "u2", Session: "m2", Kind: proto.AgentCodex, Status: proto.StatusWorking, Title: "Draft the post"},
	}
	return s
}

type tabSpec struct {
	id  string
	t1  tab1
	ids []string // pane ids, as many as the tab has programs
}

func addSession(s *proto.Snapshot, se proto.Session, tabs []tabSpec) {
	for i, ts := range tabs {
		ids := ts.ids[:len(ts.t1.programs)]
		s.Tabs = append(s.Tabs, proto.Tab{ID: ts.id, Session: se.ID, Index: i + 1, Panes: ids,
			Split: ts.t1.split, Focused: ids[ts.t1.focus]})
		se.Tabs = append(se.Tabs, ts.id)
		for j, id := range ids {
			s.Panes = append(s.Panes, proto.Pane{ID: id, Tab: ts.id, Session: se.ID, State: proto.PaneRunning,
				Cwd: se.Dir, Program: ts.t1.programs[j]})
		}
	}
	se.Current = se.Tabs[0]
	s.Sessions = append(s.Sessions, se)
}

// shown commits the view of session id of the state on machine.
func shown(machine string, s proto.Snapshot, id string) fleet.Update {
	for _, se := range s.Sessions {
		if se.ID != id {
			continue
		}
		for _, t := range s.Tabs {
			if t.ID == se.Current {
				return fleet.Update{Kind: fleet.Shown, Target: session.Target{Machine: machine, Session: id},
					View: proto.ViewOK{Session: id, Dir: se.Dir, Tab: t.ID, Panes: t.Panes}}
			}
		}
	}
	panic("shown: no session " + id)
}

// paneFrame is the first, full frame of a pane of cols by rows, with the
// cursor at x, y (hidden when x is negative) and rows 0.. from lines.
func paneFrame(pane string, cols, rows, x, y int, lines ...string) fleet.Update {
	f := proto.Frame{Pane: pane, Seq: 1, Cols: cols, Rows: rows, Full: true,
		Cursor: proto.Cursor{X: max(x, 0), Y: y, Visible: x >= 0, Shape: "block"}}
	for i := range rows {
		l := proto.Line{Y: i}
		if i < len(lines) {
			l.S = lines[i]
		}
		f.Lines = append(f.Lines, l)
	}
	return fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(f)}
}

// What the panes of the mockups show.
var (
	nvimLines = []string{"  1 package hub", "  2", "  3 import (", `  4     "strings"`, "  5 )"}
	testLines = []string{"$ go test ./internal/hub/ -count=1", "ok      devtools/internal/hub   0.412s", "$"}
	gitLines  = []string{"$ git status --short", " M internal/hub/view.go", "$"}
)

// --- the window ---

// window is a client in an outer terminal of one colour tier.
type window struct {
	*clienttest.Driver
	w world
	// hub is what the fleet feeds the next hub that opens, from its start.
	hub []hub.Event
}

// newWindow starts a client of cols by rows with the look, the default
// binding table and the real hub, in tier tr, on a dark terminal (light when
// DEVTOOLS_GALLERY is light and light is set).
func newWindow(t *testing.T, tr tier, cols, rows int, light bool, opts ...func(*client.Options)) *window {
	t.Helper()
	win := &window{w: example("done")}
	table := keys.Defaults()
	o := client.Options{
		OpenHub: win.openHub,
		Keys:    table,
		Chrome:  chrome.NewLook(chrome.LookOptions{Chords: table.Chords}),
		Colors:  lookColors,
		Env:     []string{"LANG=C.UTF-8"},
	}
	for _, f := range opts {
		f(&o)
	}
	win.Driver = clienttest.New(t, o)
	if cols != 80 || rows != 24 {
		win.Send(tea.WindowSizeMsg{Width: cols, Height: rows})
	}
	bg := darkBackground
	if light {
		bg = lightBackground
	}
	win.Send(tea.BackgroundColorMsg{Color: bg})
	win.Send(tea.ColorProfileMsg{Profile: tr.profile})
	return win
}

// openHub is the hub constructor of the devtools backend
// (internal/backend/devtools.go, openHub) with what the overlay host knows:
// the request, the theme, every part of the hub, this machine's name. The
// fleet's feed is the window's hub events, all given before the hub reads
// them and then closed, so that the synchronous driver never waits for an
// event; the fake fleet's own channel has no sender.
func (win *window) openHub(req fleet.HubRequest, _ <-chan hub.Event, theme ui.Theme) chrome.Overlay {
	events := make(chan hub.Event, len(win.hub))
	for _, ev := range win.hub {
		events <- ev
	}
	close(events)
	return hub.New(hub.ModelOptions{
		Mode: req.Mode, Query: req.Query, Here: req.Here, Events: events,
		Actions: lookActions{}, Theme: theme,
		Features: hub.Features{Agents: true, Machines: true}, Name: desktop,
	})
}

// lookActions are hub actions that are never carried out here.
type lookActions struct{}

func (lookActions) Create(hub.CreateRequest) (hub.Item, error) {
	return hub.Item{}, fmt.Errorf("not in this test")
}
func (lookActions) Merge(hub.Item) (hub.Item, error) {
	return hub.Item{}, fmt.Errorf("not in this test")
}

// show commits the view of session id of state s on machine, after the
// summary sum, then gives each pane its first frame.
func (win *window) show(sum fleet.Update, machine string, s proto.Snapshot, id string, frames ...fleet.Update) {
	win.F.SetState(s)
	win.Update(sum)
	win.Update(shown(machine, s, id))
	for _, f := range frames {
		win.Update(f)
	}
}

// open presses the prefix and then key.
func (win *window) open(key string) {
	win.Key("ctrl+space")
	win.Key(key)
}

// --- the screens ---

// A screen is one mockup: how a window of its size shows it in a tier.
type lookScreen struct {
	id         string
	cols, rows int
	build      func(t *testing.T, win *window)
}

// singleLocal is chrome-single-80x24: one pane of devtools-main.
func singleLocal(cols, rows int) func(t *testing.T, win *window) {
	return func(t *testing.T, win *window) {
		win.show(win.w.well(), "", desktopState(onePane("zsh"), "nvim"), "s1",
			paneFrame("p1", cols, rows-1, 2, 5, "~/dev/devtools/main main", "$ go test ./internal/hub/ -count=1",
				"ok      devtools/internal/hub   0.412s", "", "~/dev/devtools/main main", "$"))
	}
}

// splitRight is chrome-split-right-80x24: nvim left with the keys, a shell
// right.
func splitRight(t *testing.T, win *window) {
	win.show(win.w.well(), "", desktopState(tab1{programs: []string{"nvim", "zsh"}, split: proto.SplitRight}, "zsh"), "s1",
		paneFrame("p1", 40, 23, 4, 0, nvimLines...),
		paneFrame("p2", 39, 23, 2, 2, testLines...))
}

var lookScreens = []lookScreen{
	{"chrome-split-right-80x24", 80, 24, splitRight},
	{"chrome-split-down-80x24", 80, 24, func(t *testing.T, win *window) {
		win.show(win.w.well(), "", desktopState(tab1{programs: []string{"nvim", "zsh"}, split: proto.SplitDown, focus: 1}, "nvim"), "s1",
			paneFrame("p1", 80, 11, 4, 0, nvimLines[:3]...),
			paneFrame("p2", 80, 11, 2, 2, gitLines...))
	}},
	{"chrome-stale-80x24", 80, 24, staleSingle},
	{"chrome-stale-split-prefix-80x24", 80, 24, func(t *testing.T, win *window) {
		win.show(win.w.well(), macmini, miniState(tab1{programs: []string{"zsh", "zsh"}, split: proto.SplitRight}, proto.StatusIdle), "m3",
			paneFrame("q3", 40, 23, 2, 3, "~/notes main", `$ rg -n "status row" docs/`, "docs/plan.md:12:the status row", "$"),
			paneFrame("q4", 39, 23, 2, 3, "~/notes main", "$ ls", "inbox.md  plan.md  reading.md", "$"))
		win.lose()
		win.Key("ctrl+space")
	}},
	{"chrome-copy-80x24", 80, 24, func(t *testing.T, win *window) {
		copyScreen(t, win)
	}},
	{"chrome-exited-split-80x24", 80, 24, func(t *testing.T, win *window) {
		s := desktopState(tab1{programs: []string{"nvim", "zsh"}, split: proto.SplitRight}, "zsh")
		for i, p := range s.Panes {
			if p.ID == "p2" {
				s.Panes[i].State = proto.PaneExited
				s.Panes[i].Exit = &proto.PaneExit{Signal: "signal 9"}
			}
		}
		win.show(win.w.well(), "", s, "s1",
			paneFrame("p1", 40, 23, 4, 0, nvimLines...),
			paneFrame("p2", 39, 23, -1, 0))
	}},
	{"hub-all-80x24", 80, 24, func(t *testing.T, win *window) {
		win.show(win.w.macbookConnecting(), "", desktopState(onePane("zsh"), "nvim"), "s1",
			paneFrame("p1", 80, 23, 2, 5, "~/dev/devtools/main main", "$"))
		win.hub = win.w.hubAll()
		win.open("o")
	}},
	{"hub-stale-80x24", 80, 24, func(t *testing.T, win *window) {
		win.show(win.w.lostMini("showing"), "", desktopState(onePane("zsh"), "nvim"), "s1",
			paneFrame("p1", 80, 23, 2, 5, "~/dev/devtools/main main", "$"))
		win.hub = win.w.hubStale()
		win.open("o")
	}},
}

// staleSingle is chrome-stale-80x24: notes-main of macmini on screen, then
// macmini lost.
func staleSingle(t *testing.T, win *window) {
	showNotes(win)
	win.lose()
}

// showNotes shows notes-main of macmini, one shell in tab 1; its row 0 is
// under the banner once the machine is lost.
func showNotes(win *window) {
	win.show(win.w.well(), macmini, miniState(onePane("zsh"), proto.StatusIdle), "m3",
		paneFrame("q3", 80, 23, 2, 4, "$ cd ~/notes", "~/notes main", `$ rg -n "status row" docs/`,
			"docs/plan.md:12:the status row", "$"))
}

// lose is what the fleet sends when the machine of the view is lost
// (3.5.5): the view turns stale, then the summary without its agents.
func (win *window) lose() {
	win.Update(fleet.Update{Kind: fleet.Stale, Target: session.Target{Machine: macmini, Session: "m3"},
		Problem: hub.Problem{Reason: "lost", Retry: "retrying"}})
	win.Update(win.w.lostMini("stale"))
}

// copyScreen is chrome-copy-80x24: a shell with 8,090 lines of history;
// copy mode moved up to line 7,412 and selected it and the two lines below
// by line. The server answers the pane.scroll with the frame of those
// lines.
func copyScreen(t *testing.T, win *window) {
	t.Helper()
	const first, base, top = 0, 8090, 7410
	f := proto.Frame{Pane: "p1", Seq: 1, Cols: 80, Rows: 23, Full: true, First: first, Base: base,
		Cursor: proto.Cursor{X: 2, Y: 4, Visible: true, Shape: "block"}}
	live := []string{`$ rg -n "= [0-9]" internal/hub/view.go`, "14:    gutter = 2 // the here mark and a space",
		"15:    indent = 2 // worktrees under their project", "16:    gap    = 3 // between columns", "$ "}
	for y := range 23 {
		l := proto.Line{Y: y}
		if y < len(live) {
			l.S = live[y]
		}
		f.Lines = append(f.Lines, l)
	}
	win.show(win.w.well(), "", desktopState(onePane("zsh"), "nvim"), "s1", fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(f)})
	win.open("[")
	at := int64(base + 4)
	for ; at > top+3; at-- {
		win.Type("k")
	}
	win.Type("V")
	win.Type("kkk")
	win.Type("j")
	// the server's frame of the scrolled pane: the same output, 680 lines
	// further up
	t2 := int64(top)
	f.Seq, f.Top = 2, &t2
	win.Update(fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(f)})
}

// drawScreen builds screen sc in tier tr and returns what the window shows.
func drawScreen(t *testing.T, sc lookScreen, tr tier, light bool) (*window, golden.Screen) {
	t.Helper()
	win := newWindow(t, tr, sc.cols, sc.rows, light)
	sc.build(t, win)
	return win, win.Screen()
}

func screenOf(id string) lookScreen {
	for _, sc := range lookScreens {
		if sc.id == id {
			return sc
		}
	}
	panic("no screen " + id)
}

func lookGolden(t *testing.T, id string) {
	t.Helper()
	sc := screenOf(id)
	_, tc := drawScreen(t, sc, truecolorTier, false)
	_, pal := drawScreen(t, sc, paletteTier, false)
	golden.Look(t, "look-"+id, tc, pal)
}

func TestLookChromeScreens(t *testing.T) {
	for _, id := range []string{"chrome-split-right-80x24", "chrome-split-down-80x24", "chrome-stale-80x24",
		"chrome-stale-split-prefix-80x24", "chrome-copy-80x24", "chrome-exited-split-80x24"} {
		t.Run(id, func(t *testing.T) { lookGolden(t, id) })
	}
}

func TestLookHubScreens(t *testing.T) {
	for _, id := range []string{"hub-all-80x24", "hub-stale-80x24"} {
		t.Run(id, func(t *testing.T) { lookGolden(t, id) })
	}
}

// --- the rules no golden shows ---

// standard is a window of chrome-single-80x24 in the palette tier.
func standard(t *testing.T, opts ...func(*client.Options)) *window {
	t.Helper()
	win := newWindow(t, paletteTier, 80, 24, false, opts...)
	singleLocal(80, 24)(t, win)
	return win
}

// cellOf is the column of the first cell of text in row y, -1 when the row
// does not hold it.
func cellOf(s golden.Screen, y int, text string) int {
	row := s.Row(y)
	i := strings.Index(row, text)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(row[:i])
}

func TestLookNothingMovesWhenTheHubOpens(t *testing.T) {
	win := newWindow(t, paletteTier, 80, 24, false)
	splitRight(t, win)
	before := win.Screen()
	resizes := len(win.F.CallsOf("Resize"))
	win.hub = win.w.hubAll()
	win.open("o")
	if !win.Screen().Contains("Switch to a worktree") {
		t.Fatalf("the hub did not open:\n%s", win.Screen().Plain())
	}
	if got := len(win.F.CallsOf("Resize")); got != resizes {
		t.Errorf("opening the hub sent %d resizes: %v", got-resizes, win.F.CallsOf("Resize")[resizes:])
	}
	if got := win.Screen().Row(23); got != before.Row(23) {
		t.Errorf("the status row changed under the hub:\n want %q\n  got %q", before.Row(23), got)
	}
	win.Key("esc")
	after := win.Screen()
	if got := len(win.F.CallsOf("Resize")); got != resizes {
		t.Errorf("closing the hub sent %d resizes", got-resizes)
	}
	if msg, ok := golden.Diff(before.Runs(), after.Runs()); !ok {
		t.Errorf("the panes are not where they were after the hub closed: %s", msg)
	}
}

func TestLookPrefixKeepsEveryTabCell(t *testing.T) {
	win := standard(t)
	before := win.Screen()
	win.Key("ctrl+space")
	after := win.Screen()
	if !strings.HasPrefix(after.Row(23), " prefix") {
		t.Fatalf("no prefix block: %q", after.Row(23))
	}
	for _, tab := range []string{"1 zsh", "2 claude ◆", "3 nvim"} {
		x0, x1 := cellOf(before, 23, tab), cellOf(after, 23, tab)
		if x0 < 0 || x0 != x1 {
			t.Errorf("tab %q at column %d before the prefix and %d with it\n before %q\n   with %q", tab, x0, x1, before.Row(23), after.Row(23))
		}
	}
}

func TestLookHintStripComesAfter600msAndAtOnceAfterQuestionMark(t *testing.T) {
	win := standard(t)
	win.Key("ctrl+space")
	win.Advance(ui.HintDelay - time.Millisecond)
	if row := win.Screen().Row(23); strings.Contains(row, "esc cancel") {
		t.Fatalf("hints before %v: %q", ui.HintDelay, row)
	}
	win.Advance(time.Millisecond)
	first := win.Screen().Row(23)
	if !strings.HasPrefix(first, " prefix   esc cancel   o hub") || !strings.HasSuffix(first, "? more") {
		t.Fatalf("after %v: %q, want the first page of hints", ui.HintDelay, first)
	}
	win.Key("esc")
	if row := win.Screen().Row(23); strings.Contains(row, "prefix") {
		t.Fatalf("esc left the prefix: %q", row)
	}
	win.Key("ctrl+space")
	win.Key("?")
	if row := win.Screen().Row(23); row != first {
		t.Fatalf("after ?: %q, want the first page at once %q", row, first)
	}
	win.Key("?")
	if row := win.Screen().Row(23); !strings.HasPrefix(row, " prefix   n next tab") {
		t.Fatalf("after ? ?: %q, want the second page", row)
	}
}

func TestLookAnUnboundKeyEndsThePrefixWithANotice(t *testing.T) {
	win := standard(t)
	win.F.Reset()
	win.Key("ctrl+space")
	win.Key("z")
	row := win.Screen().Row(23)
	if strings.Contains(row, "prefix") || !strings.Contains(row, "No binding for z") {
		t.Fatalf("status row %q, want the notice and no prefix", row)
	}
	if in := win.F.Inputs(); len(in) != 0 {
		t.Fatalf("the key reached the pane: %v", in)
	}
	win.Advance(ui.NoticeLife)
	if row := win.Screen().Row(23); strings.Contains(row, "No binding") || !strings.Contains(row, "1 zsh") {
		t.Fatalf("after %v: %q, want the tabs back", ui.NoticeLife, row)
	}
}

func TestLookSwitchingRowComesAfter150ms(t *testing.T) {
	win := standard(t)
	before := win.Screen()
	sw := win.w.well()
	sw.Summary.Mode = "switching"
	sw.Summary.Target = session.Target{Machine: macmini, Session: "m3", Name: "notes-main"}
	win.Update(sw)
	win.Advance(ui.Slow - time.Millisecond)
	if row := win.Screen().Row(23); row != before.Row(23) {
		t.Fatalf("the status row changed before %v: %q", ui.Slow, row)
	}
	win.Advance(time.Millisecond)
	if row := win.Screen().Row(23); !strings.HasPrefix(row, " switching   to macmini notes-main…") {
		t.Fatalf("after %v: %q, want the switching row", ui.Slow, row)
	}
}

func TestLookAnIdleMinuteRedrawsOnlyTheLastRow(t *testing.T) {
	win := standard(t)
	before := win.Screen()
	if !strings.HasSuffix(before.Row(23), "14:32") {
		t.Fatalf("status row %q, want the clock at 14:32", before.Row(23))
	}
	win.Advance(clienttest.Start.Truncate(time.Minute).Add(time.Minute).Sub(win.Now()))
	after := win.Screen()
	for y := range 23 {
		if before.Lines[y] != after.Lines[y] {
			t.Errorf("row %d changed in an idle minute:\n before %q\n  after %q", y, before.Lines[y], after.Lines[y])
		}
	}
	if !strings.HasSuffix(after.Row(23), "14:33") {
		t.Errorf("status row %q, want the clock at 14:33", after.Row(23))
	}
	if strings.TrimSuffix(after.Row(23), "14:33") != strings.TrimSuffix(before.Row(23), "14:32") {
		t.Errorf("more than the clock changed:\n before %q\n  after %q", before.Row(23), after.Row(23))
	}
}

func TestLookStaleKeepsEveryCharacter(t *testing.T) {
	win := newWindow(t, truecolorTier, 80, 24, false)
	showNotes(win)
	before := win.Screen()
	win.lose()
	after := win.Screen()
	if !strings.HasPrefix(after.Row(0), " macmini  not reachable.") {
		t.Fatalf("row 0 %q, want the banner", after.Row(0))
	}
	for y := 1; y < 23; y++ {
		if before.Row(y) != after.Row(y) {
			t.Errorf("row %d lost characters:\n before %q\n  after %q", y, before.Row(y), after.Row(y))
		}
	}
	if after.CursorVisible {
		t.Errorf("the cursor is shown at %d,%d in a stale view", after.CursorX, after.CursorY)
	}
}

func TestLookClosingTheHubWithNoSessionEndsTheClient(t *testing.T) {
	win := newWindow(t, paletteTier, 80, 24, false, func(o *client.Options) { o.Hub = &fleet.HubRequest{} })
	win.Update(win.w.summary("none", win.w.machines, nil))
	// the hub of a plain terminal opened at the start, before the feed: it
	// shows the headings only
	if !win.Screen().Contains("Switch to a worktree") {
		t.Fatalf("the client did not start in the hub:\n%s", win.Screen().Plain())
	}
	if win.Quit() {
		t.Fatal("the client ended before the hub closed")
	}
	win.Key("esc")
	if !win.Quit() {
		t.Fatalf("closing the hub with no session left the client running:\n%s", win.Screen().Plain())
	}
}

func TestLookBellChangesTheRollupAndShowsNoNotice(t *testing.T) {
	win := standard(t)
	before := win.Screen().Row(23)
	if !strings.HasSuffix(before, "◆ 1  ✓ 1  ● 2   14:32") {
		t.Fatalf("status row %q before the bell", before)
	}
	// the claude on macmini finished and waits for the user: the summary
	// counts it, and the bell of its status change rings (3.6.3)
	w := example("blocked")
	win.Update(w.well())
	win.Update(fleet.Update{Kind: fleet.Bell, Target: session.Target{Machine: macmini, Session: "m1", Tab: "u1", Pane: "q1"}})
	after := win.Screen().Row(23)
	if !strings.HasSuffix(after, "◆ 2  ● 2   14:32") {
		t.Errorf("status row %q, want the roll-up of two agents that need the user", after)
	}
	if !strings.HasPrefix(after, " desktop  devtools-main   1 zsh  2 claude ◆  3 nvim") {
		t.Errorf("status row %q, want the address and the tabs as they were: a bell shows no notice", after)
	}
	bells := 0
	for _, r := range win.Raw() {
		bells += strings.Count(r, "\a")
	}
	if bells != 1 {
		t.Errorf("%d BELs written, want one: %q", bells, win.Raw())
	}
}

func TestLookAgentsScopeJumpsToThePane(t *testing.T) {
	// R8: prefix+a, enter, and the window shows the agent's pane: the
	// switch names its machine, session, tab and pane, for an agent of this
	// machine and for one of another
	cases := []struct {
		name  string
		downs int
		row   string
		want  session.Target
	}{
		{"the claude of desktop that needs the user", 0, "◆ needs you   desktop   devtools-main",
			session.Target{Session: "s1", Tab: "t2", Pane: "p3"}},
		{"the claude of macmini that is done", 1, "✓ done        macmini   devtools-main",
			session.Target{Machine: macmini, Session: "m1", Tab: "u1", Pane: "q1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			win := standard(t)
			win.hub = win.w.hubAgents()
			win.open("a")
			sc := win.Screen()
			if !strings.Contains(sc.Row(0), "Jump to an agent") {
				t.Fatalf("prefix+a did not open the agents scope:\n%s", sc.Plain())
			}
			if row := sc.Row(2); !strings.Contains(row, "◆ needs you   desktop") {
				t.Fatalf("first row %q, want the agent that needs the user", row)
			}
			for range c.downs {
				win.Key("down")
			}
			if row := win.Screen().Row(2 + c.downs); !strings.Contains(row, c.row) {
				t.Fatalf("row %d %q, want %q", 2+c.downs, row, c.row)
			}
			win.F.Reset()
			win.Key("enter")
			sw := win.F.CallsOf("Switch")
			if len(sw) != 1 || sw[0].Target != c.want {
				t.Fatalf("switches %+v, want one to %+v", sw, c.want)
			}
			if win.Screen().Contains("Jump to an agent") {
				t.Error("the hub stayed open after enter")
			}
		})
	}
}

// --- the gallery ---

// galleryEntry is one entry of the gallery: an id and how to draw it in a tier.
type galleryEntry struct {
	id   string
	draw func(t *testing.T, tr tier, light bool) string
}

// windowEntry draws a lookScreen through the client; it keeps the rows from
// row from on (the status row of the status-* illustrations).
func windowEntry(sc lookScreen, from int) galleryEntry {
	return galleryEntry{id: sc.id, draw: func(t *testing.T, tr tier, light bool) string {
		win, _ := drawScreen(t, sc, tr, light)
		lines := strings.Split(win.View().Content, "\n")
		return strings.Join(lines[min(from, len(lines)):], "\n")
	}}
}

// standaloneEntry is a hub screen of the standalone host: the hub alone on
// a grid of cols by rows, with the features the backend has.
func standaloneEntry(id string, cols, rows int, f hub.Features, mode hub.Mode) galleryEntry {
	return galleryEntry{id: id, draw: func(t *testing.T, tr tier, light bool) string {
		th := ui.Theme{}
		if tr == truecolorTier {
			tokens := make([]ui.Token, 0, len(lookColors))
			for _, c := range lookColors {
				var l, d uint32
				fmt.Sscanf(c.Light, "#%06x", &l)
				fmt.Sscanf(c.Dark, "#%06x", &d)
				tokens = append(tokens, ui.Token{Name: c.Token, Light: l, Dark: d})
			}
			var err error
			th, err = ui.NewTheme(ui.ThemeOptions{Tokens: tokens, Dark: !light, Known: true, Colors: 1 << 24, UTF8: true})
			if err != nil {
				t.Fatal(err)
			}
		}
		w := example("done")
		events := concatEvents(w.local, w.mini)
		ch := make(chan hub.Event, len(events))
		for _, ev := range events {
			ch <- ev
		}
		close(ch)
		m := hub.New(hub.ModelOptions{Mode: mode, Here: session.Target{Dir: dtMainDir}, Events: ch,
			Actions: lookActions{}, Theme: th, Features: f})
		runHub(t, m, m.Init())
		m.Update(tea.WindowSizeMsg{Width: cols, Height: rows})
		return m.View(cols, rows)
	}}
}

// runHub runs the hub's commands synchronously until they end, as the
// standalone host's program loop would.
func runHub(t *testing.T, m *hub.Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			runHub(t, m, c)
		}
	default:
		next, _ := m.Update(msg)
		runHub(t, m, next)
	}
}

func galleryEntries() []galleryEntry {
	var out []galleryEntry
	// the eight screens with a golden
	for _, sc := range lookScreens {
		out = append(out, windowEntry(sc, 0))
	}
	// the seven illustrations (5a.17)
	out = append(out,
		windowEntry(lookScreen{"chrome-single-80x24", 80, 24, singleLocal(80, 24)}, 0),
		windowEntry(lookScreen{"chrome-small-40x10", 40, 10, func(t *testing.T, win *window) {
			win.show(win.w.well(), "", desktopState(onePane("zsh"), ""), "s1",
				paneFrame("p1", 40, 9, 2, 3, "~/dev/devtools/main main", gitLines[0], gitLines[1], gitLines[2]))
		}}, 0),
		windowEntry(lookScreen{"chrome-split-remote-200x50", 200, 50, func(t *testing.T, win *window) {
			win.show(win.w.well(), macmini, miniState(tab1{programs: []string{"nvim", "zsh"}, split: proto.SplitRight, focus: 1}, proto.StatusWorking), "m3",
				paneFrame("q3", 100, 49, 6, 0, "  1 # Notes", "  2", "  3 ## Status row", "  4"),
				paneFrame("q4", 99, 49, 2, 3, "~/notes main", "$ ls", "inbox.md  plan.md  reading.md", "$"))
		}}, 0),
		windowEntry(lookScreen{"status-standard-200", 200, 50, func(t *testing.T, win *window) {
			s := desktopState(onePane("zsh"), "nvim")
			s.Sessions[0].Current = "t2"
			win.show(win.w.well(), "", s, "s1", paneFrame("p3", 200, 49, 0, 0))
		}}, 49),
		windowEntry(lookScreen{"status-hints-200", 200, 50, func(t *testing.T, win *window) {
			singleLocal(200, 50)(t, win)
			win.open("?")
		}}, 49),
		windowEntry(lookScreen{"hub-all-200x50", 200, 50, func(t *testing.T, win *window) {
			singleLocal(200, 50)(t, win)
			win.Update(win.w.macbookConnecting())
			win.hub = win.w.hubAll()
			win.open("o")
		}}, 0),
		windowEntry(lookScreen{"hub-agents-200x50", 200, 50, func(t *testing.T, win *window) {
			singleLocal(200, 50)(t, win)
			win.Update(win.w.macbookConnecting())
			win.hub = win.w.hubAgents()
			win.open("a")
		}}, 0),
	)
	// what the backends tmux and herdr show: the hub of the standalone host
	out = append(out,
		standaloneEntry("hub-noagents-80x24", 80, 24, hub.Features{}, hub.ModeAll),
		standaloneEntry("hub-noagents-40x10", 40, 10, hub.Features{}, hub.ModeAll),
		standaloneEntry("hub-flat-80x24", 80, 24, hub.Features{Agents: true}, hub.ModeAll),
		standaloneEntry("hub-flat-agents-80x24", 80, 24, hub.Features{Agents: true}, hub.ModeAgents),
	)
	return out
}

// galleryMessages are the twelve messages of TestMessagesOfTheBackends
// (P4-00), with the same arguments.
var galleryMessages = []struct {
	id, kind string
	args     []string
}{
	{"message-noserver-tmux", "noserver", []string{"tmux"}},
	{"message-nested-tmux", "nested", []string{"tmux", desktop}},
	{"message-othermux-tmux", "othermux", []string{"tmux", "screen"}},
	{"message-unsupported-machines", "unsupported", []string{"tmux", "machines"}},
	{"message-unsupported-agent-status", "unsupported", []string{"tmux", "agent-status"}},
	{"message-unsupported-window", "unsupported", []string{"tmux", "window"}},
	{"message-unsupported-pane-focus", "unsupported", []string{"herdr", "pane-focus"}},
	{"message-unsupported-layout", "unsupported", []string{"tmux", "layout"}},
	{"message-unsupported-detached-open", "unsupported", []string{"herdr", "detached-open"}},
	{"message-mismatch", "mismatch", []string{"tmux", "devtools"}},
	{"message-unavailable", "unavailable", []string{"herdr"}},
	{"message-ambiguous", "ambiguous", []string{"tmux", "herdr"}},
}

// TestGallery prints every screen of 5a.9 for a person (K4): the 8 screens
// with a golden and the 7 illustrations through the real client, then the 4
// hub screens of the standalone host, each in the palette tier and in
// truecolor with its escape sequences, and the 12 messages of the backends
// without colour. It returns at once unless DEVTOOLS_GALLERY is dark or
// light; it never skips, because a skipped test fails a checkpoint (6.7).
//
//	DEVTOOLS_GALLERY=dark go test ./internal/integration -run TestGallery -v
func TestGallery(t *testing.T) {
	if galleryMode != "dark" && galleryMode != "light" {
		return
	}
	light := galleryMode == "light"
	out := os.Stdout
	entries := galleryEntries()
	for _, e := range entries {
		for _, tr := range []tier{paletteTier, truecolorTier} {
			body := e.draw(t, tr, light)
			fmt.Fprintf(out, "\n\x1b[0m== %s (%s, %s)\n%s\x1b[0m\n", e.id, tr.name, galleryMode, body)
		}
	}
	for _, m := range galleryMessages {
		fmt.Fprintf(out, "\n\x1b[0m== %s\n%s\n", m.id, strings.TrimSuffix(ui.Message(m.kind, m.args...), "\n"))
	}
	fmt.Fprintf(out, "\n%d screens, each in two tiers, and %d messages\n", len(entries), len(galleryMessages))
	if len(entries) != 19 || len(galleryMessages) != 12 {
		t.Errorf("the gallery has %d screens and %d messages, want 19 and 12", len(entries), len(galleryMessages))
	}
}
