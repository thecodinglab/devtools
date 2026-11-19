package hub

// The look of the hub (5a.6.7, 5a.9.2, 5a.15.1): one column of
// ui.Measure(width) cells starting at ui.Centre, the scope bar, machine
// sections with chip headings, rows with a glyph and a word, stale rows,
// the agents scope, the selected row across the measure, the confirm row
// and the footer. It reads the model through the accessors of access.go and
// the rows refilter built; it never asks which host or which backend it
// runs under, only Features (H12). Every word comes from internal/ui
// (5a.14).

import (
	"strings"

	"devtools/internal/ui"

	"github.com/charmbracelet/x/ansi"
)

// The spacing units of 5a.6.1, in cells.
const (
	gutter   = 2 // left of every row; column 0 holds the selection bar or the here mark
	indent   = 2 // a worktree under its project
	gap      = 3 // between columns, between hints
	sep      = 2 // between a heading's chip and its phrase; between scopes
	margin   = 2 // right of the scope bar and of every row
	minTitle = 8 // a title is drawn only when at least this many cells are free

	// closingRows is the least height at which the hub closes itself off
	// from the status row with a rule; a shorter hub keeps the row for its
	// list.
	closingRows = 16

	// minRule is the least length of the rule after a machine heading; a
	// shorter one is left out.
	minRule = 3
)

// thisMachine is this machine's name, for the machine column of the agents
// scope and the one heading this machine gets: ModelOptions.Name (contract
// change 9), "" while the host does not know it.
func (m *Model) thisMachine() string { return m.opts.Name }

// seg is a run of text in one role on one ground.
type seg struct {
	text         string
	role         ui.Role
	ground       ui.Ground
	bold, italic bool
}

// line is one row of the column; a selected row is filled with the raised
// ground across the measure.
type line struct {
	segs     []seg
	selected bool
	rule     bool // a spacer drawn as a rule: it gives way like an empty row
}

func spaces(n int) seg { return seg{text: strings.Repeat(" ", max(0, n))} }

func segsWidth(segs []seg) int {
	w := 0
	for _, s := range segs {
		w += ui.Width(s.text)
	}
	return w
}

// originX is the first column of the hub's column in a pane area of width
// cells (5a.6.7): the x origin Cursor adds.
func (m *Model) originX(width int) int { return ui.Centre(width, ui.Measure(width)) }

// View returns the hub drawn for width by height cells: height lines of at
// most width cells, joined by "\n". The content is one column of
// ui.Measure(width) cells starting at originX (5a.6.7). There is no
// alternate screen: the standalone host asks for one.
func (m *Model) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	measure := ui.Measure(width)
	pad := strings.Repeat(" ", m.originX(width))
	lines := m.lines(measure, height)
	out := make([]string, len(lines))
	for i, l := range lines {
		if s := m.render(l, measure); s != "" {
			out[i] = pad + s
		}
	}
	return strings.Join(out, "\n")
}

// style renders s, on the raised ground when its row is selected and the
// theme has that ground.
func (m *Model) style(s seg, selected bool) string {
	ground := s.ground
	if selected && m.theme.HasGround(ui.GroundRaised) {
		ground = ui.GroundRaised
	}
	st := m.theme.Style(s.role, ground)
	if s.bold {
		st = st.Bold(true)
	}
	if s.italic {
		st = st.Italic(true)
	}
	return st.Render(s.text)
}

// render draws a line in w cells: what does not fit is cut at the end, and
// a selected row is filled to w.
func (m *Model) render(l line, w int) string {
	segs := l.segs
	// the padding after the last cell of a row that is not filled is not
	// drawn
	for !l.selected && len(segs) > 0 && segs[len(segs)-1].text == strings.Repeat(" ", len(segs[len(segs)-1].text)) &&
		segs[len(segs)-1].ground == ui.GroundNone {
		segs = segs[:len(segs)-1]
	}
	var b strings.Builder
	used := 0
	for _, s := range segs {
		if used >= w {
			break
		}
		if sw := ui.Width(s.text); used+sw > w {
			s.text = ansi.Truncate(s.text, w-used, "")
		}
		if s.text == "" {
			continue
		}
		used += ui.Width(s.text)
		b.WriteString(m.style(s, l.selected))
	}
	if l.selected && used < w {
		b.WriteString(m.style(spaces(w-used), true))
	}
	return b.String()
}

