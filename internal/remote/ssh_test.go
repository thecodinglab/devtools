package remote

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/testkit"
)

func TestSSHArgsAreExactlyTheFixedVector(t *testing.T) {
	want := []string{"-T",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
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
		l, f := r.dialer.Dial(ctx, "macmini")
		ch <- dialResult{l, f}
	}()
	pid := r.fakePid()
	cancel()
	if f := failed(t, ch); f.Reason != Unreachable {
		t.Fatalf("failure = %+v, want unreachable", f)
	}
	testkit.Eventually(t, 5*time.Second, "the ssh of a cancelled dial is gone", func() bool { return gone(pid) })
}
