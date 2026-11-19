// Package pane is one pane: a child program on a PTY, parsed by a vt.Term,
// with its events (3.2.1, 5.3.3). The pane actor, one goroutine per pane, is
// the only caller of its terminal.
//
// This file is the contract (P1-00a). The type Pane, its methods, Start and
// Env are written by P1-12.
package pane

import (
	"time"

	"devtools/internal/vt"
)

// Options configure a pane.
type Options struct {
	ID       string
	Argv     []string // program and arguments; Argv[0] is the path
	Arg0     string   // overrides argv[0]: "-zsh" for a login shell
	Dir      string
	Env      []string // the environment from Env; Start adds DEVTOOLS_TTY (proto.EnvTTY) once it has opened the PTY
	Cols     int
	Rows     int
	History  int
	Colors   vt.Colors
	Notify   func(Event)                                      // called from the pane's goroutines; must return quickly
	OnReflow func(remap func(abs int64) (now int64, ok bool)) // vt.Options.OnReflow, called from the pane's goroutine during a resize; must return quickly. remap answers only for lines passed to Follow

	KillGrace time.Duration // 250 ms
	SyncHold  time.Duration // 150 ms
}

// Snap is what a viewer shows: the live screen, or rows from Top on when
// scrolled.
type Snap struct {
	Cols, Rows  int
	Lines       []string // wire rows
	Cursor      vt.Cursor
	Modes       vt.Modes
	Top         int64 // effective first line when scrolled
	Scrolled    bool
	First, Base int64
}

// Info describes a pane; Pane.Info returns a copy.
type Info struct {
	State string // "running", "exited"
	Title string
	Cwd   string
	Pid   int    // the child
	Shell int    // process group of the child
	Fg    int    // foreground process group of the PTY
	TTY   string // slave device name
	Exit  *Exit
}

// Exit is how the program of a pane ended.
type Exit struct {
	Code     int
	Signal   string // "" or the signal name
	ByServer bool   // Close caused it
	Text     string // spawn error
}

// EventKind is the kind of an Event.
type EventKind int

const (
	InfoChanged EventKind = iota // title, cwd, foreground group
	Exited
	Bell
	Clipboard
	AgentReport
)

// Event is what a pane reports through Options.Notify.
type Event struct {
	Pane      string
	Kind      EventKind
	Clipboard []byte
	Agent     vt.AgentReport
}
