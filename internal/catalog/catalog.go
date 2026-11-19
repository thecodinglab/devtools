// Package catalog is what a machine offers to open, as a stream (4.3 of the
// plan): the projects and worktrees under the configured roots, the
// bookmarks, and the git state of each, filled in afterwards.
package catalog

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"devtools/internal/config"
	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// MaxGit is the number of git statuses in flight at once (4.3).
const MaxGit = 8

// DefaultRoot is the root scanned when the config names none (4.6).
const DefaultRoot = "~/dev"

// The seams of the tests: the project scan of one root and the git status
// of one path.
var (
	scan   = discovery.Scan
	status = devgit.Status
)

// held is the roots and bookmarks of the last load that succeeded; a watch
// whose load fails uses them (4.6: a file that no longer parses keeps the
// values held).
var held struct {
	sync.Mutex
	ok  bool
	cfg config.Config
}

// Watch emits one catalog.item per root as it finishes, bookmarks,
// catalog.git with at most 8 statuses in flight, catalog.error per unreadable
// root or path, and always a final catalog.done. Roots and bookmarks come
// from load only, which is called at the start of every watch; when load
// fails the values of the previous call are used and catalog.error is sent.
//
// The messages carry no id: the caller stamps the id of its watch. emit is
// never called from two goroutines at once. When ctx ends Watch returns at
// once and emit is not called again, not even with catalog.done; a scan or a
// git status already running finishes in the background and is dropped.
func Watch(ctx context.Context, load func() (config.Config, error), emit func(proto.Msg)) {
	w := &watch{ctx: ctx, emit: emit, sem: make(chan struct{}, MaxGit), seen: map[string]bool{}}
	cfg := w.config(load)
	roots := w.roots(cfg.Roots)

	scanned := make(chan struct{}, len(roots))
	for _, root := range roots {
		go func() {
			defer func() { scanned <- struct{}{} }()
			w.scanRoot(root)
		}()
	}
	for range roots {
		select {
		case <-scanned:
		case <-ctx.Done():
			w.stop()
			return
		}
	}
	w.bookmarks(cfg.Bookmarks)

	gits := make(chan struct{})
	go func() {
		w.gits.Wait()
		close(gits)
	}()
	select {
	case <-gits:
	case <-ctx.Done():
		w.stop()
		return
	}
	w.send(proto.CatalogDone{})
	w.stop()
}

type watch struct {
	ctx  context.Context
	emit func(proto.Msg)
	sem  chan struct{}  // one token per git status in flight
	gits sync.WaitGroup // the git statuses started

	mu      sync.Mutex // serialises emit; guards seen and stopped
	seen    map[string]bool
	stopped bool
}

// send emits v unless the watch is cancelled or over. It reports whether v
// was emitted.
func (w *watch) send(v proto.Typed) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sendLocked(v)
}

func (w *watch) sendLocked(v proto.Typed) bool {
	if w.stopped || w.ctx.Err() != nil {
		return false
	}
	w.emit(proto.Encode(v))
	return true
}

// stop makes every later send a no-op. It waits for a send in progress.
func (w *watch) stop() {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
}

// config calls load and keeps its values for the next watch; when load
// fails it reports the error and returns the values held, or the defaults
// when nothing was ever loaded.
func (w *watch) config(load func() (config.Config, error)) config.Config {
	cfg, err := load()
	held.Lock()
	defer held.Unlock()
	if err != nil {
		w.send(proto.CatalogError{Text: err.Error()})
		if held.ok {
			return held.cfg
		}
		return config.Config{}
	}
	held.ok = true
	held.cfg = config.Config{
		Roots:     append([]string(nil), cfg.Roots...),
		Bookmarks: append([]config.Bookmark(nil), cfg.Bookmarks...),
	}
	return held.cfg
}

