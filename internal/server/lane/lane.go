// Package lane is the output side of one server connection (3.2.3, 5.3.6):
// an ordered control queue that is never dropped, and one frame slot per
// displayed pane whose frames are built by comparing rows with what this
// connection was last sent, paced to at most one frame per pane per
// FrameInterval.
//
// One goroutine, Run, is the only writer of the connection. Everything else
// (Send, Show, Dirty, Scroll, Hide, Close) only changes the lane's state under
// its mutex and wakes Run; none of them does I/O or calls a Source, so none
// of them waits for the peer or for a pane.
//
// The guarantees of 4.2.9 the lane carries:
//
//   - W4 and W8: control messages are written before any pending frame, and
//     Show queues its message and replaces the displayed set under one lock,
//     so a frame of the old set can precede the message on the wire but never
//     follow it. A frame that was being built while Show ran is discarded.
//   - W5: per pane and displayed set, frames have seq 1, 2, 3, ...; the first
//     is full. Intermediate screens are skipped at the source, never once
//     sent.
//   - W7: a control message is never dropped. When the queue would grow
//     above ControlMax, Send and Show return false, every later Send returns
//     false and Run ends with an error, so the connection is closed and
//     nothing is silently lost. A peer that accepts nothing for StallTimeout
//     ends Run the same way.
package lane

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"devtools/internal/pane"
	"devtools/internal/proto"
)

// Defaults of Options (3.2.3, 4.2.8, 5.3.6). A zero or negative field of
// Options is replaced by its default in New.
const (
	DefaultControlMax    = 8 << 20
	DefaultFrameInterval = 16 * time.Millisecond
	DefaultStallTimeout  = 30 * time.Second
	DefaultPingInterval  = 5 * time.Second
)

// writeChunk is the largest piece of a message handed to one Write call. A
// write that does not finish within StallTimeout means the peer accepted
// less than this in that time, which counts as accepting nothing.
const writeChunk = 64 << 10

// Errors Run returns besides the error of the connection.
var (
	errControlFull = errors.New("lane: control queue above its limit")
	errStalled     = errors.New("lane: the peer accepted nothing within the stall timeout")
)

// Source is what the frame builder reads from; *pane.Pane satisfies it.
type Source interface {
	Snapshot(top int64, scrolled bool) pane.Snap
}

// Options configure a lane.
type Options struct {
	ControlMax                                int // bytes; 8 MiB
	FrameInterval, StallTimeout, PingInterval time.Duration
}

// Lane is the output of one connection: control queue, frame slots, wake
// channel.
type Lane struct {
	o Options

	mu        sync.Mutex
	ctrl      [][]byte // framed control messages, oldest first
	ctrlBytes int
	broken    error // sticky: the queue overflowed, or Run ended with an error
	slots     map[string]*slot
	order     []string // the pane ids of slots, sorted, for a stable choice

	running  bool
	conn     net.Conn  // set while Run runs
	deadline time.Time // the write deadline last set on conn
	done     chan struct{}

	closed     bool
	closeUntil time.Time // the end of the drain

	wake chan struct{} // capacity 1; a pending wake is enough
}

// slot is one displayed pane. pane and src never change; dirty, top,
// scrolled and last are guarded by Lane.mu; seq and sent belong to Run, which
// writes them under Lane.mu in the commit and reads them without it. Show
// never changes a slot: it replaces the set with new ones.
type slot struct {
	pane     string
	src      Source
	dirty    bool
	top      int64
	scrolled bool
	last     time.Time // when the last frame was committed; zero before the first

	seq  uint64
	sent sent
}

// sent is what this connection was last sent for a pane.
type sent struct {
	cols, rows  int
	lines       []string
	cursor      proto.Cursor
	modes       proto.Modes
	top         int64
	scrolled    bool
	first, base int64
}

// New returns a lane with the defaults filled in for zero fields of o.
func New(o Options) *Lane {
	if o.ControlMax <= 0 {
		o.ControlMax = DefaultControlMax
	}
	if o.FrameInterval <= 0 {
		o.FrameInterval = DefaultFrameInterval
	}
	if o.StallTimeout <= 0 {
		o.StallTimeout = DefaultStallTimeout
	}
	if o.PingInterval <= 0 {
		o.PingInterval = DefaultPingInterval
	}
	return &Lane{
		o:     o,
		slots: map[string]*slot{},
		wake:  make(chan struct{}, 1),
	}
}

