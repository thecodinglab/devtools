package fleet

import (
	"sync"

	"devtools/internal/hub"
)

// The way of the hub's events from the loop to the hub (3.5.3). Like pipe,
// it never blocks the loop; unlike pipe, what waits in it is bounded per
// machine, however fast a machine reports and however slowly the hub reads.
//
// Two things bound it. First, an event that only restates something waits
// once: a newer Sessions, Agents, or Git of the same path takes the place of
// the one waiting, and the rows of an Items join the rows of the machine's
// Items that waits, replacing a row with the same path and kind, as long as
// neither an Online nor a Complete of that machine waits after it (the two
// events that end what a scan reported). The hub ends in the same state as
// if it had read every event: each of these events sets a value that no
// other event reads. Second, a machine whose waiting events exceed
// hubBacklog rows is held back: its further events are dropped, the other
// machines go on, and once everything of it that waited has left, the loop
// sends the hub the machine's whole state again (Fleet.hubResync).

// hubBacklog is how much of one machine may wait for the hub: events plus
// the rows they carry. A full catalog, a full session and agent list and
// their git states fit several times.
const hubBacklog = 16384

// hubEntry is one waiting event.
type hubEntry struct {
	ev hub.Event
	w  int // 1 + rows
	// rows is the index of every row of an Items in ev.Items, while it may
	// take the rows of a later Items
	rows map[catKey]int
}

// hubKey names the waiting event that a new one takes the place of.
type hubKey struct {
	machine string
	kind    hub.EventKind
	path    string
}

type hubQueue struct {
	out chan hub.Event
	// caughtUp is called, from the queue's goroutine, when a machine that
	// is held back has nothing waiting anymore
	caughtUp func(machine string)

	mu     sync.Mutex
	q      []*hubEntry
	closed bool
	weight map[string]int // per machine, what waits
	held   map[string]bool
	latest map[hubKey]*hubEntry
	wake   chan struct{}
	done   chan struct{}
}

func newHubQueue(capacity int, caughtUp func(machine string)) *hubQueue {
	p := &hubQueue{
		out:      make(chan hub.Event, capacity),
		caughtUp: caughtUp,
		weight:   map[string]int{},
		held:     map[string]bool{},
		latest:   map[hubKey]*hubEntry{},
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

// keyOf is the key under which ev takes the place of a waiting event, and
// whether it has one.
func keyOf(ev hub.Event) (hubKey, bool) {
	switch ev.Kind {
	case hub.Sessions, hub.Agents, hub.Items:
		return hubKey{ev.Machine, ev.Kind, ""}, true
	case hub.Git:
		return hubKey{ev.Machine, ev.Kind, ev.Path}, true
	}
	return hubKey{}, false
}

// newRows is how many rows of ev the Items e does not have yet.
func (e *hubEntry) newRows(ev hub.Event) int {
	n := 0
	var seen map[catKey]bool
	for _, it := range ev.Items {
		k := catKey{it.Path, it.Kind}
		if _, ok := e.rows[k]; ok || seen[k] {
			continue
		}
		if seen == nil {
			seen = map[catKey]bool{}
		}
		seen[k] = true
		n++
	}
	return n
}

// addRows puts the rows of items into the Items e: a row with the path and
// kind of one there replaces it in its place, the others follow, in order.
func (e *hubEntry) addRows(items []hub.Item) {
	for _, it := range items {
		k := catKey{it.Path, it.Kind}
		if i, ok := e.rows[k]; ok {
			e.ev.Items[i] = it
			continue
		}
		e.rows[k] = len(e.ev.Items)
		e.ev.Items = append(e.ev.Items, it)
	}
	e.w = 1 + len(e.ev.Items)
}

// put queues ev for the hub. It never blocks.
func (p *hubQueue) put(ev hub.Event) {
	p.mu.Lock()
	queued := p.add(ev)
	p.mu.Unlock()
	if queued {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// add is put under p.mu; it reports whether a new entry waits.
func (p *hubQueue) add(ev hub.Event) bool {
	m := ev.Machine
	if p.closed || p.held[m] {
		return false
	}
	key, keyed := keyOf(ev)
	if e := p.latest[key]; keyed && e != nil {
		grow := 0
		if ev.Kind == hub.Items {
			grow = e.newRows(ev)
		} else {
			grow = 1 + len(ev.Items) - e.w
		}
		if p.weight[m]+grow > hubBacklog {
			p.held[m] = true
			return false
		}
		if ev.Kind == hub.Items {
			e.addRows(ev.Items)
		} else {
			e.ev, e.w = ev, 1+len(ev.Items)
		}
		p.weight[m] += grow
		return false
	}
	e := &hubEntry{ev: ev, w: 1 + len(ev.Items)}
	if ev.Kind == hub.Items {
		// the queue owns the rows it may add to
		e.ev.Items, e.rows = make([]hub.Item, 0, len(ev.Items)), map[catKey]int{}
		e.addRows(ev.Items)
	}
	// one event always waits, so that a machine that is held back has
	// something whose leaving ends it
	if p.weight[m] > 0 && p.weight[m]+e.w > hubBacklog {
		p.held[m] = true
		return false
	}
	switch ev.Kind {
	case hub.Online, hub.Complete:
		// what follows belongs to another scan of the hub's
		delete(p.latest, hubKey{m, hub.Items, ""})
	}
	if keyed {
		p.latest[key] = e
	}
	p.q = append(p.q, e)
	p.weight[m] += e.w
	return true
}

// resume lets the events of machine in again, after it was held back.
func (p *hubQueue) resume(machine string) {
	p.mu.Lock()
	delete(p.held, machine)
	p.mu.Unlock()
}

// waiting is what of machine waits: events plus their rows.
func (p *hubQueue) waiting(machine string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.weight[machine]
}

// close stops the queue; what still waits is dropped and out is closed.
func (p *hubQueue) close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	p.mu.Unlock()
}

func (p *hubQueue) run() {
	defer close(p.out)
	for {
		p.mu.Lock()
		if len(p.q) == 0 {
			p.mu.Unlock()
			select {
			case <-p.wake:
				continue
			case <-p.done:
				return
			}
		}
		e := p.q[0]
		p.q[0] = nil
		p.q = p.q[1:]
		if key, ok := keyOf(e.ev); ok && p.latest[key] == e {
			delete(p.latest, key)
		}
		e.rows = nil
		m := e.ev.Machine
		p.weight[m] -= e.w
		caught := false
		if p.weight[m] <= 0 {
			delete(p.weight, m)
			caught = p.held[m]
		}
		ev := e.ev
		p.mu.Unlock()
		if caught && p.caughtUp != nil {
			p.caughtUp(m)
		}
		select {
		case p.out <- ev:
		case <-p.done:
			return
		}
	}
}
