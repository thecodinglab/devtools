package persist_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"devtools/internal/proto"
	"devtools/internal/server/model"
	"devtools/internal/server/persist"
	"devtools/internal/session"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket, a state directory or a multiplexer reaches
// them, and every file they write is under a temporary directory.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-persist-test")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "DEVTOOLS_TEST_NO_PTY" {
			continue
		}
		if strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "XDG_") ||
			name == "TMUX" || name == "TMUX_PANE" {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// dir returns a new existing directory by its key (symlinks resolved).
func dir(t *testing.T, parts ...string) string {
	t.Helper()
	d := filepath.Join(append([]string{t.TempDir()}, parts...)...)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := session.Key(d)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sample(d string) persist.State {
	return persist.State{
		Version:  persist.Version,
		Counters: persist.Counters{Session: 4, Tab: 17, Pane: 53},
		Sessions: []persist.Session{{
			ID: "s4", Dir: d, Label: "x-main", Current: "t17", Shown: 1795000000000, Cols: 200, Rows: 49,
			Tabs: []persist.Tab{{ID: "t17", Split: "right", Focused: "p52", Panes: []persist.Pane{{ID: "p52", Cwd: d}, {ID: "p53", Cwd: d}, {ID: "p51", Cwd: d}},
				Layout: &proto.Node{Split: "right", Nodes: []proto.Node{{Pane: "p52", Weight: 3}, {Split: "down", Weight: 2, Nodes: []proto.Node{{Pane: "p53"}, {Pane: "p51"}}}}}}},
		}},
	}
}

func write(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, d string) []string {
	t.Helper()
	es, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	var ns []string
	for _, e := range es {
		ns = append(ns, e.Name())
	}
	return ns
}

func TestSaveReplacesTheFileAtomically(t *testing.T) {
	state := dir(t, "state")
	path := filepath.Join(state, persist.FileName)
	write(t, path, `{"version":1,"counters":{"session":1,"tab":1,"pane":1},"sessions":[]}`, 0o600)
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()

	want := sample(state)
	if err := persist.Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded\n%+v\nwant\n%+v", got, want)
	}
	// The old file was replaced, not written over: a reader that had it
	// open still reads the old content in full.
	b := make([]byte, 200)
	n, _ := old.Read(b)
	if !strings.Contains(string(b[:n]), `"sessions":[]`) {
		t.Fatalf("the old file was written in place: %q", b[:n])
	}
	var raw map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil || raw["version"] != 2.0 {
		t.Fatalf("file = %s (%v)", data, err)
	}
}

func TestSaveLeavesNoTempFile(t *testing.T) {
	state := dir(t, "state")
	path := filepath.Join(state, persist.FileName)
	for i := 0; i < 3; i++ {
		if err := persist.Save(path, sample(state)); err != nil {
			t.Fatal(err)
		}
	}
	if got := names(t, state); !reflect.DeepEqual(got, []string{persist.FileName}) {
		t.Fatalf("state directory holds %v", got)
	}

	// A save that cannot rename leaves neither a temp file nor a change.
	blocked := filepath.Join(state, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, persist.FileName, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := persist.Save(filepath.Join(blocked, persist.FileName), sample(state)); err == nil {
		t.Fatal("a save over a directory succeeded")
	}
	if got := names(t, blocked); !reflect.DeepEqual(got, []string{persist.FileName}) {
		t.Fatalf("after a failed save the directory holds %v", got)
	}
	if err := persist.Save(filepath.Join(state, "missing", persist.FileName), sample(state)); err == nil {
		t.Fatal("a save into a missing directory succeeded")
	}
}

func TestStateFileModeIs0600(t *testing.T) {
	state := dir(t, "state")
	path := filepath.Join(state, persist.FileName)
	if err := persist.Save(path, sample(state)); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Fatalf("mode = %o, want 600", m)
		}
	}
	check()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := persist.Save(path, sample(state)); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestLoadOfAMissingFileIsEmpty(t *testing.T) {
	state := dir(t, "state")
	got, err := persist.Load(filepath.Join(state, persist.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, persist.Empty()) || len(got.Sessions) != 0 {
		t.Fatalf("missing file loads %+v", got)
	}
	if n := len(model.Load(got).Snapshot().Sessions); n != 0 {
		t.Fatalf("%d sessions from a missing file", n)
	}
	if got := names(t, state); len(got) != 0 {
		t.Fatalf("loading a missing file wrote %v", got)
	}
}

// wantSetAside loads path and checks that the file was renamed, not
// deleted, and that the server starts empty.
func wantSetAside(t *testing.T, path, content string) {
	t.Helper()
	write(t, path, content, 0o600)
	got, err := persist.Load(path)
	var aside *persist.SetAside
	if !errors.As(err, &aside) {
		t.Fatalf("Load error = %v, want a *SetAside", err)
	}
	if len(got.Sessions) != 0 || got.Version != persist.Version {
		t.Fatalf("a set-aside file loads %+v", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s is still there (%v)", path, err)
	}
	if !strings.HasPrefix(aside.Bad, path+".bad-") {
		t.Fatalf("set aside as %s", aside.Bad)
	}
	b, err := os.ReadFile(aside.Bad)
	if err != nil || string(b) != content {
		t.Fatalf("set-aside file holds %q (%v), want %q", b, err, content)
	}
	if msg := aside.Error(); strings.Contains(msg, "\n") || msg == "" {
		t.Fatalf("error text %q is not one line", aside.Error())
	}
}

func TestCorruptFileIsSetAsideNotDeleted(t *testing.T) {
	state := dir(t, "state")
	path := filepath.Join(state, persist.FileName)
	wantSetAside(t, path, `{"version":1,"sessions":[{`)
	wantSetAside(t, path, `{"version":1}{"version":1}`)
	wantSetAside(t, path, ``)
	wantSetAside(t, path, `{"version":1,"sessions":"none"}`)
	// Four bad files within one second: none replaced another.
	var bad int
	for _, n := range names(t, state) {
		if strings.HasPrefix(n, persist.FileName+".bad-") {
			bad++
		}
	}
	if bad != 4 {
		t.Fatalf("%d set-aside files, want 4: %v", bad, names(t, state))
	}
}

func TestUnknownVersionIsSetAside(t *testing.T) {
	state := dir(t, "state")
	path := filepath.Join(state, persist.FileName)
	wantSetAside(t, path, `{"version":3,"counters":{"session":1,"tab":1,"pane":1},"sessions":[]}`)
	wantSetAside(t, path, `{"counters":{"session":1,"tab":1,"pane":1},"sessions":[]}`)
}

func TestRestoreMakesEveryPaneDormant(t *testing.T) {
	a, b := dir(t, "a"), dir(t, "b")
	path := filepath.Join(dir(t, "state"), persist.FileName)
	m := model.New()
	for _, d := range []string{a, b} {
		sid, _, _ := m.OpenSession(d, "")
		m.Display(sid)
		tab, p, _, _ := m.NewTab(sid, "")
		m.Split(p, "down", "")
		m.SelectTab(sid, tab, 0, 0)
	}
	m.PutAgent("p1", proto.Agent{Kind: "claude", Status: "blocked"})
	running := 0
	for _, p := range m.Snapshot().Panes {
		if p.State == proto.PaneRunning {
			running++
		}
	}
	if running == 0 {
		t.Fatal("no pane runs before the restart")
	}
	saved := m.Save()
	if err := persist.Save(path, saved); err != nil {
		t.Fatal(err)
	}
	p, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r := model.Load(p)
	snap := r.Snapshot()
	if len(snap.Panes) != 6 || len(snap.Agents) != 0 {
		t.Fatalf("restored %d panes and %d agents, want 6 and 0", len(snap.Panes), len(snap.Agents))
	}
	for _, pn := range snap.Panes {
		if pn.State != proto.PaneDormant || pn.Exit != nil || pn.Title != "" {
			t.Fatalf("restored pane %+v, want it dormant and bare", pn)
		}
	}
	if got := r.Save(); !reflect.DeepEqual(got, saved) {
		t.Fatalf("the structure changed in a restart\n got %+v\nwant %+v", got, saved)
	}
	// Nothing starts until a tab is displayed, and then only that tab.
	res := r.Display(snap.Sessions[0].ID)
	spawns := 0
	for _, e := range res.Effects {
		if e.Kind == model.Spawn {
			spawns++
		}
	}
	if spawns != 2 {
		t.Fatalf("displaying a restored session spawned %d panes, want the 2 of its current tab", spawns)
	}
}

func TestRestoreDropsSessionsWhoseDirectoryIsGone(t *testing.T) {
	root := dir(t)
	keep, drop := filepath.Join(root, "keep"), filepath.Join(root, "drop")
	sub := filepath.Join(keep, "sub")
	for _, d := range []string{sub, drop} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir(t, "state"), persist.FileName)
	m := model.New()
	s1, _, _ := m.OpenSession(keep, "keep")
	m.OpenSession(drop, "drop")
	m.SetPane("p1", func(p *proto.Pane) { p.Cwd = sub })
	m.NewTab(s1, sub)
	if err := persist.Save(path, m.Save()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(drop); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	p, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != 1 || p.Sessions[0].Dir != keep {
		t.Fatalf("restored %+v, want the session of %s only", p.Sessions, keep)
	}
	for _, tb := range p.Sessions[0].Tabs {
		for _, pn := range tb.Panes {
			if pn.Cwd != keep {
				t.Fatalf("pane %s starts in %s, want the session directory", pn.ID, pn.Cwd)
			}
		}
	}
	if p.Counters.Session != 2 {
		t.Fatalf("counters %+v: the dropped session's id must not come back", p.Counters)
	}
}

func TestRestoreRepairsInvariants(t *testing.T) {
	a, b := dir(t, "a"), dir(t, "b")
	path := filepath.Join(dir(t, "state"), persist.FileName)
	file := persist.State{
		Version:  persist.Version,
		Counters: persist.Counters{Session: 1, Tab: 1, Pane: 1},
		Sessions: []persist.Session{
			{ID: "s3", Dir: a, Label: "a\x1b]0;x\x07", Current: "t99", Cols: 5000, Rows: 0, Tabs: []persist.Tab{
				{ID: "t5", Split: "down", Focused: "p99", Panes: []persist.Pane{{ID: "p7", Cwd: a}}},                                   // one pane, a split
				{ID: "t6", Split: "", Focused: "p8", Panes: []persist.Pane{{ID: "p8"}, {ID: "p9", Cwd: a}, {ID: "p10", Cwd: a}}},       // three panes, no split
				{ID: "t7", Split: "diagonal", Panes: []persist.Pane{{ID: "p11", Cwd: a}, {ID: "p7", Cwd: a}, {ID: "x1"}, {ID: "p12"}}}, // a repeated and a bad pane id
				{ID: "t30", Split: "right", Focused: "p31", Panes: []persist.Pane{{ID: "p30", Cwd: a}, {ID: "p31", Cwd: a}, {ID: "p32", Cwd: a}}, // a nested layout
					Layout: &proto.Node{Split: "down", Nodes: []proto.Node{{Pane: "p30", Weight: 2}, {Split: "right", Nodes: []proto.Node{{Pane: "p31"}, {Pane: "p32"}}}}}},
				{ID: "t31", Split: "down", Panes: []persist.Pane{{ID: "p33", Cwd: a}, {ID: "p34", Cwd: a}}, // a layout of other panes
					Layout: &proto.Node{Split: "down", Nodes: []proto.Node{{Pane: "p34"}, {Pane: "p99"}}}},
				{ID: "t8", Panes: nil},                                 // no pane
				{ID: "t5", Panes: []persist.Pane{{ID: "p13", Cwd: a}}}, // a repeated tab id
			}},
			{ID: "s3", Dir: b, Tabs: []persist.Tab{{ID: "t20", Panes: []persist.Pane{{ID: "p20", Cwd: b}}}}}, // a repeated session id
			{ID: "s8", Dir: a, Tabs: []persist.Tab{{ID: "t21", Panes: []persist.Pane{{ID: "p21", Cwd: a}}}}}, // a repeated directory
			{ID: "s9", Dir: b, Tabs: nil}, // no tab
			{ID: "bogus", Dir: b, Tabs: []persist.Tab{{ID: "t22", Panes: []persist.Pane{{ID: "p22", Cwd: b}}}}}, // a bad session id
			{ID: "s10", Dir: b, Cols: 80, Rows: 24, Current: "t23", Tabs: []persist.Tab{{ID: "t23", Split: "right", Focused: "p23", Panes: []persist.Pane{{ID: "p23", Cwd: b}}}}},
		},
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(data), 0o600)
	p, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sessions) != 2 || p.Sessions[0].ID != "s3" || p.Sessions[1].ID != "s10" {
		t.Fatalf("sessions %+v, want s3 and s10", p.Sessions)
	}
	s3 := p.Sessions[0]
	if s3.Label != "a]0;x" || s3.Cols != 1000 || s3.Rows != 2 || s3.Current != "t5" {
		t.Fatalf("s3 = label %q size %dx%d current %s", s3.Label, s3.Cols, s3.Rows, s3.Current)
	}
	want := []persist.Tab{
		{ID: "t5", Split: "", Focused: "p7", Panes: []persist.Pane{{ID: "p7", Cwd: a}}, Layout: &proto.Node{Pane: "p7"}},
		{ID: "t6", Split: "right", Focused: "p8", Panes: []persist.Pane{{ID: "p8", Cwd: a}, {ID: "p9", Cwd: a}, {ID: "p10", Cwd: a}},
			Layout: &proto.Node{Split: "right", Nodes: []proto.Node{{Pane: "p8"}, {Pane: "p9"}, {Pane: "p10"}}}},
		{ID: "t7", Split: "right", Focused: "p11", Panes: []persist.Pane{{ID: "p11", Cwd: a}, {ID: "p12", Cwd: a}},
			Layout: &proto.Node{Split: "right", Nodes: []proto.Node{{Pane: "p11"}, {Pane: "p12"}}}},
		{ID: "t30", Split: "down", Focused: "p31", Panes: []persist.Pane{{ID: "p30", Cwd: a}, {ID: "p31", Cwd: a}, {ID: "p32", Cwd: a}},
			Layout: &proto.Node{Split: "down", Nodes: []proto.Node{{Pane: "p30", Weight: 2}, {Split: "right", Nodes: []proto.Node{{Pane: "p31"}, {Pane: "p32"}}}}}},
		{ID: "t31", Split: "down", Focused: "p33", Panes: []persist.Pane{{ID: "p33", Cwd: a}, {ID: "p34", Cwd: a}},
			Layout: &proto.Node{Split: "down", Nodes: []proto.Node{{Pane: "p33"}, {Pane: "p34"}}}},
	}
	if !reflect.DeepEqual(s3.Tabs, want) {
		t.Fatalf("tabs of s3\n got %+v\nwant %+v", s3.Tabs, want)
	}
	if s10 := p.Sessions[1]; s10.Tabs[0].Split != "" || s10.Cols != 80 || s10.Rows != 24 {
		t.Fatalf("s10 = %+v", s10)
	}
	if got, want := p.Counters, (persist.Counters{Session: 10, Tab: 31, Pane: 34}); got != want {
		t.Fatalf("counters %+v, want %+v", got, want)
	}

	// The model takes the repaired state: one session per directory, the
	// tabs numbered from 1, and new ids above every id of the file.
	m := model.Load(p)
	snap := m.Snapshot()
	for i, tid := range snap.Sessions[0].Tabs {
		for _, tb := range snap.Tabs {
			if tb.ID == tid && tb.Index != i+1 {
				t.Fatalf("tab %s has index %d, want %d", tid, tb.Index, i+1)
			}
		}
	}
	if id, created, _ := m.OpenSession(a, ""); created || id != "s3" {
		t.Fatalf("open of %s = %s, %v; want the restored s3", a, id, created)
	}
	if _, p, _, _ := m.NewTab("s3", ""); p != "p35" {
		t.Fatalf("new pane %s, want p35", p)
	}
}

func TestCountersSurviveSaveAndLoad(t *testing.T) {
	a := dir(t, "a")
	path := filepath.Join(dir(t, "state"), persist.FileName)
	m := model.New()
	var last [3]string
	for i := 0; i < 5; i++ {
		sid, _, _ := m.OpenSession(a, "")
		tab, p, _, _ := m.NewTab(sid, "")
		last = [3]string{sid, tab, p}
		m.CloseSession(sid)
	}
	m.OpenSession(a, "")
	if err := persist.Save(path, m.Save()); err != nil {
		t.Fatal(err)
	}
	p, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := p.Counters, (persist.Counters{Session: 6, Tab: 11, Pane: 11}); got != want {
		t.Fatalf("counters %+v, want %+v", got, want)
	}
	r := model.Load(p)
	r.CloseSession("s6")
	sid, _, _ := r.OpenSession(a, "")
	tab, pane, _, _ := r.NewTab(sid, "")
	if sid != "s7" || tab != "t13" || pane != "p13" {
		t.Fatalf("ids after the restart %s %s %s, want s7 t13 p13 (the last before were %v)", sid, tab, pane, last)
	}
}

// A file of version 1, written before tabs had a layout tree, is read: its
// tabs lie in one split of their direction. A file of a later version is
// set aside.
func TestAFileOfVersionOneIsRead(t *testing.T) {
	d := dir(t, "a")
	path := filepath.Join(dir(t, "state"), persist.FileName)
	write(t, path, `{"version":1,"counters":{"session":1,"tab":1,"pane":2},"sessions":[{"id":"s1","dir":"`+d+
		`","label":"a","current":"t1","cols":80,"rows":24,"tabs":[{"id":"t1","split":"down","focused":"p2","panes":[{"id":"p1","cwd":"`+d+`"},{"id":"p2","cwd":"`+d+`"}]}]}]}`, 0o600)
	p, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := proto.Row([]string{"p1", "p2"}, proto.SplitDown)
	if tb := p.Sessions[0].Tabs[0]; tb.Layout == nil || !reflect.DeepEqual(*tb.Layout, want) || tb.Split != proto.SplitDown {
		t.Fatalf("tab %+v, layout %+v", tb, tb.Layout)
	}
	if p.Version != persist.Version || persist.Version != 2 {
		t.Fatalf("version %d, package version %d", p.Version, persist.Version)
	}
	wantSetAside(t, path, `{"version":3,"sessions":[]}`)
}

func TestAResumeSurvivesSaveAndLoad(t *testing.T) {
	d := dir(t, "p")
	st := sample(d)
	want := persist.Resume{Kind: "claude", ID: "2ef64223-3735-4270-b86b-9f035f7e32e0"}
	st.Sessions[0].Tabs[0].Panes[1].Resume = &want
	path := filepath.Join(dir(t, "state"), persist.FileName)
	if err := persist.Save(path, st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"resume"`); n != 1 {
		t.Fatalf("state.json has %d resume records, want the one: %s", n, b)
	}
	got, err := persist.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	panes := got.Sessions[0].Tabs[0].Panes
	if r := panes[1].Resume; r == nil || *r != want {
		t.Fatalf("the resume read back is %+v, want %+v", r, want)
	}
	if panes[0].Resume != nil || panes[2].Resume != nil {
		t.Fatalf("panes without a record got one: %+v", panes)
	}
}

func TestRepairDropsAMalformedResume(t *testing.T) {
	d := dir(t, "p")
	for name, c := range map[string]struct {
		r    persist.Resume
		kept bool
	}{
		"a uuid":       {persist.Resume{Kind: "claude", ID: "2ef64223-3735-4270-b86b-9f035f7e32e0"}, true},
		"a name":       {persist.Resume{Kind: "codex", ID: "my_session.2"}, true},
		"no kind":      {persist.Resume{ID: "x"}, false},
		"no id":        {persist.Resume{Kind: "claude"}, false},
		"a space":      {persist.Resume{Kind: "claude", ID: "a b"}, false},
		"a quote":      {persist.Resume{Kind: "claude", ID: "a'b"}, false},
		"a semicolon":  {persist.Resume{Kind: "claude", ID: "a;rm"}, false},
		"a newline":    {persist.Resume{Kind: "claude", ID: "a\nb"}, false},
		"not ascii":    {persist.Resume{Kind: "claude", ID: "café"}, false},
		"kind too odd": {persist.Resume{Kind: "cl/aude", ID: "x"}, false},
		"too long":     {persist.Resume{Kind: "claude", ID: strings.Repeat("a", persist.MaxResume+1)}, false},
		"just long":    {persist.Resume{Kind: "claude", ID: strings.Repeat("a", persist.MaxResume)}, true},
	} {
		st := sample(d)
		r := c.r
		st.Sessions[0].Tabs[0].Panes[0].Resume = &r
		got := persist.Repair(st).Sessions[0].Tabs[0].Panes[0].Resume
		switch {
		case c.kept && (got == nil || *got != c.r):
			t.Errorf("%s: repaired to %+v, want it kept", name, got)
		case !c.kept && got != nil:
			t.Errorf("%s: kept %+v", name, got)
		}
		if c.kept && got == &r {
			t.Errorf("%s: Repair shares the record of its input", name)
		}
	}
}
