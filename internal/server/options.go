// Package server is the devtools server: the state loop, connections,
// handshake, requests, views, sizes, input, events and the extension seam
// (3.2.3, 5.3.6).
//
// options.go and extend.go are the contract (P1-00a).
package server

import (
	"context"
	"log/slog"
	"time"

	"devtools/internal/handover"
	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/vt"
)

// Options configure a server. Every interval is an option so that tests can
// shorten it.
type Options struct {
	StateDir string   // state.json lives here
	Shell    []string // program and arguments of a pane; nil: {$SHELL}, as a login shell
	Env      []string // the server's own environment, source for pane environments
	Version  string
	Host     string // short host name for welcome.host and XTVERSION
	History  int    // 10,000

	FrameInterval time.Duration // 16 ms
	CosmeticDelay time.Duration // 100 ms
	SyncHold      time.Duration // 150 ms
	SaveDelay     time.Duration // 5 s
	StallTimeout  time.Duration // 30 s
	PingInterval  time.Duration // 5 s; receive timeout is twice this
	HelloTimeout  time.Duration // 4 s
	KillGrace     time.Duration // 250 ms
	ProcInterval  time.Duration // 1 s
	FgInterval    time.Duration // 100 ms: foreground group of the focused pane of a displayed tab (3.2.1)

	// The running build and the installed one (update.go, upgrade.go).
	// Program is the path the server was started as (argv[0], made
	// absolute): the devtools of the profile, a symlink that follows the
	// active generation. Self is the file of the running image as the
	// kernel names it (platform.SelfExecutable), never the symlink it was
	// started by; a live upgrade starts the new image with its own. Without
	// Program or Self nothing is watched.
	Program string
	Self    platform.Identity
	// The watch of the installed build (update.go) resolves Program once
	// per UpdateInterval. InstalledVersion asks the build at a path for its
	// version; nil runs "<path> version".
	UpdateInterval   time.Duration // 10 s
	InstalledVersion func(ctx context.Context, path string) (string, error)

	NewPane func(pane.Options) Pane // nil: pane.Start; tests of the core pass a fake
	Log     *slog.Logger

	// The live upgrade (upgrade.go, internal/handover). Without Exec or
	// Program the server cannot upgrade itself and does not announce
	// CapUpgrade.
	Exec func(*Handover) error
	// Check runs the installed build's upgrade-check; nil: platform.RunCheck
	// with Env.
	Check func(ctx context.Context, path string, args []string) ([]byte, error)
	// The automatic upgrade (autoupgrade.go): off unless AutoUpgrade, which
	// boot sets unless DEVTOOLS_AUTO_UPGRADE=0. It takes its candidate from
	// the watch of the installed build.
	AutoUpgrade     bool
	UpgradeSettle   time.Duration          // 30 s: how long a new build must stay before it is taken
	UpgradeIdle     time.Duration          // 3 s without input on any window
	UpgradeEvery    time.Duration          // 10 min: at most one automatic attempt per this
	UpgradeTargetOK func(path string) bool // nil: only /nix/store paths
	// Force makes server.upgrade go ahead with the running build itself
	// (tests); a request's force needs it too.
	UpgradeForce bool
	// Journal records an attempt in the journal of the live upgrades
	// (boot's writer); the automatic upgrade records there a build the
	// preflight refused. nil: nothing is recorded.
	Journal func(handover.Attempt) error
	// UpgradeDelay is called at a phase of an attempt ("freeze": every
	// pane frozen, the model held) and returns when it may go on, or the
	// error that rolls it back (the context ended). Only the e2e tests set
	// it, to stop the service at that moment.
	UpgradeDelay func(ctx context.Context, phase string) error
}

// Defaults returns the production values of 5.3.6. StateDir, Shell, Env,
// Version, Host, Program, Self, InstalledVersion, NewPane, Log, Exec and
// Check stay zero: the caller (boot.Run) fills them.
func Defaults() Options {
	return Options{
		History:       10000,
		FrameInterval: 16 * time.Millisecond,
		CosmeticDelay: 100 * time.Millisecond,
		SyncHold:      150 * time.Millisecond,
		SaveDelay:     5 * time.Second,
		StallTimeout:  30 * time.Second,
		PingInterval:  5 * time.Second,
		HelloTimeout:  4 * time.Second,
		KillGrace:     250 * time.Millisecond,
		ProcInterval:  time.Second,
		FgInterval:    100 * time.Millisecond,

		UpdateInterval: 10 * time.Second,
		UpgradeSettle:  30 * time.Second,
		UpgradeIdle:    3 * time.Second,
		UpgradeEvery:   10 * time.Minute,
	}
}

// Pane is the method set of *pane.Pane: exactly the exported methods of
// 5.3.3, no more and no fewer. The assertion below pins the match.
type Pane interface {
	ID() string
	Key(k vt.Key) bool
	Mouse(e vt.Mouse) bool
	Paste(text string, first, last bool) bool
	Focus(focused bool) bool
	Resize(cols, rows int)
	SetColors(c vt.Colors)
	Follow(abs int64)
	Unfollow(abs int64)
	Snapshot(top int64, scrolled bool) pane.Snap
	Text(from, to vt.Pos, block bool) (text string, cut bool)
	Info() pane.Info
	Watch(wake func()) (cancel func())
	Close()
	// The live upgrade (pane/handover.go).
	Freeze(ctx context.Context) (pane.Frozen, error)
	Thaw()
	Abandon()
}

var _ Pane = (*pane.Pane)(nil)
