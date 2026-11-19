//go:build cgo

package vt

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// reflowLog records the calls of Options.OnReflow.
type reflowLog struct {
	n     int
	remap func(int64) (int64, bool)
}

func (r *reflowLog) options(o *Options) {
	o.OnReflow = func(m func(int64) (int64, bool)) {
		r.n++
		r.remap = m
	}
}

// numberedLine is the text of line i of writeLines.
func numberedLine(i int) string { return fmt.Sprintf("L%06d", i) }

// writeLines writes the lines from to from+n-1, chunk lines per Write.
func writeLines(tm *term, from, n, chunk int) {
	var b bytes.Buffer
	for i := from; i < from+n; i++ {
		b.WriteString(numberedLine(i))
		b.WriteString("\r\n")
		if (i-from+1)%chunk == 0 {
			tm.Write(b.Bytes())
			b.Reset()
		}
	}
	tm.Write(b.Bytes())
}

// textAt is the text of an absolute line, or a marker when it cannot be
// read.
func textAt(tm *term, abs int64) string {
	s, _, ok := tm.Text(abs)
	if !ok {
		return "<not ok>"
	}
	return s
}

// rangeWatch fails when Range broke one of its rules since the last check:
// first went back, base went back without a resize (a taller screen takes
// rows from the history, which keep their numbers), more than History
// lines, first above base, or a line at the edges that cannot be read.
type rangeWatch struct {
	first, base int64
	history     int64
	resized     bool // a resize since the last check
}

func (w *rangeWatch) check(t *testing.T, tm *term, what string) {
	t.Helper()
	first, base := tm.Range()
	if first < w.first || (base < w.base && !w.resized) {
		t.Fatalf("%s: Range went back from [%d, %d] to [%d, %d]", what, w.first, w.base, first, base)
	}
	w.resized = false
	if first < 0 || first > base || base-first > w.history {
		t.Fatalf("%s: Range [%d, %d] with History %d", what, first, base, w.history)
	}
	if !tm.altActive() {
		if _, ok := tm.Line(first); !ok {
			t.Fatalf("%s: Line(first=%d) is not ok", what, first)
		}
	}
	if _, ok := tm.Line(base); !ok {
		t.Fatalf("%s: Line(base=%d) is not ok", what, base)
	}
	w.first, w.base = first, base
}

func TestAbsoluteNumbersSurviveTrimming(t *testing.T) {
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 10000 })
	writeLines(tm, 0, 25000, 100)
	read := map[int64]string{}
	for _, n := range []int64{20000, 21000, 22500, 24000, 24900} {
		read[n] = textAt(tm, n)
		if read[n] != numberedLine(int(n)) {
			t.Fatalf("line %d reads %q before the trimming, want %q", n, read[n], numberedLine(int(n)))
		}
	}
	writeLines(tm, 25000, 5000, 100)
	first, _ := tm.Range()
	for n, want := range read {
		if n < first {
			t.Fatalf("line %d is no longer retained (first %d); the test reads the wrong lines", n, first)
		}
		if got := textAt(tm, n); got != want {
			t.Errorf("line %d reads %q after 30,000 lines, read %q before", n, got, want)
		}
	}
	if got := textAt(tm, first); got != numberedLine(int(first)) {
		t.Errorf("the first retained line %d reads %q", first, got)
	}
}

func TestRangeAfterThirtyThousandLines(t *testing.T) {
	const history, rows = 10000, 24
	tm := termAt(t, 80, rows, func(o *Options) { o.History = history })
	var lastFirst, lastBase int64
	for written := 0; written < 30000; written += 50 {
		writeLines(tm, written, 50, 50)
		first, base := tm.Range()
		if first < lastFirst || base < lastBase {
			t.Fatalf("after %d lines Range [%d, %d] went back from [%d, %d]", written+50, first, base, lastFirst, lastBase)
		}
		lastFirst, lastBase = first, base
		// Each line ends with CR LF, so after k lines the cursor stands on
		// row min(k, rows-1) and k - (rows-1) lines left the screen.
		left := int64(max(0, written+50-(rows-1)))
		if base != left {
			t.Fatalf("after %d lines base is %d, want the %d lines that left the screen", written+50, base, left)
		}
		if want := min(left, history); base-first != want {
			t.Fatalf("after %d lines base - first is %d, want %d", written+50, base-first, want)
		}
	}
}

