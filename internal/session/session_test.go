package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket, a state directory or a multiplexer reaches
// them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-session-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// The table of internal/tmux/tmux_test.go (TestSessionName), plus cases that
// pin the rest of the rule.
func TestNameMatchesTheOldSessionName(t *testing.T) {
	tests := []struct{ project, worktree, want string }{
		{"project", "main", "project-main"},
		{"My Project", "feature/test", "my-project-feature-test"},
		{"project", "feature.test_more", "project-feature-test-more"},
		{"!!!", "???", "devtools"},
		{"", "", "devtools"},
		{"devtools", "", "devtools"},
		{"", "main", "main"},
		{"-a-", "-b-", "a-b"},
		{"Über", "Straße", "über-straße"},
		{"a  b", "c__d", "a-b-c-d"},
		{"x1", "2y", "x1-2y"},
	}
	for _, tc := range tests {
		if got := Name(tc.project, tc.worktree); got != tc.want {
			t.Errorf("Name(%q, %q) = %q, want %q", tc.project, tc.worktree, got, tc.want)
		}
	}
}

func TestSanitizeRemovesControlAndBidiCharacters(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"a\tb\nc\rd", "abcd"},
		{"\x00\x01\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"del\x7f", "del"},
		{"c1\u0080\u0085\u009b\u009f", "c1"},
		{"\u200eleft\u200f", "left"},
		{"\u202ax\u202by\u202cz\u202dw\u202e", "xyzw"},
		{"\u2066a\u2067b\u2068c\u2069", "abc"},
		{"keep \u00a0nbsp and ä", "keep \u00a0nbsp and ä"},
		{"bad \xff byte", "bad � byte"},
		{"", ""},
	}
	for _, tc := range tests {
		got := Sanitize(tc.in, 1000)
		if got != tc.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("Sanitize(%q) = %q is not valid UTF-8", tc.in, got)
		}
	}
	// Every removed character, one by one.
	for r := rune(0); r <= 0x2070; r++ {
		bad := r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
			r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
		got := Sanitize("a"+string(r)+"b", 10)
		if bad && got != "ab" {
			t.Errorf("U+%04X kept: %q", r, got)
		}
		if !bad && got != "a"+string(r)+"b" {
			t.Errorf("U+%04X removed: %q", r, got)
		}
	}
}

func TestSanitizeCutsAtRunes(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 5, "hello"},
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"äöüäöü", 4, "äöü…"},
		{"日本語のテキスト", 3, "日本…"},
		{"ab", 1, "…"},
		{"a", 1, "a"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		// Removed characters do not count, before or after the cut.
		{"a\x1bb\x1bc", 3, "abc"},
		{"abc\x1b\x1b", 3, "abc"},
		{"\u202e\u202e\u202eabcd", 3, "ab…"},
	}
	for _, tc := range tests {
		got := Sanitize(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("Sanitize(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if n := utf8.RuneCountInString(got); tc.max > 0 && n > tc.max {
			t.Errorf("Sanitize(%q, %d) has %d runes", tc.in, tc.max, n)
		}
	}
	long := strings.Repeat("x", MaxText*2)
	if got := Sanitize(long, MaxText); utf8.RuneCountInString(got) != MaxText || !strings.HasSuffix(got, "…") {
		t.Errorf("cut to MaxText: %d runes, %q at the end", utf8.RuneCountInString(got), got[len(got)-4:])
	}
}

func TestValidMachineRejectsOptionsAndSeparators(t *testing.T) {
	good := []string{
		"macmini", "macbookpro", "desktop", "a", "A1", "host.local", "my_host",
		"my-host", "10.0.0.1", "x" + strings.Repeat("y", 63),
	}
	for _, name := range good {
		if !ValidMachine(name) {
			t.Errorf("ValidMachine(%q) = false", name)
		}
	}
	bad := []string{
		"", "-oProxyCommand=sh", "-", "--", ".hidden", "_x", "user@host",
		"host:22", "host/path", "a b", "a\tb", "a\nb", "a;b", "a|b", "$(x)",
		"`x`", "a'b", "a\"b", "host,other", "ä", "a\x00", "x" + strings.Repeat("y", 64),
		"[::1]", "a=b", "a*", "a?", "~a", "a\\b",
	}
	for _, name := range bad {
		if ValidMachine(name) {
			t.Errorf("ValidMachine(%q) = true", name)
		}
	}
}

func TestValidIDAcceptsOnlyServerIds(t *testing.T) {
	good := []string{"s1", "t17", "p52", "s0", "p" + strings.Repeat("9", 18)}
	for _, id := range good {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false", id)
		}
	}
	bad := []string{
		"", "s", "p", "x1", "c7", "S1", "P52", "s-1", "s1a", " s1", "s1 ",
		"p" + strings.Repeat("9", 19), "s١", "s1\n", "../p1", "p+1",
	}
	for _, id := range bad {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true", id)
		}
	}
}

func TestKeyResolvesSymlinks(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real", "dir")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{
		real,
		real + "/",
		filepath.Join(link, "dir"),
		filepath.Join(base, "real", ".", "dir"),
		filepath.Join(base, "real", "dir", "..", "dir"),
		link + "/./dir/",
	} {
		got, err := Key(in)
		if err != nil {
			t.Fatalf("Key(%q): %v", in, err)
		}
		if got != real {
			t.Errorf("Key(%q) = %q, want %q", in, got, real)
		}
	}

	// A relative directory is taken from the working directory.
	t.Chdir(filepath.Join(base, "real"))
	if got, err := Key("dir"); err != nil || got != real {
		t.Errorf("Key(\"dir\") = %q, %v; want %q", got, err, real)
	}

	if _, err := Key(filepath.Join(base, "missing")); err == nil {
		t.Errorf("Key of a missing directory did not fail")
	}
	dangling := filepath.Join(base, "dangling")
	if err := os.Symlink(filepath.Join(base, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := Key(dangling); err == nil {
		t.Errorf("Key of a dangling link did not fail")
	}
}
