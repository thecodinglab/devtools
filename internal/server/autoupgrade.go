package server

import (
	"errors"
	"strings"
	"time"

	"devtools/internal/handover"
	"devtools/internal/platform"
	"devtools/internal/proto"
)

// The automatic upgrade (PLAN-live-upgrade.md 6.6). The service starts the
// server through the profile's symlink (Options.Program), which a
// home-manager switch moves to the new build. The watch of the installed
// build (update.go) looks behind it every UpdateInterval and hands what it
// found to this policy: a file other than the running image that stays
// the same for UpgradeSettle is a candidate; at the first moment no window
// has had input for UpgradeIdle (at the latest an hour later) the server
// upgrades itself live, at most once per UpgradeEvery, never to a target
// whose last attempt failed, and never to a path the target policy does
// not allow (by default only /nix/store). The candidate is bound to the
// identity of the file the watch found, and checked again on the loop
// right before the model is held, so a build that was replaced meanwhile
// is never the one exec'd.

// maxIdleWait is how long a candidate waits for an idle moment.
const maxIdleWait = time.Hour

// candidate is a build behind Program, with the identity it had when it
// was found.
type candidate struct {
	program string
	id      platform.Identity
}

// still reports whether Program resolves to the candidate's file now.
func (c *candidate) still() bool {
	now, err := platform.FileIdentity(c.program)
	return err == nil && sameCandidate(now, c.id)
}

func sameCandidate(a, b platform.Identity) bool {
	return a.Path == b.Path && a.Same(b) && a.Mtime == b.Mtime && a.Size == b.Size
}

// storeOnly is the default target policy: a build in the Nix store.
func storeOnly(path string) bool { return strings.HasPrefix(path, "/nix/store/") }

// autoPolicy is the decision of the watch, pure, for the tests: what was
// seen and when, and whether to go now.
type autoPolicy struct {
	settle, idle, every time.Duration
	targetOK            func(string) bool

	cand    *candidate
	since   time.Time       // the candidate was first seen
	lastTry time.Time       // the last automatic attempt
	refused map[string]bool // targets the preflight or an attempt refused, by path
}

// step is one look at the installed build. found is what the watch found
// behind Program now (installedCandidate): nil when it is the running
// image or cannot be read. idleFor is how long no window had input (and
// no paste is open); blocked whether the journal blocks the path. It
// returns the candidate to upgrade to now, or nil.
func (a *autoPolicy) step(now time.Time, found *candidate, idleFor time.Duration, blocked func(string) bool) *candidate {
	if found == nil {
		a.cand = nil
		return nil
	}
	id := found.id
	if a.cand == nil || !sameCandidate(a.cand.id, id) {
		a.cand, a.since = found, now
		return nil
	}
	if now.Sub(a.since) < a.settle {
		return nil
	}
	if !a.targetOK(id.Path) || a.refused[id.Path] || blocked(id.Path) {
		return nil
	}
	if !a.lastTry.IsZero() && now.Sub(a.lastTry) < a.every {
		return nil
	}
	if idleFor < a.idle && now.Sub(a.since) < a.settle+maxIdleWait {
		return nil
	}
	a.lastTry = now
	return a.cand
}

// autoUpgrade is the automatic upgrade of a server, driven by the watch of
// the installed build.
type autoUpgrade struct {
	s      *Server
	policy *autoPolicy
}

// newAutoUpgrade is the automatic upgrade of s; nil when it is off or the
// server cannot upgrade itself.
func (s *Server) newAutoUpgrade() *autoUpgrade {
	if !s.o.AutoUpgrade || !s.canUpgrade() {
		return nil
	}
	targetOK := s.o.UpgradeTargetOK
	if targetOK == nil {
		targetOK = storeOnly
	}
	a := &autoPolicy{settle: s.o.UpgradeSettle, idle: s.o.UpgradeIdle, every: s.o.UpgradeEvery, targetOK: targetOK, refused: map[string]bool{}}
	// The interval runs across the exec: an image an automatic upgrade made
	// waits it from that attempt (the journal), not from its own start.
	a.lastTry = lastAutoAttempt(s.o.StateDir)
	return &autoUpgrade{s: s, policy: a}
}

// look takes one look of the watch: found is the installed build when it
// is another than the running one. An attempt it starts has ended when it
// returns (or the process runs the new build).
func (u *autoUpgrade) look(found *candidate) {
	s := u.s
	var idle time.Duration
	if found != nil {
		idle = s.idleFor()
	}
	blocked := func(path string) bool {
		return s.o.StateDir != "" && handover.LoadJournal(s.o.StateDir).Blocked(path)
	}
	c := u.policy.step(time.Now(), found, idle, blocked)
	if c == nil {
		return
	}
	s.log.Info("upgrade: a new build is installed; upgrading", "target", c.id.Path)
	started := time.Now().UnixMilli()
	done, err := s.beginUpgradeWait(upgradeReq{token: c})
	if err != nil {
		return
	}
	<-done
	if l := s.lastUpgrade(); l != nil && l.Error != "" && l.At >= started {
		u.policy.refused[c.id.Path] = true
	}
	if s.lastRefusal(c.id.Path) {
		u.policy.refused[c.id.Path] = true
	}
}

// lastAutoAttempt is when the journal of dir says the last automatic
// attempt was about to exec; zero when it says none.
func lastAutoAttempt(dir string) time.Time {
	if dir == "" {
		return time.Time{}
	}
	if ms := handover.LoadJournal(dir).LastAuto(); ms > 0 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

// idleFor is how long no interactive connection had input; 0 while a paste
// is open.
func (s *Server) idleFor() time.Duration {
	s.mu.Lock()
	for cn := range s.conns {
		if cn.pasteOpen.Load() {
			s.mu.Unlock()
			return 0
		}
	}
	s.mu.Unlock()
	var last time.Time
	if !s.onLoop(func(c *core) {
		for _, cn := range c.reg.list() {
			if cn.kind == proto.KindInteractive && cn.lastInput.After(last) {
				last = cn.lastInput
			}
		}
	}) {
		return 0
	}
	if last.IsZero() {
		return maxIdleWait
	}
	return time.Since(last)
}

// beginUpgradeWait starts an attempt of the automatic trigger and returns
// a channel closed when it ended.
func (s *Server) beginUpgradeWait(req upgradeReq) (<-chan struct{}, error) {
	if err := s.beginUpgrade(req); err != nil {
		return nil, err
	}
	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	return s.up.done, nil
}

// noteRefusal records that the preflight refused a target, for the
// automatic trigger.
func (s *Server) noteRefusal(path string, err error) {
	var ue *upgradeError
	if !errors.As(err, &ue) || ue.code != proto.CodeIncompatible {
		return
	}
	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	if s.up.refused == nil {
		s.up.refused = map[string]bool{}
	}
	s.up.refused[path] = true
}

func (s *Server) lastRefusal(path string) bool {
	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	return s.up.refused[path]
}
