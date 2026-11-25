package ui

// Every visible word of the chrome, the hub and the messages printed before
// the client starts (5a.14, 5.3.9), and the timings of the look (5a.7.3).
// This file is a contract (6.2, P4-00 a). A text that is not produced here
// does not appear on screen. An unknown kind in Notice, Message, Empty and
// Busy panics, and so does a call with too few arguments for its kind: both
// are programming errors that a test of the caller finds at once.
//
// Rules of the words (5a.14): sentence case; a full stop after a sentence,
// none after a label, a count or a hint; what happened first, what to type
// second; "session", never "workspace"; "machine", never "host" or "peer".

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The timings of 5a.7.3.
const (
	Slow       = 150 * time.Millisecond // a loading text or the switching row appears only after this
	NoticeLife = 8 * time.Second        // a notice leaves by itself
)

// The minimum size of the window (5a.6.8).
const (
	MinCols = 20
	MinRows = 6
)

// Problem is why a machine is not usable: the fields of hub.Problem.
// Reason is a remote.Reason value or "lost"; Retry is "", "retrying",
// "next-open", "in-30s" or "login".
type Problem struct{ Reason, Text, Retry string }

// StartHint is the command a person runs to start the devtools server, the
// second line of the noserver words. internal/ui imports no devtools
// package, so the command that prints the words sets it from
// platform.StartHint() first; the value here is the one of a Linux machine
// with the default service name, the example of 5a.9.3.
var StartHint = "systemctl --user start devtools-server"

// Fixed labels of 5a.14.1.
const (
	// mode blocks
	ModePrefix    = "prefix"
	ModeSwitching = "switching"
	ModeCopy      = "copy"
	ModeScrolled  = "scrolled"

	// the switching row: "to {machine} {session}…"
	SwitchingTo = "to"

	// the copy row
	Selecting      = "selecting"
	SelectingLines = "selecting lines"
	SelectingBlock = "selecting block"
	AnyKeyReturns  = "any key returns"

	// the fleet strip: "{m} retrying", "{m} off", and "↑ update" while
	// this machine's server waits for a restart to run an installed update
	FleetRetrying = "retrying"
	FleetOff      = "off"
	FleetUpdate   = "update"

	// the scope bar
	ScopeAll      = "all"
	ScopeSessions = "sessions"
	ScopeAgents   = "agents"

	// hint labels of the status row that no binding carries: "esc cancel",
	// "? more", and the hints of copy mode "v select", "y copy", "q leave"
	HintCancel = "cancel"
	HintMore   = "more"
	HintSelect = "select"
	HintCopy   = "copy"
	HintLeave  = "leave"

	// the hint of the update mark at the end of the hint strip: "↑ update
	// installed, run devtools server restart"
	HintUpdate = "update installed, run devtools server restart"

	// hub hints: "⏎ switch", "⏎ create", "⏎ jump to pane", "⏎ merge",
	// "⌃N new feature", "⌃G merge into main", "tab {next scope}",
	// "esc close", "esc clear", "esc cancel"
	HubSwitch     = "switch"
	HubCreate     = "create"
	HubJump       = "jump to pane"
	HubMerge      = "merge"
	HubNewFeature = "new feature"
	HubMergeMain  = "merge into main"
	HubClose      = "close"
	HubClear      = "clear"
	HubCancel     = "cancel"
)

// Fixed texts of 5a.14.2 to 5a.14.5.
const (
	// appended to the machine phrase in the banner (5a.6.4)
	BannerPaused = "Typing is paused."

	// the footer of the hub when the selected row cannot be acted on here
	CreateNotHere = "A feature can only be created on this machine."
	MergeNotHere  = "Only a feature worktree on this machine can be merged into main."

	// the confirm of closing a pane: "⏎ close pane", "esc cancel"
	ConfirmClosePane = "close pane"
	ConfirmCancel    = "cancel"

	// the second line of the pane message
	PressEnter = "Press enter to start it again."

	// the window is smaller than MinCols by MinRows
	TooSmall     = "Window too small"
	TooSmallNeed = "20 by 6 needed"
)

// need returns args[i] and panics when the kind was called with too few.
func need(fn, kind string, args []string, i int) string {
	if i >= len(args) {
		panic(fmt.Sprintf("ui.%s(%q) needs %d arguments, got %d", fn, kind, i+1, len(args)))
	}
	return args[i]
}

