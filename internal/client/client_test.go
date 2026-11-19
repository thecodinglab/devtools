package client_test

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client"
	"devtools/internal/client/clienttest"
	"devtools/internal/client/guard"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket or a multiplexer reaches them (10.1).
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-client-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_BIN_PATH",
		"HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_SESSION",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// --- fixtures ---

const dir = "/home/u/dev/devtools/main"

// online is the summary of a local machine that answers, named desktop.
func online(machines ...fleet.MachineState) fleet.Update {
	ms := append([]fleet.MachineState{{Name: "", Host: "desktop", State: "online"}}, machines...)
	return fleet.Update{Kind: fleet.Summary, Summary: fleet.SummaryData{Mode: "showing", Machines: ms}}
}

// snapshot is a state with session s1 and tab t1 of the given panes; the
// first pane is focused.
func snapshot(split string, panes ...proto.Pane) proto.Snapshot {
	ids := []string{}
	for _, p := range panes {
		ids = append(ids, p.ID)
	}
	for i := range panes {
		panes[i].Tab, panes[i].Session = "t1", "s1"
		if panes[i].State == "" {
			panes[i].State = proto.PaneRunning
		}
	}
	return proto.Snapshot{
		Rev:      1,
		Sessions: []proto.Session{{ID: "s1", Dir: dir, Label: "devtools-main", Tabs: []string{"t1"}, Current: "t1"}},
		Tabs:     []proto.Tab{{ID: "t1", Session: "s1", Index: 1, Panes: ids, Split: split, Focused: first(ids)}},
		Panes:    panes,
	}
}

func first(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// shown commits the view of session s1 with the tab's panes.
func shown(machine string, s proto.Snapshot) fleet.Update {
	t := s.Tabs[0]
	for _, tab := range s.Tabs {
		if tab.ID == s.Sessions[0].Current {
			t = tab
		}
	}
	return fleet.Update{Kind: fleet.Shown, Target: session.Target{Machine: machine, Session: "s1"},
		View: proto.ViewOK{Session: "s1", Dir: dir, Tab: t.ID, Panes: t.Panes}}
}

// frame is an update of kind Frame: a frame of pane, with rows y=0.. set
// from lines.
func frame(pane string, seq uint64, cols, rows int, full bool, lines ...string) fleet.Update {
	f := proto.Frame{Pane: pane, Seq: seq, Cols: cols, Rows: rows, Full: full,
		Cursor: proto.Cursor{Visible: true, Shape: "block"}}
	for y, s := range lines {
		f.Lines = append(f.Lines, proto.Line{Y: y, S: s})
	}
	if full {
		for y := len(lines); y < rows; y++ {
			f.Lines = append(f.Lines, proto.Line{Y: y})
		}
	}
	return fleet.Update{Kind: fleet.Frame, Frame: proto.Encode(f)}
}

// frameWith is frame with the cursor and modes set.
func frameWith(pane string, seq uint64, cols, rows int, cur proto.Cursor, modes proto.Modes, lines ...string) fleet.Update {
	u := frame(pane, seq, cols, rows, true, lines...)
	var f proto.Frame
	u.Frame.Decode(&f)
	f.Cursor, f.Modes = cur, modes
	u.Frame = proto.Encode(f)
	return u
}

// showOne is a driver showing one pane p1 of 80 by 23 running prog.
func showOne(t *testing.T, opts client.Options, p proto.Pane, lines ...string) *clienttest.Driver {
	t.Helper()
	d := clienttest.New(t, opts)
	s := snapshot("", p)
	d.F.SetState(s)
	d.Update(online())
	d.Update(shown("", s))
	d.Update(frame(p.ID, 1, 80, 23, true, lines...))
	return d
}

// keyOf decodes an input.key.
func keyOf(t *testing.T, m proto.Msg) proto.Key {
	t.Helper()
	if m.T != proto.TypeInputKey {
		t.Fatalf("got %s, want input.key", m.T)
	}
	var k proto.Key
	if err := m.Decode(&k); err != nil {
		t.Fatal(err)
	}
	return k
}

// --- Run ---

// ordinaryTerminal answers the guard's two queries as a terminal that is
// not a multiplexer does.
type ordinaryTerminal struct {
	mu      sync.Mutex
	pending []byte
	ready   chan struct{}
	once    sync.Once
}

func (o *ordinaryTerminal) Write(p []byte) (int, error) {
	o.once.Do(func() { close(o.ready) })
	return len(p), nil
}

func (o *ordinaryTerminal) Read(p []byte) (int, error) {
	<-o.ready
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, o.pending)
	o.pending = o.pending[n:]
	return n, nil
}