// lines is the hub in a column of w cells and h rows: the input and the
// rule that is its edge, the list, the create row, the row of the question,
// the footer and the rule that closes the hub off from the status row
// below it. In a window too short for everything the list gives way first,
// then the empty row above the footer, the closing rule and the input's
// rule; the input, the create row and the footer stay.
func (m *Model) lines(w, h int) []line {
	top := []line{m.inputLine(w), m.ruleLine(w)}
	list := m.listLines(w, h)
	var create []line
	if m.create != nil {
		if len(list) > 0 {
			create = append(create, line{})
		}
		create = append(create, m.createLine(w))
	}
	footer := m.footerLines(w) // the question row, or an empty row; the hints
	bottom := append([]line(nil), footer...)
	if h >= closingRows {
		bottom = append(bottom, m.ruleLine(w))
	}
	if room := h - len(top) - len(create) - len(bottom); len(list) > max(0, room) {
		list = list[:max(0, room)]
		if len(list) == 0 && len(create) > 1 {
			create = create[1:]
		}
	}
	body := append(append(top, list...), create...)
	// still too tall: the spacers give way in their order
	over := func() bool { return len(body)+len(bottom) > h }
	if over() && len(footer[0].segs) == 0 {
		bottom = bottom[1:]
	}
	if over() && len(bottom) > 0 && bottom[len(bottom)-1].rule {
		bottom = bottom[:len(bottom)-1]
	}
	if over() && len(body) > 1 && (len(body[1].segs) == 0 || body[1].rule) {
		body = append(body[:1], body[2:]...)
	}
	for !over() && len(body)+len(bottom) < h {
		body = append(body, line{})
	}
	lines := append(body, bottom...)
	if len(lines) > h {
		// the footer's hints are the last thing to go
		hints := lines[len(lines)-1]
		if lines[len(lines)-1].rule {
			hints = lines[len(lines)-2]
		}
		lines = append(lines[:max(0, h-1)], hints)
		lines = lines[len(lines)-h:]
	}
	return lines
}

// prompt is the input's prompt and its space.
func (m *Model) prompt() string { return m.theme.Glyph(ui.GlyphPrompt) + " " }

// scopeBar is the scopes the host has, the current one in accent and bold,
// sep apart; only the current one below a measure of 60, nothing below 30
// (5a.6.7).
func (m *Model) scopeBar(w int) []seg {
	if w < 30 {
		return nil
	}
	current := m.currentScope()
	var segs []seg
	for _, name := range m.scopeNames() {
		if w < 60 && name != current {
			continue
		}
		if len(segs) > 0 {
			segs = append(segs, spaces(sep))
		}
		if name == current {
			segs = append(segs, seg{text: name, role: ui.RoleAccent, bold: true})
		} else {
			segs = append(segs, seg{text: name, role: ui.RoleComment})
		}
	}
	return segs
}

// inputRoom is the width the query or the placeholder has in a column of w
// cells: what the gutter, the prompt and the scope bar with its gap and
// margin leave.
func (m *Model) inputRoom(w int) int {
	room := w - gutter - ui.Width(m.prompt())
	if bar := segsWidth(m.scopeBar(w)); bar > 0 {
		room -= gap + bar + margin
	}
	return max(1, room)
}

// inputText is the part of the query that is shown, and the column of the
// cursor in it.
func (m *Model) inputText(w int) (string, int) {
	text := m.queryText()
	room := m.inputRoom(w)
	runes := []rune(text)
	pos := min(m.input.Position(), len(runes))
	col := ui.Width(string(runes[:pos]))
	if width := ui.Width(text); width > room {
		cut := width - room
		text = ansi.TruncateLeft(text, cut, "")
		col = max(0, col-cut)
	}
	return text, col
}

// inputCursor is the column of the terminal cursor in a hub of width cells.
func (m *Model) inputCursor(width int) int {
	_, col := m.inputText(ui.Measure(width))
	return m.originX(width) + gutter + ui.Width(m.prompt()) + col
}

