// Package hub is the devtools switcher: one list of every worktree,
// bookmark, open session and agent, of this machine and of the other
// machines, grouped by project, that switches to the one you pick, starts a
// feature from what you type when nothing matches, and merges a finished
// feature into main.
//
// The hub is a model its host draws (Model, 5.3.8 of the plan): it is fed
// by events (Event), knows which of its parts exist (Features), starts no
// process and returns what was picked as a value. It has two hosts: the
// overlay of a devtools window (chrome.Overlay) and RunStandalone, a
// full-screen program for the tmux and herdr backends. Today's Run, Backend
// and Options are an adapter onto the same model (legacy.go).
package hub

import (
	"path/filepath"
	"strings"
	"time"

	"devtools/internal/client/chrome"
	"devtools/internal/client/screen"
	"devtools/internal/devgit"
	"devtools/internal/session"
	"devtools/internal/ui"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
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
	// KindSession is an open session devtools did not create.
	KindSession
)

// Item is one place the switcher can take you.
type Item struct {
	Kind     Kind
	Project  string
	Worktree string
	Label    string // project/worktree, or the name of a single item
	Path     string
	Root     string // the workspace root of a managed project
	Session  string // the session label devtools gives it
	State    ui.Session
	Git      ui.Git

	// The fields of the new generation (5.3.8 of the plan), declared by
	// contract change 1 (6.6) so that the hub feed of the fleet can build
	// them before the model reads them.

	SessionID string // today WorkspaceID; the server's id, valid on Machine only
	Machine   string // today Host; "" is this machine
	Tab, Pane string // ids; KindAgent
	TabIndex  int    // KindAgent, for display
	Agent     string // KindAgent: "claude", "codex"
	Status    string // KindAgent: the agent's status
	Stale     bool   // the row's machine is not online; the row cannot be picked
}

func (i Item) grouped() bool { return i.Kind == KindWorktree }

func (i Item) remote() bool { return i.Machine != "" }

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

type Mode int

const (
	ModeAll  Mode = iota // every worktree
	ModeOpen             // only open sessions; its new name ModeSessions is in event.go
	ModeNew              // name a new feature
)

// Picked is what the hub chose: the row and the target it means (the pick
// mapping of 3.5.3). The overlay's host hands Target to the fleet; the
// standalone host returns Item to the backend, which shows it.
type Picked struct {
	Item   Item
	Target session.Target
}

// scope is one of the lists the hub can show (5a.10.2).
type scope int

const (
	scopeAll scope = iota
	scopeSessions
	scopeAgents
)

func (s scope) name() string {
	switch s {
	case scopeSessions:
		return ui.ScopeSessions
	case scopeAgents:
		return ui.ScopeAgents
	}
	return ui.ScopeAll
}

// Model is the hub. *Model satisfies chrome.Overlay. It is not safe for
// concurrent use: its host calls it from one goroutine.
type Model struct {
	opts     ModelOptions
	features Features
	theme    ui.Theme
	actions  Actions
	events   <-chan Event

	// machines in the order of their first event: this machine first, then
	// the others in config order (3.5.3)
	machines []*machine
	// sortMachines orders the other machines by name, for the legacy
	// adapter, whose machines answer in no order (P2-09 removes it)
	sortMachines bool

	// items are every row the hub holds, joined: per machine its catalog
	// rows (with their session), its sessions that belong to no catalog
	// row, its agents
	items     []Item
	here      int // index of the item the hub was opened from, -1 if none
	hereAgent int // index of the agent of the pane the hub was opened from, -1 if none
	git       map[gitKey]ui.Git

	// slow is set once loading has taken long enough to say so; a note
	// that shows for a frame only flickers
	slow bool

	input   textinput.Model
	query   string // the input the rows were built for
	scope   scope
	newMode bool
	// touched is set once the cursor was moved by a key, so arriving rows
	// no longer move it to where the hub was opened from
	touched bool

	rows     []row
	cursor   int // index into selectable(), the create row last
	offset   int // first list line shown
	create   *CreateRequest
	noCreate string // why the query can't become a feature, if it can't

	confirm *Item  // a merge waiting for its confirmation
	busy    string // what is running
	message string
	failed  bool

	picked *Picked
	width  int
	height int
}

var _ chrome.Overlay = (*Model)(nil)

type gitKey struct{ machine, path string }

