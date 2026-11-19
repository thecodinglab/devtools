package hub

import (
	"os"
	"strings"

	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// RunStandalone runs the model as a full-screen program on in and out until
// a row is picked or the hub is closed. It returns the picked row, or nil
// when the hub was closed without a choice. It acts on nothing: the caller
// shows the pick.
//
// It is the second host of the model (3.10.4), for the tmux and herdr
// backends: in a pane, in a popup of that program, or in a plain terminal,
// on the alternate screen, with no status row under it (5a.6.7). When in is
// a terminal it is in raw mode while the hub runs and back in the mode it
// was in when RunStandalone returns.
//
// The host builds the hub's theme itself (5a.16.1), with ui.NewTheme from
// o.Tokens and the terminal it runs in: the locale of the process, the
// colour profile bubbletea detects, and the terminal's answer to the
// background query at start and on each focus gain. o.Theme is not used. An
// unknown token is an error that names it, and the hub does not start.
func RunStandalone(o ModelOptions, in, out *os.File) (*Item, error) {
	return runStandalone(o, o.Tokens, os.Environ(), in, out)
}

// runStandalone is RunStandalone with the color lines and the environment
// its theme is built from.
func runStandalone(o ModelOptions, tokens []ui.Token, env []string, in, out *os.File) (*Item, error) {
	s, err := newStandalone(o, tokens, env)
	if err != nil {
		return nil, err
	}
	p := tea.NewProgram(s,
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithEnvironment(env),
		// 120 frames a second, the most bubbletea allows, so the first
		// frame and everything that streams in after it show up sooner
		tea.WithFPS(120),
	)
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	s = final.(*standalone)
	if s.picked == nil {
		return nil, nil
	}
	item := s.picked.Item
	return &item, nil
}

// standalone is the bubbletea program around the model.
type standalone struct {
	m      *Model
	picked *Picked
	// look is what the theme is built from; theme is what it gave last
	look  ui.ThemeOptions
	theme ui.Theme
}

// newStandalone builds the host and the model with the theme of the inputs
// known before the terminal answers: the tokens and the locale. An unknown
// token is an error that names it, and the hub does not start.
func newStandalone(o ModelOptions, tokens []ui.Token, env []string) (*standalone, error) {
	s := &standalone{look: ui.ThemeOptions{Tokens: tokens, UTF8: utf8Locale(env), Colors: 16}}
	theme, err := ui.NewTheme(s.look)
	if err != nil {
		return nil, err
	}
	s.theme = theme
	o.Theme = theme
	s.m = New(o)
	return s, nil
}

// utf8Locale reports whether the locale of env is UTF-8: the first of
// LC_ALL, LC_CTYPE and LANG that is set decides, as it does for the C
// library; with none set the locale is C, which is not.
func utf8Locale(env []string) bool {
	vars := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := vars[name]; v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

func (s *standalone) Init() tea.Cmd {
	return tea.Batch(s.m.Init(), tea.RequestBackgroundColor)
}

func (s *standalone) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.ColorProfileMsg:
		s.look.Colors = profileColors(msg.Profile)
		return s, s.retheme()
	case tea.BackgroundColorMsg:
		s.look.Known, s.look.Dark = true, msg.IsDark()
		return s, s.retheme()
	case tea.FocusMsg:
		// the appearance may have changed while another window had focus
		return s, tea.RequestBackgroundColor
	}
	cmd, out := s.m.Update(msg)
	if out == nil {
		return s, cmd
	}
	if out.Switch != nil {
		s.picked = s.m.Picked()
	}
	return s, tea.Quit
}

// retheme builds the theme anew and hands it to the model when it changed.
// The tokens were checked when the host was built, so NewTheme cannot fail
// here; if it did, the theme stays as it was.
func (s *standalone) retheme() tea.Cmd {
	theme, err := ui.NewTheme(s.look)
	if err != nil || theme == s.theme {
		return nil
	}
	s.theme = theme
	cmd, _ := s.m.Update(ui.ThemeMsg{Theme: theme})
	return cmd
}

// profileColors is the Colors of ui.ThemeOptions for a colour profile: only
// "24-bit or not" is read (5a.16.1).
func profileColors(p colorprofile.Profile) int {
	switch p {
	case colorprofile.TrueColor:
		return 1 << 24
	case colorprofile.ANSI256:
		return 256
	}
	return 16
}

func (s *standalone) View() tea.View {
	view := fullScreen(s.m)
	// focus gains ask the terminal for its background again (5a.16.1)
	view.ReportFocus = true
	return view
}

// fullScreen is the model drawn on the alternate screen at the size of the
// last tea.WindowSizeMsg, with the terminal cursor in the input.
func fullScreen(m *Model) tea.View {
	view := tea.NewView(m.View(m.width, m.height))
	view.AltScreen = true
	if x, y, ok := m.Cursor(); ok {
		cursor := tea.NewCursor(x, y)
		cursor.Shape = tea.CursorBar
		cursor.Blink = false
		view.Cursor = cursor
	}
	return view
}
