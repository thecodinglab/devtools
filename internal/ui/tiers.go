package ui

// The two colour tiers (5a.7.1, 5a.13, 5a.16.1): the tier rule, token
// validation, and the face of every role on every ground in both tiers.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// trueColour is the value of ThemeOptions.Colors for a terminal that
// reported 24-bit colour. Only "24-bit or not" is read (5a.16.1).
const trueColour = 1 << 24

// NewTheme builds the theme for o. The palette tier is used unless Known is
// true, Colors is 1<<24 and at least one token is set (5a.16.1); then a role
// with a token is drawn from the token's value for the terminal's
// appearance, and a role without one stays as in the palette tier. An
// unknown token name, or a value that is not 0xRRGGBB, is an error that
// names the token: devtools prints it and does not start. When a token is
// given twice the last line wins, as later config lines do.
func NewTheme(o ThemeOptions) (Theme, error) {
	t := Theme{ascii: !o.UTF8}
	var rgb [tokenCount]uint32
	var has [tokenCount]bool
	for _, tok := range o.Tokens {
		i := tokenIndex(tok.Name)
		if i < 0 {
			return Theme{}, fmt.Errorf("color: unknown token %q (the tokens are %s)", tok.Name, strings.Join(tokenNames[:], ", "))
		}
		for _, v := range [2]uint32{tok.Light, tok.Dark} {
			if v > 0xFFFFFF {
				return Theme{}, fmt.Errorf("color %s: %#x is not a colour 0xRRGGBB", tok.Name, v)
			}
		}
		has[i] = true
		rgb[i] = tok.Light
		if o.Dark {
			rgb[i] = tok.Dark
		}
	}
	if o.Known && o.Colors == trueColour && len(o.Tokens) > 0 {
		// Only the truecolor tier keeps the tokens: every palette-tier theme
		// with the same glyph set is the same value, the zero value included.
		t.truecolor, t.rgb, t.has = true, rgb, has
	}
	return t, nil
}

// paletteFg is the foreground of each role in the palette tier (5a.7.1).
// accent, error and comment are palette colours in both tiers.
var paletteFg = [...]Paint{
	RoleInk:     {},
	RoleStrong:  {},
	RoleMuted:   {},
	RoleComment: {Kind: PaintPalette, V: 8},
	RoleAccent:  {Kind: PaintPalette, V: 4},
	RoleWarning: {Kind: PaintPalette, V: 3},
	RoleError:   {Kind: PaintPalette, V: 1},
}

// roleToken is the token that can give a role an RGB value, or -1.
var roleToken = [...]int{
	RoleInk:     -1,
	RoleStrong:  tokenStrong,
	RoleMuted:   tokenMuted,
	RoleComment: -1,
	RoleAccent:  -1,
	RoleWarning: tokenWarning,
	RoleError:   -1,
}

// token is the paint of token i, and whether the theme draws it. Outside
// the truecolor tier no token is drawn, so no RGB value ever leaves the
// theme for a terminal that did not report 24-bit colour.
func (t Theme) token(i int) (Paint, bool) {
	if i < 0 || !t.truecolor || !t.has[i] {
		return Paint{}, false
	}
	return Paint{Kind: PaintRGB, V: t.rgb[i]}, true
}

// fg is the foreground of role r on the default ground.
func (t Theme) fg(r Role) Paint {
	if int(r) >= len(paletteFg) {
		return Paint{}
	}
	if p, ok := t.token(roleToken[r]); ok {
		return p
	}
	return paletteFg[r]
}

// Face is the style of a cell of role r on ground g.
//
// On the default ground each role has its colour; strong is always bold.
// The raised ground, where the theme has it, follows 5a.13: comment becomes
// muted, warning is bold, error is bold ink. On the selection ground only
// ink is drawn. A ground the theme does not have (HasGround false) is drawn
// by the caller's treatment of 5a.7.1: the raised ground then has the faces
// of the default ground, and the selection ground is reverse video in which
// every role is the default foreground with its bold kept, so no pair of
// unknown colours is ever combined.
func (t Theme) Face(r Role, g Ground) Face {
	f := Face{Fg: t.fg(r), Bold: r == RoleStrong}
	switch g {
	case GroundRaised:
		bg, ok := t.token(tokenRaised)
		if !ok {
			return f
		}
		f.Bg = bg
		switch r {
		case RoleComment:
			f.Fg = t.fg(RoleMuted)
		case RoleWarning:
			f.Bold = true
		case RoleError:
			f.Fg, f.Bold = t.fg(RoleInk), true
		}
	case GroundSelection:
		f.Fg = t.fg(RoleInk)
		bg, ok := t.token(tokenSelection)
		if !ok {
			f.Fg, f.Reverse = Paint{}, true
			return f
		}
		f.Bg = bg
	}
	return f
}

// Style is Face as a lipgloss style, for strings (the hub). It encodes the
// same SGR as the face.
func (t Theme) Style(r Role, g Ground) lipgloss.Style {
	return faceStyle(t.Face(r, g))
}

// faceStyle turns a face into a lipgloss style.
func faceStyle(f Face) lipgloss.Style {
	s := lipgloss.NewStyle().Bold(f.Bold).Italic(f.Italic).Underline(f.Underline).Reverse(f.Reverse)
	if c := paintColor(f.Fg); c != nil {
		s = s.Foreground(c)
	}
	if c := paintColor(f.Bg); c != nil {
		s = s.Background(c)
	}
	return s
}

// paintColor is the terminal colour of p; nil for the default colour. A
// palette colour is one of the 16 (SGR 30-37 and 90-97, as the canvas
// writes them), never an index of the 256.
func paintColor(p Paint) ansi.Color {
	switch p.Kind {
	case PaintPalette:
		return ansi.BasicColor(p.V & 0x0f)
	case PaintRGB:
		return ansi.RGBColor{R: uint8(p.V >> 16), G: uint8(p.V >> 8), B: uint8(p.V)}
	}
	return nil
}

// HasGround reports whether g is drawn as a colour. The default ground
// always is; the raised and the selection ground only in the truecolor tier
// and only when their token is set. Where it is false the caller draws the
// treatment of 5a.7.1 instead (the pointer and a bold name for the selected
// row, reverse video for the banner and the copy selection).
func (t Theme) HasGround(g Ground) bool {
	switch g {
	case GroundNone:
		return true
	case GroundRaised:
		_, ok := t.token(tokenRaised)
		return ok
	case GroundSelection:
		_, ok := t.token(tokenSelection)
		return ok
	}
	return false
}

// Chip is a machine's name as the status row and the hub headings draw it
// (5a.7.1): " name " in bold strong ink on the raised ground, or, without
// that ground, "[name]" in bold, the brackets taking the two padding cells.
// Both forms are two cells wider than the name.
func (t Theme) Chip(name string) (string, Face) {
	if t.HasGround(GroundRaised) {
		return " " + name + " ", t.Face(RoleStrong, GroundRaised)
	}
	return t.Glyph(GlyphChipOpen) + name + t.Glyph(GlyphChipClose), t.Face(RoleStrong, GroundNone)
}
