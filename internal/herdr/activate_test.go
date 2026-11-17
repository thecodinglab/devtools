package herdr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// patchedClient takes every activation request the way a herdr client
// with the patch does: it says it received it, waits for its machine to
// connect, here connect long, then says it is done; never when connect is
// negative. It returns the requests it took.
func patchedClient(t *testing.T, connect time.Duration) func() []activationRequest {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", dir)
	client := filepath.Join(dir, "herdr", "client")
	var mu sync.Mutex
	var took []activationRequest
	done := make(chan struct{})
	ack := func(nonce, state string) {
		data, _ := json.Marshal(activationAck{Nonce: nonce, State: state})
		os.WriteFile(filepath.Join(client, "activate-ack.json"), data, 0o600)
	}
	go func() {
		seen := ""
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			data, err := os.ReadFile(filepath.Join(client, "activate-request.json"))
			var request activationRequest
			if err != nil || json.Unmarshal(data, &request) != nil || request.Nonce == seen {
				continue
			}
			seen = request.Nonce
			mu.Lock()
			took = append(took, request)
			mu.Unlock()
			ack(request.Nonce, "received")
			if connect >= 0 {
				time.Sleep(connect)
				ack(request.Nonce, "done")
			}
		}
	}()
	t.Cleanup(func() { close(done) })
	return func() []activationRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]activationRequest(nil), took...)
	}
}

func shortWaits(t *testing.T, receive, done time.Duration) {
	t.Helper()
	savedReceive, savedDone := ReceiveWait, DoneWait
	ReceiveWait, DoneWait = receive, done
	t.Cleanup(func() { ReceiveWait, DoneWait = savedReceive, savedDone })
}

func TestShowWorkspaceIsTakenByAPatchedClient(t *testing.T) {
	took := patchedClient(t, 0)
	if err := ShowWorkspace("ad61d571470a4ff1010dece8eaf53b5c", "w3"); err != nil {
		t.Fatal(err)
	}
	got := took()
	if len(got) != 1 || got[0].Machine != "ad61d571470a4ff1010dece8eaf53b5c" || got[0].Workspace != "w3" || got[0].Nonce == "" {
		t.Fatalf("requests = %+v", got)
	}
}

func TestShowWorkspaceWaitsForAMachineToConnect(t *testing.T) {
	shortWaits(t, 200*time.Millisecond, 2*time.Second)
	patchedClient(t, 500*time.Millisecond)
	if err := ShowWorkspace("ad61d571470a4ff1010dece8eaf53b5c", "w3"); err != nil {
		t.Fatal(err)
	}
}

func TestShowWorkspaceGivesUpOnAMachineThatNeverConnects(t *testing.T) {
	shortWaits(t, 200*time.Millisecond, 300*time.Millisecond)
	patchedClient(t, -1)
	err := ShowWorkspace("ad61d571470a4ff1010dece8eaf53b5c", "w3")
	if err == nil || errors.Is(err, ErrNoClient) {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestShowWorkspaceWithoutAClient(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	shortWaits(t, 100*time.Millisecond, time.Second)
	start := time.Now()
	if err := ShowWorkspace("", "w1"); !errors.Is(err, ErrNoClient) {
		t.Fatalf("err = %v, want ErrNoClient", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %s", elapsed)
	}
}
