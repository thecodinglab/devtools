package pane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"devtools/internal/platform"
	"devtools/internal/vt"
)

// The live upgrade of a pane (internal/handover). The old server image
// freezes the pane: the reader parks, the writer stops at an exact cut, the
// actor takes no more input and encodes the terminal, and the waiter cannot
// reap the child, so the frozen state is exactly true until the exec or
// until Thaw. The new image adopts the PTY master and the child in two
// steps: StageAdopt builds the pane object and restores the terminal
// without reading, writing or reaping anything, and Start runs it.

// Kinds of Frozen.Kind.
const (
	FrozenRunning = "running" // the program runs (or ended and is not reaped)
	FrozenReaped  = "reaped"  // the program was reaped, its exit is not parsed yet
	FrozenExited  = "exited"  // the pane is exited; the terminal is not handed over
)

// Frozen is what Freeze returns: the state of the pane at the cut.
type Frozen struct {
	Kind        string
	Pid         int
	Info        Info // a copy; Exit is the exit of a reaped or exited pane
	Cols, Rows  int
	First, Base int64
	Screen      []byte // the terminal's snapshot (vt.Encode)
	Pending     []byte // the bytes the program has not got yet, in order
	// Master is the PTY master of a running or reaped pane, nil for an
	// exited one. It stays the pane's: the caller duplicates it.
	Master *os.File
}

// freezeState is one Freeze until Thaw or Abandon.
type freezeState struct {
	readerOnce, writerOnce sync.Once
	readerParked           chan struct{} // closed when the reader parked
	writerParked           chan struct{} // closed when the writer parked
	resume                 chan struct{} // closed by Thaw or Abandon
	abandoned              atomic.Bool
}

func (fz *freezeState) parkReader() {
	fz.readerOnce.Do(func() { close(fz.readerParked) })
	<-fz.resume
}

func (fz *freezeState) parkWriter() {
	fz.writerOnce.Do(func() { close(fz.writerParked) })
	<-fz.resume
}

func (p *Pane) freezing() *freezeState {
	p.fzMu.Lock()
	defer p.fzMu.Unlock()
	return p.fz
}

// ErrFrozen is the error of a second Freeze.
var ErrFrozen = errors.New("pane: frozen already")

// Freeze stops the pane at a cut for a live upgrade and returns its state.
// Until Thaw or Abandon no output is read, no input is written or taken, no
// resize reaches the terminal and the child is not reaped. When ctx ends
// before the cut is made Freeze undoes what it did and fails.
func (p *Pane) Freeze(ctx context.Context) (Frozen, error) {
	p.fzMu.Lock()
	if p.fz != nil {
		p.fzMu.Unlock()
		return Frozen{}, ErrFrozen
	}
	fz := &freezeState{readerParked: make(chan struct{}), writerParked: make(chan struct{}), resume: make(chan struct{})}
	p.fz = fz
	p.fzMu.Unlock()

	if p.master != nil {
		p.procMu.Lock()
		if err := p.master.SetReadDeadline(time.Unix(1, 0)); err != nil {
			p.thaw(fz, true)
			return Frozen{}, fmt.Errorf("pane %s: the terminal is not polled: %w", p.id, err)
		}
		if err := waitFor(ctx, fz.readerParked, p.readerDone); err != nil {
			p.thaw(fz, true)
			return Frozen{}, fmt.Errorf("pane %s: the reader did not stop: %w", p.id, err)
		}
		p.out.mu.Lock()
		p.out.fz = fz
		p.out.mu.Unlock()
		select {
		case p.out.wake <- struct{}{}:
		default:
		}
		p.master.SetWriteDeadline(time.Unix(1, 0))
		if err := waitFor(ctx, fz.writerParked, p.writerDone); err != nil {
			p.thaw(fz, true)
			return Frozen{}, fmt.Errorf("pane %s: the writer did not stop: %w", p.id, err)
		}
	}

	type result struct {
		f   Frozen
		err error
	}
	reply := make(chan result, 1)
	p.ops.push(func(a *actor) {
		a.frozen = true
		var r result
		r.f.Screen, r.err = vt.Encode(a.term)
		r.f.Cols, r.f.Rows = a.term.Size()
		r.f.First, r.f.Base = a.term.Range()
		reply <- r
	})
	var r result
	select {
	case r = <-reply:
	case <-p.actorDone:
		r.err = errors.New("the pane is closed")
	case <-ctx.Done():
		r.err = ctx.Err()
		// The op may still run: Thaw's op runs after it.
	}
	if r.err != nil {
		p.thaw(fz, p.master != nil)
		return Frozen{}, fmt.Errorf("pane %s: %w", p.id, r.err)
	}
	f := r.f
	f.Pid = p.pid
	f.Info = p.Info()
	p.out.mu.Lock()
	for _, b := range p.out.bufs {
		f.Pending = append(f.Pending, b...)
	}
	p.out.mu.Unlock()
	switch {
	case p.master == nil || f.Info.State == "exited":
		f.Kind = FrozenExited
	case p.reaped:
		f.Kind, f.Master = FrozenReaped, p.master
		ex := *p.waitStatus
		f.Info.Exit = &ex
	default:
		f.Kind, f.Master = FrozenRunning, p.master
	}
	return f, nil
}

