package srccheck

import (
	"strings"
	"testing"

	"devtools/internal/cli"
)

// removedCommands are the seven commands of the LAN discovery and the fzf
// pickers that the cutover removed (4.5, 7.1.2, AG4). machines is one of
// them: TestMachinesCommandIsGone of revision 1 is this test.
var removedCommands = []string{"machines", "hosts", "serve", "session-open", "session-preview", "agent-preview", "machine-preview"}

// registered reports whether the root command of the command line has a
// subcommand of this name, hidden or not: asked for its help, a command the
// root has prints it and succeeds, a name it lacks is an unknown command.
// Asking for help runs no command: no backend is selected, nothing starts.
func registered(name string) bool {
	var stdout, stderr strings.Builder
	code := cli.Run([]string{name, "--help"}, &stdout, &stderr)
	return code == 0 && !strings.Contains(stderr.String(), "unknown command")
}

// presentAmong returns the names that has reports as registered.
func presentAmong(names []string, has func(string) bool) []string {
	var found []string
	for _, name := range names {
		if has(name) {
			found = append(found, name)
		}
	}
	return found
}

// TestRemovedCommandsAreGone (AG4): the root command has none of the seven
// removed commands. The probe is shown to see commands first: a visible
// one (pick) and a hidden one (bridge, as session-open and the previews
// were) are found by it.
func TestRemovedCommandsAreGone(t *testing.T) {
	for _, name := range []string{"pick", "bridge"} {
		if !registered(name) {
			t.Fatalf("the probe does not see the registered command %s: it would prove nothing", name)
		}
	}
	if found := presentAmong(removedCommands, registered); len(found) != 0 {
		t.Errorf("the root command has the removed commands %q", found)
	}
	// the same check on a root command that kept one of them
	kept := func(name string) bool { return name == "hosts" }
	if found := presentAmong(removedCommands, kept); len(found) != 1 || found[0] != "hosts" {
		t.Errorf("on a root command with hosts the check found %q", found)
	}
}
