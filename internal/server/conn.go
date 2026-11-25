package server

import (
	"net"
	"sync/atomic"
	"time"

	"devtools/internal/proto"
	"devtools/internal/server/lane"
	"devtools/internal/vt"
)

// conn is one connection after its handshake. id, kind, remote, pane, hello,
// nc and lane never change; viewp is replaced by the loop and read by the
// reader; pasting belongs to the reader; the other fields belong to the
// loop. The reader goroutine reads and decodes; lane.Run is the only writer
// of nc.
type conn struct {
	id     string
	kind   string // proto.KindInteractive, proto.KindOneshot
	remote bool
	pane   string // hello.pane of a one-shot, accepted
	hello  proto.Hello
	nc     net.Conn
	lane   *lane.Lane

	view      string    // the displayed session, "" without a view
	lastInput time.Time // the last interaction (3.2.3)
	interact  uint64    // the order of the last interaction, for the size owner
	focused   bool      // the window has focus: from the hello until input.focus says otherwise
	gone      bool      // dropped from the registry

	pending    bool               // view.set is between its steps 3 and 4 (view.go)
	shownTab   string             // the tab of the lane's displayed set
	shown      map[string]*handle // the lane's displayed set
	cols, rows int                // the pane area of the window
	colors     vt.Colors          // the colours of the outer terminal

	viewp   atomic.Pointer[viewState] // the view as the reader sees it
	pasting map[string]bool           // panes with a paste open (the reader's)
	// pasteOpen is len(pasting) > 0, published by the reader for the
	// upgrade's gate and idle watch, which must not read the map.
	pasteOpen atomic.Bool
}

var _ Conn = (*conn)(nil)

func (cn *conn) ID() string           { return cn.id }
func (cn *conn) Kind() string         { return cn.kind }
func (cn *conn) Remote() bool         { return cn.remote }
func (cn *conn) Pane() string         { return cn.pane }
func (cn *conn) LastInput() time.Time { return cn.lastInput }
func (cn *conn) Focused() bool        { return cn.focused }

func (cn *conn) View() (string, bool) { return cn.view, cn.view != "" }

// Has says whether the hello claimed the wire capability cap (4.1). A
// sender never requires a peer to understand what it did not claim.
func (cn *conn) Has(cap string) bool {
	for _, c := range cn.hello.Caps {
		if c == cap {
			return true
		}
	}
	return false
}

// Send queues m on the control lane; a full queue closes the connection.
// On the loop, like every method of Conn.
func (cn *conn) Send(m proto.Msg) {
	if cn.gone || cn.lane.Send(m) {
		return
	}
	cn.lane.Close(0)
	cn.nc.Close() // the reader sees it and drops the connection on the loop
}

// run serves an admitted connection: lane.Run writes, this goroutine reads
// until the connection ends, then the loop drops it.
func (s *Server) run(cn *conn) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := cn.lane.Run(cn.nc); err != nil {
			s.log.Info("connection write ended", "conn", cn.id, "err", err)
		}
		cn.nc.Close()
	}()
	s.read(cn)
	cn.nc.Close()
	s.post(func(c *core) { c.drop(cn) })
}

// read reads messages until the connection fails. A peer that sends nothing
// for two ping intervals is closed (4.2.8); the time spent handing input to
// a pane that does not read is not counted, because the deadline is set
// again before every read.
func (s *Server) read(cn *conn) {
	idle := 2 * s.o.PingInterval
	for {
		cn.nc.SetReadDeadline(time.Now().Add(idle))
		m, err := proto.ReadMsg(cn.nc)
		if err != nil {
			return
		}
		if m.T == proto.TypeServerUpgradeAck {
			s.upgradeAcked(cn, m) // the loop may be holding for the handover
			continue
		}
		if s.inReader(cn, m) {
			continue
		}
		s.post(func(c *core) {
			if !cn.gone {
				c.handle(cn, m)
			}
		})
	}
}
