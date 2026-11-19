// Package prototest is a headless protocol client and a scripted fake
// server for tests (10.1.4 of the plan). Server tests drive a real server
// with Client and never import the client program; tests of code that talks
// to a server script its answers with Server.
//
// It imports internal/proto, internal/testkit/golden and the standard
// library only. It is a test-only package (import rule 8).
package prototest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"devtools/internal/proto"
	"devtools/internal/testkit/golden"
)

// Timeout is how long a Client or a Peer waits for a message, a reply or a
// state before it fails the test.
var Timeout = 10 * time.Second

// Version is the version a hello and a welcome of this package carry.
const Version = "0.0.0-prototest"

// Interactive returns the hello of an interactive client: generation 1,
// kind interactive, local, with the colours of the outer terminal set
// (#d0d0d0 on #000000, dark: the values a server assumes without them). It
// claims no wire capability; a test that needs one adds it to Caps. A view
// of 80x24 is what the tests ask for with View.
func Interactive() proto.Hello {
	return proto.Hello{
		Gen:     proto.Gen,
		Version: Version,
		Kind:    proto.KindInteractive,
		Colors:  &proto.Colors{Fg: "#d0d0d0", Bg: "#000000", Dark: true},
	}
}

// Oneshot returns the hello of a one-shot command; pane is DEVTOOLS_PANE of
// the caller, "" outside a pane. A hello with a pane carries ancestors, the
// caller's own pid first: here the pid of the test process and of its
// parent, because the standard library cannot walk the process table. A
// test that must pass the server's check (the pid of the pane's child is
// one of them) sets Ancestors itself.
func Oneshot(pane string) proto.Hello {
	h := proto.Hello{Gen: proto.Gen, Version: Version, Kind: proto.KindOneshot, Pane: pane}
	if pane != "" {
		h.Ancestors = []int{os.Getpid(), os.Getppid()}
	}
	return h
}

