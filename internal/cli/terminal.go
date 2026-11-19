package cli

// The terminal of a command: the two descriptors mux.Detect and a backend
// are given, and the descriptor the guard probes.

import (
	"io"
	"os"
	"syscall"

	"devtools/internal/platform"

	"golang.org/x/sys/unix"
)

// terminalIn and terminalOut are the standard input and output of the
// process as files: what backend.Facts asks whether they are terminals, and
// what a backend draws on (backend.Deps.In, Out). The writers handed to Run
// are for text only. Tests replace them, with /dev/null by default (TestMain)
// and with a pty where a test needs a terminal.
var (
	terminalIn  = os.Stdin
	terminalOut = os.Stdout
)

// openProbe opens the terminal of in a second time, for the guard's probe,
// and puts it in raw mode: without raw mode the terminal's answers wait in
// the line discipline for a newline and are echoed (guard.Check). Its own
// descriptor is pollable, so the probe's read deadline works and no reader
// is left behind on in for the client to compete with (guard.Probe). done
// restores the mode and closes the descriptor.
func openProbe(in *os.File) (tty io.ReadWriter, done func(), err error) {
	path, err := platform.TTYName(in)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	restore, err := makeRaw(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, func() {
		restore()
		f.Close()
	}, nil
}

// makeRaw switches off canonical input and echo on the terminal of f, so
// every byte the terminal answers is read at once and none is shown, and
// returns the function that restores the attributes it found.
func makeRaw(f *os.File) (restore func(), err error) {
	sc, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var old *unix.Termios
	var serr error
	if err := sc.Control(func(fd uintptr) {
		old, serr = unix.IoctlGetTermios(int(fd), ioctlGetTermios)
		if serr != nil {
			return
		}
		raw := *old
		raw.Lflag &^= unix.ICANON | unix.ECHO | unix.ECHONL | unix.IEXTEN
		raw.Iflag &^= unix.ICRNL | unix.INLCR | unix.IGNCR | unix.IXON
		raw.Cc[unix.VMIN] = 1
		raw.Cc[unix.VTIME] = 0
		serr = unix.IoctlSetTermios(int(fd), ioctlSetTermios, &raw)
	}); err != nil {
		return nil, err
	}
	if serr != nil {
		return nil, serr
	}
	return func() {
		sc.Control(func(fd uintptr) {
			unix.IoctlSetTermios(int(fd), ioctlSetTermios, old)
		})
	}, nil
}
