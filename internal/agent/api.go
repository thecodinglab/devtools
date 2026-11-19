// Package agent recognises the coding agents running in panes and follows
// their status (3.6 of the plan). It imports the standard library and
// internal/session only (import rule 6).
//
// This file is the contract (P2-00) and is read-only for the package that
// implements it (P2-05): Tracker, Set, Rank and Rollup are written there.
package agent

import "time"

// Kind is what agent a pane runs; "" is not an agent.
type Kind string

const (
	Claude Kind = "claude"
	Codex  Kind = "codex"
)

// Status is what an agent is doing.
type Status string

const (
	Idle    Status = "idle"
	Working Status = "working"
	Blocked Status = "blocked"
	Done    Status = "done"
	Unknown Status = "unknown"
)

// Proc is one process of a pane's foreground group.
type Proc struct {
	Name string
	Args []string
}

// Report is one OSC 7501 report, parsed by the engine and handed over as
// vt.AgentReport. State may also be "error", which the table of 3.6 maps to
// idle.
type Report struct{ State, Kind string }

// State is what is known of the agent of one pane.
type State struct {
	Kind   Kind
	Status Status
	Since  time.Time // the last change of Status
	Title  string
}

// Change is one change of the agent of a pane, as Set reports it.
type Change struct {
	Pane   string
	State  State // Kind "" means the pane is no longer an agent
	Notify bool  // a transition into blocked or done: emit event.agent
}
