package pane

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// envMap turns an environment list into a map and fails on a name given
// twice.
func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range env {
		name, v, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("entry %q has no =", kv)
		}
		if _, dup := m[name]; dup {
			t.Fatalf("%s is set twice in %q", name, env)
		}
		m[name] = v
	}
	return m
}

// serverEnv is the environment of a server started from inside every
// multiplexer and terminal at once.
var serverEnv = []string{
	"HOME=/home/u", "USER=u", "LOGNAME=u", "SHELL=/bin/zsh", "LANG=en_US.UTF-8",
	"LC_ALL=C.UTF-8", "LC_TIME=de_CH.UTF-8", "PATH=/usr/bin:/bin", "XDG_RUNTIME_DIR=/run/user/1000",
	"TMPDIR=/tmp/u",
	"TMUX=/tmp/tmux-1000/default,1,0", "TMUX_PANE=%3", "STY=1.pts-0.host",
	"ZELLIJ=0", "ZELLIJ_SESSION_NAME=z", "HERDR_ENV=1", "HERDR_SOCKET_PATH=/run/herdr.sock",
	"HERDR_PANE_ID=4", "CLAUDECODE=1", "KITTY_WINDOW_ID=2", "GHOSTTY_RESOURCES_DIR=/g",
	"TERM=screen-256color", "COLORTERM=no", "TERM_PROGRAM=tmux",
	"DEVTOOLS_PANE=p9", "DEVTOOLS_SOCKET=/elsewhere.sock", "DEVTOOLS_TTY=/dev/pts/99",
	"SSH_AUTH_SOCK=/run/agent", "EDITOR=nvim", "LD_PRELOAD=/evil.so",
	"DISPLAY=:9", "XDG_STATE_HOME=/home/u/.local/state",
	"DEVTOOLS_PASS_ENV=SSH_AUTH_SOCK,TMUX,HERDR_ENV,ZELLIJ,CLAUDECODE,TERM,DEVTOOLS_PANE",
}

func TestPaneEnvironmentIsBuiltFromTheAllowList(t *testing.T) {
	got := Env(serverEnv, nil, "p52", "/run/user/1000/devtools/server.sock")
	if !slices.IsSorted(got) {
		t.Errorf("Env is not sorted: %q", got)
	}
	m := envMap(t, got)
	want := map[string]string{
		"HOME": "/home/u", "USER": "u", "LOGNAME": "u", "SHELL": "/bin/zsh", "LANG": "en_US.UTF-8",
		"LC_ALL": "C.UTF-8", "LC_TIME": "de_CH.UTF-8", "PATH": "/usr/bin:/bin",
		"XDG_RUNTIME_DIR": "/run/user/1000", "TMPDIR": "/tmp/u",
		"SSH_AUTH_SOCK": "/run/agent", // named by DEVTOOLS_PASS_ENV
		"TERM":          "xterm-256color", "COLORTERM": "truecolor",
		"DEVTOOLS_PANE": "p52", "DEVTOOLS_SOCKET": "/run/user/1000/devtools/server.sock",
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("Env =\n%q\nwant\n%v", got, want)
	}
	// The marks of other multiplexers and terminals never reach a pane, not
	// even when DEVTOOLS_PASS_ENV names them; DEVTOOLS_TTY is Start's.
	for name := range m {
		if denied(name) || name == "DEVTOOLS_TTY" || name == "DEVTOOLS_PASS_ENV" || name == "DISPLAY" {
			t.Errorf("%s reached the pane", name)
		}
	}
	// A name given twice: the first entry counts, as for getenv.
	m = envMap(t, Env([]string{"PATH=/first", "PATH=/second", "broken", "=x"}, nil, "p1", "/s"))
	if m["PATH"] != "/first" {
		t.Errorf("PATH = %q, want the first entry", m["PATH"])
	}
	// A value with a NUL cannot be passed to a program and is left out.
	if m := envMap(t, Env([]string{"HOME=/a\x00b"}, nil, "p1", "/s")); m["HOME"] != "" {
		t.Errorf("a HOME with a NUL reached the pane: %q", m["HOME"])
	}
}

