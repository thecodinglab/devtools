// Package screen is what everything visible is drawn with: one canvas of
// cells per client, composed in painter's order (5.3.5 of the plan).
//
// This file is a contract (6.2, P1-00b) and is read-only for P1-07, which
// writes Canvas and its methods in place of stub.go.
package screen

// Color is one colour of a cell.
type Color struct {
	Kind uint8 // 0 default, 1 one of 16, 2 one of 256, 3 RGB
	V    uint32
}

// Attr is a set of text attributes.
type Attr uint16

const (
	Bold Attr = 1 << iota
	Faint
	Italic
	Blink
	Reverse
	Conceal
	Strike
)

// Style is how a cell is drawn.
type Style struct {
	Fg, Bg, Ul Color
	Attrs      Attr
	Underline  uint8 // 0 none, 1 single, 2 double, 3 curly, 4 dotted, 5 dashed
	Link       string
}

// Cell is one cell of a canvas.
type Cell struct {
	S     string // one grapheme cluster; "" in the second cell of a wide character
	W     uint8  // 1 or 2; 0 in the second cell
	Style Style
}

// Rect is a rectangle of cells.
type Rect struct{ X, Y, W, H int }
