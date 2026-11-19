package fleet_test

import (
	"context"
	"testing"

	"devtools/internal/agent"
	"devtools/internal/client"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
)

// TestCrossMachineContractDeclaresEveryFunction names every function and
// method of the remote, agent and fleet contract of 5.3.8 with its
// signature. A missing declaration fails the build of this test.
func TestCrossMachineContractDeclaresEveryFunction(t *testing.T) {
	var (
		_ func(remote.DialOptions) remote.Dialer    = remote.NewDialer
		_ func(string) []string                     = remote.SSHArgs
		_ func(int, []byte, string) *remote.Failure = remote.Classify
		_                                           = remote.Bridge

		_ func() *agent.Tracker             = agent.New
		_                                   = (*agent.Tracker).Foreground
		_                                   = (*agent.Tracker).Title
		_                                   = (*agent.Tracker).Report
		_                                   = (*agent.Tracker).Seen
		_                                   = (*agent.Tracker).Tick
		_                                   = (*agent.Tracker).State
		_                                   = agent.NewSet
		_                                   = (*agent.Set).Title
		_                                   = (*agent.Set).Report
		_                                   = (*agent.Set).Procs
		_                                   = (*agent.Set).Seen
		_                                   = (*agent.Set).Gone
		_                                   = (*agent.Set).Tick
		_                                   = (*agent.Set).All
		_ func(agent.Status) int            = agent.Rank
		_ func([]agent.Status) agent.Status = agent.Rollup

		_ func(fleet.Options) *fleet.Fleet               = fleet.New
		_ func(*fleet.Fleet, context.Context) error      = (*fleet.Fleet).Run
		_ func(*fleet.Fleet) <-chan fleet.Update         = (*fleet.Fleet).Updates
		_ func(*fleet.Fleet, session.Target)             = (*fleet.Fleet).Switch
		_ func(*fleet.Fleet)                             = (*fleet.Fleet).Cancel
		_ func(*fleet.Fleet, proto.Msg)                  = (*fleet.Fleet).Input
		_ func(*fleet.Fleet, proto.Msg) <-chan proto.Msg = (*fleet.Fleet).Request
		_ func(*fleet.Fleet, int, int)                   = (*fleet.Fleet).Resize
		_ func(*fleet.Fleet, bool)                       = (*fleet.Fleet).Focus
		_ func(*fleet.Fleet, proto.Msg)                  = (*fleet.Fleet).Colors
		_ func(*fleet.Fleet) <-chan hub.Event            = (*fleet.Fleet).HubOpened
		_ func(*fleet.Fleet)                             = (*fleet.Fleet).HubClosed
		_ func(*fleet.Fleet, int)                        = (*fleet.Fleet).Step
		_ func(*fleet.Fleet, session.Target)             = (*fleet.Fleet).Drop

		_ client.Fleet = (*fleet.Fleet)(nil) // internal/client/options.go: *fleet.Fleet satisfies it
	)
}
