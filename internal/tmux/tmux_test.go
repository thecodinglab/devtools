package tmux

import "testing"

func TestParseSessionList(t *testing.T) {
	got, err := parseSessionList("work\t1\t0\nops\t2\t1\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []Session{
		{Name: "work", Windows: 1, Attached: false},
		{Name: "ops", Windows: 2, Attached: true},
	}
	if len(got) != len(want) {
		t.Fatalf("sessions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("session %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