// count formats n, which is a decimal number, as Number does; any other
// text is returned as it is.
func count(n string) string {
	v, err := strconv.ParseInt(n, 10, 64)
	if err != nil {
		return n
	}
	return Number(v)
}

// plural is "{n} {one}" or "{n} {many}".
func plural(n, one, many string) string {
	if n == "1" {
		return n + " " + one
	}
	return count(n) + " " + many
}

// MachinePhrase is the phrase after a machine's name in the hub heading,
// the banner and, shortened to its reason word, the fleet strip (5a.14.2).
// state is a fleet.MachineState.State: "off", "connecting", "online",
// "retrying" or "attention". listing is true while an online machine is
// still sending its rows. failed reports whether the phrase is preceded by
// the failed mark. The phrase starts in lower case because it continues the
// machine's name; online with a complete list has no phrase.
func MachinePhrase(name, state string, p Problem, listing bool) (phrase string, failed bool) {
	switch state {
	case "online":
		if listing {
			return "listing…", false
		}
		return "", false
	case "connecting":
		return "connecting…", false
	}
	switch p.Reason {
	case "":
		if state == "off" {
			return "not connected.", false
		}
		return unreachable(state, p.Retry), false
	case "unreachable", "lost":
		return unreachable(state, p.Retry), false
	case "login":
		return "needs a new ssh login. Open the hub to log in.", false
	case "auth":
		return "ssh could not log in. Open the hub to try again.", true
	case "host-key":
		return "host key not accepted. Run ssh " + name + " once and check it.", true
	case "no-devtools":
		return "devtools is not installed there.", true
	case "no-server":
		return "the devtools server is not running there.", true
	case "incompatible":
		return "incompatible. Update devtools on " + name + ".", true
	case "protocol":
		return "unexpected answer: " + ansi.Truncate(oneLine(p.Text), 60, ""), true
	}
	// a reason this table does not know yet: said in its own words
	return ReasonWord(p.Reason) + ".", true
}

// BuildPhrase is what the hub heading of an online machine says of the
// build of its server: its version, when the hub shows it because it is
// another than the one of this machine's server ("" leaves it out), and
// "update installed, restart pending" while an update installed there waits
// for a restart of the server. "" when there is nothing to say.
func BuildPhrase(version string, pending bool) string {
	parts := []string{}
	if version != "" {
		parts = append(parts, oneLine(version))
	}
	if pending {
		parts = append(parts, "update installed, restart pending")
	}
	return strings.Join(parts, ", ")
}

// WithBuild puts the build phrase before the machine phrase, a comma
// between them when both say something.
func WithBuild(build, phrase string) string {
	switch {
	case build == "":
		return phrase
	case phrase == "":
		return build
	}
	return build + ", " + phrase
}

// unreachable is the phrase of a machine that did not answer.
func unreachable(state, retry string) string {
	if retry == "next-open" || (retry == "" && state == "off") {
		return "not reachable. Opening the hub again retries."
	}
	return "not reachable. Retrying."
}

// oneLine replaces every control character of s by a space, so a text from
// another machine cannot move the cursor or start a new line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}

// LocalPhrase is the phrase of this machine: "older" when the server is
// older than the client, "stopped" when the server stopped (banner only).
func LocalPhrase(kind string) string {
	switch kind {
	case "older":
		return "server is older than this client. Run devtools server restart."
	case "stopped":
		return "the devtools server stopped. Reconnecting."
	}
	panic("ui.LocalPhrase: unknown kind " + strconv.Quote(kind))
}

// ListPhrase is the phrase of a machine whose list is not whole: "cut" at
// 2,000 rows, or "incomplete" when the scan did not finish.
func ListPhrase(kind string) string {
	switch kind {
	case "cut":
		return "list cut at 2,000 rows. Type to narrow it."
	case "incomplete":
		return "scan did not finish. Some worktrees may be missing."
	}
	panic("ui.ListPhrase: unknown kind " + strconv.Quote(kind))
}

// ReasonWord is the reason of a failed machine in the fleet strip:
// "host-key" gives "host key", "no-devtools" "no devtools".
func ReasonWord(reason string) string {
	return strings.ReplaceAll(reason, "-", " ")
}

