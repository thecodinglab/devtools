// Package clienttest drives the client's model without a terminal (5.3.7,
// P1-17a): Fleet is a fake fleet that records every call and is fed
// updates, and Driver runs the model's commands synchronously, the way
// internal/hub/hub_test.go follows a key's commands, on a fake clock.
//
// It is a test-only package (import rule 8).
package clienttest

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/testkit/golden"
)

// Call is one call the client made of the fleet.
type Call struct {
	Name       string // the method: "Switch", "Cancel", "Input", "Request", "Resize", "Focus", "Colors", "HubOpened", "HubClosed", "Step", "Run"
	Msg        proto.Msg
	Target     session.Target
	Cols, Rows int
	Focused    bool
	Delta      int
}

// Fleet is a fake client.Fleet. It records every call; its updates are what
// a test pushes; a state.get is answered from the state the test set, and
// every other request waits for Answer.
type Fleet struct {
	mu      sync.Mutex
	calls   []Call
	updates chan fleet.Update
	closed  bool
	state   *proto.Snapshot
	pending map[uint64]chan proto.Msg
	hub     chan hub.Event
	runs    bool
}

// NewFleet returns a fake fleet whose updates channel holds 256 updates,
// as the real one does.
func NewFleet() *Fleet {
	return &Fleet{updates: make(chan fleet.Update, 256), pending: map[uint64]chan proto.Msg{}}
}

var _ client.Fleet = (*Fleet)(nil)

func (f *Fleet) record(c Call) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

// Calls returns every call so far, in order.
func (f *Fleet) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// CallsOf returns the calls of one method, in order. Requests of state.get
// are left out of "Request": they are the client's own reads.
func (f *Fleet) CallsOf(name string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Name != name {
			continue
		}
		if name == "Request" && c.Msg.T == proto.TypeStateGet {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Inputs returns the messages of every Input call, in order.
func (f *Fleet) Inputs() []proto.Msg {
	var out []proto.Msg
	for _, c := range f.CallsOf("Input") {
		out = append(out, c.Msg)
	}
	return out
}

// Requests returns the messages of every Request call but state.get.
func (f *Fleet) Requests() []proto.Msg {
	var out []proto.Msg
	for _, c := range f.CallsOf("Request") {
		out = append(out, c.Msg)
	}
	return out
}

// Reset forgets the calls so far.
func (f *Fleet) Reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// SetState is the state of the displayed server: state.get is answered
// with it from now on.
func (f *Fleet) SetState(s proto.Snapshot) {
	f.mu.Lock()
	f.state = &s
	f.mu.Unlock()
}

// Push is an update of the fleet, read by a client that runs (client.Run).
func (f *Fleet) Push(u fleet.Update) { f.updates <- u }

// Close ends the updates: a running client ends.
func (f *Fleet) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.updates)
	}
}

// Answer delivers the reply of the request with m's id; false when no
// request with that id waits.
func (f *Fleet) Answer(m proto.Msg) bool {
	f.mu.Lock()
	ch, ok := f.pending[m.ID]
	delete(f.pending, m.ID)
	f.mu.Unlock()
	if ok {
		ch <- m
	}
	return ok
}

// Updates is the channel a running client reads.
func (f *Fleet) Updates() <-chan fleet.Update { return f.updates }

func (f *Fleet) Switch(t session.Target) { f.record(Call{Name: "Switch", Target: t}) }
func (f *Fleet) Cancel()                 { f.record(Call{Name: "Cancel"}) }
func (f *Fleet) Input(m proto.Msg)       { f.record(Call{Name: "Input", Msg: m}) }
func (f *Fleet) Resize(cols, rows int)   { f.record(Call{Name: "Resize", Cols: cols, Rows: rows}) }
func (f *Fleet) Focus(focused bool)      { f.record(Call{Name: "Focus", Focused: focused}) }
func (f *Fleet) Colors(m proto.Msg)      { f.record(Call{Name: "Colors", Msg: m}) }
func (f *Fleet) HubClosed()              { f.record(Call{Name: "HubClosed"}) }
func (f *Fleet) Step(delta int)          { f.record(Call{Name: "Step", Delta: delta}) }
func (f *Fleet) Drop(t session.Target)   { f.record(Call{Name: "Drop", Target: t}) }

