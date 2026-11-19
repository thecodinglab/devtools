package backend

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"devtools/internal/mux"
	"devtools/internal/testkit"
)

// tmuxVar is a value of TMUX for the socket and pid.
func tmuxVar(socket, pid string) string { return socket + "," + pid + ",0" }

func TestFactsMarksADeadTmuxAsDead(t *testing.T) {
	t.Run("the socket of TMUX does not exist", func(t *testing.T) {
		log := installFakeTmux(t)
		v := tmuxVar(filepath.Join(testkit.ShortTempDir(t), "gone"), "4242")
		f := Facts([]string{"TMUX=" + v, "TMUX_PANE=%1"}, nil, nil)
		if tty, l := f.TmuxPane(v, "%1"); l != mux.Dead || tty != "" {
			t.Errorf("TmuxPane = %q, %v; want Dead", tty, l)
		}
		if l := f.TmuxAlive(v); l != mux.Dead {
			t.Errorf("TmuxAlive = %v, want Dead", l)
		}
		c := mux.Detect(f)
		if !slices.Contains(c.Dead, "TMUX") || c.Where == mux.InTmux {
			t.Errorf("Detect = %+v, want TMUX in Dead and not InTmux", c)
		}
		wantEmpty(t, log)
	})
	t.Run("nothing listens on the socket", func(t *testing.T) {
		log := installFakeTmux(t)
		socket := staleSocket(t)
		t.Setenv("TMUX_ERR", "no server running on "+socket)
		v := tmuxVar(socket, "4242")
		f := Facts([]string{"TMUX=" + v, "TMUX_PANE=%1"}, nil, nil)
		if _, l := f.TmuxPane(v, "%1"); l != mux.Dead {
			t.Errorf("TmuxPane = %v, want Dead", l)
		}
		if c := mux.Detect(f); !slices.Contains(c.Dead, "TMUX") {
			t.Errorf("Detect = %+v, want TMUX in Dead", c)
		}
		wantCalls(t, log, "-S "+socket+" display-message -p -t %1 #{pane_tty}", "-S "+socket+" display-message -p -t %1 #{pane_tty}")
	})
	t.Run("the server answers and has no such pane", func(t *testing.T) {
		installFakeTmux(t)
		socket := herdrSocket(t)
		v := tmuxVar(socket, "4242")
		if _, l := Facts(nil, nil, nil).TmuxPane(v, "%9"); l != mux.Dead {
			t.Errorf("TmuxPane of an unknown pane = %v, want Dead", l)
		}
	})
	t.Run("a popup whose server is another one", func(t *testing.T) {
		log := installFakeTmux(t)
		socket := herdrSocket(t)
		t.Setenv("TMUX_DISPLAY", "999")
		v := tmuxVar(socket, "4242")
		f := Facts([]string{"TMUX=" + v}, nil, nil)
		if l := f.TmuxAlive(v); l != mux.Dead {
			t.Errorf("TmuxAlive with another pid = %v, want Dead", l)
		}
		if c := mux.Detect(f); !slices.Contains(c.Dead, "TMUX") {
			t.Errorf("Detect = %+v, want TMUX in Dead", c)
		}
		wantCalls(t, log, "-S "+socket+" display-message -p #{pid}", "-S "+socket+" display-message -p #{pid}")
	})
	t.Run("a live server is live", func(t *testing.T) {
		installFakeTmux(t)
		socket := herdrSocket(t)
		v := tmuxVar(socket, "4242")
		t.Setenv("TMUX_DISPLAY", "/dev/pts/7")
		if tty, l := Facts(nil, nil, nil).TmuxPane(v, "%1"); l != mux.Live || tty != "/dev/pts/7" {
			t.Errorf("TmuxPane = %q, %v; want /dev/pts/7, Live", tty, l)
		}
		t.Setenv("TMUX_DISPLAY", "4242")
		if l := Facts(nil, nil, nil).TmuxAlive(v); l != mux.Live {
			t.Errorf("TmuxAlive with its own pid = %v, want Live", l)
		}
	})
}

func TestFactsReportsAHerdrSocketNobodyListensOnAsDead(t *testing.T) {
	stale := staleSocket(t)
	missing := filepath.Join(testkit.ShortTempDir(t), "herdr.sock")
	live := herdrSocket(t)
	f := Facts(nil, nil, nil)
	for _, c := range []struct {
		name, socket string
		want         mux.Liveness
	}{
		{"a socket nobody listens on", stale, mux.Dead},
		{"a socket path that does not exist", missing, mux.Dead},
		{"a socket that accepts", live, mux.Live},
	} {
		if got := f.HerdrAlive(c.socket); got != c.want {
			t.Errorf("%s: HerdrAlive = %v, want %v", c.name, got, c.want)
		}
	}
	dead := mux.Detect(Facts([]string{"HERDR_ENV=1", "HERDR_SOCKET_PATH=" + stale}, nil, nil))
	if !slices.Contains(dead.Dead, "HERDR_ENV") || dead.Where == mux.InHerdr {
		t.Errorf("Detect with a stale herdr socket = %+v, want HERDR_ENV in Dead", dead)
	}
	inside := mux.Detect(Facts([]string{"HERDR_ENV=1", "HERDR_SOCKET_PATH=" + live}, nil, nil))
	if inside.Where != mux.InHerdr || len(inside.Dead) != 0 {
		t.Errorf("Detect with a live herdr socket = %+v, want InHerdr", inside)
	}
}

