package rowfilter_test

import (
	"testing"

	"devtools/internal/client/rowfilter"
)

// FuzzParse: Parse never panics; a row it accepts has no cell whose text
// holds a scalar below U+0020, U+007F or U+0080 to U+009F, its widths sum
// to at most cols, and its cells are well formed (checkCells).
func FuzzParse(f *testing.F) {
	for _, name := range []string{"valid.txt", "invalid.txt"} {
		for _, row := range readRows(f, name) {
			f.Add(row, uint16(80))
			f.Add(row, uint16(3))
		}
	}
	for _, row := range []string{
		"\x1b[0;1;38;2;1;2;3;48;5;200;58;5;1;4:3m日本\x1b]8;;a:b\x1b\\x\x1b]8;;\x1b\\",
		"abcdefghijk",
		"e\u0301\u200b\u200d👍🏽🇩🇪",
		"\x1b[0;38;5;255mx\x1b[0;38;5;256m",
	} {
		f.Add(row, uint16(10))
	}
	f.Fuzz(func(t *testing.T, s string, c uint16) {
		cols := int(c % 1001)
		cells, err := rowfilter.Parse(s, cols)
		if err != nil {
			if cells != nil {
				t.Fatalf("Parse(%q, %d) returned cells with its error", s, cols)
			}
			return
		}
		checkCells(t, s, cols, cells)
	})
}
