package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Bookmark struct {
	Name string
	Path string
}

type Config struct {
	Roots       []string
	Bookmarks   []Bookmark
	Multiplexer string
	// Peers are machines asked for their projects directly, besides the
	// ones a broadcast finds; Port is where devtools serve listens and
	// queries go, 0 for the default; NoDiscovery keeps devtools from looking
	// for other machines at all.
	Peers       []string
	Port        int
	NoDiscovery bool
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
		case "multiplexer":
			if rest != "tmux" && rest != "herdr" {
				return Config{}, fmt.Errorf("%s:%d: multiplexer must be tmux or herdr", path, i+1)
			}
			cfg.Multiplexer = rest
		case "peer":
			if rest == "" || strings.ContainsAny(rest, " \t") {
				return Config{}, fmt.Errorf("%s:%d: peer requires one host or host:port", path, i+1)
			}
			cfg.Peers = append(cfg.Peers, rest)
		case "port":
			port, err := strconv.Atoi(rest)
			if err != nil || port <= 0 || port > 65535 {
				return Config{}, fmt.Errorf("%s:%d: port must be a number from 1 to 65535", path, i+1)
			}
			cfg.Port = port
		case "discovery":
			if rest != "on" && rest != "off" {
				return Config{}, fmt.Errorf("%s:%d: discovery must be on or off", path, i+1)
			}
			cfg.NoDiscovery = rest == "off"
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
