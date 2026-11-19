// Package client is the program that shows a session in the outer terminal
// (3.2.4, 5.3.7 of the plan).
//
// This file is a contract (6.2, P1-00b) and is read-only for P1-17a and
// P1-17b. Run is in stub.go until P1-17a replaces it with client.go.
package client

import (
	"errors"
	"os"

	"devtools/internal/client/chrome"
	"devtools/internal/client/guard"
	"devtools/internal/client/keys"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// Fleet is what the client needs from internal/fleet; *fleet.Fleet satisfies it.
type Fleet interface {
	Updates() <-chan fleet.Update
	Switch(t session.Target)
	Cancel()
	Input(m proto.Msg)
	Request(m proto.Msg) <-chan proto.Msg
	Resize(cols, rows int)
	Focus(focused bool)
	Colors(m proto.Msg) // a view.colors message
	HubOpened() <-chan hub.Event
	HubClosed()
	Step(delta int)
	// Drop: a frame of the displayed target t (the Target of its Shown) was
	// unreadable (3.2.4, 4.2.5). The fleet ends the link of t's machine when
	// t is still the committed view and not stale; the view turns stale and
	// the link is supervised as after any loss (3.5.2, 3.5.5). Contract
	// change 14.
	Drop(t session.Target)
}

// Options configure Run.
type Options struct {
	Pass    *guard.Pass // from guard.Check; Run starts nothing without a valid one
	Fleet   Fleet
	Initial *session.Target   // what to show first; nil with Hub set: start in the hub
	Hub     *fleet.HubRequest // open the hub at start
	OpenHub func(req fleet.HubRequest, events <-chan hub.Event, theme ui.Theme) chrome.Overlay
	Keys    *keys.Table    // nil: defaults
	Chrome  chrome.Chrome  // nil: chrome.Plain
	Colors  []config.Color // the `color` lines
	Version string
	In, Out *os.File // nil: os.Stdin, os.Stdout
	Env     []string // nil: os.Environ()
}

var ErrNotTerminal = errors.New("devtools: not a terminal")
var ErrNoServer = errors.New("devtools: the server is not running")
