package agent

import "time"

// TrackerState is the whole state of a Tracker, for the live upgrade of the
// server (internal/handover): the old server image exports every tracker,
// the new one imports them, so an agent keeps its status, its pending
// change and the time of its last change.
type TrackerState struct {
	Kind      Kind      `json:"kind"`
	Missed    int       `json:"missed"`
	Title     string    `json:"title"`
	Titled    bool      `json:"titled"`
	Reported  bool      `json:"reported"`
	LastState Status    `json:"lastState"`
	Status    Status    `json:"status"`
	Done      bool      `json:"done"`
	Since     time.Time `json:"since"`
	Pending   bool      `json:"pending"`
	Target    Status    `json:"target"`
	Deadline  time.Time `json:"deadline"`
	Seen      bool      `json:"seen"`
}

func (t *Tracker) export() TrackerState {
	return TrackerState{
		Kind: t.kind, Missed: t.missed, Title: t.title, Titled: t.titled, Reported: t.reported,
		LastState: t.lastState, Status: t.status, Done: t.done, Since: t.since,
		Pending: t.pending, Target: t.target, Deadline: t.deadline, Seen: t.seen,
	}
}

func importTracker(s TrackerState) *Tracker {
	return &Tracker{
		kind: s.Kind, missed: s.Missed, title: s.Title, titled: s.Titled, reported: s.Reported,
		lastState: s.LastState, status: s.Status, done: s.Done, since: s.Since,
		pending: s.Pending, target: s.Target, deadline: s.Deadline, seen: s.Seen,
	}
}

// Export returns the state of every tracker of the set, by pane.
func (s *Set) Export() map[string]TrackerState {
	out := make(map[string]TrackerState, len(s.panes))
	for pane, t := range s.panes {
		out[pane] = t.export()
	}
	return out
}

// Import replaces the trackers of the set with the exported ones.
func (s *Set) Import(m map[string]TrackerState) {
	s.panes = make(map[string]*Tracker, len(m))
	for pane, st := range m {
		s.panes[pane] = importTracker(st)
	}
}