// Request records m. A state.get is answered at once from the state set
// with SetState; any other request waits for Answer.
func (f *Fleet) Request(m proto.Msg) <-chan proto.Msg {
	f.record(Call{Name: "Request", Msg: m})
	ch := make(chan proto.Msg, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.T == proto.TypeStateGet && f.state != nil {
		ch <- proto.Request(m.ID, *f.state)
		return ch
	}
	f.pending[m.ID] = ch
	return ch
}

// HubOpened records the call and returns a channel of hub events that
// HubEvent feeds.
func (f *Fleet) HubOpened() <-chan hub.Event {
	f.record(Call{Name: "HubOpened"})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hub = make(chan hub.Event, 64)
	return f.hub
}

// --- the driver ---

type timer struct {
	at  time.Time
	seq int
	msg tea.Msg
}

type await struct {
	reply <-chan proto.Msg
	wrap  func(proto.Msg) tea.Msg
}

// Start is the time of the fake clock when a Driver is made: Tuesday 17
// November 2026, 14:32:30 UTC.
var Start = time.Date(2026, 11, 17, 14, 32, 30, 0, time.UTC)

// Driver runs a client.Model synchronously on a fake clock.
type Driver struct {
	t      *testing.T
	M      *client.Model
	F      *Fleet
	now    time.Time
	timers []timer
	seq    int
	awaits []await
	quit   bool
	raw    []string
	w, h   int
	depth  int
}

// New makes a model with opts and a window of 80 by 24. A nil opts.Fleet
// becomes a new fake Fleet.
func New(t *testing.T, opts client.Options) *Driver {
	t.Helper()
	d := &Driver{t: t, now: Start}
	if opts.Fleet == nil {
		opts.Fleet = NewFleet()
	}
	d.F, _ = opts.Fleet.(*Fleet)
	if opts.Env == nil {
		opts.Env = []string{"LANG=C.UTF-8"}
	}
	m, err := client.NewModel(opts, client.Hooks{
		Now: func() time.Time { return d.now },
		After: func(dur time.Duration, msg tea.Msg) tea.Cmd {
			return func() tea.Msg {
				d.seq++
				d.timers = append(d.timers, timer{at: d.now.Add(dur), seq: d.seq, msg: msg})
				return nil
			}
		},
		Await: func(reply <-chan proto.Msg, wrap func(proto.Msg) tea.Msg) tea.Cmd {
			return func() tea.Msg {
				d.awaits = append(d.awaits, await{reply: reply, wrap: wrap})
				return nil
			}
		},
	})
	if err != nil {
		t.Fatalf("clienttest: NewModel: %v", err)
	}
	d.M = m
	d.run(m.Init())
	d.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	return d
}

// Now is the time of the fake clock.
func (d *Driver) Now() time.Time { return d.now }

// Quit reports whether the model ended the program.
func (d *Driver) Quit() bool { return d.quit }

// Raw returns what the model printed past the renderer (tea.Raw), in order.
func (d *Driver) Raw() []string { return slices.Clone(d.raw) }

// Send hands msg to the model and runs what it starts.
func (d *Driver) Send(msg tea.Msg) {
	d.t.Helper()
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		d.w, d.h = ws.Width, ws.Height
	}
	_, cmd := d.M.Update(msg)
	d.run(cmd)
	d.pump()
}

