package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"devtools/internal/testkit"
)

// fakes are the programs the test binary becomes when it is started under
// their name from a machine's bin directory. fakemux_test.go adds the fake
// multiplexers.
var fakes = map[string]func(args []string) int{
	fakeSSH:   runFakeSSH,
	paneShell: runPaneShell,
}

// markVars are the variables by which a command finds out where it runs
// (4.5) and the other variables of devtools and of the multiplexers. The
// test process drops them first, so that nothing a test calls directly
// can reach a multiplexer of the developer.
var markVars = []string{
	"DEVTOOLS_PANE", "DEVTOOLS_SOCKET", "DEVTOOLS_TTY", envMultiplexer, "DEVTOOLS_CONFIG",
	"DEVTOOLS_ROOT", "DEVTOOLS_TRUST_TERMINAL", "DEVTOOLS_PASS_ENV",
	"TMUX", "TMUX_PANE", "TMUX_TMPDIR", "STY", "WINDOW",
}

// markPrefixes are prefixes of variables that only a multiplexer sets.
var markPrefixes = []string{"HERDR_", "ZELLIJ"}

func scrubEnv() {
	for _, name := range markVars {
		os.Unsetenv(name)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range markPrefixes {
			if strings.HasPrefix(name, p) {
				os.Unsetenv(name)
			}
		}
	}
}

func TestMain(m *testing.M) {
	if run, ok := fakes[progName(os.Args[0])]; ok {
		os.Exit(run(os.Args[1:]))
	}
	testkit.RunHelper()
	original := os.Environ()
	scrubEnv()
	flag.Parse()
	cfg, real, err := parseReal(func(name string) string { v, _ := testkit.LookupEnv(original, name); return v })
	if err == nil && real {
		err = enterReal(cfg, original, flag.Lookup("test.run").Value.String())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// enterReal switches this run to the real mode of 10.1.8, which the user
// starts; the harness never does it on
// its own, and no automated run sets DEVTOOLS_E2E_REAL. original is the
// environment the test binary was started with, run its -run pattern. It
// refuses a run that could select a test outside RealTests and a run from
// inside a multiplexer (a real run is started from a plain terminal);
// it sets the binary under test (DEVTOOLS_E2E_BIN, else the first devtools
// on PATH: the installed binary, never a build) and the
// environment of the real processes: the original one without the marks
// of a context.
func enterReal(cfg RealConfig, original []string, run string) error {
	if !realFilterOK(run) {
		return fmt.Errorf("e2e: %s=1 runs only %s: give -run '^(%s)$' or a part of it, not %q",
			EnvReal, strings.Join(RealTests, ", "), strings.Join(RealTests, "|"), run)
	}
	for _, name := range []string{"TMUX", "STY", "ZELLIJ", "HERDR_ENV", "DEVTOOLS_PANE"} {
		if v, _ := testkit.LookupEnv(original, name); v != "" {
			return fmt.Errorf("e2e: %s=1 runs from a plain terminal, and %s is set (9.2)", EnvReal, name)
		}
	}
	env := realProcessEnv(original)
	bin, _ := testkit.LookupEnv(original, EnvBin)
	if bin == "" {
		p, err := lookPath(env, devtools)
		if err != nil {
			return fmt.Errorf("e2e: %s=1 without %s: %w", EnvReal, EnvBin, err)
		}
		bin = p
	}
	if err := setBinary(bin); err != nil {
		return err
	}
	realMode, realOn, realEnv = cfg, true, env
	return nil
}

// realProcessEnv is original without the marks of a context (markVars and
// markPrefixes) and without the variables of this harness; it keeps
// DEVTOOLS_CONFIG and the rest of what the user's shell set: HOME, PATH,
// XDG_CONFIG_HOME, SSH_AUTH_SOCK.
func realProcessEnv(original []string) []string {
	keep := map[string]bool{"DEVTOOLS_CONFIG": true}
	var out []string
	for _, kv := range original {
		name, _, _ := strings.Cut(kv, "=")
		drop := slices.Contains(markVars, name) && !keep[name]
		for _, p := range markPrefixes {
			drop = drop || strings.HasPrefix(name, p)
		}
		drop = drop || strings.HasPrefix(name, "DEVTOOLS_E2E_") || name == testkit.HelperVar
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// run sets the binary under test, building it when DEVTOOLS_E2E_BIN is not
// set, and runs the tests. A failed build fails the package: no test of
// level 6 skips. In the real mode enterReal has set the binary.
func run(m *testing.M) (int, error) {
	if realOn {
		return m.Run(), nil
	}
	if bin := os.Getenv(EnvBin); bin != "" {
		if err := setBinary(bin); err != nil {
			return 1, err
		}
		return m.Run(), nil
	}
	dir, err := os.MkdirTemp("", "devtools-e2e-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, devtools)
	if err := build(bin); err != nil {
		return 1, err
	}
	if err := setBinary(bin); err != nil {
		return 1, err
	}
	// The second build of the live upgrade tests: the same source with
	// another version stamp, a file of its own.
	v2 := filepath.Join(dir, "v2", devtools)
	if err := build(v2, "-ldflags", "-X devtools/internal/cli.buildVersion="+versionV2); err != nil {
		return 1, err
	}
	binaryV2 = v2
	return m.Run(), nil
}

// build builds ./cmd/devtools of this checkout into out.
func build(out string, flags ...string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	args := append([]string{"build"}, flags...)
	cmd := exec.Command("go", append(args, "-o", out, "./cmd/devtools")...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("e2e: go build ./cmd/devtools failed: %v\n%s", err, b)
	}
	return nil
}

// moduleRoot is the directory of go.mod above the package directory.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("e2e: no go.mod above the package directory")
		}
		dir = parent
	}
}