// inputLine is row 0: the prompt, the query or the placeholder in comment,
// and the scope bar right-aligned with a margin of 2.
func (m *Model) inputLine(w int) line {
	segs := []seg{spaces(gutter), {text: m.prompt()}}
	if text, _ := m.inputText(w); text != "" {
		segs = append(segs, seg{text: text})
	} else {
		placeholder := ui.Placeholder(m.currentScope(), m.newMode)
		segs = append(segs, seg{text: m.theme.CutEnd(placeholder, m.inputRoom(w)), role: ui.RoleComment})
	}
	if bar := m.scopeBar(w); len(bar) > 0 {
		segs = append(segs, spaces(w-margin-segsWidth(segs)-segsWidth(bar)))
		segs = append(segs, bar...)
	}
	return line{segs: segs}
}

// fixedLines is everything around the list: the input and its rule, the
// empty row above the footer, the footer and the closing rule, and the
// create row and its empty row, for a hub of h rows.
func (m *Model) fixedLines(h int) int {
	n := 4
	if h >= closingRows {
		n++
	}
	if m.create != nil {
		n += 2
	}
	return n
}

func (m *Model) listHeight(h int) int {
	return max(1, h-m.fixedLines(h))
}

// dkind is what one line of the list shows.
type dkind int

const (
	dBlank   dkind = iota // an empty row
	dNote                 // a text in place of rows
	dHeading              // a machine heading
	dProject              // a project heading
	dItem                 // a row the cursor can stop on, unless it is stale
)

// dline is one line of the list; row is the index into Model.rows, -1 for
// a line the drawing adds.
type dline struct {
	kind dkind
	row  int
}

// display is the list as lines. A project whose main worktree is a row is
// drawn as its heading with the main worktree indented under it, like its
// features (hub-all-80x24); a project without one gets the heading alone.
// While this machine has not answered yet and says nothing about it, its
// place keeps two empty rows, so that the headings below do not move when
// "Loading worktrees…" appears after ui.Slow.
func (m *Model) display() []dline {
	var ds []dline
	if m.newMode && m.queryText() == "" {
		return []dline{{kind: dNote, row: -1}}
	}
	if m.scope != scopeAgents && m.localLoading() && !m.slow && len(m.rows) > 0 && m.rows[0].kind == rowHost {
		ds = append(ds, dline{kind: dBlank, row: -1}, dline{kind: dBlank, row: -1})
	}
	for i, r := range m.rows {
		switch r.kind {
		case rowSpace:
			ds = append(ds, dline{kind: dBlank, row: i})
		case rowNote:
			ds = append(ds, dline{kind: dNote, row: i})
		case rowHost:
			ds = append(ds, dline{kind: dHeading, row: i})
		case rowHeader:
			ds = append(ds, dline{kind: dProject, row: i})
		default:
			item := m.items[r.item]
			if item.Kind != KindAgent && item.grouped() && !r.child {
				ds = append(ds, dline{kind: dProject, row: i})
			}
			ds = append(ds, dline{kind: dItem, row: i})
		}
	}
	return ds
}

// localLoading reports whether this machine's list for the scope has not
// arrived yet.
func (m *Model) localLoading() bool {
	local := m.machines[0]
	return !local.loaded || m.scope == scopeSessions && !local.sessionsKnown
}

// selectedRow is the index into Model.rows of the selected item, -1 on the
// create row or with nothing selectable.
func (m *Model) selectedRow() int {
	if rows := m.selectable(); m.cursor >= 0 && m.cursor < len(rows) {
		return rows[m.cursor]
	}
	return -1
}

// scroll is the first list line shown for a list of height lines, starting
// from offset and moved no more than needed to show the selected row, with
// the headings above it when they fit.
func (m *Model) scroll(offset, height int) int {
	ds := m.display()
	if sel := m.selectedRow(); sel >= 0 {
		at := -1
		for i, d := range ds {
			if d.kind == dItem && d.row == sel {
				at = i
				break
			}
		}
		if at >= 0 {
			top := at
			for top > 0 && ds[top-1].kind != dItem {
				top--
			}
			if at-top >= height {
				top = at
			}
			if top < offset {
				offset = top
			}
			if at >= offset+height {
				offset = at - height + 1
			}
		}
	}
	return max(0, min(offset, len(ds)-height))
}

func (m *Model) clampScroll() {
	m.offset = m.scroll(m.offset, m.listHeight(m.height))
}

