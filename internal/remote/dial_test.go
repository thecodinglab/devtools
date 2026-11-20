package remote

import (
	"bufio"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/testkit"
)

// localDialer dials srv with the fake clock clk (nil: the wall clock).
func localDialer(srv *prototest.Server, hello proto.Hello, clk clock) *dialer {
	d := NewDialer(DialOptions{Socket: srv.Socket(), Hello: hello}).(*dialer)
	if clk != nil {
		d.clk = clk
	}
	return d
}

func TestDialLocalHandshakes(t *testing.T) {
	t.Run("interactive", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		p := srv.Accept()
		// The local hello is sent as it was given.
		if p.Hello.Remote || p.Hello.TTY != "/dev/pts/7" || len(p.Hello.Ancestors) != 3 || p.Hello.Env["DISPLAY"] != ":0" {
			t.Fatalf("hello = %+v, want the local hello unchanged", p.Hello)
		}
		p.Welcome(snapshot())
		l := online(t, ch)
		if w := l.Welcome(); w.Host != "prototest" || w.Gen != proto.Gen || w.Client != "c1" {
			t.Fatalf("Welcome() = %+v", w)
		}
		m := recv(t, l)
		var s proto.Snapshot
		if m.T != proto.TypeStateSnapshot || m.Decode(&s) != nil || s.Rev != 41 {
			t.Fatalf("first message = %s %s, want the snapshot of rev 41", m.T, m.Body)
		}
		p.Send(proto.Bell{Pane: "p52"})
		if m := recv(t, l); m.T != proto.TypeEventBell {
			t.Fatalf("got %s, want event.bell", m.T)
		}
		if err := l.Send(proto.Request(7, proto.StateGet{})); err != nil {
			t.Fatal(err)
		}
		if m := p.Expect(proto.TypeStateGet, nil); m.ID != 7 {
			t.Fatalf("id %d, want 7", m.ID)
		}
		l.Close()
		p.Ended()
		if f := ended(t, l); f != nil {
			t.Fatalf("Err after Close = %+v", f)
		}
	})
	t.Run("one-shot gets no snapshot", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, prototest.Oneshot(""), nil), "")
		p := srv.Accept()
		p.Welcome(proto.Snapshot{})
		l := online(t, ch)
		if err := l.Send(proto.Request(1, proto.StateGet{})); err != nil {
			t.Fatal(err)
		}
		p.Expect(proto.TypeStateGet, nil)
		p.Send(proto.Request(1, snapshot()))
		if m := recv(t, l); m.T != proto.TypeStateSnapshot || m.ID != 1 {
			t.Fatalf("got %s id %d, want the reply to state.get", m.T, m.ID)
		}
	})
	t.Run("the server going away ends the link", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		p := srv.Accept()
		p.Welcome(snapshot())
		l := online(t, ch)
		p.Close()
		if f := ended(t, l); f == nil || f.Reason != NoServer {
			t.Fatalf("Err = %+v, want no-server", f)
		}
	})
}

func TestALinkAnswersPings(t *testing.T) {
	srv := prototest.NewServer(t)
	ch := dialAsync(localDialer(srv, localHello(), newFakeClock()), "")
	p := srv.Accept()
	p.Welcome(snapshot())
	l := online(t, ch)
	if err := proto.WriteMsg(p.Conn(), proto.Encode(proto.Ping{})); err != nil {
		t.Fatal(err)
	}
	p.Conn().SetReadDeadline(time.Now().Add(10 * time.Second))
	if m := readRaw(t, p.Conn()); m.T != proto.TypePong {
		t.Fatalf("the link answered ping with %s", m.T)
	}
	// Neither ping nor pong reaches the consumer.
	p.Send(proto.Pong{})
	p.Send(proto.Bell{Pane: "p1"})
	recv(t, l) // the snapshot
	if m := recv(t, l); m.T != proto.TypeEventBell {
		t.Fatalf("got %s, want event.bell", m.T)
	}
}

