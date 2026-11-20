package mux

// Which backend a command uses (5.3.10 of the plan). The types are a
// contract (6.2, P2-12); the bodies of Default and Select are written by
// P2-13.

import (
	"errors"
	"fmt"
)

type Choice struct {
	Env    string // DEVTOOLS_MULTIPLEXER
	Config string // the `multiplexer` directive
}

type ErrMismatch struct{ Asked, Inside Backend }
type ErrUnavailable struct {
	Backend Backend
	Reason  string
}
type ErrAmbiguous struct{ Marks []Backend }

// The Error methods are plain Go error texts; what a person reads is
// ui.Message("mismatch", ...), ("unavailable", ...) or ("noserver"), and
// ("ambiguous", ...) (5.3.9).

func (e *ErrMismatch) Error() string {
	return fmt.Sprintf("DEVTOOLS_MULTIPLEXER asks for %s inside %s", e.Asked, e.Inside)
}

func (e *ErrUnavailable) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("multiplexer %s is not available", e.Backend)
	}
	return fmt.Sprintf("multiplexer %s is not available: %s", e.Backend, e.Reason)
}

func (e *ErrAmbiguous) Error() string {
	return fmt.Sprintf("several multiplexers around this terminal: %v", e.Marks)
}

// Default is the backend used when nothing chooses one: devtools. A
// surrounding multiplexer is decided by Select before the default is asked.
func Default() Backend {
	return Devtools
}

// The two sources of a backend name, as the errors of Select name them
// (internal/mux/mux.go:34-39 on main).
const (
	sourceEnv    = "DEVTOOLS_MULTIPLEXER"
	sourceConfig = "config multiplexer"
)

// Select returns the backend of a command, or one of the typed errors. It
// is total and starts nothing.
//
//  1. c.Where is Ambiguous: *ErrAmbiguous with c.Marks.
//  2. Inside backend b: ch.Env naming another backend is *ErrMismatch;
//     ch.Config is not read; available(b) failing is *ErrUnavailable;
//     otherwise b.
//  3. Elsewhere the pick is ch.Env, else ch.Config, else Default; a name
//     Parse rejects is an error that says which source held it;
//     available(pick) failing is *ErrUnavailable; otherwise the pick.
//
// A name Parse rejects in ch.Env is that error inside a backend too. No
// step tries another backend (N26). A nil available counts as a check
// that fails for every backend.
func Select(c Context, ch Choice, available func(Backend) error) (Backend, error) {
	check := func(b Backend) error {
		if available == nil {
			return errors.New("no availability check")
		}
		return available(b)
	}

	if c.Where == Ambiguous {
		return "", &ErrAmbiguous{Marks: append([]Backend(nil), c.Marks...)}
	}

	if b, inside := c.Inside(); inside {
		if ch.Env != "" {
			asked, err := parseFrom(ch.Env, sourceEnv)
			if err != nil {
				return "", err
			}
			if asked != b {
				return "", &ErrMismatch{Asked: asked, Inside: b}
			}
		}
		if err := check(b); err != nil {
			return "", &ErrUnavailable{Backend: b, Reason: err.Error()}
		}
		return b, nil
	}

	var pick Backend
	switch {
	case ch.Env != "":
		b, err := parseFrom(ch.Env, sourceEnv)
		if err != nil {
			return "", err
		}
		pick = b
	case ch.Config != "":
		b, err := parseFrom(ch.Config, sourceConfig)
		if err != nil {
			return "", err
		}
		pick = b
	default:
		pick = Default()
	}
	if err := check(pick); err != nil {
		return "", &ErrUnavailable{Backend: pick, Reason: err.Error()}
	}
	return pick, nil
}

// parseFrom is Parse with an error that names the source of the value.
func parseFrom(value, source string) (Backend, error) {
	b, err := Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	return b, nil
}
