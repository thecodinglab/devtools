package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Bookmark struct {
	Name string
	Path string
}

type Config struct {
	Roots     []string
	Bookmarks []Bookmark
}

func Path() (string, error) {
	if path := os.Getenv("DEVTOOLS_CONFIG"); path != "" {
		return path, nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "devtools", "config"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "devtools", "config"), nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	return parse(path, string(data))
}

func parse(path, content string) (Config, error) {
	var cfg Config
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directive, rest := splitToken(line)
		switch directive {
		case "root":
			if rest == "" {
				return Config{}, fmt.Errorf("%s:%d: root requires a path", path, i+1)
			}
			cfg.Roots = append(cfg.Roots, rest)
		case "bookmark":
			name, target := splitToken(rest)
			if name == "" || target == "" {
				return Config{}, fmt.Errorf("%s:%d: bookmark requires a name and a path", path, i+1)
			}
			cfg.Bookmarks = append(cfg.Bookmarks, Bookmark{Name: name, Path: target})
		default:
			return Config{}, fmt.Errorf("%s:%d: unknown directive %q", path, i+1, directive)
		}
	}
	return cfg, nil
}

func splitToken(s string) (string, string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func AddBookmark(path, name, target string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	for _, bookmark := range cfg.Bookmarks {
		if bookmark.Name == name {
			return fmt.Errorf("bookmark %q already exists: %s", name, bookmark.Path)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += fmt.Sprintf("bookmark %s %s\n", name, target)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func RemoveBookmark(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("bookmark %q not found", name)
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, raw := range lines {
		directive, rest := splitToken(strings.TrimSpace(raw))
		if directive == "bookmark" {
			candidate, _ := splitToken(rest)
			if candidate == name {
				removed = true
				continue
			}
		}
		kept = append(kept, raw)
	}
	if !removed {
		return fmt.Errorf("bookmark %q not found", name)
	}
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644)
}
