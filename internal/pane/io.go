package pane

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// read is the reader: it reads at most readSize bytes from the master, hands
// them to the actor and waits until they were parsed. While the actor is
// busy the kernel's PTY buffer fills and the program blocks in write:
// backpressure without a queue (3.2.1). Any error ends it; on Linux the end
// of file of a PTY master is EIO.
//
// While the pane is frozen for a live upgrade (handover.go) the read ends
// with the deadline Freeze set: the reader parks until Thaw, or ends for
// good when the pane is abandoned.
func (p *Pane) read() {
	defer close(p.readerDone)
	buf := make([]byte, readSize)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			p.chunks <- buf[:n]
			<-p.ack
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				if fz := p.freezing(); fz != nil {
					fz.parkReader()
					if fz.abandoned.Load() {
						return
					}
				}
				continue
			}
			return
		}
	}
}

// outQueue is the writer queue: the bytes the actor encoded from input and
// the terminal's replies, in order.
type outQueue struct {
	mu     sync.Mutex
	bufs   [][]byte
	size   int
	closed bool
	wake   chan struct{}
	fz     *freezeState // set while frozen: the writer parks (handover.go)
}

func (q *outQueue) init() { q.wake = make(chan struct{}, 1) }

// put appends b; replies are put even while the queue is full.
func (q *outQueue) put(b []byte) {
	if len(b) == 0 {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.bufs = append(q.bufs, b)
	q.size += len(b)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// full reports whether the queue holds maxQueue bytes or more.
func (q *outQueue) full() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size >= maxQueue
}

// shut ends the writer; what is still queued is dropped.
func (q *outQueue) shut() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// write is the writer: it writes the queue to the master in order. A
// partial write continues where it stopped and EAGAIN and EINTR are retried
// (os.File does both on a polled descriptor). After a failed write the rest
// is dropped, so a pane whose terminal is gone never holds its callers.
//
// While the pane is frozen for a live upgrade the writer parks: Freeze sets
// a write deadline, a write it interrupts returns how much it wrote, and
// the rest of that buffer and of the batch go back to the front of the
// queue, so the queue holds exactly the bytes the program has not got, in
// order (handover.go).
func (p *Pane) write() {
	defer close(p.writerDone)
	q := &p.out
	broken := false
	for {
		q.mu.Lock()
		if fz := q.fz; fz != nil && !q.closed {
			q.mu.Unlock()
			fz.parkWriter()
			if fz.abandoned.Load() {
				return
			}
			continue
		}
		bufs, closed := q.bufs, q.closed
		q.bufs = nil
		q.mu.Unlock()
		if closed {
			return
		}
		if len(bufs) == 0 {
			<-q.wake
			continue
		}
		for i, b := range bufs {
			if !broken {
				n, err := p.master.Write(b)
				if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
					// Only Freeze sets a deadline, and it is no failure of
					// the terminal: the rest goes back to the front of the
					// queue, whether the freeze still holds (the writer
					// parks) or a Thaw already ended it (the writer goes
					// on, the deadline is gone).
					q.unwrite(n, append([][]byte{b[n:]}, bufs[i+1:]...))
					if !q.frozen() {
						time.Sleep(time.Millisecond)
					}
					break
				}
				if err != nil {
					broken = true
				}
			}
			q.mu.Lock()
			q.size -= len(b)
			q.mu.Unlock()
			select {
			case p.room <- struct{}{}:
			default:
			}
		}
	}
}

// frozen reports whether Freeze stopped the writer.
func (q *outQueue) frozen() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.fz != nil
}

// unwrite puts back what a write interrupted by Freeze did not write, in
// front of the queue; written bytes of it leave the size.
func (q *outQueue) unwrite(written int, rest [][]byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.size -= written
	keep := make([][]byte, 0, len(rest)+len(q.bufs))
	for _, b := range rest {
		if len(b) > 0 {
			keep = append(keep, b)
		}
	}
	q.bufs = append(keep, q.bufs...)
}

// wait is the waiter: it waits until the child ended, reaps it under the
// lock that signal takes, and reports the exit once the reader saw the end
// of file or eofWait passed, so the last output is parsed before the exit
// is reported (3.2.1).
//
// The exit is computed under the lock and kept (waitStatus), so a freeze
// for a live upgrade, which holds the lock, knows it exactly. A pane
// abandoned after a live upgrade in the same process leaves the child to
// the pane that adopted it.
func (p *Pane) wait() {
	defer close(p.waiterDone)
	waitExited(p.pid)
	var ws unix.WaitStatus
	var err error
	p.procMu.Lock()
	if p.abandoned {
		p.procMu.Unlock()
		return
	}
	for {
		_, err = unix.Wait4(p.pid, &ws, 0, nil)
		if err != unix.EINTR {
			break
		}
	}
	p.reaped = true
	ex := Exit{ByServer: p.closing.Load()}
	switch {
	case err != nil:
		ex.Code, ex.Text = -1, "wait: "+err.Error()
	case ws.Signaled():
		ex.Code, ex.Signal = -1, unix.SignalName(syscall.Signal(ws.Signal()))
	default:
		ex.Code = ws.ExitStatus()
	}
	p.waitStatus = &ex
	p.procMu.Unlock()
	close(p.reapedc)
	p.reportExit(ex)
}

// reportExit hands the exit to the actor once the reader saw the end of
// file or eofWait passed.
func (p *Pane) reportExit(ex Exit) {
	t := time.NewTimer(eofWait)
	select {
	case <-p.readerDone:
	case <-t.C:
	}
	t.Stop()
	p.exitc <- ex
}

// opQueue is the actor's queue of everything that is not input or output:
// resizes, colours, Follow and Unfollow, snapshots and text. Push never
// blocks and the actor runs the operations in the order they were pushed.
type opQueue struct {
	mu     sync.Mutex
	ops    []func(*actor)
	signal chan struct{}
}

func (q *opQueue) init() { q.signal = make(chan struct{}, 1) }

func (q *opQueue) push(f func(*actor)) {
	q.mu.Lock()
	q.ops = append(q.ops, f)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *opQueue) take() []func(*actor) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ops := q.ops
	q.ops = nil
	return ops
}
