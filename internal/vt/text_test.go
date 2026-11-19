//go:build cgo

package vt

import (
	"strings"
	"testing"
)

func TestTextHasTheWrapFlagAndOneCharacterPerCell(t *testing.T) {
	tm := termAt(t, 10, 4)
	tm.Write([]byte("0123456789ab  \r\n漢字 x   \r\nend"))
	_, base := tm.Range()
	cases := []struct {
		text    string
		wrapped bool
	}{{"0123456789", true}, {"ab", false}, {"漢字 x", false}, {"end", false}}
	for i, c := range cases {
		got, wrapped, ok := tm.Text(base + int64(i))
		if !ok || got != c.text || wrapped != c.wrapped {
			t.Errorf("Text(base+%d) = %q, %v, %v; want %q, %v", i, got, wrapped, ok, c.text, c.wrapped)
		}
	}
	// A wrapped line keeps its trailing blanks: they belong to the joined
	// line.
	tm.Write([]byte("\r\n01234567  next"))
	_, base = tm.Range()
	if got, wrapped, _ := tm.Text(base + 2); got != "01234567  " || !wrapped {
		t.Errorf("a wrapped line ending in blanks reads %q, %v", got, wrapped)
	}
}

func TestExtractJoinsSoftWrappedLines(t *testing.T) {
	tm := termAt(t, 10, 5)
	tm.Write([]byte("0123456789abcdef\r\nxy   \r\n  indented"))
	_, base := tm.Range()
	got, cut := Extract(tm, Pos{base, 0}, Pos{base + 3, 9}, false, 1<<20)
	if want := "0123456789abcdef\nxy\n  indented"; got != want || cut {
		t.Errorf("Extract = %q, %v; want %q", got, cut, want)
	}
	// From the middle of the wrapped line to the middle of the next one,
	// given in reverse order.
	got, _ = Extract(tm, Pos{base + 2, 1}, Pos{base, 8}, false, 0)
	if want := "89abcdef\nxy"; got != want {
		t.Errorf("Extract from column 8 = %q, want %q", got, want)
	}
	// A line ending at a soft wrap with blanks keeps them inside the joined
	// line.
	tm2 := termAt(t, 10, 5)
	tm2.Write([]byte("hello     world"))
	_, b2 := tm2.Range()
	if got, _ := Extract(tm2, Pos{b2, 0}, Pos{b2 + 1, 9}, false, 0); got != "hello     world" {
		t.Errorf("Extract across a wrap at blanks = %q", got)
	}
}

func TestExtractBlockTakesTheSameColumns(t *testing.T) {
	tm := termAt(t, 10, 5)
	tm.Write([]byte("abcdef\r\nghijklmnopqrs\r\nm\r\n漢字漢字"))
	_, base := tm.Range()
	got, cut := Extract(tm, Pos{base, 4}, Pos{base + 4, 1}, true, 1<<20)
	// Columns 1 to 4 of every row: the wrapped row is not joined, a short
	// row gives nothing, a wide character is taken when one of its cells is
	// inside.
	if want := "bcde\nhijk\nrs\n\n漢字漢"; got != want || cut {
		t.Errorf("block Extract = %q, %v; want %q", got, cut, want)
	}
}

func TestExtractStopsAtOneMiBAndSaysCut(t *testing.T) {
	tm := termAt(t, 200, 50, func(o *Options) { o.History = 10000 })
	fillLines(tm, 6000)
	first, base := tm.Range()
	got, cut := Extract(tm, Pos{first, 0}, Pos{base + 49, 199}, false, 1<<20)
	if !cut || len(got) != 1<<20 {
		t.Fatalf("Extract of %d lines of 200 columns gave %d bytes, cut %v; want 1 MiB and cut", base+50-first, len(got), cut)
	}
	if !strings.HasPrefix(got, textAt(tm, first)+"\n") {
		t.Errorf("the text does not start with the first line")
	}
	small, cut := Extract(tm, Pos{first, 0}, Pos{first + 2, 199}, false, 1<<20)
	if cut || len(small) != 3*200+2 {
		t.Errorf("Extract of three lines gave %d bytes, cut %v", len(small), cut)
	}
	// A cut never splits a character.
	tm2 := termAt(t, 10, 3)
	tm2.Write([]byte("漢字漢字漢"))
	_, b2 := tm2.Range()
	if got, cut := Extract(tm2, Pos{b2, 0}, Pos{b2, 9}, false, 7); got != "漢字" || !cut {
		t.Errorf("Extract cut at 7 bytes = %q, %v", got, cut)
	}
}