func (l *Lane) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// frame encodes m as it goes on the wire.
func frame(m proto.Msg) ([]byte, error) {
	var b bytes.Buffer
	if err := proto.WriteMsg(&b, m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// queue appends m to the control queue; l.mu is held. False when the lane is
// broken or m does not fit, which breaks it.
func (l *Lane) queue(m proto.Msg) bool {
	if l.broken != nil {
		return false
	}
	b, err := frame(m)
	if err != nil {
		l.broken = fmt.Errorf("lane: control message %s: %w", m.T, err)
		return false
	}
	if l.ctrlBytes+len(b) > l.o.ControlMax {
		l.broken = errControlFull
		return false
	}
	l.ctrl = append(l.ctrl, b)
	l.ctrlBytes += len(b)
	return true
}

// Send queues a control message: ordered, never dropped. False: the queue is
// full (or the lane already failed), the connection must be closed (W7).
func (l *Lane) Send(m proto.Msg) bool {
	l.mu.Lock()
	ok := l.queue(m)
	l.mu.Unlock()
	l.signal()
	return ok
}

// Show queues m (when not nil) and replaces the displayed set in one step, so
// no frame of the old set can follow m (W4, W8). Every pane starts again at
// seq 1, full. False as for Send; the displayed set is then left as it was.
func (l *Lane) Show(m *proto.Msg, panes map[string]Source) bool {
	l.mu.Lock()
	if m != nil && !l.queue(*m) {
		l.mu.Unlock()
		l.signal()
		return false
	}
	l.slots = make(map[string]*slot, len(panes))
	l.order = l.order[:0]
	for id, src := range panes {
		if src == nil {
			continue
		}
		l.slots[id] = &slot{pane: id, src: src, dirty: true}
		l.order = append(l.order, id)
	}
	sort.Strings(l.order)
	l.mu.Unlock()
	l.signal()
	return true
}

// Hide empties the displayed set: no frame is built until the next Show.
func (l *Lane) Hide() {
	l.Show(nil, nil)
}

// Dirty marks a displayed pane's output as changed; called from Pane.Watch.
// It never blocks: it takes the lane's mutex, which nobody holds across I/O
// or a Snapshot, and wakes Run without waiting. A pane that is not displayed
// is ignored.
func (l *Lane) Dirty(pane string) {
	l.mu.Lock()
	if s := l.slots[pane]; s != nil {
		s.dirty = true
	}
	l.mu.Unlock()
	l.signal()
}

// Scroll sets the connection's scroll position in a displayed pane: rows from
// absolute line top on while scrolled, the live screen otherwise. The next
// frame reflects it. A pane that is not displayed is ignored.
func (l *Lane) Scroll(pane string, top int64, scrolled bool) {
	l.mu.Lock()
	if s := l.slots[pane]; s != nil {
		s.top, s.scrolled, s.dirty = top, scrolled, true
	}
	l.mu.Unlock()
	l.signal()
}

// Close writes what is queued on the control lane, for at most drain, then
// stops Run, which returns nil. Pending frames are not written. It returns
// when Run has ended or drain has passed. Close is idempotent; a Close before
// Run makes Run drain for what remains of drain and return.
func (l *Lane) Close(drain time.Duration) {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		l.closeUntil = time.Now().Add(drain)
	}
	until := l.closeUntil
	if l.conn != nil && (l.deadline.IsZero() || until.Before(l.deadline)) {
		l.deadline = until
		l.conn.SetWriteDeadline(until)
	}
	done := l.done
	l.mu.Unlock()
	l.signal()
	if done == nil {
		return
	}
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// Run writes to w until the lane is closed or w fails. It is the only writer
// of w and does not close it. It returns nil after Close, the error of w, or
// an error when the control queue overflowed or the peer stalled.
func (l *Lane) Run(w net.Conn) (err error) {
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return errors.New("lane: Run called twice")
	}
	l.running = true
	l.conn = w
	l.done = make(chan struct{})
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.conn = nil
		if err != nil && l.broken == nil {
			l.broken = err
		}
		close(l.done)
		l.mu.Unlock()
	}()

	r := runner{l: l, w: w, lastWrite: time.Now()}
	return r.loop()
}

// runner is the state of Run.
type runner struct {
	l         *Lane
	w         net.Conn
	lastWrite time.Time
	timer     *time.Timer
}

