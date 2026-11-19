package testkit

import (
	"testing"
	"time"
)

// PollInterval is how often Eventually asks its condition again.
const PollInterval = 5 * time.Millisecond

// Eventually waits until cond returns true and fails the test when it has
// not after timeout. what names the awaited state in the failure ("the row
// 'ready' appears"). Tests never sleep for a fixed time (10.1.3); they wait
// for an observable state with this.
//
// cond is called from the calling goroutine only, first at once and then
// every PollInterval.
func Eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	if !poll(timeout, cond) {
		t.Fatalf("testkit: timed out after %v waiting for %s", timeout, what)
	}
}

// poll is the loop of Eventually: true as soon as cond is true, false when
// the timeout passed first. cond is always asked once more at the deadline.
func poll(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		left := time.Until(deadline)
		if left <= 0 {
			return false
		}
		time.Sleep(min(PollInterval, left))
	}
}
