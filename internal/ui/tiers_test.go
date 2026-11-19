package ui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

var (
	roleNames   = [...]string{"ink", "strong", "muted", "comment", "accent", "warning", "error", "elsewhere"}
	groundNames = [...]string{"none", "raised", "selection"}
)

const lastRole, lastGround = RoleElsewhere, GroundSelection

// systemsTokens are the five color lines the systems config generates from
// palette.nix (4.6, 5a.7.1): the same values as the truecolor column of
// 5a.7.1, light then dark.
var systemsTokens = []Token{
	{Name: "muted", Light: 0x55585f, Dark: 0xa6a8b0},
	{Name: "raised", Light: 0xe3e4e8, Dark: 0x30323a},
	{Name: "selection", Light: 0xd6dde9, Dark: 0x3a4150},
	{Name: "strong", Light: 0x111214, Dark: 0xe6e7eb},
	{Name: "warning", Light: 0xb04a00, Dark: 0xec8a3f},
}

func mustTheme(t *testing.T, o ThemeOptions) Theme {
	t.Helper()
	th, err := NewTheme(o)
	if err != nil {
		t.Fatalf("NewTheme(%+v): %v", o, err)
	}
	return th
}

// everyTheme is one theme for every combination of the inputs of NewTheme
// that matters to the tier rule.
func everyTheme(t *testing.T) map[string]Theme {
	t.Helper()
	out := map[string]Theme{}
	tokenSets := map[string][]Token{
		"no tokens":   nil,
		"all tokens":  systemsTokens,
		"strong only": {{Name: "strong", Light: 0x111214, Dark: 0xe6e7eb}},
		"raised only": {{Name: "raised", Light: 0xe3e4e8, Dark: 0x30323a}},
	}
	for tn, toks := range tokenSets {
		for _, colors := range []int{0, 16, 256, 1 << 24} {
			for _, known := range []bool{false, true} {
				for _, dark := range []bool{false, true} {
					for _, utf8 := range []bool{false, true} {
						o := ThemeOptions{Tokens: toks, Dark: dark, Known: known, Colors: colors, UTF8: utf8}
						out[fmt.Sprintf("%s colors=%d known=%v dark=%v utf8=%v", tn, colors, known, dark, utf8)] = mustTheme(t, o)
					}
				}
			}
		}
	}
	return out
}

func hasRGB(th Theme) bool {
	for r := RoleInk; r <= lastRole; r++ {
		for g := GroundNone; g <= lastGround; g++ {
			f := th.Face(r, g)
			if f.Fg.Kind == PaintRGB || f.Bg.Kind == PaintRGB {
				return true
			}
		}
	}
	_, f := th.Chip("macmini")
	return f.Fg.Kind == PaintRGB || f.Bg.Kind == PaintRGB
}

func TestTierChoice(t *testing.T) {
	for _, colors := range []int{16, 256, 1 << 24} {
		for _, known := range []bool{false, true} {
			for _, dark := range []bool{false, true} {
				for _, withTokens := range []bool{false, true} {
					o := ThemeOptions{Dark: dark, Known: known, Colors: colors, UTF8: true}
					if withTokens {
						o.Tokens = systemsTokens
					}
					th := mustTheme(t, o)
					want := known && colors == 1<<24 && withTokens
					name := fmt.Sprintf("colors %d, known %v, dark %v, tokens %v", colors, known, dark, withTokens)
					if got := th.HasGround(GroundRaised); got != want {
						t.Errorf("%s: the raised ground is drawn %v, want %v", name, got, want)
					}
					if got := hasRGB(th); got != want {
						t.Errorf("%s: the theme returns RGB %v, want %v", name, got, want)
					}
					if !want {
						if th != (Theme{}) {
							t.Errorf("%s: the theme is %+v, want the palette tier (the zero theme)", name, th)
						}
						continue
					}
					strong, raised := uint32(0x111214), uint32(0xe3e4e8)
					if dark {
						strong, raised = 0xe6e7eb, 0x30323a
					}
					if f := th.Face(RoleStrong, GroundNone); f.Fg != (Paint{Kind: PaintRGB, V: strong}) || !f.Bold {
						t.Errorf("%s: strong is %+v, want bold #%06x", name, f, strong)
					}
					if f := th.Face(RoleInk, GroundRaised); f.Bg != (Paint{Kind: PaintRGB, V: raised}) {
						t.Errorf("%s: ink on raised is %+v, want the ground #%06x", name, f, raised)
					}
				}
			}
		}
	}
}

