package platform

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/testkit"
)

func findProc(t *testing.T, pid int) (Proc, bool) {
	t.Helper()
	procs, err := Procs()
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	for _, p := range procs {
		if p.Pid == pid {
			return p, true
		}
	}
	return Proc{}, false
}

func TestProcsContainsThisProcessWithItsArguments(t *testing.T) {
	p, ok := findProc(t, os.Getpid())
	if !ok {
		t.Fatalf("Procs has no entry for this process (pid %d)", os.Getpid())
	}
	if !slices.Equal(p.Args, os.Args) {
		t.Errorf("Args = %q, want %q", p.Args, os.Args)
	}
	if p.PPid != os.Getppid() {
		t.Errorf("PPid = %d, want %d", p.PPid, os.Getppid())
	}
	if p.Pgid != syscall.Getpgrp() {
		t.Errorf("Pgid = %d, want %d", p.Pgid, syscall.Getpgrp())
	}
	if p.Name == "" || !strings.HasPrefix(filepath.Base(os.Args[0]), p.Name) {
		t.Errorf("Name = %q, want a prefix of %q", p.Name, filepath.Base(os.Args[0]))
	}
}

func TestProcsReportsTheProcessGroupOfAChild(t *testing.T) {
	cmd, _, _ := startHelper(t, "block", &syscall.SysProcAttr{Setpgid: true}, "", "one", "two words")
	pid := cmd.Process.Pid
	var p Proc
	testkit.Eventually(t, 5*time.Second, "the child is in the process table with its arguments", func() bool {
		var ok bool
		p, ok = findProc(t, pid)
		return ok && slices.Equal(p.Args, cmd.Args)
	})
	if p.Pgid != pid {
		t.Errorf("Pgid = %d, want the child's own group %d", p.Pgid, pid)
	}
	if p.Pgid == syscall.Getpgrp() {
		t.Errorf("Pgid = %d is the test's group", p.Pgid)
	}
	if p.PPid != os.Getpid() {
		t.Errorf("PPid = %d, want %d", p.PPid, os.Getpid())
	}
}

func TestCwdOfAChild(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	cmd, _, _ := startHelper(t, "block", nil, dir)
	got, err := Cwd(cmd.Process.Pid)
	if runtime.GOOS == "darwin" {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Cwd on darwin = %q, %v; want ErrUnsupported", got, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Cwd: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Cwd = %q, want %q", got, want)
	}
	if _, err := Cwd(1 << 30); err == nil {
		t.Errorf("Cwd of a pid that does not exist gave no error")
	}
}

func TestAncestorsEndAtPidOne(t *testing.T) {
	chain := Ancestors(os.Getpid())
	if len(chain) == 0 {
		t.Fatal("Ancestors of this process is empty")
	}
	if chain[0] != os.Getppid() {
		t.Errorf("first ancestor = %d, want the parent %d", chain[0], os.Getppid())
	}
	if last := chain[len(chain)-1]; last != 1 {
		t.Errorf("last ancestor = %d, want 1; chain %v", last, chain)
	}
	if slices.Contains(chain, os.Getpid()) {
		t.Errorf("chain %v holds the process itself", chain)
	}
	if got := Ancestors(1); len(got) != 0 {
		t.Errorf("Ancestors(1) = %v, want none", got)
	}

	cmd, _, _ := startHelper(t, "block", nil, "")
	child := Ancestors(cmd.Process.Pid)
	if len(child) == 0 || child[0] != os.Getpid() || !slices.Equal(child[1:], chain) {
		t.Errorf("Ancestors of a child = %v, want this process %d, then %v", child, os.Getpid(), chain)
	}
}

// The buffer of kern.procargs2 as the xnu sources lay it out; provisional
// until E2 part 2 reads a real one on a Mac.
func TestProcargs2Layout(t *testing.T) {
	buf := []byte{3, 0, 0, 0}
	buf = append(buf, "/usr/bin/prog\x00\x00\x00\x00"...)
	buf = append(buf, "prog\x00-x\x00two words\x00"...)
	buf = append(buf, "HOME=/h\x00PATH=/bin\x00"...)
	got := parseProcargs2(buf)
	want := []string{"prog", "-x", "two words"}
	if !slices.Equal(got, want) {
		t.Errorf("parseProcargs2 = %q, want %q", got, want)
	}
	for _, short := range [][]byte{nil, {1, 0}, {1, 0, 0, 0}, {2, 0, 0, 0, 'p', 0, 'a'}} {
		parseProcargs2(short) // must not panic
	}
	if got := parseProcargs2([]byte{2, 0, 0, 0, '/', 'p', 0, 0, 'a', 0}); !slices.Equal(got, []string{"a"}) {
		t.Errorf("a cut buffer gave %q, want [a]", got)
	}
}
