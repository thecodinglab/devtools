package chrome

// The status row of the look (5a.6.2, 5a.6.3): the address, the tabs and
// the right group of the normal row; the prefix block over the address;
// the rows the prefix with its hints, switching, copy, scrolled and the
// confirm draw on their own. A row is built as a list of pieces (text and
// face at a column) from the State and a plan or modeRow of reduce.go, and
// then drawn into the canvas. Every word comes from internal/ui, every
// colour and glyph from State.Theme.

import (
	"slices"
	"strconv"
	"strings"

	"devtools/internal/client/screen"
	"devtools/internal/ui"
)

// The spacing units of 5a.6.1.
const (
	rowMargin = 1 // left and right end of the row
	rowSep    = 2 // between siblings
	rowGap    = 3 // between groups, and between hints
)

// The widths of 5a.6.2.
const (
	sessionMax = 32 // the session label, cut in the middle
	labelMax   = 16 // a tab's label, cut at the end
)

// blockSpace is the empty cells after a block that starts a row of its own
// (5a.9.1, "followed by two empty cells, then the text").
const blockSpace = 2

// piece is one run of text in one face at column x of the row.
type piece struct {
	x    int
	text string
	face ui.Face
	key  bool // a key named in the row: it is never cut
}

// line collects pieces from left to right.
type rowLine struct {
	pieces []piece
	x      int
}

func (b *rowLine) add(text string, f ui.Face) {
	if text == "" {
		return
	}
	b.pieces = append(b.pieces, piece{x: b.x, text: text, face: f})
	b.x += ui.Width(text)
}

func (b *rowLine) key(text string, f ui.Face) {
	b.pieces = append(b.pieces, piece{x: b.x, text: text, face: f, key: true})
	b.x += ui.Width(text)
}

func (b *rowLine) skip(n int) { b.x += n }

// shifted returns the pieces of b moved right by dx.
func (b *rowLine) shifted(dx int) []piece {
	out := make([]piece, len(b.pieces))
	for i, p := range b.pieces {
		p.x += dx
		out[i] = p
	}
	return out
}

// span is a piece of an entry of the right group, before it is placed.
type span struct {
	text string
	face ui.Face
}

// entry is one entry of the right group: a machine of the fleet strip, a
// count of the roll-up, the date or the clock.
type entry []span

// The faces of the status row.
func roleFace(th ui.Theme, r ui.Role) ui.Face { return th.Face(r, ui.GroundNone) }

func withBold(f ui.Face) ui.Face { f.Bold = true; return f }

// blockFace is the mode block: reverse video and bold, the default colours,
// in both tiers (5a.7.1).
var blockFace = ui.Face{Bold: true, Reverse: true}

// StatusRows is the number of rows reserved at the bottom: 1.
func (l *look) StatusRows() int { return 1 }

// Status draws the status row from s.Mode into row 0 of c, the status area.
func (l *look) Status(c *screen.Canvas, s State) {
	w, h := c.Size()
	if w <= 0 || h <= 0 {
		return
	}
	c.Fill(screen.Rect{X: 0, Y: 0, W: w, H: 1}, screen.Cell{S: " ", W: 1})
	pieces, _ := l.row(s, w)
	for _, p := range pieces {
		if p.x >= w {
			continue
		}
		c.Text(p.x, 0, p.text, statusStyle(p.face), w-p.x)
	}
}

// row builds the status row of s at width cells and returns its pieces and
// the cells it needs, its right margin included.
func (l *look) row(s State, width int) ([]piece, int) {
	switch s.Mode {
	case "confirm":
		return confirmRow(s, width)
	case "copy", "scrolled":
		return copyRow(s, width)
	case "switching":
		return switchingRow(s, width)
	case "prefix":
		if s.Hints && l.chords != nil {
			return l.hintRow(s, width)
		}
	}
	return addressRow(s, reduce(s, width), width)
}

// showsNotice reports whether the notice of s takes the place of the tabs:
// a notice is shown in the normal row only (and in the scrolled row, which
// draws it itself); in every other mode it waits (5a.10.1).
func showsNotice(s State) bool {
	return s.Notice != "" && s.Mode != "prefix" && s.Mode != "switching" &&
		s.Mode != "copy" && s.Mode != "scrolled" && s.Mode != "confirm"
}

// thisMachine is the name of this machine: Summary.Machines[0] (C9).
func thisMachine(s State) string {
	if len(s.Summary.Machines) > 0 {
		return s.Summary.Machines[0].Name
	}
	return ""
}