// AgentWord is the word of an agent state in the hub: "blocked" gives
// "needs you"; a session without an agent ("" or "unknown") is "open".
func AgentWord(status string) string {
	switch status {
	case "blocked":
		return "needs you"
	case "done":
		return "done"
	case "working":
		return "working"
	case "idle":
		return "idle"
	}
	return "open"
}

// RollupWord is the word after a count in the roll-up: "needs you" for one
// blocked agent, "need you" for several, "done". Working and idle get no
// word.
func RollupWord(status string, n int) string {
	switch status {
	case "blocked":
		if n == 1 {
			return "needs you"
		}
		return "need you"
	case "done":
		return "done"
	}
	return ""
}

// UrgentPlace is where the most urgent agent is, after its count in the
// roll-up: "in {place}", or "first in {place}" when the count is above one.
func UrgentPlace(place string, n int) string {
	if n > 1 {
		return "first in " + place
	}
	return "in " + place
}

// Placeholder is the text of the hub's empty input, by scope ("all",
// "sessions", "agents"), or the one of a new feature.
func Placeholder(scope string, creating bool) string {
	if creating {
		return "Name the new feature"
	}
	switch scope {
	case ScopeAll:
		return "Switch to a worktree, or name a new feature"
	case ScopeSessions:
		return "Switch to an open session"
	case ScopeAgents:
		return "Jump to an agent"
	}
	panic("ui.Placeholder: unknown scope " + strconv.Quote(scope))
}

// CreateRow is the create row of the hub after its mark, in three parts
// that are drawn in three styles: "New feature ", the branch, and
// " in {project}, from {base}".
func CreateRow(branch, project, base string) [3]string {
	return [3]string{"New feature ", branch, " in " + project + ", from " + base}
}

// MergeQuestion is the question above the footer while a merge waits for
// enter.
func MergeQuestion(worktree string) string {
	return "Merge " + worktree + " into main, then remove its worktree and branch?"
}

// Empty is the text of an empty or loading list (5a.14.3). Kinds:
// projects, sessions, agents, machine (arg: the machine), query (arg: the
// query), name, loading, loading-sessions.
func Empty(kind, arg string) string {
	switch kind {
	case "projects":
		return "No projects yet. Clone one with devtools clone <url>."
	case "sessions":
		return "No open sessions. Press shift+tab to list every worktree."
	case "agents":
		return "No agents running. Start claude or codex in a pane."
	case "machine":
		return "Nothing on " + arg + " yet. Run devtools clone <url> there."
	case "query":
		return "Nothing matches " + arg + "."
	case "name":
		return "Type a name for the feature first."
	case "loading":
		return "Loading worktrees…"
	case "loading-sessions":
		return "Loading open sessions…"
	}
	panic("ui.Empty: unknown kind " + strconv.Quote(kind))
}

// Busy is the footer of the hub while it waits (5a.14.4). Kinds and
// arguments: open (session), open-remote (session, machine), create
// (branch, project), merge (worktree).
func Busy(kind string, args ...string) string {
	switch kind {
	case "open":
		return "Opening " + need("Busy", kind, args, 0) + "…"
	case "open-remote":
		return "Opening " + need("Busy", kind, args, 0) + " on " + need("Busy", kind, args, 1) + "…"
	case "create":
		return "Creating " + need("Busy", kind, args, 0) + " in " + need("Busy", kind, args, 1) + "…"
	case "merge":
		return "Merging " + need("Busy", kind, args, 0) + " into main…"
	}
	panic("ui.Busy: unknown kind " + strconv.Quote(kind))
}

// hints is the hint strip of 5a.11.2: action, argument and label, in the
// order of the strip.
var hints = [][3]string{
	{"hub", "", "hub"},
	{"hub", "sessions", "sessions"},
	{"hub", "agents", "agents"},
	{"hub", "new", "new feature"},
	{"tab-new", "", "new tab"},
	{"tab-next", "", "next tab"},
	{"tab-prev", "", "previous tab"},
	{"tab-select", "", "tab"}, // stands for tab-select 1 to 9, the keys shown as 1-9
	{"split-right", "", "split right"},
	{"split-down", "", "split down"},
	{"pane-close", "", "close pane"},
	{"copy-mode", "", "copy"},
	{"session-prev", "", "session"}, // stands for session-prev and session-next, the keys shown as ( )
	{"detach", "", "detach"},
}

