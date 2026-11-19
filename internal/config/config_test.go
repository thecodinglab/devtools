package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a config file or a multiplexer reaches them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-config-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// writeConfig writes content to the main config file in a new directory and
// returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	write(t, path, content)
	return path
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLoadParsesRootsAndBookmarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := "# workspace roots\n" +
		"root ~/dev\n" +
		"root /srv/work space\n" +
		"\n" +
		"bookmark dotfiles ~/.dotfiles\n" +
		"bookmark notes /home/user/my notes\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	wantRoots := []string{"~/dev", "/srv/work space"}
	if strings.Join(cfg.Roots, "|") != strings.Join(wantRoots, "|") {
		t.Fatalf("roots = %#v, want %#v", cfg.Roots, wantRoots)
	}
	wantBookmarks := []Bookmark{
		{Name: "dotfiles", Path: "~/.dotfiles"},
		{Name: "notes", Path: "/home/user/my notes"},
	}
	if len(cfg.Bookmarks) != len(wantBookmarks) {
		t.Fatalf("bookmarks = %#v, want %#v", cfg.Bookmarks, wantBookmarks)
	}
	for i, want := range wantBookmarks {
		if cfg.Bookmarks[i] != want {
			t.Fatalf("bookmark %d = %#v, want %#v", i, cfg.Bookmarks[i], want)
		}
	}
}

func TestLoadMissingFileReturnsEmptyConfig(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Roots) != 0 || len(cfg.Bookmarks) != 0 {
		t.Fatalf("config = %#v, want empty", cfg)
	}
}

func TestLoadRejectsUnknownDirective(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("workspace ~/dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown directive") {
		t.Fatalf("err = %v, want unknown directive", err)
	}
}

// Revision 2 moved the bookmarks devtools writes to config.local (4.6, F18):
// the main file is read-only once home-manager writes it.
func TestAddBookmarkAppendsAndPreservesExistingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config")
	local := LocalPath(path)
	if err := AddBookmark(path, "notes", "/tmp/notes"); err != nil {
		t.Fatal(err)
	}
	write(t, local, "# keep me\nroot ~/dev\nbookmark notes /tmp/notes")
	if err := AddBookmark(path, "docs", "/tmp/docs"); err != nil {
		t.Fatal(err)
	}
	want := "# keep me\nroot ~/dev\nbookmark notes /tmp/notes\nbookmark docs /tmp/docs\n"
	if got := read(t, local); got != want {
		t.Fatalf("config.local = %q, want %q", got, want)
	}
	if err := AddBookmark(path, "notes", "/elsewhere"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
}

func TestRemoveBookmarkDropsOnlyMatchingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	local := LocalPath(path)
	write(t, local, "# keep me\nroot ~/dev\nbookmark notes /tmp/notes\nbookmark docs /tmp/docs\n")
	if err := RemoveBookmark(path, "notes"); err != nil {
		t.Fatal(err)
	}
	want := "# keep me\nroot ~/dev\nbookmark docs /tmp/docs\n"
	if got := read(t, local); got != want {
		t.Fatalf("config.local = %q, want %q", got, want)
	}
	if err := RemoveBookmark(path, "notes"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

// The second half of this test, that `multiplexer screen` is a load error,
// went with P2-01: the name is checked by mux.Parse in internal/cli (4.6).
func TestLoadParsesMultiplexer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("multiplexer tmux\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Multiplexer != "tmux" {
		t.Fatalf("multiplexer = %q, want tmux", cfg.Multiplexer)
	}
}

