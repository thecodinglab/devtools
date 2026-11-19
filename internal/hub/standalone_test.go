package hub

import (
	"bytes"
	"image/color"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/session"
	"devtools/internal/testkit"
	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

// lockedBuffer collects what the hub writes to the terminal.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// cooked reports whether the terminal f is in its cooked mode: canonical
// input and echo, as a freshly opened terminal and a shell's prompt have it.
func cooked(t *testing.T, f *os.File) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), getTermios)
	if err != nil {
		t.Fatalf("read the terminal's mode: %v", err)
	}
	return tio.Lflag&unix.ICANON != 0 && tio.Lflag&unix.ECHO != 0
}

func TestStandaloneReturnsThePick(t *testing.T) {
	for _, c := range []struct {
		name string
		keys string
		want string // the label of the picked row, "" for none
	}{
		{"enter returns the row", "\r", "devtools/main"},
		{"esc on an empty query returns nothing", "\x1b", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			master, slave := testkit.OpenPTY(t, 80, 24)
			if !cooked(t, slave) {
				t.Fatal("a new terminal is not in its cooked mode")
			}
			var screen lockedBuffer
			go func() {
				io.Copy(&screen, master)
			}()

			events := make(chan Event, 4)
			events <- Event{Kind: Items, Items: []Item{worktree("devtools", "main"), worktree("xcloud", "main")}}
			events <- Event{Kind: Complete}
			type result struct {
				item *Item
				err  error
			}
			done := make(chan result, 1)
			go func() {
				// the features of the tmux backend
				item, err := RunStandalone(ModelOptions{
					Events: events,
					Here:   session.Target{Dir: "/dev/devtools/main"},
				}, slave, slave)
				done <- result{item, err}
			}()

			testkit.Eventually(t, 10*time.Second, "the hub draws its rows in raw mode", func() bool {
				return strings.Contains(screen.String(), "xcloud") && !cooked(t, slave)
			})
			if _, err := master.Write([]byte(c.keys)); err != nil {
				t.Fatal(err)
			}
			var r result
			select {
			case r = <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("the hub did not end after %q; it drew:\n%q", c.keys, screen.String())
			}
			if r.err != nil {
				t.Fatalf("RunStandalone: %v", r.err)
			}
			switch {
			case c.want == "" && r.item != nil:
				t.Fatalf("picked %+v, want nothing", r.item)
			case c.want != "" && (r.item == nil || r.item.Label != c.want):
				t.Fatalf("picked %+v, want %s", r.item, c.want)
			}
			if !cooked(t, slave) {
				t.Fatal("the terminal is not back in its cooked mode")
			}
		})
	}
}

func TestTheStandaloneHubDrawsTheGlyphsOfTheLocale(t *testing.T) {
	for _, c := range []struct {
		name         string
		locale       string
		want, refuse string
	}{
		{"not UTF-8", "C", "enter switch", "›"},
		{"UTF-8", "en_US.UTF-8", "⏎ switch", "  > "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("LC_ALL", c.locale)
			master, slave := testkit.OpenPTY(t, 80, 24)
			var screen lockedBuffer
			go func() {
				io.Copy(&screen, master)
			}()
			events := make(chan Event, 2)
			events <- Event{Kind: Items, Items: []Item{worktree("devtools", "main"), worktree("xcloud", "main")}}
			events <- Event{Kind: Complete}
			done := make(chan error, 1)
			go func() {
				_, err := RunStandalone(ModelOptions{Events: events, Here: session.Target{Dir: "/dev/devtools/main"}}, slave, slave)
				done <- err
			}()
			testkit.Eventually(t, 10*time.Second, "the hub draws its footer", func() bool {
				return strings.Contains(screen.String(), "xcloud") && strings.Contains(screen.String(), "close")
			})
			if _, err := master.Write([]byte("\x1b")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("RunStandalone: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the hub did not close on esc")
			}
			drawn := ansi.Strip(screen.String())
			if !strings.Contains(drawn, c.want) || strings.Contains(drawn, c.refuse) {
				t.Fatalf("LC_ALL=%s: the hub drew %q, want %q and no %q", c.locale, drawn, c.want, c.refuse)
			}
		})
	}
}

func TestTheLocaleDecidesTheGlyphSet(t *testing.T) {
	for _, c := range []struct {
		env  []string
		utf8 bool
	}{
		{nil, false},
		{[]string{"LANG=C"}, false},
		{[]string{"LANG=C.UTF-8"}, true},
		{[]string{"LANG=de_DE.utf8"}, true},
		{[]string{"LANG=C.UTF-8", "LC_CTYPE=C"}, false},
		{[]string{"LANG=C", "LC_CTYPE=en_US.UTF-8"}, true},
		{[]string{"LC_CTYPE=en_US.UTF-8", "LC_ALL=POSIX"}, false},
		{[]string{"LC_ALL=", "LANG=en_GB.UTF-8"}, true},
	} {
		if got := utf8Locale(c.env); got != c.utf8 {
			t.Errorf("utf8Locale(%q) = %v, want %v", c.env, got, c.utf8)
		}
	}
}

