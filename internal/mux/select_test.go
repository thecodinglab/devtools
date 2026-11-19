package mux

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// The contexts Select can be given: every value of Where, with the fields
// Detect fills for it.
var everyContext = []Context{
	{Where: InDevtools},
	{Where: InTmux},
	{Where: InHerdr},
	{Where: InForeign, Foreign: "screen"},
	{Where: Ambiguous, Marks: []Backend{Devtools, Tmux}},
	{Where: InTerminal, Dead: []string{"TMUX"}},
	{Where: NoTerminal},
}

// The four environment and four config values of TestSelectIsTotal: unset,
// two names and a name Parse rejects. Between them they name every backend.
var (
	totalEnvValues    = []string{"", "devtools", "herdr", "screen"}
	totalConfigValues = []string{"", "tmux", "devtools", "zellij"}
)

// selectSafely calls Select and turns a panic into a test failure.
func selectSafely(t *testing.T, c Context, ch Choice, available func(Backend) error) (b Backend, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Select(%+v, %+v) panicked: %v", c, ch, r)
			b, err = "", fmt.Errorf("panic")
		}
	}()
	return Select(c, ch, available)
}

func TestSelectIsTotal(t *testing.T) {
	for _, c := range everyContext {
		inside, isInside := c.Inside()
		for _, e := range totalEnvValues {
			for _, cfg := range totalConfigValues {
				for _, a := range availabilitySets() {
					ch := Choice{Env: e, Config: cfg}
					where := fmt.Sprintf("Where %d, env %q, config %q, available %s", c.Where, e, cfg, a.name)
					b, err := selectSafely(t, c, ch, a.check)
					if err == nil {
						switch b {
						case Devtools, Tmux, Herdr:
						default:
							t.Errorf("%s: Select = %q with no error", where, b)
						}
						if !a.set[b] {
							t.Errorf("%s: Select = %s, which is not available", where, b)
						}
						if isInside && b != inside {
							t.Errorf("%s: Select = %s inside %s", where, b, inside)
						}
						if !isInside {
							want := e
							if want == "" {
								want = cfg
							}
							if want == "" {
								want = string(Default(func(x Backend) bool { return a.set[x] }))
							}
							if string(b) != want {
								t.Errorf("%s: Select = %s, want %s", where, b, want)
							}
						}
						continue
					}
					if b != "" {
						t.Errorf("%s: Select returned %s with its error %v", where, b, err)
					}
					var amb *ErrAmbiguous
					var mm *ErrMismatch
					var u *ErrUnavailable
					switch {
					case errors.As(err, &amb):
						if c.Where != Ambiguous || !slices.Equal(amb.Marks, c.Marks) {
							t.Errorf("%s: %v", where, err)
						}
					case errors.As(err, &mm):
						if !isInside || mm.Inside != inside || string(mm.Asked) != e || mm.Asked == inside {
							t.Errorf("%s: %+v", where, *mm)
						}
					case errors.As(err, &u):
						if a.set[u.Backend] || u.Reason == "" {
							t.Errorf("%s: %+v for an available backend or without a reason", where, *u)
						}
						if isInside && u.Backend != inside {
							t.Errorf("%s: unavailable %s inside %s", where, u.Backend, inside)
						}
					case sourceError(err):
						if c.Where == Ambiguous {
							t.Errorf("%s: a parse error before the ambiguity: %v", where, err)
						}
					default:
						t.Errorf("%s: Select = %v, not one of the typed errors", where, err)
					}
				}
			}
		}
	}

	// A nil availability check is a check that fails, not a panic.
	if b, err := selectSafely(t, Context{Where: InTerminal}, Choice{Env: "tmux"}, nil); err == nil {
		t.Errorf("Select with a nil check = %s, want an error", b)
	} else if u, ok := err.(*ErrUnavailable); !ok || u.Backend != Tmux {
		t.Errorf("Select with a nil check = %v, want *ErrUnavailable for tmux", err)
	}
	if _, err := selectSafely(t, Context{Where: InDevtools}, Choice{}, nil); err == nil {
		t.Errorf("Select inside devtools with a nil check gave no error")
	}
	if _, err := selectSafely(t, Context{Where: NoTerminal}, Choice{}, nil); err == nil {
		t.Errorf("Select with a nil check and no choice gave no error")
	}
}