// New builds the hub for o. Nothing runs until Init.
func New(o ModelOptions) *Model {
	input := textinput.New()
	input.SetVirtualCursor(false)
	input.SetValue(o.Query)
	input.Focus()

	m := &Model{
		opts:      o,
		features:  o.Features,
		theme:     o.Theme,
		actions:   o.Actions,
		events:    o.Events,
		machines:  []*machine{newMachine("", "online")},
		here:      -1,
		hereAgent: -1,
		git:       map[gitKey]ui.Git{},
		input:     input,
		width:     80,
		height:    24,
	}
	switch o.Mode {
	case ModeSessions:
		m.scope = scopeSessions
	case ModeNew:
		m.newMode = true
	case ModeAgents:
		if m.features.Agents {
			m.scope = scopeAgents
		}
	}
	m.joinItems()
	m.refilter()
	return m
}

// Init starts the slow timer and the reading of the events.
func (m *Model) Init() tea.Cmd {
	slow := tea.Tick(ui.Slow, func(time.Time) tea.Msg { return slowMsg{} })
	return tea.Batch(slow, m.waitEvent())
}

// Picked is the row chosen, nil while the hub is open or when it was closed
// without a choice.
func (m *Model) Picked() *Picked { return m.picked }

// Bounds is the whole pane area: the hub covers it.
func (m *Model) Bounds(w, h int) screen.Rect { return screen.Rect{X: 0, Y: 0, W: w, H: h} }

// Draw draws the hub onto c, which is exactly Bounds.
func (m *Model) Draw(c *screen.Canvas) {
	w, h := c.Size()
	for y, line := range strings.Split(m.View(w, h), "\n") {
		c.Styled(0, y, line, w)
	}
}

// Cursor is where the terminal cursor goes: in the input, hidden while the
// hub waits for something or asks a question.
func (m *Model) Cursor() (x, y int, visible bool) {
	if m.busy != "" || m.confirm != nil {
		return 0, 0, false
	}
	return m.inputCursor(m.width), 0, true
}

type slowMsg struct{}

// eventMsg is one event read from ModelOptions.Events.
type eventMsg struct{ ev Event }

// eventsMsg is events the legacy adapter made; nothing waits behind it.
type eventsMsg []Event

// doneMsg is the end of a create or a merge.
type doneMsg struct {
	switchTo *Item // switch here next
	err      error
}

// waitEvent waits for the next event, nil when there is no channel.
func (m *Model) waitEvent() tea.Cmd {
	events := m.events
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return nil
		}
		return eventMsg{ev: ev}
	}
}

// Update handles one message. A non-nil Outcome closes the hub: with
// Switch set a row was picked (Picked says which), without it the hub was
// closed without a choice.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, *chrome.Outcome) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
		return nil, nil
	case ui.ThemeMsg:
		m.theme = msg.Theme
		return nil, nil
	case eventMsg:
		m.apply(msg.ev)
		m.rejoinKeepingCursor()
		return m.waitEvent(), nil
	case eventsMsg:
		for _, ev := range msg {
			m.apply(ev)
		}
		m.rejoinKeepingCursor()
		return nil, nil
	case slowMsg:
		m.slow = true
		m.rejoinKeepingCursor()
		return nil, nil
	case doneMsg:
		m.busy = ""
		if msg.err != nil {
			m.fail(msg.err)
			return nil, nil
		}
		item := *msg.switchTo
		return m.pick(item, session.Target{Machine: item.Machine, Dir: item.Path, Name: item.Session})
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.afterInput()
	return cmd, nil
}

func (m *Model) fail(err error) {
	m.busy = ""
	m.message = err.Error()
	m.failed = true
}

func (m *Model) key(msg tea.KeyPressMsg) (tea.Cmd, *chrome.Outcome) {
	key := msg.String()
	// a running merge or create gets to finish, or to undo itself: quitting
	// would leave it halfway, a rebase in progress say
	if m.busy != "" {
		return nil, nil
	}
	if key == "ctrl+c" {
		return nil, &chrome.Outcome{}
	}
	if m.confirm != nil {
		switch key {
		case "enter":
			return m.merge(*m.confirm), nil
		case "esc":
			m.confirm = nil
		}
		return nil, nil
	}
	m.message, m.failed = "", false
	switch key {
	case "esc":
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.afterInput()
			return nil, nil
		}
		return nil, &chrome.Outcome{}
	case "up", "ctrl+k":
		m.move(-1)
		return nil, nil
	case "down", "ctrl+j":
		m.move(1)
		return nil, nil
	case "pgup":
		m.move(-m.listHeight(m.height))
		return nil, nil
	case "pgdown":
		m.move(m.listHeight(m.height))
		return nil, nil
	case "tab":
		m.step(1)
		return nil, nil
	case "shift+tab":
		m.step(-1)
		return nil, nil
	case "enter":
		return m.activate()
	case "ctrl+n":
		if item, ok := m.selectedItem(); ok && !m.onCreate() && item.remote() {
			m.message = ui.CreateNotHere
			return nil, nil
		}
		return m.startCreate(), nil
	case "ctrl+g":
		item, ok := m.selectedItem()
		if !ok || !m.mergeable(item) {
			m.message = ui.MergeNotHere
			return nil, nil
		}
		m.confirm = &item
		return nil, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.afterInput()
	return cmd, nil
}

