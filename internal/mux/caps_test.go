package mux

import (
	"slices"
	"strings"
	"testing"
)

// The three sets of the static table backend.CapsOf (5.3.11), written with
// the constructor of this package. These are copies typed into the test; a
// test of package mux cannot import internal/backend (rule 12), so the table
// itself is pinned by TestCapsOfIsTheTableOfTheSpec in
// internal/backend/backend_test.go.
var (
	devtoolsSet = CapsOf(CapMachines, CapAgents, CapWindow, CapPaneFocus, CapLayout, CapDetached)
	tmuxSet     = CapsOf(CapDetached)
	herdrSet    = CapsOf(CapAgents)
)

func TestCapsValue(t *testing.T) {
	sets := []struct {
		name string
		caps Caps
		want []Capability
	}{
		{"devtools", devtoolsSet, All()},
		{"tmux", tmuxSet, []Capability{CapDetached}},
		{"herdr", herdrSet, []Capability{CapAgents}},
		{"zero", Caps{}, nil},
	}
	for _, s := range sets {
		t.Run(s.name, func(t *testing.T) {
			if got := s.caps.List(); !slices.Equal(got, s.want) {
				t.Fatalf("List() = %v, want %v", got, s.want)
			}
			for _, c := range All() {
				if got, want := s.caps.Has(c), slices.Contains(s.want, c); got != want {
					t.Errorf("Has(%s) = %v, want %v", c, got, want)
				}
			}
			if s.caps.Has("screen") || s.caps.Has("") {
				t.Errorf("Has of a name that is not a capability is true")
			}
			if again := CapsOf(s.caps.List()...); again != s.caps {
				t.Errorf("CapsOf(List()...) = %v, want %v", again.List(), s.want)
			}
		})
	}

	// The order of the arguments and repeated members do not matter, and
	// two sets with the same members are equal with ==.
	if a, b := CapsOf(CapLayout, CapAgents), CapsOf(CapAgents, CapLayout, CapAgents); a != b {
		t.Fatalf("CapsOf(layout, agent-status) != CapsOf(agent-status, layout, agent-status)")
	}
	if CapsOf("not-a-capability") != (Caps{}) {
		t.Fatalf("CapsOf of an unknown name is not the empty set")
	}
	if CapsOf() != (Caps{}) {
		t.Fatalf("CapsOf() is not the zero Caps")
	}

	// List is in the order of All, whatever the order of CapsOf.
	if got, want := CapsOf(CapDetached, CapMachines, CapLayout).List(), []Capability{CapMachines, CapLayout, CapDetached}; !slices.Equal(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}

	// All returns a copy: changing it changes nothing in the package.
	a := All()
	a[0] = "changed"
	if All()[0] != CapMachines {
		t.Fatalf("All() is shared with its callers")
	}
}

func TestParseAcceptsThreeNames(t *testing.T) {
	for _, name := range []string{"devtools", "tmux", "herdr"} {
		got, err := Parse(name)
		if err != nil {
			t.Errorf("Parse(%q) = %v", name, err)
			continue
		}
		if string(got) != name {
			t.Errorf("Parse(%q) = %q", name, got)
		}
	}
	if got := []Backend{Devtools, Tmux, Herdr}; !slices.Equal(got, []Backend{"devtools", "tmux", "herdr"}) {
		t.Errorf("the backend constants are %v", got)
	}
	for _, name := range []string{"", "screen", "zellij", "Tmux", "HERDR", " devtools", "tmux "} {
		got, err := Parse(name)
		if err == nil {
			t.Errorf("Parse(%q) = %q, want an error", name, got)
			continue
		}
		if got != "" {
			t.Errorf("Parse(%q) returned %q with its error", name, got)
		}
		for _, accepted := range []string{"devtools", "tmux", "herdr"} {
			if !strings.Contains(err.Error(), accepted) {
				t.Errorf("Parse(%q) error %q does not name %s", name, err, accepted)
			}
		}
	}
}

func TestCapsTableHasSixCapabilities(t *testing.T) {
	want := []Capability{"machines", "agent-status", "window", "pane-focus", "layout", "detached-open"}
	got := All()
	if !slices.Equal(got, want) {
		t.Fatalf("All() = %v, want %v", got, want)
	}
	seen := map[Capability]bool{}
	for _, c := range got {
		if seen[c] {
			t.Fatalf("All() names %s twice", c)
		}
		seen[c] = true
	}
	for _, s := range []struct {
		name string
		caps Caps
		n    int
	}{{"devtools", devtoolsSet, 6}, {"tmux", tmuxSet, 1}, {"herdr", herdrSet, 1}} {
		if n := len(s.caps.List()); n != s.n {
			t.Errorf("the set of %s has %d capabilities, want %d", s.name, n, s.n)
		}
	}
}

// fakeMux satisfies Multiplexer with a name and a set; every other method
// is never called by Need.
type fakeMux struct {
	Multiplexer
	name Backend
	caps Caps
}

func (f fakeMux) Name() Backend { return f.name }
func (f fakeMux) Caps() Caps    { return f.caps }

func TestNeedReturnsNilOrErrUnsupported(t *testing.T) {
	m := fakeMux{name: Tmux, caps: tmuxSet}
	if err := Need(m, CapDetached); err != nil {
		t.Fatalf("Need(tmux, detached-open) = %v, want nil", err)
	}
	err := Need(m, CapAgents)
	u, ok := err.(*ErrUnsupported)
	if !ok {
		t.Fatalf("Need(tmux, agent-status) = %#v, want *ErrUnsupported", err)
	}
	if u.Backend != Tmux || u.Capability != CapAgents {
		t.Fatalf("Need(tmux, agent-status) = %+v", *u)
	}
	if u.Error() == "" {
		t.Fatalf("(*ErrUnsupported).Error is empty")
	}
}
