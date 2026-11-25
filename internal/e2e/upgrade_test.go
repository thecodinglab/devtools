package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/ui"
)

// The live upgrade of the server (PLAN-live-upgrade.md 9.3): an isolated
// server, programs in its panes, an upgrade to a second build in the same
// process, and everything checked from outside: the same pid, the same
// boot, the screens, the programs' pids, input, the modes.

// versionV2 is the version stamped into the second build (main_test.go).
const versionV2 = "0.0.0+e2e-b"

// binaryV2 is the second build; TestMain builds it next to the first.
var binaryV2 string

// The fake service managers: a restart of the service by a test must
// never reach the user's launchd or systemd. Each logs its arguments and
// fails.
const (
	fakeLaunchctl = "launchctl"
	fakeSystemctl = "systemctl"
)

func init() {
	fakes[fakeLaunchctl] = runFakeServiceManager(fakeLaunchctl)
	fakes[fakeSystemctl] = runFakeServiceManager(fakeSystemctl)
}

func runFakeServiceManager(name string) func(args []string) int {
	return func(args []string) int {
		if root, err := selfRoot(); err == nil {
			logArgs(filepath.Join(root, "f", name+".log"), args)
		}
		return 1
	}
}

// guardServices links the fake service managers first on the machine's
// PATH and, when the test ends, fails it if any was called.
func guardServices(t *testing.T, m *Machine) {
	t.Helper()
	for _, name := range []string{fakeLaunchctl, fakeSystemctl} {
		m.link(name, self())
	}
	t.Cleanup(func() {
		for _, name := range []string{fakeLaunchctl, fakeSystemctl} {
			if log, _ := readLog(m.fakeDir(name + ".log")); len(log) > 0 {
				t.Errorf("e2e: %s was called: %q", name, log)
			}
		}
	})
}

// installBuild points the machine's devtools at target, as a switch of the
// profile moves its symlink: a new link renamed over the old one.
func installBuild(t *testing.T, m *Machine, target string) {
	t.Helper()
	tmp := m.Bin(devtools) + ".new"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, m.Bin(devtools)); err != nil {
		t.Fatal(err)
	}
}

func needV2(t *testing.T) {
	t.Helper()
	if binaryV2 == "" {
		t.Skip("no second build: DEVTOOLS_E2E_BIN names the binary under test")
	}
}

// upgradeScene is a machine with a session of two panes: the left one has
// bracketed paste on and printed a mark, the right one printed a mark on
// its primary screen and then one on the alternate screen, where it stays.
type upgradeScene struct {
	m               *Machine
	x               *Term
	left, right     int    // the shells' pids
	leftID, rightID string // the panes
	pid             int    // the server's
	w               proto.Welcome
}

func newUpgradeScene(t *testing.T, env ...string) upgradeScene {
	t.Helper()
	m := newMachine(t, "desktop")
	guardServices(t, m)
	m.AddEnv(env...)
	m.Start()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server log:\n%s", m.ServerLog())
		}
	})
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 100, 30), "alpha")
	x.Type("echo before-left-$((6*7))\r")
	waitText(t, x, 10*time.Second, "before-left-42")
	left := shellPid(t, x, "left")
	// read takes one raw line; the mode set before it stays (no line
	// editor of the shell turns it off while read runs), and the line comes
	// back with ESC shown as ^[ (shell words only: there is no cat on the
	// PATH of a NixOS machine).
	x.Type("printf '\\033[?2004h'; IFS= read -r l; e=$(printf '\\033'); printf 'got:%s\\n' \"${l//$e/^[}\"\r")
	time.Sleep(300 * time.Millisecond)
	x.Key("prefix+%")
	waitText(t, x, 10*time.Second, glyph(ui.GlyphDividerV))
	right := shellPid(t, x, "right")
	x.Type("echo before-right-$((6*7)); printf '\\033[?1049h'; echo in-alt-$((6*7))\r")
	waitText(t, x, 10*time.Second, "in-alt-42")
	if left == right || left == 0 {
		t.Fatalf("shells %d and %d", left, right)
	}
	s := upgradeScene{m: m, x: x, left: left, right: right, pid: m.ServerPid(), w: m.Welcome()}
	for _, p := range snapshot(t, m).Panes {
		switch text := paneText(t, m, p.ID); {
		case strings.Contains(text, "in-alt-42"):
			s.rightID = p.ID
		case strings.Contains(text, "before-left-42"):
			s.leftID = p.ID
		}
	}
	if s.leftID == "" || s.rightID == "" {
		t.Fatalf("the panes of the scene: left %q, right %q", s.leftID, s.rightID)
	}
	return s
}