// realPass is a pass from guard.Check on an ordinary terminal; no
// constructor exists.
func realPass(t *testing.T) *guard.Pass {
	t.Helper()
	term := &ordinaryTerminal{pending: []byte("\x1bP>|ghostty 1.2.3\x1b\\\x1b[?62;22c"), ready: make(chan struct{})}
	pass, nested, err := guard.Check(term, []string{"TERM=xterm-ghostty", "HOME=/nonexistent"}, nil, 2*time.Second, false)
	if err != nil || nested != nil || !pass.Valid() {
		t.Fatalf("guard.Check = %v, %+v, %v; want a valid pass", pass.Valid(), nested, err)
	}
	return pass
}

func everythingIsATerminal(*os.File) bool { return true }

// tempFile is an empty file that stands for the terminal.
func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func size(t *testing.T, f *os.File) int64 {
	t.Helper()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func TestClientRunNeedsAPass(t *testing.T) {
	for name, pass := range map[string]*guard.Pass{"nil": nil, "zero": {}} {
		t.Run(name, func(t *testing.T) {
			f := clienttest.NewFleet()
			out := tempFile(t)
			err := client.Run(client.Options{Pass: pass, Fleet: f, In: tempFile(t), Out: out})
			if !errors.Is(err, guard.ErrNoPass) {
				t.Fatalf("Run = %v, want guard.ErrNoPass", err)
			}
			if n := size(t, out); n != 0 {
				t.Errorf("the output received %d bytes, want none", n)
			}
			if calls := f.Calls(); len(calls) != 0 {
				t.Errorf("the fleet saw %v, want no call", calls)
			}
		})
	}
}

func TestRunRefusesWithoutATerminal(t *testing.T) {
	f := clienttest.NewFleet()
	f.Push(online())
	out := tempFile(t)
	err := client.Run(client.Options{Pass: realPass(t), Fleet: f, In: tempFile(t), Out: out})
	if !errors.Is(err, client.ErrNotTerminal) {
		t.Fatalf("Run with files = %v, want ErrNotTerminal", err)
	}
	if n := size(t, out); n != 0 {
		t.Errorf("the output received %d bytes, want none", n)
	}
}

func TestRunReturnsErrNoServerWithoutTheAlternateScreen(t *testing.T) {
	for name, machines := range map[string][]config.Machine{
		"without machine lines": nil,
		"with two":              {{Name: "macmini", Connect: true}, {Name: "laptop"}},
	} {
		t.Run(name, func(t *testing.T) {
			r := fleettest.NewUnread(t, machines...)
			r.M("").Fail(remote.NoServer)
			out, in, pass := tempFile(t), tempFile(t), realPass(t)
			done := make(chan error, 1)
			go func() {
				done <- client.RunForTest(client.Options{Pass: pass, Fleet: r.F, In: in, Out: out}, everythingIsATerminal)
			}()
			// the fleet sends a changed summary at most once per 50 ms of
			// its (fake) clock
			var err error
		wait:
			for {
				select {
				case err = <-done:
					break wait
				case <-time.After(5 * time.Millisecond):
					r.Advance(60 * time.Millisecond)
				}
			}
			if !errors.Is(err, client.ErrNoServer) {
				t.Fatalf("Run = %v, want ErrNoServer", err)
			}
			if n := size(t, out); n != 0 {
				t.Errorf("the output received %d bytes, want none", n)
			}
		})
	}
}

// pipeTerminal is a terminal made of two pipes: what the client writes is
// collected write by write.
type pipeTerminal struct {
	inR, inW   *os.File
	outR, outW *os.File
	mu         sync.Mutex
	chunks     []string
	done       chan struct{}
}

func newPipeTerminal(t *testing.T) *pipeTerminal {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeTerminal{inR: inR, inW: inW, outR: outR, outW: outW, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		buf := make([]byte, 1<<16)
		for {
			n, err := outR.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.chunks = append(p.chunks, string(buf[:n]))
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		inR.Close()
		outW.Close()
		<-p.done
		outR.Close()
	})
	return p
}

func (p *pipeTerminal) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.chunks, "")
}

