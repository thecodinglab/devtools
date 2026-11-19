package hub

import (
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"devtools/internal/ui"
)

// machine is what the hub holds of one machine (3.5.3).
type machine struct {
	name    string
	state   string // "online", "connecting", "off", "retrying", "attention"
	problem Problem
	// complete is set when a catalog scan ended since the machine came
	// online; until then an online machine is "listing"
	complete bool
	// loaded is set once a catalog row or the end of a scan arrived
	loaded bool
	// catalog rows in the order they arrived; a row with the same path and
	// kind replaces the one there
	catalog  []Item
	reported map[catalogKey]bool // the rows of the current scan
	sessions []Item
	// sessionsKnown is set once the open sessions were reported
	sessionsKnown bool
	agents        []Item
	// notes are the secondary lines under the machine's heading (Note,
	// 3.5.3), in the order they arrived; the same note again is kept once
	notes []note
}

// note is one secondary line of a machine: a path the catalog could not
// read, a list cut at its bound, or a scan that did not finish. It changes
// neither the machine's state nor its rows.
type note struct {
	reason string // "catalog", "cut", "incomplete"
	path   string
	text   string
}

// maxCatalogRows is the bound of a machine's catalog (3.5.3), the number
// ui.ListPhrase("cut") names.
const maxCatalogRows = 2000

// line is the note as the hub draws it: the path and why for a catalog
// error, the phrases of 5a.14.2 for a cut list and an unfinished scan; ""
// for a note there is nothing to say about.
func (n note) line() string {
	switch n.reason {
	case "catalog":
		switch {
		case n.path == "":
			return n.text
		case n.text == "":
			return n.path
		}
		return n.path + ": " + n.text
	case "cut":
		phrase := ui.ListPhrase("cut")
		// the phrase names the catalog's bound; a list of sessions or panes
		// is cut at its own one, which Err.Text carries
		if bound, err := strconv.Atoi(n.text); err == nil && bound > 0 {
			phrase = strings.Replace(phrase, ui.Number(maxCatalogRows), ui.Number(int64(bound)), 1)
		}
		return phrase
	case "incomplete":
		return ui.ListPhrase("incomplete")
	}
	return n.text
}

