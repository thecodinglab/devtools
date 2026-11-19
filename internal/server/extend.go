package server

import (
	"fmt"
	"sync"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/vt"
)

// The extension seam (3.2.3, 5.3.6). Cross-machine handlers and the agent
// tracker (cross.go, P2-10) register here from init. Everything here runs on
// the loop goroutine and sees the server only through Core and Conn.

// Conn is one connection as extensions see it.
type Conn interface {
	ID() string
	Kind() string // "interactive", "oneshot"
	Remote() bool
	Pane() string // hello.pane
	View() (session string, ok bool)
	LastInput() time.Time
	Focused() bool
	Has(cap string) bool // hello.caps contains cap (4.1)
	Send(m proto.Msg)    // control lane; a full queue closes the connection
}

// Core is the server as extensions see it.
type Core interface {
	State() proto.Snapshot
	Conns() []Conn
	Reply(c Conn, id uint64, result any)
	Fail(c Conn, id uint64, code, text string)
	PaneSession(pane string) (session string, ok bool)
	OpenSession(dir, label string) (session string, created bool, code string) // code "" on success, else an error code of 4.1
	CloseSession(session string) (closed bool)
	PutAgent(pane string, a proto.Agent) // goes into the state and the next delta
	DelAgent(pane string)
	After(d time.Duration, f func(Core)) // a timer on the loop
	Post(f func(Core))                   // from any goroutine
}

// Handler handles one message of a registered type, on the loop.
type Handler func(core Core, c Conn, m proto.Msg)

// Observer receives what the panes report.
type Observer interface {
	Title(core Core, pane, title string)
	Report(core Core, pane string, r vt.AgentReport)
	Foreground(core Core, pane string, group []platform.Proc) // at most once per ProcInterval per pane
	Seen(core Core, pane string)                              // the pane's tab is displayed by a connection with focus
	Gone(core Core, pane string)
}

var extensions struct {
	mu       sync.Mutex
	handlers map[string]Handler
	caps     []string
	observer Observer
}

// Extend registers a handler for a message type and, when cap is not empty,
// adds cap to welcome.caps. Call from init; a duplicate type panics, as do an
// empty type and a nil handler.
func Extend(msgType, cap string, h Handler) {
	if msgType == "" {
		panic("server: Extend with an empty message type")
	}
	if h == nil {
		panic(fmt.Sprintf("server: Extend(%q) with a nil handler", msgType))
	}
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	if extensions.handlers == nil {
		extensions.handlers = map[string]Handler{}
	}
	if _, dup := extensions.handlers[msgType]; dup {
		panic(fmt.Sprintf("server: message type %q registered twice", msgType))
	}
	extensions.handlers[msgType] = h
	if cap == "" {
		return
	}
	for _, c := range extensions.caps {
		if c == cap {
			return
		}
	}
	extensions.caps = append(extensions.caps, cap)
}

// Observe registers the one observer. Call from init; a second observer and
// a nil one panic.
func Observe(o Observer) {
	if o == nil {
		panic("server: Observe(nil)")
	}
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	if extensions.observer != nil {
		panic("server: a second observer")
	}
	extensions.observer = o
}

// extension returns the handler registered for a message type.
func extension(msgType string) (Handler, bool) {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	h, ok := extensions.handlers[msgType]
	return h, ok
}

// extensionCaps returns the capabilities the extensions add to welcome.caps,
// in the order they were first registered. The slice is a copy.
func extensionCaps() []string {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	return append([]string(nil), extensions.caps...)
}

// extensionObserver returns the observer, nil when none is registered.
func extensionObserver() Observer {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	return extensions.observer
}
