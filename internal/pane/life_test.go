package pane

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/testkit"
	"devtools/internal/vt"
)

func TestLastOutputBeforeExitIsNotLost(t *testing.T) {
	for i := range 5 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			o, r := options(t, "burst", "100")
			o.Cols = 80
			// Notify runs on the actor: it reads the terminal as it is at
			// the moment of the exit event.
			seen := make(chan string, 1)
			var p *Pane
			ready := make(chan struct{})
			o.Notify = func(e Event) {
				r.notify(e)
				if e.Kind == Exited {
					<-ready
					var rows []string
					for y := range 24 {
						rows = append(rows, p.a.term.Row(y))
					}
					seen <- strings.Join(rows, "\n")
				}
			}
			p = start(t, o)
			close(ready)
			// Hold the actor until the child was reaped: its whole output
			// then still waits in the PTY when the exit is known.
			p.ops.push(func(*actor) { <-p.reapedc })
			select {
			case s := <-seen:
				if !strings.Contains(s, "last words") {
					t.Fatalf("the screen at the exit event lacks the last output:\n%s", s)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("no Exited event")
			}
		})
	}
}

func TestExactlyOneExitedEvent(t *testing.T) {
	p, r := startHelper(t, "exit", "0")
	waitExit(t, p, r)
	p.Close()
	p.Close()
	if n := r.count(Exited); n != 1 {
		t.Errorf("%d Exited events for a program that exited", n)
	}
	q, rq := startHelper(t, "sighup")
	waitScreen(t, q, "ready")
	q.Close()
	if n := rq.count(Exited); n != 1 {
		t.Errorf("%d Exited events for a pane that was closed", n)
	}
}

func TestExitCodeAndSignalAreReported(t *testing.T) {
	p, r := startHelper(t, "exit", "7")
	info := waitExit(t, p, r)
	if info.State != "exited" || info.Exit == nil || info.Exit.Code != 7 || info.Exit.Signal != "" || info.Exit.ByServer {
		t.Errorf("exit 7: %+v %+v", info, info.Exit)
	}
	q, rq := startHelper(t, "killself", "TERM")
	info = waitExit(t, q, rq)
	if info.Exit == nil || info.Exit.Signal != "SIGTERM" || info.Exit.ByServer {
		t.Errorf("killself TERM: %+v", info.Exit)
	}
	if q.Key(vt.Key{Name: "a", Text: "a"}) || q.Paste("x", true, true) || q.Focus(true) || q.Mouse(vt.Mouse{Kind: "press", Button: "left"}) {
		t.Errorf("input to an exited pane returned true")
	}
	// The exited pane still shows its screen.
	if s := q.Snapshot(0, false); s.Rows != 24 || len(s.Lines) != 24 {
		t.Errorf("snapshot of an exited pane: %d rows", s.Rows)
	}
}

func TestSpawnFailureIsAnExitedPaneWithText(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		text string
	}{
		{[]string{"/nonexistent/devtools-test-program"}, "no such file"},
		{[]string{"devtools-test-no-such-program"}, "not found"},
		{nil, "no program"},
	} {
		o, r := options(t, "exit", "0")
		o.Argv = tc.argv
		p := start(t, o)
		info := p.Info()
		if info.State != "exited" || info.Exit == nil || !strings.Contains(info.Exit.Text, tc.text) || info.Pid != 0 {
			t.Errorf("Start(%q): %+v %+v", tc.argv, info, info.Exit)
			continue
		}
		if p.Key(vt.Key{Name: "a", Text: "a"}) {
			t.Errorf("Key on a pane that did not start returned true")
		}
		if s := p.Snapshot(0, false); s.Rows != 24 || s.Cols != 200 {
			t.Errorf("snapshot of a pane that did not start: %dx%d", s.Cols, s.Rows)
		}
		testkit.Eventually(t, 5*time.Second, "the Exited event", func() bool { return r.count(Exited) == 1 })
		began := time.Now()
		p.Close()
		if took := time.Since(began); took > time.Second {
			t.Errorf("Close of a pane that did not start took %v", took)
		}
		if n := r.count(Exited); n != 1 {
			t.Errorf("%d Exited events", n)
		}
	}
}

func TestCloseEscalatesToKill(t *testing.T) {
	o, r := options(t, "stubborn")
	o.KillGrace = 50 * time.Millisecond
	p := start(t, o)
	waitScreen(t, p, "ready")
	began := time.Now()
	p.Close()
	took := time.Since(began)
	info := p.Info()
	if info.Exit == nil || info.Exit.Signal != "SIGKILL" || !info.Exit.ByServer {
		t.Fatalf("a program that ignores HUP and TERM ended with %+v", info.Exit)
	}
	if took < 100*time.Millisecond {
		t.Errorf("Close took %v, less than two graces of 50 ms", took)
	}
	if r.count(Exited) != 1 {
		t.Errorf("%d Exited events", r.count(Exited))
	}
}

func TestCloseOfAnExitedPaneReturnsAtOnce(t *testing.T) {
	o, r := options(t, "exit", "0")
	o.KillGrace = 10 * time.Second
	p := start(t, o)
	waitExit(t, p, r)
	began := time.Now()
	p.Close()
	if took := time.Since(began); took > time.Second {
		t.Errorf("Close of an exited pane took %v", took)
	}
}

func TestCloseMarksTheExitAsCausedByTheServer(t *testing.T) {
	p, _ := startHelper(t, "sighup")
	waitScreen(t, p, "ready")
	p.Close()
	info := p.Info()
	if info.State != "exited" || info.Exit == nil || !info.Exit.ByServer || info.Exit.Signal != "SIGHUP" {
		t.Errorf("after Close: %+v %+v", info, info.Exit)
	}
}