func TestUnknownTokenIsAnError(t *testing.T) {
	_, err := NewTheme(ThemeOptions{Tokens: []Token{{Name: "muted", Light: 1, Dark: 2}, {Name: "accent", Light: 1, Dark: 2}}, Known: true, Colors: 1 << 24})
	if err == nil {
		t.Fatal("the token accent was accepted; accent is a palette colour in both tiers")
	}
	if !strings.Contains(err.Error(), `"accent"`) {
		t.Errorf("the error %q does not name the token", err)
	}
	// The token is checked whatever the terminal: a config line is wrong
	// on every terminal or on none.
	if _, err := NewTheme(ThemeOptions{Tokens: []Token{{Name: "Strong"}}}); err == nil {
		t.Error("the token Strong was accepted in the palette tier; token names are lower case")
	}
	if _, err := NewTheme(ThemeOptions{Tokens: []Token{{Name: "raised", Light: 0x1000000}}}); err == nil || !strings.Contains(err.Error(), "raised") {
		t.Errorf("a value beyond 0xFFFFFF gave %v, want an error that names raised", err)
	}
	for _, name := range TokenNames() {
		if _, err := NewTheme(ThemeOptions{Tokens: []Token{{Name: name, Light: 0xffffff, Dark: 0}}}); err != nil {
			t.Errorf("the token %s is refused: %v", name, err)
		}
	}
}

func TestNoRGBWithoutTwentyFourBitColour(t *testing.T) {
	for name, th := range everyTheme(t) {
		if strings.Contains(name, "colors=16777216 known=true") {
			continue
		}
		if hasRGB(th) {
			t.Errorf("%s: a face holds an RGB colour", name)
		}
		for r := RoleInk; r <= lastRole; r++ {
			for g := GroundNone; g <= lastGround; g++ {
				out := th.Style(r, g).Render("x")
				if strings.Contains(out, "38;2;") || strings.Contains(out, "48;2;") || strings.Contains(out, "38;5;") || strings.Contains(out, "48;5;") {
					t.Errorf("%s: %s on %s renders %q", name, roleNames[r], groundNames[g], out)
				}
			}
		}
	}
}

func TestARoleWithoutATokenStaysPalette(t *testing.T) {
	palette := mustTheme(t, ThemeOptions{UTF8: true})
	only := mustTheme(t, ThemeOptions{Tokens: []Token{{Name: "strong", Light: 0x111214, Dark: 0xe6e7eb}}, Dark: true, Known: true, Colors: 1 << 24, UTF8: true})
	if f := only.Face(RoleStrong, GroundNone); f.Fg.Kind != PaintRGB {
		t.Fatalf("strong with its token is %+v, want RGB", f)
	}
	for _, r := range []Role{RoleInk, RoleMuted, RoleComment, RoleAccent, RoleWarning, RoleError, RoleElsewhere} {
		for g := GroundNone; g <= lastGround; g++ {
			if got, want := only.Face(r, g), palette.Face(r, g); got != want {
				t.Errorf("%s on %s with only the strong token is %+v, want the palette tier's %+v", roleNames[r], groundNames[g], got, want)
			}
		}
	}
	if only.HasGround(GroundRaised) || only.HasGround(GroundSelection) {
		t.Error("without the raised and selection tokens a ground is drawn; the palette-tier treatment of 5a.7.1 is due")
	}
	if text, f := only.Chip("macmini"); text != " macmini " || f.Bg.Kind != PaintDefault {
		t.Errorf("the chip without the raised token is %q %+v, want it without a ground", text, f)
	}
	all := mustTheme(t, ThemeOptions{Tokens: systemsTokens, Dark: true, Known: true, Colors: 1 << 24, UTF8: true})
	for _, r := range []Role{RoleInk, RoleComment, RoleAccent, RoleError, RoleElsewhere} {
		if got, want := all.Face(r, GroundNone), palette.Face(r, GroundNone); got != want {
			t.Errorf("%s with every token is %+v, want the palette colour %+v in both tiers", roleNames[r], got, want)
		}
	}
	raisedOnly := mustTheme(t, ThemeOptions{Tokens: []Token{{Name: "raised", Light: 0xe3e4e8, Dark: 0x30323a}}, Known: true, Colors: 1 << 24})
	if !raisedOnly.HasGround(GroundRaised) || raisedOnly.HasGround(GroundSelection) {
		t.Error("with only the raised token, want the raised ground drawn and the selection ground not")
	}
	if f := raisedOnly.Face(RoleAccent, GroundSelection); f != (Face{Reverse: true}) {
		t.Errorf("accent on selection without its token is %+v, want reverse video", f)
	}
}

