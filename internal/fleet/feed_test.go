package fleet_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"devtools/internal/agent"
	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// firstIndex is the index of the first event of machine in evs, -1 if none.
func firstIndex(evs []hub.Event, machine string) int {
	return slices.IndexFunc(evs, func(ev hub.Event) bool { return ev.Machine == machine })
}

// DS1
func TestASlowMachineDoesNotDelayTheOthers(t *testing.T) {
	r := start(t, "M", "K")
	answer := r.M("M").Later()
	r.M("K").Online(fleettest.Snapshot(fleettest.Sess{N: 7, Dir: "/home/florian/dev/k", Label: "k"}))
	feed := r.OpenHub()
	feed.Next("", hub.Sessions)
	feed.Next("K", hub.Sessions)
	feed.None("M", hub.Online)
	evs := feed.All()
	l, m, k := firstIndex(evs, ""), firstIndex(evs, "M"), firstIndex(evs, "K")
	if !(l == 0 && l < m && m < k) {
		t.Fatalf("first events at %d, %d, %d; want the headings in the order local, M, K", l, m, k)
	}
	if evs[m].Kind != hub.Connecting || evs[k].Kind != hub.Connecting {
		t.Fatalf("first events of M and K %v, %v; want connecting", evs[m].Kind, evs[k].Kind)
	}
	sessionsK := slices.IndexFunc(evs, func(ev hub.Event) bool { return ev.Machine == "K" && ev.Kind == hub.Sessions })
	if sessionsK < m {
		t.Fatalf("K's rows came before M's heading")
	}
	r.Advance(8 * time.Second)
	answer(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/m", Label: "m"}))
	feed.Next("M", hub.Online)
	if ev := feed.Next("M", hub.Sessions); len(ev.Items) != 1 || ev.Items[0].Machine != "M" {
		t.Fatalf("M's sessions %+v", ev.Items)
	}
}

// DS5
func TestRowsOfALostMachineGoStaleAndCannotBePicked(t *testing.T) {
	r := start(t, "M", "K")
	r.M("M").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/m", Label: "m"}))
	r.M("K").Online(fleettest.Snapshot(fleettest.Sess{N: 3, Dir: "/home/florian/dev/k", Label: "k"}))
	feed := r.OpenHub()
	feed.Next("M", hub.Sessions)
	feed.Next("K", hub.Sessions)
	r.M("M").Drop()
	ev := feed.Next("M", hub.Failed)
	if ev.Err.Reason != "lost" || ev.Err.Retry != "next-open" {
		t.Fatalf("heading %+v, want lost and retried when the hub opens", ev.Err)
	}
	put := proto.Put(proto.Session{ID: "s4", Dir: "/home/florian/dev/k2", Label: "k2"})
	r.M("K").Push(proto.Encode(proto.Delta{Rev: 2, Changes: []proto.Change{put}}))
	if ev := feed.Next("K", hub.Sessions); len(ev.Items) != 2 {
		t.Fatalf("K's sessions %+v after M was lost, want two", ev.Items)
	}
	feed.None("K", hub.Failed)
	feed.None("", hub.Failed)
}

// DS8
func TestLocalRowsArriveThroughTheSameEvents(t *testing.T) {
	r := start(t)
	feed := r.OpenHub()
	feed.Next("", hub.Online)
	ev := feed.Next("", hub.Sessions)
	if len(ev.Items) != 1 || ev.Items[0].Path != here.Dir || ev.Items[0].SessionID != "s1" || ev.Items[0].Kind != hub.KindSession {
		t.Fatalf("local sessions %+v", ev.Items)
	}
	feed.Next("", hub.Agents)
	id := watchID(t, r, "", 1)
	r.M("").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry(here.Dir, "main")}}))
	r.M("").Push(proto.Request(id, proto.CatalogGit{Path: here.Dir, Git: proto.Git{Changes: 2, Branch: "main"}}))
	r.M("").Push(proto.Request(id, proto.CatalogDone{}))
	items := feed.Next("", hub.Items)
	if len(items.Items) != 1 || items.Items[0].Path != here.Dir || items.Items[0].Machine != "" {
		t.Fatalf("local rows %+v", items.Items)
	}
	if g := feed.Next("", hub.Git); g.Path != here.Dir || g.Git.Changes != 2 || g.Git.Branch != "main" {
		t.Fatalf("git %+v", g)
	}
	feed.Next("", hub.Complete)
}

