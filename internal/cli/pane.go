package cli

// devtools pane focus --direction (4.5 of the plan, P2-08b): the request
// neovim's window navigation uses. Exit 0 when the focus moved, 1 when there
// is no pane that way, 2 on an error or outside a pane. On a backend without
// pane-focus the gate prints the unsupported words with status 1.

import (
	"devtools/internal/proto"

	"github.com/spf13/cobra"
)

// snapshotLists are the lists of a snapshot the list commands print.
type snapshotLists struct{ snap proto.Snapshot }

func (s snapshotLists) sessions() []proto.Session { return s.snap.Sessions }
func (s snapshotLists) panes() []proto.Pane       { return s.snap.Panes }

func paneFocusCommand(a *app) *cobra.Command {
	var direction string
	usage := "usage: devtools pane focus --direction left|right|up|down"
	cmd := feature(featPaneFocus, &cobra.Command{
		Use:   "focus --direction left|right|up|down",
		Short: "Move the focus to the pane in a direction",
		Args:  usageNoArgs(usage),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch direction {
			case "left", "right", "up", "down":
			default:
				return &exit{code: 2, text: usage}
			}
			m, c, err := a.multiplexer()
			if err != nil {
				return err
			}
			if _, inside := c.Inside(); !inside {
				return &exit{code: 2, text: "devtools pane focus runs inside a pane"}
			}
			moved, err := m.FocusPane(direction)
			if err != nil {
				if w := unsupportedWords(err); w != err {
					return w
				}
				return &exit{code: 2, text: err.Error()}
			}
			if !moved {
				return &exit{code: 1}
			}
			return nil
		},
	})
	cmd.Flags().StringVar(&direction, "direction", "", "left, right, up or down")
	return cmd
}
