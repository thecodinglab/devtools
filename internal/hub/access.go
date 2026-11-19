package hub

// What the look (view.go, P4-04) reads of the model (H5, H12). Nothing a
// missing feature names is in here: no agent item without Features.Agents,
// no machine state without Features.Machines.

// machineView is one machine as its heading shows it.
type machineView struct {
	Name    string // config alias; "" is this machine
	State   string // "online", "connecting", "off", "retrying", "attention"
	Problem Problem
	Listing bool // online, and its catalog scan has not ended yet
	// Notes are the secondary lines under the heading (Note, 3.5.3): a
	// path the catalog could not read, a cut list, an unfinished scan
	Notes []string
}

// footerState is what the footer says besides the hints.
type footerState struct {
	Confirm *Item  // a merge waiting for enter
	Busy    string // what is running
	Message string
	Failed  bool // Message is a failure
}

// hasAgents reports whether the agents scope and agent rows exist.
func (m *Model) hasAgents() bool { return m.features.Agents }

// hasMachines reports whether machine headings and other machines' rows
// exist.
func (m *Model) hasMachines() bool { return m.features.Machines }

// currentScope is the name of the scope shown: "all", "sessions", "agents".
func (m *Model) currentScope() string { return m.scope.name() }

// scopeNames is the scopes of the scope bar, in order.
func (m *Model) scopeNames() []string {
	var names []string
	for _, s := range m.scopes() {
		names = append(names, s.name())
	}
	return names
}

// visible is the items of the list in the order of its rows.
func (m *Model) visible() []Item {
	var items []Item
	for _, r := range m.rows {
		if r.kind == rowItem {
			items = append(items, m.items[r.item])
		}
	}
	return items
}

// selected is the selected item; false on the create row or with nothing
// selectable.
func (m *Model) selected() (Item, bool) { return m.selectedItem() }

// machineViews is every machine in config order, this machine first; nil
// without Features.Machines.
func (m *Model) machineViews() []machineView {
	if !m.features.Machines {
		return nil
	}
	views := make([]machineView, 0, len(m.machines))
	for _, mc := range m.machines {
		views = append(views, machineView{Name: mc.name, State: mc.state, Problem: mc.problem, Listing: mc.online() && !mc.complete, Notes: mc.noteLines()})
	}
	return views
}

// queryText is what is typed.
func (m *Model) queryText() string { return m.input.Value() }

// footerNow is the footer's state.
func (m *Model) footerNow() footerState {
	return footerState{Confirm: m.confirm, Busy: m.busy, Message: m.message, Failed: m.failed}
}
