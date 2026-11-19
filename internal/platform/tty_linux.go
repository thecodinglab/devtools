package platform

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)

// ttyDirs are the directories TTYName searches when the link of /proc does
// not name the device.
var ttyDirs = []string{"/dev/pts", "/dev"}

// ttyCandidate reads the link /proc/self/fd/<fd>, which names the path the
// descriptor was opened with. TTYName checks it against the device number.
func ttyCandidate(f *os.File) (string, bool) {
	var path string
	err := control(f, func(fd int) error {
		var err error
		path, err = os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		return err
	})
	return path, err == nil && path != ""
}