// columns are the widths of the columns, computed from every item the hub
// holds, not from the filtered ones, so typing a query moves no column
// (5a.10.2), and which of them the measure has room for.
type columns struct {
	name, changes, sync, state int
	showChanges, showSync      bool

	// the agents scope
	agentState, machine, session, tab int
	showMachine, showTab              bool
}

func (m *Model) columns(w int) columns {
	var c columns
	machines := m.hasMachines() && len(m.machines) > 1
	for _, item := range m.items {
		state, _ := m.stateCell(item)
		if item.Kind == KindAgent {
			c.agentState = max(c.agentState, ui.Width(state))
			if machines {
				c.machine = max(c.machine, ui.Width(m.machineName(item)))
			}
			c.session = max(c.session, ui.Width(item.Session))
			c.tab = max(c.tab, ui.Width(agentTab(item)))
			continue
		}
		if item.grouped() {
			c.name = max(c.name, indent+ui.Width(item.Worktree))
		} else {
			c.name = max(c.name, ui.Width(item.Label))
		}
		git := m.gitOf(item)
		c.changes = max(c.changes, ui.Width(ui.Changes(git)))
		c.sync = max(c.sync, ui.Width(m.sync(git)))
		c.state = max(c.state, ui.Width(state))
	}
	c.name = min(c.name, max(16, w*2/5))
	c.session = min(c.session, max(12, w/3))

	// narrow: the title leaves first (by its own room), then sync, then
	// changes; the state stays (5a.6.7)
	cell := func(width int) int {
		if width == 0 {
			return 0
		}
		return gap + width
	}
	c.showChanges, c.showSync = true, true
	full := func() int {
		n := gutter + c.name + cell(c.state)
		if c.showChanges {
			n += cell(c.changes)
		}
		if c.showSync {
			n += cell(c.sync)
		}
		return n
	}
	if full()+margin > w {
		c.showSync = false
	}
	if full()+margin > w {
		c.showChanges = false
	}
	if full()+margin > w {
		c.name = max(min(c.name, 4), w-margin-gutter-cell(c.state))
	}

	// the agents scope: the tab leaves, then the machine; the session label
	// is cut in the middle
	c.showMachine, c.showTab = c.machine > 0, true
	agents := func() int {
		n := gutter + c.agentState + gap + c.session
		if c.showMachine {
			n += gap + c.machine
		}
		if c.showTab {
			n += cell(c.tab)
		}
		return n
	}
	if agents()+margin > w {
		c.showTab = false
	}
	if agents()+margin > w {
		c.showMachine = false
	}
	if agents()+margin > w {
		c.session = max(1, w-margin-gutter-c.agentState-gap)
	}
	return c
}

// machineName is the name of an item's machine as the machine column shows
// it: this machine's own name for its rows.
func (m *Model) machineName(item Item) string {
	if item.Machine == "" {
		return m.thisMachine()
	}
	return item.Machine
}

func agentTab(item Item) string {
	if item.TabIndex <= 0 {
		return item.Agent
	}
	return ui.Number(int64(item.TabIndex)) + " " + item.Agent
}

// sync is the sync cell: ahead and behind with the theme's glyphs.
func (m *Model) sync(git ui.Git) string {
	if !git.HasUpstream {
		return ""
	}
	var parts []string
	if git.Ahead > 0 {
		parts = append(parts, m.theme.Glyph(ui.GlyphAhead)+ui.Number(int64(git.Ahead)))
	}
	if git.Behind > 0 {
		parts = append(parts, m.theme.Glyph(ui.GlyphBehind)+ui.Number(int64(git.Behind)))
	}
	return strings.Join(parts, " ")
}

// stateCell is the state of a row as a glyph and a word (5a.6.7) and its
// role: ◆ needs you, ✓ done, ● working, ○ idle, and open without a glyph
// but in the column of the words; nothing for a row without an open
// session.
func (m *Model) stateCell(item Item) (string, ui.Role) {
	status := item.State.Agent
	if item.Kind == KindAgent {
		status = item.Status
	} else if !item.State.Open {
		return "", ui.RoleInk
	}
	word := ui.AgentWord(status)
	glyph := func(g ui.Glyph) string { return m.theme.Glyph(g) + " " + word }
	switch status {
	case "blocked":
		return glyph(ui.GlyphBlocked), ui.RoleWarning
	case "done":
		return glyph(ui.GlyphDone), ui.RoleInk
	case "working":
		return glyph(ui.GlyphWorking), ui.RoleMuted
	case "idle":
		return glyph(ui.GlyphIdle), ui.RoleComment
	}
	return strings.Repeat(" ", ui.Width(m.theme.Glyph(ui.GlyphBlocked))+1) + word, ui.RoleComment
}

