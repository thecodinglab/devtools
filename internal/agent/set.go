package agent

import (
	"sort"
	"time"
)

// Set is what the server holds: one Tracker per pane, created on first use.
// It reads the clock it was given at every input. After any input the caller
// calls Tick to learn when the next timer is due. Not safe for concurrent
// use; the server's state loop owns it.
type Set struct {
	now   func() time.Time
	panes map[string]*Tracker
}

// NewSet returns an empty set that reads the time from now.
func NewSet(now func() time.Time) *Set {
	return &Set{now: now, panes: map[string]*Tracker{}}
}

// Title is a title the pane set.
func (s *Set) Title(pane, title string) []Change {
	return s.input(pane, true, func(t *Tracker, now time.Time) { t.Title(now, title) })
}

// Report is one OSC 7501 root report of the pane.
func (s *Set) Report(pane string, r Report) []Change {
	return s.input(pane, true, func(t *Tracker, now time.Time) { t.Report(now, r) })
}

// Procs is one poll of the processes of the pane's foreground group.
func (s *Set) Procs(pane string, group []Proc) []Change {
	return s.input(pane, true, func(t *Tracker, now time.Time) { t.Foreground(now, group) })
}

// Seen says a focused window displays the pane's tab.
func (s *Set) Seen(pane string) []Change {
	return s.input(pane, false, func(t *Tracker, now time.Time) { t.Seen(now) })
}

// Gone removes the pane. If it was an agent, the one change says it is no
// longer one (Kind "").
func (s *Set) Gone(pane string) []Change {
	t, ok := s.panes[pane]
	if !ok {
		return nil
	}
	delete(s.panes, pane)
	if t.kind == "" {
		return nil
	}
	return []Change{{Pane: pane}}
}

// Tick lets every waiting change whose time has come take effect, in the
// order of the pane ids. next is when to call Tick again; zero means no
// timer is needed.
func (s *Set) Tick() (changes []Change, next time.Time) {
	now := s.now()
	for _, pane := range s.sorted() {
		t := s.panes[pane]
		before := t.State()
		changed, at := t.Tick(now)
		if changed {
			changes = append(changes, change(pane, before, t.State()))
		}
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	return changes, next
}

// Kind is the kind of agent the pane runs, "" when it runs none.
func (s *Set) Kind(pane string) Kind {
	if t, ok := s.panes[pane]; ok {
		return t.kind
	}
	return ""
}

// All is the state of every pane that is an agent, by pane id.
func (s *Set) All() map[string]State {
	all := make(map[string]State)
	for pane, t := range s.panes {
		if t.kind != "" {
			all[pane] = t.State()
		}
	}
	return all
}

// input runs one input on the pane's tracker, creating it when create is
// set, and returns the change it made to an agent, if any.
func (s *Set) input(pane string, create bool, f func(*Tracker, time.Time)) []Change {
	t, ok := s.panes[pane]
	if !ok {
		if !create {
			return nil
		}
		t = New()
		s.panes[pane] = t
	}
	before := t.State()
	f(t, s.now())
	after := t.State()
	if same(before, after) || before.Kind == "" && after.Kind == "" {
		return nil
	}
	return []Change{change(pane, before, after)}
}

// change describes the step from before to after. Notify is set on a
// transition into blocked or done.
func change(pane string, before, after State) Change {
	c := Change{Pane: pane, State: after}
	if after.Kind == "" {
		c.State = State{}
		return c
	}
	c.Notify = after.Status != before.Status && (after.Status == Blocked || after.Status == Done)
	return c
}

func (s *Set) sorted() []string {
	panes := make([]string, 0, len(s.panes))
	for pane := range s.panes {
		panes = append(panes, pane)
	}
	sort.Strings(panes)
	return panes
}
