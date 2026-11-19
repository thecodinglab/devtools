package catalog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devtools/internal/config"
	"devtools/internal/devgit"
	"devtools/internal/discovery"
	"devtools/internal/proto"
	"devtools/internal/testkit"
)

var testHome string

// TestMain keeps the tests hermetic: HOME is a temporary directory (so the
// default root ~/dev is inside it), and nothing of the developer's
// environment that names a config, a root or a git setting reaches them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-catalog-test")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") || strings.HasPrefix(name, "DEVTOOLS_") ||
			strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "HERDR_") ||
			strings.HasPrefix(name, "TMUX") {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	// No repository above the temporary directories is ever found.
	os.Setenv("GIT_CEILING_DIRECTORIES", strings.Join([]string{"/tmp", os.TempDir()}, string(os.PathListSeparator)))
	testHome = home
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// --- helpers ---

// recorder collects what Watch emits, in order.
type recorder struct {
	mu   sync.Mutex
	msgs []proto.Msg
	// on, when set, is called with every message after it is recorded.
	on func(proto.Msg)
	// busy counts emit calls that overlap.
	busy, overlap atomic.Int32
}

func (r *recorder) emit(m proto.Msg) {
	if r.busy.Add(1) > 1 {
		r.overlap.Add(1)
	}
	defer r.busy.Add(-1)
	r.mu.Lock()
	r.msgs = append(r.msgs, m)
	on := r.on
	r.mu.Unlock()
	if on != nil {
		on(m)
	}
}

func (r *recorder) all() []proto.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.Msg(nil), r.msgs...)
}

func fixed(cfg config.Config) func() (config.Config, error) {
	return func() (config.Config, error) { return cfg, nil }
}

// run runs one watch to its end and returns what it emitted.
func run(t *testing.T, load func() (config.Config, error)) []proto.Msg {
	t.Helper()
	r := &recorder{}
	done := make(chan struct{})
	go func() {
		Watch(context.Background(), load, r.emit)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Watch did not return")
	}
	if n := r.overlap.Load(); n != 0 {
		t.Fatalf("emit was called %d times while another call ran", n)
	}
	msgs := r.all()
	checkDoneLast(t, msgs)
	return msgs
}

func checkDoneLast(t *testing.T, msgs []proto.Msg) {
	t.Helper()
	if len(msgs) == 0 || msgs[len(msgs)-1].T != proto.TypeCatalogDone {
		t.Fatalf("the last message is not catalog.done: %v", types(msgs))
	}
	for _, m := range msgs[:len(msgs)-1] {
		if m.T == proto.TypeCatalogDone {
			t.Fatalf("catalog.done before the end: %v", types(msgs))
		}
	}
	for _, m := range msgs {
		if m.ID != 0 {
			t.Fatalf("%s carries id %d; the caller stamps the id", m.T, m.ID)
		}
		var v proto.Typed
		switch m.T {
		case proto.TypeCatalogItem:
			v = &proto.CatalogItem{}
		case proto.TypeCatalogGit:
			v = &proto.CatalogGit{}
		case proto.TypeCatalogError:
			v = &proto.CatalogError{}
		case proto.TypeCatalogDone:
			v = &proto.CatalogDone{}
		default:
			t.Fatalf("unexpected message %s", m.T)
		}
		if err := m.Decode(v); err != nil {
			t.Fatalf("%s does not decode: %v (%s)", m.T, err, m.Body)
		}
	}
}

func types(msgs []proto.Msg) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.T
	}
	return out
}

func items(t *testing.T, m proto.Msg) []proto.CatalogEntry {
	t.Helper()
	var v proto.CatalogItem
	if err := m.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v.Items
}

func gitOf(t *testing.T, m proto.Msg) proto.CatalogGit {
	t.Helper()
	var v proto.CatalogGit
	if err := m.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func errorOf(t *testing.T, m proto.Msg) proto.CatalogError {
	t.Helper()
	var v proto.CatalogError
	if err := m.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// paths returns the paths of every catalog row in msgs, by kind.
func paths(t *testing.T, msgs []proto.Msg) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range msgs {
		if m.T == proto.TypeCatalogItem {
			for _, e := range items(t, m) {
				out[e.Path] = e.Kind
			}
		}
	}
	return out
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// makeRepo makes a plain repository with one commit at dir.
func makeRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "README")
	gitCmd(t, dir, "commit", "-q", "-m", "init")
}

