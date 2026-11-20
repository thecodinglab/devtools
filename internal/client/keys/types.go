// Package keys is the key table and the prefix machine of the client
// (5.3.7, 5a.11 of the plan).
//
// This file is a contract (6.2, P1-00b) and is read-only for P1-09, which
// writes Table, Machine and their functions in place of stub.go.
package keys

// Event is one key event from the outer terminal.
type Event struct {
	Name            string // key name or one character, as in 4.2.6
	Mod             int
	Text            string
	Release, Repeat bool
}

// Action is one entry of the closed action vocabulary with its argument.
type Action struct{ Name, Arg string }

// Context is what the machine knows of the focused pane.
type Context struct {
	Program string // of the focused pane, for display
	Editor  bool   // the pane field `editor` (4.2.2): the vim rule forwards the focus keys when it is true
}

// Verdict is what the client does with a key.
type Verdict int

const (
	Forward    Verdict = iota // send the key to the pane
	Act                       // run Result.Action
	Swallow                   // consumed, nothing happens
	SendPrefix                // forward the prefix chord itself
)

// Result is the answer of Machine.Handle.
type Result struct {
	Verdict Verdict
	Action  Action
	Pending bool // the prefix is waiting for a key
	Unbound bool // the key after the prefix has no binding: Verdict is Swallow, the prefix has ended, and the client shows the notice "No binding for {key}"
	More    bool // "?" after the prefix (built in, like esc): Verdict is Swallow, Pending stays true, and the client sets State.Hints and raises State.HintPage
	Repeat  bool // Action is a resize that may repeat: the client calls EndRepeat RepeatTime after the last such result
}
