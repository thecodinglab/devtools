package proto

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ErrGap is returned by State.Apply when a delta's revision is not the next
// one. A gap is a bug: the receiver closes the connection and reconnects
// (4.2.2).
var ErrGap = errors.New("proto: state revision gap")

// Message types of 4.2.2.
const (
	TypeStateSnapshot = "state.snapshot"
	TypeStateDelta    = "state.delta"
	TypeStateGet      = "state.get"
)

// Values of the enumerated fields of the state objects. A value a receiver
// does not know is read as the value named unknown (4.1).
const (
	PaneDormant = "dormant"
	PaneRunning = "running"
	PaneExited  = "exited"

	AgentClaude = "claude"
	AgentCodex  = "codex"

	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"

	Unknown = "unknown"

	SplitNone  = ""
	SplitRight = "right"
	SplitDown  = "down"
)

// Operations and kinds of a state change.
const (
	OpPut = "put"
	OpDel = "del"

	KindSession = "session"
	KindTab     = "tab"
	KindPane    = "pane"
	KindAgent   = "agent"
)

// Session is one session: the key is its canonical directory.
type Session struct {
	ID      string   `json:"id"`
	Dir     string   `json:"dir"`
	Label   string   `json:"label"`
	Tabs    []string `json:"tabs"`    // ids in index order
	Current string   `json:"current"` // tab id
	Shown   int64    `json:"shown"`   // unix ms a view was last set on it, 0 if never
}

// Tab is one tab of a session.
type Tab struct {
	ID      string   `json:"id"`
	Session string   `json:"session"`
	Index   int      `json:"index"`   // from 1, renumbered when a tab closes
	Panes   []string `json:"panes"`   // 1 or 2 ids, first is left or top
	Split   string   `json:"split"`   // SplitNone, SplitRight, SplitDown
	Focused string   `json:"focused"` // pane id
}

// Pane is one pane of a tab.
type Pane struct {
	ID      string    `json:"id"`
	Tab     string    `json:"tab"`
	Session string    `json:"session"`
	State   string    `json:"state"` // PaneDormant, PaneRunning, PaneExited, else Unknown
	Cwd     string    `json:"cwd"`
	Program string    `json:"program"` // name of the foreground group's leader
	Editor  bool      `json:"editor"`  // a process of the foreground group is vim, neovim or fzf
	Busy    bool      `json:"busy"`    // the foreground group is not the shell's
	Title   string    `json:"title"`
	Exit    *PaneExit `json:"exit,omitempty"` // only when State is PaneExited
}

// PaneExit is how the program of an exited pane ended.
type PaneExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal"`
	Text   string `json:"text"`
}

// Agent is the agent record of a pane; Pane is its key.
type Agent struct {
	Pane    string `json:"pane"`
	Tab     string `json:"tab"`
	Session string `json:"session"`
	Kind    string `json:"kind"`   // AgentClaude, AgentCodex, else Unknown
	Status  string `json:"status"` // StatusIdle, StatusWorking, StatusBlocked, StatusDone, else Unknown
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	Since   int64  `json:"since"` // unix ms of the last status change
}

// Snapshot is state.snapshot: the whole state at revision Rev. It is the
// first message after welcome on an interactive connection and the reply to
// state.get (with the request's id).
type Snapshot struct {
	Rev      uint64    `json:"rev"`
	Sessions []Session `json:"sessions"`
	Tabs     []Tab     `json:"tabs"`
	Panes    []Pane    `json:"panes"`
	Agents   []Agent   `json:"agents"`
}

// Delta is state.delta: the changes from revision Rev-1 to Rev.
type Delta struct {
	Rev     uint64   `json:"rev"`
	Changes []Change `json:"changes"`
}

// Change is one change of a delta. A put carries the whole object in Value;
// a del has no Value. For an agent, ID is the pane id.
type Change struct {
	Op    string          `json:"op"`   // OpPut, OpDel
	Kind  string          `json:"kind"` // KindSession, KindTab, KindPane, KindAgent
	ID    string          `json:"id"`
	Value json.RawMessage `json:"value,omitempty"`
}