func TestRangeIsExactWithASmallHistory(t *testing.T) {
	// With History 100 the line limit is 1,100, so a 4 KiB piece of bare line
	// feeds would scroll more than the library holds (E10 item 1, run (g)):
	// the lines are written a few at a time, as a program prints them.
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 100 })
	writeLines(tm, 0, 5000, 7)
	first, base := tm.Range()
	if base-first != 100 {
		t.Fatalf("Range [%d, %d] holds %d lines, want 100", first, base, base-first)
	}
	if _, ok := tm.Line(first - 1); ok {
		t.Errorf("Line(first - 1) is ok, although the library may still hold the row")
	}
	if got := textAt(tm, first); got != numberedLine(int(first)) {
		t.Errorf("Line(first) reads %q, want %q", got, numberedLine(int(first)))
	}
}

// anchorLossCase writes 5,000 lines, the sequence, then 100 lines that start
// with R, and checks the loss rule of 5.3.2.
func anchorLossCase(t *testing.T, seq string, textAfter bool) {
	var rec reflowLog
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 10000 }, rec.options)
	writeLines(tm, 0, 5000, 100)
	first0, base0 := tm.Range()
	handedOut := base0 + 23 // the last line of the screen
	if _, ok := tm.Line(handedOut); !ok {
		t.Fatalf("Line(%d) of the screen is not ok", handedOut)
	}
	read := base0 - 10
	before := textAt(tm, read)
	tm.Write([]byte(seq))
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "R%03d\r\n", i)
	}
	tm.Write([]byte(b.String()))
	if rec.n != 1 {
		t.Fatalf("OnReflow was called %d times, want once", rec.n)
	}
	if n, ok := rec.remap(read); ok {
		t.Errorf("the remap places line %d (%q, read before) at %d", read, before, n)
	}
	first, base := tm.Range()
	if first <= handedOut || first < first0 {
		t.Errorf("Range [%d, %d] after the loss; every number up to %d was handed out", first, base, handedOut)
	}
	if _, ok := tm.Line(first); !ok {
		t.Fatalf("Line(first=%d) is not ok", first)
	}
	if got := textAt(tm, first); textAfter && got != "R000" {
		t.Errorf("Line(first) reads %q, want the first line written after the sequence", got)
	}
	if got := textAt(tm, base+22); got != "R099" {
		t.Errorf("the last line written reads %q at %d", got, base+22)
	}
}

func TestAnchorSurvivesReset(t *testing.T) { anchorLossCase(t, "\x1bc", true) }

func TestAnchorSurvivesClearHistory(t *testing.T) {
	// The library keeps the anchor through ESC [ 3 J, which leaves the live
	// screen alone (E10 item 1, run (f)); the sentinel on the newest history
	// row is what goes, and the adapter applies the loss rule.
	t.Run("alone", func(t *testing.T) { anchorLossCase(t, "\x1b[3J", false) })
	// What clear(1) sends: the screen is erased too, so the first line
	// after it is the first one written.
	t.Run("with the screen", func(t *testing.T) { anchorLossCase(t, "\x1b[H\x1b[2J\x1b[3J", true) })
}

func TestAnchorSurvivesOneLargeWrite(t *testing.T) {
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 10000 })
	w := rangeWatch{history: 10000}
	writeLines(tm, 0, 200, 100)
	w.check(t, tm, "200 lines")
	tm.Write(bytes.Repeat([]byte("y\n"), 32<<10))
	w.check(t, tm, "64 KiB of y LF")
	tm.Write(bytes.Repeat([]byte("\x1b[50S"), (8<<10)/5+1))
	w.check(t, tm, "8 KiB of CSI 50 S")
	tm.Write(bytes.Repeat([]byte("\n"), 1<<20))
	w.check(t, tm, "1 MiB of LF")
}