// The directives of the LAN discovery are gone with it (4.6, P2-09): each is
// an unknown directive, a fatal error that names the file, the line and the
// directive, in the main file and in the local one. A config with
// `multiplexer tmux` still loads.
func TestRemovedDirectivesAreUnknown(t *testing.T) {
	for _, line := range []string{"peer macbook.local", "port 47390", "discovery off"} {
		directive, _, _ := strings.Cut(line, " ")
		for _, local := range []bool{false, true} {
			path := filepath.Join(t.TempDir(), "config")
			file := path
			if local {
				file = LocalPath(path)
			}
			if err := os.WriteFile(file, []byte("root ~/dev\n"+line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Errorf("%q in %s loads", line, file)
				continue
			}
			want := fmt.Sprintf("%s:2: unknown directive %q", file, directive)
			if err.Error() != want {
				t.Errorf("%q in %s: error %q, want %q", line, file, err, want)
			}
		}
	}
	cfg, err := Load(writeConfig(t, "multiplexer tmux\n"))
	if err != nil {
		t.Fatalf("a config with multiplexer tmux does not load: %v", err)
	}
	if cfg.Multiplexer != "tmux" {
		t.Fatalf("multiplexer = %q, want tmux", cfg.Multiplexer)
	}
}

func TestMachineDirectiveParsesAliasAndConnect(t *testing.T) {
	path := writeConfig(t, "machine macbook\nmachine macmini connect\n  machine\tdesk.lan_2-x   connect  \n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Machine{
		{Name: "macbook"},
		{Name: "macmini", Connect: true},
		{Name: "desk.lan_2-x", Connect: true},
	}
	if len(cfg.Machines) != len(want) {
		t.Fatalf("machines = %#v, want %#v", cfg.Machines, want)
	}
	for i := range want {
		if cfg.Machines[i] != want[i] {
			t.Errorf("machine %d = %#v, want %#v", i, cfg.Machines[i], want[i])
		}
	}
	for _, bad := range []string{
		"machine\n",
		"machine macbook connect now\n",
		"machine macbook always\n",
		"machine macbook Connect\n",
	} {
		path := writeConfig(t, bad)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path+":1:") {
			t.Errorf("%q: err = %v, want an error at %s:1", bad, err, path)
		}
	}
}

// SEC4: a machine name reaches the ssh command line, so anything ssh could
// read as an option, a user, a second word or a shell token is a load error.
func TestMachineNamesThatCouldBeOptionsAreRejected(t *testing.T) {
	for _, name := range []string{
		"-oProxyCommand=x",
		"-v",
		"a;b",
		"user@host",
		"a|b",
		"$(x)",
		"a/b",
		"a:22",
		".hidden",
		"_x",
		"mäc",
		strings.Repeat("a", 65),
	} {
		path := writeConfig(t, "machine "+name+"\n")
		cfg, err := Load(path)
		if err == nil {
			t.Errorf("machine %q accepted: %#v", name, cfg.Machines)
			continue
		}
		if !strings.Contains(err.Error(), path+":1:") {
			t.Errorf("machine %q: err = %v, want it to name %s:1", name, err, path)
		}
	}
	// "a b" is two words: the second is not the flag connect.
	if _, err := Load(writeConfig(t, "machine a b\n")); err == nil {
		t.Error(`machine "a b" accepted`)
	}
	// An empty name is a machine line without a value.
	if _, err := Load(writeConfig(t, "machine \n")); err == nil {
		t.Error("machine without a name accepted")
	}
	// The longest name session.ValidMachine allows still loads.
	long := strings.Repeat("a", 64)
	cfg, err := Load(writeConfig(t, "machine "+long+"\n"))
	if err != nil || len(cfg.Machines) != 1 || cfg.Machines[0].Name != long {
		t.Errorf("64-character machine: cfg = %#v, err = %v", cfg.Machines, err)
	}
}

func TestDuplicateMachineIsAnError(t *testing.T) {
	tests := []struct {
		name, main, local, want string
	}{
		{"same line twice", "machine mac\nmachine mac\n", "", "named twice"},
		{"different flags", "machine mac\nmachine mac connect\n", "", "different flags"},
		{"differs only in case", "machine Mac\nmachine mac\n", "", "named twice"},
		{"across both files", "machine mac connect\n", "machine mac connect\n", "named twice"},
		{"across both files with different flags", "machine mac\n", "machine mac connect\n", "different flags"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.main)
			if tt.local != "" {
				write(t, LocalPath(path), tt.local)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if !strings.Contains(err.Error(), "first at "+path+":1") {
				t.Errorf("err = %v, want it to name the first line", err)
			}
		})
	}
}

