//go:build cgo && dynamic

package vt

// The shared library, for local work only (go build -tags dynamic). No
// check of the plan uses it; link_static.go is what every build links.

// #cgo pkg-config: libghostty-vt
import "C"