// StateGet is state.get, a request. Its reply is a state.snapshot with the
// request's id; no deltas follow on a one-shot connection.
type StateGet struct{}

func (Snapshot) Type() string { return TypeStateSnapshot }
func (Delta) Type() string    { return TypeStateDelta }
func (StateGet) Type() string { return TypeStateGet }

// Put returns the change that puts v (a Session, Tab, Pane or Agent).
func Put(v any) Change {
	var kind, id string
	switch o := v.(type) {
	case Session:
		kind, id = KindSession, o.ID
	case Tab:
		kind, id = KindTab, o.ID
	case Pane:
		kind, id = KindPane, o.ID
	case Agent:
		kind, id = KindAgent, o.Pane
	default:
		panic(fmt.Sprintf("proto: Put of %T", v))
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("proto: encode %s %s: %v", kind, id, err))
	}
	return Change{Op: OpPut, Kind: kind, ID: id, Value: b}
}

// Del returns the change that deletes the object of kind with id.
func Del(kind, id string) Change { return Change{Op: OpDel, Kind: kind, ID: id} }

func (p *Pane) normalize() {
	switch p.State {
	case PaneDormant, PaneRunning, PaneExited:
	default:
		p.State = Unknown
	}
}

func (a *Agent) normalize() {
	switch a.Kind {
	case AgentClaude, AgentCodex:
	default:
		a.Kind = Unknown
	}
	switch a.Status {
	case StatusIdle, StatusWorking, StatusBlocked, StatusDone:
	default:
		a.Status = Unknown
	}
}

func (s *Snapshot) normalize() {
	for i := range s.Panes {
		s.Panes[i].normalize()
	}
	for i := range s.Agents {
		s.Agents[i].normalize()
	}
}

// State is a snapshot with deltas applied; used by the fleet, prototest and
// the command line. The maps are keyed by id (agents by pane id).
type State struct {
	Rev      uint64
	Sessions map[string]Session
	Tabs     map[string]Tab
	Panes    map[string]Pane
	Agents   map[string]Agent
}

// NewState builds the state of a snapshot.
func NewState(s Snapshot) *State {
	st := &State{
		Rev:      s.Rev,
		Sessions: make(map[string]Session, len(s.Sessions)),
		Tabs:     make(map[string]Tab, len(s.Tabs)),
		Panes:    make(map[string]Pane, len(s.Panes)),
		Agents:   make(map[string]Agent, len(s.Agents)),
	}
	for _, o := range s.Sessions {
		st.Sessions[o.ID] = o
	}
	for _, o := range s.Tabs {
		st.Tabs[o.ID] = o
	}
	for _, o := range s.Panes {
		o.normalize()
		st.Panes[o.ID] = o
	}
	for _, o := range s.Agents {
		o.normalize()
		st.Agents[o.Pane] = o
	}
	return st
}

// Apply applies a delta. It returns ErrGap when d.Rev is not s.Rev+1, and an
// error wrapping ErrMalformed when a change has an unknown op or kind, a put
// without a value that decodes, or a value whose id differs from the
// change's. On an error nothing of d is applied. Deleting an object that does
// not exist is not an error.
func (s *State) Apply(d Delta) error {
	if d.Rev != s.Rev+1 {
		return fmt.Errorf("%w: have %d, got %d", ErrGap, s.Rev, d.Rev)
	}
	type staged struct {
		c Change
		v any
	}
	todo := make([]staged, 0, len(d.Changes))
	for i, c := range d.Changes {
		v, err := decodeChange(c)
		if err != nil {
			return fmt.Errorf("%w: change %d (%s %s %s): %v", ErrMalformed, i, c.Op, c.Kind, c.ID, err)
		}
		todo = append(todo, staged{c, v})
	}
	s.ensure()
	for _, t := range todo {
		if t.c.Op == OpDel {
			switch t.c.Kind {
			case KindSession:
				delete(s.Sessions, t.c.ID)
			case KindTab:
				delete(s.Tabs, t.c.ID)
			case KindPane:
				delete(s.Panes, t.c.ID)
			case KindAgent:
				delete(s.Agents, t.c.ID)
			}
			continue
		}
		switch o := t.v.(type) {
		case Session:
			s.Sessions[o.ID] = o
		case Tab:
			s.Tabs[o.ID] = o
		case Pane:
			s.Panes[o.ID] = o
		case Agent:
			s.Agents[o.Pane] = o
		}
	}
	s.Rev = d.Rev
	return nil
}

