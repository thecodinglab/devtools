package client

// Run, the start of the client program (3.2.4, 3.5.6, 5.3.7).

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"devtools/internal/client/guard"
	"devtools/internal/fleet"
)

// serverWait is how long Run waits for the local link at the start.
const serverWait = 5 * time.Second

// fps is the renderer's rate: bubbletea's maximum, so a keystroke waits at
// most 8.3 ms for the next frame (3.2.4; E3: at the default of 60, T4
// fails).
const fps = 120

// runner is the part of *fleet.Fleet that the client starts: the fleet's
// loop. Options.Fleet is the interface of 5.3.7, which has no Run; the
// client runs the loop of a fleet that has one for as long as it runs.
type runner interface {
	Run(ctx context.Context) error
}

// runConfig is what tests change about Run.
type runConfig struct {
	isTerminal func(f *os.File) bool
	options    []tea.ProgramOption // added after Run's own
	serverWait time.Duration
}

// Run runs the client until detach or until the fleet ends. It does not run
// the guard: the guard ran once before Multiplexer.Enter (5.3.10) and its
// result is o.Pass. Without a valid Pass, Run returns guard.ErrNoPass and
// touches neither the terminal nor the fleet. It returns ErrNotTerminal
// when its input or output is not a terminal, and ErrNoServer when the
// local server does not answer at the start, with or without machine lines
// (3.5.6, F45), both before it touches the terminal. A client that is
// already running when its server goes away stays; its local link retries
// (3.5.2). The program runs with tea.WithFPS(120).
func Run(o Options) error {
	return run(o, runConfig{})
}

func run(o Options, cfg runConfig) error {
	if !o.Pass.Valid() {
		return guard.ErrNoPass
	}
	if o.Fleet == nil {
		return errors.New("devtools: the client has no fleet")
	}
	if o.In == nil {
		o.In = os.Stdin
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Env == nil {
		o.Env = os.Environ()
	}
	if cfg.isTerminal == nil {
		cfg.isTerminal = func(f *os.File) bool { return term.IsTerminal(f.Fd()) }
	}
	if cfg.serverWait == 0 {
		cfg.serverWait = serverWait
	}
	if !cfg.isTerminal(o.In) || !cfg.isTerminal(o.Out) {
		return ErrNotTerminal
	}
	m, err := NewModel(o, RealHooks())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	if r, ok := o.Fleet.(runner); ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Run(ctx)
		}()
	}

	// The local link first: without a local server no client starts, and
	// nothing has been written to the terminal yet (F45).
	early, err := waitLocal(ctx, o.Fleet.Updates(), cfg.serverWait)
	if err != nil {
		return err
	}
	if o.Initial != nil {
		o.Fleet.Switch(*o.Initial)
	}

	opts := []tea.ProgramOption{
		tea.WithInput(o.In),
		tea.WithOutput(o.Out),
		tea.WithEnvironment(o.Env),
		tea.WithFPS(fps),
		tea.WithContext(ctx),
	}
	p := tea.NewProgram(m, append(opts, cfg.options...)...)
	wg.Add(1)
	go func() {
		defer wg.Done()
		readUpdates(ctx, p, o.Fleet.Updates(), early)
	}()
	_, err = p.Run()
	return err
}

// waitLocal reads updates until the summary says how the local link is:
// online goes on; any other settled state, an ended fleet or the time
// limit is ErrNoServer. The updates read are returned to be applied first.
func waitLocal(ctx context.Context, updates <-chan fleet.Update, limit time.Duration) ([]fleet.Update, error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	var early []fleet.Update
	for {
		select {
		case <-ctx.Done():
			return nil, ErrNoServer
		case <-timer.C:
			return nil, ErrNoServer
		case u, ok := <-updates:
			if !ok {
				return nil, ErrNoServer
			}
			early = append(early, u)
			if u.Kind != fleet.Summary || len(u.Summary.Machines) == 0 {
				continue
			}
			switch u.Summary.Machines[0].State {
			case "online":
				return early, nil
			case "retrying", "attention", "off":
				return nil, ErrNoServer
			}
		}
	}
}

// readUpdates hands the fleet's updates to the program: everything that is
// ready, at most maxBatch, in one message (3.2.4). The end of the channel
// ends the program.
func readUpdates(ctx context.Context, p *tea.Program, updates <-chan fleet.Update, early []fleet.Update) {
	if len(early) > 0 {
		p.Send(UpdatesMsg(early))
	}
	for {
		var u fleet.Update
		var ok bool
		select {
		case <-ctx.Done():
			return
		case u, ok = <-updates:
		}
		if !ok {
			p.Send(fleetEnded{})
			return
		}
		batch := UpdatesMsg{u}
	more:
		for len(batch) < maxBatch {
			select {
			case u, ok := <-updates:
				if !ok {
					p.Send(batch)
					p.Send(fleetEnded{})
					return
				}
				batch = append(batch, u)
			default:
				break more
			}
		}
		p.Send(batch)
	}
}
