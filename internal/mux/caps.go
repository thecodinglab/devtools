package mux

// Capabilities (5.3.10 of the plan). A capability is a tested statement
// about what a backend does; the set of each backend is the static table
// backend.CapsOf (5.3.11). This file is a contract (6.2, P2-12).

import "fmt"

type Capability string

const (
	CapMachines  Capability = "machines"
	CapAgents    Capability = "agent-status"
	CapWindow    Capability = "window"
	CapPaneFocus Capability = "pane-focus"
	CapLayout    Capability = "layout"
	CapDetached  Capability = "detached-open"
)

// all is the order of All; the index of a capability is its bit in Caps.
var all = [...]Capability{CapMachines, CapAgents, CapWindow, CapPaneFocus, CapLayout, CapDetached}

// All returns the six capabilities in the order above.
func All() []Capability {
	out := make([]Capability, len(all))
	copy(out, all[:])
	return out
}

// Caps is a value: comparable, immutable, safe to copy.
type Caps struct{ bits uint16 }

// bit is the bit of x in Caps, or 0 for a name that is not one of the six.
func bit(x Capability) uint16 {
	for i, c := range all {
		if c == x {
			return 1 << i
		}
	}
	return 0
}

// CapsOf returns the set of the given capabilities. A name that is not one
// of the six adds nothing.
func CapsOf(c ...Capability) Caps {
	var s Caps
	for _, x := range c {
		s.bits |= bit(x)
	}
	return s
}

func (c Caps) Has(x Capability) bool {
	b := bit(x)
	return b != 0 && c.bits&b != 0
}

// List returns the members in the order of All.
func (c Caps) List() []Capability {
	var out []Capability
	for i, x := range all {
		if c.bits&(1<<i) != 0 {
			out = append(out, x)
		}
	}
	return out
}

// ErrUnsupported is the only way a backend says "I cannot do this".
type ErrUnsupported struct {
	Backend    Backend
	Capability Capability
}

// Error is a plain Go error text for logs and tests; what a person reads
// is ui.Message("unsupported", ...) (5.3.9).
func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("multiplexer %s does not support %s", e.Backend, e.Capability)
}

// Need returns nil or *ErrUnsupported. It is how callers test.
func Need(m Multiplexer, c Capability) error {
	if m.Caps().Has(c) {
		return nil
	}
	return &ErrUnsupported{Backend: m.Name(), Capability: c}
}
