// Package chrome holds the interfaces between the core and the look: what
// the chrome may show (State), how it is drawn (Chrome) and the one thing
// that can cover the panes (Overlay). chrome.Plain (P1-07) is an ASCII
// implementation kept as the reference for tests; chrome.NewLook (P4-00,
// P4-02, P4-03) is what the user sees.
//
// This file is a contract (6.2, P1-00b), in the reconciled form of 5.3.5 of
// the plan, and is read-only for every other package. LookOptions and
// NewLook are in look.go (P4-00 commit b).
package chrome

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/screen"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// State is everything the chrome may show. It is per client.
type State struct {
	Width, Height int
	Theme         ui.Theme // colours and glyphs; rebuilt when the terminal's background answer changes
	WindowFocused bool
	Mode          string // what the keys do: "normal", "prefix", "copy", "scrolled", "confirm", "switching"
	View          string // what is displayed: "showing", "stale", "none". Independent of Mode: a prefix can be pending and a switch can run in a stale view
	Hints         bool   // "?" was pressed after the prefix
	HintPage      int    // how often "?" was pressed in this prefix; the look draws page HintPage modulo the number of pages
	Session       string // label, sanitised
	Machine       string // "" local
	Tabs          []Tab
	Summary       Summary   // roll-up and machine states, copied by the client from fleet.SummaryData
	Notice        string    // one line, sanitised; cleared by the client after ui.NoticeLife
	Copy          *Copy     // set in "copy" and "scrolled"
	Confirm       *Confirm  // set in "confirm"
	Target        Target    // the pending switch, while Mode is "switching"
	Now           time.Time // truncated to the minute
}

// Tab is one tab of the displayed session.
type Tab struct {
	ID      string
	Index   int
	Title   string // the agent kind when the tab's focused pane runs a detected agent, else the foreground program's name; never a title set by a program
	Current bool
	Bell    bool
	Agent   string // most urgent agent status in the tab, "" none
}

// Summary mirrors fleet.SummaryData with plain strings (rule 7 of 5.2).
type Summary struct {
	Top      string         // most urgent agent status over all online machines, "" none
	Counts   map[string]int // agent status -> number of panes
	Machines []Machine      // Machines[0] is this machine, then config order
	Urgent   Target         // the most urgent agent that is blocked or done and not on screen here: its machine and session as displayed; zero when none
}

// Machine is one machine of the summary.
type Machine struct {
	Name, Host string
	State      string // "off", "connecting", "online", "retrying", "attention"
	Problem    string // the reason value of 3.5.2; "" when none
	Retry      string // "", "retrying", "next-open", "in-30s"
	Reason     string // one sanitised line, "" none
	Agents     map[string]int
	Busy       int // agents that were not idle in the last snapshot; kept while State is "off" (the `{m} off` entry of 5a.6.2)
}

// Copy is the position of copy mode or of the scrolled view.
type Copy struct {
	Line, Total int64  // position in history for the indicator
	Selecting   string // "", "char", "line", "block"
}

// Confirm is the pending question of pane-close.
type Confirm struct{ Program string } // "nvim"

// Target is a machine and a session as displayed.
type Target struct{ Machine, Session string } // names as displayed

// Chrome draws everything around the panes.
//
// Status draws from Mode. Banner, Inactive, Pad and the pointer of Divider
// read View and WindowFocused, never Mode, so a stale view keeps its banner
// and its inactive cells while a prefix is pending or a switch runs. When
// several conditions hold the client sets Mode by this precedence: confirm,
// copy, prefix, switching, scrolled, normal. "Too small" is not a mode: the
// client calls TooSmall instead of everything else.
type Chrome interface {
	MinSize() (cols, rows int)        // 20, 6
	StatusRows() int                  // rows reserved at the bottom; 1
	Status(c *screen.Canvas, s State) // c is the status area
	Divider(c *screen.Canvas, s State, vertical bool, p Pointer)
	Banner(c *screen.Canvas, s State) (rows int)        // c is the pane area; 0 or 1; it covers, it never resizes a pane
	Pad(c *screen.Canvas, s State)                      // the area a clipped or smaller view leaves empty
	PaneMessage(c *screen.Canvas, s State, text string) // dormant or exited pane
	TooSmall(c *screen.Canvas, s State)
	Selection(s State, st screen.Style) screen.Style // how a selected cell looks
	CopyCursor(s State, st screen.Style) screen.Style
	Inactive(s State, st screen.Style) screen.Style // a cell of a stale pane
}

// Overlay is the one thing that can cover the panes: the hub.
type Overlay interface {
	Init() tea.Cmd
	// Update gets key, mouse (overlay coordinates), paste, tea.WindowSizeMsg
	// (the overlay's size) and ui.ThemeMsg, and its own messages.
	// A non-nil Outcome closes the overlay.
	Update(msg tea.Msg) (tea.Cmd, *Outcome)
	Bounds(w, h int) screen.Rect // where it wants to be inside the pane area; the hub returns all of it
	Draw(c *screen.Canvas)       // c is exactly Bounds
	Cursor() (x, y int, visible bool)
}

// Outcome is how an overlay closed.
type Outcome struct {
	Switch *session.Target // nil: closed without a choice
}

// Pointer is the pointer of a divider: towards the pane with the keys, when
// that pane touches the divider. Toward is PointNone, PointBefore (the pane
// left of or above the divider) or PointAfter (right of it or below it);
// At is the pointer's cell along the divider, from 0.
type Pointer struct{ Toward, At int }

// The values of Pointer.Toward.
const (
	PointNone   = 0
	PointBefore = -1
	PointAfter  = 1
)
