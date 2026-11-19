package cli

// devtools attach and devtools agents (4.5 of the plan, P2-08b). Both are UI
// commands: they go through enter on every backend, which inside the
// backend opens its hub (ui.hub on devtools, the standalone hub on tmux and
// herdr) and in a plain terminal runs the guard and Enter.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/proto"

	"github.com/spf13/cobra"
)

// alreadyInside is what attach prints when it opened the hub of the window
// that shows the caller's pane (4.5, 3.5.6 N1).
//
// It is not in internal/ui/words.go, where 5a.14 wants every visible text;
// adding it there is outside the files of P2-08b and is in its report.
const alreadyInside = "Already inside devtools."

func attachCommand(a *app, stdout io.Writer) *cobra.Command {
	return feature(featAttach, &cobra.Command{
		Use:   "attach",
		Short: "Turn this terminal into a window of the multiplexer, or open the hub inside one",
		Args:  usageNoArgs("usage: devtools attach"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.enter(mux.Entry{}); err != nil {
				return err
			}
			// the hub opened in the window around the caller: a backend
			// that draws the window itself says so
			if m, c, err := a.multiplexer(); err == nil {
				if _, inside := c.Inside(); inside && m.Caps().Has(mux.CapWindow) {
					fmt.Fprintln(stdout, alreadyInside)
				}
			}
			return nil
		},
	})
}

func agentsCommand(a *app, stdout io.Writer) *cobra.Command {
	var asJSON bool
	cmd := feature(featAgents, &cobra.Command{
		Use:   "agents",
		Short: "Open the hub on the agents, the ones needing attention first",
		Long: "Open the hub on the agents, the ones needing attention first.\n\n" +
			"With --json, print the agents of this machine only, one JSON object per\n" +
			"line with the field names of the wire protocol and \"machine\":\"\"; it asks\n" +
			"no other machine.",
		Args: usageNoArgs("usage: devtools agents [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON {
				return a.agentsJSON(stdout)
			}
			return a.enter(mux.Entry{Hub: &mux.HubRequest{Mode: hub.ModeAgents}})
		},
	})
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the agents of this machine only, one JSON object per line")
	return cmd
}

// agentJSON is one line of agents --json: the agent record of the wire
// (4.2.2) and the machine, always "" (F34).
type agentJSON struct {
	proto.Agent
	Machine string `json:"machine"`
}

// agentsJSON prints the agents of this machine. A backend with the layout
// capability has the agent records of the wire; another one has what its
// Agents reports.
func (a *app) agentsJSON(stdout io.Writer) error {
	m, _, err := a.multiplexer()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var agents []proto.Agent
	if m.Caps().Has(mux.CapLayout) {
		snap, err := m.Snapshot(ctx)
		if err != nil {
			return err
		}
		agents = snap.Agents
	} else {
		items, err := m.Agents(ctx)
		if err != nil {
			return unsupportedWords(err)
		}
		for _, it := range items {
			agents = append(agents, proto.Agent{
				Pane: it.Pane, Tab: it.Tab, Session: it.SessionID,
				Kind: it.Agent, Status: it.Status, Title: it.State.Title, Cwd: it.Path,
			})
		}
	}
	enc := json.NewEncoder(stdout)
	for _, ag := range agents {
		if err := enc.Encode(agentJSON{Agent: ag}); err != nil {
			return err
		}
	}
	return nil
}
