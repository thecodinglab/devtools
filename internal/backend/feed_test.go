package backend

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"devtools/internal/client/guard"
	"devtools/internal/config"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"
)

// listing is a multiplexer for the feed: it answers Sessions and Agents
// with items that claim another machine, so the test sees that the feed
// strips it, and counts the calls.
type listing struct {
	name             mux.Backend
	sessions, agents atomic.Int32
}

func (l *listing) Name() mux.Backend { return l.name }
func (l *listing) Caps() mux.Caps    { return CapsOf(l.name) }
func (l *listing) Show(dir, name string) (mux.Shown, error) {
	return 0, nil
}
func (l *listing) Leave(dir, fallbackDir, fallbackName string) (func() error, error) {
	return nil, nil
}
func (l *listing) Hub(mode hub.Mode, query string) error { return nil }
func (l *listing) Sessions(ctx context.Context) ([]hub.Item, error) {
	l.sessions.Add(1)
	return []hub.Item{{Kind: hub.KindSession, Session: "proj-main", Machine: "elsewhere"}}, nil
}
func (l *listing) Enter(p *guard.Pass, e mux.Entry) error { return nil }
func (l *listing) Agents(ctx context.Context) ([]hub.Item, error) {
	l.agents.Add(1)
	if err := mux.Need(l, mux.CapAgents); err != nil {
		return nil, err
	}
	return []hub.Item{{Kind: hub.KindAgent, Pane: "w1:p1", Machine: "elsewhere"}}, nil
}
func (l *listing) Jump(agent hub.Item) error { return nil }
func (l *listing) Snapshot(ctx context.Context) (proto.Snapshot, error) {
	return proto.Snapshot{}, nil
}
func (l *listing) FocusPane(direction string) (bool, error) { return false, nil }

// catalogConfig is a configuration with an empty root and one bookmark, so
// the catalog of the test is small and lies inside the test's directory.
func catalogConfig(t *testing.T) func() (config.Config, error) {
	t.Helper()
	root := t.TempDir()
	mark := filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(mark, 0o755); err != nil {
		t.Fatal(err)
	}
	return func() (config.Config, error) {
		return config.Config{Roots: []string{root}, Bookmarks: []config.Bookmark{{Name: "notes", Path: mark}}}, nil
	}
}

// collect reads the feed until it has seen each of the kinds, then ends ctx
// and reads to the close. It fails the test when the feed does not close.
func collect(t *testing.T, cancel context.CancelFunc, feed <-chan hub.Event, until ...hub.EventKind) []hub.Event {
	t.Helper()
	missing := map[hub.EventKind]bool{}
	for _, k := range until {
		missing[k] = true
	}
	var got []hub.Event
	deadline := time.After(10 * time.Second)
	for len(missing) > 0 {
		select {
		case e, ok := <-feed:
			if !ok {
				t.Fatalf("the feed closed before ctx ended; missing %v", missing)
			}
			got = append(got, e)
			delete(missing, e.Kind)
		case <-deadline:
			t.Fatalf("the feed did not send %v in time", missing)
		}
	}
	cancel()
	for {
		select {
		case e, ok := <-feed:
			if !ok {
				return got
			}
			got = append(got, e)
		case <-time.After(5 * time.Second):
			t.Fatal("the feed did not close when ctx ended")
		}
	}
}

func TestLocalFeedEmitsOnlyThisMachine(t *testing.T) {
	for _, b := range []mux.Backend{mux.Tmux, mux.Herdr} {
		t.Run(string(b), func(t *testing.T) {
			l := &listing{name: b}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			until := []hub.EventKind{hub.Online, hub.Items, hub.Complete, hub.Sessions}
			if b == mux.Herdr {
				until = append(until, hub.Agents)
			}
			events := collect(t, cancel, LocalFeed(ctx, l, catalogConfig(t)), until...)
			if events[0].Kind != hub.Online {
				t.Errorf("the first event is %v, want Online", events[0].Kind)
			}
			bookmark := false
			for _, e := range events {
				if e.Machine != "" {
					t.Errorf("an event of kind %v names machine %q", e.Kind, e.Machine)
				}
				for _, it := range e.Items {
					if it.Machine != "" {
						t.Errorf("an item of an event of kind %v names machine %q", e.Kind, it.Machine)
					}
					if it.Kind == hub.KindBookmark && it.Label == "notes" {
						bookmark = true
					}
				}
			}
			if !bookmark {
				t.Error("the catalog's bookmark is not in the feed")
			}
		})
	}
}

