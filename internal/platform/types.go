// Package platform holds every Linux and macOS difference: the process
// table, the peer uid, the lock, the terminal calls, the terminal device
// comparison SameTTY, and the service names (5.3.4). The package uses no cgo.
//
// This file is the contract (P1-00a). The functions are written by P1-04,
// one file per function group with _linux.go and _darwin.go variants.
package platform

import "errors"

// Proc is one process of the process table.
type Proc struct {
	Pid, PPid, Pgid int
	Name            string
	Args            []string
}

// ErrLocked is returned by Lock when another process holds the lock.
var ErrLocked = errors.New("platform: locked by another process")

// ErrUnsupported is returned by a call this system cannot answer (Cwd on
// darwin).
var ErrUnsupported = errors.ErrUnsupported

// The service names (4.5).
const (
	SystemdUnit  = "devtools-server.service"
	LaunchdLabel = "org.nix-community.home.devtools-server"
)
