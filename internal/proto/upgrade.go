package proto

// The live upgrade of the server (capability upgrade): devtools server
// restart asks the server to replace its image with the build installed
// behind its program path, in the same process, keeping every pane's
// program (internal/handover). Only a local one-shot connection may ask.

// CapUpgrade is the capability of a server that can upgrade itself live.
const CapUpgrade = "upgrade"

// Message types of the live upgrade.
const (
	TypeServerUpgrade    = "server.upgrade"
	TypeServerUpgradeAck = "server.upgrade.ack"
)

// Error codes of server.upgrade.
const (
	CodeCurrent      = "current"      // the server runs the installed build already
	CodeIncompatible = "incompatible" // the installed build cannot take over; nothing was changed
	CodeBusy         = "busy"         // an upgrade runs, the server stops, or it cannot be quiet now; try again
)

// ServerUpgrade is server.upgrade, a request. Force upgrades to the
// installed build even when it is the running one (tests).
type ServerUpgrade struct {
	Force bool `json:"force,omitempty"`
}

// ServerUpgradeResult is the ok reply of server.upgrade: the attempt id,
// which the welcome's upgraded carries once the attempt ended, the version
// of the running build and of the installed one, and how many programs
// run.
type ServerUpgradeResult struct {
	ID    string `json:"attempt"`
	From  string `json:"from"`
	To    string `json:"to"`
	Panes int    `json:"panes"`
}

// ServerUpgradeAck is server.upgrade.ack, a notification: the requester
// got the ok reply. The server waits for it, at most a moment, before it
// replaces its image, which closes every connection.
type ServerUpgradeAck struct {
	ID string `json:"attempt"`
}

// Upgraded is welcome.upgraded: the last live upgrade of this server's
// process, the one that made the running image or one that failed.
type Upgraded struct {
	ID    string `json:"attempt"`
	From  string `json:"from"`
	To    string `json:"to"`
	At    int64  `json:"at"`              // unix ms
	Kept  int    `json:"kept"`            // the programs that kept running
	Error string `json:"error,omitempty"` // set when the attempt failed; the server runs the old build
	Cold  bool   `json:"cold,omitempty"`  // the programs were restarted
	Back  bool   `json:"back,omitempty"`  // the new build could not adopt; the old one took the programs back
}

func (ServerUpgrade) Type() string    { return TypeServerUpgrade }
func (ServerUpgradeAck) Type() string { return TypeServerUpgradeAck }

func init() {
	register(func() Typed { return &ServerUpgrade{} })
	register(func() Typed { return &ServerUpgradeAck{} })
}
