package mux

// The feature table (5.3.10 of the plan). The types are a contract (6.2,
// P2-12); the 28 rows of Features are written by P2-13.

type Behaviour string

const (
	NotGated Behaviour = ""        // the feature is the same on every backend
	Refused  Behaviour = "error"   // the unsupported words, exit status 1, nothing done
	Absent   Behaviour = "absent"  // not drawn, not in a cycle, not in help
	Ignored  Behaviour = "ignored" // read, kept, unused, nothing printed
)

type Feature struct {
	ID      string     // "cmd.agents"
	Kind    string     // command, hub scope, hub row, hub key, host, key binding, status, config
	Needs   Capability // "" when the feature needs none
	Without Behaviour  // what the feature is on a backend without Needs
	Late    bool       // the need arises only inside Show, after the command's own work
}

// Features is the table below, in its order. It is not changed at run time.
//
// The rows are the feature table of 5.3.10, in its order: 13 commands,
// three hub scopes, four hub rows, one hub key, the host of the hub, the
// key table, the status row and four config directives. cmd.work is the
// one Late row: its need arises only inside Show, after the git part of
// the command is done. cmd.server needs nothing and is not gated: it
// addresses the devtools server whatever multiplexer the caller sits in.
var Features = []Feature{
	{ID: "cmd.attach", Kind: "command"},
	{ID: "cmd.pick", Kind: "command"},
	{ID: "cmd.sessions", Kind: "command"},
	{ID: "cmd.agents", Kind: "command", Needs: CapAgents, Without: Refused},
	{ID: "cmd.agents.json", Kind: "command", Needs: CapAgents, Without: Refused},
	{ID: "cmd.work", Kind: "command", Needs: CapDetached, Without: Refused, Late: true},
	{ID: "cmd.merge", Kind: "command"},
	{ID: "cmd.bookmark", Kind: "command"},
	{ID: "cmd.session.list", Kind: "command", Needs: CapLayout, Without: Refused},
	{ID: "cmd.pane.list", Kind: "command", Needs: CapLayout, Without: Refused},
	{ID: "cmd.pane.focus", Kind: "command", Needs: CapPaneFocus, Without: Refused},
	{ID: "cmd.keys", Kind: "command", Needs: CapWindow, Without: Refused},
	{ID: "cmd.server", Kind: "command"},
	{ID: "hub.scope.all", Kind: "hub scope"},
	{ID: "hub.scope.sessions", Kind: "hub scope"},
	{ID: "hub.scope.agents", Kind: "hub scope", Needs: CapAgents, Without: Absent},
	{ID: "hub.row.local", Kind: "hub row"},
	{ID: "hub.row.agent", Kind: "hub row", Needs: CapAgents, Without: Absent},
	{ID: "hub.row.remote", Kind: "hub row", Needs: CapMachines, Without: Absent},
	{ID: "hub.heading", Kind: "hub row", Needs: CapMachines, Without: Absent},
	{ID: "hub.create", Kind: "hub key"},
	{ID: "hub.overlay", Kind: "host", Needs: CapWindow, Without: Absent},
	{ID: "key.table", Kind: "key binding", Needs: CapWindow, Without: Absent},
	{ID: "status.row", Kind: "status", Needs: CapWindow, Without: Absent},
	{ID: "cfg.multiplexer", Kind: "config"},
	{ID: "cfg.root", Kind: "config"},
	{ID: "cfg.machine", Kind: "config", Needs: CapMachines, Without: Ignored},
	{ID: "cfg.bind", Kind: "config", Needs: CapWindow, Without: Ignored},
}
