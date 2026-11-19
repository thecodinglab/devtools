package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"devtools/internal/proto"
	"devtools/internal/session"
)

// NewDialer returns the Dialer of the local socket (machine "") and of
// "ssh -T <machine> devtools bridge" (4.4).
func NewDialer(opts DialOptions) Dialer {
	return &dialer{opts: opts, clk: realClock{}}
}

type dialer struct {
	opts DialOptions
	clk  clock
}

// Dial opens one link: it connects, says hello, and returns once welcome
// and, on an interactive connection, the first state.snapshot arrived; that
// snapshot is the first message of Recv. An attempt that is not online
// after 30 s, or whose ctx ends, is killed and fails as unreachable.
func (d *dialer) Dial(ctx context.Context, machine string) (Link, *Failure) {
	if machine == "" {
		return d.dialLocal(ctx)
	}
	return d.dialSSH(ctx, machine)
}

// attempt bounds one dial by the attempt limit and ctx: when either ends
// first, abort is called and the attempt's failure is the timeout.
type attempt struct {
	mu      sync.Mutex
	settled bool
	failed  *Failure
	stop    chan struct{}
}

func (d *dialer) startAttempt(ctx context.Context, abort func()) *attempt {
	a := &attempt{stop: make(chan struct{})}
	limit := d.clk.After(attemptLimit)
	go func() {
		var f *Failure
		select {
		case <-limit:
			f = failure(Unreachable, "no answer within 30 s", "")
		case <-ctx.Done():
			f = failure(Unreachable, "the attempt was cancelled", "")
		case <-a.stop:
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if !a.settled {
			a.failed = f
			abort()
		}
	}()
	return a
}

// settle ends the attempt. It returns the timeout's failure when the limit
// was first; then f, the failure the attempt itself saw, is a consequence
// of the abort.
func (a *attempt) settle(f *Failure) *Failure {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.settled {
		a.settled = true
		close(a.stop)
	}
	if a.failed != nil {
		return a.failed
	}
	return f
}

func (d *dialer) dialLocal(ctx context.Context) (Link, *Failure) {
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "unix", d.opts.Socket)
	if err != nil {
		return nil, failure(NoServer, "no devtools server answers at "+d.opts.Socket+": "+rootCause(err), "")
	}
	a := d.startAttempt(ctx, func() { conn.Close() })
	lost := func(err error) *Failure {
		if f := streamFailure(err); f != nil {
			return f
		}
		return failure(NoServer, "the devtools server closed the connection", "")
	}
	w, first, f := handshake(conn, conn, d.opts.Hello, lost)
	if f = a.settle(f); f != nil {
		conn.Close()
		return nil, f
	}
	return newLink(d.clk, w, conn, conn, func() { conn.Close() }, lost, first...), nil
}

func (d *dialer) dialSSH(ctx context.Context, machine string) (Link, *Failure) {
	// Machine names come from the config, which checks them (SEC4); this is
	// the second line of defence before anything runs.
	if !session.ValidMachine(machine) {
		return nil, failure(Protocol, "not a machine name: "+machine, "")
	}
	p, err := startSSH(d.opts.SSH, machine)
	if err != nil {
		return nil, failure(Unreachable, "ssh could not be started: "+err.Error(), "")
	}
	a := d.startAttempt(ctx, p.kill)
	r := bufio.NewReaderSize(p.stdout, 64<<10)

	kind, line, f := readMarker(r)
	switch kind {
	case markerEnd:
		f = p.classify("")
	case markerError:
		f = p.classify(line)
	}
	if f != nil {
		f = a.settle(f)
		p.kill()
		return nil, f
	}

	lost := func(err error) *Failure {
		if f := streamFailure(err); f != nil {
			return f
		}
		// The stream ended: ssh failed (exit 255) or the bridge ended.
		if code := p.wait(2 * time.Second); code == 255 {
			return p.classify("")
		}
		return failure(Unreachable, "the connection to "+machine+" ended", "")
	}
	hello := d.opts.Hello
	hello.Remote = true
	// The server ignores these on a remote hello (4.2.1); they describe
	// this machine and do not leave it.
	hello.TTY, hello.Ancestors, hello.Env, hello.Pane = "", nil, nil, ""
	w, first, f := handshake(r, p.stdin, hello, lost)
	if f = a.settle(f); f != nil {
		p.kill()
		return nil, f
	}
	l := newLink(d.clk, w, r, p.stdin, p.kill, lost, first...)
	go func() {
		select {
		case <-p.exited:
			// What the bridge wrote before it ended is still in the pipe;
			// the reader ends the link at its end. Only a pipe that
			// something else holds open ends it here.
			select {
			case <-time.After(time.Second):
				l.fail(io.EOF)
			case <-l.done:
			}
		case <-l.done:
		}
	}()
	return l, nil
}

// handshake sends hello and reads welcome and, for an interactive hello,
// the first state.snapshot (4.2.1, W3).
func handshake(r io.Reader, w io.Writer, hello proto.Hello, lost func(error) *Failure) (proto.Welcome, []proto.Msg, *Failure) {
	if hello.Gen == 0 {
		hello.Gen = proto.Gen
	}
	if err := proto.WriteMsg(w, proto.Encode(hello)); err != nil {
		return proto.Welcome{}, nil, lost(err)
	}
	m, err := readSkippingPings(r)
	if err != nil {
		return proto.Welcome{}, nil, lost(err)
	}
	var welcome proto.Welcome
	switch m.T {
	case proto.TypeWelcome:
		if err := m.Decode(&welcome); err != nil {
			return proto.Welcome{}, nil, failure(Protocol, "invalid welcome", "")
		}
		if welcome.Gen != proto.Gen {
			return proto.Welcome{}, nil, failure(Incompatible,
				fmt.Sprintf("the server speaks protocol %d, this client %d", welcome.Gen, proto.Gen), "")
		}
	case proto.TypeRefuse:
		var ref proto.Refuse
		m.Decode(&ref)
		if ref.Code == proto.CodeGeneration {
			return proto.Welcome{}, nil, failure(Incompatible, ref.Text, "the server speaks another protocol")
		}
		return proto.Welcome{}, nil, failure(Protocol, "refused ("+ref.Code+"): "+ref.Text, "")
	default:
		return proto.Welcome{}, nil, failure(Protocol, "the server answered hello with "+m.T, "")
	}
	if hello.Kind != proto.KindInteractive {
		return welcome, nil, nil
	}
	snap, err := readSkippingPings(r)
	if err != nil {
		return proto.Welcome{}, nil, lost(err)
	}
	if snap.T != proto.TypeStateSnapshot {
		return proto.Welcome{}, nil, failure(Protocol, "the server sent "+snap.T+" before state.snapshot", "")
	}
	return welcome, []proto.Msg{snap}, nil
}

func readSkippingPings(r io.Reader) (proto.Msg, error) {
	for {
		m, err := proto.ReadMsg(r)
		if err != nil || (m.T != proto.TypePing && m.T != proto.TypePong) {
			return m, err
		}
	}
}

// streamFailure classifies an error of the byte stream that is the peer's
// fault, and returns nil for the end of the stream.
func streamFailure(err error) *Failure {
	switch {
	case errors.Is(err, proto.ErrTooLarge):
		return failure(Protocol, "a message over 4 MiB", "")
	case errors.Is(err, proto.ErrMalformed):
		return failure(Protocol, "invalid message: "+err.Error(), "")
	}
	return nil
}

// rootCause is the innermost text of a net error, "connection refused"
// rather than "dial unix /path: connect: connection refused".
func rootCause(err error) string {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err.Error()
		}
		err = u
	}
}
