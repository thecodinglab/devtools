package integration

// Level 5: the client attached to a real server through a real terminal.

import (
	"strings"
	"testing"
)

func TestAttachShowsTheProgramAndTheStatusRow(t *testing.T) {
	s := newL5(t, "hex")
	x := s.attach(80, 24)
	sc := shows(x, "ready")
	// The program fills the window but the last row, which is the status
	// row: the session's label and its one tab, current.
	if sc.Rows != 24 || !strings.HasPrefix(plainRows(sc)[0], "ready") {
		t.Fatalf("the program is not at the top of the window:\n%s", sc.Plain())
	}
	if !strings.Contains(statusRow(sc), "[1") {
		t.Fatalf("the status row %q shows no current tab", statusRow(sc))
	}
}
