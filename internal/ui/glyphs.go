package ui

// The glyphs of the look (5a.7.2), both sets. Every glyph of the UTF-8 set
// is one cell wide and from a standard Unicode block; the ASCII set is used
// when the locale is not UTF-8.
//
// provisional E9: the table is the one E9 measured one cell wide in the
// engine, in x/ansi and in the outer terminal's cursor reports
// (experiments/e3-client.md, "widths.txt"). Whether ghostty draws each one
// inside its cell with the user's font, on Linux and on a Mac, is pending
// [GO]; a glyph that fails there is replaced here by its fallback.

// Glyph names one glyph of the table.
type Glyph uint8

const (
	GlyphHere     Glyph = iota // the row of the session on screen
	GlyphDividerV              // between two panes side by side
	GlyphDividerH              // between two panes one above the other
	GlyphLeft                  // pointer: the pane with the keys is to the left
	GlyphRight                 // pointer: to the right
	GlyphUp                    // pointer: above
	GlyphDown                  // pointer: below
	GlyphBlocked               // an agent waits for the user
	GlyphDone                  // an agent finished, not yet seen
	GlyphWorking               // an agent is working
	GlyphIdle                  // an agent is idle (hub only)
	GlyphBell                  // a program in the tab rang the bell
	GlyphFailed                // needs the user to act
	GlyphEllipsis              // cut text; a wait
	GlyphPrompt                // the hub's input
	GlyphEnter                 // key hint
	GlyphControl               // key hint
	GlyphAhead                 // sync: commits ahead
	GlyphBehind                // sync: commits behind
	GlyphPad                   // cells outside every pane
	GlyphCreate                // the create row
	GlyphSelected              // the selected row of a list, in the gutter
	GlyphTee                   // a feature worktree under its project, with another below it
	GlyphElbow                 // the last feature worktree under its project
	glyphCount
)

// utf8Glyphs is the UTF-8 set, by Glyph.
var utf8Glyphs = [glyphCount]string{
	GlyphHere:     "▎", // U+258E
	GlyphDividerV: "│", // U+2502
	GlyphDividerH: "─", // U+2500
	GlyphLeft:     "◂", // U+25C2
	GlyphRight:    "▸", // U+25B8
	GlyphUp:       "▴", // U+25B4
	GlyphDown:     "▾", // U+25BE
	GlyphBlocked:  "◆", // U+25C6
	GlyphDone:     "✓", // U+2713
	GlyphWorking:  "●", // U+25CF
	GlyphIdle:     "○", // U+25CB
	GlyphBell:     "!",
	GlyphFailed:   "✗", // U+2717
	GlyphEllipsis: "…", // U+2026
	GlyphPrompt:   "›", // U+203A
	GlyphEnter:    "⏎", // U+23CE
	GlyphControl:  "⌃", // U+2303
	GlyphAhead:    "↑", // U+2191
	GlyphBehind:   "↓", // U+2193
	GlyphPad:      "·", // U+00B7
	GlyphCreate:   "+",
	GlyphSelected: "▌", // U+258C
	GlyphTee:      "├", // U+251C
	GlyphElbow:    "└", // U+2514
}

// asciiGlyphs is the ASCII set, by Glyph.
var asciiGlyphs = [glyphCount]string{
	GlyphHere:     "|",
	GlyphDividerV: "|",
	GlyphDividerH: "-",
	GlyphLeft:     "<",
	GlyphRight:    ">",
	GlyphUp:       "^",
	GlyphDown:     "v",
	GlyphBlocked:  "?",
	GlyphDone:     "+",
	GlyphWorking:  "*",
	GlyphIdle:     "-",
	GlyphBell:     "!",
	GlyphFailed:   "x",
	GlyphEllipsis: "...",
	GlyphPrompt:   ">",
	GlyphEnter:    "enter",
	GlyphControl:  "ctrl+",
	GlyphAhead:    "^",
	GlyphBehind:   "v",
	GlyphPad:      ".",
	GlyphCreate:   "+",
	GlyphSelected: ">",
	GlyphTee:      "|",
	GlyphElbow:    "`",
}
