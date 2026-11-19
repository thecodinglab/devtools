package boot

import (
	"os"
	"syscall"

	"devtools/internal/platform"
)

// killPaneGroups is the last act of the watchdog: every process below the
// server gets SIGKILL through its process group, and a descendant that
// shares the server's own group gets it by pid, so the server does not
// signal itself. Pane programs lead their own sessions, so their groups
// hold the shell and its jobs.
func killPaneGroups() {
	procs, err := platform.Procs()
	if err != nil {
		return
	}
	for _, t := range descendants(os.Getpid(), procs) {
		if t.group {
			syscall.Kill(-t.id, syscall.SIGKILL)
		} else {
			syscall.Kill(t.id, syscall.SIGKILL)
		}
	}
}

// target is one kill: a process group (group) or a single pid.
type target struct {
	id    int
	group bool
}

// descendants lists the kills for every process below root in procs: each
// distinct process group other than root's own once, and each process in
// root's group by pid. It is pure, for the tests.
func descendants(root int, procs []platform.Proc) []target {
	children := map[int][]platform.Proc{}
	rootGroup := 0
	for _, p := range procs {
		children[p.PPid] = append(children[p.PPid], p)
		if p.Pid == root {
			rootGroup = p.Pgid
		}
	}
	var out []target
	groups := map[int]bool{}
	seen := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if seen[c.Pid] {
				continue
			}
			seen[c.Pid] = true
			queue = append(queue, c.Pid)
			switch {
			case c.Pgid <= 1 || c.Pgid == rootGroup:
				out = append(out, target{id: c.Pid})
			case !groups[c.Pgid]:
				groups[c.Pgid] = true
				out = append(out, target{id: c.Pgid, group: true})
			}
		}
	}
	return out
}
