package platform

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lock opens (creating it, mode 0600) the file at path and takes an
// exclusive, non-blocking flock on it. It returns ErrLocked when another
// open file holds the lock, in this process or any other. The lock lasts as
// long as the returned file is open: closing it, or the end of the process,
// frees it, so a holder that died leaves no stale lock. The descriptor is
// close-on-exec, so no child inherits the lock.
func Lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	err = control(f, func(fd int) error {
		for {
			err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			if err != unix.EINTR {
				return err
			}
		}
	})
	if err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, &os.PathError{Op: "flock", Path: path, Err: err}
	}
	return f, nil
}
