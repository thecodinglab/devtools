package ui

import (
	"fmt"
	"testing"
)

// glyphNames names every glyph for the messages of the tests below.
var glyphNames = [glyphCount]string{
	"here", "divider vertical", "divider horizontal", "pointer left", "pointer right",
	"pointer up", "pointer down", "blocked", "done", "working", "idle", "bell", "failed",
	"ellipsis", "prompt", "enter", "control", "ahead", "behind", "pad", "create",
	"selected",
}

func TestEveryGlyphIsOneCellWide(t *testing.T) {
	var th Theme
	for g := Glyph(0); g < glyphCount; g++ {
		s := th.Glyph(g)
		if s == "" {
			t.Errorf("glyph %s is empty", glyphNames[g])
			continue
		}
		if w := Width(s); w != 1 {
			t.Errorf("glyph %s %q is %d cells wide, want 1", glyphNames[g], s, w)
		}
		if n := len([]rune(s)); n != 1 {
			t.Errorf("glyph %s %q is %d code points, want 1", glyphNames[g], s, n)
		}
	}
}

// e9Record is the glyph table of 5a.7.2 as E9 recorded it in
// docs/replace-herdr/experiments/e3-client.md ("widths.txt": every glyph one
// cell in the engine with 2027 on and off, in both width functions of x/ansi
// and in the outer terminal's cursor reports). The planning directory is not
// in the worktree (6.1 rule 11), so the record is written out here.
//
// provisional E9: the half of E9 that prints the table in ghostty with the
// user's font on Linux and on a Mac is pending [GO]. A glyph that fails it
// is replaced in glyphs.go and here together.
var e9Record = map[Glyph]rune{
	GlyphHere:     0x258E,
	GlyphDividerV: 0x2502,
	GlyphDividerH: 0x2500,
	GlyphLeft:     0x25C2,
	GlyphRight:    0x25B8,
	GlyphUp:       0x25B4,
	GlyphDown:     0x25BE,
	GlyphBlocked:  0x25C6,
	GlyphDone:     0x2713,
	GlyphWorking:  0x25CF,
	GlyphIdle:     0x25CB,
	GlyphBell:     0x0021,
	GlyphFailed:   0x2717,
	GlyphEllipsis: 0x2026,
	GlyphPrompt:   0x203A,
	GlyphEnter:    0x23CE,
	GlyphControl:  0x2303,
	GlyphAhead:    0x2191,
	GlyphBehind:   0x2193,
	GlyphPad:      0x00B7,
	GlyphCreate:   0x002B,
	GlyphSelected: 0x258C,
}

func TestTheGlyphTableMatchesTheRecordOfE9(t *testing.T) {
	if len(e9Record) != int(glyphCount) {
		t.Fatalf("the record holds %d glyphs, the table %d", len(e9Record), glyphCount)
	}
	var th Theme
	for g := Glyph(0); g < glyphCount; g++ {
		want, ok := e9Record[g]
		if !ok {
			t.Errorf("glyph %s is not in the record of E9", glyphNames[g])
			continue
		}
		got := []rune(th.Glyph(g))
		if len(got) != 1 || got[0] != want {
			t.Errorf("glyph %s is %s, E9 recorded U+%04X", glyphNames[g], codePoints(th.Glyph(g)), want)
		}
	}
}

func codePoints(s string) string {
	out := ""
	for i, r := range s {
		if i > 0 {
			out += "+"
		}
		out += fmt.Sprintf("U+%04X", r)
	}
	return out
}

// asciiRecord is the ASCII column of the table of 5a.7.2.
var asciiRecord = [glyphCount]string{
	"|", "|", "-", "<", ">", "^", "v", "?", "+", "*", "-", "!", "x",
	"...", ">", "enter", "ctrl+", "^", "v", ".", "+", ">",
}

