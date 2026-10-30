// Package herdr maps devtools sessions onto herdr workspaces: a tmux session
// becomes a workspace labeled with the same name, tmux windows become tabs.
package herdr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Active reports whether devtools runs inside herdr, either in a pane or in
// a custom command, and not inside a tmux nested in it.
func Active() bool {
	if os.Getenv("TMUX") != "" {
		return false
	}
	return os.Getenv("HERDR_ENV") == "1" || os.Getenv("HERDR_SOCKET_PATH") != ""
}

type Workspace struct {
	ID      string `json:"workspace_id"`
	Label   string `json:"label"`
	Tabs    int    `json:"tab_count"`
	Focused bool   `json:"focused"`
}

func ListWorkspaces() ([]Workspace, error) {
	out, err := output("workspace", "list")
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

func ListTabs(workspace string) ([]Tab, error) {
	out, err := output("tab", "list", "--workspace", workspace)
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
func TabPanes(workspace string) (map[string][]string, error) {
	out, err := output("pane", "list", "--workspace", workspace)
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

func CapturePane(pane string) (string, error) {
	out, err := output("pane", "read", pane, "--source", "visible", "--format", "ansi")
	return string(out), err
}

// Switch focuses the workspace for a session, creating it in dir first.
func Switch(dir, session string) error {
	workspace, err := find(session)
	if err != nil {
		return err
	}
	if workspace == "" {
		_, err := output("workspace", "create", "--cwd", dir, "--label", session, "--focus")
		return err
	}
	return SwitchWorkspace(workspace)
}

func SwitchWorkspace(workspace string) error {
	_, err := output("workspace", "focus", workspace)
	return err
}

// CloseForRemoval moves over to the fallback session and returns a function
// that closes the workspace of a session about to be removed. herdr kills
// every process in a workspace it closes, a devtools running in it included,
// so only call it once the removal is done.
func CloseForRemoval(targetSession, fallbackSession, fallbackDir string) (func() error, error) {
	target, err := find(targetSession)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return func() error { return nil }, nil
	}
	if targetSession != fallbackSession {
		if err := Switch(fallbackDir, fallbackSession); err != nil {
			return nil, err
		}
	}
	return func() error {
		_, err := output("workspace", "close", target)
		return err
	}, nil
}

func find(session string) (string, error) {
	workspaces, err := ListWorkspaces()
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

func output(args ...string) ([]byte, error) {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	cmd := exec.Command(bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
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
