// Package backend holds the three implementations of mux.Multiplexer, one
// file each, one writer each (5.3.11 of the plan): devtools.go (P2-08b),
// tmux.go and herdr.go (P2-14). It runs no program itself (rule 9) and is
// imported by internal/cli and by test packages only.
//
// This file is a contract (6.2, P2-12), complete, and is not edited after
// P2-12. It calls the seams newDevtools, newTmux, newHerdr,
// devtoolsAvailable, tmuxAvailable and herdrAvailable, which are declared
// in the stub file of their backend first and in the real file after.
package backend

import (
	"fmt"
	"os"

	"devtools/internal/config"
	"devtools/internal/hub"
	"devtools/internal/mux"
)

type Deps struct {
	Config  *config.Config
	Actions hub.Actions
	Version string
	In, Out *os.File
	Env     []string
}

// The declarations the three implementations satisfy.
var (
	_ mux.Multiplexer = (*DevtoolsMultiplexer)(nil)
	_ mux.Multiplexer = (*TmuxMultiplexer)(nil)
	_ mux.Multiplexer = (*HerdrMultiplexer)(nil)
)

// CapsOf is the static table of 5.3.10: devtools all six, tmux
// detached-open, herdr agent-status. An unknown backend has the empty set.
func CapsOf(b mux.Backend) mux.Caps {
	switch b {
	case mux.Devtools:
		return mux.CapsOf(mux.All()...)
	case mux.Tmux:
		return mux.CapsOf(mux.CapDetached)
	case mux.Herdr:
		return mux.CapsOf(mux.CapAgents)
	}
	return mux.Caps{}
}

// Available returns nil, or why not. devtools: the server socket answers.
// tmux: the program is found on PATH. herdr: the program is found on PATH
// or through HERDR_BIN_PATH. The error's text is the Reason of
// *mux.ErrUnavailable.
func Available(b mux.Backend) error {
	switch b {
	case mux.Devtools:
		return devtoolsAvailable()
	case mux.Tmux:
		return tmuxAvailable()
	case mux.Herdr:
		return herdrAvailable()
	}
	return fmt.Errorf("unknown multiplexer %q", string(b))
}

// Open calls the constructor of b with the context and the dependencies.
// It starts nothing and connects to nothing; an unknown backend is an
// error.
func Open(b mux.Backend, c mux.Context, d Deps) (mux.Multiplexer, error) {
	switch b {
	case mux.Devtools:
		m, err := newDevtools(c, d)
		if err != nil {
			return nil, err
		}
		return m, nil
	case mux.Tmux:
		m, err := newTmux(c, d)
		if err != nil {
			return nil, err
		}
		return m, nil
	case mux.Herdr:
		m, err := newHerdr(c, d)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("unknown multiplexer %q", string(b))
}
