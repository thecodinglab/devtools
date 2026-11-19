// Package herdr maps devtools sessions onto herdr workspaces: a tmux session
// becomes a workspace labeled with the same name, tmux windows become tabs.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Installed reports whether a herdr binary can be run.
func Installed() bool {
	if path := os.Getenv("HERDR_BIN_PATH"); path != "" {
		stat, err := os.Stat(path)
		return err == nil && !stat.IsDir() && stat.Mode()&0o111 != 0
	}
	_, err := exec.LookPath("herdr")
	return err == nil
}

// Client runs herdr api commands against the server of this machine.
type Client struct{}

func (c Client) output(args ...string) ([]byte, error) {
	return output(args...)
}

type Workspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	Tabs        int    `json:"tab_count"`
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
}

func (c Client) ListWorkspaces() ([]Workspace, error) {
	out, err := c.output("workspace", "list")
	if err != nil {
		return nil, err
	}
	return parseWorkspaceList(out)
}

func parseWorkspaceList(out []byte) ([]Workspace, error) {
	var resp struct {
		Result struct {
			Workspaces []Workspace `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unexpected herdr workspace list output: %w", err)
	}
	return resp.Result.Workspaces, nil
}

// Switch focuses the workspace for a session, creating it in dir first.
func (c Client) Switch(dir, session string) error {
	workspace, err := c.find(session)
	if err != nil {
		return err
	}
	if workspace != "" {
		return c.FocusWorkspace(workspace)
	}
	_, err = c.output("workspace", "create", "--cwd", dir, "--label", session, "--focus")
	return err
}

func (c Client) FocusWorkspace(workspace string) error {
	_, err := c.output("workspace", "focus", workspace)
	return err
}

// Remove is CloseForRemoval for the herdr backend of internal/backend
// (P2-14): whether the caller is inside herdr is the argument inside, not
// read from the environment. Inside herdr it moves over to the fallback
// session first; outside there is nothing to move away from, and nothing
// to close without a running server. The returned function closes the
// workspace of target; herdr kills every process in it, a devtools running
// in it included, so call it once the removal is done.
func (c Client) Remove(target, fallback, fallbackDir string, inside bool) (func() error, error) {
	noop := func() error { return nil }
	if !inside {
		running, err := ServerRunning()
		if err != nil || !running {
			return noop, err
		}
	}
	workspace, err := c.find(target)
	if err != nil {
		return nil, err
	}
	if workspace == "" {
		return noop, nil
	}
	if inside && target != fallback {
		if err := c.Switch(fallbackDir, fallback); err != nil {
			return nil, err
		}
	}
	return func() error {
		_, err := c.output("workspace", "close", workspace)
		return err
	}, nil
}

func (c Client) find(session string) (string, error) {
	workspaces, err := c.ListWorkspaces()
	if err != nil {
		return "", err
	}
	for _, workspace := range workspaces {
		if workspace.Label == session {
			return workspace.ID, nil
		}
	}
	return "", nil
}

func bin() string {
	if path := os.Getenv("HERDR_BIN_PATH"); path != "" {
		return path
	}
	return "herdr"
}

func output(args ...string) ([]byte, error) {
	return outputContext(context.Background(), args...)
}

// outputContext kills herdr once ctx is done, and stops waiting for output
// from processes it left behind shortly after.
func outputContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin(), args...)
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("herdr %s: no answer in time", strings.Join(args, " "))
		}
		return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), errorMessage(stderr.Bytes(), err))
	}
	return stdout.Bytes(), nil
}

// errorMessage extracts the message of a herdr json error, falling back to
// the raw output.
func errorMessage(stderr []byte, err error) string {
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(stderr, &resp) == nil && resp.Error.Message != "" {
		return resp.Error.Message
	}
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return msg
	}
	return err.Error()
}
