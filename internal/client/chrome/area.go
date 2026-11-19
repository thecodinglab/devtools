package chrome

// The pane-area chrome of the look (P4-03): the divider and its pointer
// (5a.6.5), the banner of a stale view (5a.6.4), the pane message and the
// pad (5a.6.6), the too-small screen (5a.6.8), and the three restyles of a
// pane cell: the copy selection, the copy cursor and a cell of a stale pane
// (5a.7.1, 5a.13).
//
// Everything here reads State.Theme for colours and glyphs and State.View
// and State.WindowFocused for what to show; nothing reads State.Mode, so a
// stale view keeps its banner, its inactive cells and its comment pointer
// while a prefix is pending or a switch runs (5a.10.1).
//
// The package-level names of this file start with "area" so that they cannot
// meet the names of status.go and reduce.go (P4-02), which are written in
// parallel in the same package.

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"devtools/internal/client/screen"
	"devtools/internal/ui"
)

// MinSize is the smallest window the look draws in: 20 columns by 6 rows
// (5a.6.8). Below it the client calls TooSmall instead of everything else.
func (l *look) MinSize() (cols, rows int) { return ui.MinCols, ui.MinRows }

// Divider fills c, the one-cell line between two panes, with │ (panes side
// by side, vertical) or ─ (stacked) in comment, and puts the pointer into
// its middle cell: ◂ or ▸ towards the pane with the keys side by side, ▴ or
// ▾ stacked. The pointer is strong ink; while the window has lost the
// terminal's focus or the view is stale no pane has the keys and it is
// comment, like the line (5a.6.5).
func (l *look) Divider(c *screen.Canvas, s State, vertical bool, firstFocused bool) {
	w, h := c.Size()
	if w <= 0 || h <= 0 {
		return
	}
	t := s.Theme
	line := areaStyle(t.Face(ui.RoleComment, ui.GroundNone))
	g := ui.GlyphDividerH
	if vertical {
		g = ui.GlyphDividerV
	}
	c.Fill(screen.Rect{W: w, H: h}, screen.Cell{S: t.Glyph(g), W: 1, Style: line})

	pointer := line
	if s.WindowFocused && s.View != "stale" {
		pointer = areaStyle(t.Face(ui.RoleStrong, ui.GroundNone))
	}
	x, y := w/2, 0
	switch {
	case vertical && firstFocused:
		x, y, g = 0, h/2, ui.GlyphLeft
	case vertical:
		x, y, g = 0, h/2, ui.GlyphRight
	case firstFocused:
		g = ui.GlyphUp
	default:
		g = ui.GlyphDown
	}
	c.Set(x, y, screen.Cell{S: t.Glyph(g), W: 1, Style: pointer})
}