// makeProject makes a managed project root/name with a bare repository and
// the worktree main and, for each of branches, a worktree of that name.
func makeProject(t *testing.T, root, name string, branches ...string) {
	t.Helper()
	src := filepath.Join(testkit.ShortTempDir(t), "src")
	makeRepo(t, src)
	project := filepath.Join(root, name)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, project, "clone", "-q", "--bare", src, ".bare")
	bare := filepath.Join(project, ".bare")
	gitCmd(t, bare, "worktree", "add", "-q", filepath.Join(project, "main"), "main")
	for _, b := range branches {
		gitCmd(t, bare, "worktree", "add", "-q", "-b", b, filepath.Join(project, b), "main")
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// seam replaces the scan or the status of the package for one test.
func seamScan(t *testing.T, f func(string) ([]discovery.Target, error)) {
	t.Helper()
	old := scan
	scan = f
	t.Cleanup(func() { scan = old })
}

func seamStatus(t *testing.T, f func(string) (devgit.WorktreeStatus, error)) {
	t.Helper()
	old := status
	status = f
	t.Cleanup(func() { status = old })
}

func wait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// --- acceptance tests ---

func TestItemsOfARootArriveBeforeAnyGitStatus(t *testing.T) {
	one := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "one"))
	two := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "two"))
	makeProject(t, one, "widgets", "feature")
	makeRepo(t, filepath.Join(one, "plain"))
	makeRepo(t, filepath.Join(two, "other"))
	gitCmd(t, filepath.Join(two, "other"), "checkout", "-q", "--detach")
	if err := os.WriteFile(filepath.Join(one, "plain", "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	msgs := run(t, fixed(config.Config{Roots: []string{one, two}}))

	itemAt := map[string]int{} // path -> index of the catalog.item naming it
	rootItems := map[string][]proto.CatalogEntry{}
	for i, m := range msgs {
		if m.T != proto.TypeCatalogItem {
			continue
		}
		es := items(t, m)
		for _, e := range es {
			itemAt[e.Path] = i
			if e.Kind != proto.CatalogBookmark {
				root := filepath.Dir(e.Path)
				if e.Kind == proto.CatalogWorktree {
					root = filepath.Dir(root)
				}
				rootItems[root] = append(rootItems[root], e)
			}
		}
	}
	gits := map[string]proto.Git{}
	for i, m := range msgs {
		if m.T == proto.TypeCatalogError {
			t.Fatalf("unexpected catalog.error %+v", errorOf(t, m))
		}
		if m.T != proto.TypeCatalogGit {
			continue
		}
		g := gitOf(t, m)
		at, ok := itemAt[g.Path]
		if !ok || at > i {
			t.Fatalf("catalog.git for %s at %d before its catalog.item (%v, %d)", g.Path, i, ok, at)
		}
		// Every row of the same root came in that one earlier message.
		for _, e := range rootItems[filepath.Dir(g.Path)] {
			if itemAt[e.Path] > i {
				t.Fatalf("row %s arrived after the git status of %s", e.Path, g.Path)
			}
		}
		gits[g.Path] = g.Git
	}

	wantKinds := map[string]string{
		filepath.Join(one, "widgets", "main"):    proto.CatalogWorktree,
		filepath.Join(one, "widgets", "feature"): proto.CatalogWorktree,
		filepath.Join(one, "plain"):              proto.CatalogRepo,
		filepath.Join(two, "other"):              proto.CatalogRepo,
	}
	if got := paths(t, msgs); len(got) != len(wantKinds) {
		t.Fatalf("rows %v, want %v", got, wantKinds)
	}
	for p, kind := range wantKinds {
		if paths(t, msgs)[p] != kind {
			t.Fatalf("row %s has kind %q, want %q", p, paths(t, msgs)[p], kind)
		}
		if _, ok := gits[p]; !ok {
			t.Fatalf("no catalog.git for %s", p)
		}
	}
	if g := gits[filepath.Join(one, "widgets", "feature")]; g.Branch != "feature" || g.Detached {
		t.Fatalf("feature worktree git %+v", g)
	}
	if g := gits[filepath.Join(one, "plain")]; g.Changes != 1 || g.Branch != "main" {
		t.Fatalf("plain repo git %+v, want one change on main", g)
	}
	if g := gits[filepath.Join(two, "other")]; !g.Detached {
		t.Fatalf("detached repo git %+v", g)
	}
	for _, m := range msgs {
		if m.T != proto.TypeCatalogItem {
			continue
		}
		for _, e := range items(t, m) {
			switch e.Path {
			case filepath.Join(one, "widgets", "feature"):
				want := proto.CatalogEntry{Kind: proto.CatalogWorktree, Project: "widgets", Worktree: "feature",
					Label: "widgets/feature", Path: e.Path, Root: one, Session: "widgets-feature"}
				if e != want {
					t.Fatalf("worktree row %+v, want %+v", e, want)
				}
			case filepath.Join(one, "plain"):
				want := proto.CatalogEntry{Kind: proto.CatalogRepo, Project: "plain", Worktree: "main",
					Label: "plain", Path: e.Path, Session: "plain-main"}
				if e != want {
					t.Fatalf("repo row %+v, want %+v", e, want)
				}
			}
		}
	}
}

func TestASlowRootDoesNotDelayAnother(t *testing.T) {
	slow := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "slow"))
	fast := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "fast"))
	makeRepo(t, filepath.Join(slow, "s"))
	makeRepo(t, filepath.Join(fast, "f"))

	release := make(chan struct{})
	seamScan(t, func(root string) ([]discovery.Target, error) {
		if root == slow {
			<-release // a filesystem that does not answer
		}
		return discovery.Scan(root)
	})

	sawFast := make(chan struct{})
	var once sync.Once
	r := &recorder{}
	r.on = func(m proto.Msg) {
		if m.T == proto.TypeCatalogItem {
			for _, e := range items(t, m) {
				if e.Path == filepath.Join(fast, "f") {
					once.Do(func() { close(sawFast) })
				}
			}
		}
	}
	done := make(chan struct{})
	go func() {
		// The slow root comes first in the config.
		Watch(context.Background(), fixed(config.Config{Roots: []string{slow, fast}}), r.emit)
		close(done)
	}()
	wait(t, sawFast, "the rows of the fast root while the slow one hangs")
	select {
	case <-done:
		t.Fatal("the watch ended while a root was still scanning")
	default:
	}
	for _, m := range r.all() {
		if m.T == proto.TypeCatalogDone {
			t.Fatal("catalog.done while a root was still scanning")
		}
	}
	close(release)
	wait(t, done, "the watch to end")
	msgs := r.all()
	checkDoneLast(t, msgs)
	if _, ok := paths(t, msgs)[filepath.Join(slow, "s")]; !ok {
		t.Fatalf("the slow root's rows are missing: %v", paths(t, msgs))
	}
}