// listLines is the part of the list a column of w cells and a hub of h rows
// shows.
func (m *Model) listLines(w, h int) []line {
	ds := m.display()
	if len(ds) == 0 {
		return nil
	}
	height := m.listHeight(h)
	offset := m.scroll(m.offset, height)
	selected := m.selectedRow()
	cols := m.columns(w)
	var lines []line
	for i := offset; i < len(ds) && i < offset+height; i++ {
		lines = append(lines, m.displayLine(ds, i, cols, selected, w))
	}
	return lines
}

// staleAt reports whether the line at i belongs to a machine that is not
// online: a project heading takes the state of the row under it.
func (m *Model) staleAt(ds []dline, i int) bool {
	for j := i; j < len(ds) && ds[j].row >= 0; j++ {
		r := m.rows[ds[j].row]
		if r.kind == rowItem {
			return m.items[r.item].Stale
		}
		if ds[j].kind != dProject {
			return false
		}
	}
	return false
}

func (m *Model) displayLine(ds []dline, i int, cols columns, selected, w int) line {
	d := ds[i]
	switch d.kind {
	case dBlank:
		return line{}
	case dNote:
		text := ui.Empty("name", "")
		if d.row >= 0 {
			text = m.rows[d.row].project
		}
		return line{segs: []seg{spaces(gutter), {text: m.theme.CutEnd(text, w-gutter), role: ui.RoleComment}}}
	case dHeading:
		return m.machineHeading(m.machines[m.rows[d.row].machine], w)
	case dProject:
		r := m.rows[d.row]
		segs := []seg{spaces(gutter)}
		segs = append(segs, highlight(m.theme.CutEnd(r.project, w-gutter-margin), r.projectHits, seg{role: ui.RoleStrong})...)
		if m.staleAt(ds, i) {
			staleSegs(segs)
		}
		return line{segs: segs}
	}
	r := m.rows[d.row]
	item := m.items[r.item]
	on := d.row == selected
	var segs []seg
	if item.Kind == KindAgent {
		segs = m.agentSegs(r, item, cols, on, w)
	} else {
		segs = m.itemSegs(r, item, cols, on, w)
	}
	if item.Stale {
		staleSegs(segs)
	}
	return line{segs: segs, selected: on}
}

// staleSegs draws a row of a machine that is not online: comment and
// italic (5a.6.7).
func staleSegs(segs []seg) {
	for i := range segs {
		if strings.TrimSpace(segs[i].text) == "" {
			continue
		}
		segs[i].role, segs[i].bold, segs[i].italic = ui.RoleComment, false, true
	}
}

// gutterSegs is the gutter of a row: column 0 holds the selection bar on
// the selected row, in the accent when that is the row you are in, or the
// here mark on the row you are in; column 1 stays blank. Thickness says
// selected, the accent says here, so the two never stand side by side.
func (m *Model) gutterSegs(here, selected bool) []seg {
	mark := spaces(1)
	switch {
	case selected && here:
		mark = seg{text: m.theme.Glyph(ui.GlyphSelected), role: ui.RoleAccent}
	case selected:
		mark = seg{text: m.theme.Glyph(ui.GlyphSelected), role: ui.RoleStrong}
	case here:
		mark = seg{text: m.theme.Glyph(ui.GlyphHere), role: ui.RoleAccent}
	}
	return []seg{mark, spaces(1)}
}

// nameBase is the run a row's name is drawn in: the accent on the row you
// are in, bold on the selected row.
func nameBase(here, selected bool) seg {
	s := seg{role: ui.RoleInk}
	if here {
		s.role = ui.RoleAccent
	}
	if selected {
		s.bold = true
		if !here {
			s.role = ui.RoleStrong
		}
	}
	return s
}

