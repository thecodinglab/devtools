package e2e

import (
	"fmt"
	"testing"
	"time"
)

// Unit is the unit of a bound.
type Unit string

const (
	Millis    Unit = "ms"
	MiB       Unit = "MiB"
	MiBPerSec Unit = "MiB/s"
	Percent   Unit = "% of one core"
	Runs      Unit = "runs"
	Days      Unit = "working days"
)

// Bound is one number of a threshold.
type Bound struct {
	What  string // which value of the row: "held", "cold", "p95", "full", ...
	Value float64
	Unit  Unit
	Floor bool   // the measured value must be at least Value; otherwise at most
	Note  string // what is added on top on real machines, when the row says so
}

// Duration returns a bound in milliseconds as a duration.
func (b Bound) Duration() time.Duration {
	if b.Unit != Millis {
		panic(fmt.Sprintf("e2e: bound %q is in %s, not a time", b.What, b.Unit))
	}
	return time.Duration(b.Value * float64(time.Millisecond))
}

// Bytes returns a bound in MiB as a number of bytes.
func (b Bound) Bytes() int64 {
	if b.Unit != MiB {
		panic(fmt.Sprintf("e2e: bound %q is in %s, not a size", b.What, b.Unit))
	}
	return int64(b.Value * (1 << 20))
}

// Met reports whether a measured value keeps the bound.
func (b Bound) Met(got float64) bool {
	if b.Floor {
		return got >= b.Value
	}
	return got <= b.Value
}

// Threshold is one row of 10.4.
type Threshold struct {
	ID        string
	Criterion string
	Loopback  []Bound // two servers, one host: what the suite asserts
	LAN       []Bound // real machines
	Tests     []string
	Hard      []string // the What of the bounds that are hard at the gate; the others are a no-go only beyond NoGoFactor
}

// NoGoFactor: a threshold that is not hard is a no-go at the gate when it
// is missed by more than this factor; a smaller miss is recorded with its
// measured value and decided by the user (10.4).
const NoGoFactor = 2

// SamplesP95 is the number of runs a p95 is taken over, unless a row says
// otherwise (10.4).
const SamplesP95 = 50

func ms(what string, v float64) Bound         { return Bound{What: what, Value: v, Unit: Millis} }
func mib(what string, v float64) Bound        { return Bound{What: what, Value: v, Unit: MiB} }
func note(b Bound, n string) Bound            { b.Note = n; return b }
func floor(b Bound) Bound                     { b.Floor = true; return b }
func of(what string, v float64, u Unit) Bound { return Bound{What: what, Value: v, Unit: u} }

