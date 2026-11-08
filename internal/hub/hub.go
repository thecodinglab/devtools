// Package hub is the devtools switcher: one list of every worktree,
// bookmark and open workspace, grouped by project, that switches to the one
// you pick, starts a feature from what you type when nothing matches, and
// merges a finished feature into main.
package hub

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"devtools/internal/devgit"
	"devtools/internal/ui"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type Kind int

const (
	// KindWorktree is a worktree of a managed project: grouped under its
	// project, and the only kind a feature can start from or merge.
	KindWorktree Kind = iota
	// KindRepo is a plain checkout found under a root.
	KindRepo
	// KindBookmark is a bookmarked directory.
	KindBookmark
	// KindWorkspace is an open workspace devtools did not create.
	KindWorkspace
)

// Item is one place the switcher can take you.
type Item struct {
	Kind     Kind
	Project  string
	Worktree string
	Label    string // project/worktree, or the name of a single item
	Path     string
	Root     string // the workspace root of a managed project
	Session  string // the session or workspace label devtools gives it
	// WorkspaceID addresses a KindWorkspace item, and State is its
	// session: workspace labels need not be unique, so it can't be looked up
	// by name.
	WorkspaceID string
	State       ui.Session
}

func (i Item) grouped() bool { return i.Kind == KindWorktree }

func (i Item) isMain() bool {
	return i.Kind == KindWorktree && (i.Worktree == "main" || i.Worktree == "master")
}

// CreateRequest asks for a new feature worktree.
type CreateRequest struct {
	Root     string
	Project  string
	Branch   string
	From     string // the worktree the branch starts from
	FromPath string
}

// Backend does the work the switcher asks for.
type Backend interface {
	// Status reads the git state of a worktree.
	Status(path string) (ui.Git, error)
	// Sessions returns the open sessions by name, and every open workspace
	// as an item of its own; the switcher keeps those that belong to none of
	// its items.
	Sessions() (map[string]ui.Session, []Item, error)
	// Switch opens or focuses the session of an item.
	Switch(item Item) error
	// Create adds a feature worktree and returns it.
	Create(req CreateRequest) (Item, error)
	// Merge rebases a feature onto main, fast-forwards main, removes the
	// feature worktree and returns main.
	Merge(item Item) (Item, error)
}

type Mode int

const (
	ModeAll  Mode = iota // every worktree
	ModeOpen             // only open workspaces
	ModeNew              // name a new feature
)

type Options struct {
	// Items to list, or Load to read them once the switcher shows, so it
	// shows at once however long a scan of the roots takes.
	Items []Item
	Load  func() ([]Item, error)
	// Here is the directory the switcher was opened from.
	Here    string
	Mode    Mode
	Query   string
	Backend Backend
	// SwitchInPlace switches from inside the switcher, which shows a failure
	// right there. Without it Run returns the item and the caller switches,
	// for when switching takes over the terminal.
	SwitchInPlace bool
}

// Run shows the switcher. It returns the item to switch to when the caller
// has to switch, nil otherwise.
func Run(opts Options) (*Item, error) {
	// 120 frames a second, the most bubbletea allows, so the first frame
	// and everything that loads after it show up sooner
	final, err := tea.NewProgram(newModel(opts), tea.WithFPS(120)).Run()
	if err != nil {
		return nil, err
	}
	m := final.(model)
	return m.result, nil
}

type rowKind int

const (
	rowHeader rowKind = iota
	rowItem
)

// row is one line of the list: a project heading or an item.
type row struct {
	kind    rowKind
	item    int // index into model.items for rowItem
	project string
	child   bool // a feature worktree, indented under its project
	// rune positions the query matched, in the project and in the worktree
	projectHits []int
	nameHits    []int
}

