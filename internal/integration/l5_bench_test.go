package integration

// BenchmarkKeyToScreen: the time from a key written into the outer terminal
// to its effect on the outer terminal's screen, through the client, the
// server and the pane's program, at level 5. It is reported, not held
// against a bound (T4 of 10.4 is measured at level 6).
//
// The harness of the tests takes a *testing.T, which a benchmark does not
// have, so this file starts the three processes itself, the same way:
// the boot server of main_test.go, the attach helper on a PTY, and an
// outer terminal that is a vt.Term answering its queries.

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"devtools/internal/testkit"
	"devtools/internal/testkit/golden"
	"devtools/internal/testkit/termtest"
	"devtools/internal/vt"
)

// benchTerm is a minimal outer terminal: a PTY master whose output feeds a
// vt.Term, the engine's replies written back, XTVERSION answered with the
// name of termtest.
type benchTerm struct {
	master *os.File
	mu     sync.Mutex
	vt     vt.Term
	done   chan struct{}
}

func (x *benchTerm) read() {
	defer close(x.done)
	buf := make([]byte, 32<<10)
	for {
		n, err := x.master.Read(buf)
		if n > 0 {
			b := buf[:n]
			var out []byte
			for _, q := range [][]byte{[]byte("\x1b[>0q"), []byte("\x1b[>q")} {
				for bytes.Contains(b, q) {
					b = bytes.Replace(b, q, nil, 1)
					out = append(out, "\x1bP>|"+termtest.DefaultName+"\x1b\\"...)
				}
			}
			x.mu.Lock()
			x.vt.Write(b)
			out = append(out, x.vt.Replies()...)
			x.mu.Unlock()
			if len(out) > 0 {
				x.master.Write(out)
			}
		}
		if err != nil {
			return
		}
	}
}

// row returns the plain text of the screen's row y.
func (x *benchTerm) row(y int) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return golden.PlainRow(x.vt.Row(y))
}

// benchStart starts argv with env on the PTY of x as its controlling
// terminal.
func benchStart(b *testing.B, argv, env []string, dir string) (*benchTerm, *os.Process) {
	master, slave, err := pty.Open()
	if err != nil {
		b.Fatal(err)
	}
	pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24})
	x := &benchTerm{master: master, done: make(chan struct{}),
		vt: vt.New(vt.Options{Cols: 80, Rows: 24, History: 100, Colors: termtest.Colors})}
	p, err := os.StartProcess(argv[0], argv, &os.ProcAttr{
		Dir: dir, Env: env, Files: []*os.File{slave, slave, slave},
		Sys: &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0},
	})
	slave.Close()
	if err != nil {
		b.Fatal(err)
	}
	go x.read()
	return x, p
}

func BenchmarkKeyToScreen(b *testing.B) {
	if os.Getenv("DEVTOOLS_TEST_NO_PTY") == "1" {
		b.Skip("DEVTOOLS_TEST_NO_PTY=1")
	}
	home, err := os.MkdirTemp("/tmp", "dt-")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(home)
	dir := filepath.Join(home, "work")
	os.MkdirAll(dir, 0o700)

	// The server, as startBoot runs it, its panes the helper counter.
	argv, helper := testkit.HelperCommand("bootserver")
	srv := exec.Command(argv[0], argv[1:]...)
	srv.Env = testkit.CleanEnv(home, append(helper, "SHELL="+argv[0], envPaneSpec+"=counter",
		"GORACE=atexit_sleep_ms=0", "DEVTOOLS_PASS_ENV="+testkit.HelperVar+","+envPaneSpec+",GORACE")...)
	srv.Dir = home
	if err := srv.Start(); err != nil {
		b.Fatal(err)
	}
	defer func() {
		srv.Process.Signal(syscall.SIGTERM)
		srv.Wait()
	}()
	socket := filepath.Join(home, ".local", "state", "devtools", "server.sock")
	for deadline := time.Now().Add(15 * time.Second); ; {
		if nc, err := net.Dial("unix", socket); err == nil {
			nc.Close()
			break
		}
		if time.Now().After(deadline) {
			b.Fatal("the server does not listen")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The client on the outer terminal.
	argv, helper = testkit.HelperCommand("attach", dir, "work")
	x, client := benchStart(b, argv, testkit.CleanEnv(home, append(helper, "GORACE=atexit_sleep_ms=0")...), home)
	defer func() {
		syscall.Kill(-client.Pid, syscall.SIGHUP)
		client.Wait()
		x.master.Close()
		<-x.done
		x.mu.Lock()
		x.vt.Close()
		x.mu.Unlock()
	}()
	wait := func(want string) {
		deadline := time.Now().Add(10 * time.Second)
		for x.row(0) != want {
			if time.Now().After(deadline) {
				b.Fatalf("the screen shows %q, not %q", x.row(0), want)
			}
			time.Sleep(100 * time.Microsecond)
		}
	}
	wait("n=0")

	n := 0
	for range 20 { // warm up
		n++
		x.master.WriteString("a")
		wait(fmt.Sprintf("n=%d", n))
	}
	// One key at a time on an idle pane (T4): the pause lets the frame
	// pacer of 3.2.3 forget the previous frame, so the key's frame goes
	// out at once.
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		time.Sleep(25 * time.Millisecond)
		b.StartTimer()
		n++
		x.master.WriteString("a")
		wait(fmt.Sprintf("n=%d", n))
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N)/1000, "ms/key")
}
