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

	"devtools/internal/pane"
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

	// The installed update (update.go). Program is the path the service
	// starts the server through, the devtools of the profile, a symlink;
	// Executable is the file of the running image, as the kernel names it
	// (platform.RunningExecutable), never the symlink it was started by.
	// Either "" watches nothing.
	// InstalledVersion asks the build at a path for its version; nil runs
	// "<path> version".
	Program          string
	Executable       string
	UpdateInterval   time.Duration // 10 s
	InstalledVersion func(ctx context.Context, path string) (string, error)

	NewPane func(pane.Options) Pane // nil: pane.Start; tests of the core pass a fake
	Log     *slog.Logger
}

// Defaults returns the production values of 5.3.6. StateDir, Shell, Env,
// Version, Host, Program, Executable, InstalledVersion, NewPane and Log stay zero: the caller (boot.Run) fills them.
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
}

var _ Pane = (*pane.Pane)(nil)
