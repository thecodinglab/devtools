package remote

import (
	"fmt"
	"regexp"
	"strings"

	"devtools/internal/session"
)

// The lines the bridge writes before the first protocol byte (4.4).
const (
	readyPrefix = "devtools-bridge-ready "
	errorPrefix = "devtools-bridge-error "
)

// readyLine is the line of a bridge of this generation, without "\n".
var readyLine = fmt.Sprintf("%s%d", readyPrefix, protoGen)

// The stderr patterns of 3.5.2. The exit status 255 and these strings are
// UNVERIFIED until E5 records them on the three machines (provisional, E5);
// E5 may widen them.
var (
	hostKeyRe     = regexp.MustCompile(`Host key verification failed|REMOTE HOST IDENTIFICATION HAS CHANGED|No .* host key is known`)
	authRe        = regexp.MustCompile(`Permission denied|Too many authentication failures`)
	unreachableRe = regexp.MustCompile(`Connection timed out|Connection refused|No route to host|Network is unreachable|Could not resolve hostname`)
	notFoundRe    = regexp.MustCompile(`command not found`)
	oldBuildRe    = regexp.MustCompile(`unknown command "bridge"`)
)

// Classify turns the end of an ssh process that never became a link into a
// Failure (3.5.2): exit is its exit status (-1 when a signal ended it),
// stderr the kept tail of its standard error, and bridgeLine the
// "devtools-bridge-error" line it printed, or "". It always returns a
// Failure; a cause it cannot name is "unreachable" when ssh itself failed
// (exit 255) and "protocol" otherwise.
func Classify(exit int, stderr []byte, bridgeLine string) *Failure {
	if rest, ok := strings.CutPrefix(strings.TrimRight(bridgeLine, "\r\n"), errorPrefix); ok {
		code, text, _ := strings.Cut(rest, " ")
		if code == string(NoServer) {
			return failure(NoServer, text, "the devtools server is not running")
		}
		// A code this build does not know comes from another build.
		return failure(Incompatible, "", "the bridge reported "+code+" "+text)
	}
	s := string(stderr)
	if line := lastMatch(s, oldBuildRe); line != "" {
		return failure(Incompatible, line, "")
	}
	if line := lastMatch(s, notFoundRe); line != "" || exit == 127 {
		return failure(NoDevtools, line, "devtools is not installed there")
	}
	if exit == 255 {
		if line := lastMatch(s, hostKeyRe); line != "" {
			return failure(HostKey, line, "")
		}
		if line := lastMatch(s, authRe); line != "" {
			return failure(Auth, line, "")
		}
		if line := lastMatch(s, unreachableRe); line != "" {
			return failure(Unreachable, line, "")
		}
		// Reasons ssh does not let us tell apart collapse into unreachable
		// (RK6).
		return failure(Unreachable, lastLine(s), "ssh failed")
	}
	text := fmt.Sprintf("devtools bridge ended with status %d before it was ready", exit)
	if l := lastLine(s); l != "" {
		text += ": " + l
	}
	return failure(Protocol, text, "")
}

// failure builds a Failure whose text is text, or fallback when text is
// empty, sanitised to one line.
func failure(r Reason, text, fallback string) *Failure {
	t := session.Sanitize(strings.TrimSpace(text), session.MaxText)
	if t == "" {
		t = session.Sanitize(fallback, session.MaxText)
	}
	return &Failure{Reason: r, Text: t}
}

// lastMatch returns the last line of s that re matches, or "".
func lastMatch(s string, re *regexp.Regexp) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if re.MatchString(lines[i]) {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// lastLine returns the last line of s that is not blank, or "".
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
