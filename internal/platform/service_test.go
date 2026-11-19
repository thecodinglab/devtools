package platform

import (
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRestartArgvPerSystem(t *testing.T) {
	linux := restartArgv("linux", 501, "devtools-server")
	if want := []string{"systemctl", "--user", "restart", SystemdUnit}; !slices.Equal(linux, want) {
		t.Errorf("linux: %q, want %q", linux, want)
	}
	darwin := restartArgv("darwin", 501, "devtools-server")
	if want := []string{"launchctl", "kickstart", "-k", "gui/501/" + LaunchdLabel}; !slices.Equal(darwin, want) {
		t.Errorf("darwin: %q, want %q", darwin, want)
	}
	if got, want := startHint("linux", 501, "devtools-server"), "systemctl --user start devtools-server"; got != want {
		t.Errorf("linux hint %q, want %q", got, want)
	}
	if got, want := startHint("darwin", 501, "devtools-server"), "launchctl kickstart gui/501/"+LaunchdLabel; got != want {
		t.Errorf("darwin hint %q, want %q", got, want)
	}
	if got, want := RestartArgv(77), restartArgv(runtime.GOOS, 77, "devtools-server"); !slices.Equal(got, want) {
		t.Errorf("RestartArgv on %s = %q, want %q", runtime.GOOS, got, want)
	}
	if ServiceName() != "devtools-server" {
		t.Errorf("ServiceName without the variable = %q", ServiceName())
	}
}

func TestTheServiceNameComesFromTheEnvironment(t *testing.T) {
	t.Setenv(EnvService, "devtools-rc")
	if got := ServiceName(); got != "devtools-rc" {
		t.Fatalf("ServiceName = %q, want devtools-rc", got)
	}
	argv := RestartArgv(501)
	switch runtime.GOOS {
	case "darwin":
		if argv[len(argv)-1] != "gui/501/org.nix-community.home.devtools-rc" {
			t.Errorf("RestartArgv = %q", argv)
		}
	default:
		if argv[len(argv)-1] != "devtools-rc.service" {
			t.Errorf("RestartArgv = %q", argv)
		}
	}
	if got := strings.Join(restartArgv("darwin", 501, ServiceName()), " "); !strings.HasSuffix(got, "org.nix-community.home.devtools-rc") {
		t.Errorf("darwin argv %q does not name the label of devtools-rc", got)
	}
	if got := strings.Join(restartArgv("linux", 501, ServiceName()), " "); !strings.HasSuffix(got, " devtools-rc.service") {
		t.Errorf("linux argv %q does not name the unit of devtools-rc", got)
	}
	if hint := StartHint(); !strings.HasSuffix(hint, "devtools-rc") || strings.Contains(hint, "devtools-server") {
		t.Errorf("StartHint = %q, want one that names devtools-rc", hint)
	}

	for _, bad := range []string{"Devtools-RC", "devtools_rc", "a b", "x/y", "rc;rm", "rc.service", "ü", ""} {
		t.Setenv(EnvService, bad)
		if got := ServiceName(); got != "devtools-server" {
			t.Errorf("DEVTOOLS_SERVICE=%q gave %q, want devtools-server", bad, got)
		}
		if argv := RestartArgv(501); strings.Contains(strings.Join(argv, " "), bad) && bad != "" {
			t.Errorf("DEVTOOLS_SERVICE=%q reached RestartArgv %q", bad, argv)
		}
		if hint := StartHint(); !strings.HasSuffix(hint, "devtools-server") {
			t.Errorf("DEVTOOLS_SERVICE=%q gave StartHint %q", bad, hint)
		}
	}
}

// Restart runs RestartArgv through the injection point; no test runs a
// service manager.
func TestRestartRunsTheRestartArgv(t *testing.T) {
	t.Setenv(EnvService, "devtools-rc")
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