type model struct {
	opts    Options
	backend Backend
	// items are the found ones, then the open workspaces among others
	// that belong to none of them
	items  []Item
	found  []Item
	others []Item
	here   int // index of the item the switcher was opened from, -1 if none

	itemsLoaded    bool
	sessionsLoaded bool
	// slow is set once loading has taken long enough to say so; a note
	// that shows for a frame only flickers
	slow bool

	git      map[string]ui.Git
	sessions map[string]ui.Session

	input    textinput.Model
	query    string // the input the rows were built for
	openOnly bool
	newMode  bool

	rows     []row
	cursor   int // index into selectable(), the create row last
	offset   int // first list line shown
	create   *CreateRequest
	noCreate string // why the query can't become a feature, if it can't

	confirm *Item  // a merge waiting for its confirmation
	busy    string // what is running
	message string
	failed  bool

	result *Item
	width  int
	height int
}

func newModel(opts Options) model {
	input := textinput.New()
	input.Prompt = "› "
	styles := input.Styles()
	styles.Focused.Prompt = ui.Plain
	styles.Focused.Text = ui.Plain
	styles.Focused.Placeholder = ui.CommentText
	styles.Cursor.Color = nil
	styles.Cursor.Shape = tea.CursorBar
	styles.Cursor.Blink = false
	input.SetStyles(styles)
	input.SetVirtualCursor(false)
	input.SetValue(opts.Query)
	input.Focus()

	m := model{
		opts:     opts,
		backend:  opts.Backend,
		git:      map[string]ui.Git{},
		sessions: map[string]ui.Session{},
		input:    input,
		openOnly: opts.Mode == ModeOpen,
		newMode:  opts.Mode == ModeNew,
		width:    80,
		height:   24,
	}
	if opts.Load == nil {
		m.found = append([]Item(nil), opts.Items...)
		m.itemsLoaded = true
	}
	m.joinItems()
	m.input.Placeholder = m.placeholder()
	m.refilter()
	return m
}

// joinItems lists the found items, then the open workspaces that belong to
// none of them.
func (m *model) joinItems() {
	known := make(map[string]bool, len(m.found))
	for _, item := range m.found {
		known[item.Session] = true
	}
	m.items = append([]Item(nil), m.found...)
	for _, other := range m.others {
		if !known[other.Session] {
			m.items = append(m.items, other)
		}
	}
	m.here = findHere(m.items, m.opts.Here)
}

func (m model) placeholder() string {
	switch {
	case m.newMode:
		return "Name the new feature"
	case m.openOnly:
		return "Switch to an open workspace"
	}
	return "Switch to a worktree, or name a new feature"
}

