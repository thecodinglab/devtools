package resume

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Claude resumes Claude Code. Every running Claude Code writes
// <config>/sessions/<pid>.json, which names its conversation ("sessionId")
// and is rewritten when the conversation changes (/clear); <config> is
// $CLAUDE_CONFIG_DIR, else ~/.claude. Seen with Claude Code 2.1.319; the
// file is not a documented interface, so a Claude Code that stops writing
// it is simply not resumed. The directory is the server's: a
// CLAUDE_CONFIG_DIR set only inside a pane is not seen. `claude --resume
// <id>` opens the conversation again.
type Claude struct{}

// maxSessionFile bounds the read of a session file; the real ones are under
// a kilobyte.
const maxSessionFile = 64 << 10

func (Claude) Lookup(a Process) (string, bool) {
	pid := a.Pid
	if pid <= 0 {
		return "", false
	}
	dir := claudeDir()
	if dir == "" {
		return "", false
	}
	f, err := os.Open(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	var rec struct {
		Pid       int    `json:"pid"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(io.LimitReader(f, maxSessionFile)).Decode(&rec); err != nil {
		return "", false
	}
	if rec.Pid != pid || !isUUID(rec.SessionID) {
		return "", false
	}
	return rec.SessionID, true
}

func (Claude) Valid(id string) bool { return isUUID(id) }

func (Claude) Command(id string) []string { return []string{"claude", "--resume", id} }

// claudeDir is the configuration directory of Claude Code for the server's
// user, "" when there is no home.
func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}
