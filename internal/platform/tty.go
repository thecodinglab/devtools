package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errNotTerminal = errors.New("platform: not a terminal")

// isTerminal reports whether fd is a terminal: the terminal attributes can
// be read.
func isTerminal(fd int) bool {
	_, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	return err == nil
}

// isCharDevice reports whether st describes a character device.
func isCharDevice(st *unix.Stat_t) bool {
	return uint32(st.Mode)&unix.S_IFMT == unix.S_IFCHR
}

// ttyDevice returns the stat of the terminal f is open on, or an error when
// f is no terminal.
func ttyDevice(f *os.File) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := control(f, func(fd int) error {
		if !isTerminal(fd) {
			return errNotTerminal
		}
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		if !isCharDevice(&st) {
			return errNotTerminal
		}
		return nil
	})
	return st, err
}

// TTYName returns the path of the terminal device f is open on, for example
// /dev/pts/12 or /dev/ttys004. An error when f is no terminal or no path
// names its device.
func TTYName(f *os.File) (string, error) {
	st, err := ttyDevice(f)
	if err != nil {
		return "", err
	}
	if path, ok := ttyCandidate(f); ok && sameDevice(&st, path) {
		return path, nil
	}
	for _, dir := range ttyDirs {
		if path, ok := scanDevices(dir, &st); ok {
			return path, nil
		}
	}
	return "", errors.New("platform: no device path for the terminal")
}

// sameDevice reports whether path is a character device with the device
// number of st.
func sameDevice(st *unix.Stat_t, path string) bool {
	var other unix.Stat_t
	if err := unix.Stat(path, &other); err != nil {
		return false
	}
	return isCharDevice(&other) && other.Rdev == st.Rdev
}

// scanDevices looks in dir for the character device of st. Entries whose
// name starts with "tty" or that are numbers (the entries of /dev/pts) are
// tried first, so /dev/tty, which every terminal of a process also is, does
// not win over the real name: /dev/tty has a device number of its own and
// never matches.
func scanDevices(dir string, st *unix.Stat_t) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !(strings.HasPrefix(name, "tty") || isDigits(name)) {
			continue
		}
		path := filepath.Join(dir, name)
		if sameDevice(st, path) {
			return path, true
		}
	}
	return "", false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// SameTTY reports whether f is the terminal device at path. It compares the
// device numbers (st_rdev) of the open descriptor and of the path; it needs
// no ttyname and is the same on Linux and macOS. False on any error: when f
// is no terminal, when path does not exist or is no character device, or
// when the numbers differ.
func SameTTY(f *os.File, path string) bool {
	if path == "" {
		return false
	}
	st, err := ttyDevice(f)
	if err != nil {
		return false
	}
	return sameDevice(&st, path)
}

// ForegroundGroup returns the foreground process group of the terminal of
// master (TIOCGPGRP). On a PTY master it is the group the slave's session
// has in the foreground.
func ForegroundGroup(master *os.File) (int, error) {
	var pgrp int
	err := control(master, func(fd int) error {
		var err error
		pgrp, err = unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		return err
	})
	if err != nil {
		return 0, err
	}
	return pgrp, nil
}

// SetIUTF8 sets the IUTF8 input flag of a terminal, so the line discipline
// erases a whole UTF-8 character on backspace in canonical mode.
func SetIUTF8(f *os.File) error {
	return control(f, func(fd int) error {
		t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
		if err != nil {
			return err
		}
		if t.Iflag&unix.IUTF8 != 0 {
			return nil
		}
		t.Iflag |= unix.IUTF8
		return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
	})
}

// Winsize returns the size of a terminal (TIOCGWINSZ).
func Winsize(f *os.File) (cols, rows int, err error) {
	err = control(f, func(fd int) error {
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err != nil {
			return err
		}
		cols, rows = int(ws.Col), int(ws.Row)
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return cols, rows, nil
}
