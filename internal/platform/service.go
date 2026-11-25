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

// StartArgv starts the service when it is stopped and does nothing to one
// that runs: systemctl --user start devtools-server.service on Linux,
// launchctl kickstart gui/<uid>/org.nix-community.home.devtools-server
// (without -k) on darwin.
func StartArgv(uid int) []string {
	return startArgv(runtime.GOOS, uid)
}

func startArgv(goos string, uid int) []string {
	if goos == "darwin" {
		return []string{"launchctl", "kickstart", launchdTarget(uid)}
	}
	return []string{"systemctl", "--user", "start", SystemdUnit}
}

// Start runs StartArgv: devtools server restart when no server answers and
// the service manager runs none, so nothing that runs is ended.
func Start() error {
	return runRestart(StartArgv(os.Getuid()))
}

// ServicePidArgv is the read-only question for the pid of the running
// service: systemctl --user show -p MainPID --value devtools-server.service
// on Linux, launchctl print gui/<uid>/<label> on darwin.
func ServicePidArgv(uid int) []string {
	return servicePidArgv(runtime.GOOS, uid)
}

func servicePidArgv(goos string, uid int) []string {
	if goos == "darwin" {
		return []string{"launchctl", "print", launchdTarget(uid)}
	}
	return []string{"systemctl", "--user", "show", "-p", "MainPID", "--value", SystemdUnit}
}

// runQuery runs a read-only command of the service manager and returns its
// output; a test replaces it, so no test asks a service manager.
var runQuery = func(argv []string) ([]byte, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return out, nil
}

// ServicePid is the pid of the process the service manager runs as the
// devtools service, 0 when it runs none. The restart that ends every
// program asks it, so that it restarts the service only when the server
// that answered is the service's (a server started by hand, or by a test,
// is not).
func ServicePid() (int, error) {
	out, err := runQuery(ServicePidArgv(os.Getuid()))
	if err != nil {
		return 0, err
	}
	return parseServicePid(runtime.GOOS, string(out))
}

func parseServicePid(goos, out string) (int, error) {
	if goos != "darwin" {
		return strconv.Atoi(strings.TrimSpace(out))
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == "pid" {
			return strconv.Atoi(strings.TrimSpace(v))
		}
	}
	return 0, nil
}
