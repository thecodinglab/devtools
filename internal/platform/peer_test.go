package platform

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// unixPair returns the two ends of a connected unix socket pair as
// connections; no socket is bound and nothing listens.
func unixPair(t *testing.T) (a, b *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int, name string) *net.UnixConn {
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c.(*net.UnixConn)
	}
	return conn(fds[0], "a"), conn(fds[1], "b")
}

func TestPeerUIDIsOurs(t *testing.T) {
	a, b := unixPair(t)
	for _, c := range []*net.UnixConn{a, b} {
		uid, err := PeerUID(c)
		if err != nil {
			t.Fatalf("PeerUID: %v", err)
		}
		if uid != uint32(os.Getuid()) {
			t.Errorf("PeerUID = %d, want %d", uid, os.Getuid())
		}
	}
	if _, err := PeerUID(nil); err == nil {
		t.Error("PeerUID(nil) gave no error")
	}
	a.Close()
	if _, err := PeerUID(a); err == nil {
		t.Error("PeerUID of a closed connection gave no error")
	}
}