// findHere picks the item whose directory holds dir, the deepest one when
// they nest.
func findHere(items []Item, dir string) int {
	if dir == "" {
		return -1
	}
	dir = canonical(dir)
	best, bestLen := -1, -1
	for i, item := range items {
		path := canonical(item.Path)
		rel, err := filepath.Rel(path, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// the deepest wins; at the same depth a managed worktree beats a
		// bookmark of it, so features can start there
		if len(path) > bestLen || len(path) == bestLen && item.grouped() && !items[best].grouped() {
			best, bestLen = i, len(path)
		}
	}
	return best
}

func canonical(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path
}

// Init starts everything that takes a while, each part showing as soon as
// it is there: the items unless they were handed over, the git state of
// each worktree, the open sessions.
func (m model) Init() tea.Cmd {
	slow := tea.Tick(slowAfter, func(time.Time) tea.Msg { return slowMsg{} })
	if !m.itemsLoaded {
		return tea.Batch(m.loadItems(), m.loadSessions(), slow)
	}
	return tea.Batch(m.loadGit(m.found), m.loadSessions(), slow)
}

// slowAfter is how long loading goes unremarked.
const slowAfter = 150 * time.Millisecond

type slowMsg struct{}

type itemsMsg struct {
	items []Item
	err   error
}

type gitMsg struct {
	path string
	git  ui.Git
}

type sessionsMsg struct {
	sessions map[string]ui.Session
	others   []Item
}

type doneMsg struct {
	switchTo *Item // switch here next
	err      error
}

type switchedMsg struct{ err error }

func (m model) loadItems() tea.Cmd {
	load := m.opts.Load
	return func() tea.Msg {
		items, err := load()
		return itemsMsg{items: items, err: err}
	}
}

// loadGit reads the state of the worktrees in parallel, each showing as
// soon as it is read, so a slow repository holds up only its own row.
func (m model) loadGit(items []Item) tea.Cmd {
	backend := m.backend
	var cmds []tea.Cmd
	for _, item := range items {
		if item.Kind == KindWorkspace || item.Kind == KindBookmark {
			continue
		}
		cmds = append(cmds, func() tea.Msg {
			state, err := backend.Status(item.Path)
			if err != nil {
				return nil
			}
			return gitMsg{path: item.Path, git: state}
		})
	}
	return tea.Batch(cmds...)
}

func (m model) loadSessions() tea.Cmd {
	backend := m.backend
	return func() tea.Msg {
		sessions, others, err := backend.Sessions()
		if err != nil {
			return sessionsMsg{}
		}
		return sessionsMsg{sessions: sessions, others: others}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// room for the prompt, the cursor and the open only label
		m.input.SetWidth(max(1, m.width-gutter-lipgloss.Width(m.input.Prompt)-len("open only")-4))
		m.clampScroll()
		return m, nil
	case itemsMsg:
		m.itemsLoaded = true
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.found = msg.items
		m.joinItems()
		m.refilter()
		return m, m.loadGit(m.found)
	case gitMsg:
		m.git[msg.path] = msg.git
		return m, nil
	case slowMsg:
		m.slow = true
		return m, nil
	case sessionsMsg:
		m.sessionsLoaded = true
		if msg.sessions != nil {
			m.sessions = msg.sessions
		}
		m.others = msg.others
		m.joinItems()
		m.refilterKeepingCursor()
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		return m.switchTo(*msg.switchTo)
	case switchedMsg:
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.afterInput()
	return m, cmd
}

func (m *model) fail(err error) {
	m.busy = ""
	m.message = err.Error()
	m.failed = true
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// a running merge or create gets to finish, or to undo itself: quitting
	// would leave it halfway, a rebase in progress say
	if m.busy != "" {
		return m, nil
	}
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirm != nil {
		switch key {
		case "enter":
			return m.merge(*m.confirm)
		case "esc":
			m.confirm = nil
		}
		return m, nil
	}
	m.message, m.failed = "", false
	switch key {
	case "esc":
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.afterInput()
			return m, nil
		}
		return m, tea.Quit
	case "up", "ctrl+k", "shift+tab":
		m.move(-1)
		return m, nil
	case "down", "ctrl+j":
		m.move(1)
		return m, nil
	case "pgup":
		m.move(-m.listHeight())
		return m, nil
	case "pgdown":
		m.move(m.listHeight())
		return m, nil
	case "tab":
		m.openOnly = !m.openOnly
		m.input.Placeholder = m.placeholder()
		m.refilter()
		return m, nil
	case "enter":
		return m.activate()
	case "ctrl+n":
		return m.startCreate()
	case "ctrl+g":
		item, ok := m.selectedItem()
		if !ok || !mergeable(item) {
			m.message = "Only a feature worktree can be merged into main."
			return m, nil
		}
		m.confirm = &item
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.afterInput()
	return m, cmd
}

// session is the multiplexer state of an item.
func (m model) session(item Item) ui.Session {
	if item.Kind == KindWorkspace {
		return item.State
	}
	return m.sessions[item.Session]
}

func mergeable(item Item) bool {
	return item.Kind == KindWorktree && !item.isMain()
}

// afterInput refilters when an edit changed the query.
func (m *model) afterInput() {
	if m.input.Value() != m.query {
		m.refilter()
	}
}

// activate does what enter means on the selected row.
func (m model) activate() (tea.Model, tea.Cmd) {
	if m.onCreate() {
		return m.startCreate()
	}
	item, ok := m.selectedItem()
	if !ok {
		return m, nil
	}
	return m.switchTo(item)
}

func (m model) switchTo(item Item) (tea.Model, tea.Cmd) {
	if !m.opts.SwitchInPlace {
		m.result = &item
		return m, tea.Quit
	}
	backend := m.backend
	m.busy = "Opening " + item.Label + "…"
	return m, func() tea.Msg {
		return switchedMsg{err: backend.Switch(item)}
	}
}

func (m model) startCreate() (tea.Model, tea.Cmd) {
	if m.create == nil {
		if m.noCreate != "" {
			m.message = m.noCreate
		} else {
			m.message = "Type a name for the feature first."
		}
		return m, nil
	}
	req := *m.create
	backend := m.backend
	m.busy = "Creating " + req.Branch + " in " + req.Project + "…"
	return m, func() tea.Msg {
		item, err := backend.Create(req)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{switchTo: &item}
	}
}

func (m model) merge(item Item) (tea.Model, tea.Cmd) {
	m.confirm = nil
	backend := m.backend
	m.busy = "Merging " + item.Worktree + " into main…"
	return m, func() tea.Msg {
		main, err := backend.Merge(item)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{switchTo: &main}
	}
}

// selectable is the rows the cursor can stop on: the items, then the
// create row when there is one.
func (m model) selectable() []int {
	var rows []int
	for i, r := range m.rows {
		if r.kind == rowItem {
			rows = append(rows, i)
		}
	}
	return rows
}

func (m model) selectableCount() int {
	n := len(m.selectable())
	if m.create != nil {
		n++
	}
	return n
}

func (m model) onCreate() bool {
	return m.create != nil && m.cursor == len(m.selectable())
}

func (m model) selectedItem() (Item, bool) {
	rows := m.selectable()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return Item{}, false
	}
	return m.items[m.rows[rows[m.cursor]].item], true
}

func (m *model) move(delta int) {
	n := m.selectableCount()
	if n == 0 {
		return
	}
	m.cursor = max(0, min(n-1, m.cursor+delta))
	m.clampScroll()
}

func (m *model) refilterKeepingCursor() {
	selected, ok := m.selectedItem()
	onCreate := m.onCreate()
	m.refilter()
	if onCreate {
		m.cursor = len(m.selectable())
	} else if ok {
		for i, r := range m.selectable() {
			if item := m.items[m.rows[r].item]; item.Path == selected.Path && item.Label == selected.Label && item.WorkspaceID == selected.WorkspaceID {
				m.cursor = i
			}
		}
	}
	m.clampScroll()
}

type scored struct {
	item        int
	score       int
	projectHits []int
	nameHits    []int
}

// refilter rebuilds the rows for the query and puts the cursor where enter
// most likely means: on the best match, on where you are when nothing is
// typed, or on the create row when nothing matches or a feature is asked
// for.
func (m *model) refilter() {
	m.query = m.input.Value()
	query := strings.TrimSpace(m.query)
	m.create, m.noCreate = m.createRequest(query)

	var matches []scored
	for i, item := range m.items {
		if m.openOnly && !m.session(item).Open {
			continue
		}
		if m.newMode && m.here >= 0 && m.items[m.here].grouped() && !sameProject(item, m.items[m.here]) {
			continue
		}
		score, hits, ok := match(query, item.Label)
		if !ok {
			continue
		}
		s := scored{item: i, score: score}
		if item.grouped() {
			cut := len([]rune(item.Project)) + 1
			for _, h := range hits {
				if h < cut-1 {
					s.projectHits = append(s.projectHits, h)
				} else if h >= cut {
					s.nameHits = append(s.nameHits, h-cut)
				}
			}
		} else {
			s.nameHits = hits
		}
		matches = append(matches, s)
	}

	// groups keep the order of the items when nothing is typed, and are
	// ranked by their best match otherwise
	type group struct {
		project string
		best    int
		first   int
		members []scored
	}
	var groups []*group
	byProject := map[string]*group{}
	for order, s := range matches {
		item := m.items[s.item]
		key := "\x00" + item.Label + "\x00" + item.WorkspaceID
		if item.grouped() {
			key = item.Root + "\x00" + item.Project
		}
		g, ok := byProject[key]
		if !ok {
			g = &group{project: item.Project, best: s.score, first: order}
			byProject[key] = g
			groups = append(groups, g)
		}
		g.best = max(g.best, s.score)
		g.members = append(g.members, s)
	}
	if query != "" {
		sort.SliceStable(groups, func(i, j int) bool { return groups[i].best > groups[j].best })
		for _, g := range groups {
			sort.SliceStable(g.members, func(i, j int) bool { return g.members[i].score > g.members[j].score })
		}
	}

	// a project is one row, its main worktree, with its features indented
	// below; when main is filtered out or missing, a heading stands in
	m.rows = m.rows[:0]
	for _, g := range groups {
		if !m.items[g.members[0].item].grouped() {
			for _, s := range g.members {
				m.rows = append(m.rows, row{kind: rowItem, item: s.item, project: g.project, nameHits: s.nameHits})
			}
			continue
		}
		var hits []int
		parent := -1
		for i, s := range g.members {
			hits = append(hits, s.projectHits...)
			if m.items[s.item].isMain() && parent < 0 {
				parent = i
			}
		}
		if parent >= 0 {
			s := g.members[parent]
			m.rows = append(m.rows, row{kind: rowItem, item: s.item, project: g.project, projectHits: dedupe(hits)})
		} else {
			m.rows = append(m.rows, row{kind: rowHeader, project: g.project, projectHits: dedupe(hits)})
		}
		for i, s := range g.members {
			if i != parent {
				m.rows = append(m.rows, row{kind: rowItem, item: s.item, project: g.project, child: true, nameHits: s.nameHits})
			}
		}
	}

	m.cursor = 0
	switch {
	case m.newMode || (query != "" && len(matches) == 0):
		m.cursor = len(m.selectable())
	case query != "":
		// the best match, which a project's main row may have been put
		// above
		best := matches[0]
		for _, s := range matches {
			if s.score > best.score {
				best = s
			}
		}
		for i, r := range m.selectable() {
			if m.rows[r].item == best.item {
				m.cursor = i
			}
		}
	case query == "" && m.here >= 0:
		for i, r := range m.selectable() {
			if m.rows[r].item == m.here {
				m.cursor = i
			}
		}
	}
	if m.cursor >= m.selectableCount() {
		m.cursor = max(0, m.selectableCount()-1)
	}
	m.offset = 0
	m.clampScroll()
}

// createRequest turns the query into a feature to create: in the project
// you are in, from the worktree you are in, or in the project the query
// names before a slash, from its main worktree.
func (m model) createRequest(query string) (*CreateRequest, string) {
	if query == "" {
		return nil, ""
	}
	branch := strings.Join(strings.Fields(query), "-")

	var from *Item
	if project, rest, ok := strings.Cut(branch, "/"); ok && rest != "" {
		if main := m.mainOf(project); main != nil {
			from, branch = main, rest
		}
	}
	if from == nil && m.here >= 0 && m.items[m.here].grouped() {
		from = &m.items[m.here]
	}
	if from == nil {
		return nil, "To start a feature here, type project/name, for example devtools/" + branch + "."
	}
	worktree := devgit.WorktreeName(branch)
	for _, item := range m.items {
		if item.grouped() && sameProject(item, *from) && item.Worktree == worktree {
			return nil, ""
		}
	}
	return &CreateRequest{
		Root:     from.Root,
		Project:  from.Project,
		Branch:   branch,
		From:     from.Worktree,
		FromPath: from.Path,
	}, ""
}

// sameProject reports whether two items are worktrees of one project: a
// project of the same name under another root is another project.
func sameProject(a, b Item) bool {
	return a.grouped() && b.grouped() && a.Project == b.Project && a.Root == b.Root
}

// mainOf returns the main worktree of a managed project, or any of its
// worktrees when it has none. Of projects with the name under several roots
// it prefers the one you are in.
func (m model) mainOf(project string) *Item {
	root := ""
	if m.here >= 0 && m.items[m.here].grouped() && m.items[m.here].Project == project {
		root = m.items[m.here].Root
	}
	var any *Item
	for i := range m.items {
		item := &m.items[i]
		if !item.grouped() || item.Project != project || root != "" && item.Root != root {
			continue
		}
		if item.isMain() {
			return item
		}
		if any == nil {
			any = item
		}
	}
	return any
}
