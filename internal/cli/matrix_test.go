package cli

import (
	"slices"
	"strings"
	"testing"

	"devtools/internal/mux"
	"devtools/internal/ui"
)

// The answers of the probe for the matrix (3.4 c). They are the forms of
// 3.4 and of the tests of internal/client/guard, not recordings.
//
// provisional E6: what a devtools pane reached over ssh and a herdr pane
// answer is UNVERIFIED until the rows of E6 that need the user (pending
// [GO]); the fixtures follow 3.4 and 3.10.5.
const (
	devtoolsPaneAnswer = "\x1bP>|devtools 1.0 macmini\x1b\\\x1b[?62;22c"
	libghosttyAnswer   = "\x1bP>|libghostty\x1b\\\x1b[?62;22c"
)

// backends are the three columns of the matrix and of the feature test.
var backends = []mux.Backend{mux.Devtools, mux.Tmux, mux.Herdr}

// serverWrites are the requests of the scripted server other than the
// state.get of the liveness check of a devtools mark.
func (w *world) serverWrites() []string {
	var out []string
	for _, r := range w.server.Requests() {
		if r != "state.get {}" {
			out = append(out, r)
		}
	}
	return out
}

// started reports whether a client of any backend started.
func (w *world) started() bool {
	for _, c := range w.tmux() {
		if strings.HasPrefix(c, "new-session -A") || strings.HasPrefix(c, "attach-session") {
			return true
		}
	}
	return strings.Contains(w.herdr(), "<attach>") || len(w.rec.Enters()) > 0
}

// nothingRan fails the test unless no fake was asked to do anything (the
// liveness checks, which are reads, aside).
func (w *world) nothingRan() {
	w.t.Helper()
	if got := w.tmux(); len(got) != 0 {
		w.t.Errorf("the fake tmux ran %q", got)
	}
	if got := w.herdrWrites(); len(got) != 0 {
		w.t.Errorf("the fake herdr ran %q", got)
	}
	if got := w.serverWrites(); len(got) != 0 {
		w.t.Errorf("the server was asked %q", got)
	}
	if got := w.rec.Enters(); len(got) != 0 {
		w.t.Errorf("the devtools client started: %q", got)
	}
}

func wantResult(t *testing.T, r result, code int, stderr string) {
	t.Helper()
	if r.code != code || strings.TrimSpace(r.stderr) != stderr {
		t.Fatalf("status %d, stderr %q; want %d and %q", r.code, r.stderr, code, stderr)
	}
}

func wantStatus(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("status %d, stderr %q; want %d", r.code, r.stderr, code)
	}
}

func wantContains(t *testing.T, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to hold %q", what, got, want)
	}
}

// switchSmall is what `devtools switch small` opens.
func (w *world) switchSmall() (dir, name string) { return w.project, "small-main" }

// The cells of the matrix, by what they assert.

func (w *world) wantOpenedOnly(t *testing.T, b mux.Backend, r result, words string) {
	t.Helper()
	dir, name := w.switchSmall()
	switch b {
	case mux.Devtools:
		wantResult(t, r, 0, words)
		if got := w.serverWrites(); !slices.Equal(got, []string{`session.open {"dir":"` + dir + `","name":"` + name + `"}`}) {
			t.Fatalf("requests = %q, want one session.open", got)
		}
	case mux.Tmux:
		wantResult(t, r, 0, words)
		if got := w.tmux(); !slices.Equal(got, []string{"has-session -t " + name, "new-session -d -s " + name + " -c " + dir}) {
			t.Fatalf("tmux = %q, want the session created detached", got)
		}
	case mux.Herdr:
		wantStatus(t, r, 1)
		wantContains(t, "stderr", r.stderr, ui.Message("unsupported", "herdr", "detached-open"))
		if got := w.herdrWrites(); len(got) != 0 {
			t.Fatalf("herdr = %q, want nothing", got)
		}
	}
}

