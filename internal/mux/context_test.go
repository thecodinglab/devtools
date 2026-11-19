package mux

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestMain keeps the tests hermetic. The tests of Detect and Select read
// nothing of the process: their Facts are built from maps and closures.
// The variables are removed for legacy_test.go, which sets the ones it
// reads itself, so that a multiplexer of the developer cannot reach it.
func TestMain(m *testing.M) {
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_MULTIPLEXER",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_BIN_PATH",
		"STY", "ZELLIJ", "ZELLIJ_SESSION_NAME",
	} {
		os.Unsetenv(name)
	}
	os.Exit(m.Run())
}

// ourTTY is the terminal device of the fake process: SameTTY of it is true.
const ourTTY = "/dev/pts/7"

// world is the machine as a test describes it. Each check answers from
// its field; a check is recorded with its arguments so a test can see what
// Detect asked.
type world struct {
	env      map[string]string
	terminal bool // stdin and stdout are terminals
	hasTTY   bool // one of fds 0, 1, 2 is a terminal

	devtools  Liveness
	tmuxTTY   string // the device TmuxPane returns
	tmuxPane  Liveness
	tmuxAlive Liveness
	herdr     Liveness

	calls []string
}

func (w *world) facts() Facts {
	return Facts{
		Getenv:   func(name string) string { return w.env[name] },
		Terminal: w.terminal,
		HasTTY:   w.hasTTY,
		SameTTY: func(path string) bool {
			w.calls = append(w.calls, "SameTTY "+path)
			return path == ourTTY
		},
		Devtools: func(socket, pane string) Liveness {
			w.calls = append(w.calls, "Devtools "+socket+" "+pane)
			return w.devtools
		},
		TmuxPane: func(tmuxVar, pane string) (string, Liveness) {
			w.calls = append(w.calls, "TmuxPane "+tmuxVar+" "+pane)
			return w.tmuxTTY, w.tmuxPane
		},
		TmuxAlive: func(tmuxVar string) Liveness {
			w.calls = append(w.calls, "TmuxAlive "+tmuxVar)
			return w.tmuxAlive
		},
		HerdrAlive: func(socket string) Liveness {
			w.calls = append(w.calls, "HerdrAlive "+socket)
			return w.herdr
		},
	}
}

// The environments of the marks.
var (
	devtoolsEnv = map[string]string{
		"DEVTOOLS_PANE":   "p3",
		"DEVTOOLS_SOCKET": "/tmp/devtools-1000/server.sock",
		"DEVTOOLS_TTY":    "/dev/pts/2",
	}
	tmuxEnv      = map[string]string{"TMUX": "/tmp/tmux-1000/default,4242,0", "TMUX_PANE": "%5"}
	tmuxPopupEnv = map[string]string{"TMUX": "/tmp/tmux-1000/default,4242,0"}
	herdrEnv     = map[string]string{"HERDR_ENV": "1", "HERDR_SOCKET_PATH": "/tmp/herdr.sock"}
)