// DialRaw connects to the socket and returns the connection, for handshake
// and limit tests. It is closed when the test ends.
func DialRaw(t *testing.T, socket string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("unix", socket, Timeout)
	if err != nil {
		t.Fatalf("prototest: dial %s: %v", socket, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// paneScreen is the client's copy of one pane: the frames applied in order.
// The copy outlives the display of the pane, but its sequence does not:
// shown is true from the first complete frame after the pane became
// displayed on this connection (4.2.4 seq) until it stops being displayed
// (reset). While shown, the next frame must have seq+1, full or not (W5); a
// pane that is not shown must start with frame 1, full.
type paneScreen struct {
	shown   bool
	seq     uint64
	cols    int
	rows    int
	lines   []string
	cursor  proto.Cursor
	pending *paneScreen // pieces of a frame split at FramePiece, before the last
	frames  int
}

func (p *paneScreen) screen() golden.Screen {
	return golden.Screen{
		Cols: p.cols, Rows: p.rows,
		Lines:   append([]string(nil), p.lines...),
		CursorX: p.cursor.X, CursorY: p.cursor.Y, CursorVisible: p.cursor.Visible,
	}
}

func (p *paneScreen) clone() *paneScreen {
	c := *p
	c.lines = append([]string(nil), p.lines...)
	c.pending = nil
	return &c
}

// Client is one connection to a server, after the handshake. A goroutine
// reads every message: it applies the state and the frames, answers ping,
// and hands replies to the requests waiting for them. A protocol error of
// the server (a revision gap, a frame out of sequence, a malformed message)
// fails the test and closes the connection, as a real client would close
// it.
type Client struct {
	t    testing.TB
	conn net.Conn

	wmu      sync.Mutex // one writer at a time
	lastSend time.Time

	mu       sync.Mutex
	welcome  proto.Welcome
	state    *proto.State
	view     string // the session of the last view.ok, "" without a view
	panes    map[string]*paneScreen
	received []proto.Msg
	waiting  map[uint64]chan proto.Msg
	nextID   uint64
	closed   bool
	err      error
	changed  chan struct{} // closed and replaced on every message

	stopRead  chan struct{}
	stopOnce  sync.Once
	done      chan struct{} // the reader ended
	keepalive time.Duration
	quit      chan struct{}
	quitOnce  sync.Once
}

// Dial connects to the socket, sends hello and reads the answer. A refuse
// fails the test. On an interactive connection it also reads the
// state.snapshot that must follow welcome (W3). The connection is closed
// when the test ends.
func Dial(t *testing.T, socket string, hello proto.Hello) *Client {
	t.Helper()
	return dial(t, socket, hello)
}

func dial(t testing.TB, socket string, hello proto.Hello) *Client {
	t.Helper()
	conn, err := net.DialTimeout("unix", socket, Timeout)
	if err != nil {
		t.Fatalf("prototest: dial %s: %v", socket, err)
	}
	c := &Client{
		t:         t,
		conn:      conn,
		panes:     map[string]*paneScreen{},
		waiting:   map[uint64]chan proto.Msg{},
		changed:   make(chan struct{}),
		stopRead:  make(chan struct{}),
		done:      make(chan struct{}),
		keepalive: 5 * time.Second,
		quit:      make(chan struct{}),
	}
	t.Cleanup(c.Close)
	if err := c.write(proto.Encode(hello)); err != nil {
		t.Fatalf("prototest: send hello: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(Timeout))
	m, err := proto.ReadMsg(conn)
	if err != nil {
		t.Fatalf("prototest: read the answer to hello: %v", err)
	}
	c.received = append(c.received, m)
	switch m.T {
	case proto.TypeWelcome:
		if err := m.Decode(&c.welcome); err != nil {
			t.Fatalf("prototest: %v", err)
		}
	case proto.TypeRefuse:
		var r proto.Refuse
		m.Decode(&r)
		t.Fatalf("prototest: the server refused the hello: %s: %s", r.Code, r.Text)
	default:
		t.Fatalf("prototest: the answer to hello is %s, not welcome", m.T)
	}
	if hello.Kind == proto.KindInteractive {
		m, err := proto.ReadMsg(conn)
		if err != nil {
			t.Fatalf("prototest: read the snapshot after welcome: %v", err)
		}
		c.received = append(c.received, m)
		if m.T != proto.TypeStateSnapshot || m.ID != 0 {
			t.Fatalf("prototest: the message after welcome is %s (id %d), not state.snapshot (W3)", m.T, m.ID)
		}
		var s proto.Snapshot
		if err := m.Decode(&s); err != nil {
			t.Fatalf("prototest: %v", err)
		}
		c.state = proto.NewState(s)
	}
	conn.SetReadDeadline(time.Time{})
	go c.readLoop()
	go c.pingLoop()
	return c
}

// Close closes the connection and waits for the reader to end. It is
// called when the test ends; calling it earlier is allowed.
func (c *Client) Close() {
	c.quitOnce.Do(func() { close(c.quit) })
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.conn.Close()
	select {
	case <-c.done:
	case <-time.After(Timeout):
	}
}

// KeepAlive sets how long the client may send nothing before it sends a
// ping (4.2.8: 5 s). A test that shortened the server's intervals shortens
// this one too.
func (c *Client) KeepAlive(d time.Duration) {
	c.mu.Lock()
	c.keepalive = d
	c.mu.Unlock()
}

func (c *Client) pingLoop() {
	for {
		c.mu.Lock()
		d := c.keepalive
		c.mu.Unlock()
		tick := d / 4
		if d <= 0 {
			tick = time.Second // pings are off; look again for a new setting
		}
		select {
		case <-c.quit:
			return
		case <-c.stopRead:
			return
		case <-time.After(tick):
		}
		if d <= 0 {
			continue
		}
		c.wmu.Lock()
		idle := time.Since(c.lastSend)
		c.wmu.Unlock()
		if idle >= d {
			c.write(proto.Encode(proto.Ping{}))
		}
	}
}

func (c *Client) write(m proto.Msg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(Timeout))
	err := proto.WriteMsg(c.conn, m)
	c.lastSend = time.Now()
	return err
}

// fail reports a protocol error of the server and closes the connection.
func (c *Client) fail(format string, args ...any) {
	err := fmt.Errorf(format, args...)
	c.mu.Lock()
	already := c.closed
	if c.err == nil {
		c.err = err
	}
	c.closed = true
	c.mu.Unlock()
	if !already {
		c.t.Errorf("prototest: %v", err)
	}
	c.conn.Close()
}

func (c *Client) readLoop() {
	defer close(c.done)
	defer func() {
		c.mu.Lock()
		c.closed = true
		for id, ch := range c.waiting {
			close(ch)
			delete(c.waiting, id)
		}
		c.notify()
		c.mu.Unlock()
	}()
	for {
		select {
		case <-c.stopRead:
			// Stalled on purpose: read nothing until the connection is closed.
			<-c.quit
			return
		default:
		}
		m, err := proto.ReadMsg(c.conn)
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				c.fail("read: %v", err)
			}
			return
		}
		if !c.handle(m) {
			return
		}
	}
}

// handle applies one message; false ends the connection.
func (c *Client) handle(m proto.Msg) bool {
	c.mu.Lock()
	c.received = append(c.received, m)
	c.mu.Unlock()
	if _, ok := proto.AsReply(m); ok && m.ID != 0 {
		c.mu.Lock()
		// The view changes before the requester or a later frame sees it.
		if err := c.applyView(m); err != nil {
			c.mu.Unlock()
			c.fail("%s: %v", m.T, err)
			return false
		}
		ch, waiting := c.waiting[m.ID]
		delete(c.waiting, m.ID)
		c.notify()
		c.mu.Unlock()
		if waiting {
			ch <- m
			return true
		}
		if m.T != proto.TypeStateSnapshot {
			c.fail("a reply %s with id %d that no request waits for", m.T, m.ID)
			return false
		}
		return true
	}
	var err error
	switch m.T {
	case proto.TypePing:
		c.write(proto.Encode(proto.Pong{}))
	case proto.TypeStateDelta:
		var d proto.Delta
		if err = m.Decode(&d); err == nil {
			c.mu.Lock()
			err = c.applyDelta(d)
			c.mu.Unlock()
		}
	case proto.TypeViewEnded:
		c.mu.Lock()
		err = c.applyView(m)
		c.mu.Unlock()
	case proto.TypeFrame:
		var f proto.Frame
		if err = m.Decode(&f); err == nil {
			c.mu.Lock()
			err = c.applyFrame(f)
			c.mu.Unlock()
		}
	}
	if err != nil {
		c.fail("%s: %v", m.T, err)
		return false
	}
	c.mu.Lock()
	c.notify()
	c.mu.Unlock()
	return true
}

// notify wakes every waiter; c.mu is held.
func (c *Client) notify() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// applyFrame applies one frame or piece (4.2.4, W5); c.mu is held.
func (c *Client) applyFrame(f proto.Frame) error {
	p := c.panes[f.Pane]
	if p == nil {
		p = &paneScreen{}
		c.panes[f.Pane] = p
	}
	target := p.pending
	switch {
	case target != nil:
		// A further piece of the frame in progress.
		if f.Seq != target.seq {
			return fmt.Errorf("pane %s: piece of frame %d after a piece of frame %d", f.Pane, f.Seq, target.seq)
		}
		if f.Cols != target.cols || f.Rows != target.rows {
			return fmt.Errorf("pane %s: the pieces of frame %d have different sizes", f.Pane, f.Seq)
		}
	case !p.shown:
		// The first frame since the pane became displayed (4.2.4, W5).
		if f.Seq != 1 {
			return fmt.Errorf("pane %s: the first frame since it became displayed has seq %d, not 1", f.Pane, f.Seq)
		}
		if !f.Full {
			return fmt.Errorf("pane %s: frame 1 is not full", f.Pane)
		}
		target = p.clone()
	default:
		// Within one display the sequence only goes on: a full frame with
		// seq 1 is a restart the protocol does not allow here.
		if f.Seq != p.seq+1 {
			return fmt.Errorf("pane %s: frame seq %d after %d", f.Pane, f.Seq, p.seq)
		}
		if !f.Full && (f.Cols != p.cols || f.Rows != p.rows) {
			return fmt.Errorf("pane %s: frame %d changes the size to %dx%d and is not full", f.Pane, f.Seq, f.Cols, f.Rows)
		}
		target = p.clone()
	}
	if f.Full && p.pending == nil {
		target.lines = make([]string, f.Rows)
	}
	if len(target.lines) != f.Rows {
		lines := make([]string, f.Rows)
		copy(lines, target.lines)
		target.lines = lines
	}
	target.seq, target.cols, target.rows = f.Seq, f.Cols, f.Rows
	for _, l := range f.Lines {
		if l.Y < 0 || l.Y >= f.Rows {
			return fmt.Errorf("pane %s: frame %d names row %d of %d", f.Pane, f.Seq, l.Y, f.Rows)
		}
		target.lines[l.Y] = l.S
	}
	target.cursor = f.Cursor
	if f.More {
		p.pending = target
		return nil
	}
	target.frames = p.frames + 1
	target.pending = nil
	target.shown = true
	c.panes[f.Pane] = target
	return nil
}

// reset ends the display of the panes, every pane when none is named: the
// next frame of each must be frame 1, full. A frame split at FramePiece and
// not finished is dropped with it. c.mu is held.
func (c *Client) reset(panes ...string) {
	if len(panes) == 0 {
		for id := range c.panes {
			panes = append(panes, id)
		}
	}
	for _, id := range panes {
		if p := c.panes[id]; p != nil {
			p.shown, p.pending = false, nil
		}
	}
}

// displayed returns the current tab of the viewed session and its panes, as
// the state names them; "" and nil without a view or a known tab. c.mu is
// held.
func (c *Client) displayed() (string, []string) {
	if c.view == "" || c.state == nil {
		return "", nil
	}
	tab := c.state.Sessions[c.view].Current
	if tab == "" {
		return "", nil
	}
	return tab, c.state.Tabs[tab].Panes
}

// applyDelta applies a state delta and ends the display of every pane when
// the connection's displayed set changes (4.2.3, 4.2.4): the viewed
// session's current tab changed, or the panes of that tab did (a split, a
// closed pane). The server replaces the displayed set in one step, so every
// pane of the new set, a pane that stays included, starts again at seq 1,
// full (5.3.6 Lane.Show). c.mu is held.
func (c *Client) applyDelta(d proto.Delta) error {
	if c.state == nil {
		return errors.New("a state.delta before any snapshot")
	}
	tab, panes := c.displayed()
	panes = slices.Clone(panes)
	if err := c.state.Apply(d); err != nil {
		return err
	}
	newTab, newPanes := c.displayed()
	if newTab != tab || !slices.Equal(newPanes, panes) {
		c.reset()
	}
	return nil
}

// applyView handles view.ok and view.ended: the connection displays a new
// view or none, so every pane's sequence ends (4.2.3, 5.3.6 Lane.Show).
// view.clear, which the client sends, ends nothing here: frames the server
// sent before it read view.clear may still arrive and go on with their
// sequence, and no frame starts again before the view.ok of the next
// view.set. c.mu is held.
func (c *Client) applyView(m proto.Msg) error {
	switch m.T {
	case proto.TypeViewOK:
		var ok proto.ViewOK
		if err := m.Decode(&ok); err != nil {
			return err
		}
		c.view = ok.Session
	case proto.TypeViewEnded:
		c.view = ""
	default:
		return nil
	}
	c.reset()
	return nil
}

// Welcome returns the server's welcome.
func (c *Client) Welcome() proto.Welcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.welcome
}

// State returns the snapshot with all deltas applied so far (a copy).
func (c *Client) State() proto.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyState(c.state)
}

