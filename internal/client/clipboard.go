package client

// The clipboard (3.2.4, 4.2.8): the client writes it with OSC 52 through its
// own terminal, for a copy of copy mode and for a program's event.clipboard
// of the displayed target (the fleet forwards it only from the link of what
// is shown, SEC15). The client never reads the clipboard and never writes
// an empty value.

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"devtools/internal/fleet"
	"devtools/internal/proto"
)

// programClipboard is an event.clipboard the fleet forwarded. One of a
// target that is not the one on screen any more is dropped.
func (m *Model) programClipboard(u fleet.Update) tea.Cmd {
	if m.view == "none" || u.Target.Machine != m.target.Machine || u.Target.Session != m.target.Session {
		return nil
	}
	data := u.Data
	if len(data) > proto.MaxClip {
		data = data[:proto.MaxClip]
	}
	return m.writeClipboard(string(data))
}

// writeClipboard sets the outer terminal's clipboard to text with OSC 52,
// past the renderer, once. Nothing is written for an empty text.
func (m *Model) writeClipboard(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	return tea.Raw(ansi.SetSystemClipboard(text))
}
