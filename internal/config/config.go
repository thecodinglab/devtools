// Package config reads the two config files of devtools (4.6 of the plan):
// the main file, which home-manager writes and which is read-only, and the
// local file beside it, which devtools writes (bookmarks) and the user may
// edit. Both have one grammar. This package checks syntax only: token
// counts, the form of a colour, the form of a machine name. Chords and
// actions are checked by keys.New, colour tokens by ui.NewTheme and the
// multiplexer name by mux.Parse; config knows no backend (import rule 12).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"devtools/internal/session"
)

type Bookmark struct {
	Name string
	Path string
}

// Machine is one `machine <alias> [connect]` line (4.6).
type Machine struct {
	Name    string // ssh host alias
	Connect bool   // held for as long as the client runs
}

// Bind is one `bind <chord> <action> [arg]` line; Line is its line number.
type Bind struct {
	Chord, Action, Arg string
	Line               int
}

// Unbind is one `unbind <chord>` line.
type Unbind struct {
	Chord string
	Line  int
}

// Color is one `color <token> <light> <dark>` line.
type Color struct {
	Token, Light, Dark string
	Line               int
}

type Config struct {
	Roots     []string
	Bookmarks []Bookmark
	// Multiplexer is the value of the `multiplexer` directive as written.
	Multiplexer string

	// The directives of 4.6, in file order, the main file before the local
	// one. Line is the line number inside the file the line came from.
	Machines []Machine
	Binds    []Bind
	Unbinds  []Unbind
	Colors   []Color
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

// LocalPath is the second config file of 4.6: path with ".local" appended.
// It is the file devtools writes; path itself may be read-only.
func LocalPath(path string) string {
	return path + ".local"
}

// Load reads path and then LocalPath(path), with the same grammar, into one
// Config. List directives append, so the lines of the local file come after
// those of the main file; a `multiplexer` line of the local file replaces
// one of the main file. A missing file is no error. A malformed line or an
// unknown directive in either file is a fatal error that names the file and
// the line, and so is a machine named twice, within a file or across both.
func Load(path string) (Config, error) {
	p := newParser()
	for _, file := range []string{path, LocalPath(path)} {
		data, err := os.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Config{}, err
		}
		if err := p.parse(file, string(data)); err != nil {
			return Config{}, err
		}
	}
	return p.cfg, nil
}

// machineAt is where a machine was named first, for the duplicate check.
type machineAt struct {
	machine Machine
	file    string
	line    int
}

// parser carries what the checks across lines and files need.
type parser struct {
	cfg Config
	// machines is keyed by the lower-case name: ssh lowercases the host it
	// is given before it matches Host patterns, so "Mac" and "mac" are one
	// machine.
	machines map[string]machineAt
}

func newParser() *parser {
	return &parser{machines: map[string]machineAt{}}
}

func (p *parser) parse(path, content string) error {
	cfg := &p.cfg
	for i, raw := range strings.Split(content, "\n") {
		n := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directive, rest := splitToken(line)
		fields := strings.Fields(rest)
		switch directive {
		case "root":
			if rest == "" {
				return fmt.Errorf("%s:%d: root requires a path", path, n)
			}
			cfg.Roots = append(cfg.Roots, rest)
		case "bookmark":
			name, target := splitToken(rest)
			if name == "" || target == "" {
				return fmt.Errorf("%s:%d: bookmark requires a name and a path", path, n)
			}
			cfg.Bookmarks = append(cfg.Bookmarks, Bookmark{Name: name, Path: target})
		case "multiplexer":
			// Kept as written: mux.Parse in internal/cli checks the name
			// (4.6, 5.3.8). Here only its form, one word.
			if len(fields) != 1 {
				return fmt.Errorf("%s:%d: multiplexer requires one name", path, n)
			}
			cfg.Multiplexer = fields[0]
		case "machine":
			if err := p.machine(path, n, fields); err != nil {
				return err
			}
		case "bind":
			if len(fields) < 2 || len(fields) > 3 {
				return fmt.Errorf("%s:%d: bind requires a chord, an action and at most one argument", path, n)
			}
			b := Bind{Chord: fields[0], Action: fields[1], Line: n}
			if len(fields) == 3 {
				b.Arg = fields[2]
			}
			cfg.Binds = append(cfg.Binds, b)
		case "unbind":
			if len(fields) != 1 {
				return fmt.Errorf("%s:%d: unbind requires one chord", path, n)
			}
			cfg.Unbinds = append(cfg.Unbinds, Unbind{Chord: fields[0], Line: n})
		case "color":
			if len(fields) != 3 {
				return fmt.Errorf("%s:%d: color requires a token, a light and a dark colour", path, n)
			}
			for _, value := range fields[1:] {
				if !isHexColor(value) {
					return fmt.Errorf("%s:%d: color %s: %q is not of the form #rrggbb", path, n, fields[0], value)
				}
			}
			cfg.Colors = append(cfg.Colors, Color{Token: fields[0], Light: fields[1], Dark: fields[2], Line: n})
		default:
			return fmt.Errorf("%s:%d: unknown directive %q", path, n, directive)
		}
	}
	return nil
}

