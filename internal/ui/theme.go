package ui

// The theme of the look (5.3.9, 5a.7.1, 5a.16.1): seven roles on three
// grounds, two tiers, five tokens. This file is a contract (6.2, P4-00 a):
// the types, the token names and the glyph lookup. NewTheme, Face, Style,
// HasGround, Chip, CutEnd and CutMiddle are in stub_theme.go until P4-01
// replaces it with tiers.go.

// Role is what a piece of text is to the reader (5a.7.1).
type Role uint8

const (
	RoleInk       Role = iota // the text
	RoleStrong                // names that lead; always bold
	RoleMuted                 // secondary data
	RoleComment               // context
	RoleAccent                // here
	RoleWarning               // wants you
	RoleError                 // failed
	RoleElsewhere             // another machine: where the keys or Enter would go
)

// Ground is what a cell's background is (5a.7.1).
type Ground uint8

const (
	GroundNone      Ground = iota // the terminal's default background
	GroundRaised                  // selected row, chip, banner
	GroundSelection               // selected text in copy mode
)

// Paint is one colour of a Face.
//
// Kind: 0 default, 1 palette 0-15, 2 RGB 0xRRGGBB (written only to a
// terminal that reported 24-bit colour).
type Paint struct {
	Kind uint8
	V    uint32
}

// The values of Paint.Kind.
const (
	PaintDefault uint8 = 0 // the terminal's default colour; V is unused
	PaintPalette uint8 = 1 // V is a palette index 0-15
	PaintRGB     uint8 = 2 // V is 0xRRGGBB
)

// Face is the style of a cell: what the chrome draws with.
type Face struct {
	Fg, Bg                           Paint
	Bold, Italic, Underline, Reverse bool
}

// Token is one `color <token> <light-rgb> <dark-rgb>` line of the config
// (4.6): the RGB values of a role for a light and a dark appearance.
type Token struct {
	Name        string
	Light, Dark uint32 // 0xRRGGBB
}

// ThemeOptions are the inputs of NewTheme (5a.16.1).
type ThemeOptions struct {
	Tokens []Token // the color lines of the config
	Dark   bool    // the terminal's background is dark
	Known  bool    // the terminal answered the background query
	Colors int     // 16, 256 or 1<<24: the colour profile of the terminal
	UTF8   bool    // the locale is UTF-8; false selects the ASCII glyph set
}

// The tokens in the order of TokenNames; the index into Theme.rgb and
// Theme.has.
const (
	tokenStrong = iota
	tokenMuted
	tokenRaised
	tokenSelection
	tokenWarning
	tokenCount
)

// tokenNames is the order of TokenNames and of the token indices above.
var tokenNames = [tokenCount]string{"strong", "muted", "raised", "selection", "warning"}

// Theme is the colours and the glyph set everything is drawn with. It is a
// value: comparable and safe to copy. The zero value is the palette tier
// with UTF-8 glyphs, which is complete on any terminal (5a.7.1).
type Theme struct {
	ascii     bool               // the ASCII glyph set (ThemeOptions.UTF8 false)
	truecolor bool               // the truecolor tier: Known, 24-bit colour and at least one token (5a.16.1)
	rgb       [tokenCount]uint32 // per token, its value for the terminal's appearance
	has       [tokenCount]bool   // per token, whether the config set it
}

// TokenNames returns the five token names a color line may use, in the
// order strong, muted, raised, selection, warning.
func TokenNames() []string {
	names := make([]string, len(tokenNames))
	copy(names, tokenNames[:])
	return names
}

// tokenIndex returns the index of a token name, or -1.
func tokenIndex(name string) int {
	for i, n := range tokenNames {
		if n == name {
			return i
		}
	}
	return -1
}

// Glyph returns the glyph g in the theme's set: UTF-8, or ASCII when the
// locale is not UTF-8 (5a.7.2).
func (t Theme) Glyph(g Glyph) string {
	if t.ascii {
		return asciiGlyphs[g]
	}
	return utf8Glyphs[g]
}

// ThemeMsg tells an open overlay that the theme changed.
type ThemeMsg struct{ Theme Theme }