func TestHelloVariablesAreTakenOnlyFromTheWhitelist(t *testing.T) {
	hello := map[string]string{
		"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-1", "XAUTHORITY": "/home/u/.Xauthority",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus",
		"PATH":                     "/evil", "HOME": "/evil", "LD_PRELOAD": "/evil.so", "TMUX": "x", "HERDR_ENV": "1",
		"TERM": "dumb", "DEVTOOLS_PANE": "p0", "SSH_AUTH_SOCK": "/evil",
	}
	m := envMap(t, Env([]string{"HOME=/home/u", "PATH=/bin"}, hello, "p1", "/s"))
	want := map[string]string{
		"HOME": "/home/u", "PATH": "/bin",
		"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-1", "XAUTHORITY": "/home/u/.Xauthority",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus",
		"TERM":                     "xterm-256color", "COLORTERM": "truecolor",
		"DEVTOOLS_PANE": "p1", "DEVTOOLS_SOCKET": "/s",
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("Env with a hello = %v\nwant %v", m, want)
	}
	// An empty display variable of a hello is no value.
	if m := envMap(t, Env(nil, map[string]string{"DISPLAY": ""}, "p1", "/s")); m["DISPLAY"] != "" {
		t.Errorf("an empty DISPLAY reached the pane")
	}
	if _, ok := envMap(t, Env(nil, map[string]string{"DISPLAY": ""}, "p1", "/s"))["DISPLAY"]; ok {
		t.Errorf("an empty DISPLAY is set")
	}
}

func TestAHelloWithoutDisplayVariablesKeepsTheOnesHeld(t *testing.T) {
	held := map[string]string{}
	Hold(held, map[string]string{"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-1", "PATH": "/evil"})
	// A client from an ssh login or a console: no display variables, or
	// empty ones.
	Hold(held, map[string]string{"TERM": "xterm"})
	Hold(held, map[string]string{"DISPLAY": "", "WAYLAND_DISPLAY": ""})
	Hold(held, nil)
	want := map[string]string{"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-1"}
	if !reflect.DeepEqual(held, want) {
		t.Fatalf("held = %v, want %v", held, want)
	}
	// The newest hello that carries a variable sets it; the others stay.
	Hold(held, map[string]string{"DISPLAY": ":1"})
	want["DISPLAY"] = ":1"
	if !reflect.DeepEqual(held, want) {
		t.Fatalf("held = %v, want %v", held, want)
	}
	m := envMap(t, Env(nil, held, "p1", "/s"))
	if m["DISPLAY"] != ":1" || m["WAYLAND_DISPLAY"] != "wayland-1" {
		t.Errorf("a pane spawned later got DISPLAY=%q WAYLAND_DISPLAY=%q", m["DISPLAY"], m["WAYLAND_DISPLAY"])
	}
}

func TestPassEnvIsSplitOnCommas(t *testing.T) {
	if got, want := PassEnv(" SSH_AUTH_SOCK, DEVTOOLS_MULTIPLEXER,,EDITOR ,"), []string{"SSH_AUTH_SOCK", "DEVTOOLS_MULTIPLEXER", "EDITOR"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PassEnv = %q, want %q", got, want)
	}
	if got := PassEnv(""); len(got) != 0 {
		t.Errorf("PassEnv of nothing = %q", got)
	}
	server := []string{
		"DEVTOOLS_PASS_ENV=SSH_AUTH_SOCK,DEVTOOLS_MULTIPLEXER, DEVTOOLS_ROOT",
		"SSH_AUTH_SOCK=/run/agent", "DEVTOOLS_MULTIPLEXER=devtools", "DEVTOOLS_ROOT=/srv/dev",
		"EDITOR=nvim",
	}
	m := envMap(t, Env(server, nil, "p1", "/s"))
	for name, want := range map[string]string{"SSH_AUTH_SOCK": "/run/agent", "DEVTOOLS_MULTIPLEXER": "devtools", "DEVTOOLS_ROOT": "/srv/dev"} {
		if m[name] != want {
			t.Errorf("%s = %q, want %q", name, m[name], want)
		}
	}
	if _, ok := m["EDITOR"]; ok {
		t.Errorf("EDITOR is not listed and reached the pane")
	}
	// One whole value with commas is not one name.
	m = envMap(t, Env([]string{"DEVTOOLS_PASS_ENV=A,B", "A,B=x", "A=1", "B=2"}, nil, "p1", "/s"))
	if m["A"] != "1" || m["B"] != "2" || m["A,B"] != "" {
		t.Errorf("DEVTOOLS_PASS_ENV=A,B gave %v", m)
	}
}

func TestStartAppendsTheTerminalDeviceOnce(t *testing.T) {
	got := withTTY([]string{"HOME=/h", "DEVTOOLS_TTY=/dev/pts/1", "PATH=/bin"}, "/dev/pts/7")
	want := []string{"HOME=/h", "PATH=/bin", "DEVTOOLS_TTY=/dev/pts/7"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("withTTY = %q, want %q", got, want)
	}
}
