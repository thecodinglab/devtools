// Package testkit is the test harness of section 10.1 of the plan: the PTY
// switch, the helper programs that live inside every test binary, short
// temporary directories for unix sockets, a polling wait, and a clean
// environment for every process a test starts.
//
// It is a test-only package (import rule 8): no non-test package imports it.
package testkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MaxSocketPath is the longest path of a unix socket on the platforms the
// plan supports: sun_path holds 104 bytes on macOS and 108 on Linux, and the
// path needs its terminating NUL. ShortTempDir leaves room below it.
const MaxSocketPath = 103

// ShortTempDir returns a new empty directory under /tmp that is removed when
// the test ends. It is short enough that a socket inside it fits a unix
// socket address on macOS (104 bytes): t.TempDir lives under /var/folders on
// a Mac, which is too long once a test name is part of it.
func ShortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dt-")
	if err != nil {
		t.Fatalf("testkit: short temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// CleanEnv returns a complete environment for a process a test starts:
// HOME, XDG_STATE_HOME, PATH, LANG and SHELL, and nothing inherited from the
// environment of the test. HOME is home and XDG_STATE_HOME is
// home/.local/state, so a server or a client started with it finds its state
// directory and its socket inside the test's directory. PATH holds only the
// system's two binary directories: no tool of the developer (tmux, herdr,
// devtools) is found through it unless a test puts a directory in front.
//
// Each of extra is "NAME=value". It is added; an extra that names a variable
// already in the list replaces it, and a later extra wins over an earlier
// one.
func CleanEnv(home string, extra ...string) []string {
	env := []string{
		"HOME=" + home,
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"SHELL=/bin/sh",
	}
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		replaced := false
		for i, have := range env {
			if n, _, _ := strings.Cut(have, "="); n == name {
				env[i] = kv
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	return env
}

// LookupEnv returns the value of name in env, a list in the form of
// os.Environ, and whether it is there.
func LookupEnv(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if n, v, ok := strings.Cut(env[i], "="); ok && n == name {
			return v, true
		}
	}
	return "", false
}
