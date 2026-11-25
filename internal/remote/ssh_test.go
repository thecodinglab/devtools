package remote

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
)

func TestSSHArgsAreExactlyTheFixedVector(t *testing.T) {
	want := []string{"-T",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=40",
		"--", "macmini", "devtools", "bridge"}
	if got := SSHArgs("macmini"); !reflect.DeepEqual(got, want) {
		t.Fatalf("SSHArgs(macmini) =\n%q\nwant\n%q", got, want)
	}
	// The alias is the only variable argument and always comes after "--",
	// whatever it is (SEC3, SEC5); nothing forwards anything (SEC14).
	for _, m := range []string{"desktop", "-oProxyCommand=x", "a b", "a;b", "user@host", ""} {
		got := SSHArgs(m)
		if !reflect.DeepEqual(got[:len(got)-3], want[:len(want)-3]) {
			t.Errorf("SSHArgs(%q) changed the fixed part: %q", m, got)
		}
		if got[len(got)-3] != m || got[len(got)-2] != "devtools" || got[len(got)-1] != "bridge" {
			t.Errorf("SSHArgs(%q) does not end in -- %q devtools bridge: %q", m, m, got)
		}
		for i, a := range got[:len(got)-3] {
			for _, bad := range []string{"-t", "-A", "-X", "-Y", "-L", "-R", "-D", "-W", "-J"} {
				if a == bad {
					t.Errorf("SSHArgs(%q)[%d] is %s", m, i, a)
				}
			}
			for _, bad := range []string{"SendEnv", "SetEnv", "Forward", "ProxyCommand", "LocalCommand", "ControlMaster"} {
				if strings.Contains(a, bad) {
					t.Errorf("SSHArgs(%q)[%d] = %q names %s", m, i, a, bad)
				}
			}
		}
	}
	// Each call returns its own slice.
	a := SSHArgs("x")
	a[0] = "-t"
	if SSHArgs("x")[0] != "-T" {
		t.Fatal("SSHArgs returns a shared slice")
	}
}

func TestEverySSHInvocationIsTheBridgeWithoutATerminal(t *testing.T) {
	r := newSSHRig(t, "exit255")
	t.Setenv("FAKE_SSH_STDERR", "ssh: connect to host macmini port 22: Connection refused\r\n")
	failed(t, dialAsync(r.dialer, "macmini"))

	t.Setenv("FAKE_SSH_MODE", "no-bridge")
	failed(t, dialAsync(r.dialer, "macbook"))

	t.Setenv("FAKE_SSH_MODE", "ok")
	ch := dialAsync(r.dialer, "desktop")
	r.srv.Accept().Welcome(snapshot())
	online(t, ch).Close()

	got := r.argvs()
	if len(got) != 3 {
		t.Fatalf("%d invocations recorded, want 3: %q", len(got), got)
	}
	for i, m := range []string{"macmini", "macbook", "desktop"} {
		argv := got[i]
		if !slices.Contains(argv, "-T") {
			t.Errorf("invocation %d has no -T: %q", i, argv)
		}
		if slices.Contains(argv, "-t") || slices.Contains(argv, "-tt") || slices.Contains(argv, "-A") {
			t.Errorf("invocation %d asks for a terminal or forwards the agent: %q", i, argv)
		}
		tail := argv[len(argv)-4:]
		if want := []string{"--", m, "devtools", "bridge"}; !reflect.DeepEqual(tail, want) {
			t.Errorf("invocation %d ends in %q, want %q", i, tail, want)
		}
		if !reflect.DeepEqual(argv, SSHArgs(m)) {
			t.Errorf("invocation %d is %q, not SSHArgs(%s)", i, argv, m)
		}
	}
}

func TestAnInvalidMachineNameRunsNoSSH(t *testing.T) {
	r := newSSHRig(t, "ok")
	for _, m := range []string{"-oProxyCommand=x", "a b", "a;b", "user@host"} {
		f := failed(t, dialAsync(r.dialer, m))
		if f.Reason != Protocol {
			t.Errorf("Dial(%q) failed with %s, want %s", m, f.Reason, Protocol)
		}
	}
	if _, err := os.Stat(r.argv); err == nil {
		t.Fatalf("ssh ran for an invalid machine name: %q", r.argvs())
	}
}

