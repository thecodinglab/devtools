// Package fleet is the client's model of all machines: which are connected,
// what they report, what the window shows and what the hub and the status
// row get (3.5, 3.6.3 of the plan).
//
// This file is the contract (P2-00) and is read-only for the packages that
// implement it (P2-03, P2-06): Fleet, New and its methods are written
// there.
package fleet

import (
	"time"

	"devtools/internal/agent"
	"devtools/internal/config"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// Options configure New.
type Options struct {
	Machines   []config.Machine
	Dialer     remote.Dialer
	Clock      Clock // real time in production
	Cols, Rows int   // pane area
}

// Clock is the time the fleet runs on: real time in production, a fake one
// in tests (fleettest, P2-03).
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed, unless the
	// timer is stopped first.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a timer of a Clock.
type Timer interface {
	// Stop prevents the call; false when it already ran or was stopped.
	Stop() bool
}

// UpdateKind says what an Update carries.
type UpdateKind int

const (
	Shown     UpdateKind = iota // a switch committed; Target and View are set; frames follow
	Frame                       // a frame, event.reflow, or the state of the displayed target (state.snapshot, contract change 14)
	Summary                     // roll-up, machine states, mode
	Stale                       // the displayed machine was lost; Target and Problem are set
	Notice                      // one sanitised line for the status row
	Bell                        // ring the outer terminal once
	OpenHub                     // a routed ui.hub arrived, or nothing is left to show
	Clipboard                   // event.clipboard of the displayed target
)

// Update is one thing the client renders.
type Update struct {
	Kind    UpdateKind
	Target  session.Target
	View    proto.ViewOK
	Frame   proto.Msg
	Summary SummaryData
	Problem hub.Problem
	Notice  string
	Hub     HubRequest
	Data    []byte
}

// HubRequest is the hub a routed ui.hub asks for.
type HubRequest struct {
	Mode  hub.Mode
	Query string
	Here  session.Target
}

// SummaryData is what the status row shows of all machines.
type SummaryData struct {
	Mode     string         // "showing", "switching", "stale", "none"
	Target   session.Target // what is displayed or being switched to
	Top      agent.Status   // roll-up over all online machines
	Counts   map[agent.Status]int
	Machines []MachineState // local first, then config order
	Stale    bool           // the displayed view is stale; stays true while Mode is "switching" away from it
	Urgent   session.Target // the most urgent blocked or done agent of an online machine: machine, session, tab, pane; zero when none
}

// MachineState is the state of one machine.
type MachineState struct {
	Name    string // config alias; "" local
	Host    string // welcome.host, sanitised, secondary
	State   string // "off", "connecting", "online", "retrying", "attention"
	Problem hub.Problem
	Agents  map[agent.Status]int
	Busy    int // agents that were not idle in the last snapshot; kept while State is "off"
}
