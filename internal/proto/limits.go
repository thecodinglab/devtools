package proto

// Gen is the protocol generation (4.1). A peer with another generation is
// refused with a text that names both.
const Gen = 2

// Limits of 4.1, enforced by both sides.
const (
	MaxMessage = 4 << 20   // one message, the JSON object without its length prefix
	FramePiece = 1 << 20   // the frame builder starts a new piece at this size
	MaxRow     = 128 << 10 // one encoded row of a frame
	MaxPaste   = 256 << 10 // input.paste.text per message
	MaxClip    = 1 << 20   // event.clipboard.data, decoded; never empty
	MaxText    = 1 << 20   // the text of a pane.text result; longer selections are cut

	MaxPanes = 256 // panes of one tab: below the fleet's bound of panes per machine; in practice the minimum pane size limits them first

	MinCols, MinRows = 2, 2
	MaxCols, MaxRows = 1000, 500
)

// ClampSize clamps a pane area to the limits of 4.1: 2 to 1000 columns and
// 2 to 500 rows. The server clamps every size it is sent.
func ClampSize(cols, rows int) (int, int) {
	return clamp(cols, MinCols, MaxCols), clamp(rows, MinRows, MaxRows)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
