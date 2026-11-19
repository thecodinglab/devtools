package ui

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const (
	family   = "\U0001F468‍\U0001F469‍\U0001F467" // man, ZWJ, woman, ZWJ, girl: one wide cluster
	combined = "é"                               // e and a combining acute: one cell
)

func TestCutEndAndCutMiddle(t *testing.T) {
	utf8 := mustTheme(t, ThemeOptions{UTF8: true})
	ascii := mustTheme(t, ThemeOptions{UTF8: false})
	cases := []struct {
		name string
		th   Theme
		cut  string // "end" or "middle"
		s    string
		w    int
		want string
	}{
		{"fits", utf8, "end", "macmini", 7, "macmini"},
		{"fits with room", utf8, "middle", "macmini", 20, "macmini"},
		{"ASCII end", utf8, "end", "devtools-main", 8, "devtool…"},
		{"ASCII middle", utf8, "middle", "devtools-main", 8, "devt…ain"},
		{"ASCII middle odd", utf8, "middle", "devtools-main", 9, "devt…main"},
		{"ASCII set end", ascii, "end", "devtools-main", 8, "devto..."},
		{"ASCII set middle", ascii, "middle", "devtools-main", 9, "dev...ain"},
		{"ASCII set, no room for the mark", ascii, "end", "devtools", 3, "dev"},
		{"ASCII set middle, no room", ascii, "middle", "devtools", 4, "d..."},
		{"width 0", utf8, "end", "macmini", 0, ""},
		{"width 0 middle", utf8, "middle", "macmini", 0, ""},
		{"negative width", utf8, "end", "macmini", -3, ""},
		{"width 1", utf8, "end", "macmini", 1, "m"},
		{"width 1 middle", utf8, "middle", "macmini", 1, "m"},
		{"width 2", utf8, "end", "macmini", 2, "m…"},
		{"width 2 middle", utf8, "middle", "macmini", 2, "m…"},
		{"width 3 middle", utf8, "middle", "macmini", 3, "m…i"},
		{"empty", utf8, "end", "", 5, ""},
		{"wide end, boundary inside a character", utf8, "end", "中文字符", 4, "中…"},
		{"wide end, boundary between characters", utf8, "end", "中文字符", 5, "中文…"},
		{"wide end at width 1", utf8, "end", "中文", 1, ""},
		{"wide end at width 2", utf8, "end", "中文", 2, "…"},
		{"wide middle", utf8, "middle", "中文字符", 5, "中…符"},
		{"wide middle, a wide character at both cuts", utf8, "middle", "中文字符串", 6, "中…串"},
		{"wide middle, the head gives the tail its cell", utf8, "middle", "ab中cdef", 6, "ab…def"},
		{"emoji sequence kept whole", utf8, "end", "ab" + family + "cd", 4, "ab…"},
		{"emoji sequence fits", utf8, "end", "ab" + family + "cd", 5, "ab" + family + "…"},
		{"combining mark kept with its letter", utf8, "end", "caf" + combined + "-main", 5, "caf" + combined + "…"},
		{"combining mark in the middle", utf8, "middle", "ab" + combined + "cdefg" + combined, 5, "ab…g" + combined},
		{"styled text keeps its escapes", utf8, "end", "\x1b[1mmacmini\x1b[m", 4, "\x1b[1mmac…\x1b[m"},
	}
	for _, c := range cases {
		var got string
		if c.cut == "end" {
			got = c.th.CutEnd(c.s, c.w)
		} else {
			got = c.th.CutMiddle(c.s, c.w)
		}
		if got != c.want {
			t.Errorf("%s: Cut%s(%q, %d) = %q, want %q", c.name, strings.ToUpper(c.cut[:1])+c.cut[1:], c.s, c.w, got, c.want)
		}
	}
}

