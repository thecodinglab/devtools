package model

import "devtools/internal/proto"

// MinPaneCols and MinPaneRows are the smallest pane (3.3).
const MinPaneCols, MinPaneRows = 4, 2

// Rect is a pane's rectangle inside the pane area, in cells.
type Rect struct{ X, Y, W, H int }

// Layout returns the rectangles of a tab's panes inside cols x rows (3.3):
// one pane fills the area; split "right" puts the second pane after the
// first plus one divider column, the first w1 = cols / 2 wide; split "down"
// stacks them with one divider row, the first h1 = rows / 2 high. The first
// pane gets the extra cell of an odd remainder. A rectangle is never
// smaller than MinPaneCols by MinPaneRows, also when the area is: such a
// pane is cut by the client, never smaller than the minimum. A split value
// other than "right" and "down" is one pane.
func Layout(cols, rows int, split string) []Rect {
	rs := layout(cols, rows, split)
	for i := range rs {
		rs[i].W = max(rs[i].W, MinPaneCols)
		rs[i].H = max(rs[i].H, MinPaneRows)
	}
	return rs
}

// layout is Layout without the minimum.
func layout(cols, rows int, split string) []Rect {
	switch split {
	case proto.SplitRight:
		w1 := cols / 2
		return []Rect{{0, 0, w1, rows}, {w1 + 1, 0, cols - 1 - w1, rows}}
	case proto.SplitDown:
		h1 := rows / 2
		return []Rect{{0, 0, cols, h1}, {0, h1 + 1, cols, rows - 1 - h1}}
	}
	return []Rect{{0, 0, cols, rows}}
}

// splitFits reports whether both panes of a split in direction fit the
// minimum inside cols x rows.
func splitFits(cols, rows int, direction string) bool {
	for _, r := range layout(cols, rows, direction) {
		if r.W < MinPaneCols || r.H < MinPaneRows {
			return false
		}
	}
	return true
}
