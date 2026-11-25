package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/handover"
	"devtools/internal/platform"
	"devtools/internal/proto"
)

// The live upgrade under the real service managers (PLAN-live-upgrade.md
// 6.4, 9.4): the server runs as a disposable launchd job (macOS) or a
// transient systemd user unit (Linux) with the settings of nix/service.nix,
// its own label, HOME and state directory, and the program is a symlink in
// the test's directory that stands in for the profile's devtools; pointing
// it at another build is the install. Opt-in: DEVTOOLS_E2E_SERVICE=1. The
// production job (org.nix-community.home.devtools-server,
// devtools-server.service) and the user's state are never touched: the
// tests refuse a name that is the production one, and every job they
// create is removed when the test ends, also when it fails.

// envService turns the tests of this file on.
const envService = "DEVTOOLS_E2E_SERVICE"

// autoUpgradeByDefault is whether the server upgrades itself without
// DEVTOOLS_AUTO_UPGRADE in its environment (internal/server/boot). The
// tests run the default; while it is off they turn it on, to test it.
const autoUpgradeByDefault = true

// servicePrefix starts the name of every job of these tests.
const (
	launchdPrefix = "org.devtools-e2e."
	systemdPrefix = "devtools-e2e-"
)

// The service settings of nix/service.nix the tests reproduce.
const (
	serviceStopTimeout = 10 * time.Second // TimeoutStopSec, and launchd's ExitTimeOut
)

// cliName is the devtools the tests run commands with: the first build,
// whatever the profile's symlink points at.
const cliName = "devtools-cli"

func needService(t *testing.T) {
	t.Helper()
	if os.Getenv(envService) != "1" {
		t.Skipf("%s=1 runs the live upgrade under the real service manager", envService)
	}
	needV2(t)
	switch runtime.GOOS {
	case "darwin":
	case "linux":
		if os.Getenv("XDG_RUNTIME_DIR") == "" {
			t.Fatalf("%s=1 on Linux needs the user's systemd (XDG_RUNTIME_DIR is not set)", envService)
		}
	default:
		t.Skipf("no service manager on %s", runtime.GOOS)
	}
}

// serviceJob is one disposable job of the service manager.
type serviceJob struct {
	t    *testing.T
	m    *Machine
	name string   // the launchd label, or the systemd unit with .service
	env  []string // its environment
	up   bool     // created and not stopped by the test
}

// jobStatus is what the service manager says about a job.
type jobStatus struct {
	Pid   int    // 0: no process
	Runs  int    // launchd runs, systemd NRestarts
	State string // launchd state, systemd ActiveState/SubState/Result
}

func randomName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// production reports whether name is, or could be taken for, the job of the
// user's real server.
func production(name string) bool {
	trimmed := strings.TrimSuffix(name, ".service")
	return name == platform.LaunchdLabel || name == platform.SystemdUnit || trimmed == strings.TrimSuffix(platform.SystemdUnit, ".service") ||
		!(strings.HasPrefix(name, launchdPrefix) || strings.HasPrefix(name, systemdPrefix))
}

// launchctl and systemctl are the real tools, never the fakes of the
// machine's PATH.
func launchctl(args ...string) ([]byte, error) {
	return exec.Command("/bin/launchctl", args...).CombinedOutput()
}

func systemTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return "/run/current-system/sw/bin/" + name
}

func systemctl(args ...string) ([]byte, error) {
	return exec.Command(systemTool("systemctl"), append([]string{"--user"}, args...)...).CombinedOutput()
}

