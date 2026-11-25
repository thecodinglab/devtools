package handover

// State is the model of the server in schema version 1: everything the
// state loop's model holds, live states included (the dormant, running and
// exited panes, their sizes, the agent records). It is a copy of the shapes
// of internal/proto and internal/server/model at the time of version 1;
// the model converts to and from it (model.Handover, model.Adopt).
type State struct {
	Counters Counters  `json:"counters"`
	Sessions []Session `json:"sessions"`
	Tabs     []Tab     `json:"tabs"`
	Panes    []PaneRec `json:"panes"`
	Agents   []Agent   `json:"agents"`
}

// Counters are the numbers of the last ids issued.
type Counters struct {
	Session uint64 `json:"session"`
	Tab     uint64 `json:"tab"`
	Pane    uint64 `json:"pane"`
}

// Session is a session with the size of its pane area.
type Session struct {
	ID        string   `json:"id"`
	Dir       string   `json:"dir"`
	Label     string   `json:"label"`
	Tabs      []string `json:"tabs"`
	Current   string   `json:"current"`
	Shown     int64    `json:"shown"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
	Displayed bool     `json:"displayed"`
}

// Tab is a tab with its layout.
type Tab struct {
	ID      string   `json:"id"`
	Session string   `json:"session"`
	Index   int      `json:"index"`
	Panes   []string `json:"panes"`
	Split   string   `json:"split"`
	Focused string   `json:"focused"`
	Layout  Node     `json:"layout"`
}

// Node is a node of a tab's layout tree.
type Node struct {
	Pane   string `json:"pane,omitempty"`
	Split  string `json:"split,omitempty"`
	Weight int    `json:"weight,omitempty"`
	Nodes  []Node `json:"nodes,omitempty"`
}

// PaneRec is a pane of the model: its record on the wire and what only the
// model holds.
type PaneRec struct {
	ID      string    `json:"id"`
	Tab     string    `json:"tab"`
	Session string    `json:"session"`
	State   string    `json:"state"`
	Cwd     string    `json:"cwd"`
	Program string    `json:"program"`
	Editor  bool      `json:"editor"`
	Busy    bool      `json:"busy"`
	Title   string    `json:"title"`
	Exit    *PaneExit `json:"exit,omitempty"`
	Cols    int       `json:"cols"`
	Rows    int       `json:"rows"`
	Resume  *Resume   `json:"resume,omitempty"`
	Handed  bool      `json:"handed"`
}

// PaneExit is how the program of an exited pane ended.
type PaneExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal"`
	Text   string `json:"text"`
}

// Resume is the agent conversation a pane's next start resumes.
type Resume struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Agent is the agent record of a pane.
type Agent struct {
	Pane    string `json:"pane"`
	Tab     string `json:"tab"`
	Session string `json:"session"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Cwd     string `json:"cwd"`
	Since   int64  `json:"since"`
}
