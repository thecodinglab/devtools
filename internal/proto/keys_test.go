package proto

import "testing"

func TestValidKeyAcceptsTheFixedNamesAndSingleScalars(t *testing.T) {
	names := KeyNames()
	if len(names) != 27 {
		t.Fatalf("%d key names, want 27 (15 named keys and f1 to f12)", len(names))
	}
	for _, n := range names {
		if !ValidKey(n) {
			t.Errorf("ValidKey(%q) = false", n)
		}
	}
	for _, k := range []string{"a", "A", "/", "ä", "ß", "日", "😀", " ", " "} {
		if !ValidKey(k) {
			t.Errorf("ValidKey(%q) = false, want a single scalar accepted", k)
		}
	}
	for _, k := range []string{
		"", "ab", "Enter", "f13", "f0", "ctrl+a", "á", "\x00", "\x1b", "\t", "\r",
		"\x7f", "\u0085", "\u009b", "\xff", "\xc3",
	} {
		if ValidKey(k) {
			t.Errorf("ValidKey(%q) = true", k)
		}
	}
	names[0] = "changed"
	if KeyNames()[0] != "enter" {
		t.Fatal("KeyNames returns the package's own slice")
	}
}

func TestModifierBitsAreKittysOrder(t *testing.T) {
	got := []int{ModShift, ModAlt, ModCtrl, ModSuper, ModHyper, ModMeta, ModCaps, ModNum}
	for i, v := range got {
		if v != 1<<i {
			t.Fatalf("modifier %d = %d, want %d", i, v, 1<<i)
		}
	}
}
