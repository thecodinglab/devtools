package server

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"time"

	"devtools/internal/proto"
	"devtools/internal/server/lane"
	"devtools/internal/session"
)

// The handshake (4.2.1). The peer's uid was checked before the server saw
// the connection (boot's listener, SEC2). The first message must be hello
// and arrive within HelloTimeout; the checks that need the panes (3.4 d, the
// pane of a one-shot) run on the loop, which admits the connection, queues
// welcome and, for an interactive connection, the snapshot in the same step,
// so no delta can come between them (W3).

// refuseWrite bounds the write of a refuse to a peer that does not read.
const refuseWrite = time.Second

// helloDisplayVars are the variables of hello.env a pane spawned later
// inherits (3.2.1).
var helloDisplayVars = []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"}

// handshake reads the hello of a new connection and admits or refuses it;
// an admitted connection is then served until it ends.
func (s *Server) handshake(nc net.Conn) {
	nc.SetReadDeadline(time.Now().Add(s.o.HelloTimeout))
	m, err := proto.ReadMsg(nc)
	if err != nil {
		nc.Close() // nothing, or nothing valid, in time: closed without a message
		return
	}
	var h proto.Hello
	switch {
	case m.T != proto.TypeHello:
		s.refuse(nc, proto.CodeInvalid, "the first message must be hello, not "+session.Sanitize(m.T, 64))
		return
	case m.Decode(&h) != nil:
		s.refuse(nc, proto.CodeInvalid, "the hello does not decode")
		return
	case !proto.Speaks(h.Gen):
		// Only a generation whose bytes this build cannot read is refused;
		// what the client lacks or has beyond this server is in the caps.
		s.refuse(nc, proto.CodeGeneration, fmt.Sprintf("this server speaks protocol %d, the client protocol %d", proto.Gen, h.Gen))
		return
	case h.Kind != proto.KindInteractive && h.Kind != proto.KindOneshot:
		s.refuse(nc, proto.CodeInvalid, fmt.Sprintf("unknown connection kind %q", session.Sanitize(h.Kind, 64)))
		return
	}
	nc.SetReadDeadline(time.Time{})

	type verdict struct {
		cn         *conn
		code, text string
	}
	answer := make(chan verdict, 1)
	s.post(func(c *core) {
		cn, code, text := c.admit(nc, h)
		answer <- verdict{cn, code, text}
	})
	var v verdict
	select {
	case v = <-answer:
	case <-s.quit:
		nc.Close()
		return
	}
	if v.cn == nil {
		s.refuse(nc, v.code, v.text)
		return
	}
	s.run(v.cn)
}

// refuse answers a hello with refuse and closes the connection (rule 5).
func (s *Server) refuse(nc net.Conn, code, text string) {
	nc.SetWriteDeadline(time.Now().Add(refuseWrite))
	proto.WriteMsg(nc, proto.Encode(proto.Refuse{Code: code, Text: text}))
	nc.Close()
}

// admit runs the checks of 4.2.1 that need the panes and registers the
// connection; on the loop. It returns the connection, or nil and the refuse.
func (c *core) admit(nc net.Conn, h proto.Hello) (*conn, string, string) {
	local := !h.Remote
	if h.Kind == proto.KindInteractive && local {
		if id, ok := c.nestedIn(h); ok {
			return nil, proto.CodeNested, fmt.Sprintf("this terminal is pane %s of devtools on %s; devtools does not run inside itself", id, c.host())
		}
	}
	paneID := ""
	if h.Kind == proto.KindOneshot && h.Pane != "" {
		if !local || !c.paneOfProcess(h.Pane, h.Ancestors) {
			return nil, proto.CodeNoPane, fmt.Sprintf("%s is not the pane of this process", session.Sanitize(h.Pane, 32))
		}
		paneID = h.Pane
	}
	if h.Kind == proto.KindInteractive && local {
		for _, name := range helloDisplayVars {
			if v := h.Env[name]; v != "" {
				c.helloEnv[name] = v
			}
		}
	}
	cn := &conn{
		id:     c.reg.issue(),
		kind:   h.Kind,
		remote: h.Remote,
		pane:   paneID,
		hello:  h,
		nc:     nc,
		lane: lane.New(lane.Options{
			FrameInterval: c.s.o.FrameInterval,
			StallTimeout:  c.s.o.StallTimeout,
			PingInterval:  c.s.o.PingInterval,
		}),
		focused: h.Kind == proto.KindInteractive,
		colors:  helloColors(h),
		pasting: map[string]bool{},
	}
	c.reg.add(cn)
	c.s.mu.Lock()
	c.s.conns[cn] = true
	c.s.mu.Unlock()
	c.send(cn, proto.Encode(proto.Welcome{
		Gen:       h.Gen, // the client's, which this server speaks: an older client checks for its own
		Version:   c.s.o.Version,
		Caps:      c.caps(),
		Boot:      c.s.boot,
		Host:      c.host(),
		Pid:       pid(),
		Client:    cn.id,
		Clients:   c.reg.interactive(),
		Installed: c.installed,
		Upgraded:  c.s.lastUpgrade(),
	}))
	if cn.kind == proto.KindInteractive {
		c.send(cn, proto.Encode(c.snapshot()))
	}
	return cn, "", ""
}

// nestedIn reports the pane whose terminal the hello names, or one of whose
// children is among its ancestors (3.4 d).
func (c *core) nestedIn(h proto.Hello) (string, bool) {
	for _, id := range c.paneIDs() {
		info := c.panes[id].p.Info()
		if h.TTY != "" && info.TTY != "" && h.TTY == info.TTY {
			return id, true
		}
		if info.Pid > 0 && contains(h.Ancestors, info.Pid) {
			return id, true
		}
	}
	return "", false
}

// paneOfProcess reports whether the pane exists and the pid of its child is
// one of the caller's ancestors (4.2.1).
func (c *core) paneOfProcess(id string, ancestors []int) bool {
	if _, ok := c.known.Panes[id]; !ok {
		return false
	}
	ref, ok := c.panes[id]
	if !ok {
		return false
	}
	info := ref.p.Info()
	return info.Pid > 0 && contains(ancestors, info.Pid)
}

// caps are welcome.caps: stateget, tree and update, which the core
// implements, upgrade when the server can upgrade itself live, and what
// the extensions add.
func (c *core) caps() []string {
	caps := []string{proto.CapStateGet, proto.CapTree, proto.CapUpdate}
	if c.s.canUpgrade() {
		caps = append(caps, proto.CapUpgrade)
	}
	for _, x := range extensionCaps() {
		if !slices.Contains(caps, x) {
			caps = append(caps, x)
		}
	}
	return caps
}

func (c *core) host() string {
	return session.Sanitize(c.s.o.Host, 64)
}

// paneIDs returns the ids of the pane objects, sorted, so that the first
// match is the same every time.
func (c *core) paneIDs() []string {
	ids := make([]string, 0, len(c.panes))
	for id := range c.panes {
		ids = append(ids, id)
	}
	sortIDs(ids)
	return ids
}

func sortIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[i], ids[j]) })
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func contains(list []int, x int) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}
