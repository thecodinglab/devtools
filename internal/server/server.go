package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"devtools/internal/pane"
	"devtools/internal/server/model"
	"devtools/internal/server/persist"
)

// acceptRetry is how long Serve waits after a transient error of Accept
// before it accepts again (3.2.3).
const acceptRetry = 50 * time.Millisecond

// mailboxSize is the capacity of the state loop's mailbox (3.2.3).
const mailboxSize = 1024

// Server is one devtools server: the state loop with the model and the
// connection registry, the accept loop, and the saver (3.2.3).
type Server struct {
	o    Options
	log  *slog.Logger
	boot string // welcome.boot: random per server start

	mail chan func(*core) // the loop's mailbox
	quit chan struct{}    // closed when the loop ends
	core *core            // owned by the loop goroutine

	saves chan persist.State // to the saver; capacity 1, the newest wins

	mu       sync.Mutex
	ln       net.Listener
	socket   atomic.Value // string: the absolute path of the socket Serve listens on
	stopping atomic.Bool
	live     map[Pane]bool  // every pane object not yet released, for the stop (guarded by mu)
	conns    map[*conn]bool // every admitted connection not yet dropped, for the stop (guarded by mu)

	stopOnce sync.Once     // Shutdown or halt, whichever comes first
	stopped  chan struct{} // closed when the stop is complete
	endOnce  sync.Once     // closes quit

	saveMu     sync.Mutex // one write of state.json at a time
	saveDone   bool       // the synchronous save of the stop was written (guarded by saveMu)
	savePaused bool       // a live upgrade holds the saver (guarded by saveMu)

	// The live upgrade (upgrade.go). hmail is the mailbox the loop takes
	// from while it holds the model for a handover (core.hold); inputMu is
	// the gate of the input path: a reader applies input under its read
	// lock, the handover closes the gate under the write lock.
	hmail   chan func(*core)
	inputMu sync.RWMutex
	gated   bool // guarded by inputMu
	up      upgradeState
	paused  atomic.Bool   // Accept is paused for a handover
	resumed chan struct{} // closed when the pause ends (guarded by mu)

	wg sync.WaitGroup // the loop, the saver, the scan's ticker, the watch of the installed build, every connection's goroutines
}

// New creates a server; it loads state.json (3.9). A file that does not
// parse or has an unknown version is set aside with one log line and the
// server starts empty; a file that cannot be read is an error. The state
// loop and the saver run from here on; Serve accepts connections.
func New(o Options) (*Server, error) {
	o = withDefaults(o)
	s := newServer(o, newBoot())
	st := persist.Empty()
	if o.StateDir != "" {
		loaded, err := persist.Load(s.statePath())
		var aside *persist.SetAside
		switch {
		case errors.As(err, &aside):
			s.log.Warn("state file set aside", "file", aside.Bad, "reason", aside.Reason)
		case err != nil:
			return nil, fmt.Errorf("server: load state: %w", err)
		}
		st = loaded
	}
	s.core = newCore(s, model.Load(st))
	s.start()
	return s, nil
}

// newServer is the server of New and of an adoption (adopt.go), without its
// core and its goroutines.
func newServer(o Options, boot string) *Server {
	s := &Server{
		o:       o,
		log:     o.Log,
		boot:    boot,
		mail:    make(chan func(*core), mailboxSize),
		hmail:   make(chan func(*core), 16),
		quit:    make(chan struct{}),
		saves:   make(chan persist.State, 1),
		live:    map[Pane]bool{},
		conns:   map[*conn]bool{},
		stopped: make(chan struct{}),
	}
	s.socket.Store("")
	return s
}

// start runs the loop, the saver, the scan's ticker and the watch of the
// installed build.
func (s *Server) start() {
	s.wg.Add(4)
	go s.loop()
	go s.saver()
	go s.ticker()
	go s.watchInstall()
}