// sessionLabel is the session label at its widest, 32 cells cut in the
// middle.
func sessionLabel(s State) string { return s.Theme.CutMiddle(s.Session, sessionMax) }

// currentLabel is the label of the current tab at its widest.
func currentLabel(s State) string {
	for _, t := range s.Tabs {
		if t.Current {
			return s.Theme.CutEnd(t.Title, labelMax)
		}
	}
	return ""
}

// addressNeed is the number of cells the row of the address shape needs
// with plan p.
func addressNeed(s State, p plan, width int) int {
	_, need := addressRow(s, p, width)
	return need
}

// addressRow is the normal row, and the prefix row before its hints: the
// address (or the block over it), the tabs or the notice, and the right
// group against the right edge.
func addressRow(s State, p plan, width int) ([]piece, int) {
	th := s.Theme
	right := rightLine(s, p)
	b := &rowLine{}
	end := 0 // the end of the address
	nameEnd := 0
	if s.Machine == "" {
		if name := thisMachine(s); name != "" && p.local {
			b.skip(rowMargin)
			b.add(name, roleFace(th, ui.RoleComment))
			nameEnd, end = b.x, b.x
		}
	} else {
		// The chip starts at column 0: its own padding replaces the margin,
		// so its name is where the name of this machine would be.
		name := s.Machine
		if p.chip >= 0 {
			name = th.CutEnd(name, max(p.chip, 1))
		}
		text, f := th.Chip(name)
		b.add(text, f)
		nameEnd, end = rowMargin+ui.Width(name), b.x
	}
	if label := sessionLabel(s); label != "" && p.session != 0 {
		if p.session > 0 {
			label = th.CutMiddle(label, p.session)
		}
		if nameEnd > 0 {
			b.x = nameEnd + rowSep
		} else {
			b.x = rowMargin
		}
		b.add(label, withBold(roleFace(th, ui.RoleInk)))
		end = b.x
	}
	if s.Mode == "prefix" {
		w := max(end, ui.Width(" "+ui.ModePrefix+" "))
		b = &rowLine{}
		b.add(padCells(" "+ui.ModePrefix, w), blockFace)
		end = w
	}
	b.x = rowMargin
	if end > 0 {
		b.x = end + rowGap
		if p.blockSep && s.Mode == "prefix" {
			b.x = end + rowSep
		}
	}
	start := b.x
	rw := right.x
	rightNeed := 0
	if rw > 0 {
		rightNeed = rowGap + rw
	}
	if showsNotice(s) {
		text := s.Notice
		if p.notice > 0 {
			room := width - rowMargin - start - rightNeed
			if ui.Width(text) > room {
				floor := noticeFloor
				if p.notice > 1 {
					floor = 1
				}
				need := needOf(start+min(ui.Width(text), floor), rw)
				b.add(th.CutEnd(text, room), roleFace(th, ui.RoleInk))
				return append(b.pieces, right.shifted(width-rowMargin-rw)...), need
			}
		}
		b.add(text, roleFace(th, ui.RoleInk))
	} else {
		tabs(b, s, p)
	}
	leftEnd := end
	if b.x > start {
		leftEnd = b.x
	}
	return append(b.pieces, right.shifted(width-rowMargin-rw)...), needOf(leftEnd, rw)
}

