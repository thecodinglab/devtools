package remote

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"devtools/internal/proto"
)

// errEnded is what Send returns once the link is over.
var errEnded = errors.New("remote: the link is over")

// link is the Link of both transports: a reader and a writer goroutine on
// one byte stream, a bounded send queue, and a watchdog that pings and
// notices silence (4.2.8).
type link struct {
	clk     clock
	welcome proto.Welcome
	r       io.Reader
	w       io.Writer
	abort   func()               // closes the stream; idempotent
	lost    func(error) *Failure // why a read or write error ended the link
	first   []proto.Msg          // delivered before anything read from r

	queue chan proto.Msg
	recv  chan proto.Msg
	done  chan struct{}

	endOnce sync.Once
	mu      sync.Mutex
	err     *Failure

	lastRecv   atomic.Int64 // clock time in nanoseconds
	lastSend   atomic.Int64
	delivering atomic.Bool // the reader waits for the consumer of Recv
}

// newLink starts the three goroutines of a handshaken stream.
func newLink(clk clock, w proto.Welcome, r io.Reader, wr io.Writer, abort func(), lost func(error) *Failure, first ...proto.Msg) *link {
	l := &link{
		clk: clk, welcome: w, r: r, w: wr, abort: abort, lost: lost, first: first,
		queue: make(chan proto.Msg, sendQueue),
		recv:  make(chan proto.Msg, 16),
		done:  make(chan struct{}),
	}
	now := clk.Now().UnixNano()
	l.lastRecv.Store(now)
	l.lastSend.Store(now)
	go l.readLoop()
	go l.writeLoop()
	go l.watch()
	return l
}

func (l *link) Welcome() proto.Welcome { return l.welcome }
func (l *link) Recv() <-chan proto.Msg { return l.recv }

// Err is why the link ended, once Recv is closed: nil while it runs and
// nil when Close ended it.
func (l *link) Err() *Failure {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close ends the link: the connection is closed, an ssh process is killed.
func (l *link) Close() { l.end(nil) }

// Send queues m. It never blocks: a full queue of 256 messages means the
// peer does not read, and the link ends.
func (l *link) Send(m proto.Msg) error {
	if l.ended() {
		return errEnded
	}
	if len(m.Body) > proto.MaxMessage {
		l.end(failure(Protocol, "a message to send is over 4 MiB", ""))
		return errEnded
	}
	select {
	case l.queue <- m:
		l.lastSend.Store(l.clk.Now().UnixNano())
		return nil
	default:
		l.end(failure(Protocol, "the peer does not read: 256 messages are waiting", ""))
		return errEnded
	}
}

func (l *link) ended() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// end ends the link once, with f as its reason (nil: closed by us).
func (l *link) end(f *Failure) {
	l.endOnce.Do(func() {
		l.mu.Lock()
		l.err = f
		l.mu.Unlock()
		close(l.done)
		l.abort()
	})
}

// fail ends the link with the reason of err, unless it is over already.
func (l *link) fail(err error) {
	if l.ended() {
		return
	}
	l.end(l.lost(err))
}

func (l *link) readLoop() {
	defer close(l.recv)
	for _, m := range l.first {
		if !l.deliver(m) {
			return
		}
	}
	for {
		m, err := proto.ReadMsg(l.r)
		if err != nil {
			l.fail(err)
			return
		}
		l.lastRecv.Store(l.clk.Now().UnixNano())
		switch m.T {
		case proto.TypePing:
			l.Send(proto.Encode(proto.Pong{}))
			continue
		case proto.TypePong:
			continue
		}
		if !l.deliver(m) {
			return
		}
	}
}

// deliver hands m to the consumer of Recv. Time spent waiting for the
// consumer is not silence of the peer.
func (l *link) deliver(m proto.Msg) bool {
	l.delivering.Store(true)
	defer func() {
		l.delivering.Store(false)
		l.lastRecv.Store(l.clk.Now().UnixNano())
	}()
	select {
	case l.recv <- m:
		return true
	case <-l.done:
		return false
	}
}

func (l *link) writeLoop() {
	for {
		select {
		case m := <-l.queue:
			if err := proto.WriteMsg(l.w, m); err != nil {
				l.fail(err)
				return
			}
		case <-l.done:
			return
		}
	}
}

// watch sends a ping when nothing was sent for 5 s and ends the link when
// nothing arrived for 10 s (4.2.8).
func (l *link) watch() {
	for {
		now := l.clk.Now()
		recv := time.Unix(0, l.lastRecv.Load())
		if !l.delivering.Load() && now.Sub(recv) >= silenceLimit {
			l.end(failure(Protocol, "no message for 10 s", ""))
			return
		}
		if now.Sub(time.Unix(0, l.lastSend.Load())) >= pingEvery {
			if l.Send(proto.Encode(proto.Ping{})) != nil {
				return
			}
		}
		next := earlier(recv.Add(silenceLimit), time.Unix(0, l.lastSend.Load()).Add(pingEvery)).Sub(now)
		if next <= 0 {
			// The consumer holds the reader; look again after a ping interval.
			next = pingEvery
		}
		select {
		case <-l.clk.After(next):
		case <-l.done:
			return
		}
	}
}

// earlier returns the earlier of a and b.
func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