func (p *pipeTerminal) writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.chunks...)
}

func (p *pipeTerminal) waitFor(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(p.output(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("%q never reached the terminal; it got %q", text, p.output())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// runClient runs the client on a pipe terminal of 80 by 24 until the fleet
// is closed.
func runClient(t *testing.T, f *clienttest.Fleet, term *pipeTerminal) (wait func() error) {
	t.Helper()
	pass := realPass(t)
	done := make(chan error, 1)
	go func() {
		done <- client.RunForTest(client.Options{Pass: pass, Fleet: f, In: term.inR, Out: term.outW, Env: []string{"LANG=C.UTF-8"}},
			everythingIsATerminal, tea.WithWindowSize(80, 24))
	}()
	var once sync.Once
	var result error
	wait = func() error {
		once.Do(func() {
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				result = errors.New("the client did not end after the fleet ended")
			}
		})
		return result
	}
	t.Cleanup(func() {
		f.Close()
		if err := wait(); err != nil {
			t.Error(err)
		}
	})
	return wait
}

func TestRunDoesNotProbeTheTerminal(t *testing.T) {
	f := clienttest.NewFleet()
	f.Push(online())
	term := newPipeTerminal(t)
	wait := runClient(t, f, term)
	term.waitFor(t, "\x1b[?1049h")
	f.Close()
	if err := wait(); err != nil {
		t.Fatalf("Run = %v", err)
	}
	out := term.output()
	if strings.Contains(out, "\x1b[>q") || strings.HasPrefix(out, "\x1b[c") || strings.Contains(out, "\x1b[>0q") {
		t.Errorf("the client asked the guard's queries: %q", out)
	}
	if !strings.HasPrefix(out, "\x1b[") {
		t.Errorf("the first bytes are %q, want the start sequence of the program", out[:min(len(out), 20)])
	}
}

func TestTwoUpdatesTenMillisecondsApartAreWrittenSeparately(t *testing.T) {
	f := clienttest.NewFleet()
	s := snapshot("", proto.Pane{ID: "p1", Program: "zsh"})
	f.SetState(s)
	f.Push(online())
	term := newPipeTerminal(t)
	runClient(t, f, term)
	f.Push(shown("", s))
	f.Push(frame("p1", 1, 80, 23, true, "ready"))
	term.waitFor(t, "ready")
	f.Push(frame("p1", 2, 80, 23, false, "", "first-update"))
	time.Sleep(10 * time.Millisecond)
	f.Push(frame("p1", 3, 80, 23, false, "", "", "second-update"))
	term.waitFor(t, "second-update")
	for _, w := range term.writes() {
		if strings.Contains(w, "first-update") && strings.Contains(w, "second-update") {
			t.Fatalf("both updates were written in one write: %q", w)
		}
	}
}

func TestRunStartsTheInitialTarget(t *testing.T) {
	f := clienttest.NewFleet()
	f.Push(online())
	term := newPipeTerminal(t)
	done := make(chan error, 1)
	target := session.Target{Dir: dir, Name: "devtools-main"}
	pass := realPass(t)
	go func() {
		done <- client.RunForTest(client.Options{Pass: pass, Fleet: f, Initial: &target, In: term.inR, Out: term.outW},
			everythingIsATerminal, tea.WithWindowSize(80, 24))
	}()
	term.waitFor(t, "\x1b[?1049h")
	f.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sw := f.CallsOf("Switch")
	if len(sw) != 1 || sw[0].Target != target {
		t.Fatalf("Switch calls %+v, want one to %+v", sw, target)
	}
	term.waitFor(t, "\x1b[?1049l")
}
