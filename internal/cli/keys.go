package cli

// devtools keys (4.5 of the plan, P2-08b): every effective binding of the
// client after the bind and unbind lines were applied, then the fixed keys
// of the hub (5a.11.3) and of copy mode (5a.11.4), one per line: chord,
// action, argument. It needs no server. On a backend without window the
// gate refuses it: the keys of that backend are its own.

import (
	"fmt"
	"io"
	"strings"

	"devtools/internal/client/keys"

	"github.com/spf13/cobra"
)

// hubKeys are the fixed keys of the hub (5a.11.3), the same on every
// backend and not in the binding table.
var hubKeys = [][3]string{
	{"up", "hub", "previous row"},
	{"ctrl+k", "hub", "previous row"},
	{"down", "hub", "next row"},
	{"ctrl+j", "hub", "next row"},
	{"pgup", "hub", "page up"},
	{"pgdown", "hub", "page down"},
	{"tab", "hub", "next scope"},
	{"shift+tab", "hub", "previous scope"},
	{"enter", "hub", "switch"},
	{"ctrl+n", "hub", "new feature"},
	{"ctrl+g", "hub", "merge into main"},
	{"esc", "hub", "cancel, clear or close"},
	{"ctrl+c", "hub", "close"},
}

// copyKeys are the keys of copy mode (5a.11.4).
var copyKeys = [][3]string{
	{"h j k l", "copy-mode", "move"},
	{"w b e", "copy-mode", "word"},
	{"0 $", "copy-mode", "start, end of line"},
	{"g G", "copy-mode", "oldest line, live bottom"},
	{"ctrl+u ctrl+d", "copy-mode", "half page"},
	{"ctrl+b ctrl+f", "copy-mode", "full page"},
	{"v", "copy-mode", "select characters"},
	{"V", "copy-mode", "select lines"},
	{"ctrl+v", "copy-mode", "select a block"},
	{"y", "copy-mode", "copy and leave"},
	{"enter", "copy-mode", "copy and leave"},
	{"q", "copy-mode", "leave"},
	{"esc", "copy-mode", "clear the selection, or leave"},
}

func keysCommand(a *app, stdout io.Writer) *cobra.Command {
	return feature(featKeys, &cobra.Command{
		Use:   "keys",
		Short: "Print every key binding of the devtools window",
		Args:  usageNoArgs("usage: devtools keys"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			table, err := keys.New(cfg.Binds, cfg.Unbinds)
			if err != nil {
				return err
			}
			rows := table.Bindings()
			rows = append(rows, hubKeys...)
			rows = append(rows, copyKeys...)
			printBindings(stdout, rows)
			return nil
		},
	})
}

// printBindings prints rows as three columns, the first two padded.
func printBindings(w io.Writer, rows [][3]string) {
	var width [2]int
	for _, r := range rows {
		width[0] = max(width[0], len(r[0]))
		width[1] = max(width[1], len(r[1]))
	}
	for _, r := range rows {
		line := fmt.Sprintf("%-*s  %-*s  %s", width[0], r[0], width[1], r[1], r[2])
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}
