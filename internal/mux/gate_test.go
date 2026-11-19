package mux

import (
	"fmt"
	"testing"
)

// planTable is the feature table of 5.3.10 typed out as strings: id, kind,
// the capability it needs, what it is without it, and whether the need is
// late.
var planTable = []struct {
	id, kind, needs, without string
	late                     bool
}{
	{"cmd.attach", "command", "", "", false},
	{"cmd.pick", "command", "", "", false},
	{"cmd.sessions", "command", "", "", false},
	{"cmd.agents", "command", "agent-status", "error", false},
	{"cmd.agents.json", "command", "agent-status", "error", false},
	{"cmd.work", "command", "detached-open", "error", true},
	{"cmd.merge", "command", "", "", false},
	{"cmd.bookmark", "command", "", "", false},
	{"cmd.session.list", "command", "layout", "error", false},
	{"cmd.pane.list", "command", "layout", "error", false},
	{"cmd.pane.focus", "command", "pane-focus", "error", false},
	{"cmd.keys", "command", "window", "error", false},
	{"cmd.server", "command", "", "", false},
	{"hub.scope.all", "hub scope", "", "", false},
	{"hub.scope.sessions", "hub scope", "", "", false},
	{"hub.scope.agents", "hub scope", "agent-status", "absent", false},
	{"hub.row.local", "hub row", "", "", false},
	{"hub.row.agent", "hub row", "agent-status", "absent", false},
	{"hub.row.remote", "hub row", "machines", "absent", false},
	{"hub.heading", "hub row", "machines", "absent", false},
	{"hub.create", "hub key", "", "", false},
	{"hub.overlay", "host", "window", "absent", false},
	{"key.table", "key binding", "window", "absent", false},
	{"status.row", "status", "window", "absent", false},
	{"cfg.multiplexer", "config", "", "", false},
	{"cfg.root", "config", "", "", false},
	{"cfg.machine", "config", "machines", "ignored", false},
	{"cfg.bind", "config", "window", "ignored", false},
}

func TestFeatureIdsAreUnique(t *testing.T) {
	if len(Features) != 28 {
		t.Fatalf("Features has %d rows, want 28", len(Features))
	}
	seen := map[string]bool{}
	for _, f := range Features {
		if f.ID == "" {
			t.Errorf("a row has no id: %+v", f)
		}
		if seen[f.ID] {
			t.Errorf("the id %s is in Features twice", f.ID)
		}
		seen[f.ID] = true
	}
	if len(seen) != 28 {
		t.Fatalf("Features has %d ids, want 28", len(seen))
	}
}

func TestEveryCapabilityGatesAFeature(t *testing.T) {
	for _, c := range All() {
		n := 0
		for _, f := range Features {
			if f.Needs == c {
				n++
			}
		}
		if n == 0 {
			t.Errorf("no row of Features needs %s", c)
		}
	}
	for _, f := range Features {
		if f.Needs != "" && !CapsOf(All()...).Has(f.Needs) {
			t.Errorf("%s needs %q, which is not one of the six capabilities", f.ID, f.Needs)
		}
	}
}

func TestEveryRowWithANeedSaysWhatHappensWithoutIt(t *testing.T) {
	for _, f := range Features {
		switch {
		case f.Needs == "" && f.Without != NotGated:
			t.Errorf("%s needs nothing and says %q without it", f.ID, f.Without)
		case f.Needs != "" && f.Without == NotGated:
			t.Errorf("%s needs %s and does not say what it is without it", f.ID, f.Needs)
		case f.Late && (f.Needs == "" || f.Kind != "command"):
			t.Errorf("%s is late without being a gated command", f.ID)
		}
		switch f.Without {
		case NotGated, Refused, Absent, Ignored:
		default:
			t.Errorf("%s: Without %q is not one of the four behaviours", f.ID, f.Without)
		}
	}
}

func TestFeaturesMatchTheTableOfThePlan(t *testing.T) {
	if len(Features) != len(planTable) {
		t.Fatalf("Features has %d rows, the plan %d", len(Features), len(planTable))
	}
	for i, want := range planTable {
		f := Features[i]
		got := fmt.Sprintf("%s|%s|%s|%s|%v", f.ID, f.Kind, f.Needs, f.Without, f.Late)
		exp := fmt.Sprintf("%s|%s|%s|%s|%v", want.id, want.kind, want.needs, want.without, want.late)
		if got != exp {
			t.Errorf("row %d = %s, want %s", i+1, got, exp)
		}
	}
	late := 0
	for _, f := range Features {
		if f.Late {
			late++
			if f.ID != "cmd.work" {
				t.Errorf("%s is late; cmd.work is the one late row", f.ID)
			}
		}
	}
	if late != 1 {
		t.Errorf("%d late rows, want 1", late)
	}
}