func waitFor(ctx context.Context, a, b <-chan struct{}) error {
	select {
	case <-a:
		return nil
	case <-b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Thaw undoes Freeze: the pane goes on where it stopped, the writer with
// the bytes it had not written, a resize that waited is applied.
func (p *Pane) Thaw() {
	fz := p.freezing()
	if fz == nil {
		return
	}
	p.thaw(fz, p.master != nil)
}

func (p *Pane) thaw(fz *freezeState, locked bool) {
	p.ops.push(func(a *actor) {
		a.frozen = false
		if a.resizeLater {
			a.resizeLater = false
			a.resize()
		}
	})
	if p.master != nil {
		p.master.SetWriteDeadline(time.Time{})
		p.master.SetReadDeadline(time.Time{})
	}
	p.out.mu.Lock()
	p.out.fz = nil
	p.out.mu.Unlock()
	p.fzMu.Lock()
	p.fz = nil
	p.fzMu.Unlock()
	close(fz.resume)
	select {
	case p.out.wake <- struct{}{}:
	default:
	}
	if locked {
		p.procMu.Unlock()
	}
}

// Abandon ends a frozen pane whose program another pane object adopted in
// this process (the tests of the live upgrade, where both server images
// live in one process): its goroutines end without reading, writing,
// signalling or reaping anything, and its descriptor of the master is
// closed (the adopting pane holds its own). Close returns at once after
// it.
func (p *Pane) Abandon() {
	fz := p.freezing()
	if fz == nil {
		return
	}
	fz.abandoned.Store(true)
	if p.master != nil {
		p.abandoned = true // under procMu, which Freeze holds
		p.procMu.Unlock()
	}
	close(fz.resume)
	p.closeOnce.Do(func() {
		defer close(p.closed)
		if p.master != nil {
			<-p.readerDone
			<-p.writerDone
			p.master.Close()
		}
		close(p.quit)
		<-p.actorDone
		p.mu.Lock()
		p.shut = true
		for id, w := range p.watchers {
			delete(p.watchers, id)
			close(w.quit)
		}
		p.mu.Unlock()
	})
}

// Adoption is a pane of the old image as the new image adopts it.
type Adoption struct {
	Kind        string   // FrozenRunning or FrozenReaped
	Master      *os.File // the PTY master, non-blocking
	Pid         int
	Info        Info // State, Title, Cwd, Pid, Shell, Fg, TTY; Exit of a reaped pane
	Screen      []byte
	First, Base int64
	Pending     []byte
}

// Staged is an adopted pane that does not run yet.
type Staged struct {
	p      *Pane
	kind   string
	exit   *Exit
	notify bool
}

// Pane is the staged pane object; it serves nothing before Start.
func (s *Staged) Pane() *Pane { return s.p }

// StageAdopt builds the pane object of an adoption: the master is checked
// (pollable, the PTY of the TTY the old image named), the terminal is
// restored and the writer's queue holds the pending bytes. Nothing is read,
// written, signalled or reaped until Start; a failure leaves the master
// and the child as they were.
func StageAdopt(o Options, a Adoption) (*Staged, error) {
	if a.Master == nil || a.Pid <= 0 {
		return nil, errors.New("pane: an adoption without a terminal or a program")
	}
	if a.Kind != FrozenRunning && a.Kind != FrozenReaped {
		return nil, fmt.Errorf("pane: an adoption of kind %q", a.Kind)
	}
	if a.Kind == FrozenReaped && a.Info.Exit == nil {
		return nil, errors.New("pane: a reaped adoption without its exit")
	}
	if err := a.Master.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("pane %s: the adopted terminal is not polled: %w", o.ID, err)
	}
	if name, err := platform.PTYPeerName(a.Master); err != nil || name != a.Info.TTY {
		return nil, fmt.Errorf("pane %s: the adopted terminal is %q (%v), the old server named %q", o.ID, name, err, a.Info.TTY)
	}
	p := newPane(o)
	p.master, p.pid = a.Master, a.Pid
	p.info = a.Info
	p.info.State = "running"
	p.info.Exit = nil
	if err := p.restoreActor(a.Screen, a.First, a.Base); err != nil {
		return nil, err
	}
	p.a.notifiedFg = p.info.Fg
	if len(a.Pending) > 0 {
		b := append([]byte(nil), a.Pending...)
		p.out.bufs = [][]byte{b}
		p.out.size = len(b)
	}
	s := &Staged{p: p, kind: a.Kind}
	if a.Kind == FrozenReaped {
		ex := *a.Info.Exit
		s.exit = &ex
	}
	return s, nil
}

func (p *Pane) restoreActor(screen []byte, first, base int64) error {
	a := &actor{p: p}
	t, err := vt.Restore(a.options(), screen, first, base)
	if err != nil {
		return fmt.Errorf("pane %s: %w", p.id, err)
	}
	a.term = t
	p.a = a
	p.mu.Lock()
	p.lastSize = p.size
	p.mu.Unlock()
	return nil
}

// Start runs the staged pane: its goroutines start, and the exit of a
// program the old image reaped is reported once the output it left was
// read, as for any program.
func (s *Staged) Start() *Pane {
	p := s.p
	if s.notify {
		p.runExited()
		return p
	}
	go p.a.run()
	go p.read()
	go p.write()
	if s.kind == FrozenReaped {
		p.procMu.Lock()
		p.reaped = true
		ex := *s.exit
		p.waitStatus = &ex
		p.procMu.Unlock()
		close(p.reapedc)
		go func() {
			defer close(p.waiterDone)
			p.reportExit(ex)
		}()
		return p
	}
	go p.wait()
	return p
}

// Restored is an exited pane of the old image.
type Restored struct {
	Info        Info // State "exited" and its Exit
	Screen      []byte
	First, Base int64
}

// StageRestore builds an exited pane with the old image's last screen and
// exit; it reports Exited once when it starts, so the server applies the
// rules of an exit that it may not have applied before the upgrade.
func StageRestore(o Options, r Restored) (*Staged, error) {
	if r.Info.Exit == nil {
		return nil, errors.New("pane: a restored exited pane without its exit")
	}
	p := newPane(o)
	p.info = r.Info
	p.info.State = "exited"
	ex := *r.Info.Exit
	p.info.Exit = &ex
	if err := p.restoreActor(r.Screen, r.First, r.Base); err != nil {
		return nil, err
	}
	return &Staged{p: p, notify: true}, nil
}

// Discard frees a staged pane that will not start: its terminal, and
// neither the master nor the child are touched.
func (s *Staged) Discard() {
	if s.p.a != nil && s.p.a.term != nil {
		s.p.a.term.Close()
	}
}

// WaitExited waits until the process pid ended, without reaping it: until
// the caller reaps it, its pid cannot be another process's. Boot's cold
// recovery waits for the old programs so.
func WaitExited(pid int) { waitExited(pid) }
