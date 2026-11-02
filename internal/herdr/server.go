package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

var (
	// serverStatusTimeout bounds asking a server whether it runs, which a
	// server that accepts but never answers would block forever.
	serverStatusTimeout = 5 * time.Second
	// serverStartTimeout bounds the wait for a started server to answer.
	serverStartTimeout = 10 * time.Second
)

func ServerRunning() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), serverStatusTimeout)
	defer cancel()
	return serverRunning(ctx)
}

func serverRunning(ctx context.Context) (bool, error) {
	out, err := outputContext(ctx, "status", "server", "--json")
	if err != nil {
		return false, err
	}
	return parseServerStatus(out)
}

func parseServerStatus(out []byte) (bool, error) {
	var status struct {
		Running *bool `json:"running"`
	}
	if err := json.Unmarshal(out, &status); err != nil || status.Running == nil {
		return false, fmt.Errorf("unexpected herdr status server output: %q", strings.TrimSpace(string(out)))
	}
	return *status.Running, nil
}

// EnsureServer starts a herdr server detached from devtools when none runs,
// so it outlives devtools like a tmux server does, and waits until it
// answers.
func EnsureServer() error {
	running, err := ServerRunning()
	if err != nil || running {
		return err
	}
	if err := startServer(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), serverStartTimeout)
	defer cancel()
	for {
		running, err := serverRunning(ctx)
		if running {
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("herdr server did not start within %s", serverStartTimeout)
		}
		if err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func startServer() error {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(bin(), "server")
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	// keep the server from pinning the cwd, a worktree that may be removed
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("herdr server: %w", err)
	}
	return cmd.Process.Release()
}

// Attach runs the herdr client in the terminal until it detaches or quits.
// A new client shows the workspace the server focused last.
func Attach(args ...string) error {
	cmd := exec.Command(bin(), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("herdr %s: %s", strings.Join(args, " "), err)
	}
	return nil
}

// Visit runs focus against the local server and, outside herdr, first makes
// sure a server runs and afterwards attaches the terminal to it, like tmux
// new-session -A.
func Visit(focus func() error) error {
	if Inside() {
		return focus()
	}
	if err := EnsureServer(); err != nil {
		return err
	}
	if err := focus(); err != nil {
		return err
	}
	return Attach()
}
