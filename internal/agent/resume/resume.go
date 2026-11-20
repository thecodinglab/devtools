// Package resume brings the conversation of a coding agent back after a
// server restart. While an agent runs in a pane the server asks the agent's
// Resumer which conversation the agent process holds (Lookup) and saves the
// answer in state.json with the pane; when the restored pane starts, its
// shell runs the agent's Command for that conversation first.
//
// One Resumer per agent kind. An agent without one (Codex, so far) is
// tracked as before but not resumed. Adding one is a file implementing
// Resumer and its line in resumers.
//
// The package imports the standard library and internal/agent only. It
// reads the agents' own files; it runs nothing.
package resume

import (
	"devtools/internal/agent"
)

// Resumer is what the server needs to know of one kind of agent.
type Resumer interface {
	// Lookup returns the id of the conversation the agent process a holds,
	// ok false when that is not known (yet). It reads the file system and
	// runs off the server's loop.
	Lookup(a Process) (id string, ok bool)
	// Valid reports whether id has the form of a conversation id of this
	// agent. An id read back from state.json is used only when it is valid:
	// it becomes part of a command line.
	Valid(id string) bool
	// Command is the command that resumes conversation id, a valid id, as
	// its words. It is run by the pane's shell, so the first word is looked
	// up on the PATH the shell's startup files set.
	Command(id string) []string
}

// Process is one process of a running agent as the server sees it: what a
// Resumer can go by to find its conversation.
type Process struct {
	Pid  int
	Args []string // its arguments, argv[0] first; nil when unknown
	Cwd  string   // the directory of its pane
}

// resumers are the agents that can be resumed, by kind.
var resumers = map[agent.Kind]Resumer{
	agent.Claude: Claude{},
}

// For returns the Resumer of kind k, ok false when k cannot be resumed.
func For(k agent.Kind) (Resumer, bool) {
	r, ok := resumers[k]
	return r, ok
}

// isUUID reports whether s is a UUID in its 36-character text form, either
// case.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}