// machineHeading is the heading of another machine: its chip at column 1,
// sep, and its state phrase (5a.14.2).
func (m *Model) machineHeading(mc *machine, w int) line {
	phrase, failed := ui.MachinePhrase(mc.name, mc.state, ui.Problem(mc.problem), mc.online() && !mc.complete)
	return m.headingLine(mc.name, false, phrase, failed, w)
}

// headingLine is a machine heading: the chip of another machine at column 1,
// or this machine's name in comment where the chip's name would start, then
// sep and the phrase: muted, or ink after the failed mark in error. A rule
// in comment runs from there to the margin, so a machine's section is told
// from a project's heading by its shape as well as its hue. The name is cut
// at the end and keeps its first letter; the phrase is cut at the end.
func (m *Model) headingLine(name string, this bool, phrase string, failed bool, w int) line {
	name = m.theme.CutEnd(name, max(1, w-gutter-1-margin))
	var segs []seg
	if this {
		segs = []seg{spaces(gutter), {text: name, role: ui.RoleComment}, spaces(1)}
	} else {
		chip, _ := m.theme.Chip(name)
		segs = []seg{spaces(1), {text: chip, role: ui.RoleElsewhere, ground: ui.GroundRaised, bold: true}}
	}
	if phrase != "" {
		segs = append(segs, spaces(sep))
		room := w - margin - segsWidth(segs)
		if failed {
			mark := m.theme.Glyph(ui.GlyphFailed)
			segs = append(segs, seg{text: mark, role: ui.RoleError}, spaces(1))
			room -= ui.Width(mark) + 1
			if room > 0 {
				segs = append(segs, seg{text: m.theme.CutEnd(phrase, room)})
			}
		} else if room > 0 {
			segs = append(segs, seg{text: m.theme.CutEnd(phrase, room), role: ui.RoleMuted})
		}
		segs = append(segs, spaces(1))
	}
	if n := w - margin - segsWidth(segs); n >= minRule {
		segs = append(segs, seg{text: strings.Repeat(m.theme.Glyph(ui.GlyphDividerH), n), role: ui.RoleComment})
	}
	return line{segs: segs}
}

// itemSegs is a row of the scopes all and sessions: name, changes, sync,
// state, title (5a.6.7).
func (m *Model) itemSegs(r row, item Item, cols columns, selected bool, w int) []seg {
	segs := m.gutterSegs(r.item == m.here, selected)
	base := nameBase(r.item == m.here, selected)
	var name []seg
	if item.grouped() {
		name = append(name, spaces(indent))
		name = append(name, highlight(m.theme.CutEnd(item.Worktree, cols.name-indent), r.nameHits, base)...)
	} else {
		name = highlight(m.theme.CutEnd(item.Label, cols.name), r.nameHits, base)
	}
	segs = append(segs, name...)
	segs = append(segs, spaces(cols.name-segsWidth(name)))

	git := m.gitOf(item)
	state, stateRole := m.stateCell(item)
	for _, cell := range []struct {
		text  string
		width int
		show  bool
		role  ui.Role
	}{
		{ui.Changes(git), cols.changes, cols.showChanges, ui.RoleMuted},
		{m.sync(git), cols.sync, cols.showSync, ui.RoleMuted},
		{state, cols.state, true, stateRole},
	} {
		if cell.width == 0 || !cell.show {
			continue
		}
		segs = append(segs, spaces(gap))
		segs = append(segs, cellSegs(cell.text, cell.width, cell.role)...)
	}
	return m.titleSegs(segs, item.State.Open, item.State.Title, w)
}

// titleSegs adds an agent's title when at least minTitle cells are free,
// cut at the end; only where agents exist (H12).
func (m *Model) titleSegs(segs []seg, open bool, title string, w int) []seg {
	if !open || title == "" || !m.hasAgents() {
		return segs
	}
	if room := w - margin - segsWidth(segs) - gap; room >= minTitle {
		segs = append(segs, spaces(gap), seg{text: m.theme.CutEnd(title, room), role: ui.RoleComment})
	}
	return segs
}

