package fleettest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"devtools/internal/proto"
	"devtools/internal/remote"
)

// Dialer is a fake remote.Dialer: every machine of a Rig answers its dials
// as its script says. It records every name it is asked for, and whether
// the dial could log in.
type Dialer struct {
	mu       sync.Mutex
	machines map[string]*Machine
	names    []string
	logins   []bool
}

// Dial answers with the next step of the machine's script. A dial that
// finds no step waits for the next one the test scripts, or for ctx.
func (d *Dialer) Dial(ctx context.Context, name string, login bool) (remote.Link, *remote.Failure) {
	d.mu.Lock()
	d.names = append(d.names, name)
	d.logins = append(d.logins, login)
	m := d.machines[name]
	d.mu.Unlock()
	if m == nil {
		return nil, &remote.Failure{Reason: remote.Unreachable, Text: "not a machine of the rig"}
	}
	m.mu.Lock()
	m.dials++
	m.logins = append(m.logins, login)
	m.mu.Unlock()
	var s step
	select {
	case s = <-m.steps:
	case <-ctx.Done():
		return nil, &remote.Failure{Reason: remote.Unreachable, Text: "cancelled"}
	}
	switch {
	case s.hang:
		<-ctx.Done()
		return nil, &remote.Failure{Reason: remote.Unreachable, Text: "cancelled"}
	case s.fail != nil:
		return nil, s.fail
	case s.later != nil:
		select {
		case snap := <-s.later:
			return m.connect(snap), nil
		case <-ctx.Done():
			return nil, &remote.Failure{Reason: remote.Unreachable, Text: "cancelled"}
		}
	}
	return m.connect(s.snap), nil
}

// Names is every machine name Dial was asked for, in order.
func (d *Dialer) Names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.names...)
}

// Logins is the login flag of every dial, in the order of Names.
func (d *Dialer) Logins() []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bool(nil), d.logins...)
}

// step is one answer to a dial.
type step struct {
	snap  proto.Snapshot
	fail  *remote.Failure
	hang  bool
	later chan proto.Snapshot
}

// Machine is one scripted server of a Rig.
type Machine struct {
	t     testing.TB
	name  string
	steps chan step

	mu      sync.Mutex
	welcome proto.Welcome
	dials   int
	logins  []bool // the login flag of each dial
	sent    []proto.Msg
	live    *Link
	links   int
}

func newMachine(t testing.TB, name string) *Machine {
	host, boot := name, "boot-"+name
	if name == "" {
		host, boot = "local", "boot-local"
	}
	return &Machine{
		t:     t,
		name:  name,
		steps: make(chan step, 1024),
		welcome: proto.Welcome{
			Gen:     proto.Gen,
			Version: "test",
			Caps:    []string{proto.CapCatalog, proto.CapAgents, proto.CapRoute, proto.CapStateGet, proto.CapTree},
			Boot:    boot,
			Host:    host,
			Pid:     1,
			Client:  "c1",
			Clients: 1,
		},
	}
}

// Name is the machine's config alias; "" is the local machine.
func (m *Machine) Name() string { return m.name }

// Online makes the next dial succeed with this snapshot.
func (m *Machine) Online(s proto.Snapshot) { m.steps <- step{snap: s} }

// Fail makes the next dial fail for reason, with the reason as its text.
func (m *Machine) Fail(reason remote.Reason) {
	m.steps <- step{fail: &remote.Failure{Reason: reason, Text: string(reason)}}
}

// FailWith makes the next dial fail with f.
func (m *Machine) FailWith(f remote.Failure) { m.steps <- step{fail: &f} }

// Hang makes the next dial never return (until the fleet gives it up).
func (m *Machine) Hang() { m.steps <- step{hang: true} }

// Later makes the next dial wait until answer is called with the snapshot
// it succeeds with: a machine that answers late.
func (m *Machine) Later() (answer func(proto.Snapshot)) {
	ch := make(chan proto.Snapshot, 1)
	m.steps <- step{later: ch}
	return func(s proto.Snapshot) { ch <- s }
}