func TestChipHasTheSameWidthInBothTiers(t *testing.T) {
	truecolor := mustTheme(t, ThemeOptions{Tokens: systemsTokens, Dark: true, Known: true, Colors: 1 << 24, UTF8: true})
	for name, th := range everyTheme(t) {
		for _, machine := range []string{"m", "macmini", "macbookpro", "日本", strings.Repeat("a", 64)} {
			text, f := th.Chip(machine)
			if Width(text) != Width(machine)+2 {
				t.Errorf("%s: the chip of %q is %q, %d cells; want %d", name, machine, text, Width(text), Width(machine)+2)
			}
			if !f.Bold {
				t.Errorf("%s: the chip of %q is not bold", name, machine)
			}
			want, _ := truecolor.Chip(machine)
			if Width(text) != Width(want) {
				t.Errorf("%s: the chip of %q is %d cells, in the truecolor tier %d", name, machine, Width(text), Width(want))
			}
		}
	}
	text, f := truecolor.Chip("macmini")
	if text != " macmini " || f.Bg != (Paint{Kind: PaintRGB, V: 0x30323a}) || f.Fg != (Paint{Kind: PaintPalette, V: 5}) || !f.Bold {
		t.Errorf("the truecolor chip is %q %+v, want \" macmini \" in bold elsewhere on the raised ground", text, f)
	}
}

// appearance is the test data of TestContrastOfEveryPairInUse: the
// terminal's default colours and the palette colours the theme can return,
// as hex values, for one appearance of palette.nix.
//
// The tokens are the values of 5a.7.1, which the systems config generates
// from palette.nix. palette.nix itself lives in the systems repository,
// which no agent reads or touches (6.1 rule 6), so the default colours and
// palette colours 1, 4 and 8 here are fixtures derived from the contrast
// table of 5a.13: each reproduces every ratio of its row to two decimals
// (TestTheContrastFixtureReproducesTheTable). WCAG contrast depends on
// luminance only, so a fixture with the right luminance proves the same
// pairs as the real colour. The table has no row for palette 3, which the
// palette tier draws warning with: it stands in with the warning token's
// value (provisional: palette 3 of palette.nix is not measured, 5a.13 says
// the palette colours are the theme's business). Palette 5, which the
// elsewhere role is drawn with in both tiers, is base0E of palette.nix
// itself: purple, 7.65 and 6.49 on the two grounds.
type appearance struct {
	name              string
	dark              bool
	bg, ink           uint32
	palette           map[uint32]uint32 // palette index to hex
	table             map[Role][3]float64
	raised, selection uint32
}

var appearances = []appearance{
	{
		name: "dark", dark: true, bg: 0x1b1c20, ink: 0xc8c4c7,
		palette: map[uint32]uint32{8: 0x908c8f, 4: 0xb0adb0, 1: 0x8d8c90, 3: 0xec8a3f, 5: 0xb9a2ec},
		raised:  0x30323a, selection: 0x3a4150,
		table: map[Role][3]float64{
			RoleInk: {9.87, 7.41, 5.93}, RoleStrong: {13.78, 10.34, 8.28}, RoleMuted: {7.17, 5.39, 4.31},
			RoleComment: {5.13, 3.85, 3.09}, RoleAccent: {7.66, 5.75, 4.61}, RoleWarning: {6.71, 5.04, 4.03},
			RoleError: {5.10, 3.83, 3.06}, RoleElsewhere: {7.65, 5.75, 4.60},
		},
	},
	{
		name: "light", dark: false, bg: 0xfefafd, ink: 0x3e3a3e,
		palette: map[uint32]uint32{8: 0x6e6a6c, 4: 0x605c60, 1: 0x686569, 3: 0xb04a00, 5: 0x6a45b5},
		raised:  0xe3e4e8, selection: 0xd6dde9,
		table: map[Role][3]float64{
			RoleInk: {10.80, 8.79, 8.18}, RoleStrong: {18.12, 14.75, 13.73}, RoleMuted: {6.89, 5.61, 5.22},
			RoleComment: {5.15, 4.20, 3.90}, RoleAccent: {6.35, 5.17, 4.81}, RoleWarning: {5.31, 4.32, 4.02},
			RoleError: {5.56, 4.52, 4.21}, RoleElsewhere: {6.49, 5.28, 4.91},
		},
	},
}

