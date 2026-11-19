// Package model is the structure of a devtools server as pure code (3.3 of
// the plan): sessions, tabs, panes and agents, their layout, and every
// operation on them. An operation changes the state and returns a Result:
// the state changes for state.delta, in a valid order (4.2.2), and the
// effects the server carries out, in the order it must apply them (3.2.3).
//
// The model starts no process and touches no terminal. It reads the file
// system in one place, through a function the tests replace: whether a
// pane's directory still exists when a split or a spawn starts in it.
//
// The model is not safe for concurrent use: it belongs to the server's
// state loop.
package model

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"devtools/internal/proto"
	"devtools/internal/server/persist"
	"devtools/internal/session"
)

// DefaultCols and DefaultRows are the size of a new session (3.3).
const DefaultCols, DefaultRows = persist.DefaultCols, persist.DefaultRows

// State is the structure of one server: sessions, tabs, panes, agents and
// the id counters.
type State struct {
	sessions map[string]*sess
	tabs     map[string]*proto.Tab
	panes    map[string]*pane
	agents   map[string]proto.Agent
	byDir    map[string]string // canonical directory -> session id
	next     persist.Counters  // the numbers of the last ids issued

	now    func() time.Time  // for Session.Shown
	exists func(string) bool // a directory exists
}

// sess is a session with what the wire does not carry.
type sess struct {
	proto.Session
	cols, rows int  // the pane area of the session
	displayed  bool // Display was called since the session was opened or restored
}

// pane is a pane with the size its program runs at.
type pane struct {
	proto.Pane
	cols, rows int // the size of the last Spawn or Resize; 0 while dormant
}

// Result is the outcome of one operation.
type Result struct {
	Changes []proto.Change // for state.delta, in a valid order (4.2.2)
	Effects []Effect       // for the server to carry out, in order
}

// EffectKind is what the server does for an effect.
type EffectKind int

const (
	Spawn  EffectKind = iota // start the pane's program at Cols x Rows in Dir
	Kill                     // close the pane's program
	Resize                   // resize a running pane
	Ended                    // the session is gone: send view.ended to its viewers
	Retab                    // the session's displayed tab changed: reset frames of its viewers
)

func (k EffectKind) String() string {
	switch k {
	case Spawn:
		return "spawn"
	case Kill:
		return "kill"
	case Resize:
		return "resize"
	case Ended:
		return "ended"
	case Retab:
		return "retab"
	}
	return "effect(" + strconv.Itoa(int(k)) + ")"
}

// Effect is one thing the server carries out. Spawn has Session, Pane,
// Cols, Rows and Dir; Resize has Session, Pane, Cols and Rows; Kill has
// Session and Pane and is given for every pane that goes and is not
// dormant (a running pane's program is closed, an exited pane's terminal
// is released); Retab has Session; Ended has Session and Dir (the fields
// of view.ended).
//
// The effects of a Result are in the order of 3.2.3: every Spawn, then
// every Resize, then every Kill, then Retab, then Ended.
type Effect struct {
	Kind          EffectKind
	Session, Pane string
	Cols, Rows    int
	Dir           string
}

// Error is the error of an operation; Code is one of 4.1 (proto.Code*).
type Error struct{ Code, Text string }

func (e *Error) Error() string { return e.Code + ": " + e.Text }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Text: fmt.Sprintf(format, a...)}
}

// New returns an empty state.
func New() *State {
	return &State{
		sessions: map[string]*sess{},
		tabs:     map[string]*proto.Tab{},
		panes:    map[string]*pane{},
		agents:   map[string]proto.Agent{},
		byDir:    map[string]string{},
		now:      time.Now,
		exists:   isDir,
	}
}

func isDir(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// Snapshot returns the whole state. Every list is ordered by id; Rev is 0,
// the server sets it.
func (s *State) Snapshot() proto.Snapshot {
	snap := proto.Snapshot{
		Sessions: make([]proto.Session, 0, len(s.sessions)),
		Tabs:     make([]proto.Tab, 0, len(s.tabs)),
		Panes:    make([]proto.Pane, 0, len(s.panes)),
		Agents:   make([]proto.Agent, 0, len(s.agents)),
	}
	for _, id := range sortedIDs(s.sessions) {
		snap.Sessions = append(snap.Sessions, s.sessions[id].copy())
	}
	for _, id := range sortedIDs(s.tabs) {
		snap.Tabs = append(snap.Tabs, copyTab(s.tabs[id]))
	}
	for _, id := range sortedIDs(s.panes) {
		snap.Panes = append(snap.Panes, s.panes[id].copy())
	}
	for _, id := range sortedIDs(s.agents) {
		snap.Agents = append(snap.Agents, s.agents[id])
	}
	return snap
}

func (x *sess) copy() proto.Session {
	o := x.Session
	o.Tabs = append([]string(nil), x.Tabs...)
	return o
}

func copyTab(t *proto.Tab) proto.Tab {
	o := *t
	o.Panes = append([]string(nil), t.Panes...)
	return o
}

func (p *pane) copy() proto.Pane {
	o := p.Pane
	if p.Exit != nil {
		e := *p.Exit
		o.Exit = &e
	}
	return o
}

// sortedIDs returns the keys of m by kind and number ("p9" before "p10").
func sortedIDs[V any](m map[string]V) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sortIDs(ids)
	return ids
}

func sortIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// issue returns the next id of a kind.
func issue(prefix string, c *uint64) string {
	*c++
	return prefix + strconv.FormatUint(*c, 10)
}

// batch collects what one operation changed. The changes are written when
// the operation ends, in an order that is valid whatever the operation did
// first (4.2.2): the puts of panes, then tabs, then sessions (a new pane
// before the tab that lists it, a new tab before its session), then agents
// (after their pane); then the deletions of agents, then sessions, tabs and
// panes, each after every object that listed it was put or deleted.
type batch struct {
	s       *State
	put     map[string]bool // ids of objects to put
	del     map[string]bool // ids of objects deleted ("a"+pane id for agents)
	putA    map[string]bool // pane ids of agents to put
	effects []Effect
}

func (s *State) batch() *batch {
	return &batch{s: s, put: map[string]bool{}, del: map[string]bool{}, putA: map[string]bool{}}
}

func (b *batch) touch(id string)        { b.put[id] = true }
func (b *batch) touchAgent(pane string) { b.putA[pane] = true }
func (b *batch) effect(e Effect)        { b.effects = append(b.effects, e) }
func (b *batch) gone(id string)         { delete(b.put, id); b.del[id] = true }
func (b *batch) goneAgent(pane string)  { delete(b.putA, pane); b.del["a"+pane] = true }

// rank is the place of an effect kind in the order of 3.2.3.
func rank(k EffectKind) int {
	switch k {
	case Spawn:
		return 0
	case Resize:
		return 1
	case Kill:
		return 2
	case Retab:
		return 3
	case Ended:
		return 4
	}
	return 5
}

func (b *batch) result() Result {
	var r Result
	kinds := func(set map[string]bool, prefix byte) []string {
		var ids []string
		for id := range set {
			if id[0] == prefix {
				ids = append(ids, id)
			}
		}
		sortIDs(ids)
		return ids
	}
	for _, id := range kinds(b.put, 'p') {
		if p, ok := b.s.panes[id]; ok {
			r.Changes = append(r.Changes, proto.Put(p.copy()))
		}
	}
	for _, id := range kinds(b.put, 't') {
		if t, ok := b.s.tabs[id]; ok {
			r.Changes = append(r.Changes, proto.Put(copyTab(t)))
		}
	}
	for _, id := range kinds(b.put, 's') {
		if x, ok := b.s.sessions[id]; ok {
			r.Changes = append(r.Changes, proto.Put(x.copy()))
		}
	}
	agents := make([]string, 0, len(b.putA))
	for id := range b.putA {
		agents = append(agents, id)
	}
	sortIDs(agents)
	for _, id := range agents {
		if a, ok := b.s.agents[id]; ok {
			r.Changes = append(r.Changes, proto.Put(a))
		}
	}
	for _, id := range kinds(b.del, 'a') {
		r.Changes = append(r.Changes, proto.Del(proto.KindAgent, id[1:]))
	}
	for _, k := range []struct {
		prefix byte
		kind   string
	}{{'s', proto.KindSession}, {'t', proto.KindTab}, {'p', proto.KindPane}} {
		for _, id := range kinds(b.del, k.prefix) {
			r.Changes = append(r.Changes, proto.Del(k.kind, id))
		}
	}
	r.Effects = b.effects
	sort.SliceStable(r.Effects, func(i, j int) bool { return rank(r.Effects[i].Kind) < rank(r.Effects[j].Kind) })
	return r
}

// labelOf is the label of a session: the sanitised name given, or the name
// of the directory when none is given.
func labelOf(dir, name string) string {
	if l := session.Sanitize(name, session.MaxLabel); l != "" {
		return l
	}
	return session.Name(filepath.Base(dir), "")
}

// nowMs is the clock in unix milliseconds.
func (s *State) nowMs() int64 { return s.now().UnixMilli() }
