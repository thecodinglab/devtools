package keys

// The prefix machine (5a.11.1): idle, or the prefix pending for one key.
// There is no timeout and no repeat mode; the hint strip after 600 ms is
// the client's clock, not the machine's.

import "devtools/internal/proto"

// maxConsumed bounds the presses the machine remembers. A terminal that
// never reports releases leaves its entries until the next press of the
// same key; the bound keeps a stream of distinct names from growing the set.
const maxConsumed = 32

// Machine is the prefix machine: idle or pending, and the presses it
// consumed, whose releases and repeats it consumes too.
type Machine struct {
	table    *Table
	pending  bool
	consumed map[string]bool // key names of presses that did not reach the pane
}

// NewMachine returns an idle machine over the table; nil is the defaults.
func NewMachine(t *Table) *Machine {
	if t == nil {
		t = Defaults()
	}
	return &Machine{table: t, consumed: map[string]bool{}}
}

// Reset ends a pending prefix. The consumed presses are kept, so a key
// held across the reset still has its release consumed.
func (m *Machine) Reset() { m.pending = false }

// Handle decides one key event. Releases and repeats of a consumed press
// are swallowed; other releases are forwarded, and other repeats too
// unless the prefix is pending, which holds them back. Idle: the prefix chord makes
// the prefix pending, a direct binding acts, unless it is a focus action
// with the argument vim and the pane says editor (F13), and everything
// else is forwarded. Pending: a modifier alone keeps it pending, esc
// cancels, ? reports More, the prefix chord sends the prefix, a bound key
// acts, and any other key ends the prefix as Unbound. Nothing typed while
// the prefix is pending reaches the pane.
func (m *Machine) Handle(e Event, c Context) Result {
	name := keyName(e)
	if e.Release {
		if m.consumed[name] {
			delete(m.consumed, name)
			return m.result(Swallow)
		}
		return m.result(Forward)
	}
	if e.Repeat {
		// a repeat is never a new key: of a consumed press it is consumed,
		// of a key held from before the prefix it is held back while the
		// prefix waits (its release still reaches the pane, which had the
		// press), and otherwise it is the pane's
		if m.consumed[name] || m.pending {
			return m.result(Swallow)
		}
		return m.result(Forward)
	} else {
		// a new press: whatever was remembered for this key is stale
		delete(m.consumed, name)
	}
	if !m.pending {
		return m.idle(e, c, name)
	}
	return m.afterPrefix(e, name)
}

func (m *Machine) idle(e Event, c Context, name string) Result {
	if m.table.isPrefix(e) {
		m.pending = true
		m.consume(name)
		return m.result(Swallow)
	}
	a, ok := m.table.lookup(e, false)
	if !ok {
		return m.result(Forward)
	}
	if focusActions[a.Name] && a.Arg == "vim" && c.Editor {
		return m.result(Forward)
	}
	m.consume(name)
	r := m.result(Act)
	r.Action = a
	return r
}

func (m *Machine) afterPrefix(e Event, name string) Result {
	m.consume(name)
	if modifierKeys[name] {
		return m.result(Swallow)
	}
	mod := e.Mod &^ lockMods
	if name == "escape" && mod == 0 {
		m.pending = false
		return m.result(Swallow)
	}
	if mod&^proto.ModShift == 0 && eventText(e) == "?" {
		r := m.result(Swallow)
		r.More = true
		return r
	}
	m.pending = false
	if m.table.isPrefix(e) {
		return m.result(SendPrefix)
	}
	if a, ok := m.table.lookup(e, true); ok {
		r := m.result(Act)
		r.Action = a
		return r
	}
	r := m.result(Swallow)
	r.Unbound = true
	return r
}

// consume remembers a press whose release and repeats are swallowed.
func (m *Machine) consume(name string) {
	if len(m.consumed) >= maxConsumed {
		clear(m.consumed)
	}
	m.consumed[name] = true
}

func (m *Machine) result(v Verdict) Result {
	return Result{Verdict: v, Pending: m.pending}
}
