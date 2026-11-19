package agent

import (
	"time"

	"devtools/internal/session"
)

// Debounce is how long a change out of working waits for a contrary input
// before it takes effect (3.6.2).
const Debounce = 500 * time.Millisecond

// misses is the number of consecutive polls without a match that end an
// agent (3.6.1): a tool the agent runs can take the foreground for one.
const misses = 2

// Tracker follows the agent of one pane. It is pure: it has no clock of its
// own and starts no timer; every input carries the time it happened, and
// Tick tells when it wants to be called again. Not safe for concurrent use.
type Tracker struct {
	kind   Kind // "" while the pane is no agent
	missed int  // consecutive polls without a match

	title     string // sanitised
	titled    bool   // a title was received
	reported  bool   // the pane sent an OSC 7501 report since the agent started
	lastState Status // the mapped state of the last report

	status Status // effective: working, blocked, idle or unknown
	done   bool   // with status idle: left working and not seen since
	since  time.Time

	pending  bool      // a change out of working waits for deadline
	target   Status    // what it changes to
	deadline time.Time // when it takes effect
	seen     bool      // the pane was seen while the change waited
}

// New returns the tracker of a pane that is not an agent yet.
func New() *Tracker { return &Tracker{} }

// State is what is known of the pane's agent; Kind "" means none.
func (t *Tracker) State() State {
	if t.kind == "" {
		return State{Title: t.title}
	}
	return State{Kind: t.kind, Status: t.reportedStatus(), Since: t.since, Title: t.title}
}

// reportedStatus is the effective status with done derived.
func (t *Tracker) reportedStatus() Status {
	if t.status == Idle && t.done {
		return Done
	}
	return t.status
}

// Foreground is one poll of the processes of the pane's foreground group.
// The kind is set on the first poll that matches and cleared after two
// consecutive polls without a match.
func (t *Tracker) Foreground(now time.Time, group []Proc) (changed bool) {
	return t.update(now, func() {
		k := identify(t.kind, group)
		switch {
		case k != "" && k == t.kind:
			t.missed = 0
		case k != "":
			t.start(now, k)
		case t.kind != "":
			t.missed++
			if t.missed >= misses {
				t.end()
			}
		}
	})
}

// start makes the pane an agent of kind k. Whatever the pane already said
// counts: a report that came before the first matching poll, or the current
// title. With nothing heard the status is unknown (3.6.2; that it is unknown
// from the first moment is provisional E4).
func (t *Tracker) start(now time.Time, k Kind) {
	t.kind = k
	t.missed = 0
	t.status = Unknown
	t.done = false
	t.pending = false
	t.since = now
	if s, ok := t.source(); ok {
		t.status = s
	}
}

// end forgets the agent. The title stays: it belongs to the pane.
func (t *Tracker) end() {
	*t = Tracker{title: t.title, titled: t.titled}
}

// source is the status the pane's inputs give for the current kind, if any.
func (t *Tracker) source() (Status, bool) {
	switch t.kind {
	case Claude:
		if t.reported {
			return t.lastState, true
		}
		if t.titled {
			return fromClaudeTitle(t.title)
		}
	case Codex:
		if t.titled {
			return fromCodexTitle(t.title), true
		}
	}
	return "", false
}

// Title is a title the pane set (OSC 0 or 2). It is sanitised and cut to
// session.MaxTitle runes. The engine reports a title again when it is
// repeated; a repeated title changes nothing.
func (t *Tracker) Title(now time.Time, title string) (changed bool) {
	title = session.Sanitize(title, session.MaxTitle)
	if t.titled && title == t.title {
		return false
	}
	return t.update(now, func() {
		t.title, t.titled = title, true
		if t.kind == Claude && t.reported {
			return // once a pane has sent a report, reports are its only source
		}
		if s, ok := t.source(); ok {
			t.apply(now, s)
		}
	})
}

// Report is one OSC 7501 root report of the pane.
func (t *Tracker) Report(now time.Time, r Report) (changed bool) {
	return t.update(now, func() {
		t.reported, t.lastState = true, fromReport(r)
		if t.kind == Claude {
			t.apply(now, t.lastState)
		}
	})
}

// Seen says a focused window displays the pane's tab: done turns into idle,
// and a change out of working that is still waiting will not become done.
func (t *Tracker) Seen(now time.Time) (changed bool) {
	return t.update(now, func() {
		if t.pending {
			t.seen = true
		}
		t.done = false
	})
}

// Tick lets a waiting change take effect once its time has come. next is
// when Tick wants to be called again; zero means no timer is needed.
func (t *Tracker) Tick(now time.Time) (changed bool, next time.Time) {
	changed = t.update(now, func() {
		if t.pending && !now.Before(t.deadline) {
			t.pending = false
			t.status = t.target
			t.done = t.target == Idle && !t.seen
		}
	})
	return changed, t.next()
}

// next is the deadline of the waiting change, zero when none waits.
func (t *Tracker) next() time.Time {
	if t.pending {
		return t.deadline
	}
	return time.Time{}
}

// apply feeds the status one input gives (3.6.2): into working or blocked at
// once; out of working only after Debounce without a contrary input; any
// other change at once.
func (t *Tracker) apply(now time.Time, s Status) {
	switch {
	case s == Working || s == Blocked:
		t.pending = false
		t.done = false
		t.status = s
	case t.status == Working:
		if !t.pending {
			t.pending, t.deadline, t.seen = true, now.Add(Debounce), false
		}
		t.target = s
	default:
		if s != Idle {
			t.done = false
		}
		t.status = s
	}
}

// update runs f and reports whether the state changed; Since moves when
// the status did.
func (t *Tracker) update(now time.Time, f func()) bool {
	before := t.State()
	f()
	if t.kind != "" && t.reportedStatus() != before.Status {
		t.since = now
	}
	return !same(before, t.State())
}

func same(a, b State) bool {
	return a.Kind == b.Kind && a.Status == b.Status && a.Title == b.Title && a.Since.Equal(b.Since)
}