func copyState(s *proto.State) proto.State {
	if s == nil {
		return proto.State{}
	}
	out := proto.State{
		Rev:      s.Rev,
		Sessions: make(map[string]proto.Session, len(s.Sessions)),
		Tabs:     make(map[string]proto.Tab, len(s.Tabs)),
		Panes:    make(map[string]proto.Pane, len(s.Panes)),
		Agents:   make(map[string]proto.Agent, len(s.Agents)),
	}
	for k, v := range s.Sessions {
		out.Sessions[k] = v
	}
	for k, v := range s.Tabs {
		out.Tabs[k] = v
	}
	for k, v := range s.Panes {
		out.Panes[k] = v
	}
	for k, v := range s.Agents {
		out.Agents[k] = v
	}
	return out
}

// Screen returns the pane as the frames applied so far draw it; a pane
// without a frame is the zero Screen.
func (c *Client) Screen(pane string) golden.Screen {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.panes[pane]; p != nil {
		return p.screen()
	}
	return golden.Screen{}
}

// Frames returns how many complete frames of the pane were applied.
func (c *Client) Frames(pane string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.panes[pane]; p != nil {
		return p.frames
	}
	return 0
}

// wait waits until cond is true. cond is asked without the client's lock,
// so it may call every method of the client. It fails the test with
// describe() after Timeout or when the connection ended first.
func (c *Client) wait(what string, cond func() bool, describe func() string) {
	c.t.Helper()
	deadline := time.NewTimer(Timeout)
	defer deadline.Stop()
	for {
		// Take the channel before asking: a change after this closes it.
		c.mu.Lock()
		closed, err := c.closed, c.err
		ch := c.changed
		c.mu.Unlock()
		if cond() {
			return
		}
		if closed {
			c.t.Fatalf("prototest: the connection ended (%v) while waiting for %s; %s", err, what, describe())
		}
		select {
		case <-ch:
		case <-deadline.C:
			c.t.Fatalf("prototest: timed out after %v waiting for %s; %s", Timeout, what, describe())
		}
	}
}

