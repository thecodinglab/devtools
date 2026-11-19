package server

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/server/persist"
	"devtools/internal/testkit"
)

func TestShutdownSavesBeforeItClosesPanes(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.SaveDelay = time.Hour })
	c := ts.oneshot()
	s, _, _ := ts.open(c)
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	ok(t, c, proto.PaneSplit{Pane: tn.Pane, Direction: proto.SplitRight})
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	ts.display(s)
	ok(t, c, proto.TabSelect{Session: s, Index: 1})
	ok(t, c, proto.TabSelect{Session: s, Index: 2})
	ts.sync(c)

	var mu sync.Mutex
	var atClose []persist.State
	for _, f := range ts.panes.list() {
		f.mu.Lock()
		f.onClose = func(*fakePane) {
			st, err := persist.Load(filepath.Join(ts.state, persist.FileName))
			if err != nil {
				t.Errorf("state.json at a pane's close: %v", err)
			}
			mu.Lock()
			atClose = append(atClose, st)
			mu.Unlock()
		}
		f.mu.Unlock()
	}
	ts.s.Shutdown()
	mu.Lock()
	defer mu.Unlock()
	if len(atClose) != 3 {
		t.Fatalf("%d panes were closed, want 3", len(atClose))
	}
	for _, st := range atClose {
		if len(st.Sessions) != 1 || len(st.Sessions[0].Tabs) != 2 || st.Sessions[0].Current != tn.Tab {
			t.Fatalf("when a pane was closed state.json held %+v; the save comes first", st.Sessions)
		}
	}
	if err := <-ts.served; err != nil {
		t.Fatalf("Serve returned %v after Shutdown", err)
	}
	ts.served <- nil // for the cleanup
	a.WaitClosed()
}

func TestExitsDuringShutdownDoNotChangeTheSavedState(t *testing.T) {
	ts := newTestServer(t, func(o *Options) { o.SaveDelay = 10 * time.Millisecond })
	c := ts.oneshot()
	s, _, p := ts.open(c)
	var sp proto.PaneSplitResult
	ok(t, c, proto.PaneSplit{Pane: p, Direction: proto.SplitDown}).Msg.Decode(&sp)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	ts.sync(c)
	for _, f := range ts.panes.list() {
		f.mu.Lock()
		// The program ends on its own while the server stops: a normal exit
		// and one by a signal the server did not send.
		code, sig := 0, ""
		if f.o.ID == sp.Pane {
			sig = "SIGSEGV"
		}
		f.onClose = func(f *fakePane) { f.exit(code, sig) }
		f.mu.Unlock()
	}
	ts.s.Shutdown()
	path := filepath.Join(ts.state, persist.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("state.json changed after the stop:\n%s\n%s", before, after)
	}
	st, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Sessions) != 1 || len(st.Sessions[0].Tabs) != 1 || len(st.Sessions[0].Tabs[0].Panes) != 2 {
		t.Fatalf("the saved layout lost panes that exited during the stop: %+v", st.Sessions)
	}
	<-ts.served
	ts.served <- nil
}

func TestShutdownIsIdempotentAndStopsAccepting(t *testing.T) {
	ts := newTestServer(t)
	a := ts.interactive()
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ts.s.Shutdown()
		}()
	}
	wg.Wait()
	a.WaitClosed()
	if err := <-ts.served; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	ts.served <- nil
	if nc, err := net.DialTimeout("unix", ts.socket, time.Second); err == nil {
		nc.Close()
		t.Fatal("the server accepts after Shutdown")
	}
	testkit.Eventually(t, 5*time.Second, "nothing of the server runs", func() bool {
		select {
		case <-ts.s.quit:
			return true
		default:
			return false
		}
	})
}