// paneText is the text of the screen and the history of a pane, as the
// server has it (pane.text).
func paneText(t *testing.T, m *Machine, id string) string {
	t.Helper()
	_, r := m.Oneshot(proto.PaneText{Pane: id, From: proto.Pos{Line: 0}, To: proto.Pos{Line: 1 << 40, Col: 1 << 16}})
	if !r.OK {
		t.Fatalf("pane.text of %s: %s %s", id, r.Code, r.Text)
	}
	var res proto.PaneTextResult
	if err := r.Msg.Decode(&res); err != nil {
		t.Fatal(err)
	}
	return res.Text
}

// kept checks what a live upgrade promises, after it: the same server
// process and boot, both shells, the right pane still on the alternate
// screen, input reaching both, a paste into the left one bracketed.
func (s upgradeScene) kept(t *testing.T, version string) {
	t.Helper()
	m, x := s.m, s.x
	if pid := m.ServerPid(); pid != s.pid {
		t.Fatalf("the server is pid %d, was %d", pid, s.pid)
	}
	w := m.Welcome()
	if w.Boot != s.w.Boot || w.Pid != s.pid || w.Version != version {
		t.Fatalf("welcome after the upgrade %+v, before %+v", w, s.w)
	}
	// Before any input reaches the upgraded server, its panes show what
	// they showed before: the right one its alternate screen (not the
	// primary one under it), the left one its output.
	if text := paneText(t, m, s.rightID); !strings.Contains(text, "in-alt-42") || strings.Contains(text, "before-right-42") {
		t.Errorf("the right pane after the upgrade, before any input:\n%s", text)
	}
	if text := paneText(t, m, s.leftID); !strings.Contains(text, "before-left-42") {
		t.Errorf("the left pane after the upgrade, before any input:\n%s", text)
	}
	// The window reconnects by itself (its link retries after half a
	// second); keys typed while it is down are dropped, as after any lost
	// link. Then output that did not exist before the upgrade arrives in
	// it, from the same shell, on the alternate screen.
	eventually(t, 20*time.Second, "the window reconnected", func() bool { return m.Welcome().Clients >= 1 })
	waitText(t, x, 10*time.Second, "in-alt-42")
	typed := false
	for i := 0; i < 10 && !typed; i++ {
		x.Type("echo after-right-$((6*7))\r")
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && !typed {
			typed = strings.Contains(strings.Join(x.Screen(), "\n"), "after-right-42")
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitText(t, x, 5*time.Second, "after-right-42")
	if got := shellPid(t, x, "right-after"); got != s.right {
		t.Errorf("the right pane's shell is %d, was %d", got, s.right)
	}
	snap := snapshot(t, m)
	alt := false
	for _, p := range snap.Panes {
		if p.State != proto.PaneRunning {
			t.Errorf("pane %s is %s after the upgrade", p.ID, p.State)
		}
	}
	x.Type("printf '\\033[?1049l'; echo left-the-alt-$((6*7))\r")
	waitText(t, x, 10*time.Second, "left-the-alt-42")
	alt = !strings.Contains(strings.Join(x.Screen(), "\n"), "in-alt-42")
	if !alt {
		t.Errorf("the right pane was not on the alternate screen after the upgrade:\n%s", strings.Join(x.Screen(), "\n"))
	}
	// Leaving it shows the primary screen of before the upgrade again.
	if !strings.Contains(strings.Join(x.Screen(), "\n"), "before-right-42") || !strings.Contains(paneText(t, m, s.rightID), "before-right-42") {
		t.Errorf("the right pane's primary screen was lost:\n%s", strings.Join(x.Screen(), "\n"))
	}
	// The left pane: its old output, the paste bracketed by the mode its
	// program set before the upgrade, its shell.
	x.Key("ctrl+h")
	if !strings.Contains(strings.Join(x.Screen(), "\n"), "before-left-42") {
		t.Errorf("the left pane lost its screen:\n%s", strings.Join(x.Screen(), "\n"))
	}
	x.Type("\x1b[200~pasted\x1b[201~\r")
	waitText(t, x, 10*time.Second, "got:^[[200~pasted^[[201~")
	if got := shellPid(t, x, "left-after"); got != s.left {
		t.Errorf("the left pane's shell is %d, was %d", got, s.left)
	}
	// A program the upgraded server starts inherits none of the kept
	// descriptors (the masters, the lock, the listener, the payload, all
	// at 4096 and above): only its terminal and what the shell opens.
	x.Key("prefix+c")
	eventually(t, 10*time.Second, "a second tab", func() bool { return len(snapshot(t, m).Tabs) == 2 })
	x.Type("echo fds-$((6*7)): /dev/fd/*\r")
	var fds string
	waitUntil(t, x, 10*time.Second, "the new pane lists its descriptors", func(rows []string) bool {
		for _, row := range rows {
			if i := strings.Index(row, "fds-42: "); i >= 0 {
				fds = strings.TrimSpace(row[i+len("fds-42: "):])
				return true
			}
		}
		return false
	})
	t.Logf("the descriptors of a new pane: %s", fds)
	for _, f := range strings.Fields(fds) {
		if n, err := strconv.Atoi(strings.TrimPrefix(f, "/dev/fd/")); err != nil || n >= 256 {
			t.Errorf("a program started after the upgrade holds descriptor %s (of %s)", f, fds)
		}
	}
}

func TestLiveUpgradeKeepsPrograms(t *testing.T) {
	needV2(t)
	s := newUpgradeScene(t)
	installBuild(t, s.m, binaryV2)
	r := s.m.RunWith(RunOptions{}, "server", "restart")
	if r.Exit != 0 {
		t.Fatalf("server restart: exit %d\n%s%s\nlog:\n%s", r.Exit, r.Stdout, r.Stderr, s.m.ServerLog())
	}
	if !strings.Contains(r.Stdout, "Upgrading the devtools server from "+s.w.Version+" to "+versionV2+"; 2 programs keep running.") ||
		!strings.Contains(r.Stdout, ui.Upgrade("upgraded", versionV2, "2")) {
		t.Fatalf("server restart said:\n%s", r.Stdout)
	}
	s.kept(t, versionV2)
	if w := s.m.Welcome(); w.Upgraded == nil || w.Upgraded.Kept != 2 || w.Upgraded.Error != "" || w.Upgraded.To != versionV2 {
		t.Errorf("welcome.upgraded %+v", w.Upgraded)
	}
	waitText(t, s.x, 20*time.Second, ui.Notice("upgraded", s.w.Host, versionV2))
	// And back to the first build: an upgrade in the other direction.
	installBuild(t, s.m, Binary())
	if r := s.m.RunWith(RunOptions{}, "server", "restart"); r.Exit != 0 {
		t.Fatalf("server restart back: exit %d\n%s%s\nlog:\n%s", r.Exit, r.Stdout, r.Stderr, s.m.ServerLog())
	}
	if w := s.m.Welcome(); w.Version != s.w.Version || w.Pid != s.pid || w.Boot != s.w.Boot {
		t.Fatalf("welcome after the way back %+v", w)
	}
	// SIGTERM ends the upgraded server in order.
	start := time.Now()
	s.m.Stop()
	if d := time.Since(start); d > 15*time.Second {
		t.Errorf("the upgraded server took %v to stop", d)
	}
}

func TestLiveUpgradeSameBuildForced(t *testing.T) {
	s := newUpgradeScene(t, "DEVTOOLS_UPGRADE_FORCE=1")
	r := s.m.RunWith(RunOptions{}, "server", "restart")
	if r.Exit != 0 || !strings.Contains(r.Stdout, "already runs the installed build") {
		t.Fatalf("server restart of the running build: exit %d\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	if s.m.ServerPid() != s.pid || s.m.Welcome().Upgraded != nil {
		t.Fatalf("nothing should have happened: %+v", s.m.Welcome())
	}
	r = s.m.RunWith(RunOptions{}, "server", "restart", "--force")
	if r.Exit != 0 {
		t.Fatalf("server restart --force: exit %d\n%s%s\nlog:\n%s", r.Exit, r.Stdout, r.Stderr, s.m.ServerLog())
	}
	s.kept(t, s.w.Version)
}

func TestLiveUpgradeAutomatic(t *testing.T) {
	needV2(t)
	s := newUpgradeScene(t, "DEVTOOLS_UPGRADE_FORCE=1", "DEVTOOLS_AUTO_UPGRADE=1",
		"DEVTOOLS_UPGRADE_SETTLE=1s", "DEVTOOLS_UPGRADE_INTERVAL=200ms", "DEVTOOLS_UPGRADE_IDLE=500ms",
		"DEVTOOLS_UPGRADE_TARGET_DIR="+filepath.Dir(binaryV2))
	installBuild(t, s.m, binaryV2)
	// Polled without failing on a connection the exec ends (one that was
	// accepted right before it): a window retries such a link.
	eventually(t, 30*time.Second, "the server upgrades itself", func() bool {
		w, _, err := oneshot(s.m.Socket(), nil, 2*time.Second)
		return err == nil && w.Version == versionV2
	})
	s.kept(t, versionV2)
	// The watch that found the candidate is the one that tells about an
	// installed update: the upgraded server runs it, so none waits, from
	// its first welcome on.
	if w := s.m.Welcome(); w.Installed != "" {
		t.Errorf("welcome.installed after the upgrade %q", w.Installed)
	}
	r := s.m.RunWith(RunOptions{}, "server", "status", "--json")
	var st struct {
		Version  string          `json:"version"`
		Pending  bool            `json:"restart_pending"`
		Upgraded *proto.Upgraded `json:"upgraded"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &st); err != nil || st.Version != versionV2 || st.Pending || st.Upgraded == nil || st.Upgraded.To != versionV2 {
		t.Errorf("server status --json after the upgrade: %+v, %v\n%s%s", st, err, r.Stdout, r.Stderr)
	}
}

func TestLiveUpgradeExecBack(t *testing.T) {
	needV2(t)
	s := newUpgradeScene(t, "DEVTOOLS_UPGRADE_FORCE=1", "DEVTOOLS_UPGRADE_FAIL_ADOPT=1")
	installBuild(t, s.m, binaryV2)
	r := s.m.RunWith(RunOptions{}, "server", "restart")
	if r.Exit != 1 || !strings.Contains(r.Stdout, "the old build runs again") {
		t.Fatalf("server restart with a new build that refuses: exit %d\n%s%s\nlog:\n%s", r.Exit, r.Stdout, r.Stderr, s.m.ServerLog())
	}
	if w := s.m.Welcome(); w.Upgraded == nil || !w.Upgraded.Back {
		t.Fatalf("welcome.upgraded %+v", w.Upgraded)
	}
	s.kept(t, s.w.Version)
	b, _ := os.ReadFile(filepath.Join(s.m.StateDir, "upgrade.json"))
	if !strings.Contains(string(b), `"fallback"`) {
		t.Errorf("the journal:\n%s", b)
	}
}

func TestLiveUpgradeColdRecovery(t *testing.T) {
	needV2(t)
	s := newUpgradeScene(t, "DEVTOOLS_UPGRADE_FORCE=1", "DEVTOOLS_UPGRADE_FAIL_ADOPT=both")
	installBuild(t, s.m, binaryV2)
	r := s.m.RunWith(RunOptions{}, "server", "restart")
	if r.Exit != 1 || !strings.Contains(r.Stdout, "started cold") {
		t.Fatalf("server restart when neither build adopts: exit %d\n%s%s\nlog:\n%s", r.Exit, r.Stdout, r.Stderr, s.m.ServerLog())
	}
	w := s.m.Welcome()
	if w.Boot == s.w.Boot || w.Pid != s.pid || w.Upgraded == nil || !w.Upgraded.Cold {
		t.Fatalf("welcome after the cold recovery %+v", w)
	}
	// The window sees a restarted server, and new shells.
	waitText(t, s.x, 20*time.Second, ui.Notice("restarted", s.w.Host))
	waitUntil(t, s.x, 10*time.Second, "the split tab is shown again", func(rows []string) bool {
		return strings.Contains(strings.Join(rows, "\n"), glyph(ui.GlyphDividerV))
	})
	if got := shellPid(t, s.x, "cold"); got == s.left || got == s.right {
		t.Errorf("a shell of before the cold start, %d, still runs", got)
	}
	// No program of the old panes is left over: they were the server's
	// children and were ended.
	for _, pid := range []int{s.left, s.right} {
		eventually(t, 10*time.Second, "the old shells ended", func() bool { return !alive(pid) })
	}
}

// alive reports whether pid is a process (kill 0).
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
