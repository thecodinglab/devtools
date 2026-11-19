package guard

import (
	"errors"
	"testing"
	"time"

	"devtools/internal/testkit"
)

func TestEnvRefusesEachMultiplexerVariable(t *testing.T) {
	cases := []struct{ name, where string }{
		{"DEVTOOLS_PANE", "devtools"},
		{"TMUX", "tmux"},
		{"STY", "screen"},
		{"ZELLIJ", "zellij"},
		{"ZELLIJ_SESSION_NAME", "zellij"},
		{"HERDR_ENV", "herdr"},
	}
	for _, c := range cases {
		for _, value := range []string{"1", "/tmp/x,1,0", ""} {
			env := testkit.CleanEnv("/tmp/home", c.name+"="+value)
			n := Env(env, nil)
			if n == nil {
				t.Errorf("Env with %s=%q passed, want a refusal", c.name, value)
				continue
			}
			if n.Where != c.where || n.Host != "" {
				t.Errorf("Env with %s=%q = %+v, want Where %q and no host", c.name, value, *n, c.where)
			}
		}
	}
	// A marker that appears twice still counts, whatever the last value is.
	if Env([]string{"TMUX=/tmp/s,1,0", "PATH=/bin", "TMUX="}, nil) == nil {
		t.Error("Env with TMUX listed twice passed")
	}
	// Several markers: the first of the fixed order is named.
	if n := Env([]string{"HERDR_ENV=1", "TMUX=x", "DEVTOOLS_PANE=p1"}, nil); n == nil || n.Where != "devtools" {
		t.Errorf("Env with three markers = %+v, want devtools", n)
	}
}

func TestEnvPassesACleanEnvironment(t *testing.T) {
	envs := [][]string{
		nil,
		{},
		testkit.CleanEnv("/tmp/home"),
		// Variables next to the markers that are no markers themselves.
		testkit.CleanEnv("/tmp/home",
			"TMUX_PANE=%0", "TMUX_TMPDIR=/tmp/t", "TERM=tmux-256color", "TERM_PROGRAM=tmux",
			"HERDR_SOCKET_PATH=/tmp/h.sock", "HERDR_PANE_ID=3", "DEVTOOLS_SOCKET=/tmp/d.sock",
			"DEVTOOLS_TTY=/dev/pts/3", "WINDOW=0", "NVIM=/tmp/nvim.sock", "DEVTOOLS_TRUST_TERMINAL=1",
			"MYTMUX=1", "tmux=1", "XTMUX=1"),
	}
	for _, env := range envs {
		if n := Env(env, nil); n != nil {
			t.Errorf("Env(%q) = %+v, want nil", env, *n)
		}
	}
}

func TestEnvIgnoresDeadMarkers(t *testing.T) {
	for _, m := range markers {
		env := testkit.CleanEnv("/tmp/home", m.name+"=x")
		if n := Env(env, []string{m.name}); n != nil {
			t.Errorf("Env with %s listed dead = %+v, want nil", m.name, *n)
		}
		// The same marker not listed refuses, also when every other marker is.
		var others []string
		for _, o := range markers {
			if o.name != m.name {
				others = append(others, o.name)
			}
		}
		if n := Env(env, others); n == nil || n.Where != m.where {
			t.Errorf("Env with %s set and the others dead = %+v, want %q", m.name, n, m.where)
		}
		// A name in dead that is no marker changes nothing.
		if n := Env(env, []string{"PATH", "TMUX_PANE", "", m.name + "=x", "tmux"}); n == nil || n.Where != m.where {
			t.Errorf("Env with %s set and unrelated dead names = %+v, want %q", m.name, n, m.where)
		}
		if n := Env(testkit.CleanEnv("/tmp/home"), []string{"PATH", "NOTHING"}); n != nil {
			t.Errorf("Env of a clean environment with unrelated dead names = %+v", *n)
		}
	}
	// A dead marker does not hide a live one.
	if n := Env([]string{"DEVTOOLS_PANE=p1", "TMUX=x"}, []string{"DEVTOOLS_PANE"}); n == nil || n.Where != "tmux" {
		t.Errorf("Env with a dead DEVTOOLS_PANE and a live TMUX = %+v, want tmux", n)
	}
}

func TestAZeroPassIsNotValid(t *testing.T) {
	var nilPass *Pass
	if nilPass.Valid() {
		t.Error("a nil *Pass is valid")
	}
	if (&Pass{}).Valid() {
		t.Error("&Pass{} is valid")
	}
	var zero Pass
	if zero.Valid() {
		t.Error("the zero Pass is valid")
	}
	if ErrNoPass == nil || ErrNoPass.Error() != "devtools: no guard pass" {
		t.Errorf("ErrNoPass = %v", ErrNoPass)
	}
}

