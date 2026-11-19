package backend

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"devtools/internal/client/guard"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/session"
)

// ordinaryTerminal is a scripted reader and writer that answers the probe
// of the guard as an ordinary terminal does: XTVERSION with a name that is
// no multiplexer, then DA1. It holds the answer back until the queries were
// written.
type ordinaryTerminal struct {
	mu      sync.Mutex
	wrote   bytes.Buffer
	pending []byte
	ready   chan struct{}
	once    sync.Once
}

func newOrdinaryTerminal() *ordinaryTerminal {
	return &ordinaryTerminal{
		pending: []byte("\x1bP>|ghostty 1.2.3\x1b\\\x1b[?62;22c"),
		ready:   make(chan struct{}),
	}
}

func (o *ordinaryTerminal) Write(p []byte) (int, error) {
	o.mu.Lock()
	o.wrote.Write(p)
	o.mu.Unlock()
	o.once.Do(func() { close(o.ready) })
	return len(p), nil
}

func (o *ordinaryTerminal) Read(p []byte) (int, error) {
	<-o.ready
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.pending) == 0 {
		return 0, io.EOF
	}
	n := copy(p, o.pending)
	o.pending = o.pending[n:]
	return n, nil
}

// realPass is a pass from the real guard.Check on an ordinary terminal and
// an environment without a multiplexer mark, never a value built in the
// test (6.5, P2-14).
func realPass(t *testing.T) *guard.Pass {
	t.Helper()
	pass, nested, err := guard.Check(newOrdinaryTerminal(), []string{"TERM=xterm-ghostty", "HOME=/nonexistent"}, nil, 2*time.Second, false)
	if err != nil || nested != nil || !pass.Valid() {
		t.Fatalf("guard.Check on an ordinary terminal = %v, %+v, %v; want a valid pass", pass.Valid(), nested, err)
	}
	return pass
}

// openBackends opens the tmux and herdr backends with the fake of each
// installed and returns them with the log the fake records. P2-08b adds
// the devtools rows.
type opened struct {
	m   mux.Multiplexer
	log string
}

func openBackends(t *testing.T, c mux.Context) []opened {
	t.Helper()
	tmuxLog := installFakeTmux(t)
	herdrLog := installFakeHerdr(t, workspace("w1", "proj-feat", false, "idle"), agentObject("w1:p1", "w1", "claude", "idle"), true)
	var out []opened
	for _, b := range []struct {
		name mux.Backend
		log  string
	}{{mux.Tmux, tmuxLog}, {mux.Herdr, herdrLog}} {
		m, err := Open(b.name, c, Deps{})
		if err != nil {
			t.Fatalf("Open(%s): %v", b.name, err)
		}
		out = append(out, opened{m, b.log})
	}
	return out
}

// gated is every gated method of mux.Multiplexer with the capability it
// needs.
var gated = []struct {
	name string
	need mux.Capability
	call func(m mux.Multiplexer, ctx testingContext) error
}{
	{"Agents", mux.CapAgents, func(m mux.Multiplexer, ctx testingContext) error {
		items, err := m.Agents(ctx.t.Context())
		if items != nil {
			ctx.t.Errorf("a refused Agents returned items %+v", items)
		}
		return err
	}},
	{"Jump", mux.CapAgents, func(m mux.Multiplexer, ctx testingContext) error {
		return m.Jump(hub.Item{Kind: hub.KindAgent, Pane: "w1:p1"})
	}},
	{"Snapshot", mux.CapLayout, func(m mux.Multiplexer, ctx testingContext) error {
		_, err := m.Snapshot(ctx.t.Context())
		return err
	}},
	{"FocusPane", mux.CapPaneFocus, func(m mux.Multiplexer, ctx testingContext) error {
		moved, err := m.FocusPane("left")
		if moved {
			ctx.t.Error("a refused FocusPane moved")
		}
		return err
	}},
}

type testingContext struct{ t *testing.T }

