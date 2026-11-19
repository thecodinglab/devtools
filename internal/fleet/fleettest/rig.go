package fleettest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
)

// Wait is how long Next, Event and the other waits of the kit wait for
// something the fleet's goroutines do, in real time.
var Wait = time.Second

// Quiet is how long None waits to see that nothing comes.
var Quiet = 50 * time.Millisecond

// Rig runs a fleet of the local machine and the given machines on a fake
// clock and a fake dialer. Every update is collected, so Next can find one
// of a kind without losing the others. The fleet stops when the test ends.
type Rig struct {
	T      testing.TB
	F      *fleet.Fleet
	Clock  *Clock
	Dialer *Dialer

	machines map[string]*Machine

	mu      sync.Mutex
	got     []fleet.Update
	all     []fleet.Update
	closed  bool
	arrived chan struct{}
}

// New starts a fleet of the local machine and machines, with a pane area
// of 80 by 23. Run dials the local machine and the permanent ones at once;
// their dials wait until the test scripts them (Online, Fail, ...).
func New(t *testing.T, machines ...config.Machine) *Rig {
	t.Helper()
	return NewWith(t, fleet.Options{Machines: machines, Cols: 80, Rows: 23})
}

// NewUnread is New for a client that never reads Updates: nothing is
// collected, so Next, Summary, Take and None do not apply.
func NewUnread(t *testing.T, machines ...config.Machine) *Rig {
	t.Helper()
	return newRig(t, fleet.Options{Machines: machines, Cols: 80, Rows: 23}, false)
}

// NewWith is New with options; Dialer and Clock are the rig's.
func NewWith(t *testing.T, o fleet.Options) *Rig {
	t.Helper()
	return newRig(t, o, true)
}

func newRig(t *testing.T, o fleet.Options, collect bool) *Rig {
	t.Helper()
	r := &Rig{
		T:        t,
		Clock:    NewClock(),
		Dialer:   &Dialer{machines: map[string]*Machine{}},
		machines: map[string]*Machine{},
		arrived:  make(chan struct{}, 1),
	}
	r.addMachine("")
	for _, m := range o.Machines {
		r.addMachine(m.Name)
	}
	o.Dialer, o.Clock = r.Dialer, r.Clock
	r.F = fleet.New(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.F.Run(ctx)
	}()
	if collect {
		go r.collect()
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("fleettest: Run did not return after the context ended")
		}
	})
	return r
}

func (r *Rig) addMachine(name string) {
	if _, ok := r.machines[name]; ok {
		return
	}
	m := newMachine(r.T, name)
	r.machines[name] = m
	r.Dialer.machines[name] = m
}

