package backend

// The feed of the standalone hub (3.10.4 and 5.3.11 of the plan, P2-14):
// the hub of the tmux and herdr backends, which lists this machine only.

import (
	"context"
	"os"
	"sync"

	"devtools/internal/catalog"
	"devtools/internal/config"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// runStandalone is the standalone hub. The tests replace it with a function
// that returns a chosen pick: a hub on a terminal is the test of P2-07.
var runStandalone = hub.RunStandalone

// LocalFeed feeds the standalone hub. It emits events with Machine "" only:
// the catalog of this machine (catalog.Watch with load), Sessions of m and,
// when m has CapAgents, Agents of m. It closes the channel when ctx ends.
//
// The first event is Online for this machine, and nothing else is started
// before the hub has taken it: a hub that ends without reading its events
// (a pick cancelled at once, or a test) makes the feed run nothing, so no
// program of the backend is asked anything.
func LocalFeed(ctx context.Context, m mux.Multiplexer, load func() (config.Config, error)) <-chan hub.Event {
	out := make(chan hub.Event)
	go func() {
		defer close(out)
		send := func(e hub.Event) bool {
			select {
			case out <- local(e):
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(hub.Event{Kind: hub.Online}) || ctx.Err() != nil {
			return
		}

		events := make(chan hub.Event)
		push := func(e hub.Event) {
			select {
			case events <- e:
			case <-ctx.Done():
			}
		}
		var work sync.WaitGroup
		work.Go(func() {
			catalog.Watch(ctx, load, func(msg proto.Msg) {
				if e, ok := catalogEvent(msg); ok {
					push(e)
				}
			})
		})
		work.Go(func() {
			if items, err := m.Sessions(ctx); err == nil {
				push(hub.Event{Kind: hub.Sessions, Items: items})
			}
		})
		if m.Caps().Has(mux.CapAgents) {
			work.Go(func() {
				if items, err := m.Agents(ctx); err == nil {
					push(hub.Event{Kind: hub.Agents, Items: items})
				}
			})
		}
		go func() {
			work.Wait()
			close(events)
		}()

		for {
			select {
			case e, ok := <-events:
				if !ok {
					// everything is reported; the channel stays open, as
					// the hub expects, until ctx ends
					<-ctx.Done()
					return
				}
				if !send(e) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// local makes e an event of this machine: neither the event nor any of its
// items names another machine.
func local(e hub.Event) hub.Event {
	e.Machine = ""
	if len(e.Items) > 0 {
		items := make([]hub.Item, len(e.Items))
		for i, it := range e.Items {
			it.Machine = ""
			items[i] = it
		}
		e.Items = items
	}
	return e
}

// catalogEvent turns one message of catalog.Watch into an event of the
// hub: catalog.item is Items, catalog.git is Git, catalog.done is
// Complete. catalog.error has no event of its own in the contract of the
// hub (hub.EventKind) and is dropped: the scan goes on without the root.
func catalogEvent(msg proto.Msg) (hub.Event, bool) {
	switch msg.T {
	case proto.TypeCatalogItem:
		var ci proto.CatalogItem
		if msg.Decode(&ci) != nil {
			return hub.Event{}, false
		}
		items := make([]hub.Item, 0, len(ci.Items))
		for _, e := range ci.Items {
			if it, ok := catalogItem(e); ok {
				items = append(items, it)
			}
		}
		return hub.Event{Kind: hub.Items, Items: items}, true
	case proto.TypeCatalogGit:
		var g proto.CatalogGit
		if msg.Decode(&g) != nil {
			return hub.Event{}, false
		}
		return hub.Event{Kind: hub.Git, Path: g.Path, Git: ui.Git{
			Changes:     g.Git.Changes,
			Ahead:       g.Git.Ahead,
			Behind:      g.Git.Behind,
			HasUpstream: g.Git.HasUpstream,
			Detached:    g.Git.Detached,
			Branch:      g.Git.Branch,
		}}, true
	case proto.TypeCatalogDone:
		return hub.Event{Kind: hub.Complete}, true
	}
	return hub.Event{}, false
}

// catalogItem is one row of catalog.item as an item of the hub.
func catalogItem(e proto.CatalogEntry) (hub.Item, bool) {
	var kind hub.Kind
	switch e.Kind {
	case proto.CatalogWorktree:
		kind = hub.KindWorktree
	case proto.CatalogRepo:
		kind = hub.KindRepo
	case proto.CatalogBookmark:
		kind = hub.KindBookmark
	default:
		return hub.Item{}, false
	}
	return hub.Item{
		Kind:     kind,
		Project:  e.Project,
		Worktree: e.Worktree,
		Label:    e.Label,
		Path:     e.Path,
		Root:     e.Root,
		Session:  e.Session,
	}, true
}

// uiSession is the state of an open session or agent pane as the hub
// shows it.
func uiSession(open, focused bool, agent, title string) ui.Session {
	return ui.Session{Open: open, Focused: focused, Agent: agent, Title: title}
}

// standaloneHub runs the standalone hub of m on the terminal of d, fed by
// LocalFeed, and returns the pick, or nil when the hub was closed without
// one. The feed ends with the hub.
func standaloneHub(m mux.Multiplexer, d Deps, mode hub.Mode, query string, f hub.Features) (*hub.Item, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, out := d.In, d.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	here, _ := os.Getwd()
	return runStandalone(hub.ModelOptions{
		Mode:     mode,
		Query:    query,
		Here:     session.Target{Dir: here},
		Events:   LocalFeed(ctx, m, configLoader(d)),
		Actions:  d.Actions,
		Features: f,
	}, in, out)
}

// configLoader is the load of catalog.Watch: the configuration the command
// loaded, or the defaults without one.
func configLoader(d Deps) func() (config.Config, error) {
	return func() (config.Config, error) {
		if d.Config == nil {
			return config.Config{}, nil
		}
		return *d.Config, nil
	}
}