// hasPane reports whether a frame of the pane arrived.
func (c *Client) hasPane(pane string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.panes[pane] != nil
}

// WaitScreen waits until pred holds for the pane's screen and returns that
// screen. what names the awaited screen in the failure, which shows the
// screen as it was.
func (c *Client) WaitScreen(pane, what string, pred func(golden.Screen) bool) golden.Screen {
	c.t.Helper()
	var s golden.Screen
	c.wait(fmt.Sprintf("pane %s: %s", pane, what), func() bool {
		if !c.hasPane(pane) {
			return false
		}
		s = c.Screen(pane)
		return pred(s)
	}, func() string { return "the screen:\n" + c.Screen(pane).Plain() })
	return s
}

// WaitFor waits until a row of the pane contains text.
func (c *Client) WaitFor(pane, text string) golden.Screen {
	c.t.Helper()
	return c.WaitScreen(pane, fmt.Sprintf("%q", text), func(s golden.Screen) bool { return s.Contains(text) })
}

// WaitState waits until pred holds for the state and returns it.
func (c *Client) WaitState(what string, pred func(proto.State) bool) proto.State {
	c.t.Helper()
	var s proto.State
	c.wait(what, func() bool {
		c.mu.Lock()
		have := c.state != nil
		c.mu.Unlock()
		if !have {
			return false
		}
		s = c.State()
		return pred(s)
	}, func() string { return fmt.Sprintf("the state is at revision %d", c.State().Rev) })
	return s
}