// DS9
func TestACatalogErrorDoesNotFailTheMachine(t *testing.T) {
	r := start(t, "M")
	r.M("M").Online(fleettest.Snapshot())
	feed := r.OpenHub()
	feed.Next("M", hub.Online)
	id := watchID(t, r, "M", 1)
	r.M("M").Push(proto.Request(id, proto.CatalogError{Path: "/home/florian/work", Text: "permission denied"}))
	note := feed.Next("M", hub.Note)
	if note.Err.Reason != "catalog" || note.Path != "/home/florian/work" || note.Err.Text != "permission denied" {
		t.Fatalf("note %+v, want the catalog error", note)
	}
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/a/main", "main")}}))
	r.M("M").Push(proto.Request(id, proto.CatalogDone{}))
	feed.Next("M", hub.Items)
	feed.Next("M", hub.Complete)
	feed.None("M", hub.Failed)
	if !r.M("M").Live() {
		t.Fatal("a catalog error ended the link")
	}
}

func TestASilentCatalogIsCompletedAsUnfinished(t *testing.T) {
	r := start(t, "M")
	r.M("M").Online(fleettest.Snapshot())
	feed := r.OpenHub()
	feed.Next("M", hub.Online)
	id := watchID(t, r, "M", 1)
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/a/main", "main")}}))
	feed.Next("M", hub.Items)
	r.Clock.WaitTimer(t, 30*time.Second)
	r.Advance(30 * time.Second)
	if note := feed.Next("M", hub.Note); note.Err.Reason != "incomplete" {
		t.Fatalf("note %+v, want incomplete", note.Err)
	}
	feed.Next("M", hub.Complete)
	r.Eventually("catalog.stop", func() bool { return len(r.M("M").SentOf(proto.TypeCatalogStop)) == 1 })
}

// DS11
func TestReopeningTheHubShowsCachedRowsThenRefreshes(t *testing.T) {
	r := start(t, "K")
	r.M("K").Online(fleettest.Snapshot())
	feed := r.OpenHub()
	feed.Next("K", hub.Online)
	id := watchID(t, r, "K", 1)
	a, b := entry("/home/florian/dev/p/a", "a"), entry("/home/florian/dev/p/b", "b")
	r.M("K").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{a, b}}))
	r.M("K").Push(proto.Request(id, proto.CatalogGit{Path: a.Path, Git: proto.Git{Ahead: 1}}))
	r.M("K").Push(proto.Request(id, proto.CatalogDone{}))
	feed.Next("K", hub.Complete)
	r.F.HubClosed()
	feed.WaitClosed()
	if n := len(r.M("K").SentOf(proto.TypeCatalogStop)); n != 0 {
		t.Fatalf("%d catalog.stop after a finished scan, want none", n)
	}
	r.Advance(5 * time.Minute)

	again := r.OpenHub()
	again.Next("K", hub.Online)
	cached := again.Next("K", hub.Items)
	if len(cached.Items) != 2 || cached.Items[0].Path != a.Path || cached.Items[1].Path != b.Path {
		t.Fatalf("cached rows %+v, want a and b at once", cached.Items)
	}
	if g := again.Next("K", hub.Git); g.Path != a.Path || g.Git.Ahead != 1 {
		t.Fatalf("cached git %+v", g)
	}
	again.Next("K", hub.Complete)
	id2 := watchID(t, r, "K", 2)
	if id2 == id {
		t.Fatalf("the second watch has the id of the first, %d", id)
	}
	r.M("K").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/p/old", "old")}}))
	r.M("K").Push(proto.Request(id2, proto.CatalogItem{Items: []proto.CatalogEntry{a}}))
	r.M("K").Push(proto.Request(id2, proto.CatalogDone{}))
	fresh := again.Next("K", hub.Items)
	if len(fresh.Items) != 1 || fresh.Items[0].Path != a.Path {
		t.Fatalf("refreshed rows %+v, want a only (a reply of the old watch is dropped)", fresh.Items)
	}
	again.Next("K", hub.Complete)
	if r.M("K").Dials() != 1 {
		t.Fatalf("K was dialled %d times, want 1", r.M("K").Dials())
	}
}