func env(parts ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, p := range parts {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

func equalContext(a, b Context) bool {
	return a.Where == b.Where && a.Foreign == b.Foreign &&
		slices.Equal(a.Marks, b.Marks) && slices.Equal(a.Dead, b.Dead)
}

func TestDetect(t *testing.T) {
	devtoolsHere := env(devtoolsEnv, map[string]string{"DEVTOOLS_TTY": ourTTY})
	tests := []struct {
		name  string
		world world
		want  Context
	}{
		// Step 1: the mark whose pane is this terminal wins.
		{
			name:  "a devtools pane that is this terminal wins over a live tmux mark",
			world: world{env: env(devtoolsHere, tmuxEnv), terminal: true, hasTTY: true, devtools: Live, tmuxTTY: "/dev/pts/1", tmuxPane: Live},
			want:  Context{Where: InDevtools},
		},
		{
			name:  "tmux started by hand inside a devtools pane, the tmux pane matching, is tmux",
			world: world{env: env(devtoolsEnv, tmuxEnv), terminal: true, hasTTY: true, devtools: Live, tmuxTTY: ourTTY, tmuxPane: Live},
			want:  Context{Where: InTmux},
		},
		{
			name:  "a live herdr mark beside a matching tmux mark is tmux",
			world: world{env: env(herdrEnv, tmuxEnv), terminal: true, hasTTY: true, herdr: Live, tmuxTTY: ourTTY, tmuxPane: Live},
			want:  Context{Where: InTmux},
		},
		{
			name:  "a matching tmux pane beside a dead devtools mark is tmux and lists the dead variable",
			world: world{env: env(devtoolsEnv, tmuxEnv), terminal: true, hasTTY: true, devtools: Dead, tmuxTTY: ourTTY, tmuxPane: Live},
			want:  Context{Where: InTmux, Dead: []string{"DEVTOOLS_PANE"}},
		},
		{
			name:  "without a terminal on fds 0 to 2 nothing matches and two live marks are ambiguous",
			world: world{env: env(devtoolsHere, tmuxEnv), devtools: Live, tmuxTTY: ourTTY, tmuxPane: Live},
			want:  Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}},
		},
		{
			name:  "a tmux pane whose check did not finish does not match even with this terminal's device",
			world: world{env: env(devtoolsEnv, tmuxEnv), terminal: true, hasTTY: true, devtools: Live, tmuxTTY: ourTTY, tmuxPane: Unknown},
			want:  Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}},
		},

		// Step 2: exactly one live mark.
		{
			name:  "an editor's terminal inside a tmux pane is tmux by the single live mark",
			world: world{env: tmuxEnv, terminal: true, hasTTY: true, tmuxTTY: "/dev/pts/3", tmuxPane: Live},
			want:  Context{Where: InTmux},
		},
		{
			name:  "an editor's terminal inside a devtools pane is devtools by the single live mark",
			world: world{env: devtoolsEnv, terminal: true, hasTTY: true, devtools: Live},
			want:  Context{Where: InDevtools},
		},
		{
			name:  "a tmux popup, TMUX live and no TMUX_PANE, is tmux",
			world: world{env: tmuxPopupEnv, terminal: true, hasTTY: true, tmuxAlive: Live},
			want:  Context{Where: InTmux},
		},
		{
			name:  "a tmux popup inside a devtools pane whose server is gone is tmux",
			world: world{env: env(devtoolsEnv, tmuxPopupEnv), terminal: true, hasTTY: true, devtools: Dead, tmuxAlive: Live},
			want:  Context{Where: InTmux, Dead: []string{"DEVTOOLS_PANE"}},
		},
		{
			name:  "a herdr pane is herdr",
			world: world{env: herdrEnv, terminal: true, hasTTY: true, herdr: Live},
			want:  Context{Where: InHerdr},
		},
		{
			name:  "a single live mark counts without a terminal",
			world: world{env: tmuxEnv, tmuxTTY: "/dev/pts/3", tmuxPane: Live},
			want:  Context{Where: InTmux},
		},
		{
			name:  "STY alone is InForeign with its name",
			world: world{env: map[string]string{"STY": "1234.pts-1.host"}, terminal: true, hasTTY: true},
			want:  Context{Where: InForeign, Foreign: "screen"},
		},
		{
			name:  "ZELLIJ alone is InForeign zellij",
			world: world{env: map[string]string{"ZELLIJ": "0"}, terminal: true, hasTTY: true},
			want:  Context{Where: InForeign, Foreign: "zellij"},
		},
		{
			name:  "ZELLIJ_SESSION_NAME alone is InForeign zellij",
			world: world{env: map[string]string{"ZELLIJ_SESSION_NAME": "main"}, terminal: true, hasTTY: true},
			want:  Context{Where: InForeign, Foreign: "zellij"},
		},
		{
			name:  "the two variables of zellij are one mark",
			world: world{env: map[string]string{"ZELLIJ": "0", "ZELLIJ_SESSION_NAME": "main"}, terminal: true, hasTTY: true},
			want:  Context{Where: InForeign, Foreign: "zellij"},
		},
		{
			name:  "screen beside a dead tmux mark is screen",
			world: world{env: env(tmuxEnv, map[string]string{"STY": "1234.pts-1.host"}), terminal: true, hasTTY: true, tmuxPane: Dead},
			want:  Context{Where: InForeign, Foreign: "screen", Dead: []string{"TMUX"}},
		},

		// Step 3: no live mark.
		{
			name:  "TMUX of a server that is gone gives InTerminal with Dead TMUX",
			world: world{env: tmuxEnv, terminal: true, hasTTY: true, tmuxPane: Dead},
			want:  Context{Where: InTerminal, Dead: []string{"TMUX"}},
		},
		{
			name:  "a tmux popup whose server is gone gives InTerminal with Dead TMUX",
			world: world{env: tmuxPopupEnv, terminal: true, hasTTY: true, tmuxAlive: Dead},
			want:  Context{Where: InTerminal, Dead: []string{"TMUX"}},
		},
		{
			name:  "every dead mark is listed in the order of the mark table",
			world: world{env: env(devtoolsEnv, tmuxEnv, herdrEnv), devtools: Dead, tmuxPane: Dead, herdr: Dead},
			want:  Context{Where: NoTerminal, Dead: []string{"DEVTOOLS_PANE", "TMUX", "HERDR_ENV"}},
		},
		{
			name:  "a plain terminal is InTerminal",
			world: world{env: map[string]string{}, terminal: true, hasTTY: true},
			want:  Context{Where: InTerminal},
		},
		{
			name:  "a terminal on stderr only is not InTerminal",
			world: world{env: map[string]string{}, hasTTY: true},
			want:  Context{Where: NoTerminal},
		},
		{
			name:  "no terminal and no mark is NoTerminal",
			world: world{env: map[string]string{}},
			want:  Context{Where: NoTerminal},
		},
		{
			name:  "an empty variable is not a mark",
			world: world{env: map[string]string{"DEVTOOLS_PANE": "", "TMUX": "", "HERDR_ENV": "", "STY": "", "ZELLIJ": "", "ZELLIJ_SESSION_NAME": ""}, terminal: true, hasTTY: true, devtools: Live, tmuxPane: Live, tmuxAlive: Live, herdr: Live},
			want:  Context{Where: InTerminal},
		},

		// Step 4: several live marks, none matching.
		{
			name:  "two live marks and no match are Ambiguous",
			world: world{env: env(devtoolsEnv, tmuxEnv), terminal: true, hasTTY: true, devtools: Live, tmuxTTY: "/dev/pts/1", tmuxPane: Live},
			want:  Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}},
		},
		{
			name:  "three live marks and no match are Ambiguous with all three",
			world: world{env: env(devtoolsEnv, tmuxPopupEnv, herdrEnv), terminal: true, hasTTY: true, devtools: Live, tmuxAlive: Live, herdr: Live},
			want:  Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux, Herdr}},
		},
		{
			name:  "a live herdr mark beside screen is Ambiguous and lists the backend",
			world: world{env: env(herdrEnv, map[string]string{"STY": "1234.pts-1.host"}), terminal: true, hasTTY: true, herdr: Live},
			want:  Context{Where: Ambiguous, Marks: []Backend{Herdr}},
		},
		{
			name:  "a dead mark is listed beside an ambiguous pair",
			world: world{env: env(devtoolsEnv, tmuxEnv, herdrEnv), terminal: true, hasTTY: true, devtools: Live, tmuxTTY: "/dev/pts/1", tmuxPane: Live, herdr: Dead},
			want:  Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}, Dead: []string{"HERDR_ENV"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := tt.world
			got := Detect(w.facts())
			if !equalContext(got, tt.want) {
				t.Fatalf("Detect() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetectAsksEachCheckWithItsVariables(t *testing.T) {
	w := world{env: env(devtoolsEnv, tmuxEnv, herdrEnv), terminal: true, hasTTY: true, devtools: Live, tmuxTTY: "/dev/pts/1", tmuxPane: Live, herdr: Live}
	Detect(w.facts())
	for _, want := range []string{
		"Devtools /tmp/devtools-1000/server.sock p3",
		"SameTTY /dev/pts/2",
		"TmuxPane /tmp/tmux-1000/default,4242,0 %5",
		"SameTTY /dev/pts/1",
		"HerdrAlive /tmp/herdr.sock",
	} {
		if !slices.Contains(w.calls, want) {
			t.Errorf("Detect did not ask %q; it asked %q", want, w.calls)
		}
	}
	if slices.ContainsFunc(w.calls, func(c string) bool { return strings.HasPrefix(c, "TmuxAlive") }) {
		t.Errorf("Detect asked TmuxAlive with TMUX_PANE set: %q", w.calls)
	}

	popup := world{env: tmuxPopupEnv, terminal: true, hasTTY: true, tmuxAlive: Live}
	Detect(popup.facts())
	if !slices.Equal(popup.calls, []string{"TmuxAlive /tmp/tmux-1000/default,4242,0"}) {
		t.Errorf("a popup asked %q, want only TmuxAlive", popup.calls)
	}

	plain := world{env: map[string]string{}, terminal: true, hasTTY: true}
	Detect(plain.facts())
	if len(plain.calls) != 0 {
		t.Errorf("a plain terminal asked %q, want nothing", plain.calls)
	}
}

func TestDetectWithNilChecksCountsEveryMarkAsLive(t *testing.T) {
	// A check that is missing cannot say that a multiplexer is gone, so it
	// is treated as one that did not answer.
	e := env(devtoolsEnv, tmuxEnv)
	got := Detect(Facts{Getenv: func(n string) string { return e[n] }, Terminal: true, HasTTY: true})
	want := Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}}
	if !equalContext(got, want) {
		t.Fatalf("Detect() = %+v, want %+v", got, want)
	}
	if got := Detect(Facts{}); !equalContext(got, Context{Where: NoTerminal}) {
		t.Fatalf("Detect(Facts{}) = %+v, want NoTerminal", got)
	}
}