// WaitMessage waits for a message of type typ among the received ones, from
// index from of Received on, and returns the first.
func (c *Client) WaitMessage(typ string, from int) proto.Msg {
	c.t.Helper()
	var m proto.Msg
	c.wait("a message "+typ, func() bool {
		for i, r := range c.Received() {
			if i >= from && r.T == typ {
				m = r
				return true
			}
		}
		return false
	}, func() string { return "received: " + strings.Join(c.Types(), " ") })
	return m
}

// encode turns a message for Send and Request into a Msg: a proto.Msg as
// it is, or one of the message structs.
func (c *Client) encode(m any, id uint64) proto.Msg {
	c.t.Helper()
	switch v := m.(type) {
	case proto.Msg:
		if id != 0 {
			v.ID = id
			if len(v.Body) > 0 {
				c.t.Fatalf("prototest: Request of a proto.Msg with a body; give the message struct instead")
			}
		}
		return v
	case proto.Typed:
		if id != 0 {
			return proto.Request(id, v)
		}
		return proto.Encode(v)
	}
	c.t.Fatalf("prototest: %T is not a message", m)
	return proto.Msg{}
}

// Send sends a notification: one of the message structs, or a proto.Msg as
// it is.
func (c *Client) Send(m any) {
	c.t.Helper()
	msg := c.encode(m, 0)
	if err := c.write(msg); err != nil {
		c.t.Fatalf("prototest: send %s: %v", msg.T, err)
	}
}