// noteLines is the lines of the machine's notes, in order.
func (mc *machine) noteLines() []string {
	var lines []string
	for _, n := range mc.notes {
		if l := n.line(); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

type catalogKey struct {
	path string
	kind Kind
}

func newMachine(name, state string) *machine {
	return &machine{name: name, state: state, reported: map[catalogKey]bool{}}
}

func (mc *machine) online() bool { return mc.state == "online" }

// stateOf is the state an event puts a machine in that the hub has not
// heard of before; data of a machine says it answered.
func stateOf(ev Event) string {
	switch ev.Kind {
	case Connecting:
		return "connecting"
	case Failed:
		return failedState(ev.Err.Retry)
	case Closed:
		return "off"
	}
	return "online"
}

// failedState is the link state of a failed machine by its retry state
// (3.5.2): retried with backoff, retried every 30 s because the reason needs
// the user, or not supervised and tried again when the hub opens.
func failedState(retry string) string {
	switch retry {
	case "retrying":
		return "retrying"
	case "in-30s":
		return "attention"
	}
	return "off"
}

// machine returns the machine of ev, adding it after the others the first
// time it is named.
func (m *Model) machine(ev Event) *machine {
	for _, mc := range m.machines {
		if mc.name == ev.Machine {
			return mc
		}
	}
	mc := newMachine(ev.Machine, stateOf(ev))
	at := len(m.machines)
	if m.sortMachines {
		at = 1
		for at < len(m.machines) && m.machines[at].name < ev.Machine {
			at++
		}
	}
	m.machines = append(m.machines, nil)
	copy(m.machines[at+1:], m.machines[at:])
	m.machines[at] = mc
	return mc
}

// apply takes one event in (3.5.3). Without Features.Machines events of
// other machines are dropped; without Features.Agents agent lists are.
func (m *Model) apply(ev Event) {
	if ev.Machine != "" && !m.features.Machines {
		return
	}
	mc := m.machine(ev)
	switch ev.Kind {
	case Connecting:
		mc.state, mc.problem = "connecting", Problem{}
	case Online:
		if !mc.online() {
			// a new link starts a new scan: what the last one said of
			// itself is said again if it still holds
			mc.complete = false
			mc.reported = map[catalogKey]bool{}
			mc.notes = nil
		}
		mc.state, mc.problem = "online", Problem{}
	case Items:
		mc.loaded = true
		for _, item := range ev.Items {
			item.Machine = ev.Machine
			key := catalogKey{item.Path, item.Kind}
			mc.reported[key] = true
			replaced := false
			for i := range mc.catalog {
				if (catalogKey{mc.catalog[i].Path, mc.catalog[i].Kind}) == key {
					mc.catalog[i], replaced = item, true
					break
				}
			}
			if !replaced {
				mc.catalog = append(mc.catalog, item)
			}
		}
	case Git:
		m.git[gitKey{ev.Machine, ev.Path}] = ev.Git
	case Complete:
		kept := mc.catalog[:0]
		for _, item := range mc.catalog {
			if mc.reported[catalogKey{item.Path, item.Kind}] {
				kept = append(kept, item)
			}
		}
		mc.catalog = kept
		mc.reported = map[catalogKey]bool{}
		mc.complete, mc.loaded = true, true
	case Sessions:
		mc.sessionsKnown = true
		mc.sessions = mc.sessions[:0]
		for _, item := range ev.Items {
			item.Machine, item.Kind = ev.Machine, KindSession
			// every session the list holds is open
			item.State.Open = true
			mc.sessions = append(mc.sessions, item)
		}
	case Agents:
		if !m.features.Agents {
			return
		}
		mc.agents = mc.agents[:0]
		for _, item := range ev.Items {
			item.Machine, item.Kind = ev.Machine, KindAgent
			mc.agents = append(mc.agents, item)
		}
	case Failed:
		mc.state, mc.problem = failedState(ev.Err.Retry), ev.Err
		// without machine headings a failure of this machine is said in
		// the footer, where every failure of today's hub is (5a.6.7)
		if ev.Machine == "" && !m.features.Machines && ev.Err.Text != "" {
			m.message, m.failed = ev.Err.Text, true
		}
	case Closed:
		mc.state, mc.problem = "off", Problem{}
	case Note:
		n := note{reason: ev.Err.Reason, path: ev.Path, text: ev.Err.Text}
		if !slices.Contains(mc.notes, n) {
			mc.notes = append(mc.notes, n)
		}
	}
}

// rank is the urgency of an agent status: blocked 4, done 3, working 2,
// idle 1, unknown 0. It is the table of agent.Rank (5.3.8), which is a stub
// until P2-05 and panics; the hub uses this copy so that it runs before.
func rank(status string) int {
	switch status {
	case "blocked":
		return 4
	case "done":
		return 3
	case "working":
		return 2
	case "idle":
		return 1
	}
	return 0
}

// joinItems lists, per machine in order, its catalog rows joined with the
// session of their directory, its sessions that belong to no catalog row,
// and its agents (3.5.3). The most urgent agent of a session shows on the
// session's row. A row of a machine that is not online is stale.
func (m *Model) joinItems() {
	m.items = m.items[:0:0]
	for _, mc := range m.machines {
		online := mc.online()
		urgent := map[string]Item{}
		for _, a := range mc.agents {
			if a.SessionID == "" {
				continue
			}
			if cur, ok := urgent[a.SessionID]; !ok || rank(a.Status) > rank(cur.Status) {
				urgent[a.SessionID] = a
			}
		}
		byDir := map[string]int{}
		for i, s := range mc.sessions {
			if s.Path == "" {
				continue
			}
			if _, ok := byDir[filepath.Clean(s.Path)]; !ok {
				byDir[filepath.Clean(s.Path)] = i
			}
		}
		used := make([]bool, len(mc.sessions))
		decorate := func(item Item) Item {
			if a, ok := urgent[item.SessionID]; ok && item.SessionID != "" {
				item.State.Agent = a.Status
				if a.State.Title != "" {
					item.State.Title = a.State.Title
				}
			}
			if !m.features.Agents {
				item.State.Agent, item.State.Title = "", ""
			}
			item.Stale = item.Stale || !online
			return item
		}
		for _, item := range mc.catalog {
			if item.Path != "" {
				if i, ok := byDir[filepath.Clean(item.Path)]; ok {
					s := mc.sessions[i]
					item.SessionID, item.State = s.SessionID, s.State
					used[i] = true
				}
			}
			m.items = append(m.items, decorate(item))
		}
		for i, s := range mc.sessions {
			if !used[i] {
				m.items = append(m.items, decorate(s))
			}
		}
		if m.features.Agents {
			for _, a := range mc.agents {
				a.Stale = a.Stale || !online
				m.items = append(m.items, a)
			}
		}
	}
	m.here = m.findHere()
	m.hereAgent = m.findHereAgent()
}

type rowKind int

const (
	rowHeader rowKind = iota
	rowItem
	// rowHost heads the items of another machine, under a rowSpace
	rowHost
	rowSpace
	// rowNote is a line of text in place of rows: loading, or empty
	rowNote
)

// row is one line of the list: a machine or project heading, an item, or
// a note.
type row struct {
	kind    rowKind
	item    int    // index into Model.items for rowItem
	machine int    // index into Model.machines for rowHost
	project string // the project of a heading, the machine of rowHost, the text of rowNote
	child   bool   // a feature worktree, indented under its project
	// rune positions the query matched, in the project and in the worktree
	projectHits []int
	nameHits    []int
}

// group is the rows of one project, or one row that belongs to none.
type group struct {
	project string
	machine int
	best    int
	members []scored
}

type scored struct {
	item        int
	score       int
	projectHits []int
	nameHits    []int
}

// matchText is what the query is matched against: the label, after the
// machine's name for a row of another machine, so typing the machine lists
// what is on it. An agent is matched by its session.
func matchText(item Item) (string, int) {
	text := item.Label
	if item.Kind == KindAgent {
		text = item.Session
	}
	if item.remote() {
		return item.Machine + "/" + text, len([]rune(item.Machine)) + 1
	}
	return text, 0
}

// refilter rebuilds the rows for the query and the scope and puts the
// cursor where enter most likely means: on the best match, on where you are
// when nothing is typed, or on the create row when nothing matches or a
// feature is asked for.
func (m *Model) refilter() {
	m.query = m.input.Value()
	query := strings.TrimSpace(m.query)
	m.create, m.noCreate = m.createRequest(query)
	m.rows = m.rows[:0]
	if m.scope == scopeAgents {
		m.refilterAgents(query)
		return
	}

	machineIndex := make(map[string]int, len(m.machines))
	for i, mc := range m.machines {
		machineIndex[mc.name] = i
	}
	var hereItem *Item
	if m.here >= 0 {
		hereItem = &m.items[m.here]
	}
	var matches []scored
	for i, item := range m.items {
		if item.Kind == KindAgent {
			continue
		}
		if m.scope == scopeSessions && !item.State.Open {
			continue
		}
		if m.newMode && hereItem != nil && hereItem.grouped() && !sameProject(item, *hereItem) {
			continue
		}
		text, cut := matchText(item)
		score, hits, ok := match(query, text)
		if !ok {
			continue
		}
		hits = shift(hits, cut)
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
	var groups []*group
	byProject := map[string]*group{}
	for _, s := range matches {
		item := m.items[s.item]
		key := "\x00" + item.Machine + "\x00" + item.Label + "\x00" + item.SessionID + "\x00" + item.Path
		if item.grouped() {
			key = item.Machine + "\x00" + item.Root + "\x00" + item.Project
		}
		g, ok := byProject[key]
		if !ok {
			g = &group{project: item.Project, best: s.score, machine: machineIndex[item.Machine]}
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
	byMachine := make([][]*group, len(m.machines))
	for _, g := range groups {
		byMachine[g.machine] = append(byMachine[g.machine], g)
	}

	// this machine comes first, without a heading; then every other one in
	// its own section, in config order
	for mi, mc := range m.machines {
		gs := byMachine[mi]
		if mi == 0 {
			// this machine has no heading: its notes head its rows
			m.appendNotes(mc)
			if len(gs) == 0 {
				if note := m.localNote(query, len(matches) > 0); note != "" {
					m.rows = append(m.rows, row{kind: rowNote, project: note})
				}
			}
		} else {
			// the heading of an online machine without a matching row goes;
			// one that is not online stays under every filter
			if len(gs) == 0 && mc.online() && query != "" {
				continue
			}
			if len(m.rows) > 0 {
				m.rows = append(m.rows, row{kind: rowSpace})
			}
			m.rows = append(m.rows, row{kind: rowHost, machine: mi, project: mc.name})
			m.appendNotes(mc)
			if len(gs) == 0 && mc.online() && mc.complete && len(mc.catalog) == 0 && m.scope == scopeAll {
				m.rows = append(m.rows, row{kind: rowNote, project: ui.Empty("machine", mc.name)})
			}
		}
		m.appendGroups(gs)
	}

	m.cursor = 0
	switch {
	case m.newMode || (query != "" && len(matches) == 0):
		m.cursor = len(m.selectable())
	case query != "":
		// the best match, the topmost row among equals, so a query naming
		// only the project lands on its main worktree rather than a
		// feature listed below it
		scores := make(map[int]int, len(matches))
		best := matches[0].score
		for _, s := range matches {
			scores[s.item] = s.score
			best = max(best, s.score)
		}
		for i, r := range m.selectable() {
			if score, ok := scores[m.rows[r].item]; ok && score == best {
				m.cursor = i
				break
			}
		}
	case m.here >= 0:
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

// appendNotes adds the secondary lines of a machine under its heading, or
// above this machine's rows, which have none (3.5.3).
func (m *Model) appendNotes(mc *machine) {
	for _, l := range mc.noteLines() {
		m.rows = append(m.rows, row{kind: rowNote, project: l})
	}
}

// appendGroups adds the rows of the groups: a project is one row, its main
// worktree, with its features indented below; when main is filtered out or
// missing, a heading stands in.
func (m *Model) appendGroups(groups []*group) {
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
}

// localNote is the text in place of this machine's rows when it has none
// (5a.14.3), "" for none.
func (m *Model) localNote(query string, matched bool) string {
	local := m.machines[0]
	loading := func(kind string) string {
		if m.slow {
			return ui.Empty(kind, "")
		}
		return ""
	}
	held := false
	for _, item := range m.items {
		if item.Kind != KindAgent {
			held = true
			break
		}
	}
	switch {
	case !local.loaded:
		return loading("loading")
	case m.scope == scopeSessions && !local.sessionsKnown:
		return loading("loading-sessions")
	case matched:
		return ""
	case !held:
		return ui.Empty("projects", "")
	case query == "" && m.scope == scopeSessions:
		return ui.Empty("sessions", "")
	case m.noCreate != "":
		return m.noCreate
	case m.create == nil && query != "":
		return ui.Empty("query", query)
	}
	return ""
}

// refilterAgents builds the agents scope (3.6.4): one row per agent of
// every machine, most urgent first, then this machine first and config
// order, then session label, then pane id; below them the headings of the
// machines that are not online, so "no agents" and "failed" differ.
func (m *Model) refilterAgents(query string) {
	machineIndex := make(map[string]int, len(m.machines))
	for i, mc := range m.machines {
		machineIndex[mc.name] = i
	}
	var list []scored
	for i, item := range m.items {
		if item.Kind != KindAgent {
			continue
		}
		text, cut := matchText(item)
		_, hits, ok := match(query, text)
		if !ok {
			continue
		}
		list = append(list, scored{item: i, nameHits: shift(hits, cut)})
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := m.items[list[i].item], m.items[list[j].item]
		if ra, rb := rank(a.Status), rank(b.Status); ra != rb {
			return ra > rb
		}
		if ma, mb := machineIndex[a.Machine], machineIndex[b.Machine]; ma != mb {
			return ma < mb
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Pane < b.Pane
	})
	if len(list) == 0 {
		note := ui.Empty("agents", "")
		if query != "" {
			note = ui.Empty("query", query)
		}
		m.rows = append(m.rows, row{kind: rowNote, project: note})
	}
	for _, s := range list {
		m.rows = append(m.rows, row{kind: rowItem, item: s.item, nameHits: s.nameHits})
	}
	for mi, mc := range m.machines {
		if mi == 0 || mc.online() {
			continue
		}
		m.rows = append(m.rows, row{kind: rowSpace}, row{kind: rowHost, machine: mi, project: mc.name})
	}
	m.cursor = 0
	m.offset = 0
	m.clampScroll()
}
