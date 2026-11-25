package platform

import (
	"os"

	"golang.org/x/sys/unix"
)

// On Linux os.Executable reads /proc/self/exe, the kernel's link to the
// file of the running image.
func runningExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// runningImage is the device and inode of the running image: a stat of
// /proc/self/exe reaches the mapped file even when its path names another
// one by now (or none: os.Executable drops the " (deleted)" of the link).
func runningImage() (dev, ino uint64, ok bool) {
	var st unix.Stat_t
	if err := unix.Stat("/proc/self/exe", &st); err != nil {
		return 0, 0, false
	}
	return uint64(st.Dev), st.Ino, true
}
