package proto

import (
	"path/filepath"
	"testing"
)

func TestSocketPathPrefersDevtoolsSocketThenStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv(EnvSocket, "")
	t.Setenv("XDG_STATE_HOME", "")
	got, err := SocketPath()
	if want := filepath.Join(home, ".local", "state", "devtools", "server.sock"); err != nil || got != want {
		t.Fatalf("without variables: %q, %v; want %q", got, err, want)
	}

	state := filepath.Join(home, "xdg-state")
	t.Setenv("XDG_STATE_HOME", state)
	got, err = SocketPath()
	if want := filepath.Join(state, "devtools", "server.sock"); err != nil || got != want {
		t.Fatalf("with XDG_STATE_HOME: %q, %v; want %q", got, err, want)
	}
	dir, err := StateDir()
	if want := filepath.Join(state, "devtools"); err != nil || dir != want {
		t.Fatalf("StateDir: %q, %v; want %q", dir, err, want)
	}

	t.Setenv("XDG_STATE_HOME", "relative/state")
	dir, err = StateDir()
	if want := filepath.Join(home, ".local", "state", "devtools"); err != nil || dir != want {
		t.Fatalf("a relative XDG_STATE_HOME is not ignored: %q, %v", dir, err)
	}
	t.Setenv("XDG_STATE_HOME", state)

	sock := filepath.Join(home, "other", "s.sock")
	t.Setenv(EnvSocket, sock)
	got, err = SocketPath()
	if err != nil || got != sock {
		t.Fatalf("with DEVTOOLS_SOCKET: %q, %v; want %q", got, err, sock)
	}

	t.Setenv(EnvSocket, "relative.sock")
	if got, err := SocketPath(); err == nil {
		t.Fatalf("a relative DEVTOOLS_SOCKET gave %q", got)
	}

	t.Setenv(EnvSocket, "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if got, err := SocketPath(); err == nil {
		t.Fatalf("without HOME: %q", got)
	}
}

func TestThePaneVariablesHaveTheirNames(t *testing.T) {
	if EnvTTY != "DEVTOOLS_TTY" || EnvPane != "DEVTOOLS_PANE" || EnvSocket != "DEVTOOLS_SOCKET" {
		t.Fatal("pane variable names changed")
	}
}
