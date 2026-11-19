package platform

import "golang.org/x/sys/unix"

// RaiseFDLimit raises the soft limit of open files of this process towards
// want and never lowers it. The soft limit cannot pass the hard limit (nor,
// on macOS, kern.maxfilesperproc), so got may be less than want. got is the
// soft limit after the call; on an error it is the limit that holds.
func RaiseFDLimit(want uint64) (got uint64, err error) {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return 0, err
	}
	cur := uint64(lim.Cur)
	if want <= cur {
		return cur, nil
	}
	target := want
	if ceil := fdCeiling(uint64(lim.Max)); target > ceil {
		target = ceil
	}
	if target <= cur {
		return cur, nil
	}
	lim.Cur = target
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return cur, err
	}
	return target, nil
}
