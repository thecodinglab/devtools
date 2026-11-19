package cli

// The two routines behind every command that shows a session or turns this
// terminal into a window (3.5.6, 5.3.8 of the plan, P2-08a). This file holds
// the only calls of Multiplexer.Enter in the tree (rule 10,
// TestEnterIsCalledOnlyInEntryGo): a client of any backend starts here, in a
// plain terminal, after guard.Check passed, and nowhere else (3.4 a and c).

import (
	"fmt"
	"os"
	"time"

	"devtools/internal/client/guard"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// checkGuard is the guard every Enter needs a pass from. Tests may replace
// it to see what it was asked, never with something that makes a pass: a
// valid Pass comes only from the real guard.Check.
var checkGuard = guard.Check

// probeTimeout is how long the guard waits for the terminal to answer its
// probe (3.4 c: 2 s).
const probeTimeout = 2 * time.Second

// unknownTerminal is the guard's Where for a refusal that cannot name the
// multiplexer: a silent terminal, DA1 without XTVERSION, a relay of an
// unknown name (guard.Nested, 5.3.7).
const unknownTerminal = "unknown terminal"

// show is the one routine behind every command that shows a session: new,
// clone, migrate, work <branch>, switch <name>, session-open and a pick of
// the legacy hub. By context, with m the selected backend:
//
//   - inside the selected backend: m.Show;
//   - InTerminal: guard.Check with Context.Dead as dead; passed, m.Enter
//     with the session as the target; refused, m.Show (which needs
//     detached-open) and then the nested or othermux words, status 0;
//   - NoTerminal: m.Show;
//   - InForeign: m.Show, then the othermux words.
//
// An *mux.ErrUnsupported of m.Show (the backend cannot open a session where
// no window shows the caller) becomes the unsupported words, which say that
// the session was not opened, and status 1: a command of the kind cmd.work
// has done its git work by then.
func (a *app) show(dir, name string) (mux.Shown, error) {
	m, c, err := a.multiplexer()
	if err != nil {
		return 0, err
	}
	if _, inside := c.Inside(); inside {
		// Select returns the surrounding backend and no other, so m is it.
		shown, err := showWords(m.Show(dir, name))
		if err == nil && shown == mux.OpenedOnly {
			// a pane no window shows: the session is open, nothing moved
			a.say(ui.Message("no-client"))
			a.say(ui.Message("opened-only", name))
		}
		return shown, err
	}
	switch c.Where {
	case mux.InTerminal:
		pass, nested, gerr := a.checkTerminal(c)
		if pass.Valid() {
			target := &session.Target{Dir: dir, Name: name}
			if err := m.Enter(pass, mux.Entry{Target: target}); err != nil {
				return 0, enterWords(err)
			}
			return mux.ShownInWindow, nil
		}
		shown, err := showWords(m.Show(dir, name))
		a.say(refusalWords(m.Name(), nested, gerr))
		return shown, err
	case mux.NoTerminal:
		return showWords(m.Show(dir, name))
	case mux.InForeign:
		shown, err := showWords(m.Show(dir, name))
		a.say(ui.Message("othermux", string(m.Name()), c.Foreign))
		return shown, err
	}
	// Ambiguous never gets here: Select ends it with *mux.ErrAmbiguous.
	return 0, fmt.Errorf("devtools: no routine for the context %d", c.Where)
}

// enter is the one routine behind attach, pick, sessions and agents typed
// outside a multiplexer. Inside the selected backend it opens that
// backend's hub in the caller's window (an entry without Hub: the hub in
// mode all); in a plain terminal it runs the guard and, when it passed,
// m.Enter; without a terminal and inside screen or zellij it refuses with
// status 1 and calls nothing on the multiplexer.
func (a *app) enter(e mux.Entry) error {
	m, c, err := a.multiplexer()
	if err != nil {
		return err
	}
	if _, inside := c.Inside(); inside {
		mode, query := hub.ModeAll, ""
		if e.Hub != nil {
			mode, query = e.Hub.Mode, e.Hub.Query
		}
		return m.Hub(mode, query)
	}
	switch c.Where {
	case mux.InTerminal:
		pass, nested, gerr := a.checkTerminal(c)
		if !pass.Valid() {
			return words(refusalWords(m.Name(), nested, gerr))
		}
		return enterWords(m.Enter(pass, e))
	case mux.NoTerminal:
		return words(ui.Message("notty"))
	case mux.InForeign:
		return words(ui.Message("othermux", string(m.Name()), c.Foreign))
	}
	return fmt.Errorf("devtools: no routine for the context %d", c.Where)
}

// checkTerminal runs the guard on the terminal of this command: the
// environment check with c.Dead as the dead marks, then the probe of the
// terminal, in raw mode, through a descriptor of its own (openProbe).
func (a *app) checkTerminal(c mux.Context) (*guard.Pass, *guard.Nested, error) {
	tty, done, err := openProbe(terminalIn)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	trust := os.Getenv("DEVTOOLS_TRUST_TERMINAL") == "1"
	return checkGuard(tty, os.Environ(), c.Dead, probeTimeout, trust)
}

// refusalWords is what a person reads when the guard refused, or could not
// ask the terminal: the nested words for a devtools pane, the unknown words
// for a terminal that did not say what it is, and the othermux words for
// the multiplexer it named.
func refusalWords(b mux.Backend, n *guard.Nested, err error) string {
	switch {
	case err != nil || n == nil || n.Where == unknownTerminal:
		return ui.Message("unknown")
	case n.Where == "devtools":
		return ui.Message("nested", string(b), n.Host)
	}
	return ui.Message("othermux", string(b), n.Where)
}

// enterWords turns an *mux.ErrUnavailable of Enter (the server went away
// between the selection and the client) into the words of the selection;
// every other result passes as it is.
func enterWords(err error) error {
	if u, ok := err.(*mux.ErrUnavailable); ok {
		return selectionWords(u)
	}
	return err
}

// showWords turns the *mux.ErrUnsupported of a Show into the unsupported
// words; every other result passes as it is.
func showWords(shown mux.Shown, err error) (mux.Shown, error) {
	if u, ok := err.(*mux.ErrUnsupported); ok {
		return shown, words(ui.Message("unsupported", string(u.Backend), string(u.Capability)))
	}
	return shown, err
}