// roots returns the configured roots as absolute paths without duplicates,
// in config order; DefaultRoot when none is configured. A root that cannot
// be made absolute is reported and left out.
func (w *watch) roots(configured []string) []string {
	if len(configured) == 0 {
		configured = []string{DefaultRoot}
	}
	var roots []string
	dup := map[string]bool{}
	for _, root := range configured {
		if root == "" {
			continue
		}
		abs, err := expand(root)
		if err != nil {
			w.send(proto.CatalogError{Path: root, Text: err.Error()})
			continue
		}
		if dup[abs] {
			continue
		}
		dup[abs] = true
		roots = append(roots, abs)
	}
	return roots
}

// scanRoot scans one root and emits its rows, then starts the git status of
// each repository and worktree among them.
func (w *watch) scanRoot(root string) {
	targets, err := scan(root)
	if w.ctx.Err() != nil {
		return
	}
	if err != nil {
		w.fail(root, err)
		return
	}
	items := make([]proto.CatalogEntry, 0, len(targets))
	for _, t := range targets {
		e := proto.CatalogEntry{
			Kind:     proto.CatalogRepo,
			Project:  t.Project,
			Worktree: t.Worktree,
			Label:    t.Label,
			Path:     t.Path,
			Session:  session.Name(t.Project, t.Worktree),
		}
		if t.Kind == "worktree" {
			e.Kind = proto.CatalogWorktree
			e.Root = root
		}
		items = append(items, e)
	}
	w.mu.Lock()
	// A path under two roots is reported once, by the root that finished
	// first.
	kept := items[:0]
	for _, e := range items {
		if !w.seen[e.Path] {
			w.seen[e.Path] = true
			kept = append(kept, e)
		}
	}
	sent := w.sendLocked(proto.CatalogItem{Items: kept})
	if sent {
		// Added before this root counts as scanned, so Watch waits for the
		// statuses only after every Add.
		w.gits.Add(len(kept))
	}
	w.mu.Unlock()
	if !sent {
		return
	}
	for _, e := range kept {
		go w.git(e.Path)
	}
}

// git runs the status of one path once a token is free and emits it.
func (w *watch) git(path string) {
	defer w.gits.Done()
	select {
	case w.sem <- struct{}{}:
	case <-w.ctx.Done():
		return
	}
	defer func() { <-w.sem }()
	if w.ctx.Err() != nil {
		return
	}
	st, err := status(path)
	if err != nil {
		w.fail(path, err)
		return
	}
	w.send(proto.CatalogGit{Path: path, Git: proto.Git{
		Changes:     st.Changes,
		Ahead:       st.Ahead,
		Behind:      st.Behind,
		HasUpstream: st.HasUpstream,
		Detached:    st.Detached,
		Branch:      st.Branch,
	}})
}

// bookmarks emits the one catalog.item of the bookmarks, sorted by name. A
// bookmark whose directory is missing or not a directory is left out, as
// the hub does today; one that cannot be read is reported.
func (w *watch) bookmarks(bookmarks []config.Bookmark) {
	items := []proto.CatalogEntry{}
	for _, b := range bookmarks {
		path, err := expand(b.Path)
		if err != nil {
			w.send(proto.CatalogError{Path: b.Path, Text: err.Error()})
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				w.fail(path, err)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		items = append(items, proto.CatalogEntry{
			Kind:    proto.CatalogBookmark,
			Project: b.Name,
			Label:   b.Name,
			Path:    path,
			Session: session.Name(b.Name, ""),
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	w.send(proto.CatalogItem{Items: items})
}

// fail emits catalog.error for path. The text is the cause without the
// path, which the message carries in its own field.
func (w *watch) fail(path string, err error) {
	text := err.Error()
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Path == path {
		text = pe.Err.Error()
	}
	w.send(proto.CatalogError{Path: path, Text: text})
}

// expand makes path absolute; a leading ~ is the home directory.
func expand(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path[1:], "/"))
	}
	return filepath.Abs(path)
}
