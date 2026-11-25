package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestCloexecRoundTrip(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := int(r.Fd())
	for _, on := range []bool{false, true, false, true} {
		if err := SetCloexec(fd, on); err != nil {
			t.Fatal(err)
		}
		if got, _ := Cloexec(fd); got != on {
			t.Fatalf("Cloexec after SetCloexec(%v) = %v", on, got)
		}
	}
}

func TestDupFileLandsAboveTheFloorCloseOnExecAndNonBlocking(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := r.SetReadDeadline(time.Time{}); err != nil {
		t.Skip("the pipe is not polled here")
	}
	fd, err := DupFile(r)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if fd < HandoverFloor {
		t.Errorf("DupFile = %d, below %d", fd, HandoverFloor)
	}
	if on, _ := Cloexec(fd); !on {
		t.Errorf("the duplicate is not close-on-exec")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK == 0 {
		t.Errorf("the duplicate is not non-blocking (flags %#x, %v)", flags, err)
	}
	// The original is still polled: a deadline still works.
	r.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read on the original after DupFile: %v, want a deadline error", err)
	}
}

func TestHandoverFileHasNoNameOnDisk(t *testing.T) {
	dir := t.TempDir()
	f, err := HandoverFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if on, _ := Cloexec(int(f.Fd())); !on {
		t.Errorf("the handover file is not close-on-exec")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("the state directory holds %v", entries)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 7)
	if _, err := f.Read(b); err != nil || string(b) != "payload" {
		t.Errorf("read back %q, %v", b, err)
	}
}

func TestPTYPeerNameIsTheSlave(t *testing.T) {
	m, s, err := pty.Open()
	if err != nil {
		t.Skip("no PTY: ", err)
	}
	defer m.Close()
	defer s.Close()
	got, err := PTYPeerName(m)
	if err != nil {
		t.Fatal(err)
	}
	if got != s.Name() {
		t.Errorf("PTYPeerName = %q, want %q", got, s.Name())
	}
}

func TestAFailedExecKeepsTheFlagsAndTheLimit(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	kept, err := DupFile(r)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(kept)
	other, err := DupFile(w)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(other)
	SetCloexec(other, false) // a descriptor that leaked: the sweep catches it

	cur, hard, _ := FDLimit()
	var inExec struct{ kept, other bool }
	old := execve
	execve = func(string, []string, []string) error {
		inExec.kept, _ = Cloexec(kept)
		inExec.other, _ = Cloexec(other)
		// syscall.Exec lowers the soft limit before execve; the failure must
		// not leave it lowered.
		syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: 256, Max: hard})
		return syscall.ENOENT
	}
	defer func() { execve = old }()
	err = ExecKeeping("/nonexistent", []string{"x"}, nil, []int{kept})
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("ExecKeeping = %v, want ENOENT", err)
	}
	if inExec.kept || !inExec.other {
		t.Errorf("at execve: kept close-on-exec %v (want false), other %v (want true)", inExec.kept, inExec.other)
	}
	if on, _ := Cloexec(kept); !on {
		t.Errorf("after the failed exec the kept descriptor is not close-on-exec again")
	}
	if now, _, _ := FDLimit(); now != cur {
		t.Errorf("soft limit after the failed exec %d, want %d", now, cur)
	}
}

func TestOpenFDsListsAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fds, err := OpenFDs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fds, int(r.Fd())) || !slices.Contains(fds, 0) {
		t.Errorf("OpenFDs %v lacks %d or 0", fds, r.Fd())
	}
}

func TestFileIdentityFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	os.WriteFile(a, []byte("x"), 0o700)
	link := filepath.Join(dir, "link")
	os.Symlink(a, link)
	ia, err := FileIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	il, err := FileIdentity(link)
	if err != nil {
		t.Fatal(err)
	}
	if !ia.Same(il) || il.Path != ia.Path {
		t.Errorf("identity through the link %+v, of the file %+v", il, ia)
	}
	if _, err := FileIdentity(dir); err == nil {
		t.Errorf("FileIdentity of a directory succeeded")
	}
	self, err := SelfExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if exe, _ := os.Executable(); exe != "" {
		if want, err := FileIdentity(exe); err == nil && !want.Same(self) {
			t.Errorf("SelfExecutable %+v, os.Executable %+v", self, want)
		}
	}
}

func TestRunCheckCapsAndReportsAFailure(t *testing.T) {
	ctx := context.Background()
	out, err := RunCheck(ctx, "/bin/sh", []string{"-c", "printf hello"}, nil, 64)
	if err != nil || string(out) != "hello" {
		t.Errorf("RunCheck = %q, %v", out, err)
	}
	if _, err := RunCheck(ctx, "/bin/sh", []string{"-c", "printf 0123456789"}, nil, 4); err == nil {
		t.Errorf("RunCheck over the limit succeeded")
	}
	_, err = RunCheck(ctx, "/bin/sh", []string{"-c", "echo broken >&2; exit 3"}, nil, 64)
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("RunCheck of a failing program: %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := RunCheck(short, "/bin/sh", []string{"-c", "sleep 5"}, nil, 64); err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("RunCheck past its context: %v after %v", err, time.Since(start))
	}
}

func TestServicePidParsesBothManagers(t *testing.T) {
	if pid, err := parseServicePid("linux", "4242\n"); err != nil || pid != 4242 {
		t.Errorf("linux: %d, %v", pid, err)
	}
	if pid, err := parseServicePid("linux", "0\n"); err != nil || pid != 0 {
		t.Errorf("linux, not running: %d, %v", pid, err)
	}
	out := "gui/501/org.nix-community.home.devtools-server = {\n\tactive count = 1\n\tpid = 80723\n\tstate = running\n}\n"
	if pid, err := parseServicePid("darwin", out); err != nil || pid != 80723 {
		t.Errorf("darwin: %d, %v", pid, err)
	}
	if got := servicePidArgv("darwin", 501); strings.Join(got, " ") != "launchctl print gui/501/"+LaunchdLabel {
		t.Errorf("darwin argv %q", got)
	}
	if got := servicePidArgv("linux", 1000); got[0] != "systemctl" || !slices.Contains(got, "MainPID") {
		t.Errorf("linux argv %q", got)
	}
	_ = runtime.GOOS
}