// newServiceMachine makes a machine whose server the service manager runs,
// with the knobs that shorten the automatic upgrade (and widen its target
// policy to the builds of this run, through the test-only knob) and env.
func newServiceMachine(t *testing.T, env ...string) (*Machine, *serviceJob) {
	t.Helper()
	needService(t)
	m := newMachine(t, "desktop")
	guardServices(t, m)
	m.link(cliName, binary)
	m.link(fakeFloodName, self())
	j := &serviceJob{t: t, m: m}
	if runtime.GOOS == "darwin" {
		j.name = launchdPrefix + randomName()
	} else {
		j.name = systemdPrefix + randomName() + ".service"
	}
	if production(j.name) {
		t.Fatalf("e2e: refusing the job %q: it is or could be the production one", j.name)
	}
	j.env = serviceEnv(m, env...)
	s := m.state()
	s.mu.Lock()
	s.svcPid = func() int { return j.status().Pid }
	s.svcLog = j.log
	s.mu.Unlock()
	t.Cleanup(j.cleanup)
	j.start()
	return m, j
}

// serviceEnv is the environment of the job, in the shape of nix/service.nix:
// the machine's (a throwaway HOME, XDG_STATE_HOME, XDG_CONFIG_HOME,
// DEVTOOLS_CONFIG, the pane shell as SHELL, PATH with the machine's bin
// directory first) and the tests' knobs, as programs.devtools.server.
// environment would carry them, and DEVTOOLS_PASS_ENV naming them.
func serviceEnv(m *Machine, extra ...string) []string {
	base := []string{
		// named, so that nothing the manager's own environment holds can
		// point the job at another socket
		"DEVTOOLS_SOCKET=" + m.Socket(),
		"XDG_CONFIG_HOME=" + filepath.Join(m.Home, ".config"),
		"DEVTOOLS_CONFIG=" + m.ConfigPath(),
		"DEVTOOLS_UPGRADE_FORCE=1",
		"DEVTOOLS_UPGRADE_SETTLE=1s",
		"DEVTOOLS_UPGRADE_INTERVAL=200ms",
		"DEVTOOLS_UPGRADE_IDLE=500ms",
		"DEVTOOLS_UPGRADE_TARGET_DIR=" + filepath.Dir(Binary()),
	}
	if !autoUpgradeByDefault {
		base = append(base, "DEVTOOLS_AUTO_UPGRADE=1")
	}
	env := mergeEnv(m.Env(), append(base, extra...)...)
	var names []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name != "PATH" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return append(env, "DEVTOOLS_PASS_ENV="+strings.Join(names, ","))
}

// start creates the job and waits until its server answers.
func (j *serviceJob) start() {
	t, m := j.t, j.m
	t.Helper()
	if production(j.name) {
		t.Fatalf("e2e: refusing to start %q", j.name)
	}
	argv := []string{m.Bin(devtools), "server"}
	switch runtime.GOOS {
	case "darwin":
		plist := filepath.Join(m.Root(), "f", j.name+".plist")
		if err := os.WriteFile(plist, launchdPlist(j.name, argv, j.env, m.fakeDir("server.log")), 0o600); err != nil {
			t.Fatal(err)
		}
		j.up = true
		if out, err := launchctl("bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist); err != nil {
			t.Fatalf("launchctl bootstrap %s: %v\n%s", j.name, err, out)
		}
	default:
		args := []string{"--user", "--unit=" + j.name, "--description=devtools e2e server " + j.name,
			"-p", "Type=simple", "-p", "Restart=always", "-p", "RestartSec=1", "-p", "RestartSteps=3",
			"-p", "RestartMaxDelaySec=10", "-p", "StartLimitIntervalSec=0", "-p", "KillMode=mixed",
			"-p", "TimeoutStopSec=10", "-p", "OOMPolicy=continue"}
		for _, kv := range j.env {
			args = append(args, "--setenv="+kv)
		}
		args = append(args, "--")
		args = append(args, argv...)
		j.up = true
		if out, err := exec.Command(systemTool("systemd-run"), args...).CombinedOutput(); err != nil {
			t.Fatalf("systemd-run %s: %v\n%s", j.name, err, out)
		}
	}
	waitServing(t, m, 30*time.Second, "the job's server answers", func(proto.Welcome) bool { return true })
}

