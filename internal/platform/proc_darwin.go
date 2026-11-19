package platform

import (
	"golang.org/x/sys/unix"
)

// Procs returns the process table from sysctl kern.proc.all, with the
// arguments of each process from kern.procargs2. A process whose arguments
// cannot be read (another user's, or one that ended) is listed without
// them.
//
// UNVERIFIED until K2 on a Mac: that kern.procargs2 returns the arguments
// of the user's own processes without privileges (provisional, E2 part 2).
func Procs() ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	procs := make([]Proc, 0, len(kps))
	for i := range kps {
		kp := &kps[i]
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue // kernel_task
		}
		p := Proc{
			Pid:  pid,
			PPid: int(kp.Eproc.Ppid),
			Pgid: int(kp.Eproc.Pgid),
			Name: unix.ByteSliceToString(kp.Proc.P_comm[:]),
		}
		if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil {
			p.Args = parseProcargs2(raw)
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// Cwd is not answered on macOS: the directory of another process needs
// proc_pidinfo, which is libproc and so cgo (5.3.4).
func Cwd(pid int) (string, error) {
	return "", ErrUnsupported
}

func parentOf(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, unix.ESRCH
	}
	return int(kp.Eproc.Ppid), nil
}
