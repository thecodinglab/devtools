package platform

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fdDir lists the open descriptors of the process.
const fdDir = "/dev/fd"

// HandoverFile returns an anonymous file for the payload of a live
// upgrade: a file in dir (the state directory, mode 0700), removed at once,
// so nothing is left behind whatever happens; the descriptor keeps the
// data. Close-on-exec until the exec step clears it.
func HandoverFile(dir string) (*os.File, error) {
	f, err := os.CreateTemp(dir, "handover-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(f.Name()); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// PTYPeerName is the name of the slave of a PTY master: /dev/ttysNNN
// (TIOCPTYGNAME).
func PTYPeerName(master *os.File) (string, error) {
	var name string
	err := control(master, func(fd int) error {
		var buf [128]byte
		if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
			return e
		}
		if i := bytes.IndexByte(buf[:], 0); i >= 0 {
			name = string(buf[:i])
		} else {
			name = string(buf[:])
		}
		return nil
	})
	return name, err
}