func TestDialOverBridgeHandshakes(t *testing.T) {
	for _, mode := range []string{"ok", "slow-marker"} {
		t.Run(mode, func(t *testing.T) {
			r := newSSHRig(t, mode)
			ch := dialAsync(r.dialer, "macmini")
			p := r.srv.Accept()
			h := p.Hello
			if !h.Remote || h.Kind != proto.KindInteractive || h.Gen != proto.Gen {
				t.Fatalf("hello = %+v, want remote, interactive, generation %d", h, proto.Gen)
			}
			if h.TTY != "" || h.Ancestors != nil || h.Env != nil || h.Pane != "" {
				t.Fatalf("a remote hello carries this machine's tty, ancestors, env or pane: %+v", h)
			}
			if !reflect.DeepEqual(h.Caps, localHello().Caps) || h.Colors == nil || *h.Colors != *localHello().Colors {
				t.Fatalf("hello lost its caps or colours: %+v", h)
			}
			w := p.Welcome(snapshot())
			l := online(t, ch)
			if !reflect.DeepEqual(l.Welcome(), w) {
				t.Fatalf("Welcome() = %+v, want %+v", l.Welcome(), w)
			}
			if m := recv(t, l); m.T != proto.TypeStateSnapshot {
				t.Fatalf("the first message of Recv is %s, want state.snapshot", m.T)
			}
			p.Send(proto.Bell{Pane: "p1"})
			if m := recv(t, l); m.T != proto.TypeEventBell {
				t.Fatalf("got %s, want event.bell", m.T)
			}
			if err := l.Send(proto.Request(1, proto.StateGet{})); err != nil {
				t.Fatal(err)
			}
			if m := p.Expect(proto.TypeStateGet, nil); m.ID != 1 {
				t.Fatalf("state.get arrived with id %d", m.ID)
			}
			l.Close()
			p.Ended()
			if f := ended(t, l); f != nil {
				t.Fatalf("Err after Close = %v, want nil", f)
			}
			if err := l.Send(proto.Encode(proto.Ping{})); err == nil {
				t.Fatal("Send after Close succeeded")
			}
		})
	}
}

func TestGarbageBeforeTheMarkerIsBoundedAndSkipped(t *testing.T) {
	t.Run("64 KiB of banner is skipped", func(t *testing.T) {
		r := newSSHRig(t, "garbage")
		t.Setenv("FAKE_SSH_GARBAGE", "65536")
		ch := dialAsync(r.dialer, "macmini")
		r.srv.Accept().Welcome(snapshot())
		l := online(t, ch)
		if m := recv(t, l); m.T != proto.TypeStateSnapshot {
			t.Fatalf("got %s after the banner", m.T)
		}
	})
	t.Run("one byte more fails as protocol", func(t *testing.T) {
		r := newSSHRig(t, "garbage")
		t.Setenv("FAKE_SSH_GARBAGE", "65537")
		f := failed(t, dialAsync(r.dialer, "macmini"))
		if f.Reason != Protocol || !strings.Contains(f.Text, "64 KiB") {
			t.Fatalf("failure = %+v, want protocol naming 64 KiB", f)
		}
	})
	t.Run("a banner without a line end is bounded too", func(t *testing.T) {
		r := newSSHRig(t, "garbage")
		t.Setenv("FAKE_SSH_GARBAGE", "300000")
		t.Setenv("FAKE_SSH_NOLINE", "1")
		f := failed(t, dialAsync(r.dialer, "macmini"))
		if f.Reason != Protocol {
			t.Fatalf("failure = %+v, want protocol", f)
		}
	})
}

func TestAHangingSSHIsKilledAfterTheAttemptLimit(t *testing.T) {
	r := newSSHRig(t, "hang")
	clk := r.useFakeClock()
	ch := dialAsync(r.dialer, "macmini")
	pid := r.fakePid()
	clk.waitCalls(t, 1)

	clk.Advance(attemptLimit - time.Second)
	select {
	case res := <-ch:
		t.Fatalf("Dial returned before the limit: %+v", res.fail)
	case <-time.After(50 * time.Millisecond):
	}
	if gone(pid) {
		t.Fatal("ssh was killed before the limit")
	}

	clk.Advance(time.Second)
	f := failed(t, ch)
	if f.Reason != Unreachable || !strings.Contains(f.Text, "30 s") {
		t.Fatalf("failure = %+v, want unreachable after 30 s", f)
	}
	testkit.Eventually(t, 5*time.Second, "the hanging ssh is gone", func() bool { return gone(pid) })
}

func TestCloseKillsTheSSHProcess(t *testing.T) {
	r := newSSHRig(t, "ok")
	ch := dialAsync(r.dialer, "macmini")
	p := r.srv.Accept()
	p.Welcome(snapshot())
	l := online(t, ch)
	pid := r.fakePid()
	if gone(pid) {
		t.Fatal("the fake ssh is not running")
	}
	l.Close()
	testkit.Eventually(t, 5*time.Second, "the ssh process is gone after Close", func() bool { return gone(pid) })
	if f := ended(t, l); f != nil {
		t.Fatalf("Err after Close = %+v, want nil", f)
	}
	p.Ended()
}