func TestASilentLinkIsLostAfterTenSeconds(t *testing.T) {
	clk := newFakeClock()
	srv := prototest.NewServer(t)
	ch := dialAsync(localDialer(srv, localHello(), clk), "")
	p := srv.Accept()
	p.Welcome(snapshot())
	l := online(t, ch)
	recv(t, l)
	p.Conn().SetReadDeadline(time.Now().Add(10 * time.Second))
	clk.waitCalls(t, 2) // the attempt limit, then the watchdog

	// 5 s without sending: a ping.
	clk.Advance(5 * time.Second)
	if m := readRaw(t, p.Conn()); m.T != proto.TypePing {
		t.Fatalf("after 5 s the link sent %s, want ping", m.T)
	}
	clk.waitCalls(t, 3)
	// Something arrives at 5 s, so 10 s is not yet silence.
	p.Send(proto.Bell{Pane: "p1"})
	recv(t, l)
	clk.Advance(5 * time.Second)
	if m := readRaw(t, p.Conn()); m.T != proto.TypePing {
		t.Fatalf("after 10 s the link sent %s, want ping", m.T)
	}
	select {
	case _, ok := <-l.Recv():
		if !ok {
			t.Fatalf("the link ended after 5 s of silence: %+v", l.Err())
		}
	default:
	}
	clk.waitCalls(t, 4)
	// 10 s after the last message: lost.
	clk.Advance(5 * time.Second)
	f := ended(t, l)
	if f == nil || f.Reason != Protocol || !strings.Contains(f.Text, "10 s") {
		t.Fatalf("Err = %+v, want protocol after 10 s of silence", f)
	}
	if err := l.Send(proto.Encode(proto.Ping{})); err == nil {
		t.Fatal("Send on a lost link succeeded")
	}
}

func TestAFullSendQueueEndsTheLink(t *testing.T) {
	srv := prototest.NewServer(t)
	ch := dialAsync(localDialer(srv, localHello(), newFakeClock()), "")
	p := srv.Accept()
	p.Welcome(snapshot())
	l := online(t, ch)
	// The server reads nothing from now on; each message is 64 KiB, so the
	// socket buffer fills and then the queue of 256.
	big := proto.Msg{T: "test.pad", Body: []byte(`{"t":"test.pad","pad":"` + strings.Repeat("x", 64<<10) + `"}`)}
	sent := 0
	for ; sent < 10000; sent++ {
		if err := l.Send(big); err != nil {
			break
		}
	}
	if sent == 10000 {
		t.Fatal("Send never failed although nobody reads")
	}
	if sent < sendQueue {
		t.Fatalf("Send failed after %d messages, before the queue of %d was full", sent, sendQueue)
	}
	f := ended(t, l)
	if f == nil || f.Reason != Protocol || !strings.Contains(f.Text, "256") {
		t.Fatalf("Err = %+v, want protocol naming the queue of 256", f)
	}
}

// UNVERIFIED: the exit statuses and the stderr texts below are those of
// 3.5.2 of the plan and of the OpenSSH manual, not yet recorded on the
// three machines. They are provisional (E5): when
// docs/replace-herdr/experiments/e5-ssh-failures.md exists, its records
// replace these rows.
var classifyRows = []struct {
	name   string
	exit   int
	stderr string
	line   string
	want   Reason
}{
	{"auth, permission denied", 255, "florian@macmini: Permission denied (publickey,keyboard-interactive).\r\n", "", Auth},
	{"auth, too many failures", 255, "Received disconnect from 192.168.1.5 port 22:2: Too many authentication failures\r\nDisconnected from 192.168.1.5 port 22\r\n", "", Auth},
	{"host-key, verification failed", 255, "Host key verification failed.\r\n", "", HostKey},
	{"host-key, changed", 255, "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\r\nIT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!\r\n", "", HostKey},
	{"host-key, unknown", 255, "No ED25519 host key is known for macmini and you have requested strict checking.\r\nHost key verification failed.\r\n", "", HostKey},
	{"unreachable, timed out", 255, "ssh: connect to host macmini port 22: Connection timed out\r\n", "", Unreachable},
	{"unreachable, refused", 255, "ssh: connect to host macmini port 22: Connection refused\r\n", "", Unreachable},
	{"unreachable, no route", 255, "ssh: connect to host macmini port 22: No route to host\r\n", "", Unreachable},
	{"unreachable, network", 255, "ssh: connect to host macmini port 22: Network is unreachable\r\n", "", Unreachable},
	{"unreachable, unknown name", 255, "ssh: Could not resolve hostname macmini: Name or service not known\r\n", "", Unreachable},
	{"unreachable, any other ssh failure", 255, "kex_exchange_identification: read: Connection reset by peer\r\n", "", Unreachable},
	{"no-devtools, exit 127", 127, "bash: line 1: devtools: command not found\n", "", NoDevtools},
	{"no-devtools, zsh", 1, "zsh:1: command not found: devtools\n", "", NoDevtools},
	{"no-devtools, nothing on stderr", 127, "", "", NoDevtools},
	{"no-server", 1, "", "devtools-bridge-error no-server no devtools server answers at /home/florian/.local/state/devtools/server.sock: connection refused", NoServer},
	{"incompatible, an old build without bridge", 1, "Error: unknown command \"bridge\" for \"devtools\"\nRun 'devtools --help' for usage.\n", "", Incompatible},
	{"incompatible, an unknown bridge error", 1, "", "devtools-bridge-error future-thing something new", Incompatible},
	{"protocol, the bridge ended before its line", 0, "", "", Protocol},
}

