// Package e2e is level 6 of the test harness (10.1.8 of the plan): it drives
// the built devtools binary the way a terminal does, with two servers on one
// host standing in for two machines, and in every context a command can be
// typed in.
//
// A Machine is one devtools server with its own HOME and XDG_STATE_HOME
// under a short temporary directory. A Term is a fake outer terminal: a PTY
// whose master side feeds the real engine (internal/vt), which answers the
// queries of the program like the user's terminal does. A Link makes one
// machine reachable from another through a fake ssh on the PATH that runs
// "devtools bridge" against the other machine's state directory.
//
// The fake programs (ssh, the multiplexers, the pane shell) are the test
// binary itself, linked into a machine's bin directory under those names;
// TestMain (main_test.go) dispatches on the name it was started under.
// Every process the package starts gets a complete environment built from
// testkit.CleanEnv, never the developer's.
//
// It is a test-only package (import rule 8).
package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"devtools/internal/testkit"
)

// EnvBin names the binary under test. Unset, TestMain builds ./cmd/devtools
// into a temporary directory once per test run.
const EnvBin = "DEVTOOLS_E2E_BIN"

// EnvReal switches the suite to real machines (10.1.8): DEVTOOLS_E2E_REAL=1
// with DEVTOOLS_E2E_REMOTE=<alias> and DEVTOOLS_E2E_QUERY=<session label>.
// That mode is run only by the user or with a [GO] (9.4). In it the tests
// use the installed binary, the real ssh and the real server of each
// machine; the harness starts, stops and writes nothing of them.
const EnvReal = "DEVTOOLS_E2E_REAL"

// EnvRemote and EnvQuery are the other two variables of the real mode: the
// ssh alias of the other machine (3.5.1) and the label of the session the
// measurements switch to.
const (
	EnvRemote = "DEVTOOLS_E2E_REMOTE"
	EnvQuery  = "DEVTOOLS_E2E_QUERY"
)

// RealTests are the tests that accept the real mode, the read-and-switch
// tests of P3-05b. A run in the real mode selects only these with -run; any
// other filter fails the package before a test runs, and a test outside
// this list that asks for a machine in the real mode fails.
var RealTests = []string{"TestRemoteDiscoveryHeldAndCold", "TestSwitchLocalHeldCold", "TestInputLatencyRemote"}

// RealConfig is the configuration of the real mode.
type RealConfig struct {
	Remote string // DEVTOOLS_E2E_REMOTE: the ssh alias of the other machine
	Query  string // DEVTOOLS_E2E_QUERY: the label of the session to switch to
}

// realMode is the real mode of this run; TestMain sets it before any test
// runs. realOn is false in every isolated run, the default.
var (
	realMode RealConfig
	realOn   bool
	realEnv  []string // the environment of the processes of the real mode
)

// Real returns the configuration of the real mode and whether this run is
// one. A test of RealTests branches on it; every other test runs isolated.
func Real() (RealConfig, bool) { return realMode, realOn }

// parseReal reads the real mode from getenv. An unset or empty
// DEVTOOLS_E2E_REAL is the isolated default (false, nil). Any other value
// than "1", a missing or malformed alias, or a missing or unprintable query
// is an error: a half-given opt-in is refused, never guessed.
func parseReal(getenv func(string) string) (RealConfig, bool, error) {
	switch v := getenv(EnvReal); v {
	case "":
		return RealConfig{}, false, nil
	case "1":
	default:
		return RealConfig{}, false, fmt.Errorf("e2e: %s=%q: the real mode is %s=1", EnvReal, v, EnvReal)
	}
	c := RealConfig{Remote: getenv(EnvRemote), Query: getenv(EnvQuery)}
	if !validAlias(c.Remote) {
		return RealConfig{}, false, fmt.Errorf("e2e: %s=1 needs %s=<ssh alias of 3.5.1>, got %q", EnvReal, EnvRemote, c.Remote)
	}
	if strings.TrimSpace(c.Query) == "" || strings.ContainsFunc(c.Query, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return RealConfig{}, false, fmt.Errorf("e2e: %s=1 needs %s=<session label>, got %q", EnvReal, EnvQuery, c.Query)
	}
	return c, true, nil
}