// Thresholds are T1 to T10 of 10.4, all ESTIMATE, in the order of the
// table. A threshold is moved once at most, before the gate, with the
// reason written into the verification record; it is moved here and
// nowhere else.
//
// T4 is calibrated by E3 (variant B added p50 4.4 ms and p95 8.5 ms on the
// desktop: met, the values stay). provisional E5: the cold values of T2 and
// T3 and the remote part of T4 depend on the cold ssh connect time and the
// round trip of `ssh -T <B> cat` on the LAN, which E5 records and which is
// pending [GO]; they are the values of 10.4 until then.
var Thresholds = []Threshold{
	{
		ID: "T1", Criterion: "hub key to first local row",
		Loopback: []Bound{ms("first local row", 100)},
		LAN:      []Bound{ms("first local row", 100)},
		Tests:    []string{"TestHubFirstLocalRow"},
		Hard:     []string{"first local row"},
	},
	{
		ID: "T2", Criterion: "hub key to first row of another machine",
		Loopback: []Bound{ms("held", 150), ms("cold", 500)},
		LAN:      []Bound{ms("held", 250), ms("cold", 2000)},
		Tests:    []string{"TestRemoteDiscoveryHeldAndCold"},
	},
	{
		ID: "T2b", Criterion: "local rows with one machine unreachable",
		Loopback: []Bound{ms("local rows", 100)},
		LAN:      []Bound{ms("local rows", 100)},
		Tests:    []string{"TestHubUsableWithDeadMachine"},
		Hard:     []string{"local rows"},
	},
	{
		ID: "T3", Criterion: "selection to full frame of the target",
		Loopback: []Bound{ms("local", 100), ms("held", 200), ms("cold", 600)},
		LAN:      []Bound{ms("local", 100), ms("held", 300), ms("cold", 2500)},
		Tests:    []string{"TestSwitchLocalHeldCold"},
		Hard:     []string{"local"},
	},
	{
		ID: "T3b", Criterion: "a failed switch reports and leaves the old view",
		Loopback: []Bound{ms("report", 10000)},
		LAN:      []Bound{ms("report", 10000)},
		Tests:    []string{"TestSwitchFailureKeepsView"},
	},
	{
		ID: "T4", Criterion: "key to echo, added over the same measurement on a bare PTY, one key at a time on an idle pane",
		Loopback: []Bound{ms("local p50", 6), ms("local p95", 15)},
		LAN:      []Bound{note(ms("remote p95", 15), "plus the round trip of ssh -T <B> cat from E5")},
		Tests:    []string{"TestInputLatencyLocal", "TestInputLatencyRemote"},
		Hard:     []string{"local p50", "local p95"},
	},
	{
		ID: "T5", Criterion: "flood: 100 MiB of text in a pane",
		Loopback: []Bound{
			floor(of("throughput", 30, MiBPerSec)),
			ms("key during the flood", 250),
			mib("server growth", 50),
		},
		LAN:   []Bound{ms("local key to echo during a flood on B", 50)},
		Tests: []string{"TestFloodThroughputAndResponsiveness"},
	},
	{
		ID: "T6", Criterion: "idle cost: server and one client, 10 idle panes, 60 s",
		Loopback: []Bound{of("cpu", 1, Percent)},
		LAN:      []Bound{of("cpu", 1, Percent)},
		Tests:    []string{"TestIdleCPU"},
	},
	{
		ID: "T7", Criterion: "link cut to stale banner; link back to live view (held connection)",
		Loopback: []Bound{ms("stale", 10000), ms("back", 3000)},
		LAN:      []Bound{ms("stale", 10000), ms("back", 5000)},
		Tests:    []string{"TestLinkCutShowsStaleThenResumes"},
	},
	{
		ID: "T8", Criterion: "agent turns blocked to bell at the outer terminal",
		Loopback: []Bound{ms("bell", 2000)},
		LAN:      []Bound{note(ms("bell", 2000), "plus the link")},
		Tests:    []string{"TestAgentBlockedRingsBellOffScreen"},
	},
	{
		ID: "T9", Criterion: "stability",
		Loopback: []Bound{floor(of("suite runs without a failure", 3, Runs))},
		LAN:      []Bound{floor(of("real use without a crash or a stuck client", 3, Days))},
		Tests:    []string{"G3", "G6"},
	},
	{
		ID: "T10", Criterion: "memory of the server, resident set size",
		Loopback: []Bound{
			// restated from E10 item 13, contract change 2
			note(mib("full", 520), "20 panes of 200 x 50, each with a full history of 10,000 lines; more than 900 MiB is a no-go, 600 MiB a recorded miss"),
			note(mib("idle", 60), "10 idle empty panes"),
		},
		LAN: []Bound{
			note(mib("full", 520), "read once per machine with ps -o rss= after three days of real use, with the pane count"),
			note(mib("idle", 60), "the same"),
		},
		Tests: []string{"TestServerMemoryWithFullHistories"},
	},
}

// T10NoGo and T10RecordedMiss are the two limits of the gate for T10 with
// twenty full panes (10.4).
const (
	T10NoGo         = 900
	T10RecordedMiss = 600
)

// Row returns the threshold id; it panics for an id that 10.4 does not
// have, so a test cannot measure against a number nobody wrote down.
func Row(id string) Threshold {
	for _, th := range Thresholds {
		if th.ID == id {
			return th
		}
	}
	panic("e2e: no threshold " + id)
}

// Limit returns the loopback bound what of threshold id.
func Limit(id, what string) Bound {
	for _, b := range Row(id).Loopback {
		if b.What == what {
			return b
		}
	}
	panic(fmt.Sprintf("e2e: threshold %s has no bound %q", id, what))
}

// Measure logs a measured value beside its loopback bound (a run with -v
// prints every measured value) and fails the test when the bound is not
// kept.
func Measure(t *testing.T, id, what string, got float64) {
	t.Helper()
	b := Limit(id, what)
	rel := "at most"
	if b.Floor {
		rel = "at least"
	}
	t.Logf("%s %s: measured %.3g %s, %s %.3g %s", id, what, got, b.Unit, rel, b.Value, b.Unit)
	if !b.Met(got) {
		t.Errorf("%s %s: %.3g %s misses the threshold (%s %.3g %s)", id, what, got, b.Unit, rel, b.Value, b.Unit)
	}
}

// MeasureDuration is Measure for a time.
func MeasureDuration(t *testing.T, id, what string, got time.Duration) {
	t.Helper()
	Measure(t, id, what, float64(got)/float64(time.Millisecond))
}
