package e2e

import "golang.org/x/sys/unix"

// p2GetTermios is the ioctl that reads a terminal's mode.
const p2GetTermios = unix.TCGETS
