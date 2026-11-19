package cli

import "golang.org/x/sys/unix"

// The requests that read and write the attributes of a terminal.
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)
