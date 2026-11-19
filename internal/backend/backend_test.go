package backend_test

import (
	"slices"
	"testing"

	"devtools/internal/backend"
	"devtools/internal/mux"
)

// TestCapsOfIsTheTableOfTheSpec pins the static table backend.CapsOf to the
// capability table of 5.3.10: devtools all six, tmux detached-open, herdr
// agent-status, an unknown backend nothing. The rows are typed out as plain
// strings, not computed from mux.All or from Caps() of an implementation, so
// a wrong row in CapsOf fails here even though every Caps() returns
// CapsOf(Name()) and the refusal tests take their sets from the same table.
func TestCapsOfIsTheTableOfTheSpec(t *testing.T) {
	rows := []struct {
		backend mux.Backend
		want    []string
	}{
		{"devtools", []string{"machines", "agent-status", "window", "pane-focus", "layout", "detached-open"}},
		{"tmux", []string{"detached-open"}},
		{"herdr", []string{"agent-status"}},
		{"screen", nil},
		{"", nil},
	}
	everyName := []string{"machines", "agent-status", "window", "pane-focus", "layout", "detached-open"}
	for _, r := range rows {
		t.Run("backend="+string(r.backend), func(t *testing.T) {
			caps := backend.CapsOf(r.backend)
			var got []string
			for _, c := range caps.List() {
				got = append(got, string(c))
			}
			if !slices.Equal(got, r.want) {
				t.Fatalf("CapsOf(%q).List() = %v, want %v", r.backend, got, r.want)
			}
			for _, name := range everyName {
				if has, want := caps.Has(mux.Capability(name)), slices.Contains(r.want, name); has != want {
					t.Errorf("CapsOf(%q).Has(%s) = %v, want %v", r.backend, name, has, want)
				}
			}
		})
	}

	// The constants name the rows above.
	for _, c := range []struct {
		constant mux.Backend
		name     string
	}{{mux.Devtools, "devtools"}, {mux.Tmux, "tmux"}, {mux.Herdr, "herdr"}} {
		if string(c.constant) != c.name {
			t.Errorf("the backend constant for %s is %q", c.name, c.constant)
		}
	}
}
