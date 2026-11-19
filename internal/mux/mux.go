// Package mux describes a terminal multiplexer to the commands and
// implements none (R14, 5.3.10 of the plan): the interface Multiplexer, the
// capabilities, the detection of the context a command runs in, the
// selection of the backend and the feature table.
//
// mux.go and caps.go are contracts (6.2, P2-12) and complete. The types of
// context.go, select.go and gate.go are contracts too; their function
// bodies and the rows of Features are written by P2-13. The package imports
// the standard library, internal/hub, internal/proto, internal/session and
// internal/client/guard, and nothing else (rule 12); only legacy.go imports
// internal/herdr, until P2-08a deletes it.
package mux

import (
	"context"
	"fmt"

	"devtools/internal/client/guard"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
)

type Backend string

const (
	Devtools Backend = "devtools"
	Tmux     Backend = "tmux"
	Herdr    Backend = "herdr"
)

// Parse accepts exactly the three names.
func Parse(s string) (Backend, error) {
	switch b := Backend(s); b {
	case Devtools, Tmux, Herdr:
		return b, nil
	}
	return "", fmt.Errorf("unknown multiplexer %q, want devtools, tmux or herdr", s)
}

type HubRequest struct {
	Mode  hub.Mode
	Query string
}

// Entry says what a new window shows first.
type Entry struct {
	Target *session.Target // nil: the backend's last or default session
	Hub    *HubRequest     // nil: no hub
}

type Shown int

const (
	ShownInWindow Shown = iota // the window that shows the caller's pane shows it now
	OpenedOnly                 // the session exists; no window was switched
)

type Multiplexer interface {
	Name() Backend
	Caps() Caps

	// Every backend.

	// Show makes the session exist and, when a window shows the caller's
	// pane, switches that window to it. It never starts a client. Without
	// a window it needs CapDetached.
	Show(dir, name string) (Shown, error)
	// Leave moves the caller's window off the session of dir and returns
	// the function that ends that session.
	Leave(dir, fallbackDir, fallbackName string) (closeLast func() error, err error)
	// Hub opens the hub in the caller's window and acts on the pick.
	// Only valid inside this multiplexer.
	Hub(mode hub.Mode, query string) error
	// Sessions lists the sessions of this machine.
	Sessions(ctx context.Context) ([]hub.Item, error)
	// Enter turns this terminal into a window of the multiplexer and
	// returns when the window is detached or closed. It is the only
	// method that starts a client. A nil or invalid Pass is ErrNoPass
	// and starts nothing.
	Enter(p *guard.Pass, e Entry) error

	// Gated. Without the capability each returns *ErrUnsupported and
	// does nothing else.

	Agents(ctx context.Context) ([]hub.Item, error)       // CapAgents
	Jump(agent hub.Item) error                            // CapAgents
	Snapshot(ctx context.Context) (proto.Snapshot, error) // CapLayout
	FocusPane(direction string) (moved bool, err error)   // CapPaneFocus
}

var ErrNoPass = guard.ErrNoPass
