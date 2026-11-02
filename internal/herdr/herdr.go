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

// Inside reports whether devtools runs inside herdr, either in a pane or in
// a custom command, so the API reaches the server it is attached to.
func Inside() bool {
	return os.Getenv("HERDR_ENV") == "1" || os.Getenv("HERDR_SOCKET_PATH") != ""
}

// Installed reports whether a herdr binary can be run.
func Installed() bool {
	if path := os.Getenv("HERDR_BIN_PATH"); path != "" {
		stat, err := os.Stat(path)
		return err == nil && !stat.IsDir() && stat.Mode()&0o111 != 0
	}
	_, err := exec.LookPath("herdr")
	return err == nil
}

// Client runs herdr api commands, on a saved ssh machine when Machine is set.
type Client struct {
	Machine string
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

type Tab struct {
	ID     string `json:"tab_id"`
	Number int    `json:"number"`
	Label  string `json:"label"`
}

func (c Client) ListTabs(workspace string) ([]Tab, error) {
	out, err := c.output("tab", "list", "--workspace", workspace)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result struct {
			Tabs []Tab `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unexpected herdr tab list output: %w", err)
	}
	return resp.Result.Tabs, nil
}

// TabPanes returns the pane ids of a workspace grouped by tab, in layout
// order.
func (c Client) TabPanes(workspace string) (map[string][]string, error) {
	out, err := c.output("pane", "list", "--workspace", workspace)
	if err != nil {
		return nil, err
	}
	return parsePaneList(out)
}

func parsePaneList(out []byte) (map[string][]string, error) {
	var resp struct {
		Result struct {
			Panes []struct {
				ID  string `json:"pane_id"`
				Tab string `json:"tab_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unexpected herdr pane list output: %w", err)
	}
	panes := make(map[string][]string)
	for _, pane := range resp.Result.Panes {
		panes[pane.Tab] = append(panes[pane.Tab], pane.ID)
	}
	return panes, nil
}

func (c Client) CapturePane(pane string) (string, error) {
	out, err := c.output("pane", "read", pane, "--source", "visible", "--format", "ansi")
	return string(out), err
}

// Switch focuses the workspace for a session, creating it in dir first.
func (c Client) Switch(dir, session string) error {
	workspace, err := c.find(session)
	if err != nil {
		return err
	}
	if workspace == "" {
		_, err := c.output("workspace", "create", "--cwd", dir, "--label", session, "--focus")
		return err
	}
	return c.FocusWorkspace(workspace)
}

func (c Client) FocusWorkspace(workspace string) error {
	_, err := c.output("workspace", "focus", workspace)
	return err
}

// CloseForRemoval moves over to the fallback session and returns a function
// that closes the workspace of a session about to be removed. herdr kills
// every process in a workspace it closes, a devtools running in it included,
// so only call it once the removal is done. Outside herdr there is nothing
// to move away from, and nothing to close without a running server.
func (c Client) CloseForRemoval(targetSession, fallbackSession, fallbackDir string) (func() error, error) {
	noop := func() error { return nil }
	inside := Inside()
	if !inside && c.Machine == "" {
		running, err := ServerRunning()
		if err != nil || !running {
			return noop, err
		}
	}
	target, err := c.find(targetSession)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return noop, nil
	}
	if inside && targetSession != fallbackSession {
		if err := c.Switch(fallbackDir, fallbackSession); err != nil {
			return nil, err
		}
	}
	return func() error {
		_, err := c.output("workspace", "close", target)
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

func (c Client) args(args ...string) []string {
	if c.Machine == "" {
		return args
	}
	return append([]string{"--machine", c.Machine}, args...)
}

func (c Client) output(args ...string) ([]byte, error) {
	return output(c.args(args...)...)
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
