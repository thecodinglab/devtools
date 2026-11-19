package fleet

import (
	"strconv"
	"testing"
	"time"

	"devtools/internal/hub"
	"devtools/internal/ui"
)

// stoppedQueue is a hub queue whose goroutine does not run yet: what is put
// stays in q, where the test can look at it.
func stoppedQueue(caughtUp func(string)) *hubQueue {
	return &hubQueue{
		out:      make(chan hub.Event),
		caughtUp: caughtUp,
		weight:   map[string]int{},
		held:     map[string]bool{},
		latest:   map[hubKey]*hubEntry{},
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

func row(path, label string) hub.Item {
	return hub.Item{Kind: hub.KindWorktree, Path: path, Label: label}
}

func items(m string, rows ...hub.Item) hub.Event {
	return hub.Event{Machine: m, Kind: hub.Items, Items: rows}
}

// labels is the labels of the rows of ev, in order.
func labels(ev hub.Event) []string {
	var out []string
	for _, it := range ev.Items {
		out = append(out, it.Label)
	}
	return out
}

func kinds(p *hubQueue) []hub.EventKind {
	var out []hub.EventKind
	for _, e := range p.q {
		out = append(out, e.ev.Kind)
	}
	return out
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestARowReportedAgainWaitsOnceInItsPlace(t *testing.T) {
	p := stoppedQueue(nil)
	p.put(hub.Event{Machine: "M", Kind: hub.Online})
	for i := range 10000 {
		p.put(items("M", row("/a", "a"+strconv.Itoa(i))))
	}
	p.put(items("M", row("/b", "b"), row("/a", "a-last"), row("/b", "b-last")))
	if got := kinds(p); !equal(got, []hub.EventKind{hub.Online, hub.Items}) {
		t.Fatalf("waiting %v, want Online and one Items", got)
	}
	if got := labels(p.q[1].ev); !equal(got, []string{"a-last", "b-last"}) {
		t.Fatalf("rows %q, want the newest of each path, a first", got)
	}
	if w := p.waiting("M"); w != 1+3 {
		t.Fatalf("%d waits for M, want 4", w)
	}
}

func TestRowsOfTwoScansDoNotMix(t *testing.T) {
	p := stoppedQueue(nil)
	p.put(items("M", row("/a", "a1")))
	p.put(hub.Event{Machine: "M", Kind: hub.Complete})
	p.put(items("M", row("/a", "a2")))
	p.put(hub.Event{Machine: "M", Kind: hub.Failed})
	p.put(items("M", row("/b", "b")))
	p.put(hub.Event{Machine: "M", Kind: hub.Online})
	p.put(items("M", row("/a", "a3")))
	p.put(items("K", row("/a", "k")))
	want := []hub.EventKind{hub.Items, hub.Complete, hub.Items, hub.Failed, hub.Online, hub.Items, hub.Items}
	if got := kinds(p); !equal(got, want) {
		t.Fatalf("waiting %v, want %v", got, want)
	}
	for i, want := range [][]string{{"a1"}, nil, {"a2", "b"}, nil, nil, {"a3"}, {"k"}} {
		if got := labels(p.q[i].ev); !equal(got, want) {
			t.Fatalf("event %d has rows %q, want %q", i, got, want)
		}
	}
}

func TestTheNewestListAndGitStateTakeThePlaceOfTheWaitingOne(t *testing.T) {
	p := stoppedQueue(nil)
	p.put(hub.Event{Machine: "M", Kind: hub.Sessions, Items: []hub.Item{{Label: "old"}, {Label: "old2"}}})
	p.put(hub.Event{Machine: "M", Kind: hub.Git, Path: "/a", Git: ui.Git{Changes: 1}})
	p.put(hub.Event{Machine: "M", Kind: hub.Git, Path: "/b", Git: ui.Git{Changes: 7}})
	p.put(hub.Event{Machine: "M", Kind: hub.Online})
	p.put(hub.Event{Machine: "K", Kind: hub.Sessions, Items: []hub.Item{{Label: "k"}}})
	p.put(hub.Event{Machine: "M", Kind: hub.Sessions, Items: []hub.Item{{Label: "new"}}})
	p.put(hub.Event{Machine: "M", Kind: hub.Git, Path: "/a", Git: ui.Git{Changes: 2}})
	want := []hub.EventKind{hub.Sessions, hub.Git, hub.Git, hub.Online, hub.Sessions}
	if got := kinds(p); !equal(got, want) {
		t.Fatalf("waiting %v, want %v", got, want)
	}
	if got := labels(p.q[0].ev); !equal(got, []string{"new"}) {
		t.Fatalf("M's sessions %q, want the newest list", got)
	}
	if g := p.q[1].ev; g.Path != "/a" || g.Git.Changes != 2 {
		t.Fatalf("git of /a %+v, want the newest", g)
	}
	if g := p.q[2].ev; g.Path != "/b" || g.Git.Changes != 7 {
		t.Fatalf("git of /b %+v", g)
	}
	if w := p.waiting("M"); w != 2+1+1+1 {
		t.Fatalf("%d waits for M, want 5", w)
	}
}

func TestAMachineThatFloodsTheHubIsHeldBackAndTheOthersGoOn(t *testing.T) {
	caught := make(chan string, 4)
	p := stoppedQueue(func(m string) { caught <- m })
	defer p.close()
	p.put(hub.Event{Machine: "M", Kind: hub.Online})
	p.put(hub.Event{Machine: "K", Kind: hub.Online})
	for i := range 100000 {
		p.put(hub.Event{Machine: "M", Kind: hub.Note, Path: "/x/" + strconv.Itoa(i), Err: hub.Problem{Reason: noteCatalog}})
	}
	if w := p.waiting("M"); w > hubBacklog {
		t.Fatalf("%d waits for M, want at most %d", w, hubBacklog)
	}
	if len(p.q) > hubBacklog+1 {
		t.Fatalf("%d events wait, want at most %d", len(p.q), hubBacklog+1)
	}
	p.put(hub.Event{Machine: "K", Kind: hub.Sessions})
	if w := p.waiting("K"); w != 2 {
		t.Fatalf("%d waits for K, want its two events: K is not held back", w)
	}
	go p.run()
	notes, last := 0, hub.Event{}
	deadline := time.After(10 * time.Second)
	for notes < hubBacklog-1 || last.Machine != "K" {
		select {
		case ev := <-p.out:
			if ev.Kind == hub.Note {
				notes++
			}
			last = ev
		case <-deadline:
			t.Fatalf("the queue gave %d notes, and then nothing", notes)
		}
	}
	select {
	case m := <-caught:
		if m != "M" {
			t.Fatalf("caught up %q, want M", m)
		}
	case <-time.After(time.Second):
		t.Fatal("the queue did not say that M caught up")
	}
	if notes >= 100000 {
		t.Fatalf("%d notes arrived; the flood was not bounded", notes)
	}
	p.put(hub.Event{Machine: "M", Kind: hub.Sessions})
	if w := p.waiting("M"); w != 0 {
		t.Fatalf("%d waits for M before it is resumed, want 0", w)
	}
	p.resume("M")
	p.put(hub.Event{Machine: "M", Kind: hub.Sessions})
	select {
	case ev := <-p.out:
		if ev.Machine != "M" || ev.Kind != hub.Sessions {
			t.Fatalf("after resuming %+v, want M's sessions", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing of M after it was resumed")
	}
	select {
	case m := <-caught:
		t.Fatalf("caught up %q again", m)
	default:
	}
}
