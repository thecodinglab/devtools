package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/platform"
)

// Link makes machine b reachable from a under an ssh alias through a fake
// `ssh` on PATH that runs `devtools bridge` against b's state directory.
type Link struct {
	t     *testing.T
	a, b  *Machine
	alias string
}

// linkFile is what the fake ssh of machine a knows about one alias. It is
// a file, f/ssh/<alias>.json, because the fake runs in its own process.
type linkFile struct {
	Root    string `json:"root"`  // the machine directory of b
	Block   string `json:"block"` // a reason of 3.5.2, "" for none
	DelayMs int64  `json:"delay_ms"`
	// Shared: the shared connection of the alias (3.5.2) is up, so
	// "-O check" answers 0 and a bridge over it needs no login.
	Shared bool `json:"shared"`
}

// validAlias is the alias rule of 3.5.1.
func validAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > 64 {
		return false
	}
	for i, r := range alias {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if i > 0 {
			ok = ok || r == '.' || r == '_' || r == '-'
		}
		if !ok {
			return false
		}
	}
	return true
}

// Connect makes b reachable from a under alias: it places the fake ssh in
// a's bin directory and writes `machine <alias>` (with `connect` when
// connect is true) into a's config.
//
// In the real mode (Real) a is the real local machine, b is nil and alias
// is DEVTOOLS_E2E_REMOTE: the other machine is reached through the real
// ssh and the config the user started the run with, which already names
// it. Connect then places and writes nothing, and connect is not used.
func Connect(t *testing.T, a, b *Machine, alias string, connect bool) *Link {
	t.Helper()
	if !validAlias(alias) {
		t.Fatalf("e2e: Connect: %q is not an alias of 3.5.1", alias)
	}
	if realOn {
		if !a.IsReal() || b != nil || alias != realMode.Remote {
			t.Fatalf("e2e: %s=1: Connect(t, a, nil, %q, connect) with a from StartMachine is the one link of the real mode", EnvReal, realMode.Remote)
		}
		return &Link{t: t, a: a, alias: alias}
	}
	a.link(fakeSSH, self())
	l := &Link{t: t, a: a, b: b, alias: alias}
	l.write(func(f *linkFile) { f.Root = b.Root() })
	line := "machine " + alias
	if connect {
		line += " connect"
	}
	a.Config(line)
	return l
}

// Alias returns the alias b is reachable under.
func (l *Link) Alias() string { return l.alias }

func (l *Link) path() string { return linkPath(l.a.Root(), l.alias) }

func linkPath(root, alias string) string {
	return filepath.Join(root, "f", "ssh", alias+".json")
}

// write changes the link file under its lock.
func (l *Link) write(change func(*linkFile)) {
	l.t.Helper()
	if err := changeLink(l.a.Root(), l.alias, change); err != nil {
		l.t.Fatalf("e2e: link %s: %v", l.alias, err)
	}
}

// changeLink changes the link file of alias on the machine in root under
// its lock; the fake ssh changes it too, in its own process.
func changeLink(root, alias string, change func(*linkFile)) error {
	path := linkPath(root, alias)
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	f, _ := readLink(root, alias)
	change(&f)
	b, _ := json.Marshal(f)
	return os.WriteFile(path, b, 0o600)
}

// readLink reads the link file of alias on the machine in root.
func readLink(root, alias string) (linkFile, error) {
	var f linkFile
	b, err := os.ReadFile(linkPath(root, alias))
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(b, &f)
}

// Block makes the next attempts fail like reason, one of the reasons of
// 3.5.2 (auth, host-key, unreachable, no-devtools, no-server,
// incompatible, protocol); "" lets them through again. A running bridge is
// not touched: Cut ends it.
func (l *Link) Block(reason string) {
	l.t.Helper()
	l.refuseReal("Block")
	if _, ok := blockFailures[reason]; !ok && reason != "" {
		l.t.Fatalf("e2e: Block(%q): not a reason of 3.5.2", reason)
	}
	l.write(func(f *linkFile) { f.Block = reason })
}

// Delay makes the fake ssh wait d before it runs the bridge, like a slow
// connect; 0 removes the delay.
func (l *Link) Delay(d time.Duration) {
	l.t.Helper()
	l.refuseReal("Delay")
	l.write(func(f *linkFile) { f.DelayMs = d.Milliseconds() })
}

