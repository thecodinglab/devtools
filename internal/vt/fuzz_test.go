//go:build cgo

package vt

import (
	"math/rand/v2"
	"testing"
)

// checkGrid checks what C15 promises after any input: the size is the one
// the terminal was given, every row is inside the grammar of 4.2.5 and no
// wider than the screen, and every cell the encoder kept parses back to the
// library's cell at the same column.
func checkGrid(t *testing.T, tm *term, cols, rows int) {
	t.Helper()
	if c, r := tm.Size(); c != cols || r != rows {
		t.Fatalf("Size %dx%d, want %dx%d", c, r, cols, rows)
	}
	if c, r := tm.libSize(); c != cols || r != rows {
		t.Fatalf("the library is %dx%d, want %dx%d", c, r, cols, rows)
	}
	for y := range rows {
		cells, ok := tm.liveCells(y)
		if !ok {
			t.Fatalf("row %d cannot be read", y)
		}
		type libCell struct {
			text  string
			width int
		}
		atCol := map[int]libCell{}
		col := 0
		for _, c := range cells {
			if c.wide == wideSpacerTail {
				continue
			}
			w := 1
			if c.wide == wideWide {
				w = 2
			}
			text := string(c.text)
			if text == "" || c.wide == wideSpacerHead {
				text = " "
			}
			atCol[col] = libCell{text, w}
			col += w
		}
		row := tm.Row(y)
		if err := checkRow(row); err != nil {
			t.Fatalf("row %d %q: %v", y, row, err)
		}
		if w := rowWidth(row); w > cols {
			t.Fatalf("row %d %q measures %d cells, the screen has %d", y, row, w, cols)
		}
		col = 0
		for _, p := range parseRow(row) {
			lib, ok := atCol[col]
			switch {
			case p.text == "�" || p.text == " ":
				// a replaced cell, its pad, or a blank
			case !ok:
				t.Fatalf("row %d %q: cell %q at column %d is not on a cell of the library", y, row, p.text, col)
			case lib.text != p.text || lib.width != p.width:
				t.Fatalf("row %d %q: column %d parses as %q width %d, the library holds %q width %d", y, row, col, p.text, p.width, lib.text, lib.width)
			}
			col += p.width
		}
	}
}

func FuzzWrite(f *testing.F) {
	seeds := []string{
		"hello\r\nworld",
		"\x1b[1;31mred\x1b[0m \x1b[38;2;1;2;3mrgb\x1b[4:3;58;5;9mcurly",
		"漢字\U0001F468‍\U0001F469‍\U0001F467\U0001F1E9\U0001F1EA é",
		"\x1b[1;3H\U0001F3FD\x1b[1;1H\U0001F44D",
		"\x1b[?1049h\x1b[2Jalt\x1b[?1049l",
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
		"\x1b[?2026h\x1b[?2048h\x1b[c\x1b[>q\x1b]10;?\x07",
		"\x1b]52;c;aGVsbG8=\x07\x1b]7;file:///tmp\x07\x1b]0;t\x07\x07",
		"\x1b]7501;state=blocked:kind=question\x1b\\",
		"\x1b[?40h\x1b[?3h\x1b[8;5;5t",
		"\x1bc\x1b[3J\x1b[50S",
		"\x1bP+q544e\x1b\\\x1b_Ga=q\x1b\\",
		"\xff\xfe\xc3\x28\xe2\x82",
		"\U00011A870", // a Prepend character swallows what follows it (found by FuzzWrite)
	}
	for i, s := range seeds {
		f.Add(uint8(i*7), uint8(i*3), []byte(s))
	}
	f.Fuzz(func(t *testing.T, cb, rb uint8, data []byte) {
		cols, rows := 2+int(cb)%120, 2+int(rb)%40
		tm := termAt(t, cols, rows, func(o *Options) {
			o.History = 200
			o.Colors = Colors{Fg: "#c0caf5", Bg: "#1a1b26", Dark: true}
			o.OnTitle = func(string) {}
			o.OnCwd = func(string) {}
			o.OnClipboard = func([]byte) {}
			o.OnBell = func() {}
			o.OnAgent = func(AgentReport) {}
		})
		// Pieces of varying size, so sequences are cut everywhere.
		for p := data; len(p) > 0; {
			n := min(len(p), 1+int(p[0])%97)
			tm.Write(p[:n])
			_ = tm.Replies()
			p = p[n:]
		}
		checkGrid(t, tm, cols, rows)
		// A resize afterwards keeps the grid consistent too.
		nc, nr := 2+int(rb)%80, 2+int(cb)%30
		tm.Resize(nc, nr)
		_ = tm.Replies()
		checkGrid(t, tm, nc, nr)
		_ = tm.Cursor()
		_ = tm.Modes()
		_ = tm.SyncActive()
	})
}

// biased are the bytes that start and end escape sequences.
const biased = "\x1b[;?0123456789m]\x07\\P_H\r\n"

func TestGarbageNeverCrashes(t *testing.T) {
	const total = 48 << 20
	rng := rand.New(rand.NewPCG(48, 0x67617262616765))
	tm := termAt(t, 100, 30, func(o *Options) {
		o.History = 1000
		o.Colors = Colors{Fg: "#ffffff", Bg: "#000000"}
		o.OnTitle = func(string) {}
		o.OnCwd = func(string) {}
		o.OnClipboard = func([]byte) {}
		o.OnBell = func() {}
		o.OnAgent = func(AgentReport) {}
	})
	piece := make([]byte, 4<<10)
	for written := 0; written < total; written += len(piece) {
		for i := 0; i < len(piece); i += 8 {
			v := rng.Uint64()
			for j := range 8 {
				piece[i+j] = byte(v >> (8 * j))
			}
		}
		// Bias some pieces towards escape sequences, which random bytes
		// rarely form.
		if written%(64<<10) == 0 {
			for i := 0; i < len(piece); i += 1 + rng.IntN(16) {
				piece[i] = biased[rng.IntN(len(biased))]
			}
		}
		tm.Write(piece)
		_ = tm.Replies()
	}
	checkGrid(t, tm, 100, 30)
}
