package mux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelect(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "herdr")
	tmuxEnv := "/tmp/tmux-501/default,1,0"
	tests := []struct {
		name                       string
		tmux, herdrEnv, herdrBin   string
		override, configured, want string
		wantErr                    bool
	}{
		{name: "plain terminal", herdrBin: bin, want: "herdr"},
		{name: "inside herdr", herdrEnv: "1", herdrBin: bin, want: "herdr"},
		{name: "inside tmux", tmux: tmuxEnv, herdrBin: bin, want: "tmux"},
		{name: "tmux nested in herdr", tmux: tmuxEnv, herdrEnv: "1", herdrBin: bin, want: "tmux"},
		{name: "herdr missing", herdrBin: missing, want: "tmux"},
		{name: "env override beats tmux", tmux: tmuxEnv, herdrBin: bin, override: "herdr", want: "herdr"},
		{name: "env override beats config", herdrBin: bin, override: "herdr", configured: "tmux", want: "herdr"},
		{name: "config override", herdrBin: bin, configured: "tmux", want: "tmux"},
		{name: "config override beats tmux", tmux: tmuxEnv, herdrBin: bin, configured: "herdr", want: "herdr"},
		{name: "explicit herdr even if missing", herdrBin: missing, override: "herdr", want: "herdr"},
		{name: "invalid env", herdrBin: bin, override: "screen", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TMUX", tt.tmux)
			t.Setenv("HERDR_ENV", tt.herdrEnv)
			t.Setenv("HERDR_BIN_PATH", tt.herdrBin)
			t.Setenv("DEVTOOLS_MULTIPLEXER", tt.override)
			got, err := Select(tt.configured)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Select() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("Select() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectFindsHerdrOnPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("DEVTOOLS_MULTIPLEXER", "")
	t.Setenv("HERDR_BIN_PATH", "")
	t.Setenv("PATH", dir)
	if got, _ := Select(""); got != Herdr {
		t.Fatalf("Select() = %q, want herdr", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got, _ := Select(""); got != Tmux {
		t.Fatalf("Select() = %q, want tmux", got)
	}
}