// Cut kills the fake ssh processes of this link, and with them the
// bridges they became: the link drops as when the network goes away.
func (l *Link) Cut() {
	l.t.Helper()
	l.refuseReal("Cut")
	for _, pid := range l.Pids() {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// Shared reports whether the shared connection of this link is up: a
// dial that could log in brought it up and nothing dropped it since.
func (l *Link) Shared() bool {
	l.t.Helper()
	l.refuseReal("Shared")
	f, _ := readLink(l.a.Root(), l.alias)
	return f.Shared
}

// DropShared ends the shared connection of this link, as an outage longer
// than ssh's 10 minutes of ServerAlive or the end of ControlPersist does:
// "-O check" fails from now on, the next dial that may log in logs in
// again, and the bridges that ran over it end (Cut).
func (l *Link) DropShared() {
	l.t.Helper()
	l.refuseReal("DropShared")
	l.write(func(f *linkFile) { f.Shared = false })
	l.Cut()
}

// Logins returns how many attempts of this link logged in so far: each
// one an ssh login, which with the user's agent would ask for Touch ID.
// A bridge over the shared connection and "-O check" are not logins.
func (l *Link) Logins() int {
	b, _ := os.ReadFile(loginsPath(l.a.Root(), l.alias))
	return len(strings.Fields(string(b)))
}

// Pids returns the pids of the fake ssh processes of this link that still
// run.
func (l *Link) Pids() []int {
	l.t.Helper()
	if l.b == nil {
		return nil // the real link: no fake ssh runs
	}
	b, _ := os.ReadFile(pidsPath(l.a.Root(), l.alias))
	want := map[int]bool{}
	for _, f := range strings.Fields(string(b)) {
		if pid, err := strconv.Atoi(f); err == nil {
			want[pid] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	procs, err := platform.Procs()
	if err != nil {
		l.t.Fatalf("e2e: the process table: %v", err)
	}
	var out []int
	for _, p := range procs {
		// A pid of the file is ours only while the process is the fake or
		// the bridge it became; a pid the system reused is left alone.
		if want[p.Pid] && len(p.Args) > 0 && (p.Args[0] == l.b.Bin(devtools) || p.Args[0] == l.a.Bin(fakeSSH) || filepath.Base(p.Args[0]) == fakeSSH) {
			out = append(out, p.Pid)
		}
	}
	return out
}

// pidsPath lists the pid of every attempt of the fake ssh for alias.
func pidsPath(root, alias string) string {
	return filepath.Join(root, "f", "ssh", alias+".pids")
}

// loginsPath lists the pid of every attempt that logged in.
func loginsPath(root, alias string) string {
	return filepath.Join(root, "f", "ssh", alias+".logins")
}

// bridgesPath lists the pid of every attempt that became the bridge.
func bridgesPath(root, alias string) string {
	return filepath.Join(root, "f", "ssh", alias+".bridges")
}

// Bridges returns how many attempts of this link became `devtools bridge`
// of b so far.
func (l *Link) Bridges() int {
	b, _ := os.ReadFile(bridgesPath(l.a.Root(), l.alias))
	return len(strings.Fields(string(b)))
}

// SSHLog returns every argument list the fake ssh of this machine received,
// in order, without the program name.
func (m *Machine) SSHLog() [][]string {
	s := m.state()
	s.t.Helper()
	lists, err := readLog(m.fakeDir("ssh.log"))
	if err != nil {
		s.t.Fatalf("e2e: machine %s: the ssh log: %v", m.Name, err)
	}
	return lists
}

// readLog reads a log of argument lists, one JSON array per line; a
// missing log is empty.
func readLog(path string) ([][]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out [][]string
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			return nil, fmt.Errorf("%s:%d: %v", path, i+1, err)
		}
		out = append(out, args)
	}
	return out, nil
}

// logArgs appends one argument list to the log at path.
func logArgs(path string, args []string) error {
	if args == nil {
		args = []string{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return appendLine(path, b)
}

// refuseReal fails the test when l is the link of the real mode: the real
// ssh is not the harness's to block, delay or cut, and only the
// read-and-switch tests run in that mode.
func (l *Link) refuseReal(what string) {
	l.t.Helper()
	if l.b == nil {
		l.t.Fatalf("e2e: %s=1: %s is not available on the real link to %s", EnvReal, what, l.alias)
	}
}