func TestAnUnreadableRootIsReportedAndTheStreamEnds(t *testing.T) {
	base := testkit.ShortTempDir(t)
	locked := mkdir(t, filepath.Join(base, "locked"))
	makeRepo(t, filepath.Join(locked, "hidden"))
	notDir := filepath.Join(base, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := mkdir(t, filepath.Join(base, "good"))
	makeRepo(t, filepath.Join(good, "seen"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	msgs := run(t, fixed(config.Config{Roots: []string{locked, notDir, good}}))

	errs := map[string]string{}
	for _, m := range msgs {
		if m.T == proto.TypeCatalogError {
			e := errorOf(t, m)
			errs[e.Path] = e.Text
		}
	}
	if errs[locked] != "permission denied" {
		t.Fatalf("catalog.error of the unreadable root: %q (all %v)", errs[locked], errs)
	}
	if errs[notDir] == "" {
		t.Fatalf("no catalog.error for the root that is a file: %v", errs)
	}
	got := paths(t, msgs)
	if _, ok := got[filepath.Join(good, "seen")]; !ok {
		t.Fatalf("the readable root's rows are missing: %v", got)
	}
	if _, ok := got[filepath.Join(locked, "hidden")]; ok {
		t.Fatal("a row of the unreadable root was listed")
	}
}

func TestBookmarksAreListed(t *testing.T) {
	base := testkit.ShortTempDir(t)
	root := mkdir(t, filepath.Join(base, "root"))
	makeRepo(t, filepath.Join(root, "proj"))
	notes := mkdir(t, filepath.Join(base, "Notes"))
	inHome := mkdir(t, filepath.Join(testHome, "papers"))
	t.Cleanup(func() { os.RemoveAll(inHome) })
	file := filepath.Join(base, "a-file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	msgs := run(t, fixed(config.Config{
		Roots: []string{root},
		Bookmarks: []config.Bookmark{
			{Name: "zeta", Path: notes},
			{Name: "papers", Path: "~/papers"},
			{Name: "gone", Path: filepath.Join(base, "missing")},
			{Name: "file", Path: file},
		},
	}))

	var bookmarkMsg = -1
	var lastRoot = -1
	for i, m := range msgs {
		if m.T != proto.TypeCatalogItem {
			continue
		}
		es := items(t, m)
		if len(es) > 0 && es[0].Kind == proto.CatalogBookmark {
			if bookmarkMsg >= 0 {
				t.Fatal("the bookmarks came in more than one message")
			}
			bookmarkMsg = i
			want := []proto.CatalogEntry{
				{Kind: proto.CatalogBookmark, Project: "papers", Label: "papers", Path: inHome, Session: "papers"},
				{Kind: proto.CatalogBookmark, Project: "zeta", Label: "zeta", Path: notes, Session: "zeta"},
			}
			if len(es) != len(want) {
				t.Fatalf("bookmark rows %+v, want %+v", es, want)
			}
			for j := range want {
				if es[j] != want[j] {
					t.Fatalf("bookmark row %d: %+v, want %+v", j, es[j], want[j])
				}
			}
		} else {
			lastRoot = i
		}
	}
	if bookmarkMsg < 0 || bookmarkMsg < lastRoot {
		t.Fatalf("the bookmarks are not one message after the roots (%d, %d): %v", bookmarkMsg, lastRoot, types(msgs))
	}
	for _, m := range msgs {
		if m.T == proto.TypeCatalogGit && gitOf(t, m).Path != filepath.Join(root, "proj") {
			t.Fatalf("a git status for a bookmark: %+v", gitOf(t, m))
		}
		if m.T == proto.TypeCatalogError {
			t.Fatalf("a missing bookmark is reported: %+v", errorOf(t, m))
		}
	}

	// Without bookmarks the message is still sent, with an empty list.
	msgs = run(t, fixed(config.Config{Roots: []string{root}}))
	n := 0
	for _, m := range msgs {
		if m.T == proto.TypeCatalogItem {
			n++
			if !strings.Contains(string(m.Body), `"items":[`) {
				t.Fatalf("items is not a list: %s", m.Body)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d catalog.item messages, want one per root and one for the bookmarks", n)
	}
}

func TestGitStatusesRunAtMostEightAtATime(t *testing.T) {
	one := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "one"))
	two := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "two"))
	const repos = 12
	for i := 0; i < repos; i++ {
		makeRepo(t, filepath.Join(one, "r"+string(rune('a'+i))))
		makeRepo(t, filepath.Join(two, "q"+string(rune('a'+i))))
	}
	var inFlight, most atomic.Int32
	seamStatus(t, func(path string) (devgit.WorktreeStatus, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		return devgit.Status(path)
	})

	msgs := run(t, fixed(config.Config{Roots: []string{one, two}}))

	n := 0
	for _, m := range msgs {
		if m.T == proto.TypeCatalogGit {
			n++
		}
	}
	if n != 2*repos {
		t.Fatalf("%d catalog.git, want %d", n, 2*repos)
	}
	if got := most.Load(); got != MaxGit {
		t.Fatalf("at most %d statuses ran at once, want exactly %d", got, MaxGit)
	}
}