// launchdPlist is the agent of nix/service.nix with the test's label,
// program and environment.
func launchdPlist(label string, argv, env []string, stderr string) []byte {
	var b bytes.Buffer
	str := func(s string) string {
		var e bytes.Buffer
		xml.EscapeText(&e, []byte(s))
		return "<string>" + e.String() + "</string>"
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\"><dict>\n")
	b.WriteString("<key>Label</key>" + str(label) + "\n")
	b.WriteString("<key>ProgramArguments</key><array>")
	for _, a := range argv {
		b.WriteString(str(a))
	}
	b.WriteString("</array>\n")
	b.WriteString("<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n")
	b.WriteString("<key>ProcessType</key>" + str("Interactive") + "\n")
	b.WriteString(fmt.Sprintf("<key>ExitTimeOut</key><integer>%d</integer>\n", int(serviceStopTimeout/time.Second)))
	b.WriteString("<key>EnvironmentVariables</key><dict>\n")
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		b.WriteString("<key>" + strings.TrimSuffix(strings.TrimPrefix(str(k), "<string>"), "</string>") + "</key>" + str(v) + "\n")
	}
	b.WriteString("</dict>\n")
	b.WriteString("<key>StandardErrorPath</key>" + str(stderr) + "\n")
	b.WriteString("</dict></plist>\n")
	return b.Bytes()
}

// status asks the service manager about the job.
func (j *serviceJob) status() jobStatus {
	var st jobStatus
	if runtime.GOOS == "darwin" {
		out, err := launchctl("print", fmt.Sprintf("gui/%d/%s", os.Getuid(), j.name))
		if err != nil {
			// exit 113: "Could not find service"; anything else is no answer
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 113 {
				return jobStatus{State: "gone"}
			}
			return jobStatus{State: "unknown: " + strings.TrimSpace(string(out))}
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
				continue
			}
			k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
			if !ok || seen[k] {
				continue
			}
			seen[k] = true
			switch k {
			case "pid":
				st.Pid, _ = strconv.Atoi(v)
			case "runs":
				st.Runs, _ = strconv.Atoi(v)
			case "state":
				st.State = v
			}
		}
		return st
	}
	out, err := systemctl("show", j.name, "-p", "MainPID", "-p", "NRestarts", "-p", "ActiveState", "-p", "SubState", "-p", "Result", "-p", "LoadState")
	if err != nil {
		return jobStatus{State: "unknown: " + strings.TrimSpace(string(out))}
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[k] = v
		}
	}
	if kv["LoadState"] == "not-found" {
		return jobStatus{State: "gone"}
	}
	st.Pid, _ = strconv.Atoi(kv["MainPID"])
	st.Runs, _ = strconv.Atoi(kv["NRestarts"])
	st.State = kv["ActiveState"] + "/" + kv["SubState"] + "/" + kv["Result"]
	return st
}

// gone reports whether the service manager has no job of this name.
func (j *serviceJob) gone() bool { return j.status().State == "gone" }

// log is what the job's servers wrote to standard error: the
// StandardErrorPath file of the agent, the journal of the unit.
func (j *serviceJob) log() string {
	if runtime.GOOS == "darwin" {
		b, _ := os.ReadFile(j.m.fakeDir("server.log"))
		return string(b)
	}
	out, _ := exec.Command(systemTool("journalctl"), "--user", "-u", j.name, "-o", "cat", "--no-pager").CombinedOutput()
	return string(out)
}

