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

// defaultService is the service name when DEVTOOLS_SERVICE is not used.
const defaultService = "devtools-server"

// launchdPrefix is the label of a home-manager launchd agent without its
// name.
const launchdPrefix = "org.nix-community.home."

// ServiceName is DEVTOOLS_SERVICE when it is set and made of [a-z0-9-] only,
// else "devtools-server". RestartArgv, Restart and StartHint use the unit
// <name>.service and the label org.nix-community.home.<name> (4.5).
func ServiceName() string {
	if v := os.Getenv(EnvService); validServiceName(v) {
		return v
	}
	return defaultService
}

func validServiceName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// RestartArgv is systemctl --user restart <name>.service on Linux and
// launchctl kickstart -k gui/<uid>/org.nix-community.home.<name> on darwin.
func RestartArgv(uid int) []string {
	return restartArgv(runtime.GOOS, uid, ServiceName())
}

func restartArgv(goos string, uid int, name string) []string {
	if goos == "darwin" {
		return []string{"launchctl", "kickstart", "-k", launchdTarget(uid, name)}
	}
	return []string{"systemctl", "--user", "restart", name + ".service"}
}

// StartHint is the command a user runs to start the service, for error
// texts: systemctl --user start <name> on Linux, launchctl kickstart
// gui/<uid>/org.nix-community.home.<name> on darwin.
func StartHint() string {
	return startHint(runtime.GOOS, os.Getuid(), ServiceName())
}

func startHint(goos string, uid int, name string) string {
	if goos == "darwin" {
		return "launchctl kickstart " + launchdTarget(uid, name)
	}
	return "systemctl --user start " + name
}

func launchdTarget(uid int, name string) string {
	return "gui/" + strconv.Itoa(uid) + "/" + launchdPrefix + name
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
