package platform

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// RestartArgv is systemctl --user restart devtools-server.service on Linux
// and launchctl kickstart -k gui/<uid>/org.nix-community.home.devtools-server
// on darwin (4.5).
func RestartArgv(uid int) []string {
	return restartArgv(runtime.GOOS, uid)
}

func restartArgv(goos string, uid int) []string {
	if goos == "darwin" {
		return []string{"launchctl", "kickstart", "-k", launchdTarget(uid)}
	}
	return []string{"systemctl", "--user", "restart", SystemdUnit}
}

// StartHint is the command a user runs to start the service, for error
// texts: systemctl --user start devtools-server on Linux, launchctl
// kickstart gui/<uid>/org.nix-community.home.devtools-server on darwin.
func StartHint() string {
	return startHint(runtime.GOOS, os.Getuid())
}

func startHint(goos string, uid int) string {
	if goos == "darwin" {
		return "launchctl kickstart " + launchdTarget(uid)
	}
	return "systemctl --user start " + strings.TrimSuffix(SystemdUnit, ".service")
}

func launchdTarget(uid int) string {
	return "gui/" + strconv.Itoa(uid) + "/" + LaunchdLabel
}

// runRestart runs the restart command; a test replaces it, so no test ever
// restarts a service.
var runRestart = func(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if text := strings.TrimSpace(out.String()); text != "" {
			return fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, text)
		}
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// Restart runs RestartArgv; the body of `devtools server restart` after the
// [y/N]. The error names the command and carries what it printed.
func Restart() error {
	return runRestart(RestartArgv(os.Getuid()))
}
