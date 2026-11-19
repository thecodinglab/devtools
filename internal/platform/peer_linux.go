package platform

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID returns the uid of the process at the other end of a unix
// connection, from SO_PEERCRED: the credentials the kernel recorded when
// the connection was made, which the peer cannot choose.
func PeerUID(c *net.UnixConn) (uint32, error) {
	if c == nil {
		return 0, errNilConn
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := rc.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}
