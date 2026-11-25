package platform

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// What proc_pidpath of libproc asks the kernel (sys/proc_info.h), without
// cgo, which only internal/vt uses: __proc_info(PROC_INFO_CALL_PIDINFO,
// pid, PROC_PIDPATHINFO, 0, buf, PROC_PIDPATHINFO_MAXSIZE).
const (
	procInfoCallPidinfo = 2
	procPidpathinfo     = 11
	procPidpathMax      = 4 * 1024
)

// On Darwin os.Executable is the path given to exec, a symlink when the
// service started the profile's devtools; PROC_PIDPATHINFO names the vnode
// of the running image instead.
func runningExecutable() string {
	buf := make([]byte, procPidpathMax)
	_, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, procInfoCallPidinfo, uintptr(os.Getpid()), procPidpathinfo, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return ""
	}
	n := bytes.IndexByte(buf, 0)
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}

// runningImage tells nothing more on Darwin: PROC_PIDPATHINFO already names
// the vnode of the running image.
func runningImage() (dev, ino uint64, ok bool) { return 0, 0, false }