// realFilterOK reports whether the -run pattern of a real run selects
// only tests of RealTests: a non-empty alternation of their exact names,
// optionally in "^(...)$" or "^...$". A
// pattern of any other shape could select an isolated test that would then
// meet the real servers, so it is refused.
func realFilterOK(pattern string) bool {
	p := strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
	if strings.HasPrefix(p, "(") && strings.HasSuffix(p, ")") {
		p = p[1 : len(p)-1]
	}
	if p == "" {
		return false
	}
	for _, name := range strings.Split(p, "|") {
		if !isRealTest(name) {
			return false
		}
	}
	return true
}

// isRealTest reports whether name (a top-level test name) is in RealTests.
func isRealTest(name string) bool {
	for _, n := range RealTests {
		if n == name {
			return true
		}
	}
	return false
}

// lookPath returns the first executable file named name in the PATH of
// env.
func lookPath(env []string, name string) (string, error) {
	path, _ := testkit.LookupEnv(env, "PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("e2e: %s is not on PATH", name)
}

// envMultiplexer is the variable that names a backend (4.5).
const envMultiplexer = "DEVTOOLS_MULTIPLEXER"

// binary is the absolute path of the devtools binary under test; TestMain
// sets it before any test runs.
var binary string

// Binary returns the absolute path of the devtools binary under test.
func Binary() string { return binary }

// setBinary sets the binary under test; it must be an absolute path to an
// executable file.
func setBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("e2e: the binary under test %q is not an absolute path", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("e2e: the binary under test: %w", err)
	}
	if st.IsDir() || st.Mode()&0o111 == 0 {
		return fmt.Errorf("e2e: the binary under test %s is not an executable file", path)
	}
	binary = path
	return nil
}

// The names of the programs in a machine's bin directory that the non-test
// files place there. The fake multiplexers are placed by fakemux_test.go.
const (
	fakeSSH   = "ssh"
	paneShell = "e2e-shell"
	devtools  = "devtools"
)

// progName returns the name the test binary was started under: the base
// name of argv[0] without the "-" of a login shell. TestMain dispatches on
// it.
func progName(argv0 string) string {
	return strings.TrimPrefix(filepath.Base(argv0), "-")
}

// findSelf returns the path a fake was started from: argv[0] when it holds a
// slash, else the first file of that name on PATH, which is the one the
// caller's PATH lookup found.
func findSelf(argv0 string) (string, error) {
	if strings.Contains(argv0, "/") {
		return filepath.Abs(argv0)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, argv0)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("e2e: %s is not on PATH", argv0)
}

// markerFile marks a machine directory; newMachine writes it.
const markerFile = "f/machine"

// rootOf returns the machine directory of a fake started from path: the
// parent of its bin directory.
func rootOf(path string) (string, error) {
	root := filepath.Dir(filepath.Dir(path))
	if _, err := os.Stat(filepath.Join(root, markerFile)); err != nil {
		return "", fmt.Errorf("e2e: %s is not inside a machine directory", path)
	}
	return root, nil
}

// selfRoot returns the machine directory of the fake program the test
// binary runs as, from its argv[0].
func selfRoot() (string, error) {
	self, err := findSelf(os.Args[0])
	if err != nil {
		return "", err
	}
	return rootOf(self)
}

// lockFile takes an exclusive lock on path, creating the file, and returns
// the function that releases it. The fakes serialise their state files
// with it.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// appendLine appends one line to the log at path in a single write, so
// that fakes running at the same time do not interleave their lines.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	return errors.Join(werr, f.Close())
}

// mergeEnv returns base with every "NAME=value" of extra added: an entry of
// extra replaces the one of the same name, and a later one wins over an
// earlier one. An entry "NAME" without "=" removes NAME.
func mergeEnv(base []string, extra ...string) []string {
	out := append([]string{}, base...)
	for _, kv := range extra {
		name, _, set := strings.Cut(kv, "=")
		kept := out[:0]
		for _, have := range out {
			if n, _, _ := strings.Cut(have, "="); n != name {
				kept = append(kept, have)
			}
		}
		out = kept
		if set {
			out = append(out, kv)
		}
	}
	return out
}

// valueOf returns the value of name in env, "" when it is not there.
func valueOf(env []string, name string) string {
	v, _ := testkit.LookupEnv(env, name)
	return v
}