func TestSelectNamesTheSourceOfARejectedName(t *testing.T) {
	all := func(Backend) error { return nil }
	for _, tt := range []struct {
		c    Context
		ch   Choice
		want string
	}{
		{Context{Where: InTerminal}, Choice{Env: "screen"}, `DEVTOOLS_MULTIPLEXER: unknown multiplexer "screen", want devtools, tmux or herdr`},
		{Context{Where: InTerminal}, Choice{Env: "screen", Config: "tmux"}, `DEVTOOLS_MULTIPLEXER: unknown multiplexer "screen", want devtools, tmux or herdr`},
		{Context{Where: NoTerminal}, Choice{Config: "zellij"}, `config multiplexer: unknown multiplexer "zellij", want devtools, tmux or herdr`},
		{Context{Where: InForeign, Foreign: "screen"}, Choice{Config: "Tmux"}, `config multiplexer: unknown multiplexer "Tmux", want devtools, tmux or herdr`},
		{Context{Where: InTmux}, Choice{Env: "screen"}, `DEVTOOLS_MULTIPLEXER: unknown multiplexer "screen", want devtools, tmux or herdr`},
	} {
		b, err := Select(tt.c, tt.ch, all)
		if err == nil || err.Error() != tt.want {
			t.Errorf("Select(%+v, %+v) = %q, %v; want the error %q", tt.c, tt.ch, b, err, tt.want)
		}
	}
	// Inside a backend the config is not read, so a bad name there is no
	// error.
	if b, err := Select(Context{Where: InHerdr}, Choice{Config: "zellij"}, all); err != nil || b != Herdr {
		t.Errorf("Select inside herdr with a bad config = %q, %v; want herdr", b, err)
	}
}

func TestSelectInATerminalTakesTheVariableThenTheConfig(t *testing.T) {
	all := func(Backend) error { return nil }
	for _, c := range []Context{{Where: InTerminal}, {Where: NoTerminal}, {Where: InForeign, Foreign: "zellij"}} {
		for _, tt := range []struct {
			ch   Choice
			want Backend
		}{
			{Choice{Env: "devtools"}, Devtools},
			{Choice{Env: "tmux", Config: "herdr"}, Tmux},
			{Choice{Env: "herdr", Config: "devtools"}, Herdr},
			{Choice{Config: "devtools"}, Devtools},
			{Choice{Config: "tmux"}, Tmux},
			{Choice{Config: "herdr"}, Herdr},
		} {
			if b, err := Select(c, tt.ch, all); err != nil || b != tt.want {
				t.Errorf("Select(%+v, %+v) = %q, %v; want %s", c, tt.ch, b, err, tt.want)
			}
		}
	}
}

func TestSelectOfAnAmbiguousContextNamesTheMarks(t *testing.T) {
	marks := []Backend{Devtools, Tmux}
	c := Context{Where: Ambiguous, Marks: marks}
	_, err := Select(c, Choice{Env: "tmux", Config: "tmux"}, func(Backend) error { return nil })
	amb, ok := err.(*ErrAmbiguous)
	if !ok || !slices.Equal(amb.Marks, marks) {
		t.Fatalf("Select(Ambiguous) = %v, want *ErrAmbiguous with %v", err, marks)
	}
	amb.Marks[0] = Herdr
	if marks[0] != Devtools {
		t.Fatalf("the error shares its marks with the context")
	}
}