// withDefaults fills every zero field of o that has a production value.
func withDefaults(o Options) Options {
	d := Defaults()
	if o.History <= 0 {
		o.History = d.History
	}
	for _, f := range []struct{ v, def *time.Duration }{
		{&o.FrameInterval, &d.FrameInterval}, {&o.CosmeticDelay, &d.CosmeticDelay},
		{&o.SyncHold, &d.SyncHold}, {&o.SaveDelay, &d.SaveDelay},
		{&o.StallTimeout, &d.StallTimeout}, {&o.PingInterval, &d.PingInterval},
		{&o.HelloTimeout, &d.HelloTimeout}, {&o.KillGrace, &d.KillGrace},
		{&o.ProcInterval, &d.ProcInterval}, {&o.FgInterval, &d.FgInterval},
		{&o.UpdateInterval, &d.UpdateInterval},
	} {
		if *f.v <= 0 {
			*f.v = *f.def
		}
	}
	if o.NewPane == nil {
		o.NewPane = func(po pane.Options) Pane { return pane.Start(po) }
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	for _, f := range []struct{ v, def *time.Duration }{
		{&o.UpgradeSettle, &d.UpgradeSettle}, {&o.UpgradeIdle, &d.UpgradeIdle},
		{&o.UpgradeEvery, &d.UpgradeEvery},
	} {
		if *f.v <= 0 {
			*f.v = *f.def
		}
	}
	return o
}

// newBoot returns the random welcome.boot of one server start.
func newBoot() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("server: random boot id: %v", err))
	}
	return hex.EncodeToString(b[:])
}

func (s *Server) statePath() string { return filepath.Join(s.o.StateDir, persist.FileName) }

// socketPath is the absolute path of the socket Serve listens on, "" before
// Serve; panes get it as DEVTOOLS_SOCKET.
func (s *Server) socketPath() string { return s.socket.Load().(string) }

// Serve accepts connections on l; it returns nil after Shutdown completed.
// A transient error of Accept is retried after 50 ms (3.2.3); the listener
// closed by anything else than the server's own stop is returned as an
// error.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.ln != nil {
		s.mu.Unlock()
		return errors.New("server: Serve called twice")
	}
	if s.stopping.Load() {
		s.mu.Unlock()
		return nil
	}
	s.ln = l
	s.mu.Unlock()
	if a, ok := l.Addr().(*net.UnixAddr); ok && a.Name != "" {
		path := a.Name
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		s.socket.Store(path)
	}
	for {
		nc, err := l.Accept()
		if err != nil {
			if s.stopping.Load() {
				<-s.stopped
				return nil
			}
			if s.paused.Load() {
				// A handover paused the accepting: connections wait in the
				// listen backlog for the new image, or for the resume.
				if !s.waitResumed() {
					return nil
				}
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("server: accept: %w", err)
			}
			s.log.Warn("accept failed; retrying", "err", err)
			select {
			case <-time.After(acceptRetry):
			case <-s.quit:
				<-s.stopped
				return nil
			}
			continue
		}
		s.mu.Lock()
		if s.stopping.Load() {
			s.mu.Unlock()
			nc.Close()
			<-s.stopped
			return nil
		}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.handshake(nc)
		}()
	}
}

// halt ends the server without the stop steps of 3.9: no save, panes closed
// without waiting. It stops accepting, drops every connection, ends the loop
// and the saver and waits for every goroutine of the server. Idempotent;
// after Shutdown it only waits for the end of the stop. Tests of the core
// end their server with it; Shutdown is the orderly stop.
func (s *Server) halt() {
	s.stopOnce.Do(func() {
		defer close(s.stopped)
		s.mu.Lock()
		s.stopping.Store(true)
		if s.ln != nil {
			s.ln.Close()
		}
		s.mu.Unlock()
		done := make(chan struct{})
		s.mail <- func(c *core) {
			c.final = true
			c.dropAll()
			c.closePanes()
			close(done)
		}
		<-done
		s.end()
		s.wg.Wait()
	})
	<-s.stopped
}

// end closes quit: the loop, the saver and the scan end. Idempotent.
func (s *Server) end() { s.endOnce.Do(func() { close(s.quit) }) }

// track and untrack keep the set of pane objects the stop closes.
func (s *Server) track(p Pane) {
	s.mu.Lock()
	s.live[p] = true
	s.mu.Unlock()
}

func (s *Server) untrack(p Pane) {
	s.mu.Lock()
	delete(s.live, p)
	s.mu.Unlock()
}

// pid is welcome.pid; a variable so that nothing else of the process is
// read in the handshake.
var pid = os.Getpid

// Listener is the listener Serve accepts on; for the in-process upgrade of
// the tests (servertest).
func (s *Server) Listener() (*net.UnixListener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ul, ok := s.ln.(*net.UnixListener); ok {
		return ul, nil
	}
	return nil, errors.New("server: not serving on a unix listener")
}