func TestClosingTheHubStopsTheRunningScan(t *testing.T) {
	r := start(t)
	feed := r.OpenHub()
	feed.Next("", hub.Online)
	id := watchID(t, r, "", 1)
	r.F.HubClosed()
	feed.WaitClosed()
	r.Eventually("catalog.stop", func() bool { return len(r.M("").SentOf(proto.TypeCatalogStop)) == 1 })
	if got := r.M("").SentOf(proto.TypeCatalogStop)[0].ID; got != id {
		t.Fatalf("catalog.stop with id %d, want the watch's %d", got, id)
	}
}

// SEC7
func TestAPathWithRemovedCharactersStillOpens(t *testing.T) {
	r := start(t, "M")
	dir := "/home/florian/dev/a\x1b[31mb‮c"
	r.M("M").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: dir, Label: "a\x1b[31mb"}))
	feed := r.OpenHub()
	sessions := feed.Next("M", hub.Sessions)
	if len(sessions.Items) != 1 {
		t.Fatalf("sessions %+v", sessions.Items)
	}
	s := sessions.Items[0]
	if s.Path != dir {
		t.Fatalf("session path %q, want the value from the wire %q", s.Path, dir)
	}
	if s.Label != "a[31mb" {
		t.Fatalf("session label %q, want it sanitised", s.Label)
	}
	id := watchID(t, r, "M", 1)
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry(dir, "a\x1bb")}}))
	r.M("M").Push(proto.Request(id, proto.CatalogGit{Path: dir, Git: proto.Git{Changes: 1}}))
	row := feed.Next("M", hub.Items).Items[0]
	if row.Path != dir || row.Path != s.Path {
		t.Fatalf("row path %q, want %q, the key that joins the session", row.Path, dir)
	}
	if row.Label != "ab" {
		t.Fatalf("row label %q, want it sanitised", row.Label)
	}
	if g := feed.Next("M", hub.Git); g.Path != dir {
		t.Fatalf("git path %q, want %q", g.Path, dir)
	}
}

// SEC5
func TestNothingFromTheWireReachesACommandLine(t *testing.T) {
	r := start(t, "M connect", "K")
	hostile := []string{"-oProxyCommand=touch /tmp/pwned", "evil; rm -rf ~", "$(reboot)", "user@host", "K2", "../K"}
	w := r.M("M").Welcome()
	w.Host = hostile[0]
	r.M("M").SetWelcome(w)
	var ss []fleettest.Sess
	for i, h := range hostile {
		ss = append(ss, fleettest.Sess{N: i + 2, Dir: "/" + h, Label: h, Agent: proto.StatusWorking, Title: h})
	}
	online(t, r, "M", fleettest.Snapshot(ss...))
	feed := r.OpenHub()
	id := watchID(t, r, "M", 1)
	var rows []proto.CatalogEntry
	for _, h := range hostile {
		rows = append(rows, entry("/"+h, h), proto.CatalogEntry{Kind: proto.CatalogRepo, Path: h, Label: h})
	}
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: rows}))
	r.M("M").Push(proto.Request(id, proto.CatalogError{Path: hostile[0], Text: hostile[1]}))
	for _, h := range hostile {
		r.M("M").Push(proto.Encode(proto.UISwitch{Dir: "/" + h, Name: h}))
		r.M("M").Push(proto.Encode(proto.UIHub{Mode: proto.HubAll, Here: h}))
	}
	r.M("M").Push(fleettest.AgentEvent(2, proto.StatusBlocked, false))
	feed.Next("M", hub.Items)
	r.M("M").Drop()
	feed.Next("M", hub.Failed)
	for range 5 {
		r.Advance(time.Minute)
	}
	r.F.HubClosed()
	r.OpenHub()
	r.Advance(time.Hour)
	time.Sleep(fleettest.Quiet)
	for _, name := range r.Dialer.Names() {
		if name != "" && name != "M" && name != "K" {
			t.Fatalf("the dialer was asked for %q; only config aliases may be dialled", name)
		}
	}
}