// Banner draws the banner of a stale view over row 0 of c, the pane area,
// and returns the rows it covers: 1 while the view is stale, else 0 and
// nothing is drawn. It covers; it never resizes a pane, and it writes no
// row but row 0 (5a.6.4).
//
// The row is the raised ground across the whole width (reverse video in the
// palette tier): a margin, the machine's name in bold strong ink, sep, the
// phrase of 5a.14.2 in ink, preceded by ✗ in bold ink when the machine
// failed, then "Typing is paused.". No hue is used: error on the raised
// ground is below 4.5:1 (5a.13). When the row is too narrow the sentence
// leaves first, then the phrase is cut at the end with the ellipsis, and
// the name is cut last, never below its first letter.
func (l *look) Banner(c *screen.Canvas, s State) (rows int) {
	w, h := c.Size()
	if s.View != "stale" || w <= 0 || h <= 0 {
		return 0
	}
	t := s.Theme
	name, phrase, failed := areaBannerWords(s)

	raised := t.HasGround(ui.GroundRaised)
	face := func(r ui.Role) screen.Style {
		if raised {
			return areaStyle(t.Face(r, ui.GroundRaised))
		}
		// The palette tier: reverse video across the row, every role in the
		// default foreground with its bold kept (5a.7.1, 5a.13).
		f := t.Face(r, ui.GroundNone)
		return screen.Style{Attrs: screen.Reverse | areaBold(f.Bold || r == ui.RoleError)}
	}
	ink, strong, crossStyle := face(ui.RoleInk), face(ui.RoleStrong), face(ui.RoleError)
	c.Fill(screen.Rect{W: w, H: 1}, screen.Cell{S: " ", W: 1, Style: ink})

	const margin, sep = 1, 2
	room := max(w-2*margin, 1)
	mark := ""
	if failed {
		mark = t.Glyph(ui.GlyphFailed) + " "
	}
	nameW := ui.Width(name)
	text := mark + phrase
	switch avail := room - nameW - sep; {
	case ui.Width(text+" "+ui.BannerPaused) <= avail:
		text += " " + ui.BannerPaused
	case ui.Width(text) <= avail:
	case avail >= ui.Width(t.Glyph(ui.GlyphEllipsis))+1:
		text = t.CutEnd(text, avail)
	default:
		text = ""
		name = areaFirstLetter(t.CutEnd(name, room), name)
	}

	x := margin
	x += c.Text(x, 0, name, strong, room)
	if text == "" {
		return 1
	}
	x += sep
	if cross := t.Glyph(ui.GlyphFailed); failed && strings.HasPrefix(text, cross) {
		x += c.Text(x, 0, cross, crossStyle, w-margin-x)
		text = text[len(cross):]
	}
	c.Text(x, 0, text, ink, w-margin-x)
	return 1
}

// areaBannerWords is the name of the machine of the stale view, its phrase
// and whether the phrase is a failure. This machine ("" in State.Machine) is
// named by Summary.Machines[0] and its phrase is that its server stopped; a
// machine whose state has no phrase yet (still "online" in the summary) is
// said to be unreachable, which is what a lost link is (3.5.2).
func areaBannerWords(s State) (name, phrase string, failed bool) {
	if s.Machine == "" {
		if len(s.Summary.Machines) > 0 {
			name = s.Summary.Machines[0].Name
		}
		return name, ui.LocalPhrase("stopped"), false
	}
	name = s.Machine
	for i, m := range s.Summary.Machines {
		if i == 0 || m.Name != name {
			continue
		}
		phrase, failed = ui.MachinePhrase(name, m.State, ui.Problem{Reason: m.Problem, Text: m.Reason, Retry: m.Retry}, false)
		break
	}
	if phrase == "" || phrase == "connecting…" || phrase == "listing…" {
		phrase, failed = ui.MachinePhrase(name, "retrying", ui.Problem{Reason: "lost", Retry: "retrying"}, false)
	}
	return name, phrase, failed
}

// areaFirstLetter keeps at least the first grapheme cluster of name when
// cut is empty: a name is never cut below its first letter (5a.6.4).
func areaFirstLetter(cut, name string) string {
	if cut != "" || name == "" {
		return cut
	}
	first, _ := ansi.FirstGraphemeCluster(name, ansi.GraphemeWidth)
	return first
}

// Pad fills c, the cells a clipped or smaller view leaves to no pane, with
// · in comment (5a.6.6).
func (l *look) Pad(c *screen.Canvas, s State) {
	w, h := c.Size()
	st := areaStyle(s.Theme.Face(ui.RoleComment, ui.GroundNone))
	c.Fill(screen.Rect{W: w, H: h}, screen.Cell{S: s.Theme.Glyph(ui.GlyphPad), W: 1, Style: st})
}

// PaneMessage blanks c, the pane, and writes at its top left the reason in
// ink and "Press enter to start it again." in comment (5a.6.6). A line that
// does not fit is cut at the end; nothing wraps.
func (l *look) PaneMessage(c *screen.Canvas, s State, text string) {
	w, _ := c.Size()
	c.Clear()
	t := s.Theme
	c.Text(0, 0, t.CutEnd(text, w), areaStyle(t.Face(ui.RoleInk, ui.GroundNone)), w)
	c.Text(0, 1, t.CutEnd(ui.PressEnter, w), areaStyle(t.Face(ui.RoleComment, ui.GroundNone)), w)
}

