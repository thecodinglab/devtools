package cli

// The scripted devtools server of the tests of internal/cli: a copy of the
// fake of internal/backend (fakeserver_test.go there), because a test file
// of one package cannot be imported by another. The brief of P2-08b names
// a scripted prototest.Server; that one accepts one connection at a time
// from the test goroutine and fails the test from Accept, while a command
// of these tests opens several connections from its own goroutine (the
// liveness check, the availability check, the request), so the commands
// are served by this one instead.

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/testkit"
)

// fakeServer is a scripted devtools server on a unix socket in a short
// temporary directory. It records every hello and every request and answers
// hello, state.get, ui.hub, ui.switch, session.open, session.close and
// pane.focus from its fields. The fields are set before the code under test
// connects.
type fakeServer struct {
	t      *testing.T
	socket string
	ln     net.Listener

	mu       sync.Mutex
	snap     proto.Snapshot
	noClient bool            // ui.hub and ui.switch answer err no-client
	refuse   string          // a hello with a pane is refused with this code
	moved    bool            // pane.focus answers moved
	silent   bool            // accept, read, never answer
	garbage  bool            // answer the hello with bytes that are no message
	hangup   map[string]bool // close the connection instead of answering these types
	hellos   []proto.Hello
	requests []string // "type {body}" of every request, in order
	conns    []net.Conn
}

func newFakeServer(t *testing.T, snap proto.Snapshot) *fakeServer {
	t.Helper()
	socket := filepath.Join(testkit.ShortTempDir(t), "server.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, socket: socket, ln: ln, snap: snap, hangup: map[string]bool{}}
	go f.serve()
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		for _, c := range f.conns {
			c.Close()
		}
		f.mu.Unlock()
	})
	return f
}

func (f *fakeServer) set(fn func(f *fakeServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeServer) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, conn)
		f.mu.Unlock()
		go f.handle(conn)
	}
}

func (f *fakeServer) handle(conn net.Conn) {
	defer conn.Close()
	msg, err := proto.ReadMsg(conn)
	if err != nil || msg.T != proto.TypeHello {
		return
	}
	var hello proto.Hello
	msg.Decode(&hello)
	f.mu.Lock()
	f.hellos = append(f.hellos, hello)
	silent, garbage, refuse := f.silent, f.garbage, f.refuse
	f.mu.Unlock()
	switch {
	case silent:
		// hold the connection until the client gives up
		buf := make([]byte, 512)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	case garbage:
		conn.Write([]byte("\x00\x00\x00\x05hello"))
		return
	case refuse != "" && hello.Pane != "":
		proto.WriteMsg(conn, proto.Encode(proto.Refuse{Code: refuse, Text: hello.Pane + " is not the pane of this process"}))
		return
	}
	proto.WriteMsg(conn, proto.Encode(proto.Welcome{Gen: proto.Gen, Version: "fake", Boot: "b1", Host: "fakehost", Pid: 1, Client: "c1"}))
	for {
		msg, err := proto.ReadMsg(conn)
		if err != nil {
			return
		}
		if msg.T == proto.TypePing {
			proto.WriteMsg(conn, proto.Encode(proto.Pong{}))
			continue
		}
		if msg.ID == 0 {
			continue
		}
		f.mu.Lock()
		f.requests = append(f.requests, msg.T+" "+bodyFields(msg))
		hang := f.hangup[msg.T]
		f.mu.Unlock()
		if hang {
			return
		}
		proto.WriteMsg(conn, f.answer(msg))
	}
}

// answer is the reply to one request.
func (f *fakeServer) answer(msg proto.Msg) proto.Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch msg.T {
	case proto.TypeStateGet:
		return proto.Request(msg.ID, f.snap)
	case proto.TypeUIHub, proto.TypeUISwitch:
		if f.noClient {
			return proto.ErrReply(msg.ID, proto.CodeNoClient, "no window displays the pane")
		}
		return proto.OKReply(msg.ID, nil)
	case proto.TypeSessionOpen:
		return proto.OKReply(msg.ID, proto.SessionOpenResult{Session: "s9", Created: true})
	case proto.TypeSessionClose:
		return proto.OKReply(msg.ID, proto.SessionCloseResult{Closed: true})
	case proto.TypePaneFocus:
		var pf proto.PaneFocus
		msg.Decode(&pf)
		return proto.OKReply(msg.ID, proto.PaneFocusResult{Moved: f.moved, Pane: pf.Pane})
	}
	return proto.ErrReply(msg.ID, proto.CodeUnsupported, "unsupported: "+msg.T)
}

// bodyFields is the body of msg without "t" and "id", compact.
func bodyFields(msg proto.Msg) string {
	var m map[string]any
	if err := json.Unmarshal(msg.Body, &m); err != nil {
		return string(msg.Body)
	}
	delete(m, "t")
	delete(m, "id")
	b, _ := json.Marshal(m)
	return string(b)
}

// Requests returns the requests so far: "type {fields}".
func (f *fakeServer) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// Types returns the types of the requests so far.
func (f *fakeServer) Types() []string {
	var out []string
	for _, r := range f.Requests() {
		out = append(out, strings.SplitN(r, " ", 2)[0])
	}
	return out
}

// Hellos returns the hellos so far.
func (f *fakeServer) Hellos() []proto.Hello {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Hello(nil), f.hellos...)
}

// waitRequests waits until n requests arrived.
func (f *fakeServer) waitRequests(t *testing.T, n int) []string {
	t.Helper()
	testkit.Eventually(t, 5*time.Second, "the requests reach the fake server", func() bool {
		return len(f.Requests()) >= n
	})
	return f.Requests()
}

// refusedSocket is a path where a socket file exists and nothing listens:
// a connection is refused.
func refusedSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testkit.ShortTempDir(t), "gone.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	return path
}

// errAs is errors.As for a test that only wants the bool.
func errAs[T error](err error) (T, bool) {
	var target T
	ok := errors.As(err, &target)
	return target, ok
}
