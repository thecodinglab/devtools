package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// patchedClient takes every activation request the way a herdr client
// with the patch does, and returns the ones it took.
func patchedClient(t *testing.T) func() []activationRequest {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", dir)
	client := filepath.Join(dir, "herdr", "client")
	var mu sync.Mutex
	var took []activationRequest
	done := make(chan struct{})
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
			ack, _ := json.Marshal(map[string]string{"nonce": request.Nonce})
			os.WriteFile(filepath.Join(client, "activate-ack.json"), ack, 0o600)
		}
	}()
	t.Cleanup(func() { close(done) })
	return func() []activationRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]activationRequest(nil), took...)
	}
}

func TestShowWorkspaceIsTakenByAPatchedClient(t *testing.T) {
	took := patchedClient(t)
	shown, err := ShowWorkspace("ad61d571470a4ff1010dece8eaf53b5c", "w3")
	if err != nil || !shown {
		t.Fatalf("shown = %v, err = %v", shown, err)
	}
	got := took()
	if len(got) != 1 || got[0].Machine != "ad61d571470a4ff1010dece8eaf53b5c" || got[0].Workspace != "w3" || got[0].Nonce == "" {
		t.Fatalf("requests = %+v", got)
	}
}

func TestShowWorkspaceGivesUpWithoutAClient(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	saved := ActivationWait
	ActivationWait = 100 * time.Millisecond
	t.Cleanup(func() { ActivationWait = saved })
	start := time.Now()
	shown, err := ShowWorkspace("", "w1")
	if err != nil || shown {
		t.Fatalf("shown = %v, err = %v", shown, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("waited %s", elapsed)
	}
}