func TestCancelStopsTheScanAndSendsNothingMore(t *testing.T) {
	t.Run("during a scan", func(t *testing.T) {
		slow := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "slow"))
		fast := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "fast"))
		makeRepo(t, filepath.Join(slow, "s"))
		makeRepo(t, filepath.Join(fast, "f"))
		release := make(chan struct{})
		returned := make(chan struct{})
		seamScan(t, func(root string) ([]discovery.Target, error) {
			if root == slow {
				<-release
				defer close(returned)
			}
			return discovery.Scan(root)
		})
		gitDone := make(chan struct{}, 8)
		seamStatus(t, func(path string) (devgit.WorktreeStatus, error) {
			defer func() { gitDone <- struct{}{} }()
			return devgit.Status(path)
		})
		ctx, cancel := context.WithCancel(context.Background())
		r := &recorder{}
		sawGit := make(chan struct{})
		var once sync.Once
		r.on = func(m proto.Msg) {
			if m.T == proto.TypeCatalogGit {
				once.Do(func() { close(sawGit) })
			}
		}
		done := make(chan struct{})
		go func() {
			Watch(ctx, fixed(config.Config{Roots: []string{slow, fast}}), r.emit)
			close(done)
		}()
		wait(t, sawGit, "the fast root's git status")
		cancel()
		wait(t, done, "Watch to return after the cancel")
		before := len(r.all())
		close(release)
		wait(t, returned, "the slow scan to return")
		time.Sleep(100 * time.Millisecond) // room for a status it would start
		after := r.all()
		if len(after) != before {
			t.Fatalf("sent %v after the cancel", types(after[before:]))
		}
		for _, m := range after {
			if m.T == proto.TypeCatalogDone {
				t.Fatal("catalog.done after a cancel")
			}
		}
		if len(gitDone) != 1 {
			t.Fatalf("%d statuses ran; the cancelled watch started another", len(gitDone))
		}
	})

	t.Run("during the git statuses", func(t *testing.T) {
		root := mkdir(t, filepath.Join(testkit.ShortTempDir(t), "root"))
		for i := 0; i < 2*MaxGit; i++ {
			makeRepo(t, filepath.Join(root, "r"+string(rune('a'+i))))
		}
		release := make(chan struct{})
		var started, finished atomic.Int32
		seamStatus(t, func(path string) (devgit.WorktreeStatus, error) {
			started.Add(1)
			defer finished.Add(1)
			<-release
			return devgit.Status(path)
		})
		ctx, cancel := context.WithCancel(context.Background())
		r := &recorder{}
		done := make(chan struct{})
		go func() {
			Watch(ctx, fixed(config.Config{Roots: []string{root}}), r.emit)
			close(done)
		}()
		testkit.Eventually(t, 30*time.Second, "eight statuses running", func() bool { return started.Load() == MaxGit })
		cancel()
		wait(t, done, "Watch to return after the cancel")
		before := len(r.all())
		close(release)
		testkit.Eventually(t, 30*time.Second, "the running statuses to end", func() bool { return finished.Load() == MaxGit })
		time.Sleep(100 * time.Millisecond)
		after := r.all()
		if len(after) != before {
			t.Fatalf("sent %v after the cancel", types(after[before:]))
		}
		if got := started.Load(); got != MaxGit {
			t.Fatalf("%d statuses started; none may start after the cancel", got)
		}
		for _, m := range after {
			if m.T == proto.TypeCatalogDone || m.T == proto.TypeCatalogGit {
				t.Fatalf("%s after a cancel", m.T)
			}
		}
	})

	t.Run("before the start", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &recorder{}
		Watch(ctx, fixed(config.Config{Roots: []string{testkit.ShortTempDir(t)}}), r.emit)
		if got := r.all(); len(got) != 0 {
			t.Fatalf("a cancelled watch sent %v", types(got))
		}
	})
}