func TestNoInputSelectsABackendOtherThanTheSurroundingOne(t *testing.T) {
	values := append(slices.Clone(envValues), "Tmux", " devtools")
	for _, c := range everyContext {
		inside, ok := c.Inside()
		if !ok {
			continue
		}
		for _, e := range values {
			for _, cfg := range values {
				for _, a := range availabilitySets() {
					b, err := Select(c, Choice{Env: e, Config: cfg}, a.check)
					if err == nil && b != inside {
						t.Errorf("inside %s, env %q, config %q, available %s: Select = %s", inside, e, cfg, a.name, b)
					}
					if err != nil && b != "" {
						t.Errorf("inside %s: Select returned %s with its error %v", inside, b, err)
					}
					var mm *ErrMismatch
					if errors.As(err, &mm) && (mm.Inside != inside || string(mm.Asked) != e) {
						t.Errorf("inside %s, env %q: %+v", inside, e, *mm)
					}
					var u *ErrUnavailable
					if errors.As(err, &u) && u.Backend != inside {
						t.Errorf("inside %s: unavailable %s", inside, u.Backend)
					}
				}
			}
		}

		// The variable naming another backend is a mismatch; the config
		// naming another backend is not read.
		all := func(Backend) error { return nil }
		for _, other := range []Backend{Devtools, Tmux, Herdr} {
			if other == inside {
				continue
			}
			_, err := Select(c, Choice{Env: string(other)}, all)
			if mm, ok := err.(*ErrMismatch); !ok || mm.Asked != other || mm.Inside != inside {
				t.Errorf("inside %s, DEVTOOLS_MULTIPLEXER=%s: %v, want *ErrMismatch", inside, other, err)
			}
			if b, err := Select(c, Choice{Config: string(other)}, all); err != nil || b != inside {
				t.Errorf("inside %s, multiplexer %s in the config: %q, %v; want %s", inside, other, b, err, inside)
			}
		}
		if b, err := Select(c, Choice{Env: string(inside), Config: "zellij"}, all); err != nil || b != inside {
			t.Errorf("inside %s, the variable naming it: %q, %v", inside, b, err)
		}
	}
}

func TestAnUnavailableBackendIsAnErrorNotAFallback(t *testing.T) {
	backends := []Backend{Devtools, Tmux, Herdr}
	for _, named := range backends {
		// Everything else is installed; only the named backend fails.
		check := func(b Backend) error {
			if b == named {
				return fmt.Errorf("%s does not answer", b)
			}
			return nil
		}
		cases := []struct {
			how string
			c   Context
			ch  Choice
		}{
			{"by the variable in a terminal", Context{Where: InTerminal}, Choice{Env: string(named)}},
			{"by the variable without a terminal", Context{Where: NoTerminal}, Choice{Env: string(named), Config: "tmux"}},
			{"by the config in a terminal", Context{Where: InTerminal}, Choice{Config: string(named)}},
			{"by the config in screen", Context{Where: InForeign, Foreign: "screen"}, Choice{Config: string(named)}},
		}
		inside := map[Backend]Where{Devtools: InDevtools, Tmux: InTmux, Herdr: InHerdr}[named]
		for _, e := range []string{"", string(named)} {
			for _, cfg := range []string{"", "devtools", "tmux", "herdr"} {
				cases = append(cases, struct {
					how string
					c   Context
					ch  Choice
				}{fmt.Sprintf("by its pane, env %q, config %q", e, cfg), Context{Where: inside}, Choice{Env: e, Config: cfg}})
			}
		}
		for _, tt := range cases {
			b, err := Select(tt.c, tt.ch, check)
			u, ok := err.(*ErrUnavailable)
			if !ok {
				t.Errorf("%s named %s: Select = %q, %v; want *ErrUnavailable", named, tt.how, b, err)
				continue
			}
			if u.Backend != named || b != "" {
				t.Errorf("%s named %s: %+v and %q", named, tt.how, *u, b)
			}
			if u.Reason != fmt.Sprintf("%s does not answer", named) {
				t.Errorf("%s named %s: Reason = %q, want the text of the check", named, tt.how, u.Reason)
			}
		}
	}

	// Nothing is installed and nothing names a backend: the default is
	// unavailable and nothing else is tried.
	var asked []Backend
	none := func(b Backend) error {
		asked = append(asked, b)
		return errors.New("not installed")
	}
	_, err := Select(Context{Where: InTerminal}, Choice{}, none)
	u, ok := err.(*ErrUnavailable)
	if !ok {
		t.Fatalf("Select with nothing installed = %v, want *ErrUnavailable", err)
	}
	if want := Default(func(Backend) bool { return false }); u.Backend != want {
		t.Fatalf("Select with nothing installed: unavailable %s, want the default %s", u.Backend, want)
	}
	if asked[len(asked)-1] != u.Backend {
		t.Fatalf("the last backend asked is %s, want the one reported %s", asked[len(asked)-1], u.Backend)
	}
}