// run follows the commands, feeding their messages back in, as the
// program loop would.
func (d *Driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	d.depth++
	defer func() { d.depth-- }()
	if d.depth > 1000 {
		d.t.Fatal("clienttest: the commands do not end")
	}
	msg := cmd()
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
	case tea.QuitMsg:
		d.quit = true
	case tea.RawMsg:
		d.raw = append(d.raw, toString(msg.Msg))
	default:
		_, next := d.M.Update(msg)
		d.run(next)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// pump delivers every reply that has arrived.
func (d *Driver) pump() {
	for {
		delivered := false
		for i := 0; i < len(d.awaits); i++ {
			a := d.awaits[i]
			select {
			case m := <-a.reply:
				d.awaits = append(d.awaits[:i], d.awaits[i+1:]...)
				_, cmd := d.M.Update(a.wrap(m))
				d.run(cmd)
				delivered = true
			default:
				continue
			}
			break
		}
		if !delivered {
			return
		}
	}
}

// Key presses one chord: "a", "?", "enter", "ctrl+space", "shift+tab",
// "ctrl+h", "alt+b", "f5". A single character is the key and its text
// (an upper-case letter is shift and the letter).
func (d *Driver) Key(chord string) {
	d.t.Helper()
	d.Send(tea.KeyPressMsg(ParseKey(d.t, chord)))
}

// Release releases one chord.
func (d *Driver) Release(chord string) {
	d.t.Helper()
	d.Send(tea.KeyReleaseMsg(ParseKey(d.t, chord)))
}

// Type presses one key per character of text.
func (d *Driver) Type(text string) {
	d.t.Helper()
	for _, r := range text {
		d.Send(tea.KeyPressMsg(charKey(r)))
	}
}

// Update is one update of the fleet.
func (d *Driver) Update(u fleet.Update) {
	d.t.Helper()
	d.Send(client.UpdatesMsg{u})
}

// Reply answers the request with m's id and delivers the reply.
func (d *Driver) Reply(m proto.Msg) {
	d.t.Helper()
	if d.F == nil || !d.F.Answer(m) {
		d.t.Fatalf("clienttest: no request with id %d waits for %s", m.ID, m.T)
	}
	d.pump()
}

// Advance moves the fake clock by dur and delivers the timers that come
// due, in order.
func (d *Driver) Advance(dur time.Duration) {
	d.t.Helper()
	end := d.now.Add(dur)
	for {
		i := -1
		for j, tm := range d.timers {
			if tm.at.After(end) {
				continue
			}
			if i < 0 || tm.at.Before(d.timers[i].at) || (tm.at.Equal(d.timers[i].at) && tm.seq < d.timers[i].seq) {
				i = j
			}
		}
		if i < 0 {
			break
		}
		tm := d.timers[i]
		d.timers = append(d.timers[:i], d.timers[i+1:]...)
		if tm.at.After(d.now) {
			d.now = tm.at
		}
		d.Send(tm.msg)
	}
	d.now = end
}

// Screen is what the model shows now.
func (d *Driver) Screen() golden.Screen {
	v := d.M.View()
	s := golden.Screen{Cols: d.w, Rows: d.h, Lines: strings.Split(v.Content, "\n")}
	if v.Cursor != nil {
		s.CursorX, s.CursorY, s.CursorVisible = v.Cursor.X, v.Cursor.Y, true
	}
	return s
}

// View is the tea.View the model returns now.
func (d *Driver) View() tea.View { return d.M.View() }

// keyNames are the named keys of Key.
var keyNames = map[string]rune{
	"enter": tea.KeyEnter, "tab": tea.KeyTab, "backspace": tea.KeyBackspace,
	"esc": tea.KeyEscape, "escape": tea.KeyEscape, "space": tea.KeySpace,
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"home": tea.KeyHome, "end": tea.KeyEnd, "pageup": tea.KeyPgUp, "pagedown": tea.KeyPgDown,
	"insert": tea.KeyInsert, "delete": tea.KeyDelete,
	"f1": tea.KeyF1, "f2": tea.KeyF2, "f3": tea.KeyF3, "f4": tea.KeyF4, "f5": tea.KeyF5, "f6": tea.KeyF6,
	"f7": tea.KeyF7, "f8": tea.KeyF8, "f9": tea.KeyF9, "f10": tea.KeyF10, "f11": tea.KeyF11, "f12": tea.KeyF12,
}

var modNames = map[string]tea.KeyMod{
	"ctrl": tea.ModCtrl, "alt": tea.ModAlt, "shift": tea.ModShift, "super": tea.ModSuper,
	"hyper": tea.ModHyper, "meta": tea.ModMeta,
}

// ParseKey is the bubbletea key of a chord as Key reads it.
func ParseKey(t *testing.T, chord string) tea.Key {
	t.Helper()
	key, mods := chord, []string(nil)
	if i := strings.LastIndex(chord[:len(chord)-1], "+"); i >= 0 {
		key, mods = chord[i+1:], strings.Split(chord[:i], "+")
	}
	var mod tea.KeyMod
	for _, p := range mods {
		bit, ok := modNames[p]
		if !ok {
			t.Fatalf("clienttest: unknown modifier %q in %q", p, chord)
		}
		mod |= bit
	}
	if code, ok := keyNames[key]; ok {
		k := tea.Key{Code: code, Mod: mod}
		if code == tea.KeySpace && mod&^tea.ModShift == 0 {
			k.Text = " "
		}
		return k
	}
	rs := []rune(key)
	if len(rs) != 1 {
		t.Fatalf("clienttest: unknown key %q in %q", key, chord)
	}
	k := charKey(rs[0])
	k.Mod |= mod
	if mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModHyper|tea.ModMeta) != 0 {
		k.Text = "" // as bubbletea reports ctrl+h and alt+b
	}
	return k
}

// charKey is the key that types r.
func charKey(r rune) tea.Key {
	if r >= 'A' && r <= 'Z' {
		return tea.Key{Code: r + 'a' - 'A', ShiftedCode: r, Mod: tea.ModShift, Text: string(r)}
	}
	return tea.Key{Code: r, Text: string(r)}
}
