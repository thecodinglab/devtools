package proto

// The messages that reach past one window and one server: the requests a
// command in a pane routes to the window that displays it (4.2.10), the
// catalog stream of what a machine offers to open (4.3), and event.agent
// (4.2.8); and the wire capabilities of generation 1 (4.1).

// Wire capabilities of generation 1 (4.1). They travel in hello.caps and
// welcome.caps; a side uses a family only when it is in both lists. They are
// not the backend capabilities of internal/mux, and no name is shared.
const (
	CapCatalog  = "catalog"  // the catalog stream (4.3)
	CapAgents   = "agents"   // agent records and event.agent
	CapRoute    = "route"    // the ui.* requests (4.2.10)
	CapStateGet = "stateget" // state.get
)

// Message types of cross.go.
const (
	TypeUIHub    = "ui.hub"
	TypeUISwitch = "ui.switch"

	TypeCatalogWatch = "catalog.watch"
	TypeCatalogItem  = "catalog.item"
	TypeCatalogGit   = "catalog.git"
	TypeCatalogError = "catalog.error"
	TypeCatalogDone  = "catalog.done"
	TypeCatalogStop  = "catalog.stop"

	TypeEventAgent = "event.agent"
)

// Values of ui.hub mode (4.2.10). The field has no "unknown": any other
// value makes the message invalid.
const (
	HubAll    = "all"    // the scope "all"
	HubOpen   = "open"   // the scope "sessions"
	HubNew    = "new"    // the scope "all" with the create row selected
	HubAgents = "agents" // the scope "agents"
)

// Values of catalog.item kind (4.3). The field has no "unknown": any other
// value makes the message invalid.
const (
	CatalogWorktree = "worktree"
	CatalogRepo     = "repo"
	CatalogBookmark = "bookmark"
)

func init() {
	for _, f := range []func() Typed{
		func() Typed { return &UIHub{} },
		func() Typed { return &UISwitch{} },
		func() Typed { return &CatalogWatch{} },
		func() Typed { return &CatalogItem{} },
		func() Typed { return &CatalogGit{} },
		func() Typed { return &CatalogError{} },
		func() Typed { return &CatalogDone{} },
		func() Typed { return &CatalogStop{} },
		func() Typed { return &AgentEvent{} },
	} {
		register(f)
	}
}

// --- 4.2.10 Requests routed to the window (capability route) ---

// UIHub is ui.hub. From a one-shot it is a request (with an id) answered ok
// or err (no-client, no-pane); the routed copy the server sends to the
// chosen window has no id, carries Pane and gets no reply. It never carries
// a machine: the receiving fleet takes the link it arrived on (SEC11).
type UIHub struct {
	Mode  string `json:"mode"`           // HubAll, HubOpen, HubNew, HubAgents
	Query string `json:"query"`          // the hub's filter text
	Here  string `json:"here"`           // the caller's working directory, absolute
	Pane  string `json:"pane,omitempty"` // routed copy only: the calling pane
}

// UISwitch is ui.switch: exactly one of Session and Dir; Name goes with
// Dir. As a request it has an id and is answered ok or err (no-client,
// no-pane, no-session, no-dir); the routed copy has no id and no reply.
type UISwitch struct {
	Session string `json:"session,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Name    string `json:"name,omitempty"` // the label of a session opened by Dir
	Tab     string `json:"tab,omitempty"`
	Pane    string `json:"pane,omitempty"`
}

func (UIHub) Type() string    { return TypeUIHub }
func (UISwitch) Type() string { return TypeUISwitch }

// --- 4.3 Catalog stream (capability catalog) ---

// CatalogWatch is catalog.watch: interactive connections only. It carries
// no path and no root; the server scans its own configured roots and
// bookmarks. The replies of the stream carry its id. A second watch on a
// connection replaces the first.
type CatalogWatch struct{}

// CatalogItem is catalog.item: the rows of one root as its scan finished,
// or of the bookmarks.
type CatalogItem struct {
	Items []CatalogEntry `json:"items"`
}

// CatalogEntry is one row of catalog.item.
type CatalogEntry struct {
	Kind     string `json:"kind"` // CatalogWorktree, CatalogRepo, CatalogBookmark
	Project  string `json:"project"`
	Worktree string `json:"worktree"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	Root     string `json:"root"`
	// Session is session.Name(project, worktree), a label only: sessions are
	// joined to rows by directory, never by this name.
	Session string `json:"session"`
}

// CatalogGit is catalog.git: the git state of one worktree or repository,
// always after the catalog.item that named its path.
type CatalogGit struct {
	Path string `json:"path"`
	Git  Git    `json:"git"`
}

// Git is the git state of a path; the fields of ui.Git
// (internal/ui/ui.go:46).
type Git struct {
	Changes     int    `json:"changes"`
	Ahead       int    `json:"ahead"`
	Behind      int    `json:"behind"`
	HasUpstream bool   `json:"has_upstream"`
	Detached    bool   `json:"detached"`
	Branch      string `json:"branch"`
}

// CatalogError is catalog.error: a root or a repository could not be read.
// It does not end the stream.
type CatalogError struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// CatalogDone is catalog.done, the terminal message of the stream: every
// root and status finished, and rows the stream did not report are gone.
type CatalogDone struct{}

// CatalogStop is catalog.stop, a notification with the id of the watch:
// the client lost interest and the server cancels the scan. No reply.
type CatalogStop struct{}

func (CatalogWatch) Type() string { return TypeCatalogWatch }
func (CatalogItem) Type() string  { return TypeCatalogItem }
func (CatalogGit) Type() string   { return TypeCatalogGit }
func (CatalogError) Type() string { return TypeCatalogError }
func (CatalogDone) Type() string  { return TypeCatalogDone }
func (CatalogStop) Type() string  { return TypeCatalogStop }

// --- 4.2.8 event.agent (capability agents) ---

// AgentEvent is event.agent: sent on every interactive connection once per
// transition of an agent into blocked or done. Shown is true when a focused
// interactive connection of this server displays the pane's tab at that
// moment.
type AgentEvent struct {
	Session string `json:"session"`
	Tab     string `json:"tab"`
	Pane    string `json:"pane"`
	Kind    string `json:"kind"`   // AgentClaude, AgentCodex, else Unknown
	Status  string `json:"status"` // StatusIdle, StatusWorking, StatusBlocked, StatusDone, else Unknown
	Title   string `json:"title"`
	Shown   bool   `json:"shown"`
}

func (AgentEvent) Type() string { return TypeEventAgent }

// normalize reads an unknown kind or status as Unknown (4.1).
func (e *AgentEvent) normalize() {
	switch e.Kind {
	case AgentClaude, AgentCodex:
	default:
		e.Kind = Unknown
	}
	switch e.Status {
	case StatusIdle, StatusWorking, StatusBlocked, StatusDone:
	default:
		e.Status = Unknown
	}
}
