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

// SnapshotPrefix is the prefix of the screen formats of the engine's
// snapshot.
const SnapshotPrefix = "ghostty-snapshot/"

// SnapshotVersion is 0: the build has no engine.
func SnapshotVersion() int { return 0 }

// ScreenFormats is empty: the build has no engine.
func ScreenFormats() []string { return nil }

// Encode panics: the build has no engine.
func Encode(t Term) ([]byte, error) { panic(noEngine) }

// Restore panics: the build has no engine.
func Restore(o Options, data []byte, first, base int64) (Term, error) { panic(noEngine) }