func TestALostAnchorEndsEveryFollowedLine(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 1000 }, rec.options)
	writeLines(tm, 0, 500, 50)
	_, base := tm.Range()
	followed := []int64{base - 100, base - 1, base, base + 5}
	for _, n := range followed {
		tm.Follow(n)
	}
	tm.Write([]byte("\x1bc"))
	if rec.n != 1 {
		t.Fatalf("OnReflow was called %d times after ESC c, want once", rec.n)
	}
	for _, n := range followed {
		if m, ok := rec.remap(n); ok {
			t.Errorf("followed line %d is placed at %d after the anchor was lost", n, m)
		}
	}
	if len(tm.historyState.follows) != 0 {
		t.Errorf("%d followed lines are still held", len(tm.historyState.follows))
	}
	writeLines(tm, 0, 50, 50)
	tm.Resize(40, 24)
	if rec.n != 1 {
		t.Errorf("a reflowing resize after the loss called OnReflow again, with no followed line")
	}
}

// wrapLines writes n lines of varying length: line i is "A<i> " repeated to
// 10 + (i*7)%60 characters.
func wrapLines(tm *term, n int) {
	var b strings.Builder
	for i := range n {
		head := fmt.Sprintf("A%04d ", i)
		l := strings.Repeat(head, 20)[:10+(i*7)%60]
		b.WriteString(l)
		b.WriteString("\r\n")
		if i%50 == 49 {
			tm.Write([]byte(b.String()))
			b.Reset()
		}
	}
	tm.Write([]byte(b.String()))
}

// lineHeads returns the absolute numbers of retained lines that start with "A"
// and the six characters each starts with.
func lineHeads(tm *term) map[int64]string {
	first, base := tm.Range()
	out := map[int64]string{}
	for n := first; n < base; n++ {
		s := textAt(tm, n)
		if strings.HasPrefix(s, "A") && len(s) >= 6 {
			out[n] = s[:6]
		}
	}
	return out
}

func TestAFollowedLineIsRemappedByAReflow(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 2000 }, rec.options)
	wrapLines(tm, 300)
	heads := lineHeads(tm)
	// Follow every tenth line that starts a line of text.
	followed := map[int64]string{}
	i := 0
	for n, h := range heads {
		if i%10 == 0 {
			followed[n] = h
			tm.Follow(n)
		}
		i++
	}
	if len(followed) < 10 {
		t.Fatalf("only %d lines followed", len(followed))
	}
	for _, cols := range []int{15, 70, 40} {
		before := rec.n
		tm.Resize(cols, 10)
		if rec.n != before+1 {
			t.Fatalf("resize to %d columns called OnReflow %d times, want once", cols, rec.n-before)
		}
		next := map[int64]string{}
		for n, h := range followed {
			m, ok := rec.remap(n)
			if !ok {
				t.Errorf("at %d columns the followed line %d (%q) is not placed", cols, n, h)
				continue
			}
			if got := textAt(tm, m); !strings.HasPrefix(got, h) {
				t.Errorf("at %d columns line %d (%q) is placed at %d, which reads %q", cols, n, h, m, got)
			}
			next[m] = h
		}
		followed = next
	}
}

func TestAnUnfollowedLineIsNotRemapped(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 2000 }, rec.options)
	wrapLines(tm, 200)
	_, base := tm.Range()
	tm.Follow(base - 20)
	tm.Resize(15, 10)
	if rec.n != 1 {
		t.Fatalf("OnReflow was called %d times, want once", rec.n)
	}
	for _, n := range []int64{base - 21, base - 19, base - 5, base} {
		if m, ok := rec.remap(n); ok {
			t.Errorf("the line %d that was not followed is placed at %d", n, m)
		}
	}
	if _, ok := rec.remap(base - 20); !ok {
		t.Errorf("the followed line is not placed")
	}
}

