package platform

// maxAncestors bounds the walk of Ancestors, so a process table that
// changes under it (a pid reused as its own ancestor) cannot loop.
const maxAncestors = 4096

// Ancestors returns the parent chain of pid: its parent first, then the
// parent's parent, and so on up to and including pid 1. It does not hold
// pid itself. The walk stops early, with what it has, when a process cannot
// be read (it ended) or has no parent; for pid 1 itself the chain is empty.
func Ancestors(pid int) []int {
	var chain []int
	seen := map[int]bool{pid: true}
	for cur := pid; cur > 1 && len(chain) < maxAncestors; {
		ppid, err := parentOf(cur)
		if err != nil || ppid <= 0 || seen[ppid] {
			break
		}
		chain = append(chain, ppid)
		seen[ppid] = true
		cur = ppid
	}
	return chain
}
