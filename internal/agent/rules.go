package agent

import (
	"strings"
	"unicode/utf8"
)

// The rules of 3.6.1 and 3.6.2 of the plan, as experiment E4 recorded them
// (docs/replace-herdr/experiments/e4-agent-reports.md). E4 ran its automatable
// parts only; every rule below that rests on a part still pending carries the
// word provisional and the id E4.

// identify returns the kind of agent the processes of a pane's foreground
// group show, or "" when none is an agent. When the current kind is still
// present it wins, so a group that holds both kinds does not flip.
func identify(current Kind, group []Proc) Kind {
	if current != "" {
		for _, p := range group {
			if kindOf(p) == current {
				return current
			}
		}
	}
	for _, p := range group {
		if k := kindOf(p); k != "" {
			return k
		}
	}
	return ""
}

// kindOf applies the identity table to one process.
func kindOf(p Proc) Kind {
	switch {
	case isClaude(p):
		return Claude
	case isCodex(p):
		return Codex
	}
	return ""
}

// isClaude is the claude row of the identity table of 3.6.1, widened by E4:
// on NixOS Claude Code runs with the name ".claude-wrapped" (the wrapper's
// exec -a keeps argv[0] ".../bin/claude"), which the planning rule missed.
// The macOS shapes are provisional (E4): no ps of a Mac was recorded; the
// argv[0] rule is what covers a native install whose executable is named
// after its version.
func isClaude(p Proc) bool {
	name := base(p.Name)
	if name == "claude" || name == ".claude-wrapped" {
		return true
	}
	if len(p.Args) > 0 && base(p.Args[0]) == "claude" {
		return true
	}
	if name != "node" && name != "bun" {
		return false
	}
	for _, a := range firstArgs(p.Args, 3) {
		if base(a) == "claude" || strings.Contains(a, "/claude-code/") {
			return true
		}
	}
	return false
}

// isCodex is the codex row of the identity table of 3.6.1. The names
// ".codex-wrapped" and an argv[0] whose base name is "codex" are provisional
// (E4): inferred from the Nix wrapper of Codex 0.167.0, no Codex process was
// observed.
func isCodex(p Proc) bool {
	name := base(p.Name)
	if name == "codex" || name == ".codex-wrapped" || strings.HasPrefix(name, "codex-") {
		return true
	}
	if len(p.Args) > 0 && base(p.Args[0]) == "codex" {
		return true
	}
	if name != "node" {
		return false
	}
	for _, a := range firstArgs(p.Args, len(p.Args)) {
		if base(a) == "codex" || strings.Contains(a, "/codex/") {
			return true
		}
	}
	return false
}

// firstArgs returns at most n arguments after argv[0].
func firstArgs(args []string, n int) []string {
	if len(args) < 2 {
		return nil
	}
	args = args[1:]
	if len(args) > n {
		args = args[:n]
	}
	return args
}

// base is the last element of a slash-separated path.
func base(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// fromReport maps the state of an OSC 7501 root report (3.6.2): working and
// blocked (any kind) are themselves, everything else (idle, done, clear,
// error, a state nobody knows) is idle; done is derived by the tracker.
func fromReport(r Report) Status {
	switch r.State {
	case "working":
		return Working
	case "blocked":
		return Blocked
	}
	return Idle
}

// fromClaudeTitle is the title row of Claude in 3.6.2, used only while the
// pane has sent no report: a spinner glyph and a space is working, "✳ " is
// idle, and anything else changes nothing (ok false). E4 read the glyphs in
// the Claude Code 2.1.319 bundle ("◐ "/"◑ " while busy, "✳ " otherwise); the
// sequence they come in is provisional (E4), not recorded.
func fromClaudeTitle(title string) (Status, bool) {
	r, rest, ok := firstRune(title)
	if !ok {
		return "", false
	}
	if (isBraille(r) || r >= 0x25d0 && r <= 0x25d3) && strings.HasPrefix(rest, " ") {
		return Working, true
	}
	if r == '✳' && strings.HasPrefix(rest, " ") {
		return Idle, true
	}
	return "", false
}

// fromCodexTitle is the title row of Codex in 3.6.2. The strings ("[ ! ]
// Action Required", the braille frames) are the ones E4 found in the Codex
// 0.167.0 binary; that Codex shows them in this order is provisional (E4).
// Codex reports through OSC 7501 too (E4 part C), but until a recording
// confirms what it sends, the title is its only source (provisional E4).
func fromCodexTitle(title string) Status {
	if r, _, ok := firstRune(title); ok && isBraille(r) {
		return Working
	}
	if strings.Contains(title, "Action Required") {
		return Blocked
	}
	return Idle
}

func isBraille(r rune) bool { return r >= 0x2800 && r <= 0x28ff }

func firstRune(s string) (rune, string, bool) {
	if s == "" {
		return 0, "", false
	}
	r, n := utf8.DecodeRuneInString(s)
	return r, s[n:], true
}

// Rank orders statuses by urgency: blocked 4, done 3, working 2, idle 1,
// unknown 0. A status this package does not know ranks with unknown.
func Rank(s Status) int {
	switch s {
	case Blocked:
		return 4
	case Done:
		return 3
	case Working:
		return 2
	case Idle:
		return 1
	}
	return 0
}

// Rollup is the most urgent of statuses (blocked > done > working > idle >
// unknown); a status this package does not know counts as unknown. It is
// "" when statuses is empty: there is no agent.
func Rollup(statuses []Status) Status {
	var top Status
	for _, s := range statuses {
		if Rank(s) == 0 {
			s = Unknown
		}
		if top == "" || Rank(s) > Rank(top) {
			top = s
		}
	}
	return top
}
