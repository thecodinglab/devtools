package cli

// The feature gate (step 4 of 5.3.8 of the plan, P2-08b). Every registered
// command carries the id of its row in mux.Features. Before the command's
// own code runs, the gate looks the row up; when it needs a capability and
// is not Late, the selected backend must have it, else the command prints
// the unsupported words and ends with status 1 having done nothing (4.5).
// The gate asks for capabilities and never for a backend (5.3.10).

import (
	"fmt"

	"devtools/internal/mux"
	"devtools/internal/ui"

	"github.com/spf13/cobra"
)

// featureKey is the annotation of a command that holds its feature id.
const featureKey = "devtools.feature"

// Feature ids of the commands (the rows of kind command of mux.Features).
const (
	featAttach      = "cmd.attach"
	featPick        = "cmd.pick"
	featSessions    = "cmd.sessions"
	featAgents      = "cmd.agents"
	featAgentsJSON  = "cmd.agents.json"
	featWork        = "cmd.work"
	featMerge       = "cmd.merge"
	featBookmark    = "cmd.bookmark"
	featSessionList = "cmd.session.list"
	featPaneList    = "cmd.pane.list"
	featPaneFocus   = "cmd.pane.focus"
	featKeys        = "cmd.keys"
	featServer      = "cmd.server"
)

// feature marks cmd with its feature id and returns it.
func feature(id string, cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[featureKey] = id
	return cmd
}

// featureOf is the feature id of the command that runs: its own, else the
// one of its nearest parent that has one. agents --json is a row of its
// own.
func featureOf(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		if id := c.Annotations[featureKey]; id != "" {
			if id == featAgents {
				if f := cmd.Flags().Lookup("json"); f != nil && f.Changed {
					return featAgentsJSON
				}
			}
			return id
		}
	}
	return ""
}

// featureRow is the row of mux.Features with the id.
func featureRow(id string) (mux.Feature, bool) {
	for _, f := range mux.Features {
		if f.ID == id {
			return f, true
		}
	}
	return mux.Feature{}, false
}

// gate runs before every command (the PersistentPreRunE of the root). A
// command without a feature id reaches no multiplexer; cmd.server is not
// gated and never constructs one. A row without a need, and the Late row
// cmd.work, pass: their commands select the backend themselves.
func (a *app) gate(cmd *cobra.Command) error {
	id := featureOf(cmd)
	if id == "" {
		return nil
	}
	f, ok := featureRow(id)
	if !ok {
		return fmt.Errorf("devtools: the command %s has no row %s in the feature table", cmd.CommandPath(), id)
	}
	if f.Needs == "" || f.Late {
		return nil
	}
	m, _, err := a.multiplexer()
	if err != nil {
		return err
	}
	if err := mux.Need(m, f.Needs); err != nil {
		return unsupportedWords(err)
	}
	return nil
}

// unsupportedWords is what a person reads for an *mux.ErrUnsupported; every
// other error passes as it is.
func unsupportedWords(err error) error {
	if u, ok := err.(*mux.ErrUnsupported); ok {
		return words(ui.Message("unsupported", string(u.Backend), string(u.Capability)))
	}
	return err
}