// agentSegs is a row of the agents scope: state, machine, session, tab and
// kind, title (5a.6.7). The machine column is this machine's name in
// comment and other machines' names in the elsewhere hue; it is there only when machine
// sections exist and another machine is configured.
func (m *Model) agentSegs(r row, item Item, cols columns, selected bool, w int) []seg {
	segs := m.gutterSegs(r.item == m.hereAgent, selected)
	state, role := m.stateCell(item)
	segs = append(segs, cellSegs(state, cols.agentState, role)...)
	if cols.showMachine {
		role := ui.RoleElsewhere
		if item.Machine == "" {
			role = ui.RoleComment
		}
		segs = append(segs, spaces(gap))
		segs = append(segs, cellSegs(m.theme.CutEnd(m.machineName(item), cols.machine), cols.machine, role)...)
	}
	base := nameBase(r.item == m.hereAgent, selected)
	label := m.theme.CutMiddle(item.Session, cols.session)
	hits := r.nameHits
	if label != item.Session {
		hits = nil
	}
	labelSegs := highlight(label, hits, base)
	segs = append(segs, spaces(gap))
	segs = append(segs, labelSegs...)
	if cols.showTab {
		segs = append(segs, spaces(cols.session-segsWidth(labelSegs)+gap))
		segs = append(segs, cellSegs(agentTab(item), cols.tab, ui.RoleMuted)...)
	}
	return m.titleSegs(segs, true, item.State.Title, w)
}

// createLine is the create row: the mark and its words in muted, the branch
// in bold strong.
func (m *Model) createLine(w int) line {
	c := m.create
	parts := ui.CreateRow(c.Branch, c.Project, c.From)
	selected := m.onCreate()
	segs := m.gutterSegs(false, selected)
	segs = append(segs,
		seg{text: m.theme.Glyph(ui.GlyphCreate) + " " + parts[0], role: ui.RoleMuted},
		seg{text: parts[1], role: ui.RoleStrong, bold: true},
		seg{text: parts[2], role: ui.RoleMuted},
	)
	return line{segs: segs, selected: selected}
}

// nextScope is the name of the scope tab goes to.
func (m *Model) nextScope() string {
	scopes := m.scopes()
	for i, s := range scopes {
		if s == m.scope {
			return scopes[(i+1)%len(scopes)].name()
		}
	}
	return scopes[0].name()
}

// footer is the bottom of the hub as text: the question while a confirm is
// open, then the footer row.
func (m *Model) footer(w int) string {
	rows := m.footerLines(w)
	if len(rows[0].segs) == 0 {
		return m.render(rows[1], w)
	}
	return m.render(rows[0], w) + "\n" + m.render(rows[1], w)
}

// footerLines is row H-2, empty unless a confirm asks its question there,
// and row H-1: the hints, a busy line or a message (5a.6.7, 5a.14.4).
func (m *Model) footerLines(w int) []line {
	f := m.footerNow()
	room := max(0, w-gutter)
	switch {
	case f.Busy != "":
		return []line{{}, {segs: []seg{spaces(gutter), {text: m.theme.CutEnd(f.Busy, room), role: ui.RoleComment}}}}
	case f.Confirm != nil:
		question := line{segs: []seg{spaces(gutter), {text: m.theme.CutEnd(ui.MergeQuestion(f.Confirm.Worktree), room)}}}
		return []line{question, m.hintLine([]hint{
			{key: m.theme.Glyph(ui.GlyphEnter), label: ui.HubMerge},
			{key: "esc", label: ui.HubCancel},
		}, w)}
	case f.Message != "" && f.Failed:
		mark := m.theme.Glyph(ui.GlyphFailed)
		return []line{{}, {segs: []seg{spaces(gutter), {text: mark, role: ui.RoleError}, spaces(1), {text: m.theme.CutEnd(f.Message, room-ui.Width(mark)-1)}}}}
	case f.Message != "":
		return []line{{}, {segs: []seg{spaces(gutter), {text: m.theme.CutEnd(f.Message, room), role: ui.RoleComment}}}}
	}

	enter := m.theme.Glyph(ui.GlyphEnter)
	control := m.theme.Glyph(ui.GlyphControl)
	creating := m.onCreate()
	rows := len(m.selectable()) > 0
	var hints []hint
	switch {
	case creating:
		hints = append(hints, hint{key: enter, label: ui.HubCreate, leaves: 4})
	case rows && m.scope == scopeAgents:
		hints = append(hints, hint{key: enter, label: ui.HubJump, leaves: 4})
	case rows:
		hints = append(hints, hint{key: enter, label: ui.HubSwitch, leaves: 4})
	}
	// a feature starts only from a managed worktree of this machine
	if !creating && m.actions != nil && m.scope != scopeAgents && m.hasLocalProject() {
		hints = append(hints, hint{key: control + "N", label: ui.HubNewFeature, leaves: 2})
	}
	if item, ok := m.selected(); ok && !creating && m.mergeable(item) {
		hints = append(hints, hint{key: control + "G", label: ui.HubMergeMain, leaves: 1})
	}
	// while this machine has not answered, its list is what a scope would
	// show; tab is offered once it has (hub-loading-80x24)
	if m.machines[0].loaded || m.scope == scopeAgents {
		hints = append(hints, hint{key: "tab", label: m.nextScope(), leaves: 3})
	}
	if m.queryText() != "" {
		hints = append(hints, hint{key: "esc", label: ui.HubClear})
	} else {
		hints = append(hints, hint{key: "esc", label: ui.HubClose})
	}
	return []line{{}, m.hintLine(hints, w)}
}