func (r *runner) loop() error {
	l := r.l
	defer func() {
		if r.timer != nil {
			r.timer.Stop()
		}
	}()
	for {
		l.mu.Lock()
		if l.broken != nil {
			err := l.broken
			l.mu.Unlock()
			return err
		}
		if len(l.ctrl) > 0 {
			l.mu.Unlock()
			if done, err := r.flushControl(); done || err != nil {
				return err
			}
			continue
		}
		if l.closed {
			l.mu.Unlock()
			return nil
		}
		now := time.Now()
		s, next := l.due(now)
		var top int64
		var scrolled bool
		if s != nil {
			s.dirty = false
			top, scrolled = s.top, s.scrolled
		}
		l.mu.Unlock()

		if s != nil {
			// The Source may block (up to SyncHold for a real pane), so it is
			// read without the lock; Show may replace the slot meanwhile.
			snap := s.src.Snapshot(top, scrolled)
			pieces, st, err := build(s, snap)
			if err != nil {
				return err
			}
			if pieces == nil {
				continue // nothing changed since the last frame
			}
			if done, err := r.commit(s, st, pieces); done || err != nil {
				return err
			}
			continue
		}

		ping := r.lastWrite.Add(l.o.PingInterval)
		if !now.Before(ping) {
			if err := r.write(pingFrame); err != nil {
				return r.ended(err)
			}
			continue
		}
		wait := ping.Sub(now)
		if !next.IsZero() && next.Sub(now) < wait {
			wait = next.Sub(now)
		}
		r.sleep(wait)
	}
}

// sleep waits for a wake or for d.
func (r *runner) sleep(d time.Duration) {
	if r.timer == nil {
		r.timer = time.NewTimer(d)
	} else {
		r.timer.Reset(d)
	}
	select {
	case <-r.l.wake:
		r.timer.Stop()
	case <-r.timer.C:
	}
}

// due returns the dirty slot whose frame may be sent now, the one that has
// waited longest, or nil and the earliest time one may. l.mu is held.
func (l *Lane) due(now time.Time) (*slot, time.Time) {
	var best *slot
	var next time.Time
	for _, id := range l.order {
		s := l.slots[id]
		if !s.dirty {
			continue
		}
		at := s.last.Add(l.o.FrameInterval)
		if s.last.IsZero() || !now.Before(at) {
			if best == nil || s.last.Before(best.last) {
				best = s
			}
			continue
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return best, next
}

// flushControl writes the control queue in order until it is empty. done is
// true when the lane was closed and the drain is over.
func (r *runner) flushControl() (done bool, err error) {
	l := r.l
	for {
		l.mu.Lock()
		if len(l.ctrl) == 0 {
			l.mu.Unlock()
			return false, nil
		}
		b := l.ctrl[0]
		l.mu.Unlock()
		if err := r.write(b); err != nil {
			return true, r.ended(err)
		}
		l.mu.Lock()
		l.ctrl[0] = nil
		l.ctrl = l.ctrl[1:]
		l.ctrlBytes -= len(b)
		l.mu.Unlock()
	}
}

// commit writes the pieces of a frame built for s, unless Show replaced s
// meanwhile. Control messages queued meanwhile are written first (W8).
func (r *runner) commit(s *slot, st sent, pieces [][]byte) (done bool, err error) {
	l := r.l
	for {
		l.mu.Lock()
		if l.broken != nil {
			err := l.broken
			l.mu.Unlock()
			return true, err
		}
		if l.slots[s.pane] != s {
			l.mu.Unlock()
			return false, nil // the displayed set changed: the frame is stale
		}
		if len(l.ctrl) > 0 {
			l.mu.Unlock()
			if done, err := r.flushControl(); done || err != nil {
				return done, err
			}
			continue
		}
		if l.closed {
			l.mu.Unlock()
			return true, nil
		}
		s.seq++
		s.sent = st
		s.last = time.Now()
		l.mu.Unlock()
		break
	}
	for _, p := range pieces {
		if err := r.write(p); err != nil {
			return true, r.ended(err)
		}
	}
	return false, nil
}

// ended turns a write error into what Run returns: nil when the lane was
// closed (the drain ran out), else the error.
func (r *runner) ended(err error) error {
	r.l.mu.Lock()
	closed := r.l.closed
	r.l.mu.Unlock()
	if closed {
		return nil
	}
	return err
}

// write writes b in chunks, each bounded by the stall timeout and, once the
// lane is closed, by the end of the drain.
func (r *runner) write(b []byte) error {
	l := r.l
	for len(b) > 0 {
		n := min(len(b), writeChunk)
		l.mu.Lock()
		dl := time.Now().Add(l.o.StallTimeout)
		if l.closed && l.closeUntil.Before(dl) {
			dl = l.closeUntil
		}
		l.deadline = dl
		r.w.SetWriteDeadline(dl)
		l.mu.Unlock()
		m, err := r.w.Write(b[:n])
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("%w: %v", errStalled, err)
			}
			return err
		}
		b = b[m:]
		r.lastWrite = time.Now()
	}
	return nil
}

var pingFrame = func() []byte {
	b, err := frame(proto.Encode(proto.Ping{}))
	if err != nil {
		panic(err)
	}
	return b
}()
