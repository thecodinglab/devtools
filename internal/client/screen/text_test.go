package screen

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestTextCutsAtACellBoundary(t *testing.T) {
	c := New(10, 1)
	if n := c.Text(0, 0, "ab日本", Style{}, 5); n != 4 {
		t.Errorf("width drawn = %d, want 4: the second wide character does not fit in 5", n)
	}
	if got := c.Lines()[0]; got != "ab日" {
		t.Errorf("row = %q", got)
	}
	checkInvariant(t, c)

	// the right edge cuts like maxW
	c = New(5, 1)
	if n := c.Text(2, 0, "x日本", Style{}, 100); n != 3 {
		t.Errorf("width drawn at the edge = %d, want 3", n)
	}
	if got := c.Lines()[0]; got != "  x日" {
		t.Errorf("row = %q", got)
	}

	// a cluster is never split: e + combining acute is one cell, and so is
	// a flag or an emoji with a skin tone
	c = New(10, 1)
	if n := c.Text(0, 0, "é👍🏽x", Style{}, 10); n != 4 {
		t.Errorf("width of clusters = %d, want 4", n)
	}
	if got := c.At(0, 0); got.S != "é" || got.W != 1 {
		t.Errorf("cell 0 = %+v", got)
	}
	if got := c.At(1, 0); got.S != "👍🏽" || got.W != 2 {
		t.Errorf("cell 1 = %+v", got)
	}

	// no room, no text
	if n := c.Text(0, 0, "abc", Style{}, 0); n != 0 {
		t.Errorf("maxW 0 drew %d", n)
	}
	if n := c.Text(0, 0, "abc", Style{}, -3); n != 0 {
		t.Errorf("negative maxW drew %d", n)
	}
}

func TestTextDropsControlCharacters(t *testing.T) {
	c := New(40, 1)
	n := c.Text(0, 0, "a\x1b[31mb\r\nc\td\x07e\x7ff\u0085g\u009bh\x00i\xffj", Style{}, 40)
	got := c.Lines()[0]
	want := "a[31mbcdefgh" + "i" + replacement + "j"
	if got != want {
		t.Errorf("row = %q, want %q", got, want)
	}
	if n != len([]rune(want)) {
		t.Errorf("width drawn = %d, want %d", n, len([]rune(want)))
	}
	// a lone combining mark has no cell
	c = New(5, 1)
	if n := c.Text(0, 0, "́‍", Style{}, 5); n != 0 {
		t.Errorf("zero-width clusters drew %d cells", n)
	}
}

func TestStyledReadsLipglossOutput(t *testing.T) {
	cases := []struct {
		name  string
		style lipgloss.Style
		want  Style
	}{
		{"bold", lipgloss.NewStyle().Bold(true), Style{Attrs: Bold}},
		{"faint italic", lipgloss.NewStyle().Faint(true).Italic(true), Style{Attrs: Faint | Italic}},
		{"reverse strike", lipgloss.NewStyle().Reverse(true).Strikethrough(true), Style{Attrs: Reverse | Strike}},
		{"underline", lipgloss.NewStyle().Underline(true), Style{Underline: 1}},
		{"ansi fg", lipgloss.NewStyle().Foreground(lipgloss.Color("1")), Style{Fg: Color{Kind: 1, V: 1}}},
		{"bright fg", lipgloss.NewStyle().Foreground(lipgloss.Color("8")), Style{Fg: Color{Kind: 1, V: 8}}},
		{"256 bg", lipgloss.NewStyle().Background(lipgloss.Color("236")), Style{Bg: Color{Kind: 2, V: 236}}},
		{"rgb fg on rgb bg", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e6e7eb")).Background(lipgloss.Color("#30323a")),
			Style{Attrs: Bold, Fg: Color{Kind: 3, V: 0xe6e7eb}, Bg: Color{Kind: 3, V: 0x30323a}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.style.Render("hi") + " x"
			c := New(10, 1)
			if n := c.Styled(0, 0, s, 10); n != 4 {
				t.Errorf("width of %q = %d, want 4", s, n)
			}
			for x := range 2 {
				if got := c.At(x, 0).Style; got != tc.want {
					t.Errorf("%q: cell %d style %+v, want %+v", s, x, got, tc.want)
				}
			}
			if got := c.At(3, 0); got.S != "x" || got.Style != (Style{}) {
				t.Errorf("%q: the text after the reset is %+v", s, got)
			}
		})
	}

	// relative SGR, colon forms and the default colours
	c := New(20, 1)
	c.Styled(0, 0, "\x1b[1;31ma\x1b[22mb\x1b[39;4:3mc\x1b[38:2::1:2:3;58:5:9;24md\x1b[0;48;5;7;49me", 20)
	want := []Style{
		{Attrs: Bold, Fg: Color{Kind: 1, V: 1}},
		{Fg: Color{Kind: 1, V: 1}},
		{Underline: 3},
		{Fg: Color{Kind: 3, V: 0x010203}, Ul: Color{Kind: 2, V: 9}},
		{},
	}
	for x, w := range want {
		if got := c.At(x, 0).Style; got != w {
			t.Errorf("cell %d (%q) style %+v, want %+v", x, c.At(x, 0).S, got, w)
		}
	}

	// cut like Text
	c = New(10, 1)
	if n := c.Styled(0, 0, "\x1b[1mab日", 3); n != 2 {
		t.Errorf("Styled cut at 3 drew %d, want 2", n)
	}
}

func TestStyledDropsEverySequenceThatIsNotSGR(t *testing.T) {
	inputs := []string{
		"\x1b[2Ja\x1b[Hb\x1b[10;10Hc",
		"\x1b[?1049ha\x1b[?25lb\x1b[>4;2mc",
		"\x1b]0;title\x07a\x1b]52;c;aGVsbG8=\x1b\\b\x1b]8;;https://example.com\x1b\\c\x1b]8;;\x1b\\",
		"\x1bPq#0;2;0;0;0\x1b\\a\x1b_apc\x1b\\b\x1b^pm\x1b\\c",
		"\x1b7a\x1b8b\x1bcc",
		"\x1b(Ba\x1b=b\x1b>c",
		"a\x07b\x08\rc",
		"a\x1b[2 qb\x1b[4 mc",
	}
	for _, in := range inputs {
		c := New(10, 1)
		n := c.Styled(0, 0, in, 10)
		if got := c.Lines()[0]; got != "abc" || n != 3 {
			t.Errorf("Styled(%q) drew %q (%d cells), want \"abc\"", in, got, n)
		}
		for x := range 3 {
			if st := c.At(x, 0).Style; st != (Style{}) {
				t.Errorf("Styled(%q): cell %d has style %+v", in, x, st)
			}
		}
	}
	// a sequence cut short by the end of the line draws nothing
	for _, in := range []string{"ab\x1b", "ab\x1b[1", "ab\x1b]8;;http", "ab\x1bP"} {
		c := New(10, 1)
		if n := c.Styled(0, 0, in, 10); n != 2 || c.Lines()[0] != "ab" {
			t.Errorf("Styled(%q) drew %q", in, c.Lines()[0])
		}
	}
}
