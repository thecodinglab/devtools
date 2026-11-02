// Package mux picks the terminal multiplexer that hosts devtools sessions.
package mux

import (
	"fmt"
	"os"

	"devtools/internal/herdr"
)

type Backend string

const (
	Tmux  Backend = "tmux"
	Herdr Backend = "herdr"
)

// Select returns the backend for sessions. DEVTOOLS_MULTIPLEXER, then the
// configured multiplexer, override the default: tmux inside tmux, herdr
// otherwise, falling back to tmux when herdr is not installed.
func Select(configured string) (Backend, error) {
	if env := os.Getenv("DEVTOOLS_MULTIPLEXER"); env != "" {
		return parse(env, "DEVTOOLS_MULTIPLEXER")
	}
	if configured != "" {
		return parse(configured, "config multiplexer")
	}
	if os.Getenv("TMUX") != "" || !herdr.Installed() {
		return Tmux, nil
	}
	return Herdr, nil
}

func parse(value, source string) (Backend, error) {
	switch backend := Backend(value); backend {
	case Tmux, Herdr:
		return backend, nil
	}
	return "", fmt.Errorf("%s: unknown multiplexer %q, want tmux or herdr", source, value)
}
