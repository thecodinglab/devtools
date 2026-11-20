package chrome

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"devtools/internal/client/screen"
	"devtools/internal/ui"
)

// Plain is the reference chrome: the layout of 5a.6 in ASCII and in the
// terminal's own colours, with the words of internal/ui. It stays in the
// tree as the chrome tests draw with and as what the client uses when
// Options.Chrome is nil (5.3.7, C17); what the user sees is NewLook.
//
// What it leaves out of the look on purpose: the theme (State.Theme is not
// read), the reduction order of 5a.6.2 (the right group is dropped whole
// when the row is too narrow, and the left part is cut at the end), the
// hint strip (Plain has no chords, so a prefix with hints shows only
// "esc cancel"), and the cut rules of the mode rows below 80 columns.
// Styles are attributes only: the mode block is reverse and bold, the
// current tab and the session are bold, the banner is reverse.
type Plain struct{}

var _ Chrome = Plain{}

// The marks of Plain: the ASCII set of 5a.7.2.
const (
	plainBlocked = "?"
	plainDone    = "+"
	plainWorking = "*"
	plainBell    = "!"
	plainFailed  = "x"
	plainDots    = "..."
	plainPad     = "."
	plainV       = "|"
	plainH       = "-"
)

var (
	plainBold  = screen.Style{Attrs: screen.Bold}
	plainBlock = screen.Style{Attrs: screen.Reverse | screen.Bold}
	plainRaise = screen.Style{Attrs: screen.Reverse}
)

// MinSize is 20 columns by 6 rows (5a.6.8).
func (Plain) MinSize() (cols, rows int) { return ui.MinCols, ui.MinRows }

// StatusRows is 1: the status row is the last row.
func (Plain) StatusRows() int { return 1 }

// part is one piece of a status row: text in a style.
type part struct {
	s  string
	st screen.Style
}

// Status draws the status row from s.Mode into row 0 of c.
func (p Plain) Status(c *screen.Canvas, s State) {
	w, h := c.Size()
	if h == 0 {
		return
	}
	c.Fill(screen.Rect{X: 0, Y: 0, W: w, H: 1}, screen.Cell{S: " ", W: 1})
	var (
		left  []part
		right []item
	)
	switch s.Mode {
	case "confirm":
		program := ""
		if s.Confirm != nil {
			program = s.Confirm.Program
		}
		left = []part{{" " + ui.ConfirmClose(program, 0), screen.Style{}},
			{"   enter " + ui.ConfirmClosePane + "   esc " + ui.ConfirmCancel, screen.Style{}}}
	case "prefix":
		left = []part{{" " + ui.ModePrefix + " ", plainBlock}}
		if s.Hints {
			left = append(left, part{" esc " + ui.HintCancel, screen.Style{}})
			break
		}
		left = append(left, p.tabsOrNotice(s)...)
		right = p.rightGroup(s)
	case "switching":
		left = []part{{" " + ui.ModeSwitching + " ", plainBlock},
			{" " + joinNonEmpty(ui.SwitchingTo, s.Target.Machine, s.Target.Session) + plainDots, screen.Style{}}}
		right = p.rightGroup(s)
	case "copy", "scrolled":
		word := ui.ModeCopy
		if s.Mode == "scrolled" {
			word = ui.ModeScrolled
		}
		left = []part{{" " + word + " ", plainBlock}}
		if s.Copy != nil {
			left = append(left, part{" " + ui.CopyPosition(s.Copy.Line, s.Copy.Total, 0), screen.Style{}})
			if phrase := selecting(s.Copy.Selecting); phrase != "" && s.Mode == "copy" {
				left = append(left, part{"  " + phrase, screen.Style{}})
			}
		}
		if s.Mode == "copy" {
			right = []item{{s: "v " + ui.HintSelect + "   y " + ui.HintCopy + "   q " + ui.HintLeave, drop: 1}}
		} else {
			right = []item{{s: ui.AnyKeyReturns, drop: 1}}
		}
	default:
		left = p.address(s)
		left = append(left, p.tabsOrNotice(s)...)
		right = p.rightGroup(s)
	}
	drawRow(c, w, left, right)
}

// item is one entry of the right group: its text, the group it belongs to
// (entries of one group are 2 cells apart, groups 3) and when it leaves
// (lowest first).
type item struct {
	s     string
	group int
	drop  int
}

// drawRow draws left from column 0, cut at the end, and right against the
// right edge with a margin of 1 and at least 3 cells between them; items
// of right leave by their drop order until it fits.
func drawRow(c *screen.Canvas, w int, left []part, right []item) {
	x := 0
	for _, pt := range left {
		x += c.Text(x, 0, pt.s, pt.st, w-x)
	}
	lw := partsWidth(left)
	items := append([]item(nil), right...)
	for len(items) > 0 && lw+3+ui.Width(joinItems(items))+1 > w {
		low := items[0].drop
		for _, it := range items {
			low = min(low, it.drop)
		}
		kept := items[:0]
		for _, it := range items {
			if it.drop != low {
				kept = append(kept, it)
			}
		}
		items = kept
	}
	if len(items) == 0 {
		return
	}
	text := joinItems(items)
	c.Text(w-1-ui.Width(text), 0, text, screen.Style{}, w)
}

