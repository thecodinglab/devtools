package chrome

// The entry point of the look (P4-00 commit b, step 7 of 6.2): the chrome the
// user sees, as opposed to the ASCII reference Plain (P1-07). This file is a
// contract and is read-only for every other package (6.6).
//
// The methods of the look are written by two packages, each in its own
// files, so that they can work in parallel: Status and StatusRows by P4-02
// (status.go, reduce.go; stub_status.go until then), every other method of
// Chrome by P4-03 (area.go; stub_area.go until then). The compile-time
// assertion below fails the build if a method is missing after a stub file
// is deleted.

// LookOptions configures the look.
type LookOptions struct {
	Chords func(action, arg string) []string // keys.Table.Chords; nil: no hint strip
}

// look is the Chrome returned by NewLook. It keeps no state between calls:
// everything it shows comes from the State it is given, the theme included.
type look struct {
	chords func(action, arg string) []string // LookOptions.Chords; nil: no hint strip
}

var _ Chrome = (*look)(nil)

// NewLook returns the chrome of 5a: the status row of 5a.6.2 and 5a.6.3, the
// divider, banner, pad, pane message and too-small screen of 5a.6.4 to
// 5a.6.8, in the colours and glyphs of State.Theme.
func NewLook(o LookOptions) Chrome {
	return &look{chords: o.Chords}
}
