package platform

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION of <sys/ucred.h>, the only layout of
// struct xucred the kernel fills.
const xucredVersion = 0

// PeerUID returns the effective uid of the process at the other end of a
// unix connection, from LOCAL_PEERCRED: the credentials the kernel recorded
// when the connection was made, which the peer cannot choose.
//
// UNVERIFIED until K2 on a Mac (provisional, E2).
func PeerUID(c *net.UnixConn) (uint32, error) {
	if c == nil {
		return 0, errNilConn
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var credErr error
	if err := rc.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if cred.Version != xucredVersion {
		return 0, errors.New("platform: unknown xucred version")
	}
	return cred.Uid, nil
}