func TestALostSSHLinkEndsWithAReason(t *testing.T) {
	r := newSSHRig(t, "ok")
	ch := dialAsync(r.dialer, "macmini")
	p := r.srv.Accept()
	p.Welcome(snapshot())
	l := online(t, ch)
	recv(t, l)
	p.Close() // the remote server goes away; the bridge exits 0
	f := ended(t, l)
	if f == nil || f.Reason != Unreachable {
		t.Fatalf("Err = %+v, want unreachable", f)
	}
}

func TestACancelledDialKillsTheSSHProcess(t *testing.T) {
	r := newSSHRig(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan dialResult, 1)
	go func() {
		l, f := r.dialer.Dial(ctx, "macmini", true)
		ch <- dialResult{l, f}
	}()
	pid := r.fakePid()
	cancel()
	if f := failed(t, ch); f.Reason != Unreachable {
		t.Fatalf("failure = %+v, want unreachable", f)
	}
	testkit.Eventually(t, 5*time.Second, "the ssh of a cancelled dial is gone", func() bool { return gone(pid) })
}

// fixedPart is the fixed options of every ssh command line (3.5.2).
var fixedPart = []string{"-T",
	"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
	"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1",
	"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=40"}

// joined is the vectors a, then b, ... in one new slice.
func joined(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestTheSharedConnectionArgsAreExactlyTheFixedVectors(t *testing.T) {
	const path = "/home/florian/.local/state/devtools/ssh/%C"
	bridge := []string{"--", "macmini", "devtools", "bridge"}
	rows := []struct {
		name string
		got  []string
		want []string
	}{
		{"nothing shared", bridgeArgs("macmini", sharing{}), joined(fixedPart, bridge)},
		{"a login becomes or reuses the master", bridgeArgs("macmini", sharing{path: path, login: true}), joined(fixedPart,
			[]string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + path, "-o", "ControlPersist=15m"}, bridge)},
		{"without a login only the master is used", bridgeArgs("macmini", sharing{path: path}), joined(fixedPart,
			[]string{"-o", "ControlMaster=no", "-o", "ControlPath=" + path, "-o", "PreferredAuthentications=none"}, bridge)},
		{"the check", checkArgs("macmini", path), joined(fixedPart,
			[]string{"-o", "ControlMaster=no", "-o", "ControlPath=" + path, "-o", "PreferredAuthentications=none",
				"-O", "check", "--", "macmini"})},
	}
	for _, row := range rows {
		if !reflect.DeepEqual(row.got, row.want) {
			t.Errorf("%s:\n%q\nwant\n%q", row.name, row.got, row.want)
		}
		// Nothing forwards anything (SEC14), and the alias comes after "--".
		i := slices.Index(row.got, "--")
		if i < 0 || row.got[i+1] != "macmini" {
			t.Errorf("%s: the alias does not follow --: %q", row.name, row.got)
			continue
		}
		for _, a := range row.got[:i] {
			for _, bad := range []string{"-t", "-A", "-X", "-Y", "-L", "-R", "-D", "-W", "-J"} {
				if a == bad {
					t.Errorf("%s: %s in %q", row.name, a, row.got)
				}
			}
			for _, bad := range []string{"SendEnv", "SetEnv", "Forward", "ProxyCommand", "LocalCommand"} {
				if strings.Contains(a, bad) {
					t.Errorf("%s: %q names %s", row.name, a, bad)
				}
			}
		}
	}
	if !reflect.DeepEqual(SSHArgs("macmini"), rows[0].want) {
		t.Errorf("SSHArgs is not the vector that shares nothing: %q", SSHArgs("macmini"))
	}
}

