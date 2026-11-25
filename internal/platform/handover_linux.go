package platform

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// fdDir lists the open descriptors of the process.
const fdDir = "/proc/self/fd"

// HandoverFile returns an anonymous file for the payload of a live
// upgrade: a memfd, in memory only, close-on-exec until the exec step
// clears it. dir is not used on Linux.
func HandoverFile(dir string) (*os.File, error) {
	fd, err := unix.MemfdCreate("devtools-handover", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, &os.PathError{Op: "memfd_create", Path: "devtools-handover", Err: err}
	}
	return os.NewFile(uintptr(fd), "devtools-handover"), nil
}

// PTYPeerName is the name of the slave of a PTY master: /dev/pts/N
// (TIOCGPTN).
func PTYPeerName(master *os.File) (string, error) {
	var name string
	err := control(master, func(fd int) error {
		n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
		if err != nil {
			return err
		}
		name = "/dev/pts/" + strconv.FormatUint(uint64(n), 10)
		return nil
	})
	return name, err
}