func TestTheStandaloneHubTakesTheTerminalsAppearance(t *testing.T) {
	tokens := []ui.Token{{Name: "strong", Light: 0x102030, Dark: 0xa0b0c0}}
	theme := func(o ui.ThemeOptions) ui.Theme {
		t.Helper()
		o.Tokens, o.UTF8 = tokens, true
		th, err := ui.NewTheme(o)
		if err != nil {
			t.Fatal(err)
		}
		return th
	}
	s, err := newStandalone(ModelOptions{Features: Features{}, Here: session.Target{Dir: "/dev/devtools/main"}}, tokens, []string{"LANG=C.UTF-8"})
	if err != nil {
		t.Fatal(err)
	}
	s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s.Update(eventMsg{ev: Event{Kind: Items, Items: []Item{worktree("devtools", "main")}}})
	palette := theme(ui.ThemeOptions{Colors: 16})
	if s.m.theme != palette {
		t.Fatal("the first frame is not drawn in the palette tier")
	}
	// the strong role of the selected row in the token's value (5a.16.1)
	dark, light := "38;2;160;176;192", "38;2;16;32;48"
	for _, step := range []struct {
		name string
		msg  tea.Msg
		want ui.Theme
		rgb  string // in the view, "" for none
	}{
		{"24-bit colour without an answer", tea.ColorProfileMsg{Profile: colorprofile.TrueColor}, palette, ""},
		{"a dark background", tea.BackgroundColorMsg{Color: color.Black}, theme(ui.ThemeOptions{Dark: true, Known: true, Colors: 1 << 24}), dark},
		{"a light background", tea.BackgroundColorMsg{Color: color.White}, theme(ui.ThemeOptions{Known: true, Colors: 1 << 24}), light},
		{"256 colours", tea.ColorProfileMsg{Profile: colorprofile.ANSI256}, palette, ""},
	} {
		s.Update(step.msg)
		if s.m.theme != step.want {
			t.Fatalf("%s: the model has not the theme of the terminal", step.name)
		}
		view := s.m.View(80, 24)
		for _, rgb := range []string{dark, light} {
			if strings.Contains(view, rgb) != (rgb == step.rgb) {
				t.Fatalf("%s: the view has %s %v, want only %q:\n%q", step.name, rgb, strings.Contains(view, rgb), step.rgb, view)
			}
		}
	}
	// a focus gain asks for the background again
	if _, cmd := s.Update(tea.FocusMsg{}); cmd == nil || cmd() != tea.RequestBackgroundColor() {
		t.Fatal("a focus gain does not ask the terminal for its background")
	}
	if cmd := s.Init(); cmd == nil {
		t.Fatal("Init asks nothing")
	}
}

func TestTheStandaloneHubRefusesAnUnknownToken(t *testing.T) {
	tokens := []ui.Token{{Name: "accent", Light: 0x102030, Dark: 0xa0b0c0}}
	if _, err := newStandalone(ModelOptions{}, tokens, nil); err == nil || !strings.Contains(err.Error(), `"accent"`) {
		t.Fatalf("newStandalone with the token accent: %v, want an error that names it", err)
	}
	item, err := runStandalone(ModelOptions{}, tokens, nil, nil, nil)
	if item != nil || err == nil || !strings.Contains(err.Error(), `"accent"`) {
		t.Fatalf("runStandalone with the token accent = %v, %v; want no hub and an error that names it", item, err)
	}
}

// The color lines of the config reach the standalone hub through
// ModelOptions.Tokens (contract change 12): an unknown one stops
// RunStandalone before anything is drawn.
func TestRunStandaloneRefusesAnUnknownTokenOfTheOptions(t *testing.T) {
	master, slave := testkit.OpenPTY(t, 80, 24)
	var screen lockedBuffer
	go func() {
		io.Copy(&screen, master)
	}()
	events := make(chan Event, 2)
	events <- Event{Kind: Items, Items: []Item{worktree("devtools", "main")}}
	events <- Event{Kind: Complete}
	type result struct {
		item *Item
		err  error
	}
	done := make(chan result, 1)
	go func() {
		item, err := RunStandalone(ModelOptions{
			Events: events,
			Here:   session.Target{Dir: "/dev/devtools/main"},
			Tokens: []ui.Token{{Name: "accent", Light: 0x102030, Dark: 0xa0b0c0}},
		}, slave, slave)
		done <- result{item, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		// the hub started: close it so the test ends
		master.Write([]byte("\x1b"))
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
		t.Fatalf("RunStandalone with the token accent started the hub; it drew:\n%q", screen.String())
	}
	if r.item != nil || r.err == nil || !strings.Contains(r.err.Error(), `"accent"`) {
		t.Fatalf("RunStandalone with the token accent = %v, %v; want no hub and an error that names it", r.item, r.err)
	}
	if !cooked(t, slave) {
		t.Fatal("the terminal is not in its cooked mode after the refusal")
	}
}