// hasLocalProject reports whether a managed worktree of this machine is in
// the hub, so that a feature can start here.
func (m *Model) hasLocalProject() bool {
	for _, item := range m.items {
		if item.grouped() && !item.remote() {
			return true
		}
	}
	return false
}

// hint is one key and its label in the footer; leaves is its place in the
// order in which hints leave a narrow footer, 1 first, 0 never.
type hint struct {
	key, label string
	leaves     int
}

// hintLine is the hints, keys in bold strong ink and labels in comment, gap apart.
// When they do not fit, ⌃G leaves first, then ⌃N, then tab, then enter;
// a key is never cut.
func (m *Model) hintLine(hints []hint, w int) line {
	build := func(hs []hint) []seg {
		segs := []seg{spaces(gutter)}
		for i, h := range hs {
			if i > 0 {
				segs = append(segs, spaces(gap))
			}
			segs = append(segs, seg{text: h.key, role: ui.RoleStrong, bold: true}, spaces(1), seg{text: h.label, role: ui.RoleComment})
		}
		return segs
	}
	segs := build(hints)
	for order := 1; segsWidth(segs)+margin > w && order <= 4; order++ {
		kept := hints[:0:0]
		for _, h := range hints {
			if h.leaves != order {
				kept = append(kept, h)
			}
		}
		hints = kept
		segs = build(hints)
	}
	return line{segs: segs}
}

// highlight splits text into runs of base, the matched runes in bold strong.
func highlight(text string, hits []int, base seg) []seg {
	if len(hits) == 0 {
		base.text = text
		return []seg{base}
	}
	isHit := make(map[int]bool, len(hits))
	for _, h := range hits {
		isHit[h] = true
	}
	var segs []seg
	var run []rune
	runHit := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		s := base
		if runHit {
			s = seg{role: ui.RoleStrong, bold: true, ground: base.ground}
		}
		s.text = string(run)
		segs = append(segs, s)
		run = run[:0]
	}
	for i, r := range []rune(text) {
		if isHit[i] != runHit {
			flush()
			runHit = isHit[i]
		}
		run = append(run, r)
	}
	flush()
	return segs
}

// cellSegs is the text of a column cell in role r, padded to width cells:
// the spaces before and after it take no role, so only the text is styled.
func cellSegs(text string, width int, r ui.Role) []seg {
	trimmed := strings.TrimLeft(text, " ")
	segs := []seg{spaces(len(text) - len(trimmed))}
	if trimmed != "" {
		segs = append(segs, seg{text: trimmed, role: r})
	}
	return append(segs, spaces(width-ui.Width(text)))
}

// ruleLine is the edge of the input field: a rule in comment across the
// column, between the gutter and the margin. It stands where an empty row
// would, and gives way like one in a short window.
func (m *Model) ruleLine(w int) line {
	n := w - gutter - margin
	if n <= 0 {
		return line{}
	}
	return line{segs: []seg{spaces(gutter), {text: strings.Repeat(m.theme.Glyph(ui.GlyphDividerH), n), role: ui.RoleComment}}, rule: true}
}
