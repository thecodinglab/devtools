package fleet

import (
	"slices"
	"strings"
	"time"

	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/ui"
)

// The discovery stream of 3.5.3: what the hub gets while it is open.

// catalogSilence completes a catalog that sends nothing for this long, with
// a note that the scan did not finish.
const catalogSilence = 30 * time.Second

// Reasons of a hub.Note, a secondary line under a machine's heading that
// does not change its state: "catalog" (a catalog.error; Path and Err.Text
// say which path and why), "cut" (a list was cut at the number in Err.Text)
// or "incomplete" (the scan did not finish).
const (
	noteCatalog    = "catalog"
	noteCut        = "cut"
	noteIncomplete = "incomplete"
)

// catalogState is the catalog of one machine: the stream running now, and
// the rows of the last scan that finished, shown at once when the hub opens
// again (DS11).
type catalogState struct {
	watch   uint64 // id of the running catalog.watch; 0 none
	scan    []hub.Item
	seen    map[catKey]int // row → index in scan
	paths   map[string]bool
	git     map[string]ui.Git
	cut     bool
	silence *timer

	cached   bool
	cache    []hub.Item
	cacheGit map[string]ui.Git
	cacheCut bool
}

type catKey struct {
	path string
	kind hub.Kind
}

// hubEvent hands ev to the hub, if it is open.
func (f *Fleet) hubEvent(ev hub.Event) {
	if f.hub != nil {
		f.hub.put(ev)
	}
}

// openHub starts discovery (3.5.3): one event per machine at once, local
// first, then config order; then what each machine has, or a dial.
func (f *Fleet) openHub(p *hubQueue) {
	if f.hub != nil {
		for _, mc := range f.machines {
			f.stopCatalog(mc, true)
		}
		f.hub.close()
	}
	f.hub = p
	f.stopIdle()
	now := f.clock.Now()
	for _, mc := range f.list() {
		switch {
		case mc.online():
			f.hubOnline(mc)
		case !mc.dialing && mc.status == "attention" && !loginHelps(mc) && now.Sub(mc.lastTry) < attentionGap:
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Failed, Err: mc.problem})
		default:
			// opening the hub is the user's presence: its dial may log in
			// (3.5.2), and is the retry the words of auth and login ask for
			f.grantLogin(mc, userGrant)
			if mc.dialing {
				f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Connecting})
			} else {
				f.dial(mc)
			}
		}
	}
}

// closeHub ends discovery: every catalog stream is stopped and the idle
// timers of on-demand machines start.
func (f *Fleet) closeHub() {
	if f.hub == nil {
		return
	}
	for _, mc := range f.machines {
		f.stopCatalog(mc, true)
	}
	f.hub.close()
	f.hub = nil
	for _, mc := range f.machines {
		f.startIdle(mc)
	}
}

// hubOnline is what the hub gets of a machine that is online: the state,
// its sessions and agents, the cached catalog, and then a new scan.
func (f *Fleet) hubOnline(mc *machine) {
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Online})
	f.hubState(mc)
	f.hubCache(mc, true)
	f.startCatalog(mc)
}

// hubCache is the catalog of mc's last finished scan, if there is one; with
// complete, it ends there and the hub removes the rows it does not hold.
func (f *Fleet) hubCache(mc *machine, complete bool) {
	if !mc.cat.cached {
		return
	}
	f.hubRows(mc, mc.cat.cache, mc.cat.cacheGit, mc.cat.cacheCut)
	if complete {
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Complete})
	}
}

// hubRows is catalog rows of mc, their git states and the note of a cut.
func (f *Fleet) hubRows(mc *machine, rows []hub.Item, git map[string]ui.Git, cut bool) {
	if len(rows) > 0 {
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Items, Items: slices.Clone(rows)})
	}
	for _, it := range rows {
		if g, ok := git[it.Path]; ok {
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Git, Path: it.Path, Git: g})
		}
	}
	if cut {
		f.note(mc, noteCut, "", itoa(maxCatalog))
	}
}

// hubCaughtUp is the hub's queue saying that everything of the machine
// named that waited, after it was held back, has left (hubqueue.go).
func (f *Fleet) hubCaughtUp(p *hubQueue, name string) {
	mc := f.byName[name]
	if f.hub != p || mc == nil {
		return
	}
	p.resume(name)
	f.hubResync(mc)
}