func TestBackendsRefuseWhatTheyLack(t *testing.T) {
	refused := 0
	for _, c := range []mux.Context{inTmux, inHerdr, inTerminal} {
		for _, b := range []mux.Backend{mux.Tmux, mux.Herdr} {
			for _, g := range gated {
				if CapsOf(b).Has(g.need) {
					continue
				}
				refused++
				t.Run(whereName(c.Where)+"/"+string(b)+"/"+g.name, func(t *testing.T) {
					all := openBackends(t, c)
					for _, o := range all {
						if o.m.Name() != b {
							continue
						}
						err := g.call(o.m, testingContext{t})
						var u *mux.ErrUnsupported
						if !errors.As(err, &u) || u.Backend != b || u.Capability != g.need {
							t.Fatalf("%s.%s = %v, want *ErrUnsupported{%s, %s}", b, g.name, err, b, g.need)
						}
					}
					// neither fake program saw anything
					for _, o := range all {
						wantEmpty(t, o.log)
					}
				})
			}
		}
	}
	// tmux lacks all four, herdr Snapshot and FocusPane: six per context.
	if refused != 3*6 {
		t.Errorf("%d refusals were tested, want 18", refused)
	}

	// The devtools rows (P2-08b): devtools has every capability, so no
	// gated method is refused, inside a pane or outside one.
	for _, c := range []mux.Context{inDevtools, inTerminal} {
		calls := map[string]func(d mux.Multiplexer) error{
			"Agents": func(d mux.Multiplexer) error { _, err := d.Agents(t.Context()); return err },
			"Jump": func(d mux.Multiplexer) error {
				return d.Jump(hub.Item{Kind: hub.KindAgent, SessionID: "s2", Tab: "t2", Pane: "p2"})
			},
			"Snapshot":  func(d mux.Multiplexer) error { _, err := d.Snapshot(t.Context()); return err },
			"FocusPane": func(d mux.Multiplexer) error { _, err := d.FocusPane("left"); return err },
		}
		for _, g := range gated {
			t.Run(whereName(c.Where)+"/devtools/"+g.name, func(t *testing.T) {
				f := newFakeServer(t, paneSnapshot)
				f.set(func(f *fakeServer) { f.moved = true })
				d := openDevtools(t, f, c, "p1", Deps{})
				if !d.Caps().Has(g.need) {
					t.Fatalf("devtools lacks %s", g.need)
				}
				if _, ok := errAs[*mux.ErrUnsupported](calls[g.name](d)); ok {
					t.Fatalf("devtools.%s was refused", g.name)
				}
			})
		}
	}

	// Show without a window needs detached-open, which herdr lacks: the one
	// late refusal (cmd.work of the feature table).
	t.Run("herdr Show outside herdr", func(t *testing.T) {
		for _, o := range openBackends(t, inTerminal) {
			if o.m.Caps().Has(mux.CapDetached) {
				continue
			}
			_, err := o.m.Show("/work/proj/feat", "proj-feat")
			var u *mux.ErrUnsupported
			if !errors.As(err, &u) || u.Backend != o.m.Name() || u.Capability != mux.CapDetached {
				t.Fatalf("%s.Show outside = %v, want *ErrUnsupported{%s, detached-open}", o.m.Name(), err, o.m.Name())
			}
			wantEmpty(t, o.log)
		}
	})
}

func TestCapsTableMatchesTheImplementations(t *testing.T) {
	for _, c := range []mux.Context{inTmux, inHerdr, inTerminal} {
		for _, o := range openBackends(t, c) {
			if got, want := o.m.Caps(), CapsOf(o.m.Name()); got != want {
				t.Errorf("%s.Caps() = %v, CapsOf = %v", o.m.Name(), got.List(), want.List())
			}
		}
	}
	// the devtools rows (P2-08b)
	f := newFakeServer(t, paneSnapshot)
	for _, c := range []mux.Context{inDevtools, inTmux, inTerminal} {
		d := openDevtools(t, f, c, "p1", Deps{})
		if got, want := d.Caps(), CapsOf(mux.Devtools); got != want {
			t.Errorf("devtools.Caps() = %v, CapsOf = %v", got.List(), want.List())
		}
	}
	for _, b := range []mux.Backend{mux.Devtools, mux.Tmux, mux.Herdr} {
		m, err := Open(b, inTerminal, Deps{})
		if err != nil {
			t.Fatal(err)
		}
		if m.Name() != b {
			t.Errorf("Open(%s).Name() = %s", b, m.Name())
		}
	}
}

func TestEnterNeedsAPass(t *testing.T) {
	entries := []struct {
		name string
		e    mux.Entry
	}{
		{"no entry", mux.Entry{}},
		{"a target", mux.Entry{Target: &session.Target{Dir: "/work/proj/feat", Name: "proj-feat"}}},
		{"a hub", mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAll}}},
	}
	passes := []struct {
		name string
		p    *guard.Pass
	}{{"nil", nil}, {"zero", &guard.Pass{}}}
	for _, c := range []mux.Context{inTerminal, {Where: mux.NoTerminal}} {
		for _, e := range entries {
			for _, p := range passes {
				t.Run(whereName(c.Where)+"/"+e.name+"/"+p.name, func(t *testing.T) {
					noHub(t)
					for _, o := range openBackends(t, c) {
						if err := o.m.Enter(p.p, e.e); !errors.Is(err, mux.ErrNoPass) {
							t.Errorf("%s.Enter with a %s pass = %v, want ErrNoPass", o.m.Name(), p.name, err)
						}
						wantEmpty(t, o.log)
					}
					// the devtools rows (P2-08b): the recording runner
					// and the fake server saw nothing
					seen := recordClient(t, nil)
					f := newFakeServer(t, paneSnapshot)
					d := openDevtools(t, f, c, "", Deps{})
					if err := d.Enter(p.p, e.e); !errors.Is(err, mux.ErrNoPass) {
						t.Errorf("devtools.Enter with a %s pass = %v, want ErrNoPass", p.name, err)
					}
					if len(*seen) != 0 || len(f.Hellos()) != 0 {
						t.Errorf("devtools.Enter without a pass ran the client %d times and said %d hellos", len(*seen), len(f.Hellos()))
					}
				})
			}
		}
	}
}
