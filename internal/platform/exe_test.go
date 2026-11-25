package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The running image is a file, and the one the test binary is.
func TestRunningExecutableIsTheTestBinary(t *testing.T) {
	p := RunningExecutable()
	if p == "" {
		t.Skip("the running image cannot be told here")
	}
	got, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(got, want) {
		t.Fatalf("RunningExecutable = %s, not the test binary %s", p, exe)
	}
}

// Started through a symlink, the running image is still the file the
// symlink pointed at, also after it points elsewhere: the identity of the
// running build does not go through the path it was started by.
func TestRunningExecutableIsNotTheSymlinkItWasStartedBy(t *testing.T) {
	if link := os.Getenv("DEVTOOLS_EXE_LINK"); link != "" {
		// the profile switches between exec and the first look
		tmp := link + ".new"
		if err := os.Symlink(filepath.Join(filepath.Dir(link), "other"), tmp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, link); err != nil {
			t.Fatal(err)
		}
		fmt.Println(RunningExecutable())
		return
	}
	if RunningExecutable() == "" {
		t.Skip("the running image cannot be told here")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"real", "other"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "-test.run=^TestRunningExecutableIsNotTheSymlinkItWasStartedBy$")
	cmd.Env = append(os.Environ(), "DEVTOOLS_EXE_LINK="+link)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	gi, err := os.Stat(got)
	if err != nil {
		t.Fatalf("RunningExecutable in the child = %q: %v", got, err)
	}
	ri, _ := os.Stat(real)
	if !os.SameFile(gi, ri) || filepath.Base(got) != "real" {
		t.Fatalf("RunningExecutable started through %s = %s, want %s", link, got, real)
	}
}
