package remote

import "time"

// clock is the time source of the dialer, the link and the bridge. The real
// one is the wall clock; the tests replace it with a fake one that moves
// only when the test says so, so the 30 s attempt limit, the 5 s ping, the
// 10 s silence and the 60 s of the bridge are tested without waiting.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Intervals of 3.5.2, 4.2.8 and 4.4.
const (
	attemptLimit  = 30 * time.Second // a dial that is not online by then is killed
	checkLimit    = 5 * time.Second  // an "ssh -O check" that has not answered by then is killed
	pingEvery     = 5 * time.Second  // a ping when nothing was sent for this long
	silenceLimit  = 10 * time.Second // the link is lost when nothing arrived for this long
	bridgeSilence = 60 * time.Second // the bridge exits when stdin was silent this long
)

// Bounds of SEC12.
const (
	sendQueue  = 256      // messages waiting to be written on one link
	maxBanner  = 64 << 10 // bytes of other output allowed before the ready line
	keptStderr = 8 << 10  // the tail of ssh's stderr kept for Classify
)
