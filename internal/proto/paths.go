package proto

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Variables of the pane environment (4.7) and of the command line (4.5).
const (
	// EnvPane holds the pane's id on its server, for example "p52".
	EnvPane = "DEVTOOLS_PANE"
	// EnvSocket holds the absolute path of the server's socket.
	EnvSocket = "DEVTOOLS_SOCKET"
	// EnvTTY names the pane variable that holds the path of the pane's
	// terminal device (3.2.1, 4.7). pane.Start sets it; mux.Detect reads it
	// (5.3.10).
	EnvTTY = "DEVTOOLS_TTY"
)

// SocketName is the file name of the server's socket inside StateDir.
const SocketName = "server.sock"

// StateDir returns ${XDG_STATE_HOME:-$HOME/.local/state}/devtools. A relative
// XDG_STATE_HOME is ignored, as the XDG base directory specification asks.
func StateDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "devtools"), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", errors.New("devtools: neither XDG_STATE_HOME nor HOME is set")
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("devtools: HOME %q is not an absolute path", home)
	}
	return filepath.Join(home, ".local", "state", "devtools"), nil
}

// SocketPath returns $DEVTOOLS_SOCKET when it is set, else
// StateDir()/server.sock. A relative DEVTOOLS_SOCKET is an error: the socket
// a pane names must not depend on the working directory of the caller.
func SocketPath() (string, error) {
	if s := os.Getenv(EnvSocket); s != "" {
		if !filepath.IsAbs(s) {
			return "", fmt.Errorf("devtools: %s %q is not an absolute path", EnvSocket, s)
		}
		return filepath.Clean(s), nil
	}
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SocketName), nil
}
