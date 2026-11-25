package cli

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildIdentity(t *testing.T) {
	info := func(main string, settings ...string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			bi := &debug.BuildInfo{Main: debug.Module{Version: main}}
			for i := 0; i+1 < len(settings); i += 2 {
				bi.Settings = append(bi.Settings, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
			}
			return bi, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	for _, c := range []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"stamped wins", "0.1.0+04c5fef", info("v1.2.3", "vcs.revision", "6ee3170abcdef"), "0.1.0+04c5fef"},
		{"module version", "", info("v1.2.3", "vcs.revision", "6ee3170abcdef"), "v1.2.3"},
		{"checkout", "", info("(devel)", "vcs.revision", "04c5fefa1b2c3d4e", "vcs.modified", "false"), "04c5fef"},
		{"dirty checkout", "", info("(devel)", "vcs.revision", "04c5fefa1b2c3d4e", "vcs.modified", "true"), "04c5fef-dirty"},
		{"no vcs", "", info("(devel)"), "dev"},
		{"no build info", "", none, "dev"},
	} {
		if got := buildIdentity(c.stamped, c.read); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVersionCommandPrintsTheBuild(t *testing.T) {
	newWorld(t)
	for _, args := range [][]string{{"version"}, {"--version"}} {
		wantResult(t, runCommand(args...), 0, "")
		r := runCommand(args...)
		if r.stdout != version+"\n" || r.stderr != "" {
			t.Fatalf("%v: stdout %q stderr %q", args, r.stdout, r.stderr)
		}
	}
	wantResult(t, runCommand("version", "extra"), 1, "usage: devtools version")
}

// server status says a restart is pending only when the server saw
// another build installed; a server of another version than this devtools
// is told as that, since this devtools may not be the installed build.
func TestServerStatusSaysTheServerRunsAnotherBuild(t *testing.T) {
	w := newWorld(t)

	w.server.set(func(f *fakeServer) { f.version = version })
	r := runCommand("server", "status")
	wantStatus(t, r, 0)
	if strings.Contains(r.stdout, "restart") {
		t.Fatalf("the same build asks for a restart:\n%s", r.stdout)
	}
	var st serverStatus
	r = runCommand("server", "status", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil || st.Restart || st.Other || st.CLIVersion != version || st.Installed != "" {
		t.Fatalf("same build: %q, %v", r.stdout, err)
	}

	w.server.set(func(f *fakeServer) { f.version = "0.1.0+6ee3170" })
	r = runCommand("server", "status")
	wantContains(t, "stdout", r.stdout, "The server runs 0.1.0+6ee3170, and this devtools is "+version+".\n")
	if strings.Contains(r.stdout, "restart") {
		t.Fatalf("another build of this devtools asks for a restart:\n%s", r.stdout)
	}
	r = runCommand("server", "status", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil || st.Restart || !st.Other || st.Version != "0.1.0+6ee3170" {
		t.Fatalf("another build: %q, %v", r.stdout, err)
	}

	// the server saw an update installed: its word is the installed version
	w.server.set(func(f *fakeServer) { f.version, f.installed = version, "0.1.0+04c5fef" })
	r = runCommand("server", "status")
	wantContains(t, "stdout", r.stdout, "The server runs "+version+", but 0.1.0+04c5fef is installed.")
	r = runCommand("server", "status", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &st); err != nil || !st.Restart || st.Other || st.Installed != "0.1.0+04c5fef" {
		t.Fatalf("installed update: %q, %v", r.stdout, err)
	}
}
