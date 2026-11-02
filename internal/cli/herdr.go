package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"devtools/internal/herdr"
	"devtools/internal/picker"

	"github.com/spf13/cobra"
)

func agentsCommand(stdout io.Writer) *cobra.Command {
	var machine string
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Pick a herdr agent, the ones needing attention first",
		Args:  usageNoArgs("usage: devtools agents [--machine <label-or-id>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := findMachine(machine)
			if err != nil {
				return err
			}
			machine = profile.ID
			if err := ensureLocalServer(machine); err != nil {
				return err
			}
			client := herdr.Client{Machine: machine}
			agents, err := client.ListAgents()
			if err != nil {
				return err
			}
			workspaces, err := client.ListWorkspaces()
			if err != nil {
				return err
			}
			preview, err := selfCommand(machineArgs(machine, "agent-preview", "{2}")...)
			if err != nil {
				return err
			}
			option, err := picker.SelectOption(agentOptions(agents, workspaces), "no herdr agents found", preview)
			if err != nil {
				return err
			}
			return visitHerdr(stdout, profile, func(client herdr.Client) error {
				return client.FocusAgent(option.Value)
			})
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "pick an agent on a saved herdr machine")
	return cmd
}

func agentOptions(agents []herdr.Agent, workspaces []herdr.Workspace) []picker.Option {
	labels := make(map[string]string, len(workspaces))
	for _, workspace := range workspaces {
		labels[workspace.ID] = workspace.Label
	}
	rows := make([][]string, 0, len(agents))
	for _, agent := range agents {
		workspace := labels[agent.Workspace]
		if workspace == "" {
			workspace = agent.Workspace
		}
		kind := agent.Kind
		if agent.Name != "" {
			kind += " " + agent.Name
		}
		rows = append(rows, []string{workspace, kind, agent.Status, agent.Title})
	}
	options := make([]picker.Option, 0, len(agents))
	// fzf shows the aligned first field only, the pane id keeps lines distinct
	for i, line := range alignColumns(rows) {
		options = append(options, picker.Option{
			Label: line + "\t" + agents[i].Pane,
			Value: agents[i].Pane,
		})
	}
	return options
}

func agentPreviewCommand(stdout io.Writer) *cobra.Command {
	var machine string
	cmd := &cobra.Command{
		Use:    "agent-preview <pane>",
		Short:  "Render the fzf preview for a herdr agent",
		Hidden: true,
		Args:   usageExactArgs(1, "usage: devtools agent-preview [--machine <label-or-id>] <pane>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := herdr.Client{Machine: machine}.ReadAgent(args[0])
			if err != nil {
				return err
			}
			for _, line := range tailLines(content, previewHeight()) {
				fmt.Fprintln(stdout, line)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&machine, "machine", "", "saved herdr machine")
	return cmd
}

func machinesCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "machines",
		Short: "Pick a saved herdr machine, then one of its workspaces",
		Args:  usageNoArgs("usage: devtools machines"),
		RunE: func(cmd *cobra.Command, args []string) error {
			machines, err := herdr.ListMachines()
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(machines))
			for _, machine := range machines {
				state := "enabled"
				if !machine.Enabled {
					state = "disabled"
				}
				if machine.Selected {
					state += ", selected"
				}
				rows = append(rows, []string{machine.Label, machine.Target, state})
			}
			options := make([]picker.Option, 0, len(machines))
			for i, line := range alignColumns(rows) {
				options = append(options, picker.Option{
					Label: line + "\t" + machines[i].ID,
					Value: machines[i].ID,
				})
			}
			preview, err := selfCommand("machine-preview", "{2}")
			if err != nil {
				return err
			}
			option, err := picker.SelectOption(options, "no saved herdr machines, add one with herdr machine add", preview)
			if err != nil {
				return err
			}
			return pickWorkspace(stdout, option.Value)
		},
	}
}

func machinePreviewCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "machine-preview <machine>",
		Short:  "Render the fzf preview for a saved herdr machine",
		Hidden: true,
		Args:   usageExactArgs(1, "usage: devtools machine-preview <label-or-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaces, err := herdr.Client{Machine: args[0]}.ListWorkspaces()
			if err != nil {
				return err
			}
			rows := make([][]string, 0, len(workspaces))
			for _, workspace := range workspaces {
				rows = append(rows, []string{workspace.Label, fmt.Sprintf("%d tabs", workspace.Tabs), workspace.AgentStatus})
			}
			for _, line := range alignColumns(rows) {
				fmt.Fprintln(stdout, line)
			}
			return nil
		},
	}
}

// pickWorkspace picks a workspace on the local server, or on a saved machine
// when one is given, and lands there.
func pickWorkspace(stdout io.Writer, machine string) error {
	profile, err := findMachine(machine)
	if err != nil {
		return err
	}
	machine = profile.ID
	if err := ensureLocalServer(machine); err != nil {
		return err
	}
	client := herdr.Client{Machine: machine}
	workspaces, err := client.ListWorkspaces()
	if err != nil {
		return err
	}
	options := make([]picker.Option, 0, len(workspaces))
	for _, workspace := range workspaces {
		// labels need not be unique, the id makes every line distinct
		label := fmt.Sprintf("%s\t%d tabs\t%s", workspace.Label, workspace.Tabs, workspace.ID)
		if workspace.Focused {
			label += "\tfocused"
		}
		options = append(options, picker.Option{
			Label: label,
			Value: workspace.ID,
		})
	}
	preview, err := selfCommand(machineArgs(machine, "session-preview", "{3}")...)
	if err != nil {
		return err
	}
	option, err := picker.SelectOption(options, "no herdr workspaces found", preview)
	if err != nil {
		return err
	}
	return visitHerdr(stdout, profile, func(client herdr.Client) error {
		return client.FocusWorkspace(option.Value)
	})
}

// findMachine resolves a machine label or id, none standing for the local
// server.
func findMachine(selector string) (herdr.Machine, error) {
	if selector == "" {
		return herdr.Machine{}, nil
	}
	return herdr.FindMachine(selector)
}

// ensureLocalServer starts herdr before a picker lists its workspaces or
// agents from a plain terminal, so a cold start shows what herdr restores.
func ensureLocalServer(machine string) error {
	if machine != "" || herdr.Inside() {
		return nil
	}
	return herdr.EnsureServer()
}

// visitHerdr focuses something on the local server, or on a saved machine
// when the profile is set, and lands the user there. Outside herdr that
// attaches the terminal, to the machine's server over ssh if need be. Inside
// herdr nothing can switch the machine the herdr window shows, so only tell
// the user to.
func visitHerdr(stdout io.Writer, profile herdr.Machine, focus func(herdr.Client) error) error {
	if profile.ID == "" {
		return herdr.Visit(func() error { return focus(herdr.Client{}) })
	}
	if err := focus(herdr.Client{Machine: profile.ID}); err != nil {
		return err
	}
	if herdr.Inside() {
		if !profile.Selected {
			fmt.Fprintf(stdout, "select machine %s in the herdr sidebar to see it\n", profile.Label)
		}
		return nil
	}
	if profile.Target == "" {
		return herdr.Attach()
	}
	return herdr.Attach(profile.RemoteArgs()...)
}

func machineArgs(machine string, args ...string) []string {
	if machine == "" {
		return args
	}
	return append([]string{args[0], "--machine", machine}, args[1:]...)
}

// selfCommand is a shell command running devtools with args, for fzf to run
// as a preview; fzf placeholders like {1} are passed through unquoted.
func selfCommand(args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	parts := []string{fmt.Sprintf("%q", exe)}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " "), nil
}

var (
	plainWord   = regexp.MustCompile(`^[A-Za-z0-9_./:@=+-]+$`)
	placeholder = regexp.MustCompile(`^\{\d+\}$`)
)

func shellQuote(arg string) string {
	if plainWord.MatchString(arg) || placeholder.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// alignColumns pads all but the last column of the rows to a common width.
func alignColumns(rows [][]string) []string {
	var widths []int
	for _, row := range rows {
		for i, cell := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len([]rune(clean(cell))))
		}
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			cell = clean(cell)
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-len([]rune(cell))))
			}
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	return lines
}

// clean keeps tabs and newlines, which fzf splits on, out of a cell
func clean(cell string) string {
	return strings.Join(strings.Fields(cell), " ")
}
