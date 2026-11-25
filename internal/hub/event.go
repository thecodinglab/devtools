package hub

// The contract of the hub (P2-00, 5.3.8 of the plan): what feeds it, what
// it may ask for, which of its parts exist, and its options. The model, its
// two hosts and the Item it lists are written by P2-07; this file is
// read-only for it.

import (
	"devtools/internal/session"
	"devtools/internal/ui"
)

// ModeAgents follows ModeNew in the order of 5.3.8. The wire names of the
// modes are all, open, new and agents (4.2.10).
const ModeAgents Mode = ModeNew + 1

// KindAgent, the kind of Item of 5.3.8 that hub.go does not declare, is a
// row for one agent pane; it follows KindSession (contract change 18).
const KindAgent Kind = KindSession + 1

// EventKind says what an Event reports.
type EventKind int

const (
	Connecting EventKind = iota // a machine is being dialled
	Online                      // it answered
	Items                       // catalog rows; replace rows with the same (Machine, Path)
	Git                         // git state of one path
	Complete                    // the catalog scan ended; rows not reported in it go
	Sessions                    // the full list of open sessions of the machine
	Agents                      // the full list of agent panes of the machine
	Failed                      // the machine is not usable; Err says why; its rows turn stale
	Closed                      // an on-demand machine was disconnected; state "off"
	// Note is a secondary line under the machine's heading; it changes
	// neither the state nor the rows. Err.Reason is "catalog" (a
	// catalog.error: Path and Err.Text say which path and why), "cut" (a
	// list reached its bound, 2,000 items, 500 sessions or 1,000 panes;
	// Err.Text is the bound) or "incomplete" (the scan did not finish).
	Note
)

// Problem is why a machine is not usable.
type Problem struct {
	Reason string // a remote.Reason value, or "lost"
	Text   string
	Retry  string // "", "retrying", "next-open", "in-30s", "login"
}

// Event is one thing the hub learns about one machine.
type Event struct {
	Machine string // "" is this machine
	Kind    EventKind
	Items   []Item
	Path    string
	Git     ui.Git
	Err     Problem
}

// Actions is the local-only work the hub may ask for.
type Actions interface {
	Create(req CreateRequest) (Item, error)
	Merge(item Item) (Item, error)
}

// Features says which parts of the hub exist. The host fills it from the
// capability set of the backend; the hub never asks for a backend.
type Features struct {
	Agents   bool // scope agents and agent rows exist
	Machines bool // machine headings and rows of other machines exist
}

// ModelOptions are the options of the hub's model (New, P2-07) and of
// RunStandalone.
type ModelOptions struct {
	Mode     Mode
	Query    string
	Here     session.Target // machine and directory of the asking pane
	Events   <-chan Event
	Actions  Actions
	Theme    ui.Theme
	Features Features
	// Name is this machine's name, the one the status row shows
	// (chrome.Summary.Machines[0].Name, C9). The machine column of the
	// agents scope and this machine's heading draw it. "" when the host
	// does not know it; without Features.Machines nothing draws it.
	Name string
	// Tokens are the color lines of the config. RunStandalone builds its
	// theme from them with ui.NewTheme (5a.16.1) and fails on an unknown
	// token; New ignores them, the overlay host passes Theme.
	Tokens []ui.Token
}
