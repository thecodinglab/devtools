package resume

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"devtools/internal/agent"
)

const id = "2ef64223-3735-4270-b86b-9f035f7e32e0"

// claudeHome makes a Claude Code configuration directory and points
// CLAUDE_CONFIG_DIR at it.
func claudeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return dir
}

func writeSession(t *testing.T, dir string, pid int, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeIsResumableAndCodexIsNot(t *testing.T) {
	if _, ok := For(agent.Claude); !ok {
		t.Fatal("claude has no resumer")
	}
	if _, ok := For(agent.Codex); ok {
		t.Fatal("codex has a resumer")
	}
	if _, ok := For(""); ok {
		t.Fatal("no agent has a resumer")
	}
}

func TestClaudeLookupReadsTheSessionFileOfThePid(t *testing.T) {
	dir := claudeHome(t)
	// The shape Claude Code 2.1.319 writes, shortened.
	writeSession(t, dir, 687252, `{"pid":687252,"sessionId":"`+id+`","cwd":"/home/u/p","startedAt":1795102345107,"version":"2.1.319","kind":"interactive"}`)
	got, ok := Claude{}.Lookup(Process{Pid: 687252})
	if !ok || got != id {
		t.Fatalf("Lookup = %q, %v; want %q", got, ok, id)
	}
}

func TestClaudeLookupFallsBackToTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSession(t, dir, 42, `{"pid":42,"sessionId":"`+id+`"}`)
	if got, ok := (Claude{}).Lookup(Process{Pid: 42}); !ok || got != id {
		t.Fatalf("Lookup = %q, %v", got, ok)
	}
}

func TestClaudeLookupRefusesWhatItCannotTrust(t *testing.T) {
	dir := claudeHome(t)
	for name, c := range map[string]struct {
		pid  int
		body string
	}{
		"no file":        {pid: 1},
		"not json":       {pid: 2, body: `{"pid":2,`},
		"another pid":    {pid: 3, body: `{"pid":4,"sessionId":"` + id + `"}`},
		"no session":     {pid: 5, body: `{"pid":5}`},
		"not a uuid":     {pid: 6, body: `{"pid":6,"sessionId":"x; rm -rf ~"}`},
		"pid zero":       {pid: 0},
		"negative pid":   {pid: -7},
		"id wrong shape": {pid: 8, body: `{"pid":8,"sessionId":"2ef64223x3735-4270-b86b-9f035f7e32e0"}`},
	} {
		if c.body != "" {
			writeSession(t, dir, c.pid, c.body)
		}
		if got, ok := (Claude{}).Lookup(Process{Pid: c.pid}); ok {
			t.Errorf("%s: Lookup = %q, want nothing", name, got)
		}
	}
}

func TestClaudeCommandResumesTheConversation(t *testing.T) {
	if got := (Claude{}).Command(id); !slices.Equal(got, []string{"claude", "--resume", id}) {
		t.Fatalf("Command = %q", got)
	}
}

func TestIsUUID(t *testing.T) {
	for s, want := range map[string]bool{
		id:                                      true,
		"2EF64223-3735-4270-B86B-9F035F7E32E0":  true,
		"":                                      false,
		"2ef64223-3735-4270-b86b-9f035f7e32e":   false,
		"2ef64223-3735-4270-b86b-9f035f7e32e0a": false,
		"2ef64223-3735-4270-b86b-9f035f7e32g0":  false,
		"2ef64223 3735-4270-b86b-9f035f7e32e0":  false,
		"'ef64223-3735-4270-b86b-9f035f7e32e0":  false,
	} {
		if got := isUUID(s); got != want {
			t.Errorf("isUUID(%q) = %v", s, got)
		}
	}
}
