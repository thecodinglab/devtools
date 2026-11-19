package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	Name     string
	Windows  int
	Attached bool
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

// The functions below are the ones the tmux backend of internal/backend
// calls (P2-14, 5.3.11 of the plan). None of them reads the environment:
// whether the caller is inside tmux is the argument inside of Remove, and
// the TMUX variable reaches PaneTTY and Alive as their argument.

// checkLimit bounds each of the two liveness checks, PaneTTY and Alive.
const checkLimit = 500 * time.Millisecond

// Has reports whether the tmux server has a session of this name.
func Has(session string) bool { return hasSession(session) }

// NewDetached creates the session in dir without attaching a client to it.
func NewDetached(session, dir string) error {
	return run("tmux", "new-session", "-d", "-s", session, "-c", dir)
}

// SwitchClient switches the current client to the session. It starts no
// client: inside tmux the current client is the one of the caller's pane.
func SwitchClient(session string) error {
	return run("tmux", "switch-client", "-t", session)
}

// Attach runs a tmux client in this terminal until it detaches or ends. It
// is the only function that starts a tmux client. With a dir it attaches
// to the session or creates it there (new-session -A); with a session and
// no dir it attaches to that session; with neither it attaches to the
// session tmux used last.
func Attach(session, dir string) error {
	switch {
	case dir != "" && session != "":
		return runAttached("tmux", "new-session", "-A", "-s", session, "-c", dir)
	case session != "":
		return runAttached("tmux", "attach-session", "-t", session)
	}
	return runAttached("tmux", "attach-session")
}

// Remove leaves the session target and ends it, for merge and done. Inside
// tmux the client first moves to fallback, created detached in fallbackDir
// when it is missing, or detaches when fallback is target itself; outside
// there is no client to move. Then the session is killed when it exists.
func Remove(target, fallback, fallbackDir string, inside bool) error {
	if inside {
		if target == fallback {
			if err := run("tmux", "detach-client"); err != nil {
				return err
			}
		} else {
			if !Has(fallback) {
				if err := NewDetached(fallback, fallbackDir); err != nil {
					return err
				}
			}
			if err := SwitchClient(fallback); err != nil {
				return err
			}
		}
	}
	if Has(target) {
		return run("tmux", "kill-session", "-t", target)
	}
	return nil
}

// PaneTTY asks the tmux server named in tmuxVar (the value of TMUX,
// "socket,pid,session") for the terminal device of pane. known is false
// with a nil error when the question was answered and the answer is no:
// the socket does not exist or refuses, or the server has no such pane. An
// error means it was not answered: the limit of 500 ms ended first, or
// anything else failed.
func PaneTTY(tmuxVar, pane string) (tty string, known bool, err error) {
	out, answered, err := ask(tmuxVar, "display-message", "-p", "-t", pane, "#{pane_tty}")
	if err != nil || !answered {
		return "", false, err
	}
	tty = strings.TrimSpace(out)
	if tty == "" {
		return "", false, nil
	}
	return tty, true, nil
}

// Alive reports whether the tmux server named in tmuxVar is the one that
// set it: its pid equals the pid field of the variable. alive is false
// with a nil error when the socket does not exist or refuses, or when
// another server answers there; an error as for PaneTTY.
func Alive(tmuxVar string) (alive bool, err error) {
	_, pid, ok := splitTmuxVar(tmuxVar)
	if !ok {
		return false, fmt.Errorf("tmux: malformed TMUX value %q", tmuxVar)
	}
	out, answered, err := ask(tmuxVar, "display-message", "-p", "#{pid}")
	if err != nil || !answered {
		return false, err
	}
	return strings.TrimSpace(out) == pid, nil
}

// Installed reports whether a tmux program is found on PATH.
func Installed() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// splitTmuxVar splits the value of TMUX, "socket,pid,session", into the
// socket path and the pid. The socket path may itself hold commas, so the
// two fields are taken from the end.
func splitTmuxVar(v string) (socket, pid string, ok bool) {
	i := strings.LastIndexByte(v, ',')
	if i < 0 {
		return "", "", false
	}
	rest := v[:i]
	j := strings.LastIndexByte(rest, ',')
	if j <= 0 {
		return "", "", false
	}
	return rest[:j], rest[j+1:], true
}

// ask runs one tmux command against the socket named in tmuxVar within
// checkLimit. answered is false with a nil error when the answer is "no":
// the socket does not exist, nothing listens there, or the server knows no
// such target. Every other failure, the limit included, is an error.
func ask(tmuxVar string, args ...string) (out string, answered bool, err error) {
	socket, _, ok := splitTmuxVar(tmuxVar)
	if !ok {
		return "", false, fmt.Errorf("tmux: malformed TMUX value %q", tmuxVar)
	}
	if _, err := os.Stat(socket); errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkLimit)
	defer cancel()
	argv := append([]string{"-S", socket}, args...)
	cmd := exec.CommandContext(ctx, "tmux", argv...)
	// a tmux that left a child behind holding the pipes is not waited for
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return "", false, fmt.Errorf("tmux %s: no answer within %s", strings.Join(argv, " "), checkLimit)
	}
	if runErr == nil {
		return stdout.String(), true, nil
	}
	msg := strings.TrimSpace(stderr.String())
	var exit *exec.ExitError
	if errors.As(runErr, &exit) && saysNo(msg) {
		return "", false, nil
	}
	if msg == "" {
		msg = runErr.Error()
	}
	return "", false, fmt.Errorf("tmux %s: %s", strings.Join(argv, " "), msg)
}

// saysNo reports whether tmux's error text is one of the answers "no": no
// server listens on the socket, or the server has no such pane, window or
// session.
func saysNo(msg string) bool {
	for _, no := range []string{
		"no server running on",
		"Connection refused",
		"can't find pane",
		"can't find window",
		"can't find session",
	} {
		if strings.Contains(msg, no) {
			return true
		}
	}
	return false
}
