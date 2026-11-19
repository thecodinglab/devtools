package hub

import "golang.org/x/sys/unix"

// getTermios is the ioctl that reads a terminal's mode.
const getTermios = unix.TCGETS
