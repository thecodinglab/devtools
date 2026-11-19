package platform

import "testing"

func TestStatNamesWithSpacesAndParentheses(t *testing.T) {
	p, err := parseStat(42, "42 (a (b) c) S 7 9 9 0 -1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "a (b) c" || p.PPid != 7 || p.Pgid != 9 {
		t.Errorf("parsed %+v", p)
	}
	for _, bad := range []string{"42 (x", "42 (x) S", "42 (x) S y 9"} {
		if _, err := parseStat(42, bad); err == nil {
			t.Errorf("the stat line %q gave no error", bad)
		}
	}
	if got := splitCmdline([]byte("a\x00b c\x00\x00\x00")); len(got) != 4 || got[1] != "b c" {
		t.Errorf("splitCmdline = %q", got)
	}
	if got := splitCmdline(nil); got != nil {
		t.Errorf("splitCmdline of a kernel thread = %q", got)
	}
}
