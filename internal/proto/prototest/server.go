package prototest

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"devtools/internal/proto"
)

// Server is a scripted fake for tests of code that talks to a server. It
// listens on a socket in a new short directory under /tmp; the test accepts
// each connection and answers it by hand.
type Server struct {
	t      *testing.T
	dir    string
	socket string
	ln     *net.UnixListener

	mu    sync.Mutex
	peers []*Peer
}

// NewServer listens on a temporary socket until the test ends.
func NewServer(t *testing.T) *Server {
	t.Helper()
	// t.TempDir is too long for a unix socket on macOS (104 bytes).
	dir, err := os.MkdirTemp("/tmp", "dt-")
	if err != nil {
		t.Fatalf("prototest: %v", err)
	}
	socket := filepath.Join(dir, "server.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("prototest: listen on %s: %v", socket, err)
	}
	s := &Server{t: t, dir: dir, socket: socket, ln: ln}
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		peers := s.peers
		s.mu.Unlock()
		for _, p := range peers {
			p.Close()
		}
		os.RemoveAll(dir)
	})
	return s
}

// Socket returns the path of the socket.
func (s *Server) Socket() string { return s.socket }

// Accept waits for the next connection and its hello and returns the peer.
// Nothing is answered yet: the test sends welcome (Peer.Welcome) or refuse.
// A connection that does not send hello as its first message fails the
// test.
func (s *Server) Accept() *Peer {
	s.t.Helper()
	s.ln.SetDeadline(time.Now().Add(Timeout))
	conn, err := s.ln.Accept()
	if err != nil {
		s.t.Fatalf("prototest: accept: %v", err)
	}
	p := &Peer{t: s.t, conn: conn}
	s.mu.Lock()
	s.peers = append(s.peers, p)
	s.mu.Unlock()
	m := p.Next()
	if m.T != proto.TypeHello {
		s.t.Fatalf("prototest: the first message is %s, not hello", m.T)
	}
	if err := m.Decode(&p.Hello); err != nil {
		s.t.Fatalf("prototest: %v", err)
	}
	return p
}

// Peer is one accepted connection of a Server, driven by the test.
type Peer struct {
	t     *testing.T
	conn  net.Conn
	Hello proto.Hello

	wmu  sync.Mutex
	once sync.Once
}

// Conn returns the connection, for tests of writes that block.
func (p *Peer) Conn() net.Conn { return p.conn }

// Close closes the connection.
func (p *Peer) Close() { p.once.Do(func() { p.conn.Close() }) }

// Send sends one of the message structs as a notification, or a proto.Msg
// as it is.
func (p *Peer) Send(m any) {
	p.t.Helper()
	var msg proto.Msg
	switch v := m.(type) {
	case proto.Msg:
		msg = v
	case proto.Typed:
		msg = proto.Encode(v)
	default:
		p.t.Fatalf("prototest: %T is not a message", m)
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.conn.SetWriteDeadline(time.Now().Add(Timeout))
	if err := proto.WriteMsg(p.conn, msg); err != nil {
		p.t.Fatalf("prototest: send %s: %v", msg.T, err)
	}
}

// Welcome answers the hello with a welcome and, on an interactive
// connection, with snap as the state.snapshot that follows it (W3).
func (p *Peer) Welcome(snap proto.Snapshot) proto.Welcome {
	p.t.Helper()
	w := proto.Welcome{
		Gen: proto.Gen, Version: Version, Caps: p.Hello.Caps,
		Boot: "b0", Host: "prototest", Pid: os.Getpid(), Client: "c1", Clients: 1,
	}
	if p.Hello.Kind != proto.KindInteractive {
		w.Clients = 0
	}
	p.Send(w)
	if p.Hello.Kind == proto.KindInteractive {
		p.Send(snap)
	}
	return w
}

// Reply answers the request id with ok and the fields of result (nil for
// none).
func (p *Peer) Reply(id uint64, result any) {
	p.t.Helper()
	p.Send(proto.OKReply(id, result))
}

// Fail answers the request id with err.
func (p *Peer) Fail(id uint64, code, text string) {
	p.t.Helper()
	p.Send(proto.ErrReply(id, code, text))
}

// Next returns the next message of the client. A ping is answered with
// pong and, like a pong, is not returned. The end of the connection or
// Timeout without a message fails the test.
func (p *Peer) Next() proto.Msg {
	p.t.Helper()
	for {
		p.conn.SetReadDeadline(time.Now().Add(Timeout))
		m, err := proto.ReadMsg(p.conn)
		if err != nil {
			p.t.Fatalf("prototest: read from the client: %v", err)
		}
		switch m.T {
		case proto.TypePing:
			p.Send(proto.Pong{})
			continue
		case proto.TypePong:
			continue
		}
		return m
	}
}

// Expect returns the next message of the client, which must be of type t,
// decoded into v when v is not nil.
func (p *Peer) Expect(t string, v any) proto.Msg {
	p.t.Helper()
	m := p.Next()
	if m.T != t {
		p.t.Fatalf("prototest: the client sent %s (%s), want %s", m.T, m.Body, t)
	}
	if v != nil {
		if err := m.Decode(v); err != nil {
			p.t.Fatalf("prototest: %v", err)
		}
	}
	return m
}

// Ended waits until the client closes the connection; a message that
// arrives first fails the test.
func (p *Peer) Ended() {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(Timeout))
	for {
		m, err := proto.ReadMsg(p.conn)
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			p.t.Fatalf("prototest: the client did not close the connection within %v", Timeout)
		}
		if err != nil {
			return
		}
		if m.T == proto.TypePing || m.T == proto.TypePong {
			continue
		}
		p.t.Fatalf("prototest: the client sent %s, want the end of the connection", m.T)
	}
}
