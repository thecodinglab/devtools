//go:build cgo

package vt

import (
	"bytes"
	"strings"
	"testing"
)

// The operations of FuzzHistory: the input is cut at 0xFF into chunks. A
// chunk that starts with 0xFE and has two more bytes resizes the terminal;
// one that starts with 0xFD and has one more byte follows a line in Range;
// every other chunk is written.
const (
	fuzzSplit  = 0xFF
	fuzzResize = 0xFE
	fuzzFollow = 0xFD
)

func fuzzResizeOp(cols, rows int) []byte { return []byte{fuzzResize, byte(cols - 1), byte(rows - 1)} }

func FuzzHistory(f *testing.F) {
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, []byte{fuzzSplit}) }
	// The sequences that lose the anchor or come close (5.3.2).
	f.Add([]byte("\x1bc"))
	f.Add([]byte("\x1b[3J"))
	f.Add([]byte("\x1b[H\x1b[2J\x1b[3J"))
	f.Add(bytes.Repeat([]byte("\x1b[50S"), (8<<10)/5+1))
	f.Add(bytes.Repeat([]byte("\n"), 64<<10))
	f.Add(bytes.Repeat([]byte("y\n"), 32<<10))
	// Reflows with followed lines, narrowing to 4 columns and back.
	f.Add(join([]byte{fuzzFollow, 3}, []byte{fuzzFollow, 200}, fuzzResizeOp(4, 6), []byte("tail\r\n"), fuzzResizeOp(120, 6), fuzzResizeOp(20, 30)))
	// The alternate screen, a resize on it, and back.
	f.Add(join([]byte{fuzzFollow, 50}, []byte("\x1b[?1049hvim\r\n"), fuzzResizeOp(9, 4), []byte("\x1b[?1049l"), fuzzResizeOp(20, 6)))
	f.Add(join([]byte("\x1b[?1049h"), []byte("\x1bc"), []byte(strings.Repeat("z\r\n", 500))))
	f.Fuzz(func(t *testing.T, data []byte) {
		const history = 300
		var rec reflowLog
		tm := termAt(t, 20, 6, func(o *Options) { o.History = history }, rec.options)
		w := rangeWatch{history: history}
		var long strings.Builder
		for i := range 400 {
			long.WriteString(numberedLine(i))
			if i%3 == 0 {
				long.WriteString(" and a longer tail")
			}
			long.WriteString("\r\n")
		}
		tm.Write([]byte(long.String()))
		w.check(t, tm, "the first lines")
		followed := map[int64]bool{}
		for i, op := range bytes.Split(data, []byte{fuzzSplit}) {
			calls := rec.n
			switch {
			case len(op) == 3 && op[0] == fuzzResize:
				tm.Resize(1+int(op[1])%120, 1+int(op[2])%40)
				w.resized = true
			case len(op) == 2 && op[0] == fuzzFollow:
				first, base := tm.Range()
				_, rows := tm.Size()
				n := first + int64(op[1])%(base+int64(rows)-first)
				tm.Follow(n)
				followed[n] = true
			default:
				tm.Write(op)
			}
			tm.Replies()
			w.check(t, tm, "operation "+string(rune('0'+i%10)))
			if rec.n != calls {
				// Every followed line answers either not ok or a number
				// inside Range.
				first, _ := tm.Range()
				next := map[int64]bool{}
				for n := range followed {
					if m, ok := rec.remap(n); ok {
						if m < first {
							t.Fatalf("followed line %d is placed at %d, below first %d", n, m, first)
						}
						next[m] = true
					}
				}
				followed = next
			}
		}
	})
}