func TestEveryFailureReasonIsClassified(t *testing.T) {
	seen := map[Reason]bool{}
	for _, row := range classifyRows {
		t.Run("classify "+row.name, func(t *testing.T) {
			f := Classify(row.exit, []byte(row.stderr), row.line)
			if f == nil || f.Reason != row.want {
				t.Fatalf("Classify(%d, %q, %q) = %+v, want %s", row.exit, row.stderr, row.line, f, row.want)
			}
			if f.Text == "" || strings.ContainsAny(f.Text, "\r\n\x1b") {
				t.Fatalf("Text %q is not one sanitised line", f.Text)
			}
			seen[row.want] = true
		})
	}

	// The reasons that come from the stream, not from ssh's end.
	t.Run("no-server over the bridge", func(t *testing.T) {
		r := newSSHRig(t, "ok")
		t.Setenv("FAKE_SSH_SOCKET", filepath.Join(r.dir, "nothing.sock"))
		f := failed(t, dialAsync(r.dialer, "macmini"))
		if f.Reason != NoServer || !strings.Contains(f.Text, "nothing.sock") {
			t.Fatalf("failure = %+v, want no-server naming the socket", f)
		}
		seen[NoServer] = true
	})
	t.Run("no-server locally", func(t *testing.T) {
		d := NewDialer(DialOptions{Socket: filepath.Join(testkit.ShortTempDir(t), "server.sock"), Hello: localHello()})
		f := failed(t, dialAsync(d, ""))
		if f.Reason != NoServer {
			t.Fatalf("failure = %+v, want no-server", f)
		}
	})
	t.Run("no-devtools over ssh", func(t *testing.T) {
		r := newSSHRig(t, "exit")
		t.Setenv("FAKE_SSH_EXIT", "127")
		t.Setenv("FAKE_SSH_STDERR", "bash: line 1: devtools: command not found\n")
		if f := failed(t, dialAsync(r.dialer, "macmini")); f.Reason != NoDevtools {
			t.Fatalf("failure = %+v, want no-devtools", f)
		}
	})
	t.Run("auth over ssh", func(t *testing.T) {
		r := newSSHRig(t, "exit255")
		t.Setenv("FAKE_SSH_STDERR", "florian@macmini: Permission denied (publickey).\r\n")
		f := failed(t, dialAsync(r.dialer, "macmini"))
		if f.Reason != Auth || f.Text != "florian@macmini: Permission denied (publickey)." {
			t.Fatalf("failure = %+v, want auth with the line of ssh", f)
		}
	})
	t.Run("incompatible, the old build", func(t *testing.T) {
		r := newSSHRig(t, "no-bridge")
		if f := failed(t, dialAsync(r.dialer, "macmini")); f.Reason != Incompatible {
			t.Fatalf("failure = %+v, want incompatible", f)
		}
	})
	t.Run("incompatible, welcome of another generation", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		srv.Accept().Send(proto.Welcome{Gen: 3, Version: "3.0.0", Boot: "b", Host: "h"})
		f := failed(t, ch)
		if f.Reason != Incompatible || !strings.Contains(f.Text, "3") || !strings.Contains(f.Text, "2") {
			t.Fatalf("failure = %+v, want incompatible naming both generations", f)
		}
		seen[Incompatible] = true
	})
	t.Run("incompatible, refuse generation", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		srv.Accept().Send(proto.Refuse{Code: proto.CodeGeneration, Text: "this server speaks protocol 2"})
		if f := failed(t, ch); f.Reason != Incompatible || f.Text != "this server speaks protocol 2" {
			t.Fatalf("failure = %+v, want incompatible with the server's text", f)
		}
	})
	t.Run("incompatible, a bridge of another generation", func(t *testing.T) {
		kind, _, f := readMarker(bufio.NewReader(strings.NewReader("banner\ndevtools-bridge-ready 3\n")))
		if kind != markerFail || f == nil || f.Reason != Incompatible {
			t.Fatalf("readMarker = %v %+v, want incompatible", kind, f)
		}
	})
	t.Run("protocol, more than 64 KiB before the marker", func(t *testing.T) {
		r := newSSHRig(t, "garbage")
		t.Setenv("FAKE_SSH_GARBAGE", "70000")
		if f := failed(t, dialAsync(r.dialer, "macmini")); f.Reason != Protocol {
			t.Fatalf("failure = %+v, want protocol", f)
		}
		seen[Protocol] = true
	})
	t.Run("protocol, a message over 4 MiB", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		p := srv.Accept()
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], proto.MaxMessage+1)
		p.Conn().Write(prefix[:])
		if f := failed(t, ch); f.Reason != Protocol || !strings.Contains(f.Text, "4 MiB") {
			t.Fatalf("failure = %+v, want protocol naming 4 MiB", f)
		}
	})
	t.Run("protocol, invalid JSON over the bridge", func(t *testing.T) {
		r := newSSHRig(t, "ok")
		ch := dialAsync(r.dialer, "macmini")
		p := r.srv.Accept()
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], 8)
		p.Conn().Write(append(prefix[:], "not json"...))
		if f := failed(t, ch); f.Reason != Protocol {
			t.Fatalf("failure = %+v, want protocol", f)
		}
	})
	t.Run("protocol, invalid JSON on a live link", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		p := srv.Accept()
		p.Welcome(snapshot())
		l := online(t, ch)
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], 3)
		p.Conn().Write(append(prefix[:], "{x}"...))
		if f := ended(t, l); f == nil || f.Reason != Protocol {
			t.Fatalf("Err = %+v, want protocol", f)
		}
	})
	t.Run("protocol, something other than welcome", func(t *testing.T) {
		srv := prototest.NewServer(t)
		ch := dialAsync(localDialer(srv, localHello(), nil), "")
		srv.Accept().Send(proto.Bell{Pane: "p1"})
		if f := failed(t, ch); f.Reason != Protocol {
			t.Fatalf("failure = %+v, want protocol", f)
		}
	})

	// The 30 s limit (unreachable) and 10 s of silence (protocol) have tests
	// of their own with a fake clock.
	for _, r := range []Reason{Auth, HostKey, Unreachable, NoDevtools, NoServer, Incompatible, Protocol} {
		if !seen[r] {
			t.Errorf("no row classifies %s", r)
		}
	}
}

func TestClassifyKeepsTheTextOneLineAndBounded(t *testing.T) {
	stderr := strings.Repeat("noise\n", 2000) + "evil \x1b]52;c;aGk=\x07 \u202e Permission denied (publickey).\r\n"
	f := Classify(255, []byte(stderr), "")
	if f.Reason != Auth {
		t.Fatalf("reason %s, want auth", f.Reason)
	}
	if strings.ContainsAny(f.Text, "\x1b\x07\r\n\u202e") {
		t.Fatalf("Text %q keeps a control character", f.Text)
	}
	long := Classify(255, []byte(strings.Repeat("Connection refused ", 100)), "")
	if n := len([]rune(long.Text)); n > 400 {
		t.Fatalf("Text has %d runes, more than 400", n)
	}
}
