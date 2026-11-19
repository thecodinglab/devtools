package backend

// The facts of mux.Detect for the real process (5.3.10 and 5.3.11 of the
// plan, P2-14). Detect itself is pure; everything it learns about the
// environment, the terminal and the multiplexers around it comes from here.

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"devtools/internal/mux"
	"devtools/internal/platform"
	"devtools/internal/tmux"
)

// checkLimit bounds each liveness check.
const checkLimit = 500 * time.Millisecond

// Facts fills mux.Facts for the real process: Getenv over env (a list in
// the form of os.Environ, the last entry of a name wins); Terminal when in
// and out are terminals, HasTTY when in, out or standard error is one;
// SameTTY through platform.SameTTY over the same three; Devtools through
// devtoolsKnows; TmuxPane and TmuxAlive through internal/tmux; HerdrAlive
// by connecting to the socket and closing. Each check has a limit of
// 500 ms and answers Dead only when it finished and the answer is no.
func Facts(env []string, in, out *os.File) mux.Facts {
	vars := make(map[string]string, len(env))
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok {
			vars[name] = value
		}
	}
	files := []*os.File{in, out, os.Stderr}
	return mux.Facts{
		Getenv:   func(name string) string { return vars[name] },
		Terminal: isTerminal(in) && isTerminal(out),
		HasTTY:   isTerminal(in) || isTerminal(out) || isTerminal(os.Stderr),
		SameTTY: func(path string) bool {
			for _, f := range files {
				if f != nil && platform.SameTTY(f, path) {
					return true
				}
			}
			return false
		},
		Devtools:   devtoolsKnows,
		TmuxPane:   tmuxPane,
		TmuxAlive:  tmuxAlive,
		HerdrAlive: herdrAlive,
	}
}

// isTerminal reports whether f is a terminal. It asks through platform,
// which goes through SyscallConn and not Fd, so a descriptor the runtime
// polls is not switched to blocking mode (the guard's probe needs its read
// deadline later).
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	_, _, err := platform.Winsize(f)
	return err == nil
}

// tmuxPane is Facts.TmuxPane: Live with the pane's terminal device when the
// server named in tmuxVar knows pane, Dead when it answered no.
func tmuxPane(tmuxVar, pane string) (string, mux.Liveness) {
	tty, known, err := tmux.PaneTTY(tmuxVar, pane)
	switch {
	case err != nil:
		return "", mux.Unknown
	case !known:
		return "", mux.Dead
	}
	return tty, mux.Live
}

// tmuxAlive is Facts.TmuxAlive, for a tmux mark without TMUX_PANE (a
// popup): Live when the server named in tmuxVar is the one that set it.
func tmuxAlive(tmuxVar string) mux.Liveness {
	alive, err := tmux.Alive(tmuxVar)
	switch {
	case err != nil:
		return mux.Unknown
	case !alive:
		return mux.Dead
	}
	return mux.Live
}

// herdrAlive is Facts.HerdrAlive: Live when something accepts a connection
// at socket, Dead when the path does not exist or the connection is
// refused, Unknown when the limit ends first or anything else fails. An
// empty path cannot be checked and is Unknown, which counts as live.
func herdrAlive(socket string) mux.Liveness {
	if socket == "" {
		return mux.Unknown
	}
	if _, err := os.Lstat(socket); errors.Is(err, fs.ErrNotExist) {
		return mux.Dead
	}
	conn, err := net.DialTimeout("unix", socket, checkLimit)
	switch {
	case err == nil:
		conn.Close()
		return mux.Live
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, fs.ErrNotExist):
		return mux.Dead
	}
	return mux.Unknown
}