// The values TestAnUnansweredMarkCountsAsLive and TestSelectIsTotal try.
var (
	envValues    = []string{"", "devtools", "tmux", "herdr", "screen"}
	configValues = []string{"", "devtools", "tmux", "herdr", "screen"}
)

// availabilitySets returns the eight sets of available backends as checks.
func availabilitySets() []struct {
	name  string
	check func(Backend) error
	set   map[Backend]bool
} {
	var out []struct {
		name  string
		check func(Backend) error
		set   map[Backend]bool
	}
	all := []Backend{Devtools, Tmux, Herdr}
	for bits := 0; bits < 8; bits++ {
		set := map[Backend]bool{}
		var names []string
		for i, b := range all {
			if bits&(1<<i) != 0 {
				set[b] = true
				names = append(names, string(b))
			}
		}
		name := strings.Join(names, "+")
		if name == "" {
			name = "none"
		}
		out = append(out, struct {
			name  string
			check func(Backend) error
			set   map[Backend]bool
		}{name, func(b Backend) error {
			if set[b] {
				return nil
			}
			return errors.New(string(b) + " is not installed")
		}, set})
	}
	return out
}

// sourceError reports whether err is the error of a name Parse rejects,
// which says which source held it.
func sourceError(err error) bool {
	s := err.Error()
	return strings.HasPrefix(s, "DEVTOOLS_MULTIPLEXER: ") || strings.HasPrefix(s, "config multiplexer: ")
}

