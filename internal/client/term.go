package client

// What the client learns of the outer terminal (3.2.4, 5a.16.1): its focus,
// its foreground and background colours, asked at start and on every focus
// gain, and its colour profile. A changed colour answer goes to the fleet
// as view.colors and sets the light or dark tier of the theme.

import (
	"fmt"
	"image/color"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/proto"
	"devtools/internal/ui"
)

// colors is what the terminal answered.
type colors struct {
	fg, bg  string // "#rrggbb"; "" before an answer
	dark    bool
	known   bool   // a background answer came (ThemeOptions.Known)
	sent    string // the last view.colors sent, as "fg bg dark"
	profile int    // 16, 256 or 1<<24
}

// defaultFg is what the server assumes without an answer (4.2.1).
const defaultFg = "#d0d0d0"

// askColors asks the terminal for its foreground, then its background.
func (m *Model) askColors() tea.Cmd {
	return tea.Batch(tea.RequestForegroundColor, tea.RequestBackgroundColor)
}

// focus is a focus change of the outer terminal: the fleet hears it, and a
// gain asks for the colours again.
func (m *Model) focus(focused bool) tea.Cmd {
	if focused == m.focused {
		if focused {
			return m.askColors()
		}
		return nil
	}
	m.focused = focused
	m.fleet.Focus(focused)
	if focused {
		return m.askColors()
	}
	return nil
}

func (m *Model) foreground(msg tea.ForegroundColorMsg) tea.Cmd {
	if msg.Color == nil {
		return nil
	}
	m.colors.fg = hexColor(msg.Color)
	return m.colorsChanged()
}

func (m *Model) background(msg tea.BackgroundColorMsg) tea.Cmd {
	if msg.Color == nil {
		return nil
	}
	m.colors.bg = hexColor(msg.Color)
	dark := msg.IsDark()
	if !m.colors.known || dark != m.colors.dark {
		m.colors.known, m.colors.dark = true, dark
		if err := m.rebuildTheme(); err == nil {
			m.comp.full = true
		}
	}
	return tea.Batch(m.colorsChanged(), m.themeToOverlay())
}

// colorsChanged sends view.colors when the answer differs from what was
// sent; nothing before the background is known.
func (m *Model) colorsChanged() tea.Cmd {
	c := &m.colors
	if c.bg == "" {
		return nil
	}
	fg := c.fg
	if fg == "" {
		fg = defaultFg
	}
	key := fmt.Sprintf("%s %s %v", fg, c.bg, c.dark)
	if key == c.sent {
		return nil
	}
	c.sent = key
	m.fleet.Colors(proto.Encode(proto.ViewColors{Fg: fg, Bg: c.bg, Dark: c.dark}))
	return nil
}

// profile is the colour profile bubbletea detected; only "24-bit or not"
// changes the theme (5a.16.1).
func (m *Model) profile(msg tea.ColorProfileMsg) tea.Cmd {
	p := 16
	switch msg.Profile.String() {
	case "TrueColor":
		p = 1 << 24
	case "ANSI256":
		p = 256
	}
	if p == m.colors.profile {
		return nil
	}
	m.colors.profile = p
	if err := m.rebuildTheme(); err != nil {
		return nil
	}
	m.comp.full = true
	return m.themeToOverlay()
}

// themeToOverlay tells an open hub the theme.
func (m *Model) themeToOverlay() tea.Cmd {
	if m.overlay == nil {
		return nil
	}
	return m.toOverlay(ui.ThemeMsg{Theme: m.theme})
}

// hexColor is c as "#rrggbb".
func hexColor(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}
