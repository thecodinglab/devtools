package pane

import (
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"devtools/internal/platform"
)

// openPTY opens a PTY pair. The master comes back pollable: creack/pty
// reads the slave's name through File.Fd, which switches a descriptor to
// blocking mode for good, and a blocking master would keep its reader in a
// read that Close cannot end while some process still holds the slave. So
// the master's descriptor is duplicated (close-on-exec), set non-blocking
// and wrapped again; the runtime then polls it, Read and Write park instead
// of holding a thread, and Close ends both.
func openPTY() (master, slave *os.File, err error) {
	m, s, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.FcntlInt(m.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	m.Close()
	if err != nil {
		s.Close()
		return nil, nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		s.Close()
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), "/dev/ptmx")
	// A descriptor the runtime could not register with its poller has no
	// deadlines; non-blocking and unpolled, its reads would fail with
	// EAGAIN. It is put back to blocking mode then: the reader holds a
	// thread, and a read ends only with the slave's last holder.
	//
	// provisional E2: kqueue on a PTY master is expected to work on
	// current macOS; whether it does is checked by E2 part 2. Linux's epoll
	// takes it.
	if err := f.SetReadDeadline(time.Time{}); err != nil {
		control(f, func(fd int) error { return unix.SetNonblock(fd, false) })
	}
	return f, s, nil
}

// control runs fn with the descriptor of f without File.Fd (see openPTY).
func control(f *os.File, fn func(fd int) error) error {
	if f == nil {
		return errors.New("pane: no terminal")
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

// setSize sets the window size of the PTY (TIOCSWINSZ); the kernel sends
// SIGWINCH to the foreground process group. On macOS the size is read back
// and set once more when it differs (3.2.1, R02 8).
//
// provisional E2: the read-back on macOS follows R02 8 and was not run on a
// Mac; E2 part 2 checks the window-size read-back there.
func setSize(master *os.File, cols, rows int) error {
	set := func() error {
		return control(master, func(fd int) error {
			return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
		})
	}
	if err := set(); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if c, r, err := platform.Winsize(master); err == nil && (c != cols || r != rows) {
			return set()
		}
	}
	return nil
}
