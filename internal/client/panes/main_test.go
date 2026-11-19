package panes

import (
	"os"
	"testing"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a home, a socket or a multiplexer reaches them. The package
// reads no environment; this is the convention of 10.1.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-panes-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
