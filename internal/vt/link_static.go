//go:build cgo && !dynamic

package vt

// The link line of every build of the product: the static library through
// pkg-config (P3-08). The shared variant is link_dynamic.go; the two lines
// must stay in two files, because with both in one file a cached static
// build was linked under -tags dynamic (report 09, 5.5 point 5).

// #cgo pkg-config: --static libghostty-vt-static
import "C"