// TooSmall blanks c, the whole window, and centres "Window too small" and
// "20 by 6 needed" in comment (5a.6.8).
func (l *look) TooSmall(c *screen.Canvas, s State) {
	c.Clear()
	w, h := c.Size()
	t := s.Theme
	st := areaStyle(t.Face(ui.RoleComment, ui.GroundNone))
	top := max((h-2)/2, 0)
	for i, line := range []string{ui.TooSmall, ui.TooSmallNeed} {
		line = t.CutEnd(line, w)
		c.Text((w-ui.Width(line))/2, top+i, line, st, w)
	}
}

// Selection is how a selected cell of copy mode looks: the selection ground
// with only ink on it, so the text loses its own colours and a selection
// looks the same over any program's output (5a.13). In the palette tier,
// without that ground, it is reverse video with every colour the default
// (5a.7.1). Attributes other than reverse video are kept.
func (l *look) Selection(s State, st screen.Style) screen.Style {
	t := s.Theme
	out := areaKeep(st)
	if t.HasGround(ui.GroundSelection) {
		f := t.Face(ui.RoleInk, ui.GroundSelection)
		out.Fg, out.Bg = areaColor(f.Fg), areaColor(f.Bg)
		return out
	}
	out.Attrs |= screen.Reverse
	return out
}

// CopyCursor is how the copy cursor looks: one cell in reverse video in the
// default colours where the selection has a ground of its own; underlined
// and bold in the palette tier, where the selection is reverse video and a
// reversed cursor would vanish inside it (5a.7.1). Either is visible inside
// a selection and outside one.
func (l *look) CopyCursor(s State, st screen.Style) screen.Style {
	if s.Theme.HasGround(ui.GroundSelection) {
		out := areaKeep(st)
		out.Attrs |= screen.Reverse
		return out
	}
	st.Attrs |= screen.Bold
	st.Underline = 1
	st.Ul = screen.Color{}
	return st
}

// Inactive is how a cell of a stale pane looks: its character stays, its
// colours and attributes go, and it is drawn in comment on the default
// background ("restyled as inactive", 3.5.5, 5a.6.4). The client applies it
// to every pane cell while the view is stale (C6); it does not depend on
// the mode.
func (l *look) Inactive(s State, st screen.Style) screen.Style {
	return screen.Style{Fg: areaColor(s.Theme.Face(ui.RoleComment, ui.GroundNone).Fg)}
}

// areaKeep is st without its colours and without reverse video: what the
// selection and the copy cursor keep of a cell.
func areaKeep(st screen.Style) screen.Style {
	return screen.Style{Attrs: st.Attrs &^ screen.Reverse, Underline: st.Underline, Link: st.Link}
}

// areaStyle is a face of the theme as a style of the canvas.
func areaStyle(f ui.Face) screen.Style {
	st := screen.Style{Fg: areaColor(f.Fg), Bg: areaColor(f.Bg), Attrs: areaBold(f.Bold)}
	if f.Italic {
		st.Attrs |= screen.Italic
	}
	if f.Reverse {
		st.Attrs |= screen.Reverse
	}
	if f.Underline {
		st.Underline = 1
	}
	return st
}

// areaBold is the bold attribute when b holds.
func areaBold(b bool) screen.Attr {
	if b {
		return screen.Bold
	}
	return 0
}

// areaColor is a paint of the theme as a colour of the canvas: a palette
// colour is one of the 16, RGB is RGB.
func areaColor(p ui.Paint) screen.Color {
	switch p.Kind {
	case ui.PaintPalette:
		return screen.Color{Kind: 1, V: p.V & 0x0f}
	case ui.PaintRGB:
		return screen.Color{Kind: 3, V: p.V & 0xffffff}
	}
	return screen.Color{}
}
