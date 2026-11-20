package remote

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devtools/internal/testkit"
)

// listen opens a unix socket in a short directory.
func listen(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	path := filepath.Join(testkit.ShortTempDir(t), "server.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, path
}

func accept(t *testing.T, ln *net.UnixListener) net.Conn {
	t.Helper()
	ln.SetDeadline(time.Now().Add(10 * time.Second))
	c, err := ln.Accept()
	if err != nil {
		t.Fatalf("the bridge did not connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// noConnection checks that nobody connects to ln for a while.
func noConnection(t *testing.T, ln *net.UnixListener) {
	t.Helper()
	ln.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if c, err := ln.Accept(); err == nil {
		c.Close()
		t.Fatal("something connected to a socket that is not the state socket")
	}
}

// bridgeRun is a Bridge running on pipes.
type bridgeRun struct {
	stdin  *io.PipeWriter
	stdout *bufio.Reader
	done   chan int
}

func startBridge(t *testing.T, socket string) *bridgeRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	b := &bridgeRun{stdin: inW, stdout: bufio.NewReader(outR), done: make(chan int, 1)}
	go func() {
		code := Bridge(inR, outW, socket, false)
		outW.Close()
		b.done <- code
	}()
	t.Cleanup(func() { inW.Close(); outR.Close() })
	return b
}

func (b *bridgeRun) ready(t *testing.T) {
	t.Helper()
	line, err := b.stdout.ReadString('\n')
	if err != nil || line != "devtools-bridge-ready 1\n" {
		t.Fatalf("first line %q (%v), want the ready line", line, err)
	}
}

func (b *bridgeRun) exit(t *testing.T) int {
	t.Helper()
	go io.Copy(io.Discard, b.stdout)
	select {
	case code := <-b.done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge did not exit")
		return -1
	}
}

func useBridgeClock(t *testing.T) *fakeClock {
	clk := newFakeClock()
	old := bridgeClock
	bridgeClock = clk
	t.Cleanup(func() { bridgeClock = old })
	return clk
}

func TestBridgeRefusesATerminal(t *testing.T) {
	ln, socket := listen(t)
	var stderr bytes.Buffer
	old := bridgeStderr
	bridgeStderr = &stderr
	defer func() { bridgeStderr = old }()
	var stdout bytes.Buffer
	if code := Bridge(strings.NewReader("hello"), &stdout, socket, true); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if got := stderr.String(); got != "devtools bridge is run by devtools over ssh, not by hand\n" {
		t.Fatalf("stderr %q", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout %q, want nothing", stdout.String())
	}
	noConnection(t, ln)
}

func TestBridgeReportsNoServer(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	stale := filepath.Join(dir, "stale.sock")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, socket := range map[string]string{
		"no socket":       filepath.Join(dir, "server.sock"),
		"nothing listens": stale,
		"a relative path": "server.sock",
	} {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			if code := Bridge(strings.NewReader(""), &stdout, socket, false); code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			out := stdout.String()
			if !strings.HasPrefix(out, "devtools-bridge-error no-server ") || !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
				t.Fatalf("stdout %q, want one devtools-bridge-error no-server line", out)
			}
			if f := Classify(1, nil, strings.TrimSuffix(out, "\n")); f.Reason != NoServer || !strings.Contains(f.Text, socket) {
				t.Fatalf("the client reads %+v from it", f)
			}
		})
	}
}

func TestBridgeCopiesBytesUntouched(t *testing.T) {
	ln, socket := listen(t)
	b := startBridge(t, socket)
	conn := accept(t, ln)
	b.ready(t)

	var all []byte
	for i := 0; i < 4096; i++ {
		for c := 0; c < 256; c++ {
			all = append(all, byte(c))
		}
	}
	up := append(append([]byte(nil), all...), "devtools-bridge-ready 1\n\x1b[?1049h\r\n"...)
	down := append([]byte("\x00\x00\x00\x05{\"t\":"), all...)

	go b.stdin.Write(up)
	got := make([]byte, len(up))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, up) {
		t.Fatal("bytes from the client reached the server changed")
	}
	go conn.Write(down)
	got = make([]byte, len(down))
	if _, err := io.ReadFull(b.stdout, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, down) {
		t.Fatal("bytes from the server reached the client changed")
	}
	b.stdin.Close()
	if code := b.exit(t); code != 0 {
		t.Fatalf("exit %d after the end of stdin, want 0", code)
	}
}

func TestBridgeExitsWhenItsClientGoesAway(t *testing.T) {
	t.Run("end of stdin", func(t *testing.T) {
		ln, socket := listen(t)
		b := startBridge(t, socket)
		conn := accept(t, ln)
		b.ready(t)
		b.stdin.Close()
		if code := b.exit(t); code != 0 {
			t.Fatalf("exit %d, want 0", code)
		}
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("the socket was not closed: %v", err)
		}
	})
	t.Run("the socket closes", func(t *testing.T) {
		ln, socket := listen(t)
		b := startBridge(t, socket)
		conn := accept(t, ln)
		b.ready(t)
		conn.Close()
		if code := b.exit(t); code != 0 {
			t.Fatalf("exit %d, want 0", code)
		}
	})
	t.Run("60 s of silence on stdin", func(t *testing.T) {
		clk := useBridgeClock(t)
		ln, socket := listen(t)
		b := startBridge(t, socket)
		conn := accept(t, ln)
		b.ready(t)
		clk.waitCalls(t, 1)
		clk.Advance(30 * time.Second)
		// A byte at 30 s: the silence starts again.
		go b.stdin.Write([]byte{0})
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
			t.Fatal(err)
		}
		clk.Advance(30 * time.Second) // the timer of 60 s fires; 30 s were silent
		clk.waitCalls(t, 2)
		select {
		case code := <-b.done:
			t.Fatalf("the bridge exited %d after 30 s of silence", code)
		case <-time.After(50 * time.Millisecond):
		}
		clk.Advance(30 * time.Second)
		if code := b.exit(t); code != 1 {
			t.Fatalf("exit %d after 60 s of silence, want 1", code)
		}
	})
}

func TestBridgeConnectsOnlyToTheStateSocket(t *testing.T) {
	state, socket := listen(t)
	other, otherPath := listen(t)
	// Neither the environment nor any byte the client sends names where
	// the bridge connects.
	t.Setenv("DEVTOOLS_SOCKET", otherPath)
	t.Setenv("XDG_STATE_HOME", filepath.Dir(otherPath))
	b := startBridge(t, socket)
	conn := accept(t, state)
	b.ready(t)
	msg := []byte(`{"t":"hello","socket":"` + otherPath + `"}` + "\n" + otherPath + "\n")
	go b.stdin.Write(msg)
	got := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("the state socket got %q (%v)", got, err)
	}
	noConnection(t, other)
	noConnection(t, state)
	b.stdin.Close()
	if code := b.exit(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