func (w *world) wantShownInside(t *testing.T, b mux.Backend, r result) {
	t.Helper()
	dir, name := w.switchSmall()
	wantStatus(t, r, 0)
	switch b {
	case mux.Devtools:
		if got := w.serverWrites(); !slices.Equal(got, []string{`ui.switch {"dir":"` + dir + `","name":"` + name + `"}`}) {
			t.Fatalf("requests = %q, want one ui.switch", got)
		}
	case mux.Tmux:
		if got := w.tmux(); !hasCall(got, "switch-client -t "+name) {
			t.Fatalf("tmux = %q, want switch-client", got)
		}
	case mux.Herdr:
		wantContains(t, "herdr", w.herdr(), "workspace create --cwd "+dir+" --label "+name+" --focus")
	}
	w.noClient()
}

// hubInPane runs a UI command inside the pane of b, which has a terminal
// for the standalone hub, and closes the hub with esc.
func (w *world) hubInPane(t *testing.T, b mux.Backend, args ...string) {
	t.Helper()
	if b == mux.Devtools {
		r := runCommand(args...)
		wantStatus(t, r, 0)
		got := w.serverWrites()
		if len(got) != 1 || !strings.HasPrefix(got[0], "ui.hub ") {
			t.Fatalf("requests = %q, want one ui.hub", got)
		}
		return
	}
	term := scriptedTerminal(t, "")
	r := runWithHub(t, term, "small", "\x1b", args...)
	wantStatus(t, r, 0)
	if got := w.serverWrites(); len(got) != 0 {
		t.Fatalf("a ui.hub reached the server: %q", got)
	}
	if b == mux.Tmux && len(w.tmuxWrites()) != 0 {
		t.Fatalf("tmux = %q, want only the listing of the hub", w.tmux())
	}
	if b == mux.Herdr && len(w.herdrWrites()) != 0 {
		t.Fatalf("herdr = %q, want only the listing of the hub", w.herdr())
	}
}

type matrixRow struct {
	name   string
	client bool // a client start is allowed: rows 1, 1a and 1b
	cells  map[mux.Backend]func(t *testing.T, w *world)
}

// every is a row whose three cells are one function of the backend.
func every(f func(t *testing.T, w *world, b mux.Backend)) map[mux.Backend]func(t *testing.T, w *world) {
	cells := map[mux.Backend]func(t *testing.T, w *world){}
	for _, b := range backends {
		cells[b] = func(t *testing.T, w *world) { f(t, w, b) }
	}
	return cells
}

// other is the backend whose pane the cells of rows 7 and 8 run in.
func other(b mux.Backend) mux.Backend {
	switch b {
	case mux.Devtools:
		return mux.Tmux
	case mux.Tmux:
		return mux.Herdr
	}
	return mux.Devtools
}

