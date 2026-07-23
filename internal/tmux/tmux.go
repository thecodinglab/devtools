package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"
)

type Session struct {
	Name     string
	Windows  int
	Attached bool
}

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

func ListSessions() ([]Session, error) {
	out, err := output("tmux", "list-sessions", "-F", "#{session_name}\t#{session_windows}\t#{session_attached}")
	if err != nil {
		return nil, err
	}
	return parseSessionList(out)
}

func parseSessionList(out string) ([]Session, error) {
	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected tmux list-sessions output %q", line)
		}
		windows, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("unexpected tmux session window count %q", parts[1])
		}
		sessions = append(sessions, Session{
			Name:     parts[0],
			Windows:  windows,
			Attached: parts[2] != "0",
		})
	}
	return sessions, nil
}

type Window struct {
	ID    string
	Index int
	Name  string
}

func ListWindows(session string) ([]Window, error) {
	out, err := output("tmux", "list-windows", "-t", session, "-F", "#{window_id}\t#{window_index}\t#{window_name}")
	if err != nil {
		return nil, err
	}
	return parseWindowList(out)
}

func parseWindowList(out string) ([]Window, error) {
	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected tmux list-windows output %q", line)
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("unexpected tmux window index %q", parts[1])
		}
		windows = append(windows, Window{ID: parts[0], Index: index, Name: parts[2]})
	}
	return windows, nil
}

func CapturePane(target string) (string, error) {
	return output("tmux", "capture-pane", "-ep", "-t", target)
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

func SwitchSession(session string) error {
	if os.Getenv("TMUX") == "" {
		return runAttached("tmux", "attach-session", "-t", session)
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

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
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