func TestFollowIsCounted(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 2000 }, rec.options)
	wrapLines(tm, 200)
	_, base := tm.Range()
	n := base - 30
	tm.Follow(n)
	tm.Follow(n)
	tm.Unfollow(n)
	tm.Resize(15, 10)
	m, ok := rec.remap(n)
	if rec.n != 1 || !ok {
		t.Fatalf("a line followed twice and unfollowed once is not placed (%d calls)", rec.n)
	}
	tm.Unfollow(m)
	calls := rec.n
	tm.Resize(40, 10)
	if rec.n != calls {
		if k, ok := rec.remap(m); ok {
			t.Errorf("a line followed twice and unfollowed twice is still placed at %d", k)
		}
	}
	if len(tm.historyState.follows) != 0 {
		t.Errorf("%d followed lines are held after the last Unfollow", len(tm.historyState.follows))
	}
	tm.Unfollow(m) // one too many: ignored
}

func TestALineTrimmedWhileFollowedIsNotOk(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 100 }, rec.options)
	writeLines(tm, 0, 300, 5)
	// A line far enough behind that the library frees its row: the
	// library holds at most History + historyHeadroom(40) = 1,363 rows.
	first, base := tm.Range()
	tm.Follow(first)
	tm.Follow(base - 3)
	writeLines(tm, 300, 1500, 5)
	tm.Resize(30, 10)
	if rec.n != 1 {
		t.Fatalf("OnReflow was called %d times, want once", rec.n)
	}
	for _, n := range []int64{first, base - 3} {
		if m, ok := rec.remap(n); ok {
			t.Errorf("line %d, followed and trimmed, is placed at %d", n, m)
		}
	}
	// A line the library still holds but that the history cap moved below
	// first is outside Range and not ok either (E10 item 2).
	_, base = tm.Range()
	tm.Follow(base - 3)
	writeLines(tm, 1800, 150, 5)
	if tm.scrollbackRows() < 250 {
		t.Fatalf("the library holds only %d rows; the line is not held below first", tm.scrollbackRows())
	}
	tm.Resize(35, 10)
	if rec.n != 2 {
		t.Fatalf("OnReflow was called %d times, want twice", rec.n)
	}
	if m, ok := rec.remap(base - 3); ok {
		t.Errorf("line %d, below first and still held by the library, is placed at %d", base-3, m)
	}
	if len(tm.historyState.follows) != 0 {
		t.Errorf("%d followed lines are held after they ended", len(tm.historyState.follows))
	}
}

func TestTheAnchorIsReplantedAfterEveryPiece(t *testing.T) {
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 10000 })
	pieces := 0
	tm.onPiece = func() {
		pieces++
		s := &tm.historyState
		_, base := tm.Range()
		r := tm.histRead()
		if r.alt || r.anchor != int64(r.hist) || s.anchorAbs != base {
			t.Fatalf("after piece %d the anchor stands on screen row %d with number %d; the live screen starts at row %d with number %d",
				pieces, r.anchor, s.anchorAbs, r.hist, base)
		}
		if r.hist > 0 && r.sentinel != r.anchor-1 {
			t.Fatalf("after piece %d the sentinel stands on row %d, the anchor on %d", pieces, r.sentinel, r.anchor)
		}
	}
	var b bytes.Buffer
	for i := 0; b.Len() < 64<<10; i++ {
		b.WriteString(numberedLine(i))
		b.WriteString("\r\n")
	}
	tm.Write(b.Bytes()[:64<<10])
	if pieces != 16 {
		t.Fatalf("%d pieces, want 16", pieces)
	}
}

func TestOnReflowIsNotCalledWhenNoFollowedLineMoved(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 1000 }, rec.options)
	writeLines(tm, 0, 200, 20) // lines of 7 characters: no width rewraps them
	_, base := tm.Range()
	tm.Resize(30, 10) // nothing was followed
	if rec.n != 0 {
		t.Fatalf("a resize with no followed line called OnReflow")
	}
	tm.Follow(base - 50)
	tm.Follow(base + 2)
	for _, cols := range []int{60, 20, 40} {
		tm.Resize(cols, 10)
	}
	if rec.n != 0 {
		t.Errorf("resizes that moved no followed line called OnReflow %d times", rec.n)
	}
	tm.Resize(4, 10) // now every line wraps
	if rec.n != 1 {
		t.Errorf("a resize that moved the followed lines called OnReflow %d times, want once", rec.n)
	}
}