// TestR3Matrix is the table of 10.2.1: eighteen contexts on three
// backends. Each cell sets up the context, runs the command through
// newRootCommand and asserts the cell and the three records. Over the whole
// table a client start is recorded in the rows 1, 1a and 1b only.
func TestR3Matrix(t *testing.T) {
	rows := []matrixRow{
		{"1 plain terminal, attach", true, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			scriptedTerminal(t, ordinaryTerminal)
			r := runCommand("attach")
			wantStatus(t, r, 0)
			switch b {
			case mux.Devtools:
				if got := w.rec.Enters(); !slices.Equal(got, []string{"Enter valid=true last"}) {
					t.Fatalf("enters = %q", got)
				}
			case mux.Tmux:
				if !hasCall(w.tmux(), "attach-session") {
					t.Fatalf("tmux = %q, want attach-session", w.tmux())
				}
			case mux.Herdr:
				if h := w.herdr(); !strings.Contains(h, "server\n") || !strings.HasSuffix(h, "<attach>") {
					t.Fatalf("herdr = %q, want the server started, then <attach>", h)
				}
			}
		})},
		{"1a plain terminal, switch", true, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			scriptedTerminal(t, ordinaryTerminal)
			r := runCommand("switch", "small")
			wantStatus(t, r, 0)
			dir, name := w.switchSmall()
			switch b {
			case mux.Devtools:
				if got := w.rec.Enters(); !slices.Equal(got, []string{"Enter valid=true target " + dir + " " + name}) {
					t.Fatalf("enters = %q", got)
				}
			case mux.Tmux:
				if !hasCall(w.tmux(), "new-session -A -s "+name+" -c "+dir) {
					t.Fatalf("tmux = %q, want tmux.Attach on the session", w.tmux())
				}
			case mux.Herdr:
				h := w.herdr()
				wantContains(t, "herdr", h, "server\n")
				wantContains(t, "herdr", h, "workspace create --cwd "+dir+" --label "+name+" --focus\n<attach>")
			}
		})},
		{"1b plain terminal, pick", true, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			dir, name := w.switchSmall()
			if b == mux.Devtools {
				scriptedTerminal(t, ordinaryTerminal)
				wantStatus(t, runCommand("pick"), 0)
				if got := w.rec.Enters(); !slices.Equal(got, []string{"Enter valid=true hub 0 \"\""}) {
					t.Fatalf("enters = %q, want the client with the hub", got)
				}
				return
			}
			term := scriptedTerminal(t, ordinaryTerminal)
			wantStatus(t, runWithHub(t, term, "small", "\r", "pick"), 0)
			if b == mux.Tmux {
				if !hasCall(w.tmux(), "new-session -A -s "+name+" -c "+dir) {
					t.Fatalf("tmux = %q, want tmux.Attach on the pick", w.tmux())
				}
			} else {
				wantContains(t, "herdr", w.herdr(), "workspace create --cwd "+dir+" --label "+name+" --focus\n<attach>")
			}
			// again with the keys cancelling: no client, nothing but the
			// listing of the hub's feed
			clearFile(t, w.tmuxLog)
			clearFile(t, w.herdrLog)
			wantStatus(t, runWithHub(t, term, "small", "\x1b", "pick"), 0)
			if got := w.tmuxWrites(); len(got) != 0 {
				t.Fatalf("a cancelled pick ran tmux %q", got)
			}
			if got := w.herdrWrites(); len(got) != 0 {
				t.Fatalf("a cancelled pick ran herdr %q", got)
			}
		})},
		{"2 the terminal answers devtools, attach", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			scriptedTerminal(t, devtoolsPaneAnswer)
			wantResult(t, runCommand("attach"), 1, ui.Message("nested", string(b), "macmini"))
			w.nothingRan()
		})},
		{"3 the terminal answers devtools, switch", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			scriptedTerminal(t, devtoolsPaneAnswer)
			r := runCommand("switch", "small")
			w.wantOpenedOnly(t, b, r, ui.Message("nested", string(b), "macmini"))
			w.noClient()
		})},
		{"4 the terminal answers tmux or libghostty, attach", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			scriptedTerminal(t, tmuxPane)
			wantResult(t, runCommand("attach"), 1, ui.Message("othermux", string(b), "tmux"))
			// the guard does not name a libghostty host (P1-11): the words
			// are those of a terminal it cannot tell
			scriptedTerminal(t, libghosttyAnswer)
			wantResult(t, runCommand("attach"), 1, ui.Message("unknown"))
			w.nothingRan()
		})},
		{"5 inside this backend's pane, attach", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.inside(b)
			w.hubInPane(t, b, "attach")
			w.noClient()
		})},
		{"6 inside this backend's pane, switch", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.inside(b)
			w.wantShownInside(t, b, runCommand("switch", "small"))
		})},
		{"7 inside another backend's pane, the variable names this one", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.inside(other(b))
			w.use(b)
			wantResult(t, runCommand("switch", "small"), 1, ui.Message("mismatch", string(b), string(other(b))))
			w.nothingRan()
		})},
		{"8 inside another backend's pane, the config names this one", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.inside(other(b))
			w.config("multiplexer " + string(b))
			w.wantShownInside(t, other(b), runCommand("switch", "small"))
		})},
		{"9 tmux popup, pick and attach", false, map[mux.Backend]func(t *testing.T, w *world){
			mux.Tmux: func(t *testing.T, w *world) {
				w.tmuxPopup()
				term := scriptedTerminal(t, "")
				wantStatus(t, runWithHub(t, term, "small", "\r", "pick"), 0)
				if !hasCall(w.tmux(), "switch-client -t small-main") {
					t.Fatalf("tmux = %q, want the pick as switch-client", w.tmux())
				}
				w.noClient()
				clearFile(t, w.tmuxLog)
				wantStatus(t, runWithHub(t, term, "small", "\x1b", "attach"), 0)
				if got := w.tmuxWrites(); len(got) != 0 {
					t.Fatalf("attach in a popup ran tmux %q", got)
				}
				w.noClient()
			},
		}},
		{"10 inside screen or zellij, attach", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			for _, f := range []struct{ name, variable string }{{"screen", "STY"}, {"zellij", "ZELLIJ"}} {
				t.Setenv(f.variable, "1")
				wantResult(t, runCommand("attach"), 1, ui.Message("othermux", string(b), f.name))
				t.Setenv(f.variable, "")
				unsetEmptyMarks(t)
			}
			w.nothingRan()
		})},
		{"11 inside screen or zellij, switch", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			t.Setenv("STY", "1234.pts-0.host")
			w.wantOpenedOnly(t, b, runCommand("switch", "small"), ui.Message("othermux", string(b), "screen"))
			w.noClient()
		})},
		{"12 a clean environment in a tmux pane, switch", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			scriptedTerminal(t, tmuxPane)
			w.wantOpenedOnly(t, b, runCommand("switch", "small"), ui.Message("othermux", string(b), "tmux"))
			if hasCall(w.tmux(), "switch-client") {
				t.Fatalf("tmux = %q: nothing proves it is the same tmux server", w.tmux())
			}
			w.noClient()
		})},
		{"13 no terminal, switch", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.use(b)
			w.wantOpenedOnly(t, b, runCommand("switch", "small"), "")
			w.noClient()
		})},
		{"14 two live marks, none matching", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.inside(b)
			w.inside(other(b))
			r := runCommand("attach")
			wantStatus(t, r, 1)
			wantContains(t, "stderr", r.stderr, "This terminal looks like it is inside both")
			w.nothingRan()
		})},
		{"15 no terminal, a tmux pane inside a devtools pane, switch", false, every(func(t *testing.T, w *world, b mux.Backend) {
			w.inDevtools()
			w.inTmux()
			w.use(b)
			r := runCommand("switch", "small")
			wantStatus(t, r, 1)
			wantContains(t, "stderr", r.stderr, "This terminal looks like it is inside both")
			w.nothingRan()
		})},
		{"16 inside this backend's pane, the check does not finish, the config names another", false, map[mux.Backend]func(t *testing.T, w *world){
			mux.Devtools: func(t *testing.T, w *world) {
				t.Setenv("DEVTOOLS_SOCKET", silentServer(t))
				w.inDevtools()
				w.config("multiplexer tmux")
				wantResult(t, runCommand("switch", "small"), 1, ui.Message("noserver", "tmux"))
				w.nothingRan()
			},
			mux.Tmux: func(t *testing.T, w *world) {
				w.tmuxLog = installSlowFakeTmux(t)
				w.inTmux()
				w.config("multiplexer herdr")
				w.wantShownInside(t, mux.Tmux, runCommand("switch", "small"))
			},
			mux.Herdr: func(t *testing.T, w *world) {
				// provisional P2-08b: an unanswered herdr check is an
				// empty HERDR_SOCKET_PATH, which herdrAlive answers
				// Unknown; a socket that accepts and stalls cannot be
				// built for a plain connect
				t.Setenv("HERDR_ENV", "1")
				t.Setenv("HERDR_RUNNING", "")
				w.config("multiplexer tmux")
				w.wantShownInside(t, mux.Herdr, runCommand("switch", "small"))
			},
		}},
	}
	if len(rows) != 18 {
		t.Fatalf("%d contexts, want 18", len(rows))
	}
	for _, row := range rows {
		for _, b := range backends {
			cell, ok := row.cells[b]
			if !ok {
				continue
			}
			t.Run(row.name+"/"+string(b), func(t *testing.T) {
				w := newWorld(t)
				cell(t, w)
				if w.started() && !row.client {
					t.Fatalf("a client started in %s", row.name)
				}
			})
		}
	}
}

// installSlowFakeTmux is the fake tmux whose display-message, the check of
// the tmux mark, answers only after a second, beyond the limit of 500 ms.
func installSlowFakeTmux(t *testing.T) string {
	t.Helper()
	script := strings.Replace(fakeTmuxScript(""), "if [ \"$1\" = display-message ]; then\n", "if [ \"$1\" = display-message ]; then\nsleep 1\n", 1)
	return installFakeTmuxScript(t, script)
}
