package server

import (
	"errors"
	"testing"
	"time"

	"devtools/internal/handover"
	"devtools/internal/platform"
)

func TestTheAutomaticUpgradeWaitsSettlesAndIsIdle(t *testing.T) {
	self := platform.Identity{Path: "/nix/store/a-devtools/bin/devtools", Dev: 1, Ino: 1}
	b := platform.Identity{Path: "/nix/store/b-devtools/bin/devtools", Dev: 1, Ino: 2, Mtime: 5}
	c := platform.Identity{Path: "/nix/store/c-devtools/bin/devtools", Dev: 1, Ino: 3, Mtime: 6}
	policy := func() *autoPolicy {
		return &autoPolicy{settle: 30 * time.Second, idle: 3 * time.Second, every: 10 * time.Minute, targetOK: storeOnly, refused: map[string]bool{}}
	}
	never := func(string) bool { return false }
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	idle := time.Minute

	a := policy()
	if a.step(at(0), installedCandidate("p", self, nil, self), idle, never) != nil {
		t.Fatal("the running build is a candidate")
	}
	if a.step(at(10), installedCandidate("p", b, nil, self), idle, never) != nil || a.step(at(39), installedCandidate("p", b, nil, self), idle, never) != nil {
		t.Fatal("a build that has not settled for 30 s is taken")
	}
	got := a.step(at(40), installedCandidate("p", b, nil, self), idle, never)
	if got == nil || got.id.Path != b.Path {
		t.Fatalf("a settled build is not taken: %+v", got)
	}
	// At most one automatic attempt per ten minutes.
	if a.step(at(50), installedCandidate("p", b, nil, self), idle, never) != nil {
		t.Fatal("a second attempt within ten minutes")
	}

	// A -> B -> C while B settles: C starts its own debounce, B is never taken.
	a = policy()
	a.step(at(0), installedCandidate("p", b, nil, self), idle, never)
	a.step(at(20), installedCandidate("p", c, nil, self), idle, never)
	if got := a.step(at(35), installedCandidate("p", c, nil, self), idle, never); got != nil {
		t.Fatalf("C was taken before it settled: %+v", got)
	}
	if got := a.step(at(51), installedCandidate("p", c, nil, self), idle, never); got == nil || got.id.Path != c.Path {
		t.Fatalf("C after it settled: %+v", got)
	}
	// A -> B -> A: back to the running build, nothing to do.
	a = policy()
	a.step(at(0), installedCandidate("p", b, nil, self), idle, never)
	a.step(at(20), installedCandidate("p", self, nil, self), idle, never)
	if got := a.step(at(60), installedCandidate("p", self, nil, self), idle, never); got != nil || a.cand != nil {
		t.Fatalf("A -> B -> A took %+v", got)
	}
	// The same path rebuilt in place (another mtime) settles again.
	a = policy()
	a.step(at(0), installedCandidate("p", b, nil, self), idle, never)
	b2 := b
	b2.Mtime = 99
	if a.step(at(31), installedCandidate("p", b2, nil, self), idle, never) != nil {
		t.Fatal("a changed file was taken without settling")
	}
	// A profile in the middle of a switch (no file) forgets the candidate.
	a = policy()
	a.step(at(0), installedCandidate("p", b, nil, self), idle, never)
	a.step(at(10), installedCandidate("p", platform.Identity{}, errors.New("no such file"), self), idle, never)
	if a.cand != nil || a.step(at(40), installedCandidate("p", b, nil, self), idle, never) != nil {
		t.Fatal("a candidate survived a moment without a file")
	}
}