func TestTheControlDirIsPrivateAndShort(t *testing.T) {
	base := testkit.ShortTempDir(t)
	limit := len(syscall.RawSockaddrUnix{}.Path)

	t.Run("a missing directory is made 0700", func(t *testing.T) {
		dir := filepath.Join(base, "made", "ssh")
		path, ok := controlPath(dir)
		if !ok || path != dir+"/%C" {
			t.Fatalf("controlPath(%q) = %q, %v; want %q", dir, path, ok, dir+"/%C")
		}
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("the directory is %v, %v; want a directory of mode 0700", fi.Mode(), err)
		}
		if path, ok := controlPath(dir); !ok || path != dir+"/%C" {
			t.Fatalf("the second call: %q, %v", path, ok)
		}
	})
	off := func(t *testing.T, dir string) {
		t.Helper()
		if path, ok := controlPath(dir); ok {
			t.Fatalf("controlPath(%q) = %q; want nothing shared", dir, path)
		}
	}
	t.Run("none", func(t *testing.T) { off(t, "") })
	t.Run("relative", func(t *testing.T) { off(t, "state/ssh") })
	t.Run("a directory others can read", func(t *testing.T) {
		dir := filepath.Join(base, "open")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Chmod(dir, 0o755)
		off(t, dir)
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o755 {
			t.Fatalf("the mode was changed to %v", fi.Mode())
		}
	})
	t.Run("a symlink to a private directory", func(t *testing.T) {
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		off(t, link)
	})
	t.Run("a file", func(t *testing.T) {
		file := filepath.Join(base, "file")
		if err := os.WriteFile(file, nil, 0o700); err != nil {
			t.Fatal(err)
		}
		off(t, file)
	})
	t.Run("a byte the option parser of ssh reads", func(t *testing.T) {
		for _, name := range []string{"a b", "a%b", "a$b", "a\"b", "a'b", "a\\b", "a\tb"} {
			off(t, filepath.Join(base, name))
		}
	})
	t.Run("the longest directory that leaves room for the socket", func(t *testing.T) {
		// ControlDir, "/", 40 bytes of %C and 20 of ssh's temporary
		// suffix must fit the platform's limit.
		n := limit - 1 - 40 - 20
		dir := base + "/" + strings.Repeat("d", n-len(base)-1)
		if len(dir) != n {
			t.Fatalf("the test made a directory of %d bytes, want %d", len(dir), n)
		}
		if _, ok := controlPath(dir); !ok {
			t.Fatalf("a directory of %d bytes shares nothing (limit %d)", n, limit)
		}
		off(t, dir+"e")
		if _, err := os.Stat(dir + "e"); err == nil {
			t.Fatal("a directory that is too long was made")
		}
	})
}

func TestALoginDialBecomesOrReusesTheMaster(t *testing.T) {
	r := newSSHRig(t, "ok")
	dir := r.share()
	ch := dialLogin(r.dialer, "macmini", true)
	r.srv.Accept().Welcome(snapshot())
	online(t, ch).Close()
	got := r.argvs()
	want := bridgeArgs("macmini", sharing{path: dir + "/%C", login: true})
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("invocations %q, want only %q", got, want)
	}
}

func TestADialWithoutLoginRunsOverTheMasterOnly(t *testing.T) {
	r := newSSHRig(t, "ok")
	dir := r.share()
	t.Setenv("FAKE_SSH_CHECK", "up")
	ch := dialLogin(r.dialer, "macmini", false)
	r.srv.Accept().Welcome(snapshot())
	online(t, ch).Close()
	got := r.argvs()
	path := dir + "/%C"
	want := [][]string{checkArgs("macmini", path), bridgeArgs("macmini", sharing{path: path})}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invocations\n%q\nwant\n%q", got, want)
	}
}

func TestADialWithoutLoginAndWithoutMasterNeedsALogin(t *testing.T) {
	t.Run("no master", func(t *testing.T) {
		r := newSSHRig(t, "ok")
		dir := r.share()
		f := failed(t, dialLogin(r.dialer, "macmini", false))
		if f.Reason != NeedsLogin || !strings.Contains(f.Text, "macmini") || strings.ContainsAny(f.Text, "\r\n") {
			t.Fatalf("failure = %+v, want login naming the machine on one line", f)
		}
		// Only the check ran, never the ssh of the bridge.
		if got := r.argvs(); len(got) != 1 || !reflect.DeepEqual(got[0], checkArgs("macmini", dir+"/%C")) {
			t.Fatalf("invocations %q, want only the check", got)
		}
	})
	t.Run("nothing is shared", func(t *testing.T) {
		r := newSSHRig(t, "ok")
		t.Setenv("FAKE_SSH_CHECK", "up")
		if f := failed(t, dialLogin(r.dialer, "macmini", false)); f.Reason != NeedsLogin {
			t.Fatalf("failure = %+v, want login", f)
		}
		if _, err := os.Stat(r.argv); err == nil {
			t.Fatalf("ssh ran although nothing is shared: %q", r.argvs())
		}
	})
	t.Run("the local socket ignores login", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialLogin(localDialer(srv, localHello(), nil), "", false)
		srv.Accept().Welcome(snapshot())
		online(t, ch)
	})
}