func TestChildLeadsASessionWithTheControllingTerminal(t *testing.T) {
	p, _ := startHelper(t, "session")
	out := waitScreen(t, p, "sid=")
	var sid, pid, pgrp, fg int
	line := out[strings.Index(out, "sid="):]
	if _, err := fmt.Sscanf(line, "sid=%d pid=%d pgrp=%d fg=%d", &sid, &pid, &pgrp, &fg); err != nil {
		t.Fatalf("the helper printed %q: %v", line, err)
	}
	if sid != pid || pgrp != pid || fg != pid {
		t.Errorf("sid %d pgrp %d fg %d, want all %d", sid, pgrp, fg, pid)
	}
	info := p.Info()
	if info.Pid != pid || info.Shell != pid || info.Fg != pid || info.State != "running" {
		t.Errorf("Info %+v, the child is %d", info, pid)
	}
}

func TestEndOfFileArrivesWhenTheChildExits(t *testing.T) {
	o, r := options(t, "exit", "0")
	p := start(t, o)
	testkit.Eventually(t, 20*time.Second, "the reader's end of file", func() bool {
		select {
		case <-p.readerDone:
			return true
		default:
			return false
		}
	})
	waitExit(t, p, r)
}

func TestIUTF8IsSet(t *testing.T) {
	p, _ := startHelper(t, "cat")
	// In canonical mode with IUTF8 one erase removes the whole character
	// "ä" (two bytes); without it, one byte stays behind.
	for _, k := range []vt.Key{{Name: "x", Text: "x"}, {Name: "ä", Text: "ä"}, {Name: "backspace"}, {Name: "enter"}} {
		p.Key(k)
	}
	testkit.Eventually(t, 20*time.Second, "cat repeats the line", func() bool {
		s := p.Snapshot(0, false)
		return s.Lines[1] != ""
	})
	if s := p.Snapshot(0, false); s.Lines[0] != "x" || s.Lines[1] != "x" {
		t.Errorf("the line discipline erased one byte of a character: %q", s.Lines[:2])
	}
}

func TestLoginShellArgvZero(t *testing.T) {
	// The env helper prints its environment, its argv[0] and its directory,
	// then exits.
	run := func(o Options, r *recorder) (arg0, dir string) {
		t.Helper()
		p := start(t, o)
		waitExit(t, p, r)
		lines := strings.Split(screen(p), "\n")
		if len(lines) < 2 {
			t.Fatalf("the env helper printed %q", lines)
		}
		return lines[len(lines)-2], lines[len(lines)-1]
	}
	same := func(a, b string) bool {
		ra, _ := filepath.EvalSymlinks(a)
		rb, _ := filepath.EvalSymlinks(b)
		return ra != "" && ra == rb
	}
	o, r := options(t, "env")
	o.Arg0 = "-dtsh"
	sub := filepath.Join(o.Dir, "work")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	o.Dir = sub
	arg0, dir := run(o, r)
	if arg0 != "-dtsh" {
		t.Errorf("argv[0] = %q, want -dtsh", arg0)
	}
	if !same(dir, sub) {
		t.Errorf("the program started in %q, want %q", dir, sub)
	}
	// Without Arg0, argv[0] is the program's path.
	o, r = options(t, "env")
	if arg0, _ := run(o, r); arg0 != o.Argv[0] {
		t.Errorf("argv[0] without Arg0 = %q, want %q", arg0, o.Argv[0])
	}
	// A directory that does not exist: HOME of the pane's environment.
	o, r = options(t, "env")
	o.Dir = filepath.Join(o.Dir, "gone")
	home, _ := lookEnv(o.Env, "HOME")
	if _, dir := run(o, r); !same(dir, home) {
		t.Errorf("without its directory the program started in %q, want %q", dir, home)
	}
}

func TestThePaneKnowsItsTerminalDevice(t *testing.T) {
	p, _ := startHelper(t, "tty")
	out := waitScreen(t, p, "same=")
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			vals[k] = v
		}
	}
	info := p.Info()
	if vals["env"] == "" || vals["env"] != vals["tty"] || vals["env"] != info.TTY {
		t.Errorf("%s=%q, the terminal is %q, Info.TTY %q", proto.EnvTTY, vals["env"], vals["tty"], info.TTY)
	}
	if vals["same"] != "true" {
		t.Errorf("platform.SameTTY of the child's standard input and %s is %q", proto.EnvTTY, vals["same"])
	}
	if platform.SameTTY(nil, info.TTY) {
		t.Errorf("SameTTY of nothing is true")
	}
}

func TestNoGoroutineIsLeftAfterClose(t *testing.T) {
	testkit.RequirePTY(t)
	before := runtime.NumGoroutine()
	o, _ := options(t, "hex")
	p := Start(o)
	p.Watch(func() {})
	cancel := p.Watch(func() {})
	waitScreen(t, p, "ready")
	if n := runtime.NumGoroutine(); n < before+6 {
		t.Errorf("%d goroutines while the pane runs, %d before", n, before)
	}
	cancel()
	p.Close()
	o.Argv = nil
	q := Start(o) // a pane whose program did not start
	q.Watch(func() {})
	q.Close()
	testkit.Eventually(t, 10*time.Second, "the goroutines of the panes end", func() bool {
		return runtime.NumGoroutine() <= before
	})
	// Watch on a closed pane starts nothing.
	p.Watch(func() {})()
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("Watch on a closed pane left %d goroutines", n-before)
	}
}

func TestTheMasterIsPolledSoCloseEndsTheReader(t *testing.T) {
	p, _ := startHelper(t, "hex")
	waitScreen(t, p, "ready")
	if err := p.master.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("the PTY master is not polled by the runtime: %v", err)
	}
}