// HintOrder is the action and argument of every hint of the strip, in the
// order of 5a.11.2. "esc cancel" comes before them and "? more" after them;
// both are built in and have no action. The entry tab-select stands for
// tab-select 1 to 9 and session-prev for the pair session-prev and
// session-next.
func HintOrder() [][2]string {
	order := make([][2]string, len(hints))
	for i, h := range hints {
		order[i] = [2]string{h[0], h[1]}
	}
	return order
}

// HintLabel is the label of a hint of the strip; "" for an action that is
// not in the strip.
func HintLabel(action, arg string) string {
	if action == "session-next" {
		action = "session-prev"
	}
	if action == "tab-select" {
		arg = ""
	}
	for _, h := range hints {
		if h[0] == action && h[1] == arg {
			return h[2]
		}
	}
	return ""
}

// Notice is a line of the status row that leaves by itself (5a.14.4). Its
// text is at most 50 cells with a machine name of seven letters, except
// where a server's text or a machine's long name is part of it; the status
// row cuts what is longer. Kinds and arguments: copied (count), unbound
// (key), no-answer (machine), still-connecting (machine), machine (machine,
// reason, and for the reason protocol the text), refused (machine, the
// server's text), keys-dropped, restarted (machine), closed-idle (machine),
// self (machine), older-server, bad-frame (machine).
func Notice(kind string, args ...string) string {
	arg := func(i int) string { return need("Notice", kind, args, i) }
	switch kind {
	case "copied":
		return "Copied " + plural(arg(0), "character", "characters")
	case "unbound":
		return "No binding for " + arg(0)
	case "no-answer":
		return arg(0) + " did not answer"
	case "still-connecting":
		return "still connecting to " + arg(0)
	case "machine":
		p := Problem{Reason: arg(1), Retry: "retrying"}
		if len(args) > 2 {
			p.Text = args[2]
		}
		phrase, _ := MachinePhrase(arg(0), "retrying", p, false)
		return arg(0) + " " + firstSentence(phrase)
	case "refused":
		return arg(0) + ": " + oneLine(arg(1))
	case "keys-dropped":
		return "Some keys typed during the switch were dropped"
	case "restarted":
		return arg(0) + " restarted. Its shells are new."
	case "closed-idle":
		return arg(0) + " closed after 10 min idle"
	case "self":
		return arg(0) + " is this machine and is left out"
	case "older-server":
		return "Server is older. Run devtools server restart."
	case "bad-frame":
		return arg(0) + " sent an unreadable frame. Reconnecting."
	}
	panic("ui.Notice: unknown kind " + strconv.Quote(kind))
}

// firstSentence is s up to and including its first full stop that ends a
// sentence, or s when it has none.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// ConfirmClose is the question of the confirm of closing a pane: 0 the
// whole question, 1 "Close {program}?", 2 "Close?" (5a.14.5, 5a.6.3).
func ConfirmClose(program string, short int) string {
	switch short {
	case 0:
		return "Close this pane? " + program + " is still running."
	case 1:
		return "Close " + program + "?"
	}
	return "Close?"
}

// CopyPosition is the position in the copy and scrolled rows: 0 "line {n}
// of {total}", 1 "line {n}", 2 "{n}" (the cut rules of 5a.6.3).
func CopyPosition(n, total int64, short int) string {
	switch short {
	case 0:
		return "line " + Number(n) + " of " + Number(total)
	case 1:
		return "line " + Number(n)
	}
	return Number(n)
}

// PaneKilled is the first line of the pane message of a program ended by a
// signal.
func PaneKilled(program string, signal int) string {
	return program + " was killed (signal " + strconv.Itoa(signal) + ")."
}

// PaneNotStarted is the first line of the pane message of a program that
// could not be started.
func PaneNotStarted(program, text string) string {
	return program + " could not be started: " + oneLine(text)
}

// Date is the date of the status row: "Tue 17 Nov".
func Date(t time.Time) string { return t.Format("Mon 2 Jan") }

// Clock is the time of the status row, 24 hours: "14:32".
func Clock(t time.Time) string { return t.Format("15:04") }

// unsupportedPhrases is what a backend cannot do, by capability (5a.14.6).
// The capability names are the ones of internal/mux (5.3.10), written out
// here because internal/ui imports no devtools package.
var unsupportedPhrases = map[string]string{
	"machines":      "show sessions on other machines",
	"agent-status":  "list agents",
	"window":        "show devtools key bindings",
	"pane-focus":    "move the pane focus for devtools",
	"layout":        "list sessions and panes as JSON",
	"detached-open": "open a session without showing it",
}

