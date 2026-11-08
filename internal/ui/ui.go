// Package ui holds the look devtools draws everything in: the switcher and
// the plain command output share its colours and its words for git and
// session state.
//
// The colours are terminal palette indices, not values, so they follow the
// terminal's own palette and with it a light/dark switch. The palette is
// quiet: names in the ink, context in the comment grey, a raised ground under
// the selected row. Blue only ever marks where you are, orange an agent that
// waits for you, red an error. A clean worktree says nothing at all, and
// every state is spelled out in words, so none of it rests on colour alone.
package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

var (
	// the base16 layout of tinted-shell: 0-15 the ansi colours, 16-21 the
	// slots that do not fit in sixteen
	Comment = lipgloss.Color("8")  // context, hints, punctuation
	Muted   = lipgloss.Color("20") // secondary data: changes, ahead/behind
	Strong  = lipgloss.Color("21") // the strong ink: names that lead a group
	Raised  = lipgloss.Color("19") // the ground of the selected row
	Accent  = lipgloss.Color("4")  // here
	Warning = lipgloss.Color("16") // wants you
	Error   = lipgloss.Color("1")  // failed
)

var (
	Plain       = lipgloss.NewStyle()
	CommentText = lipgloss.NewStyle().Foreground(Comment)
	MutedText   = lipgloss.NewStyle().Foreground(Muted)
	StrongText  = lipgloss.NewStyle().Foreground(Strong).Bold(true)
	AccentText  = lipgloss.NewStyle().Foreground(Accent)
	WarningText = lipgloss.NewStyle().Foreground(Warning)
	ErrorText   = lipgloss.NewStyle().Foreground(Error)
)

// HereMark sits in the gutter beside the worktree you are in.
const HereMark = "▎"

// Git is the state of a worktree as far as the lists show it.
type Git struct {
	Changes     int
	Ahead       int
	Behind      int
	HasUpstream bool
	Detached    bool
	Branch      string
}

// Changes is the count of uncommitted tracked changes, nothing when clean.
func Changes(git Git) string {
	if git.Changes == 0 {
		return ""
	}
	return fmt.Sprintf("%d changed", git.Changes)
}

// Sync is how far a branch is from its upstream, nothing when in step or
// without one.
func Sync(git Git) string {
	if !git.HasUpstream {
		return ""
	}
	var parts []string
	if git.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", git.Ahead))
	}
	if git.Behind > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", git.Behind))
	}
	return strings.Join(parts, " ")
}

// Session is the multiplexer side of a worktree: whether a workspace or
// session is open for it and what its agent is doing.
type Session struct {
	Open    bool
	Focused bool
	Agent   string // herdr agent status: blocked, done, idle, working, unknown
	Title   string // the agent's terminal title
}

// SessionState names a session's state in a word and the style to draw it
// in. An agent waiting on you is the one state that asks for attention.
func SessionState(session Session) (string, lipgloss.Style) {
	if !session.Open {
		return "", Plain
	}
	switch session.Agent {
	case "blocked":
		return "needs you", WarningText
	case "done":
		return "done", Plain
	case "working":
		return "working", MutedText
	case "idle":
		return "idle", CommentText
	}
	return "open", CommentText
}

// Truncate cuts s to width cells, marking the cut with an ellipsis.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// Pad fills s with spaces up to width cells.
func Pad(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}