func TestAnUnansweredMarkCountsAsLive(t *testing.T) {
	marks := []struct {
		name     string
		world    world
		where    Where
		backend  Backend
		variable string
	}{
		{"devtools", world{env: devtoolsEnv, devtools: Unknown}, InDevtools, Devtools, "DEVTOOLS_PANE"},
		{"tmux pane", world{env: tmuxEnv, tmuxTTY: "", tmuxPane: Unknown}, InTmux, Tmux, "TMUX"},
		{"tmux popup", world{env: tmuxPopupEnv, tmuxAlive: Unknown}, InTmux, Tmux, "TMUX"},
		{"herdr", world{env: herdrEnv, herdr: Unknown}, InHerdr, Herdr, "HERDR_ENV"},
	}
	for _, m := range marks {
		for _, terminal := range []bool{false, true} {
			w := m.world
			w.terminal, w.hasTTY = terminal, terminal
			c := Detect(w.facts())
			if c.Where != m.where {
				t.Errorf("%s, terminal %v: Where = %v, want %v", m.name, terminal, c.Where, m.where)
				continue
			}
			if slices.Contains(c.Dead, m.variable) || len(c.Dead) != 0 {
				t.Errorf("%s, terminal %v: Dead = %v, want empty", m.name, terminal, c.Dead)
			}
			for _, e := range envValues {
				for _, cfg := range configValues {
					for _, a := range availabilitySets() {
						b, err := Select(c, Choice{Env: e, Config: cfg}, a.check)
						if err == nil {
							if b != m.backend {
								t.Errorf("%s: Select(env %q, config %q, %s) = %s, want %s", m.name, e, cfg, a.name, b, m.backend)
							}
							continue
						}
						if b != "" {
							t.Errorf("%s: Select returned %s with its error %v", m.name, b, err)
						}
						var mm *ErrMismatch
						var u *ErrUnavailable
						switch {
						case errors.As(err, &mm):
							if mm.Inside != m.backend {
								t.Errorf("%s: mismatch inside %s, want %s", m.name, mm.Inside, m.backend)
							}
						case errors.As(err, &u):
							if u.Backend != m.backend {
								t.Errorf("%s: unavailable %s, want %s", m.name, u.Backend, m.backend)
							}
						case sourceError(err):
						default:
							t.Errorf("%s: Select(env %q, config %q, %s) = %v, not a typed error", m.name, e, cfg, a.name, err)
						}
					}
				}
			}
		}
	}

	// Beside a second mark that is live, an unanswered mark still counts:
	// the context is ambiguous, never the other backend.
	w := world{env: env(devtoolsEnv, tmuxEnv), terminal: true, hasTTY: true, devtools: Unknown, tmuxTTY: "/dev/pts/1", tmuxPane: Live}
	if c := Detect(w.facts()); !equalContext(c, Context{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}}) {
		t.Errorf("an unanswered devtools mark beside live tmux: %+v, want Ambiguous", c)
	}
}
