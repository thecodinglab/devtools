package server

import (
	"sync"
	"time"

	"devtools/internal/server/persist"
)

// The stop steps of 3.9, driven from the caller's goroutine (boot's signal
// goroutine) and not dependent on the loop making progress: stop accepting;
// take the state from the loop, waiting at most stateWait, and save it
// synchronously; close every pane in parallel, at most paneWait; drain every
// lane for laneDrain and close the connections. Removing the socket and
// releasing the lock are boot's, and so is the watchdog. Exits of programs
// during the stop change nothing (exited, events.go), and nothing is saved
// after the synchronous save.
const (
	stateWait = time.Second
	paneWait  = 750 * time.Millisecond
	laneDrain = 500 * time.Millisecond
	// endWait bounds the wait for the server's own goroutines at the end; a
	// reader that a pane still holds does not keep Shutdown from returning.
	endWait = time.Second
)

// Shutdown stops the server (3.9); safe from a signal goroutine;
// idempotent: a second call waits for the first.
func (s *Server) Shutdown() {
	s.stopOnce.Do(s.shutdown)
	<-s.stopped
}

func (s *Server) shutdown() {
	defer close(s.stopped)

	// Stop accepting.
	s.mu.Lock()
	s.stopping.Store(true)
	if s.ln != nil {
		s.ln.Close()
	}
	s.mu.Unlock()

	// Take the state and save it before any pane is closed.
	got := make(chan persist.State, 1)
	timer := time.NewTimer(stateWait)
	defer timer.Stop()
	take := func(c *core) {
		c.final = true
		got <- c.model.Save()
	}
	select {
	case s.mail <- take:
		select {
		case st := <-got:
			s.saveFinal(st)
		case <-timer.C:
			s.log.Error("the state loop did not hand over the state; it is not saved", "after", stateWait.String())
		}
	case <-timer.C:
		s.log.Error("the state loop's mailbox stayed full; the state is not saved", "after", stateWait.String())
	}

	// Close every pane in parallel.
	s.mu.Lock()
	panes := make([]Pane, 0, len(s.live))
	for p := range s.live {
		panes = append(panes, p)
	}
	conns := make([]*conn, 0, len(s.conns))
	for cn := range s.conns {
		conns = append(conns, cn)
	}
	s.mu.Unlock()
	waitAll(paneWait, len(panes), func(i int) { panes[i].Close() })

	// Drain every lane and close the connections.
	waitAll(laneDrain+100*time.Millisecond, len(conns), func(i int) {
		conns[i].lane.Close(laneDrain)
		conns[i].nc.Close()
	})

	// End the loop, the saver and the scan, and wait for them.
	s.end()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(endWait):
		s.log.Warn("goroutines of the server still run after the stop", "after", endWait.String())
	}
}

// waitAll runs f(0) .. f(n-1) in parallel and waits for them, at most d.
func waitAll(d time.Duration, n int, f func(i int)) {
	if n == 0 {
		return
	}
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			f(i)
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// saveFinal is the synchronous save of the stop; the saver writes nothing
// after it.
func (s *Server) saveFinal(st persist.State) {
	if s.o.StateDir == "" {
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.saveDone = true
	if err := persist.Save(s.statePath(), st); err != nil {
		s.log.Error("saving the state at shutdown failed", "file", s.statePath(), "err", err)
	}
}
