package hub

import (
	"strings"

	"devtools/internal/ui"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	gutter = 2 // the here mark and a space
	indent = 2 // worktrees under their project
	gap    = 3 // between columns
)

var (
	projectStyle = lipgloss.NewStyle().Foreground(ui.Strong)
	// the letters the query matched are bold, the selected row is in the
	// strong ink on the raised ground, so a match still shows on it
	hitStyle      = ui.StrongText
	selectedStyle = lipgloss.NewStyle().Foreground(ui.Strong)
)

// seg is a run of text in one style.
type seg struct {
	text  string
	style lipgloss.Style
}

func (m model) View() tea.View {
	var lines []string

	input := strings.Repeat(" ", gutter) + m.input.View()
	if m.openOnly {
		label := ui.AccentText.Render("open only")
		space := m.width - lipgloss.Width(input) - lipgloss.Width(label) - 2
		if space > 0 {
			input += strings.Repeat(" ", space) + label
		}
	}
	lines = append(lines, input, "")

	list := m.listLines()
	var create []string
	if m.create != nil {
		if len(list) > 0 {
			create = append(create, "")
		}
		create = append(create, m.createLine())
	}
	// in a window too short for everything the list gives way first, so
	// the create row and the footer stay in sight
	if room := m.height - 1 - len(lines) - len(create); len(list) > max(0, room) {
		list = list[:max(0, room)]
		if len(list) == 0 && len(create) > 1 {
			create = create[1:]
		}
	}
	lines = append(lines, list...)
	lines = append(lines, create...)
	// the footer keeps to the bottom edge, however short the list
	if len(lines) > m.height-1 {
		lines = lines[len(lines)-max(0, m.height-1):]
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	lines = append(lines, m.footer())

	for i, line := range lines {
		lines[i] = ansi.Truncate(line, m.width, "")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	if m.busy == "" && m.confirm == nil {
		if cursor := m.input.Cursor(); cursor != nil {
			cursor.X += gutter
			view.Cursor = cursor
		}
	}
	return view
}

// fixedLines is everything around the list: the input and a space, the
// create row and its space, the space and the footer.
func (m model) fixedLines() int {
	n := 4
	if m.create != nil {
		n += 2
	}
	return n
}

func (m model) listHeight() int {
	return max(1, m.height-m.fixedLines())
}

func (m *model) clampScroll() {
	height := m.listHeight()
	rows := m.selectable()
	if m.cursor < len(rows) {
		r := rows[m.cursor]
		// bring the project heading along when scrolling up to its first
		// worktree
		top := r
		if r > 0 && m.rows[r-1].kind == rowHeader {
			top = r - 1
		}
		if top < m.offset {
			m.offset = top
		}
		if r >= m.offset+height {
			m.offset = r - height + 1
		}
	}
	m.offset = max(0, min(m.offset, len(m.rows)-height))
}

type columns struct {
	name, changes, sync, state int
}

func (m model) columns() columns {
	var c columns
	for _, r := range m.rows {
		if r.kind == rowHeader {
			c.name = max(c.name, lipgloss.Width(r.project))
			continue
		}
		item := m.items[r.item]
		switch {
		case r.child:
			c.name = max(c.name, indent+lipgloss.Width(item.Worktree))
		case item.grouped():
			c.name = max(c.name, lipgloss.Width(item.Project))
		default:
			c.name = max(c.name, lipgloss.Width(item.Label))
		}
		git := m.git[item.Path]
		c.changes = max(c.changes, lipgloss.Width(ui.Changes(git)))
		c.sync = max(c.sync, lipgloss.Width(ui.Sync(git)))
		state, _ := ui.SessionState(m.session(item))
		c.state = max(c.state, lipgloss.Width(state))
	}
	c.name = min(c.name, max(16, m.width*2/5))
	return c
}

func (m model) listLines() []string {
	height := m.listHeight()
	if len(m.rows) == 0 {
		message := m.emptyMessage()
		if message == "" {
			return nil
		}
		return []string{strings.Repeat(" ", gutter) + ui.CommentText.Render(message)}
	}
	selectedRow := -1
	if rows := m.selectable(); m.cursor < len(rows) {
		selectedRow = rows[m.cursor]
	}
	cols := m.columns()
	var lines []string
	for i := m.offset; i < len(m.rows) && i < m.offset+height; i++ {
		lines = append(lines, m.rowLine(m.rows[i], cols, i == selectedRow))
	}
	return lines
}

func (m model) emptyMessage() string {
	query := strings.TrimSpace(m.input.Value())
	switch {
	case !m.itemsLoaded:
		return m.loading("Loading worktrees…")
	case m.openOnly && !m.sessionsLoaded:
		return m.loading("Loading open workspaces…")
	case len(m.items) == 0:
		return "No projects yet. Clone one with devtools clone <url>."
	case query == "" && m.openOnly:
		return "No open workspaces. Press tab to show every worktree."
	case m.noCreate != "":
		return m.noCreate
	case m.create == nil:
		return "Nothing matches " + query + "."
	}
	return ""
}

// loading says what is loading once it takes a while, nothing before.
func (m model) loading(note string) string {
	if !m.slow {
		return ""
	}
	return note
}

func (m model) rowLine(r row, cols columns, selected bool) string {
	if r.kind == rowHeader {
		segs := []seg{{strings.Repeat(" ", gutter), ui.Plain}}
		segs = append(segs, highlight(r.project, r.projectHits, projectStyle, hitStyle)...)
		return render(segs, m.width, false)
	}
	item := m.items[r.item]
	mark := " "
	if r.item == m.here {
		mark = ui.HereMark
	}
	segs := []seg{{mark, ui.AccentText}, {" ", ui.Plain}}

	var name []seg
	base := projectStyle
	if r.child {
		base = ui.Plain
	}
	if selected {
		base = selectedStyle
	}
	switch {
	case r.child:
		name = append(name, seg{strings.Repeat(" ", indent), ui.Plain})
		name = append(name, highlight(ui.Truncate(item.Worktree, cols.name-indent), r.nameHits, base, hitStyle)...)
	case item.grouped():
		name = highlight(ui.Truncate(item.Project, cols.name), r.projectHits, base, hitStyle)
	default:
		name = highlight(ui.Truncate(item.Label, cols.name), r.nameHits, base, hitStyle)
	}
	segs = append(segs, name...)
	segs = append(segs, seg{strings.Repeat(" ", max(0, cols.name-segsWidth(name))), ui.Plain})

	git := m.git[item.Path]
	session := m.session(item)
	state, stateStyle := ui.SessionState(session)
	for _, cell := range []struct {
		text  string
		width int
		style lipgloss.Style
	}{
		{ui.Changes(git), cols.changes, ui.MutedText},
		{ui.Sync(git), cols.sync, ui.MutedText},
		{state, cols.state, stateStyle},
	} {
		if cell.width == 0 {
			continue
		}
		segs = append(segs, seg{strings.Repeat(" ", gap), ui.Plain}, seg{ui.Pad(cell.text, cell.width), cell.style})
	}
	if session.Open && session.Title != "" {
		used := segsWidth(segs) + gap
		if room := m.width - used - 2; room > 8 {
			segs = append(segs, seg{strings.Repeat(" ", gap), ui.Plain}, seg{ui.Truncate(session.Title, room), ui.CommentText})
		}
	}
	return render(segs, m.width, selected)
}

func (m model) createLine() string {
	c := m.create
	segs := []seg{
		{strings.Repeat(" ", gutter), ui.Plain},
		{"+ New feature ", ui.Plain},
		{c.Branch, ui.StrongText},
		{" in " + c.Project + ", from " + c.From, ui.CommentText},
	}
	return render(segs, m.width, m.onCreate())
}

func (m model) footer() string {
	pad := strings.Repeat(" ", gutter)
	switch {
	case m.busy != "":
		return pad + ui.CommentText.Render(m.busy)
	case m.confirm != nil:
		question := "Merge " + m.confirm.Worktree + " into main, then remove its worktree and branch?"
		return pad + ui.Plain.Render(question) + strings.Repeat(" ", gap) + hints([][2]string{{"⏎", "merge"}, {"esc", "cancel"}})
	case m.message != "":
		style := ui.CommentText
		if m.failed {
			style = ui.ErrorText
		}
		return pad + style.Render(m.message)
	}

	var keys [][2]string
	if m.onCreate() {
		keys = append(keys, [2]string{"⏎", "create"})
	} else if m.selectableCount() > 0 {
		keys = append(keys, [2]string{"⏎", "switch"})
	}
	if !m.onCreate() {
		keys = append(keys, [2]string{"⌃N", "new feature"})
	}
	if item, ok := m.selectedItem(); ok && mergeable(item) && !m.onCreate() {
		keys = append(keys, [2]string{"⌃G", "merge into main"})
	}
	if m.openOnly {
		keys = append(keys, [2]string{"tab", "show all"})
	} else {
		keys = append(keys, [2]string{"tab", "open only"})
	}
	return pad + hints(keys)
}

func hints(keys [][2]string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = ui.MutedText.Render(k[0]) + " " + ui.CommentText.Render(k[1])
	}
	return strings.Join(parts, strings.Repeat(" ", gap))
}

// highlight splits text into runs, the matched runes in hit.
func highlight(text string, hits []int, base, hit lipgloss.Style) []seg {
	if len(hits) == 0 {
		return []seg{{text, base}}
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
		style := base
		if runHit {
			style = hit
		}
		segs = append(segs, seg{string(run), style})
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

func segsWidth(segs []seg) int {
	w := 0
	for _, s := range segs {
		w += lipgloss.Width(s.text)
	}
	return w
}

// render draws the runs, laid on the raised ground across the whole width
// when the row is selected.
func render(segs []seg, width int, selected bool) string {
	var b strings.Builder
	for _, s := range segs {
		style := s.style
		if selected {
			style = style.Background(ui.Raised)
		}
		b.WriteString(style.Render(s.text))
	}
	if selected {
		if rest := width - segsWidth(segs); rest > 0 {
			b.WriteString(lipgloss.NewStyle().Background(ui.Raised).Render(strings.Repeat(" ", rest)))
		}
	}
	return b.String()
}
