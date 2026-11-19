package platform

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestRaiseFDLimitNeverLowers(t *testing.T) {
	var orig unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &orig); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Setrlimit(unix.RLIMIT_NOFILE, &orig) })

	soft := uint64(orig.Cur)
	got, err := RaiseFDLimit(1)
	if err != nil || got != soft {
		t.Fatalf("RaiseFDLimit(1) = %d, %v; want the limit %d unchanged", got, err, soft)
	}
	if got, err := RaiseFDLimit(soft); err != nil || got != soft {
		t.Fatalf("RaiseFDLimit(%d) = %d, %v; want %d", soft, got, err, soft)
	}

	// Lower the soft limit, which a process may always do, and raise it
	// again.
	if soft < 64 {
		t.Skipf("soft limit %d too low to lower", soft)
	}
	low := unix.Rlimit{Cur: orig.Cur / 2, Max: orig.Max}
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &low); err != nil {
		t.Fatal(err)
	}
	got, err = RaiseFDLimit(soft)
	if err != nil || got != soft {
		t.Fatalf("RaiseFDLimit(%d) from %d = %d, %v; want %d", soft, low.Cur, got, err, soft)
	}
	var now unix.Rlimit
	unix.Getrlimit(unix.RLIMIT_NOFILE, &now)
	if uint64(now.Cur) != soft {
		t.Errorf("soft limit is %d, want %d", now.Cur, soft)
	}
	if got, err := RaiseFDLimit(soft / 4); err != nil || got != soft {
		t.Errorf("RaiseFDLimit(%d) = %d, %v; want %d, not lowered", soft/4, got, err, soft)
	}

	// Asking past the hard limit stops at it and does not fail.
	if orig.Max != unix.RLIM_INFINITY {
		got, err := RaiseFDLimit(uint64(orig.Max) + 1000)
		if err != nil || got > uint64(orig.Max) || got < soft {
			t.Errorf("RaiseFDLimit past the hard limit %d = %d, %v", orig.Max, got, err)
		}
	}
}