// scopes is the scopes the host's features have, in the order of the
// scope bar.
func (m *Model) scopes() []scope {
	if m.features.Agents {
		return []scope{scopeAll, scopeSessions, scopeAgents}
	}
	return []scope{scopeAll, scopeSessions}
}

// step goes to the next scope (delta 1) or the previous one (-1), wrapping.
// The query survives; naming a new feature is the all scope only.
func (m *Model) step(delta int) {
	scopes := m.scopes()
	at := 0
	for i, s := range scopes {
		if s == m.scope {
			at = i
		}
	}
	m.scope = scopes[(at+delta+len(scopes))%len(scopes)]
	m.newMode = false
	m.touched = false
	m.refilter()
}

// session is the session state of an item, as the join left it.
func (m *Model) session(item Item) ui.Session { return item.State }

// gitOf is the git state of an item: what a Git event said, else what the
// item brought.
func (m *Model) gitOf(item Item) ui.Git {
	if git, ok := m.git[gitKey{item.Machine, item.Path}]; ok {
		return git
	}
	return item.Git
}

func (m *Model) mergeable(item Item) bool {
	return m.actions != nil && item.Kind == KindWorktree && !item.isMain() && !item.remote()
}

// afterInput refilters when an edit changed the query.
func (m *Model) afterInput() {
	if m.input.Value() != m.query {
		m.touched = false
		m.refilter()
	}
}

// activate does what enter means on the selected row.
func (m *Model) activate() (tea.Cmd, *chrome.Outcome) {
	if m.onCreate() {
		return m.startCreate(), nil
	}
	item, ok := m.selectedItem()
	if !ok {
		return nil, nil
	}
	return m.pick(item, target(item))
}

// target is what picking a row means (3.5.3).
func target(item Item) session.Target {
	switch {
	case item.Kind == KindAgent:
		return session.Target{Machine: item.Machine, Session: item.SessionID, Tab: item.Tab, Pane: item.Pane}
	case item.SessionID != "":
		return session.Target{Machine: item.Machine, Session: item.SessionID}
	}
	return session.Target{Machine: item.Machine, Dir: item.Path, Name: item.Session}
}

func (m *Model) pick(item Item, t session.Target) (tea.Cmd, *chrome.Outcome) {
	m.picked = &Picked{Item: item, Target: t}
	return nil, &chrome.Outcome{Switch: &t}
}

func (m *Model) startCreate() tea.Cmd {
	if m.create == nil {
		if m.noCreate != "" {
			m.message = m.noCreate
		} else {
			m.message = ui.Empty("name", "")
		}
		return nil
	}
	req := *m.create
	actions := m.actions
	m.busy = ui.Busy("create", req.Branch, req.Project)
	return func() tea.Msg {
		item, err := actions.Create(req)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{switchTo: &item}
	}
}

func (m *Model) merge(item Item) tea.Cmd {
	m.confirm = nil
	actions := m.actions
	m.busy = ui.Busy("merge", item.Worktree)
	return func() tea.Msg {
		main, err := actions.Merge(item)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{switchTo: &main}
	}
}

// selectable is the rows the cursor can stop on: the items whose machine
// is online, then the create row when there is one.
func (m *Model) selectable() []int {
	var rows []int
	for i, r := range m.rows {
		if r.kind == rowItem && !m.items[r.item].Stale {
			rows = append(rows, i)
		}
	}
	return rows
}

func (m *Model) selectableCount() int {
	n := len(m.selectable())
	if m.create != nil {
		n++
	}
	return n
}

func (m *Model) onCreate() bool {
	return m.create != nil && m.cursor == len(m.selectable())
}

func (m *Model) selectedItem() (Item, bool) {
	rows := m.selectable()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return Item{}, false
	}
	return m.items[m.rows[rows[m.cursor]].item], true
}

func (m *Model) move(delta int) {
	n := m.selectableCount()
	if n == 0 {
		return
	}
	m.touched = true
	m.cursor = max(0, min(n-1, m.cursor+delta))
	m.clampScroll()
}