func TestFactsReportsAnUnansweredCheckAsUnknown(t *testing.T) {
	t.Run("a tmux that does not answer within the limit", func(t *testing.T) {
		installFakeTmux(t)
		// the socket accepts and never answers; the fake stands for the
		// tmux client that waits on it
		socket := herdrSocket(t)
		t.Setenv("TMUX_SLEEP", "5")
		v := tmuxVar(socket, "4242")
		f := Facts([]string{"TMUX=" + v, "TMUX_PANE=%1"}, nil, nil)
		start := time.Now()
		if _, l := f.TmuxPane(v, "%1"); l != mux.Unknown {
			t.Errorf("TmuxPane past the limit = %v, want Unknown", l)
		}
		if l := f.TmuxAlive(v); l != mux.Unknown {
			t.Errorf("TmuxAlive past the limit = %v, want Unknown", l)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("two checks took %s; the limit is 500 ms each", d)
		}
		c := mux.Detect(f)
		if c.Where != mux.InTmux || slices.Contains(c.Dead, "TMUX") {
			t.Errorf("Detect = %+v, want InTmux with TMUX not in Dead (N18)", c)
		}
	})
	t.Run("a tmux that fails otherwise", func(t *testing.T) {
		installFakeTmux(t)
		socket := herdrSocket(t)
		t.Setenv("TMUX_ERR", "protocol version mismatch")
		v := tmuxVar(socket, "4242")
		if _, l := Facts(nil, nil, nil).TmuxPane(v, "%1"); l != mux.Unknown {
			t.Errorf("TmuxPane = %v, want Unknown", l)
		}
		if l := Facts(nil, nil, nil).TmuxAlive(v); l != mux.Unknown {
			t.Errorf("TmuxAlive = %v, want Unknown", l)
		}
	})
	t.Run("a herdr socket that cannot be reached for another reason", func(t *testing.T) {
		dir := filepath.Join(testkit.ShortTempDir(t), "locked")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		f := Facts(nil, nil, nil)
		if l := f.HerdrAlive(filepath.Join(dir, "herdr.sock")); l != mux.Unknown {
			t.Errorf("HerdrAlive behind a directory without permission = %v, want Unknown", l)
		}
		if l := f.HerdrAlive(""); l != mux.Unknown {
			t.Errorf("HerdrAlive of an empty path = %v, want Unknown", l)
		}
		c := mux.Detect(Facts([]string{"HERDR_ENV=1"}, nil, nil))
		if c.Where != mux.InHerdr || len(c.Dead) != 0 {
			t.Errorf("Detect with HERDR_ENV and no socket path = %+v, want InHerdr (N18)", c)
		}
	})
	t.Run("a missing socket and a refused connection are Dead, not Unknown", func(t *testing.T) {
		log := installFakeTmux(t)
		f := Facts(nil, nil, nil)
		missing := filepath.Join(testkit.ShortTempDir(t), "gone")
		stale := staleSocket(t)
		if l := f.HerdrAlive(missing); l != mux.Dead {
			t.Errorf("HerdrAlive of a missing socket = %v", l)
		}
		if l := f.HerdrAlive(stale); l != mux.Dead {
			t.Errorf("HerdrAlive of a refused socket = %v", l)
		}
		if _, l := f.TmuxPane(tmuxVar(missing, "1"), "%1"); l != mux.Dead {
			t.Errorf("TmuxPane of a missing socket = %v", l)
		}
		wantEmpty(t, log)
		t.Setenv("TMUX_ERR", "no server running on "+stale)
		if l := f.TmuxAlive(tmuxVar(stale, "1")); l != mux.Dead {
			t.Errorf("TmuxAlive of a refused socket = %v", l)
		}
	})
}

func TestFactsReadsTheEnvironmentListAndTheTerminals(t *testing.T) {
	f := Facts([]string{"A=1", "B=two", "A=3", "EMPTY="}, nil, nil)
	for name, want := range map[string]string{"A": "3", "B": "two", "EMPTY": "", "MISSING": ""} {
		if got := f.Getenv(name); got != want {
			t.Errorf("Getenv(%s) = %q, want %q", name, got, want)
		}
	}
	if f.Devtools == nil || f.TmuxPane == nil || f.TmuxAlive == nil || f.HerdrAlive == nil || f.SameTTY == nil {
		t.Fatal("a check of Facts is nil")
	}
	if f.Terminal {
		t.Error("Terminal without files")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if p := Facts(nil, r, w); p.Terminal {
		t.Error("Terminal on two pipes")
	}

	_, slave := testkit.OpenPTY(t, 80, 24)
	tty := Facts(nil, slave, slave)
	if !tty.Terminal || !tty.HasTTY {
		t.Errorf("on a PTY: Terminal %v, HasTTY %v; want both", tty.Terminal, tty.HasTTY)
	}
	if !tty.SameTTY(slave.Name()) {
		t.Errorf("SameTTY(%s) is false for the PTY the files are", slave.Name())
	}
	if tty.SameTTY(os.DevNull) || tty.SameTTY("") {
		t.Error("SameTTY is true for /dev/null or an empty path")
	}
	if Facts(nil, slave, w).Terminal {
		t.Error("Terminal when only stdin is a terminal")
	}
	if !Facts(nil, r, slave).HasTTY {
		t.Error("HasTTY is false when stdout is a terminal")
	}
}