// hubResync sends the hub mc's whole state again, after events of mc were
// dropped because the hub did not read them (hubqueue.go). The hub ends as
// it would have with every event, except for the notes in between.
func (f *Fleet) hubResync(mc *machine) {
	switch {
	case mc.dropped:
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Closed})
	case mc.online():
		// Connecting first, so that Online starts a new scan in the hub,
		// which forgets what the scans it saw reported: the next Complete
		// keeps only rows sent from here on
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Connecting})
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Online})
		f.hubState(mc)
		f.hubCache(mc, true)
		switch c := &mc.cat; {
		case c.watch != 0:
			// the running scan, as far as it got
			f.hubRows(mc, c.scan, c.git, c.cut)
		case !hasCap(mc, proto.CapCatalog):
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Complete})
		}
	default:
		// the rows stay, stale (3.5.3 step 6), as they were when the link
		// ended
		if mc.st != nil {
			f.hubState(mc)
		}
		f.hubCache(mc, false)
		if mc.dialing {
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Connecting})
		} else {
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Failed, Err: mc.problem})
		}
	}
}

// hubState is the sessions and agents of mc, for the hub.
func (f *Fleet) hubState(mc *machine) {
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Sessions, Items: sessionItems(mc)})
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Agents, Items: agentItems(mc)})
}

// stateChanged follows a change of mc's state: the status row, and the hub
// while it is open (3.5.3 step 7).
func (f *Fleet) stateChanged(mc *machine) {
	f.dirty = true
	if f.hub != nil && mc.online() {
		f.hubState(mc)
	}
}

// note puts a secondary line under mc's heading.
func (f *Fleet) note(mc *machine, reason, path, text string) {
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Note, Path: path, Err: hub.Problem{Reason: reason, Text: text}})
}

// listCut says that a list of mc was cut at n.
func (f *Fleet) listCut(mc *machine, n int) { f.note(mc, noteCut, "", itoa(n)) }

// hasCap reports whether the server of mc speaks the wire family c.
func hasCap(mc *machine, c string) bool { return slices.Contains(mc.welcome.Caps, c) }

// startCatalog sends catalog.watch on mc's link (3.5.3 step 4). A server
// without the catalog family has nothing to list: its scan is complete at
// once.
func (f *Fleet) startCatalog(mc *machine) {
	f.stopCatalog(mc, true)
	if !hasCap(mc, proto.CapCatalog) {
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Complete})
		return
	}
	id := mc.newID()
	if !f.send(mc, proto.Request(id, proto.CatalogWatch{})) {
		return
	}
	mc.cat.watch = id
	mc.cat.scan = nil
	mc.cat.seen = map[catKey]int{}
	mc.cat.paths = map[string]bool{}
	mc.cat.git = map[string]ui.Git{}
	mc.cat.cut = false
	f.armSilence(mc)
}

// armSilence (re)starts the 30 s silence limit of mc's running catalog.
func (f *Fleet) armSilence(mc *machine) {
	mc.cat.silence.stop()
	id := mc.cat.watch
	mc.cat.silence = f.after(catalogSilence, func() {
		if mc.cat.watch != id {
			return
		}
		f.note(mc, noteIncomplete, "", "")
		f.finishCatalog(mc, true)
	})
}

// stopCatalog ends mc's running catalog stream, telling the server when
// send is set. What it had delivered is forgotten; the cache stays.
func (f *Fleet) stopCatalog(mc *machine, send bool) {
	if mc.cat.watch == 0 {
		return
	}
	if send {
		f.send(mc, proto.Request(mc.cat.watch, proto.CatalogStop{}))
	}
	mc.cat.watch = 0
	mc.cat.silence.stop()
	mc.cat.silence = nil
}

// finishCatalog ends mc's scan: its rows become the cache and the hub
// removes the rows it did not report. stop tells the server to give up.
func (f *Fleet) finishCatalog(mc *machine, stop bool) {
	c := &mc.cat
	c.cached, c.cache, c.cacheGit, c.cacheCut = true, c.scan, c.git, c.cut
	f.stopCatalog(mc, stop)
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Complete})
}

