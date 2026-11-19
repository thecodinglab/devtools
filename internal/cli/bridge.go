package cli

// The body of devtools bridge (4.4 of the plan) is remote.Bridge; this file
// names it for the command of server.go, so a test can see the command run
// without a server.

import "devtools/internal/remote"

// runBridge is remote.Bridge; tests replace it.
var runBridge = remote.Bridge
