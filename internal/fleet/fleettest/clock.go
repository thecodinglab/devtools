// Package fleettest is the test kit of internal/fleet (P2-03): a fake
// clock, a fake remote.Dialer whose links are scripted per machine, and
// Rig, which runs a fleet on both. Tests never start ssh and never wait
// for real time to pass, except for the short waits that see a goroutine
// act.
//
// It is a test-only package (import rule 8): no non-test package imports
// it.
package fleettest

import (
	"slices"
	"sync"
	"testing"
	"time"

	"devtools/internal/fleet"
)

// Start is the time a Clock starts at: Tue 17 Nov 2026, 14:32 (the date of
// the mockups of 5a.9).
var Start = time.Date(2026, time.November, 17, 14, 32, 0, 0, time.UTC)

// Clock is a fake fleet.Clock: time moves only by Advance.
type Clock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*clockTimer
}

type clockTimer struct {
	c   *Clock
	at  time.Time
	seq int
	fn  func()
}

// NewClock returns a clock at Start.
func NewClock() *Clock { return &Clock{now: Start} }

// Now is the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc calls fn once the clock was advanced by d. The call happens in
// the goroutine that advances the clock.
func (c *Clock) AfterFunc(d time.Duration, fn func()) fleet.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &clockTimer{c: c, at: c.now.Add(max(d, 0)), seq: c.seq, fn: fn}
	c.timers = append(c.timers, t)
	return t
}

// Stop removes the timer; false when it already ran or was stopped.
func (t *clockTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	for i, o := range t.c.timers {
		if o == t {
			t.c.timers = append(t.c.timers[:i], t.c.timers[i+1:]...)
			return true
		}
	}
	return false
}

// Advance moves the clock by d and runs every timer that falls due, in the
// order of their times, each with the clock at its time.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for {
		i := c.due(target)
		if i < 0 {
			break
		}
		t := c.timers[i]
		c.timers = append(c.timers[:i], c.timers[i+1:]...)
		if t.at.After(c.now) {
			c.now = t.at
		}
		c.mu.Unlock()
		t.fn()
		c.mu.Lock()
	}
	if target.After(c.now) {
		c.now = target
	}
	c.mu.Unlock()
}

// due is the index of the earliest timer due by target, -1 for none.
func (c *Clock) due(target time.Time) int {
	best := -1
	for i, t := range c.timers {
		if t.at.After(target) {
			continue
		}
		if best < 0 || t.at.Before(c.timers[best].at) || (t.at.Equal(c.timers[best].at) && t.seq < c.timers[best].seq) {
			best = i
		}
	}
	return best
}

// Pending is how long from now each pending timer has to go, shortest
// first.
func (c *Clock) Pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, 0, len(c.timers))
	for _, t := range c.timers {
		out = append(out, t.at.Sub(c.now))
	}
	slices.Sort(out)
	return out
}

// WaitTimer waits, for at most 1 s of real time, until a timer is pending
// that falls due exactly d from now, and fails the test otherwise. It is
// how a test knows that the fleet has scheduled what it is going to
// advance to.
func (c *Clock) WaitTimer(t testing.TB, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if slices.Contains(c.Pending(), d) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fleettest: no timer due in %v; pending %v", d, c.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}
