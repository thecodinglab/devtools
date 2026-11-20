package platform

import (
	"errors"
	"os"
	"runtime"
	"slices"
	"testing"
)

func TestRestartArgvPerSystem(t *testing.T) {
	linux := restartArgv("linux", 501)
	if want := []string{"systemctl", "--user", "restart", SystemdUnit}; !slices.Equal(linux, want) {
		t.Errorf("linux: %q, want %q", linux, want)
	}
	darwin := restartArgv("darwin", 501)
	if want := []string{"launchctl", "kickstart", "-k", "gui/501/" + LaunchdLabel}; !slices.Equal(darwin, want) {
		t.Errorf("darwin: %q, want %q", darwin, want)
	}
	if got, want := startHint("linux", 501), "systemctl --user start devtools-server"; got != want {
		t.Errorf("linux hint %q, want %q", got, want)
	}
	if got, want := startHint("darwin", 501), "launchctl kickstart gui/501/"+LaunchdLabel; got != want {
		t.Errorf("darwin hint %q, want %q", got, want)
	}
	if got, want := RestartArgv(77), restartArgv(runtime.GOOS, 77); !slices.Equal(got, want) {
		t.Errorf("RestartArgv on %s = %q, want %q", runtime.GOOS, got, want)
	}
}

// Restart runs RestartArgv through the injection point; no test runs a
// service manager.
func TestRestartRunsTheRestartArgv(t *testing.T) {
	var ran []string
	old := runRestart
	runRestart = func(argv []string) error { ran = argv; return errors.New("fake") }
	t.Cleanup(func() { runRestart = old })
	if err := Restart(); err == nil || err.Error() != "fake" {
		t.Errorf("Restart = %v, want the error of the command", err)
	}
	if !slices.Equal(ran, RestartArgv(os.Getuid())) {
		t.Errorf("Restart ran %q, want %q", ran, RestartArgv(os.Getuid()))
	}
}