func TestDefaultBeforeAndAfterTheCutover(t *testing.T) {
	installed := func(set ...Backend) func(Backend) bool {
		return func(b Backend) bool { return slices.Contains(set, b) }
	}
	for _, tt := range []struct {
		name      string
		installed func(Backend) bool
		before    Backend
	}{
		{"herdr and tmux installed", installed(Herdr, Tmux), Herdr},
		{"herdr only", installed(Herdr), Herdr},
		{"every backend", installed(Devtools, Tmux, Herdr), Herdr},
		{"tmux only", installed(Tmux), Tmux},
		{"devtools and tmux", installed(Devtools, Tmux), Tmux},
		{"nothing installed", installed(), Tmux},
		{"no check", nil, Tmux},
	} {
		if got := defaultRule(false, tt.installed); got != tt.before {
			t.Errorf("before the cutover, %s: %s, want %s", tt.name, got, tt.before)
		}
		if got := defaultRule(true, tt.installed); got != Devtools {
			t.Errorf("after the cutover, %s: %s, want devtools", tt.name, got)
		}
	}
}

// The four cases of today's TestSelect (internal/mux/mux_test.go:25, :27,
// :30, :31 on main) whose results revision 2 changes, with their new
// results; the other cases of that test are rows of the tests above.
// P2-08a moved them here when it deleted legacy.go and legacy_test.go.
func TestSelectTheFourCasesTodaysSelectorDecidedOtherwise(t *testing.T) {
	installed := func(set ...Backend) func(Backend) error {
		return func(b Backend) error {
			if slices.Contains(set, b) {
				return nil
			}
			return fmt.Errorf("%s is not installed", b)
		}
	}
	tmuxAndHerdr := installed(Tmux, Herdr)

	// :25 "tmux nested in herdr" was tmux by the order of two ifs. Now
	// Detect decides: tmux when the tmux pane is this terminal, else both
	// marks are live and the context is ambiguous.
	t.Run("tmux nested in herdr", func(t *testing.T) {
		w := &world{env: env(tmuxEnv, herdrEnv), terminal: true, hasTTY: true, tmuxPane: Live, herdr: Live}
		w.tmuxTTY = ourTTY
		if b, err := Select(Detect(w.facts()), Choice{}, tmuxAndHerdr); err != nil || b != Tmux {
			t.Errorf("the tmux pane is this terminal: Select = %q, %v; want tmux", b, err)
		}
		w.tmuxTTY = "/dev/pts/9"
		_, err := Select(Detect(w.facts()), Choice{}, tmuxAndHerdr)
		if amb, ok := err.(*ErrAmbiguous); !ok || !slices.Equal(amb.Marks, []Backend{Tmux, Herdr}) {
			t.Errorf("no mark matches: Select = %v, want *ErrAmbiguous with tmux and herdr", err)
		}
	})

	// :27 "env override beats tmux" was herdr. Inside tmux the variable
	// naming another backend is a mismatch.
	t.Run("the variable naming herdr inside tmux", func(t *testing.T) {
		_, err := Select(Context{Where: InTmux}, Choice{Env: "herdr"}, tmuxAndHerdr)
		if mm, ok := err.(*ErrMismatch); !ok || mm.Asked != Herdr || mm.Inside != Tmux {
			t.Errorf("Select = %v, want *ErrMismatch{herdr, tmux}", err)
		}
	})

	// :30 "config override beats tmux" was herdr. Inside tmux the config
	// line is not read.
	t.Run("the config naming herdr inside tmux", func(t *testing.T) {
		if b, err := Select(Context{Where: InTmux}, Choice{Config: "herdr"}, tmuxAndHerdr); err != nil || b != Tmux {
			t.Errorf("Select = %q, %v; want tmux", b, err)
		}
	})

	// :31 "explicit herdr even if missing" was herdr. A backend that is not
	// installed is an error, never another backend.
	t.Run("the variable naming a missing herdr", func(t *testing.T) {
		b, err := Select(Context{Where: InTerminal}, Choice{Env: "herdr"}, installed(Tmux))
		if u, ok := err.(*ErrUnavailable); !ok || u.Backend != Herdr || b != "" {
			t.Errorf("Select = %q, %v; want *ErrUnavailable for herdr", b, err)
		}
	})
}
