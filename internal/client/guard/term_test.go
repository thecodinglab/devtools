package guard

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain is hermetic by construction: the guard reads no environment of
// its own (Env and Check are handed the list), no file and no terminal
// other than the scripted ones and the pipes of these tests.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// The answers E6 recorded from real programs, verbatim
// (docs/replace-herdr/experiments/e6-probe.md, "Raw output").
const (
	answerDevtools   = "\x1bP>|devtools 0.0.0-e6 florian-nixos\x1b\\\x1b[?62;22c"
	answerLibghostty = "\x1bP>|libghostty\x1b\\\x1b[?62;22c"
	answerTmux       = "\x1bP>|tmux 3.8\x1b\\\x1b[?1;2;4c"
	answerScreen     = "\x1b[?1;2c" // GNU screen 5.0.2: DA1, no XTVERSION
	answerNvim       = "\x1bP>|libvterm(0.3)\x1b\\\x1b[?61;22;52c"
	// answerTerminal stands in for a plain terminal that names itself. That
	// ghostty answers XTVERSION is UNVERIFIED; provisional E6 (row 1 of its
	// pending [GO] steps): the fixture is the form of 3.4, not a recording.
	answerTerminal = "\x1bP>|ghostty 1.3.1\x1b\\\x1b[?62;22c"
)

// fakeTerm is a scripted terminal: it records every write, and once the
// two queries were written it lets its reader see the reply, chunk by
// chunk, after the bytes that were "typed" before. Without a reply it is
// silent: a read blocks until the test ends. It has no SetReadDeadline, so
// Probe reads it through its goroutine.
type fakeTerm struct {
	mu      sync.Mutex
	written []byte
	writes  int
	reply   []string
	eof     bool // the reader sees io.EOF after the reply
	chunks  chan string
	pending string
	stop    chan struct{}
	once    sync.Once
}

func newFakeTerm(t testing.TB, typed, reply []string) *fakeTerm {
	f := &fakeTerm{
		reply:  reply,
		chunks: make(chan string, len(typed)+len(reply)+1),
		stop:   make(chan struct{}),
	}
	for _, c := range typed {
		f.chunks <- c
	}
	t.Cleanup(func() { f.once.Do(func() { close(f.stop) }) })
	return f
}

func (f *fakeTerm) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, b...)
	f.writes++
	if strings.Contains(string(f.written), queries) && f.reply != nil {
		for _, c := range f.reply {
			f.chunks <- c
		}
		if f.eof {
			close(f.chunks)
		}
		f.reply = nil
	}
	return len(b), nil
}

func (f *fakeTerm) Read(b []byte) (int, error) {
	if f.pending == "" {
		select {
		case c, ok := <-f.chunks:
			if !ok {
				return 0, io.EOF
			}
			f.pending = c
		case <-f.stop:
			return 0, io.EOF
		}
	}
	n := copy(b, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func (f *fakeTerm) Written() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.written), f.writes
}

// pipeTerm is a terminal made of two pipes, whose read deadline works as
// the one of a terminal the program opened does. The test writes the
// terminal's answers into answers.
type pipeTerm struct {
	r       *os.File // what Probe reads
	answers *os.File // the other end of r
	w       *os.File // what Probe writes
	queries *os.File // the other end of w
}

func newPipeTerm(t *testing.T) *pipeTerm {
	t.Helper()
	r, answers, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	queries, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, f := range []*os.File{r, answers, w, queries} {
			f.Close()
		}
	})
	return &pipeTerm{r: r, answers: answers, w: w, queries: queries}
}

func (p *pipeTerm) Read(b []byte) (int, error)        { return p.r.Read(b) }
func (p *pipeTerm) Write(b []byte) (int, error)       { return p.w.Write(b) }
func (p *pipeTerm) SetReadDeadline(t time.Time) error { return p.r.SetReadDeadline(t) }

// answerAfterQueries starts a goroutine that reads the two queries from the
// pipe and then writes the reply, as a terminal does.
func (p *pipeTerm) answerAfterQueries(t *testing.T, reply string) {
	t.Helper()
	go func() {
		buf := make([]byte, len(queries))
		if _, err := io.ReadFull(p.queries, buf); err != nil {
			return
		}
		p.answers.WriteString(reply)
	}()
}

// probe runs Probe on a scripted terminal with the given reply chunks.
func probe(t *testing.T, reply ...string) Answer {
	t.Helper()
	a, err := Probe(newFakeTerm(t, nil, reply), 2*time.Second)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return a
}
