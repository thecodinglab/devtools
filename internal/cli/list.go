package cli

// devtools session list --json and devtools pane list --json (4.5 of the
// plan, P2-08b): the sessions or the panes of the server, one JSON object
// per line with the field names of the wire (4.2.2), from m.Snapshot. Both
// need the layout capability; the gate refuses them on a backend without it.

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/spf13/cobra"
)

// exit is an error that ends the command with a status of its own; its
// text, when there is one, is printed on standard error.
type exit struct {
	code int
	text string
}

func (e *exit) Error() string { return e.text }

func sessionCommand(a *app, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session <command>",
		Short: "List the sessions of the devtools server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(snapshotListCommand(a, stdout, featSessionList, "devtools session list --json",
		"Print the sessions of this machine, one JSON object per line",
		func(enc *json.Encoder, snap snapshotLists) error {
			for _, s := range snap.sessions() {
				if err := enc.Encode(s); err != nil {
					return err
				}
			}
			return nil
		}))
	return cmd
}

func paneCommand(a *app, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pane <command>",
		Short: "List the panes of the devtools server, or move the focus between them",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		snapshotListCommand(a, stdout, featPaneList, "devtools pane list --json",
			"Print the panes of this machine, one JSON object per line",
			func(enc *json.Encoder, snap snapshotLists) error {
				for _, p := range snap.panes() {
					if err := enc.Encode(p); err != nil {
						return err
					}
				}
				return nil
			}),
		paneFocusCommand(a),
	)
	return cmd
}

// snapshotListCommand is a `list --json` command over the snapshot of the
// backend.
func snapshotListCommand(a *app, stdout io.Writer, id, usage, short string, print func(*json.Encoder, snapshotLists) error) *cobra.Command {
	var asJSON bool
	cmd := feature(id, &cobra.Command{
		Use:   "list --json",
		Short: short,
		Args:  usageNoArgs("usage: " + usage),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !asJSON {
				return &exit{code: 2, text: "usage: " + usage}
			}
			m, _, err := a.multiplexer()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			snap, err := m.Snapshot(ctx)
			if err != nil {
				return unsupportedWords(err)
			}
			return print(json.NewEncoder(stdout), snapshotLists{snap})
		},
	})
	cmd.Flags().BoolVar(&asJSON, "json", false, "one JSON object per line, with the field names of the wire")
	return cmd
}
