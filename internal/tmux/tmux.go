package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

func SessionName(project, worktree string) string {
	name := strings.Trim(project+"-"+worktree, "-")
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	clean := strings.Trim(b.String(), "-")
	if clean == "" {
		return "devtools"
	}
	return clean
}

func Switch(dir, session string) error {
	if os.Getenv("TMUX") == "" {
		return runAttached("tmux", "new-session", "-A", "-s", session, "-c", dir)
	}
	if !hasSession(session) {
		if err := run("tmux", "new-session", "-d", "-s", session, "-c", dir); err != nil {
			return err
		}
	}
	return run("tmux", "switch-client", "-t", session)
}

func CloseForRemoval(targetSession, fallbackSession, fallbackDir string) error {
	if os.Getenv("TMUX") != "" {
		if targetSession == fallbackSession {
			if err := run("tmux", "detach-client"); err != nil {
				return err
			}
		} else {
			if err := Switch(fallbackDir, fallbackSession); err != nil {
				return err
			}
		}
	}
	if hasSession(targetSession) {
		return run("tmux", "kill-session", "-t", targetSession)
	}
	return nil
}

func hasSession(session string) bool {
	err := run("tmux", "has-session", "-t", session)
	return err == nil
}

func runAttached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), err)
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}