// joinItems is the text of the right group.
func joinItems(items []item) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			if items[i-1].group == it.group {
				b.WriteString("  ")
			} else {
				b.WriteString("   ")
			}
		}
		b.WriteString(it.s)
	}
	return b.String()
}

func partsWidth(ps []part) int {
	n := 0
	for _, pt := range ps {
		n += ui.Width(pt.s)
	}
	return n
}

// address is this machine's name, or "@name" for another machine, then the
// session label in bold.
func (Plain) address(s State) []part {
	name := s.Machine
	if name != "" {
		name = "@" + name
	} else if len(s.Summary.Machines) > 0 {
		name = s.Summary.Machines[0].Name
	}
	var out []part
	if name != "" {
		out = append(out, part{" " + name, screen.Style{}})
	}
	if s.Session != "" {
		out = append(out, part{"  ", screen.Style{}}, part{s.Session, plainBold})
	}
	return out
}

// tabsOrNotice is the tabs, "{index} {label}" each, the current one in
// brackets and bold, then the agent mark and the bell; or the notice,
// which takes their place.
func (Plain) tabsOrNotice(s State) []part {
	if s.Notice != "" {
		return []part{{"   " + s.Notice, screen.Style{}}}
	}
	var out []part
	for i, t := range s.Tabs {
		sep := "  "
		if i == 0 {
			sep = "   "
		}
		label := strconv.Itoa(t.Index)
		if t.Title != "" {
			label += " " + t.Title
		}
		if m := agentMark(t.Agent); m != "" {
			label += " " + m
		}
		if t.Bell {
			label += " " + plainBell
		}
		st := screen.Style{}
		if t.Current {
			label, st = "["+label+"]", plainBold
		}
		out = append(out, part{sep, screen.Style{}}, part{label, st})
	}
	return out
}

// rightGroup is the fleet strip, the roll-up and the date and time, as
// items in the order they are drawn. The drop numbers are the order in
// which they leave when the row is too narrow (a short form of 5a.6.2):
// the date, the fleet strip, the clock, the done and working counts; the
// blocked count stays while anything of the group is drawn.
func (Plain) rightGroup(s State) []item {
	var out []item
	for i, m := range s.Summary.Machines {
		if i == 0 {
			continue
		}
		var text string
		switch m.State {
		case "online":
			text = m.Name
		case "connecting":
			text = m.Name + " " + plainDots
		case "retrying":
			text = m.Name + " " + ui.FleetRetrying
		case "attention":
			text = joinNonEmpty(m.Name, plainFailed, ui.ReasonWord(m.Problem))
		case "off":
			if m.Busy > 0 {
				text = m.Name + " " + ui.FleetOff
			}
		}
		if text != "" {
			out = append(out, item{s: text, group: 1, drop: 2})
		}
	}
	for _, st := range []string{"blocked", "done", "working"} {
		n := s.Summary.Counts[st]
		if n <= 0 {
			continue
		}
		drop := 4
		if st == "blocked" {
			drop = 5
		}
		out = append(out, item{s: joinNonEmpty(agentMark(st), strconv.Itoa(n), ui.RollupWord(st, n)), group: 2, drop: drop})
	}
	if !s.Now.IsZero() {
		out = append(out, item{s: ui.Date(s.Now), group: 3, drop: 1}, item{s: ui.Clock(s.Now), group: 3, drop: 3})
	}
	return out
}

// agentMark is the ASCII mark of an agent status; idle and none have none.
func agentMark(status string) string {
	switch status {
	case "blocked":
		return plainBlocked
	case "done":
		return plainDone
	case "working":
		return plainWorking
	}
	return ""
}

func selecting(kind string) string {
	switch kind {
	case "char":
		return ui.Selecting
	case "line":
		return ui.SelectingLines
	case "block":
		return ui.SelectingBlock
	}
	return ""
}

