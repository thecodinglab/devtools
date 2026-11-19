package client

// The internal tests of the model, and what the tests of package client_test
// read of the model's inside (this file is compiled into the same test
// binary, so client_test can call these).

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/chrome"
	"devtools/internal/client/screen"
	"devtools/internal/config"
)

// RunForTest is Run with the terminal check replaced and extra options for
// the program (a window size, since a pipe has none).
func RunForTest(o Options, isTerminal func(*os.File) bool, extra ...tea.ProgramOption) error {
	return run(o, runConfig{isTerminal: isTerminal, options: extra})
}

// ChromeStateOf is what the model gives the chrome now.
func ChromeStateOf(m *Model) chrome.State { return m.chromeState() }

// CanvasOf is the model's canvas.
func CanvasOf(m *Model) *screen.Canvas { return m.canvas }

// StatusDrawsOf counts how often the status row was drawn.
func StatusDrawsOf(m *Model) int { return m.comp.statusDraws }

func TestPasteChunksNeverSplitARune(t *testing.T) {
	s := strings.Repeat("aé€😀", 1000)
	for _, n := range []int{4, 5, 6, 7, 64} {
		var joined strings.Builder
		cs := chunks(s, n)
		for i, c := range cs {
			if len(c.text) > n && utf8.RuneCountInString(c.text) > 1 {
				t.Fatalf("n=%d: chunk %d has %d bytes", n, i, len(c.text))
			}
			if !utf8.ValidString(c.text) {
				t.Fatalf("n=%d: chunk %d splits a rune: %q", n, i, c.text)
			}
			if c.more != (i < len(cs)-1) {
				t.Fatalf("n=%d: chunk %d more=%v of %d", n, i, c.more, len(cs))
			}
			joined.WriteString(c.text)
		}
		if joined.String() != s {
			t.Fatalf("n=%d: the chunks do not join to the paste", n)
		}
	}
}

func TestTheLocaleDecidesTheGlyphSet(t *testing.T) {
	for _, c := range []struct {
		env  []string
		want bool
	}{
		{nil, false},
		{[]string{"LANG=C.UTF-8"}, true},
		{[]string{"LANG=en_US.utf8"}, true},
		{[]string{"LANG=C"}, false},
		{[]string{"LC_ALL=C", "LANG=C.UTF-8"}, false},
		{[]string{"LC_CTYPE=de_DE.UTF-8", "LANG=C"}, true},
	} {
		if got := utf8Locale(c.env); got != c.want {
			t.Errorf("utf8Locale(%q) = %v, want %v", c.env, got, c.want)
		}
	}
}

func TestAnUnknownColourTokenIsAnErrorOfNewModel(t *testing.T) {
	_, err := NewModel(Options{Colors: []config.Color{{Token: "nonsense", Light: "#000000", Dark: "#ffffff", Line: 3}}}, Hooks{})
	if err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("NewModel with an unknown token: %v, want an error naming it", err)
	}
	_, err = NewModel(Options{Colors: []config.Color{{Token: "strong", Light: "red", Dark: "#ffffff", Line: 4}}}, Hooks{})
	if err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Fatalf("NewModel with a malformed colour: %v, want an error naming the line", err)
	}
}