// strings walks v and calls fn with every string in it, with the name of
// its field. Item.Path and Item.Root are keys and are not shown: they are
// left out.
func walkStrings(v reflect.Value, field string, fn func(field, s string)) {
	switch v.Kind() {
	case reflect.String:
		fn(field, v.String())
	case reflect.Struct:
		for i := range v.NumField() {
			name := v.Type().Field(i).Name
			if v.Type() == reflect.TypeOf(hub.Item{}) && (name == "Path" || name == "Root") {
				continue
			}
			walkStrings(v.Field(i), name, fn)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkStrings(v.Index(i), field, fn)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			walkStrings(k, field, fn)
			walkStrings(v.MapIndex(k), field, fn)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkStrings(v.Elem(), field, fn)
		}
	}
}

func clean(s string) bool { return session.Sanitize(s, len(s)+1) == s }

// SEC7
func TestHostileNamesAreDefusedBeforeTheyReachTheHub(t *testing.T) {
	bad := "\x1b[2J\x1b]52;c;aGVsbG8=\x07evil\rname‮exe⁦\x9b"
	r := start(t, "M connect")
	w := r.M("M").Welcome()
	w.Host = bad
	r.M("M").SetWelcome(w)
	snap := fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/x", Label: bad, Agent: proto.StatusWorking, Title: bad})
	snap.Panes[0].Title, snap.Panes[0].Program = bad, bad
	online(t, r, "M", snap)
	feed := r.OpenHub()
	id := watchID(t, r, "M", 1)
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{
		{Kind: proto.CatalogWorktree, Project: bad, Worktree: bad, Label: bad, Path: "/home/florian/dev/x", Root: "/home/florian/dev", Session: bad},
		{Kind: proto.CatalogBookmark, Path: "/home/florian/notes", Label: ""},
	}}))
	r.M("M").Push(proto.Request(id, proto.CatalogGit{Path: "/home/florian/dev/x", Git: proto.Git{Branch: bad}}))
	r.M("M").Push(proto.Request(id, proto.CatalogError{Path: bad, Text: bad}))
	r.M("M").Push(proto.Request(id, proto.CatalogDone{}))
	feed.Next("M", hub.Complete)
	s := r.Settle(func(s fleet.SummaryData) bool { return fleettest.MachineOf(t, s, "M").Host != "" })
	r.M("M").DropWith(nil)
	feed.Next("M", hub.Failed)

	n := 0
	check := func(field, s string) {
		n++
		if !clean(s) {
			t.Errorf("field %s reaches the hub or the status row as %q", field, s)
		}
	}
	for _, ev := range feed.All() {
		walkStrings(reflect.ValueOf(ev), "Event", check)
	}
	walkStrings(reflect.ValueOf(s), "Summary", check)
	if n < 10 {
		t.Fatalf("only %d strings were checked", n)
	}
	if h := fleettest.MachineOf(t, s, "M").Host; h != "[2J]52;c;aGVsbG8=evilnameexe\uFFFD" {
		t.Fatalf("host %q", h)
	}
}