func TestLocalFeedListsAgentsOnlyWithTheCapability(t *testing.T) {
	t.Run("tmux, without agent-status: no Agents event and no call", func(t *testing.T) {
		l := &listing{name: mux.Tmux}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		events := collect(t, cancel, LocalFeed(ctx, l, catalogConfig(t)), hub.Online, hub.Complete, hub.Sessions)
		for _, e := range events {
			if e.Kind == hub.Agents {
				t.Errorf("an Agents event: %+v", e)
			}
		}
		if n := l.agents.Load(); n != 0 {
			t.Errorf("Agents was called %d times", n)
		}
	})
	t.Run("herdr, with agent-status: one Agents event", func(t *testing.T) {
		l := &listing{name: mux.Herdr}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		events := collect(t, cancel, LocalFeed(ctx, l, catalogConfig(t)), hub.Online, hub.Complete, hub.Sessions, hub.Agents)
		n := 0
		for _, e := range events {
			if e.Kind == hub.Agents {
				n++
				if len(e.Items) != 1 || e.Items[0].Kind != hub.KindAgent {
					t.Errorf("Agents event items = %+v", e.Items)
				}
			}
		}
		if n != 1 || l.agents.Load() != 1 {
			t.Errorf("%d Agents events and %d calls, want one each", n, l.agents.Load())
		}
	})
}

func TestLocalFeedAsksNothingBeforeTheHubReads(t *testing.T) {
	l := &listing{name: mux.Herdr}
	loads := atomic.Int32{}
	load := func() (config.Config, error) {
		loads.Add(1)
		return config.Config{Roots: []string{t.TempDir()}}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	feed := LocalFeed(ctx, l, load)
	time.Sleep(100 * time.Millisecond)
	if l.sessions.Load() != 0 || l.agents.Load() != 0 || loads.Load() != 0 {
		t.Fatalf("before the first read: %d Sessions, %d Agents, %d loads; want none",
			l.sessions.Load(), l.agents.Load(), loads.Load())
	}
	cancel()
	select {
	case _, ok := <-feed:
		if ok {
			// the Online event may still be taken once; the close follows
			if _, ok := <-feed; ok {
				t.Fatal("the feed sent more after ctx ended")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the feed did not close when ctx ended")
	}
	if l.sessions.Load() != 0 || l.agents.Load() != 0 {
		t.Errorf("after ctx ended: %d Sessions, %d Agents; want none", l.sessions.Load(), l.agents.Load())
	}
}

func TestLocalFeedTurnsTheCatalogIntoEvents(t *testing.T) {
	item, ok := catalogEvent(proto.Encode(proto.CatalogItem{Items: []proto.CatalogEntry{
		{Kind: proto.CatalogWorktree, Project: "proj", Worktree: "feat", Label: "proj/feat", Path: "/r/proj/feat", Root: "/r", Session: "proj-feat"},
		{Kind: "something else", Path: "/x"},
	}}))
	if !ok || item.Kind != hub.Items || len(item.Items) != 1 {
		t.Fatalf("catalog.item = %+v, %v", item, ok)
	}
	if it := item.Items[0]; it.Kind != hub.KindWorktree || it.Path != "/r/proj/feat" || it.Session != "proj-feat" || it.Root != "/r" {
		t.Errorf("item = %+v", it)
	}
	git, ok := catalogEvent(proto.Encode(proto.CatalogGit{Path: "/r/proj/feat", Git: proto.Git{Changes: 2, Ahead: 1, Branch: "feat"}}))
	if !ok || git.Kind != hub.Git || git.Path != "/r/proj/feat" || git.Git.Changes != 2 || git.Git.Ahead != 1 || git.Git.Branch != "feat" {
		t.Errorf("catalog.git = %+v, %v", git, ok)
	}
	if done, ok := catalogEvent(proto.Encode(proto.CatalogDone{})); !ok || done.Kind != hub.Complete {
		t.Errorf("catalog.done = %+v, %v", done, ok)
	}
	if _, ok := catalogEvent(proto.Encode(proto.CatalogError{Path: "/r", Text: "denied"})); ok {
		t.Error("catalog.error became an event; the contract of the hub has no kind for it")
	}
}
