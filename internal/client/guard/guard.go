package guard

// The nesting guard of 3.4 c (P1-11): the environment check, the verdict on
// the terminal's answer, and Check, the one function that makes a valid
// Pass. The probe itself is in probe.go.

import (
	"io"
	"strings"
	"time"
)

// marker is one environment variable that says the terminal is inside a
// multiplexer, and what Nested.Where then names.
type marker struct{ name, where string }

// markers are the variables of 3.4 c and 5.3.7, in the order Env tests them.
// The order decides which one Env names when several are set.
var markers = []marker{
	{"DEVTOOLS_PANE", "devtools"},
	{"TMUX", "tmux"},
	{"STY", "screen"},
	{"ZELLIJ", "zellij"},
	{"ZELLIJ_SESSION_NAME", "zellij"},
	{"HERDR_ENV", "herdr"},
}

// Env refuses when a marker variable is set and not listed in dead.
// Markers: DEVTOOLS_PANE, TMUX, STY, ZELLIJ, ZELLIJ_SESSION_NAME, HERDR_ENV.
//
// env is a list in the form of os.Environ. A marker counts as set when it
// is present at all, also with an empty value, and when any of several
// entries of the same name is present: the guard fails closed, and an empty
// TMUX is the usual way to make a multiplexer nest. dead holds variable
// names (mux.Context.Dead): a marker named there does not refuse, and a
// name in dead that is no marker changes nothing.
func Env(env, dead []string) *Nested {
	set := make(map[string]bool, len(markers))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	for _, m := range markers {
		if !set[m.name] || listed(dead, m.name) {
			continue
		}
		return &Nested{Where: m.where}
	}
	return nil
}

func listed(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// unknownTerminal is the Where of every refusal that cannot name the
// multiplexer: a silent terminal, DA1 without XTVERSION, and the names of a
// relay that is not one of the three backends.
const unknownTerminal = "unknown terminal"

// refused is the list of XTVERSION names Answer.Nested refuses, matched as
// a prefix of the name, without regard to case and after leading blanks.
//
// devtools: a devtools pane, which answers "devtools <version> <host>".
// tmux: a tmux pane or popup; tmux 3.8 answers "tmux 3.8" itself whether a
// client is attached or not (E6, report 10 T14).
// libghostty: the answer of libghostty-vt when its host set no XTVERSION
// callback (E6), so some multiplexer or embedder that did not name itself.
// It names no backend, because which program it is cannot be told.
// libvterm: neovim's :terminal, which answered "libvterm(0.3)" (E6, the one
// relay that answered with a name of its own).
//
// provisional E6: the rows of E6 that need the user (a real ghostty window,
// ssh localhost, a herdr pane and popup, the Macs) are pending [GO]. That
// an unpatched herdr pane answers "libghostty" is UNVERIFIED (report 10,
// Hd8); if herdr answers with a name of its own, that name is added here.
var refused = []struct{ prefix, where string }{
	{"devtools", "devtools"},
	{"tmux", "tmux"},
	{"libghostty", unknownTerminal},
	{"libvterm", unknownTerminal},
}

// refusedName returns the Where of a name on the list, and false for a name
// that is not on it.
func refusedName(name string) (string, bool) {
	n := strings.ToLower(strings.TrimLeft(name, " \t"))
	for _, r := range refused {
		if strings.HasPrefix(n, r.prefix) {
			return r.where, true
		}
	}
	return "", false
}

// Nested is nil only for an XTVERSION name that is not on the list, or, with
// trust (DEVTOOLS_TRUST_TERMINAL=1), for DA1 without XTVERSION.
//
// A name that is empty or blank counts as no XTVERSION answer. Without any
// answer the terminal is refused whatever trust says (fail closed), and
// trust never admits a name on the list.
func (a Answer) Nested(trust bool) *Nested {
	name := strings.TrimLeft(a.XTVersion, " \t")
	if strings.TrimSpace(name) == "" {
		if a.DA1 && trust {
			return nil
		}
		return &Nested{Where: unknownTerminal}
	}
	where, ok := refusedName(name)
	if !ok {
		return nil
	}
	n := &Nested{Where: where}
	if where == "devtools" {
		n.Host = devtoolsHost(name)
	}
	return n
}

// devtoolsHost is the host of the answer "devtools <version> <host>", or ""
// when the answer carries none or carries one outside [A-Za-z0-9._-], the
// set the engine's identity is restricted to (3.2.2). The host is shown to
// the user, so nothing else from the terminal reaches the words.
func devtoolsHost(name string) string {
	f := strings.Fields(name)
	if len(f) < 3 || len(f[2]) > 253 {
		return ""
	}
	for _, c := range []byte(f[2]) {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-'
		if !ok {
			return ""
		}
	}
	return f[2]
}

// Check is Env, then Probe, then Answer.Nested. It returns exactly one of a
// valid *Pass (nothing refused), a *Nested (the refusal) or an error (the
// terminal could not be asked).
//
// When Env refuses, the terminal is not touched. tty must be the terminal
// the client would draw on, in raw mode (without it the answers wait in the
// line discipline and are echoed); see Probe for the read deadline.
func Check(tty io.ReadWriter, env, dead []string, timeout time.Duration, trust bool) (*Pass, *Nested, error) {
	if n := Env(env, dead); n != nil {
		return nil, n, nil
	}
	a, err := Probe(tty, timeout)
	if err != nil {
		return nil, nil, err
	}
	if n := a.Nested(trust); n != nil {
		return nil, n, nil
	}
	// The only place in the tree that makes a valid Pass
	// (TestOnlyCheckMakesAValidPass).
	return &Pass{ok: true}, nil, nil
}
