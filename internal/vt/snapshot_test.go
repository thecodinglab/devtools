//go:build cgo

package vt

import (
	"fmt"
	"strings"
	"testing"
)

// roundTrip encodes tm and restores it at the same size with o's
// callbacks; the restored terminal is closed with the test.
func roundTrip(t *testing.T, tm *term, o ...func(*Options)) *term {
	t.Helper()
	b, err := Encode(tm)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	cols, rows := tm.Size()
	opts := Options{Cols: cols, Rows: rows, History: 1000}
	for _, f := range o {
		f(&opts)
	}
	first, base := tm.Range()
	r, err := Restore(opts, b, first, base)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	t.Cleanup(r.Close)
	return r.(*term)
}

func TestSnapshotVersionIsOneForThisEngine(t *testing.T) {
	// libghostty-vt 1.0.1 writes snapshot version 1 (snapshot/envelope.zig).
	// A library update that changes it changes what a live upgrade between
	// the two builds can carry: look at internal/handover before changing
	// this number.
	if v := SnapshotVersion(); v != 1 {
		t.Fatalf("SnapshotVersion() = %d, want 1", v)
	}
	if got := ScreenFormats(); len(got) != 1 || got[0] != "ghostty-snapshot/1" {
		t.Fatalf("ScreenFormats() = %q", got)
	}
}

func TestASnapshotKeepsScreenModesTitleAndHistory(t *testing.T) {
	tm, rec := recorded(t, 40, 6)
	for i := range 30 {
		fmt.Fprintf(writerOf(tm), "line %d \x1b[1;31mred\x1b[m é 🙂\r\n", i)
	}
	tm.Write([]byte("\x1b]0;the title\x07\x1b]7;file://test-host/tmp/x\x1b\\"))
	tm.Write([]byte("\x1b[?2004h\x1b[?1004h\x1b[?1h\x1b[?1002h\x1b[?1006h\x1b[>5u"))
	tm.Write([]byte("prompt$ "))
	_ = rec
	before := snapOf(tm)
	first, base := tm.Range()

	r, rrec := func() (*term, *recorder) {
		rr := &recorder{}
		return roundTrip(t, tm, rr.options), rr
	}()
	if got := snapOf(r); got != before {
		t.Errorf("restored terminal:\n%s\nwant\n%s", got, before)
	}
	if f, b := r.Range(); f != first || b != base {
		t.Errorf("Range after restore = %d..%d, want %d..%d", f, b, first, base)
	}
	for abs := first; abs < base; abs++ {
		want, _ := tm.Line(abs)
		if got, ok := r.Line(abs); !ok || got != want {
			t.Fatalf("history line %d = %q (%v), want %q", abs, got, ok, want)
		}
	}
	m := r.Modes()
	if !m.Paste || !m.Focus || !m.AppCursor || m.Mouse != "drag" || !m.MouseSGR || m.Kitty != 5 {
		t.Errorf("modes after restore %+v", m)
	}
	// The restored terminal goes on: output after the restore lands after
	// the prompt, and the callbacks are the new ones.
	r.Write([]byte("typed\x1b]2;new title\x07"))
	if got := r.Row(r.Cursor().Y); !strings.HasPrefix(stripSGR(got), "prompt$ typed") {
		t.Errorf("row after more output %q", got)
	}
	if len(rrec.titles) == 0 || rrec.titles[len(rrec.titles)-1] != "new title" {
		t.Errorf("titles of the restored terminal %q", rrec.titles)
	}
}

