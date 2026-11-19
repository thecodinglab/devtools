package platform

import (
	"os"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)

// ttyDirs are the directories TTYName searches: a PTY slave on macOS is
// /dev/ttysNNN.
//
// UNVERIFIED until K2 on a Mac (provisional, E2 part 2).
var ttyDirs = []string{"/dev"}

// ttyCandidate has nothing to offer on macOS, where no file names the path
// a descriptor was opened with; TTYName searches /dev.
func ttyCandidate(f *os.File) (string, bool) { return "", false }