func TestCatalogScansOnlyConfiguredRoots(t *testing.T) {
	base := testkit.ShortTempDir(t)
	configured := mkdir(t, filepath.Join(base, "configured"))
	other := mkdir(t, filepath.Join(base, "other"))
	makeRepo(t, filepath.Join(configured, "mine"))
	makeRepo(t, filepath.Join(other, "theirs"))
	dev := filepath.Join(testHome, "dev")
	makeRepo(t, filepath.Join(dev, "home"))
	t.Cleanup(func() { os.RemoveAll(dev) })
	// The variable of the git commands of a process (3.5.3) is not read.
	t.Setenv("DEVTOOLS_ROOT", other)

	got := paths(t, run(t, fixed(config.Config{Roots: []string{configured}})))
	if len(got) != 1 || got[filepath.Join(configured, "mine")] == "" {
		t.Fatalf("rows %v, want only the configured root's", got)
	}

	// No root configured: the default ~/dev of this user, nothing else.
	got = paths(t, run(t, fixed(config.Config{})))
	if len(got) != 1 || got[filepath.Join(dev, "home")] == "" {
		t.Fatalf("rows %v, want only ~/dev's", got)
	}

	// A root written with ~ and a duplicate of it are one root.
	got = paths(t, run(t, fixed(config.Config{Roots: []string{"~/dev", dev, "~/dev/"}})))
	if len(got) != 1 {
		t.Fatalf("rows %v, want one", got)
	}
}