// SEC10
func TestAnIdOfOneMachineIsNeverSentToAnother(t *testing.T) {
	r := start(t, "M connect", "K connect")
	a := fleettest.Sess{N: 4, Dir: "/home/florian/dev/m", Label: "m", Agent: proto.StatusWorking}
	b := fleettest.Sess{N: 4, Dir: "/home/florian/dev/k", Label: "k"}
	online(t, r, "M", fleettest.Snapshot(a))
	online(t, r, "K", fleettest.Snapshot(b))
	feed := r.OpenHub()
	sm, sk := feed.Next("M", hub.Sessions), feed.Next("K", hub.Sessions)
	if sm.Items[0].Machine != "M" || sm.Items[0].Path != a.Dir || sk.Items[0].Machine != "K" || sk.Items[0].Path != b.Dir {
		t.Fatalf("the session s4 of two machines is mixed up: %+v, %+v", sm.Items, sk.Items)
	}
	idM, idK := watchID(t, r, "M", 1), watchID(t, r, "K", 1)
	// a reply with an id that is not the one of K's watch is dropped
	r.M("K").Push(proto.Request(idK+100, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/x", "x")}}))
	r.M("M").Push(proto.Request(idM, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/m", "m")}}))
	if ev := feed.Next("M", hub.Items); ev.Items[0].Machine != "M" {
		t.Fatalf("rows %+v", ev.Items)
	}
	feed.None("K", hub.Items)
	// an invalid id from the wire is dropped
	bogus := proto.Put(proto.Session{ID: "s4; rm", Dir: "/home/florian/dev/y", Label: "y"})
	r.M("K").Push(proto.Encode(proto.Delta{Rev: 2, Changes: []proto.Change{bogus}}))
	if ev := feed.Next("K", hub.Sessions); len(ev.Items) != 1 || ev.Items[0].SessionID != "s4" {
		t.Fatalf("K's sessions %+v, want the invalid id dropped", ev.Items)
	}
	r.M("M").Push(fleettest.AgentEvent(4, proto.StatusBlocked, false))
	r.M("M").Push(fleettest.AgentDelta(2, 4, proto.StatusBlocked))
	got := r.Settle(func(s fleet.SummaryData) bool { return s.Urgent.Pane != "" })
	if got.Urgent != (session.Target{Machine: "M", Session: "s4", Tab: "t4", Pane: "p4"}) {
		t.Fatalf("urgent %+v, want the agent of M", got.Urgent)
	}
	r.F.HubClosed()
	r.Eventually("catalog.stop to K", func() bool { return len(r.M("K").SentOf(proto.TypeCatalogStop)) == 1 })
	for _, m := range r.M("K").Sent() {
		switch m.T {
		case proto.TypeCatalogWatch, proto.TypeCatalogStop:
			if m.ID != idK {
				t.Fatalf("K got %s with the id %d, not its watch's %d", m.T, m.ID, idK)
			}
		default:
			t.Fatalf("K got %s", m.T)
		}
	}
}

// marker drives the agent of session N of machine name through deltas, so
// that a test knows when the loop has read everything pushed before.
type marker struct {
	t    *testing.T
	r    *fleettest.Rig
	name string
	n    int
	rev  uint64 // of the last delta pushed
	busy bool   // the agent works; it starts idle
}

// delta pushes a state.delta of the next revision.
func (m *marker) delta(changes ...proto.Change) {
	m.rev++
	m.r.M(m.name).Push(proto.Encode(proto.Delta{Rev: m.rev, Changes: changes}))
}

// sync changes the agent's status and waits until the status row says so.
func (m *marker) sync() {
	m.t.Helper()
	m.busy = !m.busy
	status := proto.StatusIdle
	if m.busy {
		status = proto.StatusWorking
	}
	m.rev++
	m.r.M(m.name).Push(fleettest.AgentDelta(m.rev, m.n, status))
	m.r.Settle(func(s fleet.SummaryData) bool {
		return fleettest.MachineOf(m.t, s, m.name).Agents[agent.Status(status)] == 1
	})
}

// eventsOf is the events of machine and kind in evs.
func eventsOf(evs []hub.Event, machine string, kind hub.EventKind) []hub.Event {
	var out []hub.Event
	for _, ev := range evs {
		if ev.Machine == machine && ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// hasRow reports whether an Items of evs holds a row of path with label.
func hasRow(evs []hub.Event, path, label string) bool {
	for _, ev := range evs {
		for _, it := range ev.Items {
			if it.Path == path && (label == "" || it.Label == label) {
				return true
			}
		}
	}
	return false
}

func TestARowReportedAgainAndAgainWaitsForAnUnreadHubOnce(t *testing.T) {
	r := start(t, "M")
	r.M("M").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/m", Label: "m", Agent: proto.StatusIdle}))
	ch := r.Hub() // opened, not read for now
	id := watchID(t, r, "M", 1)
	mk := &marker{t: t, r: r, name: "M", n: 2, rev: 1}
	const n = 10000
	for i := range n {
		r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/p/a", "a"+itoa(i))}}))
		if i%2000 == 1999 {
			mk.sync()
		}
	}
	feed := fleettest.Collect(t, ch)
	newest := "a" + itoa(n-1)
	r.Eventually("the newest row", func() bool { return hasRow(eventsOf(feed.All(), "M", hub.Items), "/home/florian/dev/p/a", newest) })
	time.Sleep(fleettest.Quiet)
	// what the channel of the hub holds, one on its way, and the rest as one
	if got := len(eventsOf(feed.All(), "M", hub.Items)); got > 256+2 {
		t.Fatalf("%d events of rows for one path reached the hub, want at most %d", got, 256+2)
	}
}

func TestAMachineThatFloodsAnUnreadHubIsSentInFullWhenTheHubReads(t *testing.T) {
	r := start(t, "M", "K")
	w := r.M("M").Welcome()
	w.Version, w.Installed = "0.1.0+6ee3170", "0.1.0+04c5fef"
	r.M("M").SetWelcome(w)
	r.M("M").Online(fleettest.Snapshot(fleettest.Sess{N: 2, Dir: "/home/florian/dev/m", Label: "m", Agent: proto.StatusIdle}))
	r.M("K").Online(fleettest.Snapshot(fleettest.Sess{N: 3, Dir: "/home/florian/dev/k", Label: "k"}))
	ch := r.Hub() // opened, not read for now
	id := watchID(t, r, "M", 1)
	watchID(t, r, "K", 1)
	mk := &marker{t: t, r: r, name: "M", n: 2, rev: 1}
	const n = 20000
	for i := range n {
		r.M("M").Push(proto.Request(id, proto.CatalogError{Path: "/home/florian/x/" + itoa(i), Text: "permission denied"}))
		if i%2000 == 1999 {
			mk.sync()
		}
	}
	r.M("M").Push(proto.Request(id, proto.CatalogItem{Items: []proto.CatalogEntry{entry("/home/florian/dev/p/a", "a")}}))
	mk.delta(proto.Put(proto.Session{ID: "s4", Dir: "/home/florian/dev/m4", Label: "m4"}))
	mk.sync()
	r.M("K").Push(proto.Encode(proto.Delta{Rev: 2, Changes: []proto.Change{proto.Put(proto.Session{ID: "s5", Dir: "/home/florian/dev/k5", Label: "k5"})}}))

	feed := fleettest.Collect(t, ch)
	r.Eventually("M's rows and sessions after the flood", func() bool {
		evs := feed.All()
		ss := eventsOf(evs, "M", hub.Sessions)
		return hasRow(eventsOf(evs, "M", hub.Items), "/home/florian/dev/p/a", "") && len(ss) > 0 && len(ss[len(ss)-1].Items) == 2
	})
	r.Eventually("K's new session", func() bool {
		ss := eventsOf(feed.All(), "K", hub.Sessions)
		return len(ss) > 0 && len(ss[len(ss)-1].Items) == 2
	})
	time.Sleep(fleettest.Quiet)
	evs := feed.All()
	if got := len(eventsOf(evs, "M", hub.Note)); got >= n {
		t.Fatalf("%d notes of M reached the hub: everything waited for it", got)
	}
	if got := len(eventsOf(evs, "M", hub.Failed)); got != 0 {
		t.Fatalf("M failed %d times", got)
	}
	// the last word on M's state is that it is online
	var last hub.Event
	for _, ev := range evs {
		if ev.Machine == "M" && (ev.Kind == hub.Online || ev.Kind == hub.Connecting || ev.Kind == hub.Failed) {
			last = ev
		}
	}
	if last.Kind != hub.Online {
		t.Fatalf("the last state event of M is %s, want Online", fleettest.EventName(last.Kind))
	}
	// and it keeps the builds of M's server
	if last.Version != "0.1.0+6ee3170" || last.Installed != "0.1.0+04c5fef" {
		t.Fatalf("the last Online of M lost the builds: version %q, installed %q", last.Version, last.Installed)
	}
}