// SetWelcome is the welcome of the links made from now on.
func (m *Machine) SetWelcome(w proto.Welcome) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.welcome = w
}

// Welcome is the welcome of the next link.
func (m *Machine) Welcome() proto.Welcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.welcome
}

// Drop ends the live link without a reason: the connection was lost.
func (m *Machine) Drop() { m.DropWith(nil) }

// DropWith ends the live link with the failure f (nil: lost).
func (m *Machine) DropWith(f *remote.Failure) {
	m.t.Helper()
	m.mu.Lock()
	l := m.live
	m.live = nil
	m.mu.Unlock()
	if l == nil {
		m.t.Fatalf("fleettest: %q has no live link to drop", m.name)
	}
	l.end(f, false)
}

// Push sends msg from the server to the fleet on the live link.
func (m *Machine) Push(msg proto.Msg) {
	m.t.Helper()
	m.mu.Lock()
	l := m.live
	m.mu.Unlock()
	if l == nil {
		m.t.Fatalf("fleettest: %q has no live link to push %s on", m.name, msg.T)
	}
	if !l.push(msg) {
		m.t.Fatalf("fleettest: %q: the link of %s is over or full", m.name, msg.T)
	}
}

// Live reports whether the machine has a link the fleet has not closed.
func (m *Machine) Live() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live != nil
}

// Sent is every message the fleet sent to this machine, on every link, in
// order.
func (m *Machine) Sent() []proto.Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]proto.Msg(nil), m.sent...)
}

// SentOf is the messages of type typ the fleet sent.
func (m *Machine) SentOf(typ string) []proto.Msg {
	var out []proto.Msg
	for _, s := range m.Sent() {
		if s.T == typ {
			out = append(out, s)
		}
	}
	return out
}

// Dials is how often the fleet dialled this machine.
func (m *Machine) Dials() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dials
}

// DialLogins is the login flag of every dial of this machine, in order:
// whether the dial could start a new ssh login.
func (m *Machine) DialLogins() []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]bool(nil), m.logins...)
}

// Links is how many links were made to this machine.
func (m *Machine) Links() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.links
}

// connect makes a link whose first message is snap.
func (m *Machine) connect(snap proto.Snapshot) *Link {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := &Link{m: m, welcome: m.welcome, recv: make(chan proto.Msg, 8192)}
	l.recv <- proto.Encode(snap)
	if m.live != nil {
		go m.live.end(nil, false)
	}
	m.live = l
	m.links++
	return l
}

// Link is a scripted remote.Link.
type Link struct {
	m       *Machine
	welcome proto.Welcome
	recv    chan proto.Msg

	mu     sync.Mutex
	ended  bool
	closed bool // by the fleet
	err    *remote.Failure
}

var errOver = errors.New("fleettest: the link is over")

// Send records m as sent to the machine.
func (l *Link) Send(msg proto.Msg) error {
	l.mu.Lock()
	ended := l.ended
	l.mu.Unlock()
	if ended {
		return errOver
	}
	l.m.mu.Lock()
	l.m.sent = append(l.m.sent, msg)
	l.m.mu.Unlock()
	return nil
}

// Recv is what the server sends; closed when the link ends.
func (l *Link) Recv() <-chan proto.Msg { return l.recv }

// Welcome is the server's welcome.
func (l *Link) Welcome() proto.Welcome { return l.welcome }

// Err is why the link ended; nil after Close.
func (l *Link) Err() *remote.Failure {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	return l.err
}

// Close ends the link from the fleet's side.
func (l *Link) Close() {
	l.end(nil, true)
	l.m.mu.Lock()
	if l.m.live == l {
		l.m.live = nil
	}
	l.m.mu.Unlock()
}

func (l *Link) end(f *remote.Failure, byFleet bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return
	}
	l.ended, l.closed, l.err = true, byFleet, f
	close(l.recv)
}

func (l *Link) push(msg proto.Msg) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return false
	}
	select {
	case l.recv <- msg:
		return true
	default:
		return false
	}
}