func TestDoneIsAlwaysLast(t *testing.T) {
	base := testkit.ShortTempDir(t)
	root := mkdir(t, filepath.Join(base, "root"))
	makeRepo(t, filepath.Join(root, "a"))
	makeRepo(t, filepath.Join(root, "b"))
	bad := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(bad, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A .git with a HEAD but nothing else: discovery lists it, git fails.
	if err := os.WriteFile(filepath.Join(bad, ".git", "HEAD"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func() (config.Config, error){
		"nothing configured and no ~/dev": fixed(config.Config{}),
		"a missing root":                  fixed(config.Config{Roots: []string{filepath.Join(base, "none")}}),
		"roots, statuses and a failing status": fixed(config.Config{
			Roots: []string{root, filepath.Join(base, "none")}, Bookmarks: []config.Bookmark{{Name: "b", Path: base}}}),
		"a failing load":        func() (config.Config, error) { return config.Config{}, errors.New("config:3: unknown directive") },
		"a root that is a file": fixed(config.Config{Roots: []string{filepath.Join(root, "a", "README")}}),
	}
	for name, load := range cases {
		t.Run(name, func(t *testing.T) {
			// run checks that catalog.done is there once and last.
			msgs := run(t, load)
			if name == "roots, statuses and a failing status" {
				var failed bool
				for _, m := range msgs {
					if m.T == proto.TypeCatalogError && errorOf(t, m).Path == bad {
						failed = true
					}
				}
				if !failed {
					t.Fatalf("no catalog.error for the broken repository: %v", types(msgs))
				}
			}
		})
	}
}

func TestABookmarkAddedAfterStartIsInTheNextCatalog(t *testing.T) {
	base := testkit.ShortTempDir(t)
	cfgPath := filepath.Join(base, "config")
	root := mkdir(t, filepath.Join(base, "root"))
	notes := mkdir(t, filepath.Join(base, "notes"))
	if err := os.WriteFile(cfgPath, []byte("root "+root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	load := func() (config.Config, error) { return config.Load(cfgPath) }

	if _, ok := paths(t, run(t, load))[notes]; ok {
		t.Fatal("the bookmark is listed before it is added")
	}
	if err := config.AddBookmark(cfgPath, "notes", notes); err != nil {
		t.Fatal(err)
	}
	if kind := paths(t, run(t, load))[notes]; kind != proto.CatalogBookmark {
		t.Fatalf("the added bookmark is not in the next catalog (kind %q)", kind)
	}
}

func TestAFailingLoadIsReportedAndKeepsTheValuesHeld(t *testing.T) {
	base := testkit.ShortTempDir(t)
	root := mkdir(t, filepath.Join(base, "root"))
	makeRepo(t, filepath.Join(root, "kept"))
	notes := mkdir(t, filepath.Join(base, "notes"))
	good := config.Config{Roots: []string{root}, Bookmarks: []config.Bookmark{{Name: "notes", Path: notes}}}
	fail := errors.New("/x/config:4: unknown directive \"peer\"")
	var broken atomic.Bool
	load := func() (config.Config, error) {
		if broken.Load() {
			return config.Config{Roots: []string{"/elsewhere"}}, fail
		}
		return good, nil
	}

	first := paths(t, run(t, load))
	broken.Store(true)
	msgs := run(t, load)
	var errs []proto.CatalogError
	for _, m := range msgs {
		if m.T == proto.TypeCatalogError {
			errs = append(errs, errorOf(t, m))
		}
	}
	if len(errs) != 1 || errs[0].Text != fail.Error() {
		t.Fatalf("catalog.error %+v, want one with the load's error", errs)
	}
	if msgs[0].T != proto.TypeCatalogError {
		t.Fatalf("the load's error is not first: %v", types(msgs))
	}
	second := paths(t, msgs)
	if len(second) != len(first) || second[filepath.Join(root, "kept")] == "" || second[notes] == "" {
		t.Fatalf("rows after a failing load %v, want the held ones %v", second, first)
	}
}
