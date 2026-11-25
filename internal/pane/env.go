package pane

import (
	"sort"
	"strings"

	"devtools/internal/proto"
)

// EnvPassEnv names the server variable that lists, comma separated, further
// variables of the server's environment that reach every pane (3.2.1). The
// Nix module writes it from programs.devtools.server.environment (8.4.3).
const EnvPassEnv = "DEVTOOLS_PASS_ENV"

// serverVars are the variables of the server's own environment that reach a
// pane; LC_* is matched by its prefix (3.2.1).
var serverVars = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "LANG": true,
	"PATH": true, "XDG_RUNTIME_DIR": true, "TMPDIR": true,
}

// DisplayVars are the variables a local interactive hello hands to the
// panes spawned after it (3.2.1); every other variable of hello.env is
// ignored.
var DisplayVars = []string{"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS"}

// fixedVars are set by Env itself (TERM, COLORTERM, DEVTOOLS_PANE,
// DEVTOOLS_SOCKET) or by Start (DEVTOOLS_TTY); a value of the server's
// environment for one of them never reaches a pane.
var fixedVars = map[string]bool{
	"TERM": true, "COLORTERM": true,
	proto.EnvPane: true, proto.EnvSocket: true, proto.EnvTTY: true,
}

// denied reports whether name is the mark of another multiplexer or of the
// terminal the server was started from. Such a variable never reaches a
// pane, not even when DEVTOOLS_PASS_ENV names it: a devtools pane never
// carries the mark of tmux, screen, zellij, herdr, kitty, ghostty or Claude
// Code, whichever of them the server was started from (3.2.1, 4.7).
func denied(name string) bool {
	switch name {
	case "TMUX", "TMUX_PANE", "STY", "CLAUDECODE", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR":
		return true
	}
	// The descriptor inventory of a live upgrade (internal/handover) and
	// the knobs of its tests are the server's own; boot removes the first
	// from its environment, and no pane carries any of them.
	return strings.HasPrefix(name, "ZELLIJ") || strings.HasPrefix(name, "HERDR_") || strings.HasPrefix(name, "DEVTOOLS_HANDOVER") || strings.HasPrefix(name, "DEVTOOLS_UPGRADE_")
}

// validName reports whether name can be the name of an environment entry.
func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "=\x00")
}

// validValue reports whether v can be the value of an environment entry: the
// system call that starts the program takes C strings.
func validValue(v string) bool { return !strings.Contains(v, "\x00") }

// PassEnv returns the names DEVTOOLS_PASS_ENV lists: split on commas, blanks
// around each name removed, empty names dropped.
func PassEnv(value string) []string {
	var names []string
	for _, n := range strings.Split(value, ",") {
		if n = strings.TrimSpace(n); validName(n) {
			names = append(names, n)
		}
	}
	return names
}

// Env builds a pane's environment from the allow-list of 3.2.1; pure. It
// sets DEVTOOLS_PANE and DEVTOOLS_SOCKET. It does not set DEVTOOLS_TTY: the
// device path exists only after Start opened the PTY, and Start appends it.
//
// server is the server's own environment in the form of os.Environ; of a
// name given twice the first entry counts, as for getenv. hello holds the
// display variables (DisplayVars) the server keeps from local interactive
// hellos (Hold); every other key of it is ignored, and so is an empty value.
// The result is sorted by name.
func Env(server []string, hello map[string]string, paneID, socket string) []string {
	have := map[string]string{}
	for _, kv := range server {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || !validName(name) {
			continue
		}
		if _, dup := have[name]; !dup {
			have[name] = v
		}
	}
	pass := map[string]bool{}
	for _, n := range PassEnv(have[EnvPassEnv]) {
		pass[n] = true
	}
	vals := map[string]string{}
	for name, v := range have {
		if denied(name) || fixedVars[name] || !validValue(v) {
			continue
		}
		if serverVars[name] || strings.HasPrefix(name, "LC_") || pass[name] {
			vals[name] = v
		}
	}
	for _, name := range DisplayVars {
		if v := hello[name]; v != "" && validValue(v) {
			vals[name] = v
		}
	}
	vals["TERM"] = "xterm-256color"
	vals["COLORTERM"] = "truecolor"
	vals[proto.EnvPane] = paneID
	vals[proto.EnvSocket] = socket

	env := make([]string, 0, len(vals))
	for name, v := range vals {
		env = append(env, name+"="+v)
	}
	sort.Strings(env)
	return env
}

// Hold records the display variables of one local interactive hello in
// held, the values the server keeps for the panes it spawns later: each
// variable keeps the value of the newest hello that carried it. A hello
// without one of them, or with an empty value, changes nothing for that
// variable, so a client started from an ssh login or a console cannot take
// the display away from the panes (3.2.1). Variables other than DisplayVars
// are ignored.
func Hold(held map[string]string, hello map[string]string) {
	for _, name := range DisplayVars {
		if v := hello[name]; v != "" && validValue(v) {
			held[name] = v
		}
	}
}

// withTTY returns env without any DEVTOOLS_TTY entry and with
// DEVTOOLS_TTY=tty appended.
func withTTY(env []string, tty string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name == proto.EnvTTY {
			continue
		}
		out = append(out, kv)
	}
	return append(out, proto.EnvTTY+"="+tty)
}

// lookEnv returns the value of name in env (first entry wins).
func lookEnv(env []string, name string) (string, bool) {
	for _, kv := range env {
		if n, v, ok := strings.Cut(kv, "="); ok && n == name {
			return v, true
		}
	}
	return "", false
}
