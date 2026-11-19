package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"devtools/internal/testkit"
)

func TestForegroundGroupFollowsTheChild(t *testing.T) {
	argv, env := testkit.HelperCommand("fgchild")
	p := testkit.StartPTY(t, argv, testkit.CleanEnv(testkit.ShortTempDir(t), env...), 80, 24)
	p.WaitFor("ready", 5*time.Second)
	child := p.Cmd.Process.Pid
	if got, err := ForegroundGroup(p.Master); err != nil || got != child {
		t.Fatalf("ForegroundGroup = %d, %v; want the child's group %d", got, err, child)
	}

	p.Write("go\r")
	var grandchild int
	re := regexp.MustCompile(`fg (\d+)`)
	testkit.Eventually(t, 5*time.Second, "the child names the group it put in the foreground", func() bool {
		m := re.FindStringSubmatch(p.Output())
		if m == nil {
			return false
		}
		grandchild, _ = strconv.Atoi(m[1])
		return true
	})
	t.Cleanup(func() { syscall.Kill(grandchild, syscall.SIGKILL) })
	testkit.Eventually(t, 5*time.Second, "the foreground group is the grandchild's", func() bool {
		got, err := ForegroundGroup(p.Master)
		return err == nil && got == grandchild
	})
	if _, err := ForegroundGroup(nil); err == nil {
		t.Error("ForegroundGroup(nil) gave no error")
	}
}

func TestSetIUTF8(t *testing.T) {
	_, slave := testkit.OpenPTY(t, 80, 24)
	var fd int
	if err := control(slave, func(f int) error { fd = f; return nil }); err != nil {
		t.Fatal(err)
	}
	tios, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	tios.Iflag &^= unix.IUTF8
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, tios); err != nil {
		t.Fatal(err)
	}
	if err := SetIUTF8(slave); err != nil {
		t.Fatalf("SetIUTF8: %v", err)
	}
	tios, err = unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	if tios.Iflag&unix.IUTF8 == 0 {
		t.Error("IUTF8 is not set")
	}
	if err := SetIUTF8(slave); err != nil {
		t.Errorf("SetIUTF8 a second time: %v", err)
	}
	if err := SetIUTF8(regularFile(t)); err == nil {
		t.Error("SetIUTF8 on a regular file gave no error")
	}
}

func TestWinsizeRoundTrip(t *testing.T) {
	master, slave := testkit.OpenPTY(t, 100, 30)
	for _, f := range []*os.File{slave, master} {
		if c, r, err := Winsize(f); err != nil || c != 100 || r != 30 {
			t.Errorf("Winsize(%s) = %dx%d, %v; want 100x30", f.Name(), c, r, err)
		}
	}
	if err := control(master, func(fd int) error {
		return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: 132, Row: 43})
	}); err != nil {
		t.Fatal(err)
	}
	if c, r, err := Winsize(slave); err != nil || c != 132 || r != 43 {
		t.Errorf("Winsize after a resize = %dx%d, %v; want 132x43", c, r, err)
	}
	if _, _, err := Winsize(regularFile(t)); err == nil {
		t.Error("Winsize of a regular file gave no error")
	}
}

func TestTTYNameIsTheSlavePath(t *testing.T) {
	_, slave := testkit.OpenPTY(t, 80, 24)
	got, err := TTYName(slave)
	if err != nil {
		t.Fatalf("TTYName: %v", err)
	}
	if got != slave.Name() {
		t.Errorf("TTYName = %q, want %q", got, slave.Name())
	}
	if _, err := TTYName(regularFile(t)); err == nil {
		t.Error("TTYName of a regular file gave no error")
	}
}

func TestSameTTYMatchesAPtyAndRejectsAnother(t *testing.T) {
	_, one := testkit.OpenPTY(t, 80, 24)
	_, two := testkit.OpenPTY(t, 80, 24)
	if !SameTTY(one, one.Name()) {
		t.Errorf("SameTTY(%s, its own path) = false", one.Name())
	}
	if !SameTTY(two, two.Name()) {
		t.Errorf("SameTTY(%s, its own path) = false", two.Name())
	}
	if SameTTY(one, two.Name()) {
		t.Errorf("SameTTY(%s, %s) = true", one.Name(), two.Name())
	}
	reg := regularFile(t)
	if SameTTY(reg, reg.Name()) {
		t.Error("SameTTY of a regular file and its own path = true")
	}
	if SameTTY(one, reg.Name()) {
		t.Error("SameTTY of a terminal and a regular file = true")
	}
	if SameTTY(one, filepath.Join(testkit.ShortTempDir(t), "missing")) {
		t.Error("SameTTY with a missing path = true")
	}
	if SameTTY(one, "") || SameTTY(nil, one.Name()) {
		t.Error("SameTTY with an empty path or a nil file = true")
	}
}

func regularFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(testkit.ShortTempDir(t), "regular"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