// failingTerm fails every write.
type failingTerm struct{}

func (failingTerm) Read([]byte) (int, error)  { return 0, errors.New("read: no terminal") }
func (failingTerm) Write([]byte) (int, error) { return 0, errors.New("write: no terminal") }

func TestCheckGivesAPassOnlyWhenNothingRefused(t *testing.T) {
	clean := testkit.CleanEnv("/tmp/home")
	cases := []struct {
		name    string
		env     []string
		dead    []string
		tty     func(t *testing.T) *fakeTerm
		failing bool
		trust   bool
		want    string // "pass", "nested" or "error"
		where   string
		probed  bool // the terminal was asked
	}{
		{name: "a refusing environment", env: testkit.CleanEnv("/tmp/home", "TMUX=/tmp/s,1,0"),
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerTerminal}) },
			want: "nested", where: "tmux"},
		{name: "a refusing environment with trust", env: testkit.CleanEnv("/tmp/home", "STY=1.x"), trust: true,
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerTerminal}) },
			want: "nested", where: "screen"},
		{name: "a refusing answer", env: clean,
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerDevtools}) },
			want: "nested", where: "devtools", probed: true},
		{name: "a refusing answer behind a dead marker", env: testkit.CleanEnv("/tmp/home", "DEVTOOLS_PANE=p1"),
			dead: []string{"DEVTOOLS_PANE"},
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerDevtools}) },
			want: "nested", where: "devtools", probed: true},
		{name: "a silent terminal", env: clean, trust: true,
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, nil) },
			want: "nested", where: "unknown terminal", probed: true},
		{name: "a probe error", env: clean, failing: true, want: "error"},
		{name: "a probe error with trust", env: clean, failing: true, trust: true, want: "error"},
		{name: "a terminal that hangs up", env: clean,
			tty: func(t *testing.T) *fakeTerm {
				f := newFakeTerm(t, nil, []string{"\x1bP>|ghos"})
				f.eof = true
				return f
			},
			want: "error", probed: true},
		{name: "a clean run", env: clean,
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerTerminal}) },
			want: "pass", probed: true},
		{name: "a clean run behind a dead marker", env: testkit.CleanEnv("/tmp/home", "TMUX=/tmp/gone,1,0"),
			dead: []string{"TMUX"},
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerTerminal}) },
			want: "pass", probed: true},
		{name: "a DA1-only terminal with trust", env: clean, trust: true,
			tty:  func(t *testing.T) *fakeTerm { return newFakeTerm(t, nil, []string{answerScreen}) },
			want: "pass", probed: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tty *fakeTerm
			var pass *Pass
			var nested *Nested
			var err error
			if c.failing {
				pass, nested, err = Check(failingTerm{}, c.env, c.dead, 200*time.Millisecond, c.trust)
			} else {
				tty = c.tty(t)
				pass, nested, err = Check(tty, c.env, c.dead, 200*time.Millisecond, c.trust)
			}
			set := 0
			if pass != nil {
				set++
			}
			if nested != nil {
				set++
			}
			if err != nil {
				set++
			}
			if set != 1 {
				t.Fatalf("Check = %v, %v, %v: want exactly one result set", pass, nested, err)
			}
			switch c.want {
			case "pass":
				if !pass.Valid() {
					t.Errorf("Check gave %+v, %v, %v; want a valid pass", pass, nested, err)
				}
			case "nested":
				if nested == nil || nested.Where != c.where {
					t.Errorf("Check gave %v, %+v, %v; want Nested{Where: %q}", pass, nested, err, c.where)
				}
			case "error":
				if err == nil {
					t.Errorf("Check gave %v, %+v, nil; want an error", pass, nested)
				}
			}
			if pass != nil && c.want != "pass" {
				t.Errorf("a pass was returned for %s", c.name)
			}
			if tty != nil {
				written, _ := tty.Written()
				if probed := written != ""; probed != c.probed {
					t.Errorf("the terminal was asked: %v (wrote %q), want %v", probed, written, c.probed)
				}
			}
		})
	}
}

func TestEveryPassOfCheckIsValidAndNew(t *testing.T) {
	env := testkit.CleanEnv("/tmp/home")
	p1, _, err := Check(newFakeTerm(t, nil, []string{answerTerminal}), env, nil, time.Second, false)
	if err != nil || !p1.Valid() {
		t.Fatalf("first Check: %v, %v", p1, err)
	}
	p2, _, err := Check(newFakeTerm(t, nil, []string{answerTerminal}), env, nil, time.Second, false)
	if err != nil || !p2.Valid() {
		t.Fatalf("second Check: %v, %v", p2, err)
	}
	if p1 == p2 {
		t.Error("two Checks returned the same *Pass")
	}
}
