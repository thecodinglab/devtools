package proto

// Gen is the protocol generation (4.1): the framing, the envelope and the
// handshake, nothing else. It stays 1. A feature is a capability (cross.go):
// the server lists what it implements in welcome.caps, the client in
// hello.caps, and each side uses a family only when both list it, so an
// older peer is still served with what it has instead of being refused.
// Bump Gen only for a change that no capability can describe, one that
// makes the bytes themselves unreadable to the other side.
const Gen = 1

// gens are the generations this build speaks: Gen, and 2, which the builds
// of 20 November 2026 named the tree layout (CapTree) with; it frames
// exactly as 1 does.
var gens = [...]int{Gen, 2}

// Speaks reports whether this build reads and writes generation g. A
// server answers a hello of such a generation with a welcome of the same
// one, so a peer that checks for its own generation accepts it.
func Speaks(g int) bool {
	for _, x := range gens {
		if x == g {
			return true
		}
	}
	return false
}

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

// Gens returns the generations this build speaks, for the preflight of a
// live upgrade (internal/handover).
func Gens() []int { return append([]int(nil), gens[:]...) }