func TestBindUnbindColorKeepTheirLine(t *testing.T) {
	content := "# keys\n" +
		"bind prefix+f hub new\n" +
		"\n" +
		"bind ctrl+h focus-left\n" +
		"unbind prefix+x\n" +
		"color muted #55585f #A6A8B0\n" +
		"bind prefix+g Hub\n"
	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	wantBinds := []Bind{
		{Chord: "prefix+f", Action: "hub", Arg: "new", Line: 2},
		{Chord: "ctrl+h", Action: "focus-left", Line: 4},
		// Chords and actions are kept as written: keys.New checks them.
		{Chord: "prefix+g", Action: "Hub", Line: 7},
	}
	if len(cfg.Binds) != len(wantBinds) {
		t.Fatalf("binds = %#v, want %#v", cfg.Binds, wantBinds)
	}
	for i := range wantBinds {
		if cfg.Binds[i] != wantBinds[i] {
			t.Errorf("bind %d = %#v, want %#v", i, cfg.Binds[i], wantBinds[i])
		}
	}
	if len(cfg.Unbinds) != 1 || cfg.Unbinds[0] != (Unbind{Chord: "prefix+x", Line: 5}) {
		t.Errorf("unbinds = %#v", cfg.Unbinds)
	}
	if len(cfg.Colors) != 1 || cfg.Colors[0] != (Color{Token: "muted", Light: "#55585f", Dark: "#A6A8B0", Line: 6}) {
		t.Errorf("colors = %#v", cfg.Colors)
	}

	for _, bad := range []string{
		"bind\n",
		"bind prefix+f\n",
		"bind prefix+f hub new extra\n",
		"unbind\n",
		"unbind prefix+f prefix+g\n",
		"color muted\n",
		"color muted #55585f\n",
		"color muted #55585f #a6a8b0 #000000\n",
		"color muted 55585f #a6a8b0\n",
		"color muted #55585 #a6a8b0\n",
		"color muted #55585f #a6a8bg\n",
		"color muted #55585f red\n",
	} {
		path := writeConfig(t, "root ~/dev\n"+bad)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path+":2:") {
			t.Errorf("%q: err = %v, want an error at %s:2", bad, err, path)
		}
	}
}

func TestLocalFileIsReadAfterTheMainFile(t *testing.T) {
	path := writeConfig(t, "root ~/dev\nbookmark notes /notes\nmultiplexer tmux\nmachine macbook\nbind prefix+f hub new\ncolor muted #000000 #ffffff\n")
	write(t, LocalPath(path), "# by hand\nroot ~/work\nbookmark vault /vault\nmultiplexer devtools\nmachine macmini connect\nunbind prefix+f\nbind prefix+f hub\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Roots, "|"); got != "~/dev|~/work" {
		t.Errorf("roots = %q", got)
	}
	if len(cfg.Bookmarks) != 2 || cfg.Bookmarks[0].Name != "notes" || cfg.Bookmarks[1].Name != "vault" {
		t.Errorf("bookmarks = %#v", cfg.Bookmarks)
	}
	if cfg.Multiplexer != "devtools" {
		t.Errorf("multiplexer = %q, want the local file's devtools", cfg.Multiplexer)
	}
	if len(cfg.Machines) != 2 || cfg.Machines[0] != (Machine{Name: "macbook"}) || cfg.Machines[1] != (Machine{Name: "macmini", Connect: true}) {
		t.Errorf("machines = %#v", cfg.Machines)
	}
	wantBinds := []Bind{
		{Chord: "prefix+f", Action: "hub", Arg: "new", Line: 5},
		{Chord: "prefix+f", Action: "hub", Line: 7},
	}
	if len(cfg.Binds) != 2 || cfg.Binds[0] != wantBinds[0] || cfg.Binds[1] != wantBinds[1] {
		t.Errorf("binds = %#v, want %#v", cfg.Binds, wantBinds)
	}
	if len(cfg.Unbinds) != 1 || cfg.Unbinds[0] != (Unbind{Chord: "prefix+f", Line: 6}) {
		t.Errorf("unbinds = %#v", cfg.Unbinds)
	}

	// The local file alone is enough.
	dir := t.TempDir()
	alone := filepath.Join(dir, "config")
	write(t, LocalPath(alone), "bookmark vault /vault\n")
	cfg, err = Load(alone)
	if err != nil || len(cfg.Bookmarks) != 1 || cfg.Bookmarks[0].Name != "vault" {
		t.Errorf("local file only: cfg = %#v, err = %v", cfg, err)
	}

	// The local file is the main path with ".local" appended, also for a
	// path from DEVTOOLS_CONFIG.
	t.Setenv("DEVTOOLS_CONFIG", "/x/y/devtools.conf")
	main, err := Path()
	if err != nil || LocalPath(main) != "/x/y/devtools.conf.local" {
		t.Errorf("local path of %q = %q, err = %v", main, LocalPath(main), err)
	}
}