// machine checks one `machine <alias> [connect]` line (3.5.1, SEC4). The
// alias must pass session.ValidMachine, so ssh can never read it as an
// option, a user or a second argument; and a machine is named once only.
func (p *parser) machine(path string, n int, fields []string) error {
	if len(fields) < 1 || len(fields) > 2 {
		return fmt.Errorf("%s:%d: machine requires an ssh host alias and optionally connect", path, n)
	}
	m := Machine{Name: fields[0]}
	if !session.ValidMachine(m.Name) {
		return fmt.Errorf("%s:%d: machine %q is not a valid host alias: letters, digits, '.', '_' and '-', starting with a letter or a digit, at most 64", path, n, m.Name)
	}
	if len(fields) == 2 {
		if fields[1] != "connect" {
			return fmt.Errorf("%s:%d: machine %s: unknown flag %q, want connect", path, n, m.Name, fields[1])
		}
		m.Connect = true
	}
	key := strings.ToLower(m.Name)
	if first, ok := p.machines[key]; ok {
		if first.machine.Connect != m.Connect {
			return fmt.Errorf("%s:%d: machine %s is named twice with different flags (first at %s:%d)", path, n, m.Name, first.file, first.line)
		}
		return fmt.Errorf("%s:%d: machine %s is named twice (first at %s:%d)", path, n, m.Name, first.file, first.line)
	}
	p.machines[key] = machineAt{machine: m, file: path, line: n}
	p.cfg.Machines = append(p.cfg.Machines, m)
	return nil
}

func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

func splitToken(s string) (string, string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// AddBookmark adds `bookmark <name> <target>` to the local file of path
// (LocalPath, 4.6), creating the file and its directory if needed. path is
// the main config file as Path returns it; it is read, never written. A name
// that either file already has is refused.
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
	local := LocalPath(path)
	data, err := os.ReadFile(local)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += fmt.Sprintf("bookmark %s %s\n", name, target)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	return os.WriteFile(local, []byte(content), 0o644)
}

// RemoveBookmark removes the `bookmark <name>` lines from the local file of
// path and keeps every other line as it was. A bookmark that only the main
// file has is not removed, because devtools does not write that file; the
// error names it.
func RemoveBookmark(path, name string) error {
	local := LocalPath(path)
	data, err := os.ReadFile(local)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, raw := range lines {
		if bookmarkName(raw) == name {
			removed = true
			continue
		}
		kept = append(kept, raw)
	}
	if !removed {
		if main, err := os.ReadFile(path); err == nil {
			for _, raw := range strings.Split(string(main), "\n") {
				if bookmarkName(raw) == name {
					return fmt.Errorf("bookmark %q is in %s, which devtools does not write: remove it there", name, path)
				}
			}
		}
		return fmt.Errorf("bookmark %q not found", name)
	}
	return os.WriteFile(local, []byte(strings.Join(kept, "\n")), 0o644)
}

// bookmarkName is the name of a `bookmark` line, or "" for any other line.
func bookmarkName(raw string) string {
	directive, rest := splitToken(strings.TrimSpace(raw))
	if directive != "bookmark" {
		return ""
	}
	name, _ := splitToken(rest)
	return name
}
