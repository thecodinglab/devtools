package cli

import (
	"strings"
	"testing"

	"devtools/internal/mux"
	"devtools/internal/ui"
)

// SEC-W1-01 (review of wave 1), the part P2-08b closed: agents and
// sessions typed in a live tmux pane, with herdr installed and so the
// default before the cutover, stay with tmux. sessions opens the tmux hub
// in the pane; agents is refused by the feature gate with the unsupported
// words and status 1, because tmux has no agents (mux.Features). Neither
// asks herdr anything, starts its server or attaches a client of any
// backend.
func TestAgentsAndSessionsInsideTmuxOpenTheTmuxHubAndNeverHerdr(t *testing.T) {
	for _, command := range []string{"agents", "sessions"} {
		t.Run(command, func(t *testing.T) {
			w := newWorld(t)
			w.inTmux()
			term := scriptedTerminal(t, "")
			if command == "agents" {
				wantResult(t, runCommand(command), 1, ui.Message("unsupported", string(mux.Tmux), string(mux.CapAgents)))
			} else {
				wantStatus(t, runWithHub(t, term, "\x1b[?1049h", "\x1b", command), 0)
			}
			if got := w.rec.Opened(); len(got) != 1 || got[0] != mux.Tmux {
				t.Fatalf("opened %v, want the tmux backend only", got)
			}
			if w.rec.ctx.Where != mux.InTmux {
				t.Fatalf("context = %+v, want inside tmux", w.rec.ctx)
			}
			if got := w.herdr(); got != "" {
				t.Fatalf("the fake herdr ran %q", got)
			}
			w.noClient()
			if got := w.tmuxWrites(); len(got) != 0 {
				t.Fatalf("tmux was asked %q, want only the hub's listing", got)
			}
		})
	}
}

// O-01: the build sandbox of `nix flake check` sets TERM=xterm-256color and
// no COLORTERM. Inside tmux bubbletea's colour detection then runs
// "tmux info", which the fake tmux logs and every test of the tmux hub
// counted as a call of the backend. The fake tmux's helper settles the
// colour profile itself, so the tests give the same answer in the
// sandbox as in the dev shell; this test runs the hub in the sandbox's
// environment and fails when the detection reaches the fake again.
func TestFakeTmuxHubIsHermeticInTheBuildSandbox(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM_PROGRAM", "")
	w := newWorld(t)
	w.inTmux()
	term := scriptedTerminal(t, "")
	wantStatus(t, runWithHub(t, term, "\x1b[?1049h", "\x1b", "sessions"), 0)
	for _, c := range w.tmux() {
		if c == "info" || strings.HasPrefix(c, "info ") {
			t.Fatalf("tmux was asked %q, the colour detection reached the fake", w.tmux())
		}
	}
	if got := w.tmuxWrites(); len(got) != 0 {
		t.Fatalf("tmux was asked %q, want only the hub's listing", got)
	}
}
