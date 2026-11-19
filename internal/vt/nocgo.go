//go:build !cgo

package vt

// This file exists so that the cross vet of CHECK
// (GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go vet) type-checks on Linux. No
// build of the product uses it: every binary is built with cgo and the
// engine.

const noEngine = "devtools: built without cgo; the terminal engine is missing"

// New panics: the build has no engine.
func New(o Options) Term { panic(noEngine) }

// SetIdentity panics: the build has no engine.
func SetIdentity(name, version, host string) { panic(noEngine) }

// Extract panics: the build has no engine.
func Extract(t Term, from, to Pos, block bool, max int) (string, bool) { panic(noEngine) }