func TestTheASCIISetIsASCII(t *testing.T) {
	th, err := NewTheme(ThemeOptions{UTF8: false})
	if err != nil {
		t.Fatal(err)
	}
	for g := Glyph(0); g < glyphCount; g++ {
		s := th.Glyph(g)
		if s != asciiRecord[g] {
			t.Errorf("glyph %s in ASCII is %q, the table of 5a.7.2 says %q", glyphNames[g], s, asciiRecord[g])
		}
		if s == "" {
			t.Errorf("glyph %s in ASCII is empty", glyphNames[g])
		}
		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] >= 0x7f {
				t.Errorf("glyph %s in ASCII is %q, byte %d is not printable ASCII", glyphNames[g], s, i)
			}
		}
		if Width(s) != len(s) {
			t.Errorf("glyph %s in ASCII is %q, %d cells for %d bytes", glyphNames[g], s, Width(s), len(s))
		}
	}
}

func TestTheZeroThemeIsThePaletteTier(t *testing.T) {
	var zero Theme
	built, err := NewTheme(ThemeOptions{UTF8: true})
	if err != nil {
		t.Fatal(err)
	}
	if built != zero {
		t.Errorf("NewTheme with UTF-8 and nothing else is %+v, want the zero theme", built)
	}
	if zero.Glyph(GlyphHere) != "▎" {
		t.Errorf("the zero theme's here glyph is %q, want the UTF-8 set", zero.Glyph(GlyphHere))
	}
	for _, g := range []Ground{GroundRaised, GroundSelection} {
		if zero.HasGround(g) {
			t.Errorf("the zero theme has ground %d; the palette tier has none", g)
		}
	}
	if !zero.HasGround(GroundNone) {
		t.Error("the zero theme has no ground at all; the default ground is always there")
	}
	for r := RoleInk; r <= RoleElsewhere; r++ {
		for g := GroundNone; g <= GroundSelection; g++ {
			f := zero.Face(r, g)
			if f.Fg.Kind == PaintRGB || f.Bg.Kind == PaintRGB {
				t.Errorf("role %d on ground %d has an RGB colour in the palette tier: %+v", r, g, f)
			}
			if f.Bg.Kind != PaintDefault {
				t.Errorf("role %d on ground %d sets a background in the palette tier: %+v", r, g, f)
			}
		}
	}
	want := map[Role]Face{
		RoleInk:       {},
		RoleStrong:    {Bold: true},
		RoleMuted:     {},
		RoleComment:   {Fg: Paint{Kind: PaintPalette, V: 8}},
		RoleAccent:    {Fg: Paint{Kind: PaintPalette, V: 4}},
		RoleWarning:   {Fg: Paint{Kind: PaintPalette, V: 3}},
		RoleError:     {Fg: Paint{Kind: PaintPalette, V: 1}},
		RoleElsewhere: {Fg: Paint{Kind: PaintPalette, V: 5}},
	}
	for r, f := range want {
		if got := zero.Face(r, GroundNone); got != f {
			t.Errorf("role %d is %+v, want %+v (5a.7.1)", r, got, f)
		}
	}
	if f := zero.Face(RoleAccent, GroundSelection); f != (Face{Reverse: true}) {
		t.Errorf("accent on the selection ground is %+v, want reverse video in the default colours", f)
	}
	text, f := zero.Chip("macmini")
	if text != " macmini " || !f.Bold || f.Fg != (Paint{Kind: PaintPalette, V: 5}) || f.Bg.Kind != PaintDefault {
		t.Errorf("the chip is %q %+v, want \" macmini \" in bold elsewhere without a ground", text, f)
	}
}

func TestEveryTokenNameIsKnown(t *testing.T) {
	want := []string{"strong", "muted", "raised", "selection", "warning"}
	got := TokenNames()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("TokenNames() = %v, want %v", got, want)
	}
	got[0] = "changed"
	if TokenNames()[0] != "strong" {
		t.Error("TokenNames returns the table itself; a caller can change it")
	}
}
