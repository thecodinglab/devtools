// Package peer finds devtools on the other machines of the local network
// and reads what they hold. Every machine runs devtools serve, which
// answers a UDP query, broadcast or sent to it directly, with an announce
// saying where to fetch its snapshot over HTTP: the projects, worktrees and
// open sessions on it. It only ever reads, so finding the other machines
// asks for no login; switching to a session on one goes over ssh.
package peer

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"

	"devtools/internal/ui"
)

// DefaultPort is the UDP port queries go to and the TCP port snapshots are
// served on.
const DefaultPort = 47390

// Protocol is the version of the messages; a machine speaking another one
// is reported, not read.
const Protocol = 1

const snapshotPath = "/v1/snapshot"

type query struct {
	Protocol int    `json:"devtools"`
	Type     string `json:"type"`
}

const queryType = "query"

// Announce is a machine's answer to a query.
type Announce struct {
	Protocol int    `json:"devtools"`
	Name     string `json:"name"`     // what the switcher calls the machine
	Hostname string `json:"hostname"` // its os hostname, to recognize itself
	SSH      string `json:"ssh"`      // the ssh destination that reaches it
	Port     int    `json:"port"`     // the TCP port of its snapshot
}

// Snapshot is what a machine holds.
type Snapshot struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	SSH      string `json:"ssh"`
	// Multiplexer is the backend its sessions live in, herdr or tmux, and
	// HerdrRunning whether a herdr server runs there, which a herdr client
	// on another machine needs to reach it.
	Multiplexer  string `json:"multiplexer"`
	HerdrRunning bool   `json:"herdr_running"`
	Items        []Item `json:"items"`
}

// Item is one place on a machine, laid out like the switcher's items.
type Item struct {
	Kind        string     `json:"kind"` // worktree, repo, bookmark or workspace
	Project     string     `json:"project"`
	Worktree    string     `json:"worktree,omitempty"`
	Label       string     `json:"label"`
	Path        string     `json:"path,omitempty"`
	Root        string     `json:"root,omitempty"`
	Session     string     `json:"session"`
	WorkspaceID string     `json:"workspace_id,omitempty"`
	Git         ui.Git     `json:"git"`
	State       ui.Session `json:"state"`
}

func (a Announce) validate() error {
	if a.Protocol != Protocol {
		return fmt.Errorf("speaks devtools protocol %d, this devtools speaks %d", a.Protocol, Protocol)
	}
	if strings.TrimSpace(a.Name) == "" || a.Hostname == "" {
		return fmt.Errorf("announces no name")
	}
	if a.Port <= 0 || a.Port > 65535 {
		return fmt.Errorf("announces port %d", a.Port)
	}
	return ValidSSH(a.SSH)
}

// sshDestination is what an ssh destination may look like: an ssh config
// alias or user@host. A destination comes from whoever answers on the
// network, so anything ssh could read as an option is refused.
var sshDestination = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.%+@:\[\]-]*$`)

// ValidSSH reports whether target can be handed to ssh as a destination.
func ValidSSH(target string) error {
	if !sshDestination.MatchString(target) {
		return fmt.Errorf("announces ssh destination %q, which devtools will not pass to ssh", target)
	}
	return nil
}

func encodeQuery() []byte {
	data, _ := json.Marshal(query{Protocol: Protocol, Type: queryType})
	return data
}

func parseQuery(data []byte) bool {
	var q query
	return json.Unmarshal(data, &q) == nil && q.Type == queryType && q.Protocol > 0
}

// allowed reports whether a request may come from addr: the loopback, a
// private or link-local network, or a CGNAT range like Tailscale's. A
// machine never answers the open internet.
func allowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}