func TestEveryLineInRangeIsReadableAfterAReflowingResize(t *testing.T) {
	tm := termAt(t, 60, 10, func(o *Options) { o.History = 2000 })
	wrapLines(tm, 3000)
	for _, cols := range []int{25, 7, 120, 60} {
		tm.Resize(cols, 10)
		first, base := tm.Range()
		if base-first > 2000 || base-first <= 0 {
			t.Errorf("at %d columns Range [%d, %d] holds %d lines", cols, first, base, base-first)
		}
		for n := first; n < base+10; n++ {
			if _, ok := tm.Line(n); !ok {
				t.Fatalf("at %d columns Line(%d) in [%d, %d] is not ok", cols, n, first, base+10)
			}
			if _, _, ok := tm.Text(n); !ok {
				t.Fatalf("at %d columns Text(%d) is not ok", cols, n)
			}
		}
	}
}

func TestAReflowNeverLowersFirst(t *testing.T) {
	var rec reflowLog
	const history = 10000
	tm := termAt(t, 200, 50, func(o *Options) { o.History = history }, rec.options)
	fillLines(tm, history+historyHeadroom(200)+50)
	first, base := tm.Range()
	if base-first != history {
		t.Fatalf("the history is not full: [%d, %d]", first, base)
	}
	followed := []int64{first, first + 1, first + 500, first + 4000, base - 300, base - 1, base}
	for _, n := range followed {
		tm.Follow(n)
	}
	last := first
	for _, cols := range []int{80, 4, 200} {
		tm.Resize(cols, 50)
		f, b := tm.Range()
		if f < last || f < 0 {
			t.Fatalf("at %d columns first is %d, was %d", cols, f, last)
		}
		if b-f > history {
			t.Fatalf("at %d columns Range [%d, %d] holds more than History", cols, f, b)
		}
		last = f
		var next []int64
		for _, n := range followed {
			if m, ok := rec.remap(n); ok {
				if m < f {
					t.Errorf("at %d columns line %d is placed at %d, below first %d", cols, n, m, f)
				}
				next = append(next, m)
			}
		}
		followed = next
	}
	if rec.n == 0 {
		t.Fatalf("OnReflow was never called")
	}
	// The oldest followed lines cannot survive 200 -> 80: the library holds
	// History rows of 80 columns, a third of the lines.
	if len(followed) >= 7 {
		t.Errorf("every followed line survived three reflowLog; the oldest ones should be below first")
	}
}

func TestRangeIsExactAtNarrowWidths(t *testing.T) {
	const history = 10000
	for _, cols := range []int{4, 40} {
		tm := termAt(t, cols, 24, func(o *Options) { o.History = history })
		w := rangeWatch{history: history}
		for range 30 {
			fillLines(tm, 1000)
			w.check(t, tm, fmt.Sprintf("%d columns", cols))
		}
		first, base := tm.Range()
		if base-first != history {
			t.Errorf("at %d columns Range [%d, %d] holds %d lines, want %d", cols, first, base, base-first, history)
		}
		if _, ok := tm.Line(first); !ok {
			t.Errorf("at %d columns Line(first) is not ok", cols)
		}
	}
}

func TestAbsoluteLinesKeepGrowingPastTheCap(t *testing.T) {
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 100 })
	for round := 1; round <= 10; round++ {
		writeLines(tm, (round-1)*500, 500, 10)
		first, base := tm.Range()
		if want := int64(round*500 - 23); base != want {
			t.Fatalf("after %d lines base is %d, want %d", round*500, base, want)
		}
		if base-first != 100 {
			t.Fatalf("after %d lines Range [%d, %d]", round*500, first, base)
		}
		if got := textAt(tm, base-1); got != numberedLine(int(base-1)) {
			t.Fatalf("line %d reads %q", base-1, got)
		}
	}
}