func (r *Rig) collect() {
	for u := range r.F.Updates() {
		r.mu.Lock()
		r.got = append(r.got, u)
		r.all = append(r.all, u)
		r.mu.Unlock()
		select {
		case r.arrived <- struct{}{}:
		default:
		}
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

// M is the scripted server of a machine; "" is the local machine.
func (r *Rig) M(name string) *Machine {
	r.T.Helper()
	m, ok := r.machines[name]
	if !ok {
		r.T.Fatalf("fleettest: %q is not a machine of the rig", name)
	}
	return m
}

// Advance moves the fake clock and runs the timers that fall due.
func (r *Rig) Advance(d time.Duration) { r.Clock.Advance(d) }

// Next is the first collected update of kind, which it takes out; the test
// fails when none arrives within Wait.
func (r *Rig) Next(kind fleet.UpdateKind) fleet.Update {
	r.T.Helper()
	return r.NextWhere(kind, func(fleet.Update) bool { return true })
}

// NextWhere is the first collected update of kind for which ok holds; it
// and the updates of kind before it are taken out.
func (r *Rig) NextWhere(kind fleet.UpdateKind, ok func(fleet.Update) bool) fleet.Update {
	r.T.Helper()
	deadline := time.After(Wait)
	for {
		r.mu.Lock()
		for i, u := range r.got {
			if u.Kind != kind {
				continue
			}
			if ok(u) {
				r.got = append(r.got[:i:i], r.got[i+1:]...)
				r.dropKindBefore(kind, i)
				r.mu.Unlock()
				return u
			}
		}
		r.mu.Unlock()
		select {
		case <-r.arrived:
		case <-deadline:
			r.T.Fatalf("fleettest: no update of kind %s within %v; have %s", KindName(kind), Wait, r.describe())
			return fleet.Update{}
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// dropKindBefore removes the updates of kind before index i; r.mu is held.
func (r *Rig) dropKindBefore(kind fleet.UpdateKind, i int) {
	kept := r.got[:0]
	for j, u := range r.got {
		if j < i && u.Kind == kind {
			continue
		}
		kept = append(kept, u)
	}
	r.got = kept
}

// Summary waits, for at most Wait, until a collected summary that was not
// taken out, or the newest summary, satisfies ok, and returns it. The
// summaries up to it are taken out. The summary is the status row's
// current value: a test asks what it is, not only what changes.
func (r *Rig) Summary(ok func(fleet.SummaryData) bool) fleet.SummaryData {
	r.T.Helper()
	return r.waitSummary(ok, false)
}

// Settle is Summary, moving the clock by the 50 ms of the summary's rate
// limit whenever no summary satisfies ok yet. It is for tests in which
// time does not matter otherwise: every step moves the clock.
func (r *Rig) Settle(ok func(fleet.SummaryData) bool) fleet.SummaryData {
	r.T.Helper()
	return r.waitSummary(ok, true)
}

func (r *Rig) waitSummary(ok func(fleet.SummaryData) bool, advance bool) fleet.SummaryData {
	r.T.Helper()
	deadline := time.Now().Add(Wait)
	for {
		if s, found := r.findSummary(ok); found {
			return s
		}
		if time.Now().After(deadline) {
			last, _ := r.Latest()
			r.T.Fatalf("fleettest: no summary as wanted within %v; the newest is %+v", Wait, last)
		}
		time.Sleep(5 * time.Millisecond)
		if advance {
			r.Advance(50 * time.Millisecond)
		}
	}
}

// findSummary looks for a summary that satisfies ok; ok runs without the
// lock, so it may fail the test.
func (r *Rig) findSummary(ok func(fleet.SummaryData) bool) (fleet.SummaryData, bool) {
	r.mu.Lock()
	var pending []fleet.Update
	for _, u := range r.got {
		if u.Kind == fleet.Summary {
			pending = append(pending, u)
		}
	}
	newest, have := fleet.Update{}, false
	for i := len(r.all) - 1; i >= 0; i-- {
		if r.all[i].Kind == fleet.Summary {
			newest, have = r.all[i], true
			break
		}
	}
	r.mu.Unlock()
	for i, u := range pending {
		if ok(u.Summary) {
			r.takeSummaries(i + 1)
			return u.Summary, true
		}
	}
	if have && ok(newest.Summary) {
		r.takeSummaries(len(pending))
		return newest.Summary, true
	}
	return fleet.SummaryData{}, false
}

// takeSummaries takes out the first n collected summaries.
func (r *Rig) takeSummaries(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.got[:0]
	for _, u := range r.got {
		if u.Kind == fleet.Summary && n > 0 {
			n--
			continue
		}
		kept = append(kept, u)
	}
	r.got = kept
}

// Latest is the newest summary that arrived, taken out or not.
func (r *Rig) Latest() (fleet.SummaryData, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.all) - 1; i >= 0; i-- {
		if r.all[i].Kind == fleet.Summary {
			return r.all[i].Summary, true
		}
	}
	return fleet.SummaryData{}, false
}

// MachineOf is the state of the machine name in s; the test fails when s
// has none.
func MachineOf(t testing.TB, s fleet.SummaryData, name string) fleet.MachineState {
	t.Helper()
	for _, m := range s.Machines {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("fleettest: the summary has no machine %q", name)
	return fleet.MachineState{}
}

// StateOf is the state of the machine name in s, "" when s has none.
func StateOf(s fleet.SummaryData, name string) string {
	for _, m := range s.Machines {
		if m.Name == name {
			return m.State
		}
	}
	return ""
}

// Take takes out and returns every collected update of kind.
func (r *Rig) Take(kind fleet.UpdateKind) []fleet.Update {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []fleet.Update
	kept := r.got[:0]
	for _, u := range r.got {
		if u.Kind == kind {
			out = append(out, u)
		} else {
			kept = append(kept, u)
		}
	}
	r.got = kept
	return out
}

// Count is how many updates of kind arrived since the rig started, taken
// out or not.
func (r *Rig) Count(kind fleet.UpdateKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.all {
		if u.Kind == kind {
			n++
		}
	}
	return n
}

// None fails the test when an update of kind arrives within Quiet, or was
// collected and not taken out.
func (r *Rig) None(kind fleet.UpdateKind) {
	r.T.Helper()
	time.Sleep(Quiet)
	if got := r.Take(kind); len(got) > 0 {
		r.T.Fatalf("fleettest: %d unexpected updates of kind %s: %+v", len(got), KindName(kind), got)
	}
}

// Hub opens the hub (Fleet.HubOpened) and returns its events.
func (r *Rig) Hub() <-chan hub.Event { return r.F.HubOpened() }

// OpenHub opens the hub and collects its events.
func (r *Rig) OpenHub() *Feed { return Collect(r.T, r.Hub()) }

// Eventually waits, for at most Wait, until cond holds, and fails the test
// with what otherwise.
func (r *Rig) Eventually(what string, cond func() bool) {
	r.T.Helper()
	deadline := time.Now().Add(Wait)
	for !cond() {
		if time.Now().After(deadline) {
			r.T.Fatalf("fleettest: %s did not happen within %v", what, Wait)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *Rig) describe() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, u := range r.got {
		fmt.Fprintf(&b, "%s ", KindName(u.Kind))
	}
	if b.Len() == 0 {
		return "nothing"
	}
	return b.String()
}

// KindName is the name of an update kind, for messages.
func KindName(k fleet.UpdateKind) string {
	names := []string{"Shown", "Frame", "Summary", "Stale", "Notice", "Bell", "OpenHub", "Clipboard"}
	if int(k) >= 0 && int(k) < len(names) {
		return names[k]
	}
	return fmt.Sprintf("UpdateKind(%d)", int(k))
}

// Feed collects the events of one opening of the hub.
type Feed struct {
	t       testing.TB
	mu      sync.Mutex
	got     []hub.Event
	all     []hub.Event
	closed  bool
	arrived chan struct{}
}

// Collect reads ch until it is closed.
func Collect(t testing.TB, ch <-chan hub.Event) *Feed {
	f := &Feed{t: t, arrived: make(chan struct{}, 1)}
	go func() {
		for ev := range ch {
			f.mu.Lock()
			f.got = append(f.got, ev)
			f.all = append(f.all, ev)
			f.mu.Unlock()
			select {
			case f.arrived <- struct{}{}:
			default:
			}
		}
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		select {
		case f.arrived <- struct{}{}:
		default:
		}
	}()
	return f
}

// Next is the first collected event of machine and kind, which it takes
// out with the events of that machine and kind before it; the test fails
// when none arrives within Wait.
func (f *Feed) Next(machine string, kind hub.EventKind) hub.Event {
	f.t.Helper()
	deadline := time.After(Wait)
	for {
		f.mu.Lock()
		for i, ev := range f.got {
			if ev.Machine == machine && ev.Kind == kind {
				f.got = append(f.got[:i:i], f.got[i+1:]...)
				f.mu.Unlock()
				return ev
			}
		}
		f.mu.Unlock()
		select {
		case <-f.arrived:
		case <-deadline:
			f.t.Fatalf("fleettest: no hub event %s of %q within %v; have %s", EventName(kind), machine, Wait, f.describe())
			return hub.Event{}
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// None fails the test when an event of machine and kind arrives within
// Quiet, or was collected and not taken out.
func (f *Feed) None(machine string, kind hub.EventKind) {
	f.t.Helper()
	time.Sleep(Quiet)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.got {
		if ev.Machine == machine && ev.Kind == kind {
			f.t.Fatalf("fleettest: unexpected hub event %s of %q: %+v", EventName(kind), machine, ev)
		}
	}
}

// All is every event that arrived, in order, taken out or not.
func (f *Feed) All() []hub.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hub.Event(nil), f.all...)
}

// Closed reports whether the channel was closed.
func (f *Feed) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// WaitClosed waits for the channel to be closed.
func (f *Feed) WaitClosed() {
	f.t.Helper()
	deadline := time.Now().Add(Wait)
	for !f.Closed() {
		if time.Now().After(deadline) {
			f.t.Fatalf("fleettest: the hub's channel was not closed within %v", Wait)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *Feed) describe() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, ev := range f.got {
		fmt.Fprintf(&b, "%s(%q) ", EventName(ev.Kind), ev.Machine)
	}
	if b.Len() == 0 {
		return "nothing"
	}
	return b.String()
}

// EventName is the name of a hub event kind, for messages.
func EventName(k hub.EventKind) string {
	names := []string{"Connecting", "Online", "Items", "Git", "Complete", "Sessions", "Agents", "Failed", "Closed", "Note"}
	if int(k) >= 0 && int(k) < len(names) {
		return names[k]
	}
	return fmt.Sprintf("EventKind(%d)", int(k))
}

// Sess is one session of a snapshot built by Snapshot: the session id
// "s<N>", its one tab "t<N>" and pane "p<N>" share the number N. Agent is
// the status of an agent in the pane, "" for none; Kind its kind, claude
// when empty.
type Sess struct {
	N     int
	Dir   string
	Label string
	Agent string
	Kind  string
	Title string
}

// Snapshot builds a snapshot of revision 1 from sessions.
func Snapshot(ss ...Sess) proto.Snapshot {
	snap := proto.Snapshot{Rev: 1, Sessions: []proto.Session{}, Tabs: []proto.Tab{}, Panes: []proto.Pane{}, Agents: []proto.Agent{}}
	for _, s := range ss {
		sid, tid, pid := fmt.Sprintf("s%d", s.N), fmt.Sprintf("t%d", s.N), fmt.Sprintf("p%d", s.N)
		snap.Sessions = append(snap.Sessions, proto.Session{ID: sid, Dir: s.Dir, Label: s.Label, Tabs: []string{tid}, Current: tid})
		snap.Tabs = append(snap.Tabs, proto.Tab{ID: tid, Session: sid, Index: 1, Panes: []string{pid}, Focused: pid})
		snap.Panes = append(snap.Panes, proto.Pane{ID: pid, Tab: tid, Session: sid, State: proto.PaneRunning, Cwd: s.Dir, Program: "zsh"})
		if s.Agent != "" {
			kind := s.Kind
			if kind == "" {
				kind = proto.AgentClaude
			}
			snap.Agents = append(snap.Agents, proto.Agent{Pane: pid, Tab: tid, Session: sid, Kind: kind, Status: s.Agent, Title: s.Title, Cwd: s.Dir})
		}
	}
	return snap
}

// AgentEvent is event.agent for the pane of session N of Snapshot.
func AgentEvent(n int, status string, shown bool) proto.Msg {
	return proto.Encode(proto.AgentEvent{
		Session: fmt.Sprintf("s%d", n), Tab: fmt.Sprintf("t%d", n), Pane: fmt.Sprintf("p%d", n),
		Kind: proto.AgentClaude, Status: status, Shown: shown,
	})
}

// AgentDelta is a state.delta of revision rev that puts the agent of
// session N of Snapshot in status.
func AgentDelta(rev uint64, n int, status string) proto.Msg {
	a := proto.Agent{
		Pane: fmt.Sprintf("p%d", n), Tab: fmt.Sprintf("t%d", n), Session: fmt.Sprintf("s%d", n),
		Kind: proto.AgentClaude, Status: status,
	}
	return proto.Encode(proto.Delta{Rev: rev, Changes: []proto.Change{proto.Put(a)}})
}
