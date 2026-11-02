package herdr

import (
	"encoding/json"
	"fmt"
	"sort"
)

type Agent struct {
	Pane      string `json:"pane_id"`
	Workspace string `json:"workspace_id"`
	Name      string `json:"name"`
	Kind      string `json:"agent"`
	Status    string `json:"agent_status"`
	Title     string `json:"terminal_title_stripped"`
	Cwd       string `json:"cwd"`
	Focused   bool   `json:"focused"`
}

// ListAgents returns the agents of all workspaces, the ones needing
// attention first.
func (c Client) ListAgents() ([]Agent, error) {
	out, err := c.output("agent", "list")
	if err != nil {
		return nil, err
	}
	agents, err := parseAgentList(out)
	if err != nil {
		return nil, err
	}
	SortAgents(agents)
	return agents, nil
}

func parseAgentList(out []byte) ([]Agent, error) {
	var resp struct {
		Result struct {
			Agents []Agent `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("unexpected herdr agent list output: %w", err)
	}
	return resp.Result.Agents, nil
}

var statusRank = map[string]int{
	"blocked": 0,
	"done":    1,
	"idle":    2,
	"working": 3,
	"unknown": 4,
}

// SortAgents orders agents by how urgently they want the user: blocked on a
// question, then done, idle, working and unknown, keeping herdr's order
// within a status.
func SortAgents(agents []Agent) {
	rank := func(status string) int {
		if r, ok := statusRank[status]; ok {
			return r
		}
		return len(statusRank)
	}
	sort.SliceStable(agents, func(i, j int) bool {
		return rank(agents[i].Status) < rank(agents[j].Status)
	})
}

// ReadAgent returns the visible screen of an agent's pane.
func (c Client) ReadAgent(target string) (string, error) {
	out, err := c.output("agent", "read", target, "--source", "visible", "--format", "ansi")
	return string(out), err
}

func (c Client) FocusAgent(target string) error {
	_, err := c.output("agent", "focus", target)
	return err
}
