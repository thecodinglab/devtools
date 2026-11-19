// Package session holds what every part of devtools agrees on about a
// session: its label, its identity, what a window can show, and the checks
// and the cleaning of the strings that cross a connection (5.3.8 of the
// plan).
//
// The package imports the standard library only (import rule 6).
package session

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of the strings shown to users, in runes, for Sanitize.
const MaxLabel, MaxTitle, MaxText = 200, 256, 400

// Name is the label of the session of a project's worktree: lower case,
// letters and digits, every other run of characters one dash, no dash at
// either end, "devtools" when nothing is left. It is today's
// tmux.SessionName, moved unchanged (internal/tmux/tmux.go:19-39).
func Name(project, worktree string) string {
	name := strings.Trim(project+"-"+worktree, "-")
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		return "devtools"
	}
	return clean
}

// Target is what a window can show. Machine "" is the machine the client
// runs on. Exactly one of Session and Dir is set; Name goes with Dir.
type Target struct {
	Machine string
	Session string // server session id
	Dir     string // open or focus the session of this directory
	Name    string // label for a session opened by Dir
	Tab     string // optional
	Pane    string // optional
}

// Key is the identity of a session: the cleaned absolute path of dir with
// symlinks resolved. It is computed on the machine that owns the directory
// only. A directory that does not exist has no key: the error is that of
// resolving it.
func Key(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

// ValidID reports whether id is a server id of a session, a tab or a pane:
// ^[stp][0-9]{1,18}$.
func ValidID(id string) bool {
	if len(id) < 2 || len(id) > 19 {
		return false
	}
	switch id[0] {
	case 's', 't', 'p':
	default:
		return false
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

// ValidMachine reports whether name can be a machine, an ssh host alias:
// ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$. The first character is never "-", so
// a name cannot be read as an option of ssh, and no character separates a
// user, a port or a command (no "@", ":", "/", space or control).
func ValidMachine(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// Sanitize removes the C0 control characters (U+0000 to U+001F, so also
// tab and newline), DEL (U+007F), the C1 control characters (U+0080 to
// U+009F) and the bidi controls U+200E, U+200F, U+202A to U+202E and U+2066
// to U+2069, then cuts the result to at most max runes, the last of which is
// "…" when anything was cut. A byte that is not valid UTF-8 becomes U+FFFD,
// so the result is always valid UTF-8. A max below 1 yields "".
func Sanitize(s string, max int) string {
	if max < 1 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	cut := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if removed(r) {
			continue
		}
		if n == max {
			cut = true
			break
		}
		b.WriteRune(r) // utf8.RuneError for an invalid byte writes U+FFFD
		n++
	}
	if !cut {
		return b.String()
	}
	// Keep max-1 runes and the ellipsis.
	out := b.String()
	_, last := utf8.DecodeLastRuneInString(out)
	return out[:len(out)-last] + "…"
}

// removed reports whether Sanitize drops r.
func removed(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x200e, r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
