package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestAddBookmarkAppendsAndPreservesExistingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config")
	if err := AddBookmark(path, "notes", "/tmp/notes"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# keep me\nroot ~/dev\nbookmark notes /tmp/notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddBookmark(path, "docs", "/tmp/docs"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep me\nroot ~/dev\nbookmark notes /tmp/notes\nbookmark docs /tmp/docs\n"
	if string(data) != want {
		t.Fatalf("config = %q, want %q", data, want)
	}
	if err := AddBookmark(path, "notes", "/elsewhere"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
}

func TestRemoveBookmarkDropsOnlyMatchingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := "# keep me\nroot ~/dev\nbookmark notes /tmp/notes\nbookmark docs /tmp/docs\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBookmark(path, "notes"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# keep me\nroot ~/dev\nbookmark docs /tmp/docs\n"
	if string(data) != want {
		t.Fatalf("config = %q, want %q", data, want)
	}
	if err := RemoveBookmark(path, "notes"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}