func TestBookmarksAreWrittenToTheLocalFile(t *testing.T) {
	path := writeConfig(t, "root ~/dev\nbookmark notes /notes\n")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	mainBefore := read(t, path)

	if err := AddBookmark(path, "vault", "/home/user/my vault"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != mainBefore {
		t.Fatalf("main file changed to %q", got)
	}
	if got, want := read(t, LocalPath(path)), "bookmark vault /home/user/my vault\n"; got != want {
		t.Fatalf("config.local = %q, want %q", got, want)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bookmarks) != 2 || cfg.Bookmarks[1] != (Bookmark{Name: "vault", Path: "/home/user/my vault"}) {
		t.Fatalf("bookmarks = %#v", cfg.Bookmarks)
	}

	// A name of the main file is a duplicate too.
	if err := AddBookmark(path, "notes", "/elsewhere"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
	// A bookmark of the main file is not removed: that file is not ours.
	if err := RemoveBookmark(path, "notes"); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want it to name %s", err, path)
	}
	if got := read(t, path); got != mainBefore {
		t.Fatalf("main file changed to %q", got)
	}

	if err := RemoveBookmark(path, "vault"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, LocalPath(path)); got != "" {
		t.Fatalf("config.local after remove = %q, want empty", got)
	}
	if got := read(t, path); got != mainBefore {
		t.Fatalf("main file changed to %q", got)
	}
	if err := RemoveBookmark(path, "vault"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestUnknownDirectiveIsStillFatalInBothFiles(t *testing.T) {
	path := writeConfig(t, "root ~/dev\nworkspace ~/dev\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path+":2: unknown directive") {
		t.Errorf("main file: err = %v, want unknown directive at %s:2", err, path)
	}

	path = writeConfig(t, "root ~/dev\n")
	local := LocalPath(path)
	write(t, local, "# by hand\n\nworkspace ~/dev\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), local+":3: unknown directive") {
		t.Errorf("local file: err = %v, want unknown directive at %s:3", err, local)
	}

	// A malformed line of the local file is fatal as well.
	write(t, local, "bind prefix+f\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), local+":1:") {
		t.Errorf("malformed local line: err = %v, want an error at %s:1", err, local)
	}
}

func TestMultiplexerValueIsKeptAsWritten(t *testing.T) {
	for _, name := range []string{"devtools", "tmux", "herdr", "screen", "Tmux"} {
		cfg, err := Load(writeConfig(t, "multiplexer "+name+"\n"))
		if err != nil {
			t.Errorf("multiplexer %s: %v", name, err)
			continue
		}
		if cfg.Multiplexer != name {
			t.Errorf("multiplexer = %q, want %q", cfg.Multiplexer, name)
		}
	}
	// Without a value, or with two, the line is malformed.
	for _, bad := range []string{"multiplexer\n", "multiplexer   \n", "multiplexer tmux herdr\n"} {
		path := writeConfig(t, bad)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path+":1:") {
			t.Errorf("%q: err = %v, want an error at %s:1", bad, err, path)
		}
	}
}