func TestCutsNeverExceedTheWidthNorSplitACluster(t *testing.T) {
	inputs := []string{
		"devtools-main", "中文字符串中文", family + family + "x" + family, "a" + combined + combined + "bc",
		"x中y" + family + "z" + combined, strings.Repeat("m", 64),
	}
	for _, th := range []Theme{mustTheme(t, ThemeOptions{UTF8: true}), mustTheme(t, ThemeOptions{})} {
		for _, s := range inputs {
			parts := map[string]bool{}
			for _, c := range clusters(s) {
				parts[c.s] = true
			}
			for w := 0; w <= Width(s)+1; w++ {
				for name, got := range map[string]string{"CutEnd": th.CutEnd(s, w), "CutMiddle": th.CutMiddle(s, w)} {
					if Width(got) > w {
						t.Errorf("%s(%q, %d) = %q, %d cells", name, s, w, got, Width(got))
					}
					if w > 0 && Width(got) < w-1 && Width(s) >= w {
						t.Errorf("%s(%q, %d) = %q, %d cells: more than one cell lost", name, s, w, got, Width(got))
					}
					rest := strings.ReplaceAll(got, th.Glyph(GlyphEllipsis), "")
					for _, c := range clusters(rest) {
						if !parts[c.s] {
							t.Errorf("%s(%q, %d) = %q holds %q, which is no cluster of the input", name, s, w, got, c.s)
						}
					}
				}
			}
		}
	}
}

// TestWidthMatchesTheCanvas holds Width to the canvas's width function,
// which internal/ui cannot import (rule 6): ansi.StringWidth of the pinned
// x/ansi in grapheme mode (4.2.5), cluster by cluster as the canvas lays
// cells, with the widths report 09 proved for the engine with mode 2027.
func TestWidthMatchesTheCanvas(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"macmini", 7},
		{"中文", 4},
		{family, 2},
		{combined, 1},
		{"\U0001F1E9\U0001F1EA", 2}, // a flag: two regional indicators, one cluster
		{"❤️", 2},                   // a heart with the emoji selector
		{"\x1b[1;38;2;1;2;3mab\x1b[m", 2},
		{"◆ 1 needs you", 13},
	}
	for g := Glyph(0); g < glyphCount; g++ {
		cases = append(cases, struct {
			s    string
			want int
		}{utf8Glyphs[g], 1})
	}
	for _, c := range cases {
		if got := Width(c.s); got != c.want {
			t.Errorf("Width(%q) = %d, want %d", c.s, got, c.want)
		}
		if got, canvas := Width(c.s), ansi.StringWidth(c.s); got != canvas {
			t.Errorf("Width(%q) = %d, the canvas's function gives %d", c.s, got, canvas)
		}
		sum := 0
		for _, cl := range clusters(ansi.Strip(c.s)) {
			sum += cl.w
		}
		if sum != Width(c.s) {
			t.Errorf("Width(%q) = %d, its clusters take %d cells", c.s, Width(c.s), sum)
		}
	}
}

func TestNumber(t *testing.T) {
	cases := map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 7412: "7,412", 8113: "8,113",
		100000: "100,000", 1234567: "1,234,567", -1: "-1", -1000: "-1,000", -123456: "-123,456",
		math.MaxInt64: "9,223,372,036,854,775,807", math.MinInt64: "-9,223,372,036,854,775,808",
	}
	for n, want := range cases {
		if got := Number(n); got != want {
			t.Errorf("Number(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestMeasureAndCentre(t *testing.T) {
	for _, c := range []struct{ width, measure, x0 int }{
		{0, 0, 0}, {20, 20, 0}, {80, 80, 0}, {100, 100, 0}, {101, 100, 0}, {135, 100, 17}, {200, 100, 50}, {280, 100, 90}, {-5, 0, 0},
	} {
		if m := Measure(c.width); m != c.measure {
			t.Errorf("Measure(%d) = %d, want %d", c.width, m, c.measure)
		}
		if x := Centre(c.width, Measure(c.width)); x != c.x0 {
			t.Errorf("Centre(%d, %d) = %d, want %d", c.width, Measure(c.width), x, c.x0)
		}
	}
}