func (s *State) ensure() {
	if s.Sessions == nil {
		s.Sessions = map[string]Session{}
	}
	if s.Tabs == nil {
		s.Tabs = map[string]Tab{}
	}
	if s.Panes == nil {
		s.Panes = map[string]Pane{}
	}
	if s.Agents == nil {
		s.Agents = map[string]Agent{}
	}
}

// decodeChange checks one change and decodes the value of a put.
func decodeChange(c Change) (any, error) {
	switch c.Kind {
	case KindSession, KindTab, KindPane, KindAgent:
	default:
		return nil, errors.New("unknown kind")
	}
	if c.ID == "" {
		return nil, errors.New("no id")
	}
	switch c.Op {
	case OpDel:
		return nil, nil
	case OpPut:
	default:
		return nil, errors.New("unknown op")
	}
	if len(c.Value) == 0 || string(c.Value) == "null" {
		return nil, errors.New("put without a value")
	}
	var id string
	var v any
	switch c.Kind {
	case KindSession:
		var o Session
		if err := json.Unmarshal(c.Value, &o); err != nil {
			return nil, err
		}
		if o.ID == "" {
			o.ID = c.ID
		}
		id, v = o.ID, o
	case KindTab:
		var o Tab
		if err := json.Unmarshal(c.Value, &o); err != nil {
			return nil, err
		}
		if o.ID == "" {
			o.ID = c.ID
		}
		id, v = o.ID, o
	case KindPane:
		var o Pane
		if err := json.Unmarshal(c.Value, &o); err != nil {
			return nil, err
		}
		if o.ID == "" {
			o.ID = c.ID
		}
		o.normalize()
		id, v = o.ID, o
	case KindAgent:
		var o Agent
		if err := json.Unmarshal(c.Value, &o); err != nil {
			return nil, err
		}
		if o.Pane == "" {
			o.Pane = c.ID
		}
		o.normalize()
		id, v = o.Pane, o
	}
	if id != c.ID {
		return nil, fmt.Errorf("value names %q", id)
	}
	return v, nil
}

// SessionView is what a client needs to draw one session.
type SessionView struct {
	Session Session
	Tabs    []Tab   // index order
	Panes   []Pane  // of the current tab, in tab order
	Agents  []Agent // of the session: by tab index, then by the pane's place in its tab
}

// View returns the view of a session, false when the state has no session
// with that id. Ids a session or tab lists but the state does not hold are
// left out.
func (s *State) View(session string) (SessionView, bool) {
	sess, ok := s.Sessions[session]
	if !ok {
		return SessionView{}, false
	}
	v := SessionView{Session: sess}
	for _, id := range sess.Tabs {
		if t, ok := s.Tabs[id]; ok {
			v.Tabs = append(v.Tabs, t)
		}
	}
	sort.SliceStable(v.Tabs, func(i, j int) bool { return v.Tabs[i].Index < v.Tabs[j].Index })
	if cur, ok := s.Tabs[sess.Current]; ok {
		for _, id := range cur.Panes {
			if p, ok := s.Panes[id]; ok {
				v.Panes = append(v.Panes, p)
			}
		}
	}
	seen := map[string]bool{}
	for _, t := range v.Tabs {
		for _, id := range t.Panes {
			if a, ok := s.Agents[id]; ok && a.Session == session && !seen[id] {
				seen[id] = true
				v.Agents = append(v.Agents, a)
			}
		}
	}
	var rest []Agent
	for id, a := range s.Agents {
		if a.Session == session && !seen[id] {
			rest = append(rest, a)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Pane < rest[j].Pane })
	v.Agents = append(v.Agents, rest...)
	return v, true
}
