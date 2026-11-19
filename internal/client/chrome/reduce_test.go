package chrome

import (
	"testing"
)

func TestReductionOrderIsTheTable(t *testing.T) {
	// The table of 5a.6.2, row by row.
	want := []string{
		"the date",
		"the words of the roll-up",
		"names of online machines in the fleet strip",
		"the clock",
		"labels of tabs other than the current one",
		"the working and done counts; reason words in the fleet strip",
		"this machine's name",
		"tabs other than the current one, replaced by +{n}",
		"the session label: cut to 12 cells, then removed",
		"the fleet strip",
		"the current tab's label, cut at the end, down to nothing",
		"+{n}",
		"the chip's name, cut at the end until the row fits; its first letter always stays",
	}
	if len(reduction) != len(want) {
		t.Fatalf("the table has %d steps, want %d", len(reduction), len(want))
	}
	for i, st := range reduction {
		if st.n != i+1 || st.what != want[i] {
			t.Errorf("step %d is %d %q, want %d %q", i+1, st.n, st.what, i+1, want[i])
		}
	}
	right := map[int]bool{1: true, 2: true, 3: true, 4: true, 6: true, 10: true}
	for _, st := range reduction {
		if st.right != right[st.n] {
			t.Errorf("step %d: right = %v", st.n, st.right)
		}
	}

	// The steps of the mockups of 5a.9.1.
	for _, tc := range []struct {
		name  string
		s     State
		width int
		steps int
	}{
		{"standard", statusStandard(), 135, 0},
		{"standard", statusStandard(), 80, 3},
		{"standard", statusStandard(), 60, 5},
		{"standard", statusStandard(), 40, 7},
		{"standard", statusStandard(), 20, 11},
		{"remote", statusRemote(), 80, 2},
		{"remote", statusRemote(), 40, 9},
		{"remote", statusRemote(), 20, 13},
	} {
		if got := reduce(withTheme(t, tc.s), tc.width).steps; got != tc.steps {
			t.Errorf("%s at %d columns: %d steps, want %d", tc.name, tc.width, got, tc.steps)
		}
	}

	// A narrower row never undoes a step: from 300 columns down to 20 the
	// steps only grow, and every step applied leaves what it removes
	// removed.
	for _, c := range statusCases() {
		s := withTheme(t, c.s)
		if s.Mode != "normal" && s.Mode != "prefix" {
			continue
		}
		last := -1
		for w := 300; w >= 20; w-- {
			p := reduce(s, w)
			if p.steps < last {
				t.Errorf("%s: %d steps at %d columns, %d at %d", c.name, p.steps, w, last, w+1)
			}
			last = p.steps
			if p.steps >= 1 && p.date || p.steps >= 4 && p.clock || p.steps >= 7 && p.local || p.steps >= 10 && p.fleet {
				t.Errorf("%s at %d columns: plan %+v keeps what a step applied removed", c.name, w, p)
			}
		}
	}

	// The cut rules of 5a.6.3, mode by mode.
	cuts := map[string][]string{
		"prefix":    {"the label of the page's hint, cut at the end", "the label of ? more"},
		"switching": {"the right group by its steps down to the blocked count", "to", "the blocked count", "the machine name", "the session label, cut at the end"},
		"copy":      {"the hints from the left", "the selecting phrase", "of {total}", "line"},
		"scrolled":  {"any key returns", "the notice, cut at the end", "of {total}", "line"},
		"confirm":   {"the question becomes Close {program}?", "the gaps shrink to sep", "the hint labels (the keys stay)", "the program name, cut at the end with …, then dropped"},
	}
	if len(modeCuts) != len(cuts) {
		t.Errorf("%d modes have cuts, want %d", len(modeCuts), len(cuts))
	}
	for mode, whats := range cuts {
		got := modeCuts[mode]
		if len(got) != len(whats) {
			t.Errorf("%s: %d cuts, want %d", mode, len(got), len(whats))
			continue
		}
		for i := range whats {
			if got[i].what != whats[i] {
				t.Errorf("%s cut %d is %q, want %q", mode, i+1, got[i].what, whats[i])
			}
		}
	}
}

func TestNothingIsReducedAtTwoHundredColumns(t *testing.T) {
	s := withTheme(t, statusStandard())
	if p := reduce(s, 200); p != fullPlan() {
		t.Errorf("plan at 200 columns = %+v, want nothing reduced", p)
	}
	// status-standard-200, an illustration of 5a.9.1
	want := " desktop  devtools-main   1 zsh  2 claude ◆  3 nvim                                                                                          macmini   ◆ 1 needs you  ✓ 1 done  ● 2   Tue 17 Nov  14:32"
	if got := statusText(t, statusLook(), statusStandard(), 200); got != want {
		t.Errorf("at 200 columns:\n got %q\nwant %q", got, want)
	}
	// chrome-split-remote-200x50, its last row
	remote := statusRemote()
	remote.Tabs = []Tab{{ID: "t1", Index: 1, Title: "zsh", Current: true}, {ID: "t2", Index: 2, Title: "claude", Agent: "working"}}
	want = " macmini  notes-main   1 zsh  2 claude ●                                                                                                     macmini   ◆ 1 needs you  ✓ 1 done  ● 2   Tue 17 Nov  14:32"
	if got := statusText(t, statusLook(), remote, 200); got != want {
		t.Errorf("a remote session at 200 columns:\n got %q\nwant %q", got, want)
	}
}
