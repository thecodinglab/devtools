//go:build cgo

package rowfilter_test

// This file imports internal/vt in test code only, as internal/e2e does
// (P1-08): it is where the two halves of the row grammar of 4.2.5 meet, the
// engine's row encoder on the server and the client's filter. The package
// itself imports screen, x/ansi and the standard library only. It needs the
// engine, so it builds with cgo only (the cross vet of CHECK runs without).

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"devtools/internal/client/rowfilter"
	"devtools/internal/client/screen"
	"devtools/internal/vt"
)

// The clusters rows are made of. Every one has the same width in the engine
// (mode 2027) and in x/ansi, and no two of them join when written side by
// side, so the encoder writes each as it is and never as U+FFFD.
// provisional (E9): these widths are the engine's, which E9 found equal to
// x/ansi's in 35 of 35 cases; the outer terminal's half of E9 is pending.
var (
	narrowClusters = []string{
		"ä", "ß", "€", "\u00e9", "e\u0301", "ñ", "Ω", "→", "\u00a0", "\uFFFD",
	}
	wideClusters = []string{"日", "本", "語", "中", "😀", "👍🏽", "🎉"}
	// The long link stays below what the engine keeps: libghostty-vt
	// 1.0.1 drops an OSC 8 whose uri is 2047 bytes or longer, so the
	// encoder never sees one (the filter's limit of 2048 is tested in
	// TestRejectsALinkWithBytesOutsideTheURISet).
	links = []string{"https://example.com/a?b=c", "file:///tmp/x", "a:b#c", strings.Repeat("u", 1500)}
)

// genStyle returns a random style the encoder writes as it is: a colour of
// the 16 is Kind 1 for the foreground and the background (the encoder
// writes palette 0 to 15 in their short form there), the underline colour
// is Kind 2 or RGB and only with an underline (the encoder drops it
// without one).
func genStyle(r *rand.Rand) screen.Style {
	var st screen.Style
	if r.IntN(3) == 0 {
		return st
	}
	st.Attrs = screen.Attr(r.IntN(1 << 7))
	if r.IntN(3) == 0 {
		st.Underline = uint8(1 + r.IntN(5))
	}
	color := func(short bool) screen.Color {
		switch r.IntN(4) {
		case 1:
			if short {
				return screen.Color{Kind: 1, V: uint32(r.IntN(16))}
			}
			return screen.Color{Kind: 2, V: uint32(r.IntN(256))}
		case 2:
			if short {
				return screen.Color{Kind: 2, V: uint32(16 + r.IntN(240))}
			}
			return screen.Color{Kind: 2, V: uint32(r.IntN(256))}
		case 3:
			return screen.Color{Kind: 3, V: uint32(r.IntN(1 << 24))}
		}
		return screen.Color{}
	}
	st.Fg, st.Bg = color(true), color(true)
	if st.Underline != 0 {
		st.Ul = color(false)
	}
	return st
}

// program writes a style as a program would: a reset, then the attributes
// and each colour in an SGR of their own. One SGR with all of them can hold
// more than the 24 parameters the engine's parser keeps, and the engine
// then ignores it whole (libghostty-vt 1.0.1).
func program(b *strings.Builder, st screen.Style) {
	b.WriteString("\x1b[0")
	for _, a := range []struct {
		bit screen.Attr
		p   string
	}{{screen.Bold, ";1"}, {screen.Faint, ";2"}, {screen.Italic, ";3"}, {screen.Blink, ";5"}, {screen.Reverse, ";7"}, {screen.Conceal, ";8"}, {screen.Strike, ";9"}} {
		if st.Attrs&a.bit != 0 {
			b.WriteString(a.p)
		}
	}
	if st.Underline != 0 {
		fmt.Fprintf(b, ";4:%d", st.Underline)
	}
	b.WriteByte('m')
	color := func(c screen.Color, ext int) {
		switch c.Kind {
		case 1, 2:
			fmt.Fprintf(b, "\x1b[%d;5;%dm", ext, c.V)
		case 3:
			fmt.Fprintf(b, "\x1b[%d;2;%d;%d;%dm", ext, c.V>>16&0xff, c.V>>8&0xff, c.V&0xff)
		}
	}
	color(st.Fg, 38)
	color(st.Bg, 48)
	color(st.Ul, 58)
}

// genRow returns the cells of a random row at most cols wide, without the
// trailing blank cells the encoder leaves out, and the program output that
// draws it on a cleared line.
func genRow(r *rand.Rand, cols int) ([]screen.Cell, string) {
	var (
		cells []screen.Cell
		out   strings.Builder
		w     int
	)
	out.WriteString("\x1b[0m\x1b]8;;\x1b\\\x1b[H\x1b[2K")
	target := r.IntN(cols + 1)
	st, link := screen.Style{}, ""
	for w < target {
		if r.IntN(4) == 0 {
			st = genStyle(r)
		}
		if r.IntN(6) == 0 {
			link = ""
			if r.IntN(2) == 0 {
				link = links[r.IntN(len(links))]
			}
		}
		var s string
		cw := 1
		switch k := r.IntN(10); {
		case k < 6:
			s = string(rune(0x20 + r.IntN(0x5f)))
		case k < 8:
			s = narrowClusters[r.IntN(len(narrowClusters))]
		default:
			s, cw = wideClusters[r.IntN(len(wideClusters))], 2
		}
		if w+cw > cols {
			break
		}
		cell := st
		cell.Link = link
		program(&out, st)
		out.WriteString("\x1b]8;;" + link + "\x1b\\")
		out.WriteString(s)
		cells = append(cells, screen.Cell{S: s, W: uint8(cw), Style: cell})
		if cw == 2 {
			cells = append(cells, screen.Cell{S: "", W: 0, Style: cell})
		}
		w += cw
	}
	for len(cells) > 0 && cells[len(cells)-1] == (screen.Cell{S: " ", W: 1}) {
		cells = cells[:len(cells)-1]
	}
	return cells, out.String()
}

func TestParseInvertsEncodeRow(t *testing.T) {
	const cols, rows = 80, 2
	term := vt.New(vt.Options{Cols: cols, Rows: rows, History: 100})
	defer term.Close()
	r := rand.New(rand.NewPCG(8, 1))
	failures := 0
	for n := range 10000 {
		want, out := genRow(r, cols)
		term.Write([]byte(out))
		term.Replies()
		row := term.Row(0)
		got, err := rowfilter.Parse(row, cols)
		if err != nil {
			t.Errorf("row %d: %v; the encoder wrote %s", n, err, short(row))
		} else if d := diff(got, want); d != "" {
			t.Errorf("row %d: %s; program output %s, encoded %s", n, d, short(out), short(row))
		} else {
			continue
		}
		if failures++; failures == 5 {
			t.Fatal("stopping after 5 rows")
		}
	}
}

func diff(got, want []screen.Cell) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("cell %d is %#v, want %#v", i, got[i], want[i])
		}
	}
	if len(got) != len(want) {
		return fmt.Sprintf("%d cells, want %d", len(got), len(want))
	}
	return ""
}

// short quotes s for a message, the long link cut down.
func short(s string) string {
	return fmt.Sprintf("%q", strings.ReplaceAll(s, strings.Repeat("u", 1500), "u{1500}"))
}