func linear(c uint32) float64 {
	x := float64(c) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

func luminance(h uint32) float64 {
	return 0.2126*linear(h>>16&0xff) + 0.7152*linear(h>>8&0xff) + 0.0722*linear(h&0xff)
}

// contrast is the WCAG 2 contrast ratio of two colours.
func contrast(a, b uint32) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// hex resolves a paint against an appearance; fg tells which default
// colour Paint{} stands for.
func (a appearance) hex(t *testing.T, p Paint, fg bool) uint32 {
	t.Helper()
	switch p.Kind {
	case PaintDefault:
		if fg {
			return a.ink
		}
		return a.bg
	case PaintRGB:
		return p.V
	case PaintPalette:
		if v, ok := a.palette[p.V]; ok {
			return v
		}
	}
	t.Fatalf("%s: no test value for the paint %+v", a.name, p)
	return 0
}

func (a appearance) token(t *testing.T, r Role) uint32 {
	for _, tok := range systemsTokens {
		if tok.Name == roleNames[r] {
			if a.dark {
				return tok.Dark
			}
			return tok.Light
		}
	}
	return a.hex(t, paletteFg[r], true)
}

func TestTheContrastFixtureReproducesTheTable(t *testing.T) {
	for _, a := range appearances {
		for r := RoleInk; r <= lastRole; r++ {
			fg := a.token(t, r)
			for i, bg := range []uint32{a.bg, a.raised, a.selection} {
				got, want := contrast(fg, bg), a.table[r][i]
				if math.Abs(got-want) > 0.015 {
					t.Errorf("%s: %s on %s is %.3f, the table of 5a.13 says %.2f", a.name, roleNames[r], groundNames[i], got, want)
				}
			}
		}
	}
}

func TestContrastOfEveryPairInUse(t *testing.T) {
	for _, a := range appearances {
		themes := map[string]Theme{
			"truecolor": mustTheme(t, ThemeOptions{Tokens: systemsTokens, Dark: a.dark, Known: true, Colors: 1 << 24, UTF8: true}),
			"palette":   mustTheme(t, ThemeOptions{Tokens: systemsTokens, Dark: a.dark, Known: true, Colors: 256, UTF8: true}),
		}
		for tier, th := range themes {
			faces := map[string]Face{}
			for r := RoleInk; r <= lastRole; r++ {
				for g := GroundNone; g <= lastGround; g++ {
					faces[roleNames[r]+" on "+groundNames[g]] = th.Face(r, g)
				}
			}
			_, chip := th.Chip("macmini")
			faces["the chip"] = chip
			for name, f := range faces {
				fg, bg := a.hex(t, f.Fg, true), a.hex(t, f.Bg, false)
				if f.Reverse {
					fg, bg = a.hex(t, f.Bg, false), a.hex(t, f.Fg, true)
				}
				need := 4.5
				if f.Bold {
					need = 3
				}
				if c := contrast(fg, bg); c < need {
					t.Errorf("%s, %s tier: %s is #%06x on #%06x, contrast %.2f, need %.1f", a.name, tier, name, fg, bg, c, need)
				}
			}
		}
	}
}

func TestRaisedAndSelectionFollowTheRulesOfAccessibility(t *testing.T) {
	th := mustTheme(t, ThemeOptions{Tokens: systemsTokens, Dark: false, Known: true, Colors: 1 << 24, UTF8: true})
	ink, muted := th.Face(RoleInk, GroundNone).Fg, th.Face(RoleMuted, GroundNone).Fg
	if f := th.Face(RoleComment, GroundRaised); f.Fg != muted {
		t.Errorf("comment on raised is %+v, want muted", f)
	}
	if f := th.Face(RoleWarning, GroundRaised); !f.Bold || f.Fg.Kind != PaintRGB {
		t.Errorf("warning on raised is %+v, want bold warning", f)
	}
	if f := th.Face(RoleError, GroundRaised); !f.Bold || f.Fg != ink {
		t.Errorf("error on raised is %+v, want bold ink", f)
	}
	for r := RoleInk; r <= lastRole; r++ {
		f := th.Face(r, GroundSelection)
		if f.Fg != ink || f.Bg != (Paint{Kind: PaintRGB, V: 0xd6dde9}) {
			t.Errorf("%s on selection is %+v, want ink on the selection ground", roleNames[r], f)
		}
	}
}

func TestReversedCellDrawsEveryRoleAsInk(t *testing.T) {
	for name, th := range everyTheme(t) {
		for r := RoleInk; r <= lastRole; r++ {
			for g := GroundNone; g <= lastGround; g++ {
				f := th.Face(r, g)
				if !f.Reverse {
					continue
				}
				if f.Fg != (Paint{}) || f.Bg != (Paint{}) {
					t.Errorf("%s: %s on %s is reversed with colours %+v", name, roleNames[r], groundNames[g], f)
				}
				if f.Bold != (r == RoleStrong) {
					t.Errorf("%s: %s on %s is reversed with bold %v", name, roleNames[r], groundNames[g], f.Bold)
				}
			}
		}
		if !th.HasGround(GroundSelection) {
			for r := RoleInk; r <= lastRole; r++ {
				if f := th.Face(r, GroundSelection); !f.Reverse {
					t.Errorf("%s: %s on selection without its ground is %+v, want reverse video", name, roleNames[r], f)
				}
			}
		}
	}
}

// sgrFace reads the SGR of one styled "x" back into a face. It knows the
// parameters a theme can produce and fails on any other.
func sgrFace(t *testing.T, out string) Face {
	t.Helper()
	var f Face
	if out == "x" {
		return f
	}
	if !strings.HasPrefix(out, "\x1b[") {
		t.Fatalf("styled output %q does not start with CSI", out)
	}
	end := strings.IndexByte(out, 'm')
	if end < 0 || !strings.HasPrefix(out[end+1:], "x") {
		t.Fatalf("styled output %q is not one SGR and the text", out)
	}
	if rest := out[end+2:]; rest != "\x1b[m" && rest != "\x1b[0m" {
		t.Fatalf("styled output %q does not end with a reset", out)
	}
	ps := strings.Split(out[2:end], ";")
	num := func(i int) uint32 {
		if i >= len(ps) {
			t.Fatalf("styled output %q is cut short", out)
		}
		n, err := strconv.Atoi(ps[i])
		if err != nil || n < 0 || n > 255 {
			t.Fatalf("styled output %q has parameter %q", out, ps[i])
		}
		return uint32(n)
	}
	for i := 0; i < len(ps); i++ {
		switch p := ps[i]; {
		case p == "1":
			f.Bold = true
		case p == "3":
			f.Italic = true
		case p == "4" || p == "4:1":
			f.Underline = true
		case p == "7":
			f.Reverse = true
		case p == "38" || p == "48":
			if num(i+1) != 2 {
				t.Fatalf("styled output %q uses a colour that is neither palette nor RGB", out)
			}
			v := Paint{Kind: PaintRGB, V: num(i+2)<<16 | num(i+3)<<8 | num(i+4)}
			if p == "38" {
				f.Fg = v
			} else {
				f.Bg = v
			}
			i += 4
		default:
			n := num(i)
			switch {
			case n >= 30 && n <= 37:
				f.Fg = Paint{Kind: PaintPalette, V: n - 30}
			case n >= 90 && n <= 97:
				f.Fg = Paint{Kind: PaintPalette, V: n - 90 + 8}
			case n >= 40 && n <= 47:
				f.Bg = Paint{Kind: PaintPalette, V: n - 40}
			case n >= 100 && n <= 107:
				f.Bg = Paint{Kind: PaintPalette, V: n - 100 + 8}
			default:
				t.Fatalf("styled output %q has the unknown parameter %q", out, p)
			}
		}
	}
	return f
}

func TestStyleAndFaceAgree(t *testing.T) {
	for name, th := range everyTheme(t) {
		for r := RoleInk; r <= lastRole; r++ {
			for g := GroundNone; g <= lastGround; g++ {
				want := th.Face(r, g)
				if got := sgrFace(t, th.Style(r, g).Render("x")); got != want {
					t.Errorf("%s: %s on %s: the style encodes %+v, the face is %+v", name, roleNames[r], groundNames[g], got, want)
				}
			}
		}
	}
	// faceStyle also carries what the callers add to a face (the copy
	// cursor's underline, the stale rows' italic).
	extra := Face{Fg: Paint{Kind: PaintPalette, V: 12}, Bg: Paint{Kind: PaintRGB, V: 0x010203}, Italic: true, Underline: true}
	if got := sgrFace(t, faceStyle(extra).Render("x")); got != extra {
		t.Errorf("faceStyle(%+v) encodes %+v", extra, got)
	}
}
