package platform

import (
	"errors"
	"os"
)

// control runs fn with the descriptor of f. It goes through SyscallConn
// and not through Fd, because Fd switches a descriptor that the runtime
// polls (a PTY master) to blocking mode for good.
func control(f *os.File, fn func(fd int) error) error {
	if f == nil {
		return errors.New("platform: nil file")
	}
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := rc.Control(func(fd uintptr) { inner = fn(int(fd)) }); err != nil {
		return err
	}
	return inner
}