// Message is what a command prints on the normal screen, without colour,
// before or instead of starting a client (5a.9.3, 5a.14.6). Lines are
// separated by "\n"; there is no newline after the last one. Backends are
// passed as their program names in lower case (devtools, tmux, herdr) and
// capabilities as their names in internal/mux. Kinds and arguments:
//
//	noserver     none, or "tmux" when tmux is installed (a third line names it)
//	nested       backend, host, and optionally the chord of "hub" (default "ctrl+space o")
//	othermux     backend, where (screen, zellij, tmux, herdr)
//	unknown      none
//	notty        optionally the command ("attach" gives "devtools attach needs a terminal.")
//	generation   the number of panes the restart ends
//	drift        the server's version, the installed version ("unknown" when not known)
//	otherbuild   the server's version, this devtools' version
//	restart      the number of panes, the number of sessions
//	no-client    none
//	opened-only  the session's name
//	unsupported  backend, capability
//	mismatch     asked, inside
//	unavailable  backend (tmux or herdr; for devtools the kind is noserver)
//	ambiguous    a, b
//
// noserver's second line names StartHint.
func Message(kind string, args ...string) string {
	arg := func(i int) string { return need("Message", kind, args, i) }
	switch kind {
	case "noserver":
		text := "The devtools server is not running.\nStart it with: " + StartHint
		if len(args) == 0 {
			return text
		}
		if args[0] != "tmux" || len(args) > 1 {
			panic(fmt.Sprintf("ui.Message(%q) takes no argument or the one word tmux, got %q", kind, args))
		}
		return text + "\nTo use tmux for now, put DEVTOOLS_MULTIPLEXER=tmux before the command."
	case "nested":
		backend, host := arg(0), arg(1)
		first := "This terminal is a devtools pane on " + host + "."
		if backend != "devtools" {
			return first + "\n" + backend + " was not started, because it would run inside that pane."
		}
		chord := "ctrl+space o"
		if len(args) > 2 {
			chord = args[2]
		}
		return first + "\nOpen the hub with " + chord + " to switch there."
	case "othermux":
		backend, where := arg(0), arg(1)
		return backend + " does not start inside " + where + ". Detach from " + where + " first."
	case "unknown":
		return "devtools could not tell what this terminal is, so it did not start."
	case "notty":
		if len(args) > 0 && args[0] != "" {
			return "devtools " + args[0] + " needs a terminal."
		}
		return "devtools needs a terminal."
	case "generation":
		return "The devtools server is from another version of devtools.\n" +
			"Restart it with: devtools server restart (this ends " + plural(arg(0), "pane", "panes") + ")"
	case "drift":
		installed := "another build"
		if v := arg(1); v != "" && v != "unknown" {
			installed = v
		}
		return "The server runs " + arg(0) + ", but " + installed + " is installed.\n" +
			"Run devtools server restart to start the installed one."
	case "otherbuild":
		return "The server runs " + arg(0) + ", and this devtools is " + arg(1) + "."
	case "restart":
		return "Restart the devtools server? This ends " + plural(arg(0), "pane", "panes") +
			" in " + plural(arg(1), "session", "sessions") + ". [y/N]"
	case "no-client":
		return "No window shows this pane."
	case "opened-only":
		return "Opened " + arg(0) + ". No window shows this pane, so nothing was switched."
	case "unsupported":
		backend, capability := arg(0), arg(1)
		phrase, ok := unsupportedPhrases[capability]
		if !ok {
			panic("ui.Message(\"unsupported\"): unknown capability " + strconv.Quote(capability))
		}
		text := backend + " cannot " + phrase + "."
		if capability != "detached-open" {
			text += " A devtools window can."
		}
		return text
	case "mismatch":
		asked, inside := arg(0), arg(1)
		return "DEVTOOLS_MULTIPLEXER is set to " + asked + ", but this terminal is inside " + inside + ".\n" +
			"Unset it, or detach from " + inside + " first."
	case "unavailable":
		return arg(0) + " is not installed on this machine."
	case "ambiguous":
		return "This terminal looks like it is inside both " + arg(0) + " and " + arg(1) + ".\n" +
			"devtools cannot tell which one, so it did nothing."
	}
	panic("ui.Message: unknown kind " + strconv.Quote(kind))
}