// stop stops the job as the service manager does (launchctl bootout,
// systemctl --user stop) and returns how long that took.
func (j *serviceJob) stop() time.Duration {
	start := time.Now()
	var out []byte
	var err error
	if runtime.GOOS == "darwin" {
		out, err = launchctl("bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), j.name))
	} else {
		out, err = systemctl("stop", j.name)
		systemctl("reset-failed", j.name)
	}
	// launchctl bootout returns before the job ended: the stop is over
	// when the manager has no job and no server of the machine runs
	if testkit_poll(30*time.Second, func() bool { return j.gone() && len(machineServers(j.m)) == 0 }) {
		j.up = false
	} else {
		j.t.Errorf("e2e: the stop of %s did not end it (%v: %s): %+v", j.name, err, strings.TrimSpace(string(out)), j.status())
	}
	return time.Since(start)
}

// cleanup removes the job, also after a failure, checks that it is gone
// and that no server or pane program of the machine is left.
func (j *serviceJob) cleanup() {
	t, m := j.t, j.m
	if t.Failed() {
		t.Logf("job %s: %+v\nserver log:\n%s", j.name, j.status(), j.log())
	}
	if j.up || !j.gone() {
		j.stop()
	}
	if !testkit_poll(10*time.Second, j.gone) {
		t.Errorf("e2e: the job %s is still there after its stop: %+v", j.name, j.status())
	}
	// no server of this machine may be left
	if !testkit_poll(10*time.Second, func() bool { return len(machineServers(m)) == 0 }) {
		for _, pid := range machineServers(m) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("e2e: a server of %s outlived its job; killed", m.Root())
	}
}

// machineServers are the pids of the processes that run `devtools server`
// through the machine's program.
func machineServers(m *Machine) []int {
	procs, err := platform.Procs()
	if err != nil {
		return nil
	}
	var pids []int
	for _, p := range procs {
		if len(p.Args) >= 2 && p.Args[0] == m.Bin(devtools) && p.Args[1] == "server" {
			pids = append(pids, p.Pid)
		}
	}
	return pids
}

// testkit_poll is poll without a test: whether cond became true within d.
func testkit_poll(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitServing polls the welcome of m's server until ok accepts it; a
// connection the exec ends or the backlog holds is no failure.
func waitServing(t *testing.T, m *Machine, d time.Duration, what string, ok func(proto.Welcome) bool) proto.Welcome {
	t.Helper()
	var got proto.Welcome
	eventually(t, d, what, func() bool {
		w, _, err := oneshot(m.Socket(), nil, 2*time.Second)
		if err == nil && ok(w) {
			got = w
			return true
		}
		return false
	})
	return got
}

func journalOf(m *Machine) handover.Journal { return handover.LoadJournal(m.StateDir) }

// attemptsTo are the journal's attempts to the build at path.
func attemptsTo(m *Machine, path string) []handover.Attempt {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	var out []handover.Attempt
	for _, a := range journalOf(m).Attempts {
		if a.Target == resolved {
			out = append(out, a)
		}
	}
	return out
}

// cliStatus is `devtools server status --json` of the first build.
type cliStatus struct {
	Pid      int             `json:"pid"`
	Version  string          `json:"version"`
	Boot     string          `json:"boot"`
	Upgraded *proto.Upgraded `json:"upgraded"`
	Install  string          `json:"installed"`
	Pending  bool            `json:"restart_pending"`
}

func serverStatusOf(t *testing.T, m *Machine) (cliStatus, string) {
	t.Helper()
	r := m.RunBin(cliName, RunOptions{}, "server", "status", "--json")
	var st cliStatus
	if r.Exit != 0 || json.Unmarshal([]byte(r.Stdout), &st) != nil {
		t.Fatalf("server status --json: exit %d\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	text := m.RunBin(cliName, RunOptions{}, "server", "status")
	return st, text.Stdout
}

// writeBuild writes a script that stands in for a build into a directory
// of its own inside the target directory of the automatic upgrade.
func writeBuild(t *testing.T, name, script string) string {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Dir(Binary()), name+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, devtools)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func countLines(log, text string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, text) {
			n++
		}
	}
	return n
}

// (a) The automatic upgrade under the manager: the same process, as the
// manager sees it, and no restart.
func TestServiceAutoUpgradeKeepsPrograms(t *testing.T) {
	m, j := newServiceMachine(t)
	before := j.status()
	if before.Pid == 0 || before.Pid != m.Welcome().Pid {
		t.Fatalf("the job %+v, the server pid %d", before, m.Welcome().Pid)
	}
	s := sceneOn(t, m)
	installBuild(t, m, binaryV2)
	waitServing(t, m, 40*time.Second, "the server upgrades itself", func(w proto.Welcome) bool { return w.Version == versionV2 })
	s.kept(t, versionV2)
	after := j.status()
	if after.Pid != before.Pid || after.Runs != before.Runs {
		t.Errorf("the manager saw a change: before %+v, after %+v", before, after)
	}
	if w := m.Welcome(); w.Pid != after.Pid {
		t.Errorf("the server is pid %d, the manager's %d", w.Pid, after.Pid)
	}
	as := attemptsTo(m, binaryV2)
	if len(as) != 1 || !as[0].Auto || (as[0].Result != handover.Committed && as[0].Result != handover.OK) {
		t.Errorf("the journal's attempts to the new build: %+v", as)
	}
	t.Logf("job before %+v, after %+v", before, after)
}

// (b) A build that does not even start: the preflight refuses it, nothing
// is exec'd, and it is not tried again.
func TestServiceBrokenBuildIsRefused(t *testing.T) {
	m, j := newServiceMachine(t)
	before := j.status()
	s := sceneOn(t, m)
	broken := writeBuild(t, "broken", "#!/bin/sh\necho 'e2e: a build that does not start' >&2\nexit 1\n")
	installBuild(t, m, broken)
	eventually(t, 30*time.Second, "the preflight refuses the build", func() bool {
		return countLines(m.ServerLog(), "upgrade: preflight refused") >= 1
	})
	// many intervals of the watch and of the rate limit
	time.Sleep(5 * time.Second)
	if n := countLines(m.ServerLog(), "upgrade: preflight refused"); n != 1 {
		t.Errorf("the preflight ran %d times for the same build", n)
	}
	if n := countLines(m.ServerLog(), "upgrade: exec"); n != 0 {
		t.Errorf("an exec happened:\n%s", m.ServerLog())
	}
	after := j.status()
	w := m.Welcome()
	if after.Pid != before.Pid || after.Runs != before.Runs || w.Pid != before.Pid || w.Boot != s.w.Boot || w.Version != s.w.Version {
		t.Errorf("the server changed: job %+v then %+v, welcome %+v", before, after, w)
	}
	if w.Upgraded == nil || !strings.Contains(w.Upgraded.Error, "cannot take over") {
		t.Errorf("welcome.upgraded %+v", w.Upgraded)
	}
	st, text := serverStatusOf(t, m)
	if !st.Pending || st.Upgraded == nil || !strings.Contains(text, "failed") {
		t.Errorf("server status:\n%s(%+v)", text, st)
	}
	t.Logf("server status:\n%s", text)
	as := attemptsTo(m, broken)
	if len(as) != 1 || as[0].Result != handover.Refused || !strings.HasPrefix(as[0].Reason, "preflight") {
		t.Errorf("the journal's attempts to the broken build: %+v", as)
	}
	// the programs run on, and input reaches them
	s.x.Key("ctrl+l")
	if got := shellPid(t, s.x, "after-refusal"); got != s.right {
		t.Errorf("the right pane's shell is %d, was %d", got, s.right)
	}
}

// (c) A build that takes over and crashes before its health interval: the
// manager starts the profile's build (that one) again, the cold start
// marks the attempt crashed, the sessions come back as layout without
// their programs, and the automatic upgrade does not take it again.
func TestServiceCrashAfterAdopt(t *testing.T) {
	m, j := newServiceMachine(t, "DEVTOOLS_UPGRADE_CRASH="+versionV2)
	before := j.status()
	s := sceneOn(t, m)
	lay := layout(snapshot(t, m), "alpha")
	installBuild(t, m, binaryV2)
	w := waitServing(t, m, 60*time.Second, "the manager runs the crashed build again", func(w proto.Welcome) bool {
		return w.Version == versionV2 && w.Pid != s.pid && w.Boot != s.w.Boot
	})
	if countLines(m.ServerLog(), "crashing in a second") != 1 || countLines(m.ServerLog(), "upgrade: adopted") != 1 {
		t.Errorf("the new build did not adopt and crash once:\n%s", m.ServerLog())
	}
	after := j.status()
	if after.Pid != w.Pid || after.Runs != before.Runs+1 {
		t.Errorf("the job before %+v, after %+v, the server %d", before, after, w.Pid)
	}
	as := attemptsTo(m, binaryV2)
	if len(as) != 1 || as[0].Result != handover.Crashed || !strings.Contains(as[0].Reason, "first minute") {
		t.Errorf("the journal's attempts to the new build: %+v", as)
	}
	if w.Upgraded == nil || !w.Upgraded.Cold || !strings.Contains(w.Upgraded.Error, "ended") {
		t.Errorf("welcome.upgraded %+v", w.Upgraded)
	}
	_, text := serverStatusOf(t, m)
	t.Logf("server status after the crash:\n%s", text)
	// The programs ended with the process; the sessions are restored as
	// their layout, with new shells.
	for _, pid := range []int{s.left, s.right} {
		eventually(t, 10*time.Second, "the old shells ended", func() bool { return !alive(pid) })
	}
	if got := layout(snapshot(t, m), "alpha"); got != lay {
		t.Errorf("the layout after the crash:\n%s\nwant\n%s", got, lay)
	}
	// The way out: the profile back on the first build, which the
	// automatic upgrade takes live.
	installBuild(t, m, Binary())
	waitServing(t, m, 40*time.Second, "the server upgrades to the first build", func(x proto.Welcome) bool {
		return x.Version == s.w.Version && x.Pid == w.Pid
	})
	// And forward again to the build that crashed: never automatically.
	n := len(journalOf(m).Attempts)
	installBuild(t, m, binaryV2)
	time.Sleep(6 * time.Second)
	if x := m.Welcome(); x.Version != s.w.Version || x.Pid != w.Pid {
		t.Errorf("the server took the build that crashed again: %+v", x)
	}
	if got := len(journalOf(m).Attempts); got != n {
		t.Errorf("the journal has %d attempts, had %d: %+v", got, n, journalOf(m).Attempts)
	}
}

// (c') A build that passes the preflight and crashes at every start: the
// exec'd process ends at once and the manager starts the profile's build,
// the same one, in a loop (6.4: the journal cannot stop that). Rolling the
// profile back is the recovery; the next start of the old build marks the
// attempt crashed, and the automatic upgrade never takes the build again.
func TestServiceStartupCrashLoop(t *testing.T) {
	m, j := newServiceMachine(t)
	before := j.status()
	project(t, m, "alpha")
	x := openSession(t, m, plain(m, 100, 30), "alpha")
	shell := shellPid(t, x, "loop")
	crashing := writeBuild(t, "crashing", fmt.Sprintf(`#!/bin/sh
# answers the preflight as the second build, and crashes as the server
case "$1" in
version) exec %[1]q "$@" ;;
server) [ "$2" = upgrade-check ] && exec %[1]q "$@" ;;
esac
echo "e2e: this build crashes at start" >&2
exit 1
`, binaryV2))
	installBuild(t, m, crashing)
	// Both managers start it again for as long as it crashes: launchd every
	// ten seconds (its throttle), systemd after 1 s growing to 10 s, with no
	// start limit (nix/service.nix: StartLimitIntervalSec=0), so neither
	// gives up.
	var looping jobStatus
	eventually(t, 90*time.Second, "the manager keeps restarting the crashing build", func() bool {
		looping = j.status()
		return looping.Runs >= before.Runs+3
	})
	if strings.Contains(looping.State, "start-limit-hit") || strings.Contains(looping.State, "failed/") {
		t.Errorf("the manager gave up on the crashing build: %+v", looping)
	}
	t.Logf("the job while the build crashes: %+v", looping)
	if c := countLines(m.ServerLog(), "this build crashes at start"); c < 2 {
		t.Errorf("the crashing build ran %d times", c)
	}
	as := attemptsTo(m, crashing)
	if len(as) != 1 || as[0].Result != handover.Pending {
		t.Errorf("the journal while the build crashes: %+v", as)
	}
	if _, _, err := oneshot(m.Socket(), nil, time.Second); err == nil {
		t.Errorf("a server answers while the build crashes at every start")
	}
	eventually(t, 10*time.Second, "the shell ended", func() bool { return !alive(shell) })

	// The recovery of the README: the profile back on the old build, which
	// the manager's next try starts, on both systems, with nothing started
	// by hand.
	installBuild(t, m, Binary())
	w := waitServing(t, m, 60*time.Second, "the old build serves again", func(w proto.Welcome) bool { return w.Version != versionV2 })
	as = attemptsTo(m, crashing)
	if len(as) != 1 || as[0].Result != handover.Crashed || !strings.Contains(as[0].Reason, "during the exec") {
		t.Errorf("the journal after the recovery: %+v", as)
	}
	if w.Upgraded == nil || !w.Upgraded.Cold {
		t.Errorf("welcome.upgraded after the recovery %+v", w.Upgraded)
	}
	if _, ok := sessionOf(snapshot(t, m), "alpha"); !ok {
		t.Errorf("the session was not restored")
	}
	_, text := serverStatusOf(t, m)
	t.Logf("server status after the recovery:\n%s", text)
	// The same build once more: the journal blocks it.
	installBuild(t, m, crashing)
	time.Sleep(6 * time.Second)
	if x := m.Welcome(); x.Pid != w.Pid || x.Boot != w.Boot {
		t.Errorf("the server took the crashing build again: %+v", x)
	}
	if as := attemptsTo(m, crashing); len(as) != 1 {
		t.Errorf("the crashing build was tried again: %+v", as)
	}
}

// (d) The exec back under the manager: the new build refuses, the old one
// takes the programs back, in the same process.
func TestServiceExecBack(t *testing.T) {
	m, j := newServiceMachine(t, "DEVTOOLS_UPGRADE_FAIL_ADOPT=1")
	before := j.status()
	s := sceneOn(t, m)
	installBuild(t, m, binaryV2)
	waitServing(t, m, 40*time.Second, "the old build took the programs back", func(w proto.Welcome) bool {
		return w.Upgraded != nil && w.Upgraded.Back
	})
	s.kept(t, s.w.Version)
	after := j.status()
	if after.Pid != before.Pid || after.Runs != before.Runs {
		t.Errorf("the manager saw a change: before %+v, after %+v", before, after)
	}
	as := attemptsTo(m, binaryV2)
	if len(as) != 1 || as[0].Result != handover.Fallback {
		t.Errorf("the journal's attempts: %+v", as)
	}
	time.Sleep(5 * time.Second)
	if as := attemptsTo(m, binaryV2); len(as) != 1 {
		t.Errorf("the build that refused was tried again: %+v", as)
	}
}

// (e) A stop of the service while an attempt is in the middle: the job
// ends within TimeoutStopSec, no program of a pane is left, the journal is
// not left pending (the next start resolves it), and the next start is a
// clean cold start.
func TestServiceStopDuringHandover(t *testing.T) {
	needService(t)
	for _, phase := range []string{"freeze", "exec", "boundary", "stage"} {
		t.Run(phase, func(t *testing.T) {
			m, j := newServiceMachine(t, "DEVTOOLS_UPGRADE_DELAY="+phase+":3s")
			s := sceneOn(t, m)
			lay := layout(snapshot(t, m), "alpha")
			installBuild(t, m, binaryV2)
			eventually(t, 30*time.Second, "the attempt waits at "+phase, func() bool {
				for _, line := range strings.Split(m.ServerLog(), "\n") {
					if strings.Contains(line, "DEVTOOLS_UPGRADE_DELAY says") && strings.Contains(line, "phase="+phase) {
						return true
					}
				}
				return false
			})
			took := j.stop()
			t.Logf("the stop took %v", took)
			if took > serviceStopTimeout {
				t.Errorf("the stop took %v, more than TimeoutStopSec %v", took, serviceStopTimeout)
			}
			if !j.gone() {
				t.Errorf("the job is still there: %+v", j.status())
			}
			for _, pid := range []int{s.left, s.right} {
				eventually(t, 5*time.Second, "the programs of the panes ended", func() bool { return !alive(pid) })
			}
			if pids := machineServers(m); len(pids) != 0 {
				t.Errorf("servers left: %v", pids)
			}
			last, _ := journalOf(m).Last()
			t.Logf("the journal after the stop: %+v", journalOf(m).Attempts)
			log := m.ServerLog()

			// The next start: cold, of whatever the profile names.
			j.start()
			w := m.Welcome()
			if w.Boot == s.w.Boot || w.Pid == s.pid {
				t.Errorf("the next start is no cold start: %+v", w)
			}
			for _, a := range journalOf(m).Attempts {
				if a.Result == handover.Pending || a.Result == handover.Committed {
					t.Errorf("the journal is left %s after the next start: %+v", a.Result, a)
				}
			}
			if got := layout(snapshot(t, m), "alpha"); got != lay {
				t.Errorf("the layout after the next start:\n%s\nwant\n%s", got, lay)
			}
			after, _ := journalOf(m).Last()
			t.Logf("phase %s: stop %v, journal after the stop %q (%s), after the next start %q (%s), version %s", phase, took, last.Result, last.Reason, after.Result, after.Reason, w.Version)
			if t.Failed() {
				t.Logf("log of the stopped job:\n%s", log)
			}
		})
	}
}

// (f) The opt-out: DEVTOOLS_AUTO_UPGRADE=0 in the service's environment.
func TestServiceAutoUpgradeOptOut(t *testing.T) {
	m, j := newServiceMachine(t, "DEVTOOLS_AUTO_UPGRADE=0")
	before := j.status()
	w0 := m.Welcome()
	installBuild(t, m, binaryV2)
	waitServing(t, m, 30*time.Second, "the server sees the installed build", func(w proto.Welcome) bool { return w.Installed == versionV2 })
	time.Sleep(5 * time.Second)
	w := m.Welcome()
	if w.Version != w0.Version || w.Pid != before.Pid || w.Boot != w0.Boot || w.Upgraded != nil {
		t.Errorf("the server upgraded itself: %+v", w)
	}
	st, text := serverStatusOf(t, m)
	if !st.Pending || st.Install != versionV2 || !strings.Contains(text, versionV2) {
		t.Errorf("server status:\n%s(%+v)", text, st)
	}
	if n := len(journalOf(m).Attempts); n != 0 || strings.Contains(m.ServerLog(), "upgrade: a new build is installed") {
		t.Errorf("an attempt happened: %+v\n%s", journalOf(m).Attempts, m.ServerLog())
	}
	if after := j.status(); after.Pid != before.Pid || after.Runs != before.Runs {
		t.Errorf("the job before %+v, after %+v", before, after)
	}
}

// After every test of this file: no job of these tests is left.
func TestServiceLeavesNoJob(t *testing.T) {
	needService(t)
	if runtime.GOOS == "darwin" {
		out, _ := launchctl("list")
		if strings.Contains(string(out), "devtools-e2e") {
			t.Errorf("launchctl list shows a job of the tests:\n%s", grepLines(string(out), "devtools-e2e"))
		}
		return
	}
	out, _ := systemctl("list-units", systemdPrefix+"*", "--all", "--plain", "--no-legend")
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("systemctl --user list-units shows a unit of the tests:\n%s", out)
	}
}

func grepLines(s, sub string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
