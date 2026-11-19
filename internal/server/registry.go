package server

import (
	"strconv"
	"time"

	"devtools/internal/proto"
)

// registry is the set of connections that passed the handshake, owned by
// the loop (3.2.3).
type registry struct {
	next  uint64           // the number of the last connection id issued
	conns map[string]*conn // by id
	order []*conn          // by id number, the oldest first
}

func (r *registry) init() { r.conns = map[string]*conn{} }

// issue returns a new connection id: "c" and a number (4.2.1).
func (r *registry) issue() string {
	r.next++
	return "c" + strconv.FormatUint(r.next, 10)
}

func (r *registry) add(cn *conn) {
	r.conns[cn.id] = cn
	r.order = append(r.order, cn)
}

func (r *registry) remove(cn *conn) bool {
	if r.conns[cn.id] != cn {
		return false
	}
	delete(r.conns, cn.id)
	for i, x := range r.order {
		if x == cn {
			r.order = append(r.order[:i:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// list returns the connections, the oldest first. The slice is the
// caller's.
func (r *registry) list() []*conn { return append([]*conn(nil), r.order...) }

// interactive counts the interactive connections.
func (r *registry) interactive() int {
	n := 0
	for _, cn := range r.order {
		if cn.kind == proto.KindInteractive {
			n++
		}
	}
	return n
}

// drop removes a connection from the registry and closes it: what is queued
// on its control lane is written for a short drain on a goroutine of its
// own, then the socket is closed, which ends its reader. Every line it
// followed is let go; its reader finds no view any more. Idempotent.
func (c *core) drop(cn *conn) {
	if !c.reg.remove(cn) {
		return
	}
	cn.gone = true
	cn.viewp.Store(nil)
	c.leave(cn)
	cn.view = ""
	c.s.mu.Lock()
	delete(c.s.conns, cn)
	c.s.mu.Unlock()
	go func() {
		cn.lane.Close(dropDrain)
		cn.nc.Close()
	}()
}

// dropDrain is how long a dropped connection's queued control messages may
// take to be written.
const dropDrain = 100 * time.Millisecond

// dropAll drops every connection; for halt.
func (c *core) dropAll() {
	for _, cn := range c.reg.list() {
		c.drop(cn)
	}
}

// send queues m on the control lane of cn; a full queue closes the
// connection (W7).
func (c *core) send(cn *conn, m proto.Msg) {
	if cn.gone {
		return
	}
	if !cn.lane.Send(m) {
		c.s.log.Warn("connection closed: its control queue is full or it stalled", "conn", cn.id)
		c.drop(cn)
	}
}
