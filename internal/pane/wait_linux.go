package pane

import "golang.org/x/sys/unix"

// waitExited blocks until the child pid has ended, without reaping it
// (waitid with WNOWAIT). While the child is not reaped its pid, and with it
// its process group id, cannot be given to another process, so a signal
// sent before the waiter reaps cannot hit a stranger (3.2.1).
func waitExited(pid int) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return
		}
	}
}