func TestASnapshotKeepsTheAlternateScreenAndThePrimaryUnderIt(t *testing.T) {
	tm := termAt(t, 30, 5)
	for i := range 12 {
		fmt.Fprintf(writerOf(tm), "primary %d\r\n", i)
	}
	tm.Write([]byte("\x1b[?1049h\x1b[Halt screen\x1b[>1u"))
	first, base := tm.Range()
	r := roundTrip(t, tm)
	if m := r.Modes(); !m.Alt || m.Kitty != 1 {
		t.Fatalf("modes after restore %+v, want the alternate screen with kitty flags 1", m)
	}
	if got := r.Row(0); !strings.Contains(got, "alt screen") {
		t.Errorf("row 0 on the alternate screen %q", got)
	}
	r.Write([]byte("\x1b[?1049l"))
	if m := r.Modes(); m.Alt || m.Kitty != 0 {
		t.Errorf("modes after leaving the alternate screen %+v", m)
	}
	if got := r.Row(3); !strings.Contains(got, "primary 11") {
		t.Errorf("the primary screen after leaving the alternate one: row 3 %q", got)
	}
	// Leaving the alternate screen renumbers the history once: above every
	// number handed out before.
	r.Write([]byte("x"))
	if f, b := r.Range(); f < base+5 || b < f {
		t.Errorf("Range after the primary screen returned = %d..%d, want above %d..%d", f, b, first, base)
	}
}

func TestASnapshotKeepsAnUnfinishedSequence(t *testing.T) {
	tm, _ := recorded(t, 20, 3)
	tm.Write([]byte("\x1b]0;half"))
	rr := &recorder{}
	r := roundTrip(t, tm, rr.options)
	r.Write([]byte("-done\x07"))
	if len(rr.titles) == 0 || rr.titles[len(rr.titles)-1] != "half-done" {
		t.Fatalf("title after the rest of the OSC %q, want half-done", rr.titles)
	}
	// A UTF-8 character cut in two.
	tm2 := termAt(t, 20, 3)
	tm2.Write([]byte("é\xc3"))
	r2 := roundTrip(t, tm2)
	r2.Write([]byte("\xa9!"))
	if got := r2.Row(0); !strings.HasPrefix(got, "éé!") {
		t.Fatalf("row after the rest of the character %q, want éé!", got)
	}
	// And the restored terminal can be encoded mid-sequence again.
	r2.Write([]byte("\x1b]2;again"))
	r3 := roundTrip(t, r2)
	_ = r3
}

func TestGraphemeClusteringOffStaysOffAfterRestore(t *testing.T) {
	tm := termAt(t, 20, 3)
	tm.Write([]byte("\x1b[?2027l"))
	r := roundTrip(t, tm)
	r.Write([]byte("\x1b[?2027$p"))
	if got, want := string(r.Replies()), "\x1b[?2027;2$y"; got != want {
		t.Fatalf("DECRQM 2027 after restore = %q, want %q (off)", got, want)
	}
}

func TestRestoreResizesToThePaneAndRefusesJunk(t *testing.T) {
	tm := termAt(t, 30, 5)
	tm.Write([]byte("hello"))
	b, err := Encode(tm)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(Options{Cols: 40, Rows: 8, History: 1000}, b, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if c, rr := r.Size(); c != 40 || rr != 8 {
		t.Errorf("size %dx%d, want 40x8", c, rr)
	}
	if c, rr := r.(*term).libSize(); c != 40 || rr != 8 {
		t.Errorf("library size %dx%d, want 40x8", c, rr)
	}
	for _, junk := range [][]byte{nil, []byte("GHOSTSNP"), append([]byte("GHOSTSNP\x09\x00"), b[10:]...), b[:len(b)/2]} {
		if r, err := Restore(Options{Cols: 30, Rows: 5}, junk, 0, 0); err == nil {
			r.Close()
			t.Errorf("Restore of %d bytes of junk succeeded", len(junk))
		}
	}
	if _, err := Restore(Options{Cols: 30, Rows: 5}, b, 5, 2); err == nil {
		t.Errorf("Restore with base below first succeeded")
	}
}

// snapOf is what a viewer sees of a terminal, as text: size, cursor,
// modes and rows.
func snapOf(tm *term) string {
	var b strings.Builder
	c, r := tm.Size()
	fmt.Fprintf(&b, "%dx%d cursor %+v modes %+v\n", c, r, tm.Cursor(), tm.Modes())
	for y := range r {
		b.WriteString(tm.Row(y) + "\n")
	}
	return b.String()
}

// stripSGR drops the escape sequences of a wire row.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i++; i < len(s) && (s[i] < '@' || s[i] > '~' || s[i] == '['); i++ {
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