// Request sends a request with the next id and waits for its reply: ok,
// err, or the named replies view.ok and state.snapshot.
func (c *Client) Request(m any) proto.Reply {
	c.t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan proto.Msg, 1)
	c.waiting[id] = ch
	c.mu.Unlock()
	msg := c.encode(m, id)
	if err := c.write(msg); err != nil {
		c.t.Fatalf("prototest: send %s: %v", msg.T, err)
	}
	select {
	case r, ok := <-ch:
		if !ok {
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			c.t.Fatalf("prototest: the connection ended (%v) before the reply to %s %d", err, msg.T, id)
		}
		reply, _ := proto.AsReply(r)
		return reply
	case <-time.After(Timeout):
		c.mu.Lock()
		delete(c.waiting, id)
		c.mu.Unlock()
		c.t.Fatalf("prototest: no reply to %s %d within %v", msg.T, id, Timeout)
	}
	return proto.Reply{}
}

// View sets the view and returns view.ok; an err reply fails the test.
func (c *Client) View(v proto.ViewSet) proto.ViewOK {
	c.t.Helper()
	r := c.Request(v)
	if !r.OK {
		c.t.Fatalf("prototest: view.set: %s: %s", r.Code, r.Text)
	}
	if r.Msg.T != proto.TypeViewOK {
		c.t.Fatalf("prototest: view.set answered %s, not view.ok", r.Msg.T)
	}
	var ok proto.ViewOK
	if err := r.Msg.Decode(&ok); err != nil {
		c.t.Fatalf("prototest: %v", err)
	}
	return ok
}

// Key sends one input.key.
func (c *Client) Key(pane, name string, mod int, text string) {
	c.t.Helper()
	c.Send(proto.Key{Pane: pane, Key: name, Mod: mod, Text: text})
}

// Type sends text as one input.key per rune: a printable rune as itself
// with its text (an upper-case letter as its lower-case key with shift),
// a space as "space", and newline, carriage return, tab, backspace, DEL
// and escape as their key names; another control character is ctrl and
// its letter.
func (c *Client) Type(pane, text string) {
	c.t.Helper()
	for _, r := range text {
		c.Send(KeyOf(pane, r))
	}
}

// KeyOf is the input.key Type sends for one rune.
func KeyOf(pane string, r rune) proto.Key {
	k := proto.Key{Pane: pane}
	switch {
	case r == '\r' || r == '\n':
		k.Key = "enter"
	case r == '\t':
		k.Key = "tab"
	case r == '\b' || r == 0x7f:
		k.Key = "backspace"
	case r == 0x1b:
		k.Key = "escape"
	case r == ' ':
		k.Key, k.Text = "space", " "
	case r < 0x20:
		k.Key, k.Mod = string(rune('a'+r-1)), proto.ModCtrl
		if r == 0 {
			k.Key = "space"
		}
	case unicode.IsUpper(r) && unicode.ToLower(r) != r:
		k.Key, k.Mod, k.Text, k.Shifted = string(unicode.ToLower(r)), proto.ModShift, string(r), string(r)
	default:
		k.Key, k.Text = string(r), string(r)
	}
	return k
}

// Received returns every message received, in order, the welcome first.
func (c *Client) Received() []proto.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]proto.Msg(nil), c.received...)
}

// Types returns the types of every message received, in order.
func (c *Client) Types() []string {
	var ts []string
	for _, m := range c.Received() {
		ts = append(ts, m.T)
	}
	return ts
}

// StopReading simulates a stalled client: from now on nothing more is read
// from the connection and no ping is sent, so the peer's writes back up
// once the socket's buffers are full. A message the reader is decoding at
// that moment is still applied.
func (c *Client) StopReading() {
	c.stopOnce.Do(func() { close(c.stopRead) })
}

// Closed reports whether the connection has ended.
func (c *Client) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Err returns the protocol error that ended the connection, nil if none.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// WaitClosed waits until the connection has ended. The condition takes the
// client's lock (Closed): wait asks it without the lock, while the reader
// sets closed under it.
func (c *Client) WaitClosed() {
	c.t.Helper()
	c.wait("the end of the connection", c.Closed, func() string { return "it is still open" })
}

// PaneIDs returns the panes of which frames arrived, sorted.
func (c *Client) PaneIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ids []string
	for id := range c.panes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