// padCells pads s with spaces to w cells.
func padCells(s string, w int) string {
	if n := w - ui.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// tabs draws the tabs from b.x: "{index} {label}", the agent mark and the
// bell flag, sep apart; the tabs the plan leaves out as "+{n}" after the
// current one.
func tabs(b *rowLine, s State, p plan) {
	th := s.Theme
	hidden, first := 0, true
	for _, t := range s.Tabs {
		if !t.Current && !p.others {
			hidden++
			continue
		}
		if !first {
			b.skip(rowSep)
		}
		first = false
		label := th.CutEnd(t.Title, labelMax)
		f := roleFace(th, ui.RoleComment)
		switch {
		case t.Current:
			f = withBold(roleFace(th, ui.RoleAccent))
			if p.current >= 0 {
				label = th.CutEnd(label, p.current)
			}
		case !p.labels:
			label = ""
		}
		text := strconv.Itoa(t.Index)
		if label != "" {
			text += " " + label
		}
		b.add(text, f)
		if m, mf, ok := agentMarkOf(th, t.Agent); ok {
			b.skip(1)
			b.add(m, mf)
		}
		if t.Bell {
			b.skip(1)
			b.add(th.Glyph(ui.GlyphBell), roleFace(th, ui.RoleWarning))
		}
	}
	if hidden > 0 && p.plus {
		if !first {
			b.skip(rowSep)
		}
		b.add("+"+strconv.Itoa(hidden), roleFace(th, ui.RoleComment))
	}
}

// agentMarkOf is the mark of an agent status and its face: blocked in
// warning, done in ink, working in muted. Idle and none have no mark in
// the status row.
func agentMarkOf(th ui.Theme, status string) (string, ui.Face, bool) {
	switch status {
	case "blocked":
		return th.Glyph(ui.GlyphBlocked), roleFace(th, ui.RoleWarning), true
	case "done":
		return th.Glyph(ui.GlyphDone), roleFace(th, ui.RoleInk), true
	case "working":
		return th.Glyph(ui.GlyphWorking), roleFace(th, ui.RoleMuted), true
	}
	return "", ui.Face{}, false
}

// rightLine is the right group from column 0: the fleet strip, the roll-up,
// the date and the clock, the entries of a group sep apart, the groups gap
// apart.
func rightLine(s State, p plan) *rowLine {
	b := &rowLine{}
	firstGroup := true
	for _, g := range [][]entry{fleetEntries(s, p), rollupEntries(s, p), timeEntries(s, p)} {
		if len(g) == 0 {
			continue
		}
		if !firstGroup {
			b.skip(rowGap)
		}
		firstGroup = false
		for i, e := range g {
			if i > 0 {
				b.skip(rowSep)
			}
			for _, sp := range e {
				if strings.TrimSpace(sp.text) == "" {
					b.skip(ui.Width(sp.text))
					continue
				}
				b.add(sp.text, sp.face)
			}
		}
	}
	return b
}

// fleetEntries is the fleet strip: one entry per configured machine other
// than this one, in config order, by state (5a.6.2).
func fleetEntries(s State, p plan) []entry {
	if !p.fleet || len(s.Summary.Machines) < 2 {
		return nil
	}
	th := s.Theme
	var out []entry
	for _, m := range s.Summary.Machines[1:] {
		switch m.State {
		case "online":
			if p.online {
				out = append(out, entry{{m.Name, roleFace(th, ui.RoleComment)}})
			}
		case "connecting":
			out = append(out, entry{{m.Name + " " + th.Glyph(ui.GlyphEllipsis), roleFace(th, ui.RoleMuted)}})
		case "retrying":
			out = append(out, entry{{m.Name + " " + ui.FleetRetrying, roleFace(th, ui.RoleMuted)}})
		case "attention":
			e := entry{{m.Name, roleFace(th, ui.RoleInk)}, {" ", ui.Face{}}, {th.Glyph(ui.GlyphFailed), roleFace(th, ui.RoleError)}}
			if word := ui.ReasonWord(m.Problem); word != "" && p.reasons {
				e = append(e, span{" ", ui.Face{}}, span{word, roleFace(th, ui.RoleInk)})
			}
			out = append(out, e)
		case "off":
			if m.Busy > 0 {
				out = append(out, entry{{m.Name + " " + ui.FleetOff, roleFace(th, ui.RoleComment)}})
			}
		}
	}
	return out
}

// rollupEntries is the roll-up: the blocked, done and working counts that
// are above zero, the place of the most urgent agent after its count.
func rollupEntries(s State, p plan) []entry {
	th := s.Theme
	urgent := ""
	if p.urgent {
		urgent = urgentStatus(s)
	}
	var out []entry
	for _, st := range []string{"blocked", "done", "working"} {
		n := s.Summary.Counts[st]
		if n <= 0 || (st == "blocked" && !p.blocked) || (st != "blocked" && !p.counts) {
			continue
		}
		mark, f, _ := agentMarkOf(th, st)
		text := mark + " " + ui.Number(int64(n))
		if word := ui.RollupWord(st, n); word != "" && p.words {
			text += " " + word
		}
		e := entry{{text, f}}
		if st == urgent {
			place := urgentPlace(s)
			words := strings.TrimSuffix(ui.UrgentPlace(place, n), place)
			e = append(e, span{" ", ui.Face{}}, span{strings.TrimSpace(words), roleFace(th, ui.RoleComment)},
				span{" ", ui.Face{}}, span{place, roleFace(th, ui.RoleInk)})
		}
		out = append(out, e)
	}
	return out
}

// urgentStatus is the status of Summary.Urgent, whose count the place
// follows: "blocked" while a blocked agent is not on screen, else "done";
// "" when no agent is named or its count is zero.
//
// Summary.Urgent carries the machine and the session but not the status.
// It is the most urgent blocked or done agent that is not on screen, so it
// is blocked exactly when more agents are blocked than the session on
// screen shows; the tabs of that session are what the look knows of it,
// and a tab marks its most urgent agent once.
func urgentStatus(s State) string {
	if s.Summary.Urgent == (Target{}) {
		return ""
	}
	shown := 0
	for _, t := range s.Tabs {
		if t.Agent == "blocked" {
			shown++
		}
	}
	st := "done"
	if s.Summary.Counts["blocked"] > shown {
		st = "blocked"
	}
	if s.Summary.Counts[st] <= 0 {
		return ""
	}
	return st
}

// urgentPlace is "{machine} {session}" of the most urgent agent, the
// machine left out for this machine.
func urgentPlace(s State) string {
	u := s.Summary.Urgent
	if u.Machine == "" || u.Machine == thisMachine(s) {
		return u.Session
	}
	if u.Session == "" {
		return u.Machine
	}
	return u.Machine + " " + u.Session
}

// timeEntries is the date in comment and the clock in ink.
func timeEntries(s State, p plan) []entry {
	if s.Now.IsZero() {
		return nil
	}
	th := s.Theme
	var out []entry
	if p.date {
		out = append(out, entry{{ui.Date(s.Now), roleFace(th, ui.RoleComment)}})
	}
	if p.clock {
		out = append(out, entry{{ui.Clock(s.Now), roleFace(th, ui.RoleInk)}})
	}
	return out
}

// ownBlock starts a row of its own: the block of the mode at column 0 and
// two empty cells after it.
func ownBlock(word string) *rowLine {
	b := &rowLine{}
	b.add(" "+word+" ", blockFace)
	b.skip(blockSpace)
	return b
}

// needOf is the cells a row needs whose left part ends at leftEnd and whose
// right part, gap apart, is rw cells wide; the right margin included.
func needOf(leftEnd, rw int) int {
	if rw > 0 {
		return leftEnd + rowGap + rw + rowMargin
	}
	return leftEnd + rowMargin
}

// switchingSession is "{session}…": the session of the pending switch with
// the mark of a wait.
func switchingSession(s State) string {
	return s.Target.Session + s.Theme.Glyph(ui.GlyphEllipsis)
}

// switchingRow is " switching " and "to {machine} {session}…"; the right
// group stays as the normal row has it and is cut further by the cuts of
// the mode.
func switchingRow(s State, width int) ([]piece, int) {
	normal := s
	normal.Mode, normal.Notice = "normal", ""
	build := func(r modeRow) ([]piece, int) {
		th := s.Theme
		right := rightLine(s, r.plan)
		b := ownBlock(ui.ModeSwitching)
		var words []string
		if r.to {
			words = append(words, ui.SwitchingTo)
		}
		if r.machine && s.Target.Machine != "" {
			words = append(words, s.Target.Machine)
		}
		session := switchingSession(s)
		if r.session >= 0 {
			session = th.CutEnd(session, r.session)
		}
		words = append(words, session)
		b.add(strings.Join(words, " "), roleFace(th, ui.RoleInk))
		return append(b.pieces, right.shifted(width-rowMargin-right.x)...), needOf(b.x, right.x)
	}
	r := cutRow("switching", s, fullRow(reduce(normal, width)), width, func(r modeRow) int {
		_, n := build(r)
		return n
	})
	return build(r)
}

// selectingPhrase is the phrase of copy mode's selection.
func selectingPhrase(kind string) string {
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

// copyRow is " copy " or " scrolled ", the position, the selecting phrase
// (copy) or the notice (scrolled), and at the right end the hints of copy
// mode or "any key returns".
func copyRow(s State, width int) ([]piece, int) {
	th := s.Theme
	build := func(r modeRow) ([]piece, int) {
		word := ui.ModeCopy
		if s.Mode == "scrolled" {
			word = ui.ModeScrolled
		}
		b := ownBlock(word)
		if s.Copy != nil {
			b.add(ui.CopyPosition(s.Copy.Line, s.Copy.Total, r.position), roleFace(th, ui.RoleInk))
		}
		right := &rowLine{}
		if s.Mode == "copy" {
			if phrase := selectingPhrase(copySelecting(s)); phrase != "" && r.phrase {
				b.skip(rowGap)
				b.add(phrase, roleFace(th, ui.RoleInk))
			}
			hints := []hint{{"v", ui.HintSelect}, {"y", ui.HintCopy}, {"q", ui.HintLeave}}
			for i, h := range hints[r.hints:] {
				if i > 0 {
					right.skip(rowGap)
				}
				drawHint(right, th, h, -1)
			}
		} else {
			if s.Notice != "" && r.notice != 0 {
				text := s.Notice
				if r.notice > 0 {
					text = th.CutEnd(text, r.notice)
				}
				b.skip(rowGap)
				b.add(text, roleFace(th, ui.RoleInk))
			}
			if r.anyKey {
				right.add(ui.AnyKeyReturns, roleFace(th, ui.RoleComment))
			}
		}
		return append(b.pieces, right.shifted(width-rowMargin-right.x)...), needOf(b.x, right.x)
	}
	r := cutRow(s.Mode, s, fullRow(plan{}), width, func(r modeRow) int {
		_, n := build(r)
		return n
	})
	return build(r)
}

func copySelecting(s State) string {
	if s.Copy == nil {
		return ""
	}
	return s.Copy.Selecting
}

// confirmProgram is the program the confirm names.
func confirmProgram(s State) string {
	if s.Confirm == nil {
		return ""
	}
	return s.Confirm.Program
}

// confirmRow takes the whole row: margin, the question in ink, then
// "⏎ close pane" and "esc cancel" as hints, gap apart.
func confirmRow(s State, width int) ([]piece, int) {
	th := s.Theme
	build := func(r modeRow) ([]piece, int) {
		b := &rowLine{}
		b.skip(rowMargin)
		program := confirmProgram(s)
		if r.program >= 0 {
			program = th.CutEnd(program, r.program)
		}
		short := r.question
		if program == "" {
			short = 2
		}
		b.add(ui.ConfirmClose(program, short), roleFace(th, ui.RoleInk))
		space := rowGap
		if r.sep {
			space = rowSep
		}
		for _, h := range []hint{{th.Glyph(ui.GlyphEnter), ui.ConfirmClosePane}, {"esc", ui.ConfirmCancel}} {
			b.skip(space)
			if !r.labels {
				h.label = ""
			}
			drawHint(b, th, h, -1)
		}
		return b.pieces, needOf(b.x, 0)
	}
	r := cutRow("confirm", s, fullRow(plan{}), width, func(r modeRow) int {
		_, n := build(r)
		return n
	})
	return build(r)
}

// hint is a key and its label.
type hint struct{ key, label string }

func (h hint) width() int {
	if h.label == "" {
		return ui.Width(h.key)
	}
	return ui.Width(h.key) + 1 + ui.Width(h.label)
}

// drawHint draws "{key} {label}", the key in muted, the label in comment
// and cut at the end to label cells unless label is -1. The key is never
// cut.
func drawHint(b *rowLine, th ui.Theme, h hint, label int) {
	b.key(h.key, roleFace(th, ui.RoleMuted))
	text := h.label
	if label >= 0 {
		text = th.CutEnd(text, label)
	}
	if text != "" {
		b.skip(1)
		b.add(text, roleFace(th, ui.RoleComment))
	}
}

// moreHint is the hint that shows the next page.
var moreHint = hint{"?", ui.HintMore}

// escHint is the first hint of the strip.
var escHint = hint{"esc", ui.HintCancel}

// longestHintLabel is the widest label of the strip.
func longestHintLabel() int {
	n := ui.Width(moreHint.label)
	for _, o := range ui.HintOrder() {
		n = max(n, ui.Width(ui.HintLabel(o[0], o[1])))
	}
	return max(n, ui.Width(escHint.label))
}

// hints is the hint strip of 5a.11.2: "esc cancel", then one hint per
// action of ui.HintOrder that has a chord after the prefix, with the first
// such chord as its key. An action without one is left out.
func (l *look) hints() []hint {
	out := []hint{escHint}
	for _, o := range ui.HintOrder() {
		if k := l.hintKey(o[0], o[1]); k != "" {
			out = append(out, hint{k, ui.HintLabel(o[0], o[1])})
		}
	}
	return out
}

// afterPrefix is the keys of the chords that follow the prefix.
func (l *look) afterPrefix(action, arg string) []string {
	if l.chords == nil {
		return nil
	}
	var out []string
	for _, c := range l.chords(action, arg) {
		if k, ok := strings.CutPrefix(c, "prefix+"); ok && k != "" {
			out = append(out, k)
		}
	}
	return out
}

// hintKey is the key the strip shows for an action: the first chord after
// the prefix; for tab-select the range of its digits ("1-9"); for
// session-prev the keys of session-prev and session-next ("( )").
func (l *look) hintKey(action, arg string) string {
	switch action {
	case "tab-select":
		ks := l.afterPrefix(action, "")
		if len(ks) == 0 {
			return ""
		}
		return digitRange(ks)
	case "session-prev":
		var ks []string
		for _, a := range []string{"session-prev", "session-next"} {
			if k := l.afterPrefix(a, ""); len(k) > 0 {
				ks = append(ks, k[0])
			}
		}
		return strings.Join(ks, " ")
	}
	if ks := l.afterPrefix(action, arg); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// digitRange is "{first}-{last}" when the keys are single digits without a
// hole, else the first key.
func digitRange(ks []string) string {
	var ds []int
	for _, k := range ks {
		if len(k) != 1 || k[0] < '0' || k[0] > '9' {
			return ks[0]
		}
		ds = append(ds, int(k[0]-'0'))
	}
	slices.Sort(ds)
	ds = slices.Compact(ds)
	if len(ds) == 1 {
		return strconv.Itoa(ds[0])
	}
	if ds[len(ds)-1]-ds[0] != len(ds)-1 {
		return ks[0]
	}
	return strconv.Itoa(ds[0]) + "-" + strconv.Itoa(ds[len(ds)-1])
}

// stripWidth is the cells of hints joined gap apart.
func stripWidth(hs []hint) int {
	n := 0
	for i, h := range hs {
		if i > 0 {
			n += rowGap
		}
		n += h.width()
	}
	return n
}

// hintPages splits the strip into pages of avail cells (5a.11.2): one page
// when every hint fits, else pages filled in order with as many hints as
// fit before a closing "? more", at least one hint each.
func hintPages(hs []hint, avail int) [][]hint {
	if stripWidth(hs) <= avail {
		return [][]hint{hs}
	}
	room := avail - rowGap - moreHint.width()
	var pages [][]hint
	var page []hint
	for _, h := range hs {
		if len(page) > 0 && stripWidth(append(slices.Clip(page), h)) > room {
			pages = append(pages, page)
			page = nil
		}
		page = append(page, h)
	}
	if len(page) > 0 {
		pages = append(pages, page)
	}
	return pages
}

// hintStart is the column the hints start at: after the block " prefix "
// and its two empty cells.
var hintStart = ui.Width(" "+ui.ModePrefix+" ") + blockSpace

// hintRow is " prefix " and page State.HintPage of the hint strip, "? more"
// at its end when there is more than one page. The right group is hidden.
func (l *look) hintRow(s State, width int) ([]piece, int) {
	th := s.Theme
	pages := hintPages(l.hints(), width-rowMargin-hintStart)
	n := len(pages)
	page := pages[((s.HintPage%n)+n)%n]
	build := func(r modeRow) ([]piece, int) {
		b := ownBlock(ui.ModePrefix)
		for i, h := range page {
			if i > 0 {
				b.skip(rowGap)
			}
			drawHint(b, th, h, r.label)
		}
		if n > 1 {
			b.skip(rowGap)
			more := moreHint
			if !r.more {
				more.label = ""
			}
			drawHint(b, th, more, -1)
		}
		return b.pieces, needOf(b.x, 0)
	}
	r := cutRow("prefix", s, fullRow(plan{}), width, func(r modeRow) int {
		_, n := build(r)
		return n
	})
	return build(r)
}

// statusStyle is a face of the theme as a style of the canvas.
func statusStyle(f ui.Face) screen.Style {
	st := screen.Style{Fg: statusColor(f.Fg), Bg: statusColor(f.Bg)}
	if f.Bold {
		st.Attrs |= screen.Bold
	}
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

// statusColor is a paint of the theme as a colour of the canvas.
func statusColor(p ui.Paint) screen.Color {
	switch p.Kind {
	case ui.PaintPalette:
		return screen.Color{Kind: 1, V: p.V}
	case ui.PaintRGB:
		return screen.Color{Kind: 3, V: p.V}
	}
	return screen.Color{}
}
