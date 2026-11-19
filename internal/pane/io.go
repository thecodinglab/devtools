package pane

import (
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
func (p *Pane) write() {
	defer close(p.writerDone)
	q := &p.out
	broken := false
	for {
		q.mu.Lock()
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
		for _, b := range bufs {
			if !broken {
				if _, err := p.master.Write(b); err != nil {
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

// wait is the waiter: it waits until the child ended, reaps it under the
// lock that signal takes, and reports the exit once the reader saw the end
// of file or eofWait passed, so the last output is parsed before the exit
// is reported (3.2.1).
func (p *Pane) wait() {
	defer close(p.waiterDone)
	waitExited(p.pid)
	var ws unix.WaitStatus
	var err error
	p.procMu.Lock()
	for {
		_, err = unix.Wait4(p.pid, &ws, 0, nil)
		if err != unix.EINTR {
			break
		}
	}
	p.reaped = true
	byServer := p.closing.Load()
	p.procMu.Unlock()
	close(p.reapedc)

	ex := Exit{ByServer: byServer}
	switch {
	case err != nil:
		ex.Code, ex.Text = -1, "wait: "+err.Error()
	case ws.Signaled():
		ex.Code, ex.Signal = -1, unix.SignalName(syscall.Signal(ws.Signal()))
	default:
		ex.Code = ws.ExitStatus()
	}
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
