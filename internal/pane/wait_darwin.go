package pane

import "golang.org/x/sys/unix"

// waitExited blocks until the child pid has ended, without reaping it.
// macOS has no waitid in golang.org/x/sys, so the end is awaited with a
// kqueue filter on the process (NOTE_EXIT); a child that already ended
// cannot be registered (ESRCH), which also means it is waitable. While the
// child is not reaped its pid and process group id cannot be reused, so a
// signal sent before the waiter reaps cannot hit a stranger (3.2.1).
//
// provisional E2: written from the kqueue documentation and type-checked
// only; E2 part 2 runs the PTY path on a Mac. When the kqueue cannot be
// used, the function returns at once and the caller's blocking wait4 is the
// wait, with the small window between reaping and marking the child reaped
// that Go's own os.Process has on macOS.
func waitExited(pid int) {
	kq, err := unix.Kqueue()
	if err != nil {
		return
	}
	defer unix.Close(kq)
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT
	out := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, []unix.Kevent_t{ev}, out, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil || n > 0 {
			// An event, an EV_ERROR entry (ESRCH: already ended) or a
			// failure: in each case the caller's wait4 decides.
			return
		}
	}
}