// catalogMessage handles one message of mc's catalog stream (3.5.3 step 5).
// A message of a stream that is not running is dropped.
func (f *Fleet) catalogMessage(mc *machine, m proto.Msg) {
	c := &mc.cat
	if f.hub == nil || c.watch == 0 || m.ID != c.watch {
		return
	}
	f.armSilence(mc)
	switch m.T {
	case proto.TypeCatalogItem:
		var ci proto.CatalogItem
		if m.Decode(&ci) != nil {
			return
		}
		var rows []hub.Item
		wasCut := c.cut
		for _, e := range ci.Items {
			it, ok := cleanEntry(mc.name, e)
			if !ok {
				continue
			}
			k := catKey{it.Path, it.Kind}
			if i, ok := c.seen[k]; ok {
				c.scan[i] = it
			} else if len(c.scan) >= maxCatalog {
				c.cut = true
				continue
			} else {
				c.seen[k] = len(c.scan)
				c.scan = append(c.scan, it)
			}
			c.paths[it.Path] = true
			rows = append(rows, it)
		}
		if len(rows) > 0 {
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Items, Items: rows})
		}
		if c.cut && !wasCut {
			f.listCut(mc, maxCatalog)
		}
	case proto.TypeCatalogGit:
		var g proto.CatalogGit
		if m.Decode(&g) != nil || !c.paths[g.Path] {
			return
		}
		git := cleanGit(g.Git)
		c.git[g.Path] = git
		f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Git, Path: g.Path, Git: git})
	case proto.TypeCatalogError:
		var e proto.CatalogError
		if m.Decode(&e) != nil {
			return
		}
		f.note(mc, noteCatalog, cleanText(e.Path), cleanText(e.Text))
	case proto.TypeCatalogDone:
		f.finishCatalog(mc, false)
	}
}

// sessionItems is the open sessions of mc as hub rows, in snapshot order;
// a session shows its most urgent agent.
func sessionItems(mc *machine) []hub.Item {
	st := mc.st
	if st == nil {
		return []hub.Item{}
	}
	top := map[string]proto.Agent{}
	for _, a := range st.Agents {
		if cur, ok := top[a.Session]; !ok || rank(a.Status) > rank(cur.Status) ||
			(rank(a.Status) == rank(cur.Status) && a.Pane < cur.Pane) {
			top[a.Session] = a
		}
	}
	items := make([]hub.Item, 0, len(mc.order))
	for _, id := range mc.order {
		s, ok := st.Sessions[id]
		if !ok {
			continue
		}
		it := hub.Item{
			Kind:      hub.KindSession,
			Label:     s.Label,
			Path:      s.Dir,
			Session:   s.Label,
			SessionID: s.ID,
			Machine:   mc.name,
			State:     ui.Session{Open: true},
		}
		if a, ok := top[s.ID]; ok {
			it.State.Agent, it.State.Title = a.Status, a.Title
		}
		items = append(items, it)
	}
	return items
}

// agentItems is the agents of mc as hub rows: by urgency, then session
// label, then pane id (3.6.4).
func agentItems(mc *machine) []hub.Item {
	st := mc.st
	if st == nil {
		return []hub.Item{}
	}
	items := make([]hub.Item, 0, len(st.Agents))
	for _, a := range st.Agents {
		label := ""
		if s, ok := st.Sessions[a.Session]; ok {
			label = s.Label
		}
		it := hub.Item{
			Kind:      hub.KindAgent,
			Label:     label,
			Path:      a.Cwd,
			Session:   label,
			SessionID: a.Session,
			Machine:   mc.name,
			Tab:       a.Tab,
			Pane:      a.Pane,
			Agent:     a.Kind,
			Status:    a.Status,
			State:     ui.Session{Open: true, Agent: a.Status, Title: a.Title},
		}
		if t, ok := st.Tabs[a.Tab]; ok {
			it.TabIndex = t.Index
		}
		items = append(items, it)
	}
	slices.SortFunc(items, func(a, b hub.Item) int {
		if ra, rb := rank(a.Status), rank(b.Status); ra != rb {
			return rb - ra
		}
		if c := strings.Compare(a.Session, b.Session); c != 0 {
			return c
		}
		return strings.Compare(a.Pane, b.Pane)
	})
	return items
}
