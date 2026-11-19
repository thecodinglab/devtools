package platform

import "golang.org/x/sys/unix"

// fdCeiling is the highest soft limit of open files a process may set: the
// hard limit, which is often RLIM_INFINITY on macOS, and kern.maxfilesperproc,
// above which setrlimit fails with EINVAL.
//
// UNVERIFIED until K2 on a Mac (provisional, E2).
func fdCeiling(hard uint64) uint64 {
	if n, err := unix.SysctlUint32("kern.maxfilesperproc"); err == nil && uint64(n) < hard {
		return uint64(n)
	}
	return hard
}