// ssh has no mode that only multiplexes: when the master does not serve
// it, it logs in as usual (OpenSSH mux.c, muxclient). A dial that may not
// log in must still never ask the agent (3.5.2).
func TestADialWithoutLoginNeverAsksTheAgent(t *testing.T) {
	t.Run("ControlMaster yes in the user's config", func(t *testing.T) {
		// muxclient skips the master for yes and ask, even for -O check.
		r := newSSHRig(t, "ok")
		dir := r.share()
		t.Setenv("FAKE_SSH_CONFIG_MASTER", "yes")
		if f := failed(t, dialLogin(r.dialer, "macmini", false)); f.Reason != NeedsLogin {
			t.Fatalf("failure = %+v, want login", f)
		}
		if n := r.logins(); n != 0 {
			t.Fatalf("the agent was asked %d times", n)
		}
		if got := r.argvs(); len(got) != 1 || !reflect.DeepEqual(got[0], checkArgs("macmini", dir+"/%C")) {
			t.Fatalf("invocations %q, want only the check", got)
		}
	})
	t.Run("a master that takes no new session", func(t *testing.T) {
		// MaxSessions of the server used up: the check finds the master
		// up, the bridge's session is refused and ssh falls back to a
		// login.
		r := newSSHRig(t, "ok")
		dir := r.share()
		t.Setenv("FAKE_SSH_CHECK", "refuse")
		f := failed(t, dialLogin(r.dialer, "macmini", false))
		if f.Reason != NeedsLogin || !strings.Contains(f.Text, "macmini") {
			t.Fatalf("failure = %+v, want login naming the machine", f)
		}
		if n := r.logins(); n != 0 {
			t.Fatalf("the agent was asked %d times", n)
		}
		path := dir + "/%C"
		want := [][]string{checkArgs("macmini", path), bridgeArgs("macmini", sharing{path: path})}
		if got := r.argvs(); !reflect.DeepEqual(got, want) {
			t.Fatalf("invocations\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("a dial that may log in still does", func(t *testing.T) {
		r := newSSHRig(t, "ok")
		r.share()
		t.Setenv("FAKE_SSH_CHECK", "refuse")
		ch := dialLogin(r.dialer, "macmini", true)
		r.srv.Accept().Welcome(snapshot())
		online(t, ch).Close()
		if n := r.logins(); n != 1 {
			t.Fatalf("the agent was asked %d times, want once", n)
		}
	})
	t.Run("an auth failure of a login stays auth", func(t *testing.T) {
		r := newSSHRig(t, "exit255")
		r.share()
		t.Setenv("FAKE_SSH_STDERR", "florian@macmini: Permission denied (publickey).\r\n")
		if f := failed(t, dialLogin(r.dialer, "macmini", true)); f.Reason != Auth {
			t.Fatalf("failure = %+v, want auth", f)
		}
	})
}

func TestAHangingCheckIsKilledAfterFiveSeconds(t *testing.T) {
	r := newSSHRig(t, "ok")
	r.share()
	t.Setenv("FAKE_SSH_CHECK", "hang")
	clk := r.useFakeClock()
	ch := dialLogin(r.dialer, "macmini", false)
	pid := r.fakePid()
	clk.waitCalls(t, 1)

	clk.Advance(checkLimit - time.Second)
	select {
	case res := <-ch:
		t.Fatalf("Dial returned before the limit of the check: %+v", res.fail)
	case <-time.After(50 * time.Millisecond):
	}
	if gone(pid) {
		t.Fatal("the check was killed before its limit")
	}
	clk.Advance(time.Second)
	if f := failed(t, ch); f.Reason != NeedsLogin {
		t.Fatalf("failure = %+v, want login", f)
	}
	testkit.Eventually(t, 5*time.Second, "the hanging check is gone", func() bool { return gone(pid) })
	if got := r.argvs(); len(got) != 1 {
		t.Fatalf("invocations %q, want only the check", got)
	}
}

func TestACancelledDialKillsTheCheck(t *testing.T) {
	r := newSSHRig(t, "ok")
	r.share()
	t.Setenv("FAKE_SSH_CHECK", "hang")
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan dialResult, 1)
	go func() {
		l, f := r.dialer.Dial(ctx, "macmini", false)
		ch <- dialResult{l, f}
	}()
	pid := r.fakePid()
	cancel()
	if f := failed(t, ch); f.Reason != NeedsLogin {
		t.Fatalf("failure = %+v, want login", f)
	}
	testkit.Eventually(t, 5*time.Second, "the check of a cancelled dial is gone", func() bool { return gone(pid) })
}