func joinNonEmpty(words ...string) string {
	var out []string
	for _, w := range words {
		if w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// Divider fills c with "|" (panes side by side) or "-" (stacked) and puts
// the pointer at the pane with the keys at p.At when p points: "<" or ">",
// "^" or "v". While the window has lost the focus or the view is stale no
// pane has the keys and there is no pointer.
func (Plain) Divider(c *screen.Canvas, s State, vertical bool, p Pointer) {
	w, h := c.Size()
	line := plainH
	if vertical {
		line = plainV
	}
	c.Fill(screen.Rect{X: 0, Y: 0, W: w, H: h}, screen.Cell{S: line, W: 1})
	if !s.WindowFocused || s.View == "stale" || p.Toward == PointNone || w <= 0 || h <= 0 {
		return
	}
	before := p.Toward == PointBefore
	x, y := min(max(p.At, 0), w-1), min(max(p.At, 0), h-1)
	switch {
	case vertical && before:
		c.Set(0, y, screen.Cell{S: "<", W: 1, Style: plainBold})
	case vertical:
		c.Set(0, y, screen.Cell{S: ">", W: 1, Style: plainBold})
	case before:
		c.Set(x, 0, screen.Cell{S: "^", W: 1, Style: plainBold})
	default:
		c.Set(x, 0, screen.Cell{S: "v", W: 1, Style: plainBold})
	}
}

// Banner draws the banner of a stale view over row 0 of c, the pane area,
// in reverse video: the machine's name, then the phrase of 5a.14.2 and
// "Typing is paused.". When the row is too narrow the sentence leaves
// first, then the phrase is cut with "...", then the name. It returns 1,
// or 0 when the view is not stale.
func (Plain) Banner(c *screen.Canvas, s State) (rows int) {
	w, h := c.Size()
	if s.View != "stale" || w <= 0 || h <= 0 {
		return 0
	}
	name, phrase := bannerWords(s)
	c.Fill(screen.Rect{X: 0, Y: 0, W: w, H: 1}, screen.Cell{S: " ", W: 1, Style: plainRaise})
	room := w - 2 // the margin at both ends
	full := name + "  " + phrase + " " + ui.BannerPaused
	text := full
	switch {
	case ui.Width(full) <= room:
	case ui.Width(name+"  "+phrase) <= room:
		text = name + "  " + phrase
	case ui.Width(name)+2+len(plainDots) < room:
		text = name + "  " + cutEnd(phrase, room-ui.Width(name)-2)
	default:
		text = cutEnd(name, room)
	}
	c.Text(1, 0, text, plainRaise, room)
	return 1
}

// bannerWords is the name of the machine of the stale view and its phrase.
func bannerWords(s State) (name, phrase string) {
	name = s.Machine
	if name == "" && len(s.Summary.Machines) > 0 {
		name = s.Summary.Machines[0].Name
	}
	m := Machine{Name: name, State: "retrying", Problem: "lost"}
	for _, mm := range s.Summary.Machines {
		if mm.Name == name {
			m = mm
			break
		}
	}
	phrase, _ = ui.MachinePhrase(name, m.State, ui.Problem{Reason: m.Problem, Text: m.Reason, Retry: m.Retry}, false)
	if phrase == "" {
		phrase, _ = ui.MachinePhrase(name, "retrying", ui.Problem{Reason: "lost"}, false)
	}
	return name, phrase
}

// cutEnd cuts s to n cells, ending in "..." when it was cut.
func cutEnd(s string, n int) string {
	if n <= len(plainDots) {
		return ansi.Truncate(s, max(n, 0), "")
	}
	return ansi.Truncate(s, n, plainDots)
}

// Pad fills c with "." in the default style.
func (Plain) Pad(c *screen.Canvas, s State) {
	w, h := c.Size()
	c.Fill(screen.Rect{X: 0, Y: 0, W: w, H: h}, screen.Cell{S: plainPad, W: 1})
}

// PaneMessage blanks the first two rows of c and writes the reason and
// "Press enter to start it again." at their left.
func (Plain) PaneMessage(c *screen.Canvas, s State, text string) {
	w, _ := c.Size()
	c.Fill(screen.Rect{X: 0, Y: 0, W: w, H: 2}, screen.Cell{S: " ", W: 1})
	c.Text(0, 0, text, screen.Style{}, w)
	c.Text(0, 1, ui.PressEnter, screen.Style{}, w)
}

// TooSmall blanks c and centres "Window too small" and "20 by 6 needed".
func (Plain) TooSmall(c *screen.Canvas, s State) {
	c.Clear()
	w, h := c.Size()
	top := (h - 2) / 2
	if h < 2 {
		top = 0
	}
	for i, line := range []string{ui.TooSmall, ui.TooSmallNeed} {
		lw := min(ui.Width(line), w)
		c.Text((w-lw)/2, top+i, line, screen.Style{}, w)
	}
}

// Selection shows a selected cell in reverse video, or, when it is in
// reverse video already, without.
func (Plain) Selection(s State, st screen.Style) screen.Style {
	st.Attrs ^= screen.Reverse
	return st
}

// CopyCursor is the selection's look with an underline.
func (Plain) CopyCursor(s State, st screen.Style) screen.Style {
	st.Attrs ^= screen.Reverse
	st.Underline = 1
	return st
}

// Inactive is a cell of a stale pane: the character stays, its colours and
// attributes go, and it is faint.
func (Plain) Inactive(s State, st screen.Style) screen.Style {
	return screen.Style{Attrs: screen.Faint}
}
