package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestMain keeps the tests hermetic (10.1 of the plan). No test may reach a
// tmux or a herdr of the developer: TMUX, TMUX_PANE, every HERDR_ and
// DEVTOOLS_ variable are removed, HERDR_BIN_PATH names a program that does
// not exist, TMUX_TMPDIR is a directory of the test, and every directory of
// PATH that holds a tmux or a herdr is taken out of PATH. A test that wants
// one puts its fake first on PATH (tmux) or names it in HERDR_BIN_PATH.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("/tmp", "dt-backend-")
	if err != nil {
		panic(err)
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "TMUX") || strings.HasPrefix(name, "HERDR_") ||
			strings.HasPrefix(name, "DEVTOOLS_") || strings.HasPrefix(name, "XDG_") ||
			name == "STY" || strings.HasPrefix(name, "ZELLIJ") {
			os.Unsetenv(name)
		}
	}
	os.Setenv("HOME", tmp)
	os.Setenv("TMUX_TMPDIR", tmp)
	os.Setenv("HERDR_BIN_PATH", filepath.Join(tmp, "no-herdr"))
	os.Setenv("DEVTOOLS_CONFIG", filepath.Join(tmp, "config"))
	var keep []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if executable(filepath.Join(dir, "tmux")) || executable(filepath.Join(dir, "herdr")) {
			continue
		}
		keep = append(keep, dir)
	}
	os.Setenv("PATH", strings.Join(keep, string(os.PathListSeparator)))
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func executable(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// The fake tmux (10.2.8): a script named tmux first on the test's PATH that
// records its arguments, one call per line of $TMUX_LOG, and answers from
// the environment of the test:
//
//	has-session -t <s>   exit 0 when <s> is one of the words of $TMUX_HAS
//	list-sessions        prints $TMUX_SESSIONS
//	display-message      sleeps $TMUX_SLEEP seconds when set, then prints
//	                     $TMUX_ERR on stderr and fails when set, else
//	                     prints $TMUX_DISPLAY
//
// An -S <socket> in front of the command is recorded and skipped.
// installFakeTmux and installFakeTmuxWithSessions are copies of today's
// helpers of internal/cli/cli_test.go:1176 and :1264, extended by the three
// answers above; the originals stay with the tests of internal/cli.
const fakeTmuxScript = `#!/bin/sh
printf '%s\n' "$*" >> "$TMUX_LOG"
if [ "$1" = -S ]; then shift 2; fi
case "$1" in
has-session)
	for s in $TMUX_HAS; do
		if [ "$s" = "$3" ]; then exit 0; fi
	done
	echo "can't find session: $3" >&2
	exit 1;;
list-sessions)
	printf '%s\n' "$TMUX_SESSIONS";;
display-message)
	if [ -n "$TMUX_SLEEP" ]; then sleep "$TMUX_SLEEP"; fi
	if [ -n "$TMUX_ERR" ]; then echo "$TMUX_ERR" >&2; exit 1; fi
	printf '%s\n' "$TMUX_DISPLAY";;
esac
exit 0
`

func installFakeTmux(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmux.log")
	fakeTmux := filepath.Join(bin, "tmux")
	if err := os.WriteFile(fakeTmux, []byte(fakeTmuxScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", log)
	t.Setenv("TMUX", "")
	for _, name := range []string{"TMUX_HAS", "TMUX_SESSIONS", "TMUX_SLEEP", "TMUX_ERR", "TMUX_DISPLAY"} {
		t.Setenv(name, "")
	}
	if p, err := exec.LookPath("tmux"); err != nil || p != fakeTmux {
		t.Fatalf("the fake tmux is not the tmux on PATH: %q, %v", p, err)
	}
	return log
}

func installFakeTmuxWithSessions(t *testing.T, sessions string) string {
	t.Helper()
	log := installFakeTmux(t)
	t.Setenv("TMUX_SESSIONS", sessions)
	return log
}

// tmuxHas makes the fake answer has-session yes for these sessions.
func tmuxHas(t *testing.T, sessions ...string) {
	t.Setenv("TMUX_HAS", strings.Join(sessions, " "))
}

// calls returns the lines of a log of a fake program; nil when it is empty.
func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// wantCalls fails the test unless the log holds exactly want, in order.
func wantCalls(t *testing.T, log string, want ...string) {
	t.Helper()
	if got := calls(t, log); !slices.Equal(got, want) {
		t.Fatalf("calls of the fake =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// wantNoCall fails the test when any call of the log starts with one of
// the prefixes.
func wantNoCall(t *testing.T, log string, prefixes ...string) {
	t.Helper()
	for _, c := range calls(t, log) {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				t.Errorf("the fake was called with %q, which must not run here", c)
			}
		}
	}
}

// wantEmpty fails the test when the fake was called at all.
func wantEmpty(t *testing.T, log string) {
	t.Helper()
	if got := calls(t, log); len(got) != 0 {
		t.Fatalf("the fake was called:\n  %s", strings.Join(got, "\n  "))
	}
}