func TestTheAutomaticUpgradeRespectsIdleThePolicyAndTheJournal(t *testing.T) {
	self := platform.Identity{Path: "/nix/store/a/bin/devtools", Dev: 1, Ino: 1}
	b := platform.Identity{Path: "/nix/store/b/bin/devtools", Dev: 1, Ino: 2}
	local := platform.Identity{Path: "/home/u/bin/devtools", Dev: 1, Ino: 3}
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	never := func(string) bool { return false }
	a := &autoPolicy{settle: 30 * time.Second, idle: 3 * time.Second, every: 10 * time.Minute, targetOK: storeOnly, refused: map[string]bool{}}
	a.step(at(0), installedCandidate("p", b, nil, self), 0, never)
	if a.step(at(40), installedCandidate("p", b, nil, self), time.Second, never) != nil {
		t.Fatal("taken while a window had input a second ago")
	}
	if got := a.step(at(40+3600), installedCandidate("p", b, nil, self), time.Second, never); got == nil {
		t.Fatal("not taken after an hour of waiting for an idle moment")
	}
	// Only the Nix store, unless the policy says otherwise.
	a = &autoPolicy{settle: time.Second, idle: 0, every: time.Second, targetOK: storeOnly, refused: map[string]bool{}}
	a.step(at(0), installedCandidate("p", local, nil, self), time.Hour, never)
	if a.step(at(5), installedCandidate("p", local, nil, self), time.Hour, never) != nil {
		t.Fatal("a build outside the store was taken")
	}
	// A target the journal blocks or the preflight refused is not tried again.
	a = &autoPolicy{settle: time.Second, idle: 0, every: time.Second, targetOK: storeOnly, refused: map[string]bool{}}
	a.step(at(0), installedCandidate("p", b, nil, self), time.Hour, never)
	if a.step(at(5), installedCandidate("p", b, nil, self), time.Hour, func(p string) bool { return p == b.Path }) != nil {
		t.Fatal("a target the journal blocks was taken")
	}
	a.refused[b.Path] = true
	if a.step(at(10), installedCandidate("p", b, nil, self), time.Hour, never) != nil {
		t.Fatal("a refused target was taken")
	}
}

// The interval of the automatic upgrade runs across the exec: after an
// automatic A -> B, a profile rolled back to A within ten minutes is not
// taken by the new image before the ten minutes are over.
func TestTheIntervalOfTheAutomaticUpgradeSurvivesTheExec(t *testing.T) {
	dir := t.TempDir()
	if got := lastAutoAttempt(dir); !got.IsZero() {
		t.Fatalf("an empty journal has an attempt at %v", got)
	}
	upgraded := time.Now().Add(-2 * time.Minute)
	if err := handover.Update(dir, func(j *handover.Journal) {
		j.Put(handover.Attempt{ID: "01", Target: "/nix/store/b/bin/devtools", Result: handover.OK, Auto: true, Started: upgraded.UnixMilli()})
		j.Put(handover.Attempt{ID: "02", Target: "/nix/store/c/bin/devtools", Result: handover.Failed, Started: time.Now().UnixMilli()}) // manual
	}); err != nil {
		t.Fatal(err)
	}
	got := lastAutoAttempt(dir)
	if got.UnixMilli() != upgraded.UnixMilli() {
		t.Fatalf("the last automatic attempt %v, want %v", got, upgraded)
	}
	self := platform.Identity{Path: "/nix/store/b/bin/devtools", Dev: 1, Ino: 2}
	a := platform.Identity{Path: "/nix/store/a/bin/devtools", Dev: 1, Ino: 1}
	never := func(string) bool { return false }
	p := &autoPolicy{settle: 30 * time.Second, idle: 3 * time.Second, every: 10 * time.Minute, targetOK: storeOnly, refused: map[string]bool{}, lastTry: got}
	now := time.Now()
	p.step(now, installedCandidate("p", a, nil, self), time.Hour, never)
	if c := p.step(now.Add(time.Minute), installedCandidate("p", a, nil, self), time.Hour, never); c != nil {
		t.Fatal("the rollback to A was taken three minutes after the upgrade to B")
	}
	if c := p.step(upgraded.Add(10*time.Minute+time.Second), installedCandidate("p", a, nil, self), time.Hour, never); c == nil {
		t.Fatal("the rollback to A was not taken after the interval")
	}
}
