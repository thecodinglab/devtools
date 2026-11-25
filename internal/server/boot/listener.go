package boot

import (
	"errors"
	"log/slog"
	"net"
	"time"
)

// peerListener is the listener the server is given (SEC2): Accept returns
// only connections whose peer has the server's uid. Any other connection,
// and one whose peer cannot be read, is closed before anything is read from
// it or written to it, so the server never sees it and never reads its
// hello. The socket's mode 0600 in a 0700 directory already keeps other
// users out; this is the second, independent check of 3.8.
type peerListener struct {
	net.Listener
	uid  uint32
	peer func(*net.UnixConn) (uint32, error)
	log  *slog.Logger
}

func newPeerListener(l net.Listener, uid uint32, peer func(*net.UnixConn) (uint32, error), log *slog.Logger) net.Listener {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &peerListener{Listener: l, uid: uid, peer: peer, log: log}
}

// Accept waits for the next connection of the same user. Errors of the
// underlying listener are returned as they are.
func (p *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := p.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if p.allowed(c) {
			return c, nil
		}
		c.Close()
	}
}

// allowed reports whether the peer of c is this user.
func (p *peerListener) allowed(c net.Conn) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		p.log.Warn("closed a connection that is not a unix socket")
		return false
	}
	uid, err := p.peer(uc)
	if err != nil {
		p.log.Warn("closed a connection whose peer cannot be read", "err", err.Error())
		return false
	}
	if uid != p.uid {
		p.log.Warn("closed a connection of another user", "uid", uid)
		return false
	}
	return true
}

// SetDeadline sets the deadline of Accept on the listener beneath; the
// server pauses accepting with it during a live upgrade, so connections
// wait in the listen backlog (server/upgrade.go).
func (p *peerListener) SetDeadline(t time.Time) error {
	if d, ok := p.Listener.(interface{ SetDeadline(time.Time) error }); ok {
		return d.SetDeadline(t)
	}
	return errors.New("boot: the listener has no deadline")
}
