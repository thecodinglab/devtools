package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"devtools/internal/backend"
	"devtools/internal/client/guard"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
)

// recordingMux is a mux.Multiplexer that records every call of a method
// that acts, with its arguments, and returns what the test set. Name and
// Caps act on nothing and are not recorded.
type recordingMux struct {
	name mux.Backend

	showShown mux.Shown
	showErr   error
	leaveErr  error
	closeLast func() error // run by the function Leave returns; nil: nothing
	hubErr    error
	enterErr  error

	mu     sync.Mutex
	calls  []string
	passes []*guard.Pass // what each Enter was given

	opened []mux.Backend // what openBackend was asked for
	ctx    mux.Context   // the context the last open was given
	deps   backend.Deps  // the dependencies the last open was given
}

func (r *recordingMux) record(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, fmt.Sprintf(format, args...))
}

// Calls returns the recorded calls.
func (r *recordingMux) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// count is the number of recorded calls of the method.
func (r *recordingMux) count(method string) int {
	n := 0
	for _, c := range r.Calls() {
		if c == method || strings.HasPrefix(c, method+" ") {
			n++
		}
	}
	return n
}

func (r *recordingMux) Name() mux.Backend {
	if r.name == "" {
		return mux.Tmux
	}
	return r.name
}

func (r *recordingMux) Caps() mux.Caps { return backend.CapsOf(r.Name()) }

func (r *recordingMux) Show(dir, name string) (mux.Shown, error) {
	r.record("Show %s %s", dir, name)
	return r.showShown, r.showErr
}

func (r *recordingMux) Leave(dir, fallbackDir, fallbackName string) (func() error, error) {
	r.record("Leave %s %s %s", dir, fallbackDir, fallbackName)
	if r.leaveErr != nil {
		return nil, r.leaveErr
	}
	return func() error {
		r.record("closeLast")
		if r.closeLast != nil {
			return r.closeLast()
		}
		return nil
	}, nil
}

func (r *recordingMux) Hub(mode hub.Mode, query string) error {
	r.record("Hub %d %q", mode, query)
	return r.hubErr
}

func (r *recordingMux) Sessions(ctx context.Context) ([]hub.Item, error) {
	r.record("Sessions")
	return nil, nil
}

func (r *recordingMux) Enter(p *guard.Pass, e mux.Entry) error {
	what := "last"
	switch {
	case e.Hub != nil:
		what = fmt.Sprintf("hub %d %q", e.Hub.Mode, e.Hub.Query)
	case e.Target != nil:
		what = fmt.Sprintf("target %s %s", e.Target.Dir, e.Target.Name)
	}
	r.record("Enter valid=%v %s", p.Valid(), what)
	r.mu.Lock()
	r.passes = append(r.passes, p)
	r.mu.Unlock()
	if !p.Valid() {
		return mux.ErrNoPass
	}
	return r.enterErr
}

func (r *recordingMux) Agents(ctx context.Context) ([]hub.Item, error) {
	r.record("Agents")
	return nil, nil
}

func (r *recordingMux) Jump(agent hub.Item) error {
	r.record("Jump %s", agent.Pane)
	return nil
}

func (r *recordingMux) Snapshot(ctx context.Context) (proto.Snapshot, error) {
	r.record("Snapshot")
	return proto.Snapshot{}, nil
}

func (r *recordingMux) FocusPane(direction string) (bool, error) {
	r.record("FocusPane %s", direction)
	return false, nil
}

// useRecording hands r to every command of the test through openBackend:
// the selection runs as it does for real, and the backend it chose is
// recorded in r.opened.
func useRecording(t *testing.T, r *recordingMux) {
	t.Helper()
	saved := openBackend
	openBackend = func(b mux.Backend, c mux.Context, d backend.Deps) (mux.Multiplexer, error) {
		r.mu.Lock()
		r.opened = append(r.opened, b)
		r.ctx = c
		r.deps = d
		r.mu.Unlock()
		return r, nil
	}
	t.Cleanup(func() { openBackend = saved })
}
