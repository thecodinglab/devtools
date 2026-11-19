// Package guard is the nesting guard: the environment check and the probe
// of the outer terminal, and the Pass that every Multiplexer.Enter and
// client.Run need (3.4, 5.3.7 of the plan). It imports the standard library
// only.
//
// This file is a contract (6.2, P1-00b) and is read-only for P1-11, which
// writes Env, Probe, Answer.Nested and Check in place of stub.go.
package guard

import "errors"

// Nested says what the terminal is inside. Where is one of "devtools",
// "tmux", "screen", "zellij", "herdr", "unknown terminal"; Host comes from
// the XTVERSION answer when it carries one.
type Nested struct{ Where, Host string }

// Answer is what the outer terminal answered to the probe.
type Answer struct {
	XTVersion string // name and version as answered; "" when no XTVERSION answer arrived
	DA1       bool   // a DA1 answer arrived
}

// Pass proves that Check ran and found no multiplexer around this
// terminal. Only Check returns a valid one: the field is unexported and
// there is no constructor, so &Pass{} and nil are both invalid.
type Pass struct{ ok bool }

// Valid is false for nil and for the zero value.
func (p *Pass) Valid() bool { return p != nil && p.ok }

// ErrNoPass is returned by whoever needs a valid Pass and got none.
var ErrNoPass = errors.New("devtools: no guard pass")
