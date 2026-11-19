// Package vt is the adapter over the terminal engine, libghostty-vt 1.0.1,
// and the only package with import "C" (import rule 5). It exports no C
// type: every other package sees the Go types of this file and nothing of
// the library (5.3.2).
//
// This file is the contract. It imports the standard library only and has
// no build constraint, so the contract type-checks with and without cgo.
package vt

// Colors are the default colours of a terminal, used for the answers to
// OSC 10 and 11.
type Colors struct {
	Fg, Bg string // "#rrggbb"
	Dark   bool
}

// Cursor is the cursor of the live screen.
type Cursor struct {
	X, Y    int
	Visible bool
	Shape   string // "block", "underline", "bar"
	Blink   bool
}

// Modes are the modes of a terminal that the server and the client act on.
// modifyOtherKeys is not among them: the library has no data item for it and
// only the key encoder, which reads it itself, needs it.
type Modes struct {
	Alt       bool
	Mouse     string // "", "press", "drag", "all"
	MouseSGR  bool
	Paste     bool // 2004
	Focus     bool // 1004
	AppCursor bool // DECCKM
	AppKeypad bool // DECKPAM, DEC mode 66
	Kitty     int  // current flags
}

// AgentReport is one OSC 7501 root report (3.6.2, E4), with the library's
// state and kind names. It carries no id: records of subagents and
// background tasks never reach OnAgent (contract change 2).
type AgentReport struct{ State, Kind string }

// Pos is a position: an absolute line number and a cell.
type Pos struct {
	Line int64 // absolute
	Col  int
}

// Options configure a new terminal.
type Options struct {
	Cols, Rows int
	History    int // lines; 10,000 in production
	Colors     Colors
	// Callbacks run inside Write and Resize, on the caller's goroutine and
	// thread. A callback must not call a method of the Term it belongs to.
	OnTitle     func(string) // the title, sanitised, at most 256 runes
	OnCwd       func(string) // OSC 7 with a file URL for this host; absolute path. An empty one is ignored
	OnClipboard func([]byte) // OSC 52 set; non-empty, at most 1 MiB. An OSC 52 read is ignored
	OnBell      func()
	// OnAgent receives only the root records of OSC 7501 (empty id). A
	// record with a non-empty id (a subagent or background task, E4) is
	// dropped by the adapter, its clear included.
	OnAgent func(AgentReport)
	// OnReflow runs inside Resize after the engine reflowed. remap answers
	// for lines that were followed before the resize; for any other line,
	// and for a line whose new number would be below first (Range), ok is
	// false. A reflow never lowers first.
	OnReflow func(remap func(abs int64) (now int64, ok bool))
}

// Term is one terminal of the engine. It is not safe for concurrent use:
// one goroutine at a time calls it, and the pane actor is that goroutine.
type Term interface {
	Write(p []byte) // feed program output
	Resize(cols, rows int)
	Size() (cols, rows int)
	Replies() []byte                                // bytes to write to the PTY; drains the queue. Call after every Write and every Resize
	Row(y int) string                               // wire row (4.2.5) of live screen row y
	Line(abs int64) (row string, ok bool)           // wire row of an absolute line, history or screen
	Text(abs int64) (text string, wrapped, ok bool) // plain text; wrapped: continues on the next line
	Range() (first, base int64)                     // oldest retained line; line in screen row 0
	// Follow pins a history or screen line to its text, so that OnReflow
	// can say where it went. Each Follow needs one Unfollow.
	Follow(abs int64)
	Unfollow(abs int64)
	Cursor() Cursor
	Modes() Modes
	SyncActive() bool
	SetColors(c Colors)
	// Input. Each returns nil when there is nothing to send. The engine
	// reads its own modes; the caller passes none.
	EncodeKey(k Key) []byte                           // nil: unknown name, or a release the program did not ask for
	EncodeMouse(e Mouse) []byte                       // nil: the program did not ask for this event
	EncodePaste(text string, first, last bool) []byte // one chunk of a paste; first and last place the brackets of mode 2004
	EncodeFocus(focused bool) []byte                  // nil unless mode 1004
	Close()                                           // frees the engine's memory; the Term is unusable afterwards
}

// Key is one key event (4.2.6).
type Key struct {
	Name            string // 4.2.6
	Mod             int
	Text            string
	Shifted, Base   rune
	Repeat, Release bool
}

// Mouse is one mouse event (4.2.6), in cells of the pane.
type Mouse struct {
	Kind, Button string
	X, Y, Mod    int
}

// The functions of the contract that the build provides:
//
//	func New(o Options) Term
//	func SetIdentity(name, version, host string) // XTVERSION answer "devtools <version> <host>"; process-wide, once, before New
//	func Extract(t Term, from, to Pos, block bool, max int) (text string, cut bool) // text helpers over Term, used by the pane actor
//
// With cgo they are in the files of the engine (P1-02) and of the history
// (P1-20); without cgo, nocgo.go declares them so that the cross vet
// type-checks.
