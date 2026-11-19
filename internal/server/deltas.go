package server

import (
	"encoding/json"

	"devtools/internal/proto"
)

// State deltas (4.2.2). Every interactive connection receives every delta,
// so one revision counter serves them all: a connection's snapshot carries
// the revision of the last delta queued before it, and each later delta is
// one more (W3).
//
// Structural changes are sent at once. Cosmetic ones (title, cwd, program,
// shown, agent records) caused by a pane or an extension are collected and
// sent at most once per CosmeticDelay; a structural delta sends them first,
// in a delta of their own. Changes caused by a request are always sent at
// once, so that the reply follows its delta (W2).

// deltas is the outgoing side of the state, owned by the loop.
type deltas struct {
	rev     uint64         // the revision of the last delta queued
	pending []proto.Change // cosmetic changes not yet sent, one per object
	at      map[string]int // kind and id -> index in pending
	armed   bool           // a flush of pending is scheduled
}

// publish sends changes: at once when now is true or any of them is
// structural, otherwise after CosmeticDelay with the other cosmetic ones.
// It classifies against c.known before it applies the changes there.
func (c *core) publish(changes []proto.Change, now bool) {
	cosmetic := !now
	for _, ch := range changes {
		if cosmetic && !c.isCosmetic(ch) {
			cosmetic = false
		}
	}
	if err := c.known.Apply(proto.Delta{Rev: c.known.Rev + 1, Changes: changes}); err != nil {
		// The model produced a change the wire cannot carry: a bug.
		panic("server: the model's changes do not apply: " + err.Error())
	}
	if cosmetic {
		c.hold(changes)
		return
	}
	c.flushCosmetic()
	c.broadcast(changes)
}

// isCosmetic reports whether a change touches only cosmetic fields of an
// object the connections already know (4.2.2): an agent record, or a put of
// a session or a pane that differs from what c.known holds only in shown,
// or in title, cwd and program.
func (c *core) isCosmetic(ch proto.Change) bool {
	if ch.Kind == proto.KindAgent {
		return true
	}
	if ch.Op != proto.OpPut {
		return false
	}
	switch ch.Kind {
	case proto.KindSession:
		old, ok := c.known.Sessions[ch.ID]
		var v proto.Session
		if !ok || json.Unmarshal(ch.Value, &v) != nil {
			return false
		}
		v.Shown, old.Shown = 0, 0
		return equalSession(v, old)
	case proto.KindPane:
		old, ok := c.known.Panes[ch.ID]
		var v proto.Pane
		if !ok || json.Unmarshal(ch.Value, &v) != nil {
			return false
		}
		v.Title, v.Cwd, v.Program = "", "", ""
		old.Title, old.Cwd, old.Program = "", "", ""
		return equalPane(v, old)
	}
	return false
}

func equalSession(a, b proto.Session) bool {
	if a.ID != b.ID || a.Dir != b.Dir || a.Label != b.Label || a.Current != b.Current || a.Shown != b.Shown || len(a.Tabs) != len(b.Tabs) {
		return false
	}
	for i := range a.Tabs {
		if a.Tabs[i] != b.Tabs[i] {
			return false
		}
	}
	return true
}

func equalPane(a, b proto.Pane) bool {
	if (a.Exit == nil) != (b.Exit == nil) || (a.Exit != nil && *a.Exit != *b.Exit) {
		return false
	}
	a.Exit, b.Exit = nil, nil
	return a == b
}

// hold adds cosmetic changes to the pending ones, a later change of an
// object replacing the earlier, and schedules their flush.
func (c *core) hold(changes []proto.Change) {
	d := &c.out
	if d.at == nil {
		d.at = map[string]int{}
	}
	for _, ch := range changes {
		key := ch.Kind + " " + ch.ID
		if i, ok := d.at[key]; ok {
			d.pending[i] = ch
			continue
		}
		d.at[key] = len(d.pending)
		d.pending = append(d.pending, ch)
	}
	if !d.armed {
		d.armed = true
		c.after(c.s.o.CosmeticDelay, func(c *core) {
			c.out.armed = false
			c.flushCosmetic()
		})
	}
}

// flushCosmetic sends the pending cosmetic changes, if any, as one delta.
func (c *core) flushCosmetic() {
	d := &c.out
	if len(d.pending) == 0 {
		return
	}
	changes := d.pending
	d.pending, d.at = nil, nil
	c.broadcast(changes)
}

// broadcast queues one delta with the next revision on every interactive
// connection. For a connection whose displayed set the delta changes (the
// current tab of its session, or the panes of that tab), the delta and the
// new set are queued in one step (lane.Show), so no frame of a pane the
// delta takes off the screen follows it and every pane of the new set
// starts again at seq 1 (4.2.3, W4).
func (c *core) broadcast(changes []proto.Change) {
	c.out.rev++
	m := proto.Encode(proto.Delta{Rev: c.out.rev, Changes: changes})
	for _, cn := range c.reg.list() {
		if cn.kind != proto.KindInteractive {
			continue
		}
		if c.stale(cn) {
			c.show(cn, &m)
			continue
		}
		c.send(cn, m)
	}
}

// snapshot is the state.snapshot of this moment: the whole model at the
// revision of the last delta queued. Cosmetic changes still pending are in
// it already; their delta, when it comes, puts the same values again.
func (c *core) snapshot() proto.Snapshot {
	return c.State()
}
