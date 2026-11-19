package client

// The typeahead gate (3.2.4): after a request that can change which pane
// has the focus, keys and paste wait until the reply arrived and the state
// holds its result (W2), at most gateLimit events and at most gateWait,
// and are then routed again, so prefix+c followed at once by typing reaches
// the new tab's pane.

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	gateLimit = 256
	gateWait  = 2 * time.Second
)

// gated are the requests that can change which pane has the focus.
var gated = map[string]bool{
	"tab.new": true, "tab.select": true, "tab.close": true,
	"pane.split": true, "pane.focus": true, "pane.close": true,
}

// gate is the queue of the typeahead gate.
type gate struct {
	open    bool // events wait
	replied bool // the reply arrived; the gate waits for the state that follows it
	gen     int
	queue   []tea.Msg
}

// drop empties the gate without routing what waited.
func (g *gate) drop() {
	g.open, g.replied = false, false
	g.queue = nil
	g.gen++
}

// arm opens the gate for a request that was just sent.
func (m *Model) arm() tea.Cmd {
	m.gate.gen++
	m.gate.open, m.gate.replied = true, false
	return m.hk.After(gateWait, gateDue{gen: m.gate.gen})
}

// queued reports whether a key or paste event waits behind the gate, and
// queues it when it does. An event beyond gateLimit is dropped.
func (m *Model) queued(msg tea.Msg) bool {
	if !m.gate.open || m.overlay != nil {
		return false
	}
	if len(m.gate.queue) < gateLimit {
		m.gate.queue = append(m.gate.queue, msg)
	}
	return true
}

// gateReplied is the reply of the gated request: the gate now waits for
// the state read that follows it.
func (m *Model) gateReplied() tea.Cmd {
	if !m.gate.open {
		return nil
	}
	m.gate.replied = true
	if m.view != "showing" {
		return m.release()
	}
	return m.refreshState()
}

// gateRead is a state read: a gate whose request was answered opens.
func (m *Model) gateRead() tea.Cmd {
	if m.gate.open && m.gate.replied && !m.refresh {
		return m.release()
	}
	return nil
}

// gateTimeout: the gate gave up waiting (2 s).
func (m *Model) gateTimeout(gen int) tea.Cmd {
	if !m.gate.open || gen != m.gate.gen {
		return nil
	}
	return m.release()
}

// release routes what waited, in order, as if it had been typed now. A
// key among them that sends a gated request opens the gate again, and the
// rest waits once more.
func (m *Model) release() tea.Cmd {
	queue := m.gate.queue
	m.gate.open, m.gate.replied = false, false
	m.gate.queue = nil
	m.gate.gen++
	var cmds []tea.Cmd
	for i, msg := range queue {
		if m.gate.open {
			m.gate.queue = append(m.gate.queue, queue[i:]...)
			break
		}
		cmds = append(cmds, m.input(msg))
	}
	return tea.Batch(cmds...)
}