func TestLineOutsideTheRangeIsNotOk(t *testing.T) {
	tm := termAt(t, 80, 24, func(o *Options) { o.History = 100 })
	writeLines(tm, 0, 400, 10)
	first, base := tm.Range()
	for _, n := range []int64{-1, first - 1, first - 50, base + 24, base + 1000} {
		if _, ok := tm.Line(n); ok {
			t.Errorf("Line(%d) outside [%d, %d] is ok", n, first, base+23)
		}
		if _, _, ok := tm.Text(n); ok {
			t.Errorf("Text(%d) outside [%d, %d] is ok", n, first, base+23)
		}
	}
	for _, n := range []int64{first, base - 1, base, base + 23} {
		if _, ok := tm.Line(n); !ok {
			t.Errorf("Line(%d) inside [%d, %d] is not ok", n, first, base+23)
		}
	}
	tm.Close()
	if _, ok := tm.Line(first); ok {
		t.Errorf("Line is ok after Close")
	}
}

func TestLineIsTheWireRowOfTheLine(t *testing.T) {
	tm := termAt(t, 30, 4)
	tm.Write([]byte("\x1b[1;31mred\x1b[0m 漢\r\n"))
	writeLines(tm, 0, 10, 10)
	first, base := tm.Range()
	if first != 0 {
		t.Fatalf("first is %d", first)
	}
	if got, ok := tm.Line(0); !ok || got != "\x1b[0;1;31mred\x1b[0m 漢" {
		t.Errorf("Line(0) = %q, %v", got, ok)
	}
	for y := range 4 {
		if got, ok := tm.Line(base + int64(y)); !ok || got != tm.Row(y) {
			t.Errorf("Line(base+%d) = %q, Row(%d) = %q", y, got, y, tm.Row(y))
		}
	}
}

func TestTheAlternateScreenLeavesTheNumbersAlone(t *testing.T) {
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 1000 })
	writeLines(tm, 0, 300, 20)
	first, base := tm.Range()
	tm.Write([]byte("\x1b[?1049h"))
	for i := range 200 {
		fmt.Fprintf(writerOf(tm), "alt %d\r\n", i)
	}
	if f, b := tm.Range(); f != first || b != base {
		t.Fatalf("the alternate screen moved Range from [%d, %d] to [%d, %d]", first, base, f, b)
	}
	if got, ok := tm.Line(base + 8); !ok || got != "alt 199" {
		t.Errorf("Line(base+8) on the alternate screen = %q, %v", got, ok)
	}
	// The library reads only the active screen: the history of the
	// primary screen cannot be read while the alternate one is shown.
	if _, ok := tm.Line(base - 1); ok {
		t.Errorf("a history line is ok while the alternate screen is active")
	}
	tm.Write([]byte("\x1b[?1049l"))
	if f, b := tm.Range(); f != first || b != base {
		t.Fatalf("back on the primary screen Range is [%d, %d], was [%d, %d]", f, b, first, base)
	}
	for _, n := range []int64{first, base - 1} {
		if got := textAt(tm, n); got != numberedLine(int(n)) {
			t.Errorf("line %d reads %q after the alternate screen", n, got)
		}
	}
}

func TestAResizeOnTheAlternateScreenIsReportedWhenThePrimaryReturns(t *testing.T) {
	var rec reflowLog
	tm := termAt(t, 40, 10, func(o *Options) { o.History = 2000 }, rec.options)
	wrapLines(tm, 200)
	heads := lineHeads(tm)
	var n int64 = -1
	for k := range heads {
		n = max(n, k)
	}
	tm.Follow(n)
	tm.Write([]byte("\x1b[?1049hvim"))
	tm.Resize(15, 10)
	if rec.n != 0 {
		t.Fatalf("OnReflow was called while the alternate screen is active")
	}
	tm.Write([]byte("\x1b[?1049l"))
	if rec.n != 1 {
		t.Fatalf("OnReflow was called %d times when the primary screen returned, want once", rec.n)
	}
	m, ok := rec.remap(n)
	if !ok || !strings.HasPrefix(textAt(tm, m), heads[n]) {
		t.Errorf("line %d (%q) is placed at %d, %v, which reads %q", n, heads[n], m, ok, textAt(tm, m))
	}
	w := rangeWatch{history: 2000}
	w.check(t, tm, "back on the primary screen")
}