// identity is what makes a row the same row when the rows are built anew
// (H7): its machine and path, or its machine and pane.
func identity(i Item) string {
	switch i.Kind {
	case KindAgent:
		return "a\x00" + i.Machine + "\x00" + i.Pane
	case KindSession:
		return "s\x00" + i.Machine + "\x00" + i.SessionID + "\x00" + i.Path + "\x00" + i.Label
	}
	return "c\x00" + string(rune('0'+i.Kind)) + "\x00" + i.Machine + "\x00" + i.Path + "\x00" + i.Label
}

// rejoinKeepingCursor joins the items anew and keeps the cursor on the
// item it was on. The rows point into the items by index, so the selected
// item is read before joining reorders them. Until a key moved the cursor,
// it goes where the hub was opened from once that row is there.
func (m *Model) rejoinKeepingCursor() {
	selected, ok := m.selectedItem()
	onCreate := m.onCreate()
	m.joinItems()
	m.refilter()
	switch {
	case onCreate:
		m.cursor = len(m.selectable())
	case ok && (m.touched || m.here < 0 || m.query != "" || m.scope == scopeAgents):
		want := identity(selected)
		for i, r := range m.selectable() {
			if identity(m.items[m.rows[r].item]) == want {
				m.cursor = i
				break
			}
		}
	}
	m.clampScroll()
}

// createRequest turns the query into a feature to create: in the project
// you are in, from the worktree you are in, or in the project the query
// names before a slash, from its main worktree.
//
// The two texts of noCreate are today's (hub.go before P2-07); internal/ui
// has no words for them yet, and the tests of the legacy hub pin them.
func (m *Model) createRequest(query string) (*CreateRequest, string) {
	if query == "" || m.actions == nil || m.scope == scopeAgents {
		return nil, ""
	}
	branch := strings.Join(strings.Fields(query), "-")

	var from *Item
	if project, rest, ok := strings.Cut(branch, "/"); ok && rest != "" {
		if main := m.mainOf(project); main != nil {
			from, branch = main, rest
		} else if machine := m.machineOf(project); machine != "" {
			return nil, project + " is on " + machine + ", start its features over there."
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
// project of the same name under another root or on another machine is
// another project.
func sameProject(a, b Item) bool {
	return a.grouped() && b.grouped() && a.Project == b.Project && a.Root == b.Root && a.Machine == b.Machine
}

// mainOf returns the main worktree of a managed project on this machine,
// or any of its worktrees when it has none. Of projects with the name under
// several roots it prefers the one you are in.
func (m *Model) mainOf(project string) *Item {
	root := ""
	if m.here >= 0 && m.items[m.here].grouped() && m.items[m.here].Project == project {
		root = m.items[m.here].Root
	}
	var any *Item
	for i := range m.items {
		item := &m.items[i]
		if !item.grouped() || item.remote() || item.Project != project || root != "" && item.Root != root {
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

// machineOf names the other machine a managed project is on, if it is on
// one.
func (m *Model) machineOf(project string) string {
	for _, item := range m.items {
		if item.remote() && item.grouped() && item.Project == project {
			return item.Machine
		}
	}
	return ""
}

// findHere picks the item whose directory holds dir, the deepest one when
// they nest, among the items of the machine the hub was opened on.
func (m *Model) findHere() int {
	dir := m.opts.Here.Dir
	if dir == "" {
		return -1
	}
	local := m.opts.Here.Machine == ""
	clean := func(p string) string {
		if local {
			return canonical(p)
		}
		return filepath.Clean(p)
	}
	dir = clean(dir)
	best, bestLen := -1, -1
	for i, item := range m.items {
		// a path over there may well exist here too
		if item.Machine != m.opts.Here.Machine || item.Kind == KindAgent || item.Path == "" {
			continue
		}
		path := clean(item.Path)
		rel, err := filepath.Rel(path, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// the deepest wins; at the same depth a managed worktree beats a
		// bookmark of it, so features can start there
		if len(path) > bestLen || len(path) == bestLen && item.grouped() && !m.items[best].grouped() {
			best, bestLen = i, len(path)
		}
	}
	return best
}

// findHereAgent picks the agent of the pane the hub was opened from.
func (m *Model) findHereAgent() int {
	if m.opts.Here.Pane == "" {
		return -1
	}
	for i, item := range m.items {
		if item.Kind == KindAgent && item.Machine == m.opts.Here.Machine && item.Pane == m.opts.Here.Pane {
			return i
		}
	}
	return -1
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
