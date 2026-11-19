package mux

// Where a command runs (5.3.10 of the plan). The types are a contract
// (6.2, P2-12); the body of Detect is written by P2-13.

type Where int

const (
	InDevtools Where = iota
	InTmux
	InHerdr
	InForeign  // screen or zellij
	Ambiguous  // several live marks, none matching this terminal
	InTerminal // no live mark; stdin and stdout are terminals
	NoTerminal // no live mark; not a terminal
)

type Context struct {
	Where   Where
	Foreign string    // "screen" or "zellij" when InForeign
	Marks   []Backend // the live marks when Ambiguous
	Dead    []string  // marker variables that are set but dead
}

// Inside reports the backend whose pane this is: a backend and true for
// InDevtools, InTmux and InHerdr, false for every other value.
func (c Context) Inside() (Backend, bool) {
	switch c.Where {
	case InDevtools:
		return Devtools, true
	case InTmux:
		return Tmux, true
	case InHerdr:
		return Herdr, true
	}
	return "", false
}

// Liveness is the answer of one check whether the multiplexer behind a
// mark is still there.
type Liveness int

const (
	Dead    Liveness = iota // the check finished and the answer is no
	Live                    // the check finished and the answer is yes
	Unknown                 // the check did not finish: a time limit or an unexpected error
)

type Facts struct {
	Getenv     func(string) string
	Terminal   bool                               // stdin and stdout are terminals
	HasTTY     bool                               // one of fds 0, 1, 2 is a terminal
	SameTTY    func(path string) bool             // one of fds 0, 1, 2 is the terminal device at path
	Devtools   func(socket, pane string) Liveness // the server at socket knows pane
	TmuxPane   func(tmuxVar, pane string) (tty string, l Liveness)
	TmuxAlive  func(tmuxVar string) Liveness
	HerdrAlive func(socket string) Liveness // something accepts a connection at socket
}

// The marker variables of the marks (the mark table of 5.3.10).
const (
	envDevtoolsPane   = "DEVTOOLS_PANE"
	envDevtoolsSocket = "DEVTOOLS_SOCKET"
	envDevtoolsTTY    = "DEVTOOLS_TTY"
	envTmux           = "TMUX"
	envTmuxPane       = "TMUX_PANE"
	envHerdr          = "HERDR_ENV"
	envHerdrSocket    = "HERDR_SOCKET_PATH"
	envScreen         = "STY"
	envZellij         = "ZELLIJ"
	envZellijSession  = "ZELLIJ_SESSION_NAME"
)

// mark is one multiplexer whose trace is in the environment and whose
// check did not answer Dead.
type mark struct {
	where   Where
	backend Backend // "" for a foreign mark
	foreign string  // "screen" or "zellij" for a foreign mark
	matches bool    // this terminal is the mark's pane
}

// Detect finds where a command runs. It is pure: it reads the environment
// and the machine only through f.
//
// It decides in four steps: (1) with f.HasTTY, a live mark whose pane is
// this terminal; (2) otherwise exactly one live mark, or InForeign for a
// foreign one; (3) with no live mark InTerminal or NoTerminal; (4) several
// live marks and none matching is Ambiguous. A mark is live unless its
// check answered Dead: Unknown, a check that did not finish, counts as
// live, so a slow or hung multiplexer is never treated as gone (N18). In
// every case Dead lists the marker variables that are set and whose check
// answered Dead; a mark that answered Unknown is never listed.
func Detect(f Facts) Context {
	getenv := f.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	sameTTY := func(path string) bool {
		return path != "" && f.SameTTY != nil && f.SameTTY(path)
	}

	var live []mark
	var dead []string

	// devtools: set by DEVTOOLS_PANE, checked by the server at
	// DEVTOOLS_SOCKET, matches when DEVTOOLS_TTY is this terminal.
	if pane := getenv(envDevtoolsPane); pane != "" {
		l := Unknown
		if f.Devtools != nil {
			l = f.Devtools(getenv(envDevtoolsSocket), pane)
		}
		if l == Dead {
			dead = append(dead, envDevtoolsPane)
		} else {
			live = append(live, mark{
				where:   InDevtools,
				backend: Devtools,
				matches: f.HasTTY && sameTTY(getenv(envDevtoolsTTY)),
			})
		}
	}

	// tmux: set by TMUX; checked with TMUX_PANE by TmuxPane, without it
	// (a popup) by TmuxAlive; matches when the pane's device is this
	// terminal, which only a TmuxPane that answered Live can tell.
	if tmuxVar := getenv(envTmux); tmuxVar != "" {
		l, matches := Unknown, false
		if pane := getenv(envTmuxPane); pane != "" {
			if f.TmuxPane != nil {
				var tty string
				tty, l = f.TmuxPane(tmuxVar, pane)
				matches = l == Live && f.HasTTY && sameTTY(tty)
			}
		} else if f.TmuxAlive != nil {
			l = f.TmuxAlive(tmuxVar)
		}
		if l == Dead {
			dead = append(dead, envTmux)
		} else {
			live = append(live, mark{where: InTmux, backend: Tmux, matches: matches})
		}
	}

	// herdr: set by HERDR_ENV, checked at HERDR_SOCKET_PATH, never matches.
	if getenv(envHerdr) != "" {
		l := Unknown
		if f.HerdrAlive != nil {
			l = f.HerdrAlive(getenv(envHerdrSocket))
		}
		if l == Dead {
			dead = append(dead, envHerdr)
		} else {
			live = append(live, mark{where: InHerdr, backend: Herdr})
		}
	}

	// screen and zellij: always live, never match. The two variables of
	// zellij are one mark.
	if getenv(envScreen) != "" {
		live = append(live, mark{where: InForeign, foreign: "screen"})
	}
	if getenv(envZellij) != "" || getenv(envZellijSession) != "" {
		live = append(live, mark{where: InForeign, foreign: "zellij"})
	}

	// Step 1: the mark whose pane is this terminal. A terminal device
	// belongs to one pane, so at most one matches; should the facts say
	// otherwise, the first in the order of the table wins.
	for _, m := range live {
		if m.matches {
			return m.context(dead)
		}
	}
	switch len(live) {
	case 1: // step 2
		return live[0].context(dead)
	case 0: // step 3
		if f.Terminal {
			return Context{Where: InTerminal, Dead: dead}
		}
		return Context{Where: NoTerminal, Dead: dead}
	}
	// Step 4: several live marks, none matching.
	var marks []Backend
	for _, m := range live {
		if m.backend != "" {
			marks = append(marks, m.backend)
		}
	}
	return Context{Where: Ambiguous, Marks: marks, Dead: dead}
}

func (m mark) context(dead []string) Context {
	return Context{Where: m.where, Foreign: m.foreign, Dead: dead}
}
