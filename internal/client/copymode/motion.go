package copymode

import (
	"unicode"
	"unicode/utf8"

	"devtools/internal/client/screen"
)

// cell is the cell at p, and whether its line is shown. A line that is not
// shown reads as blank.
func (m *Model) cell(line int64, col int) (screen.Cell, bool) {
	row := line - m.src.Top()
	if row < 0 || row >= int64(m.rows) || col < 0 || col >= m.cols {
		return screen.Cell{S: " ", W: 1}, false
	}
	return m.src.Cell(int(row), col), true
}

func (m *Model) shown(line int64) bool {
	row := line - m.src.Top()
	return row >= 0 && row < int64(m.rows)
}

// width is the number of cells the character at p covers: 2 for the first
// cell of a wide character, 1 otherwise.
func (m *Model) width(p Pos) int {
	c, _ := m.cell(p.Line, p.Col)
	if c.W == 2 {
		return 2
	}
	return 1
}

// snap clamps col to the line and moves it from the second cell of a wide
// character to its first, so a wide character is one step.
func (m *Model) snap(line int64, col int) int {
	col = clampInt(col, 0, m.cols-1)
	for col > 0 {
		c, _ := m.cell(line, col)
		if c.W != 0 {
			break
		}
		col--
	}
	return col
}

// nextStart is the column of the character after the one at p, or p's
// column at the end of the line.
func (m *Model) nextStart(p Pos) int {
	n := p.Col + m.width(p)
	if n >= m.cols {
		return p.Col
	}
	return n
}

// prevStart is the column of the character before the one at p.
func (m *Model) prevStart(p Pos) int {
	if p.Col == 0 {
		return 0
	}
	return m.snap(p.Line, p.Col-1)
}

// lineEnd is the column of the last character of a line that is not blank,
// 0 for a blank line, and the last column for a line that is not shown.
func (m *Model) lineEnd(line int64) int {
	if !m.shown(line) {
		return m.snap(line, m.cols-1)
	}
	for col := m.cols - 1; col > 0; col-- {
		c, _ := m.cell(line, col)
		if c.W != 0 && !blank(c) {
			return col
		}
	}
	return 0
}

// Character classes of the word motions: a run of one class is a word.
const (
	classBlank = iota
	classWord
	classPunct
)

func blank(c screen.Cell) bool { return c.S == "" || c.S == " " }

func classOf(c screen.Cell) int {
	if blank(c) {
		return classBlank
	}
	r, _ := utf8.DecodeRuneInString(c.S)
	switch {
	case unicode.IsSpace(r):
		return classBlank
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return classWord
	case r >= utf8.RuneSelf && !unicode.IsPunct(r) && !unicode.IsSymbol(r):
		return classWord
	}
	return classPunct
}

// A slot of the word motions is a character, or the end of a line, which
// counts as a blank so that a word never runs on into the next line.
type slot struct {
	p   Pos
	eol bool
}

func (m *Model) class(s slot) int {
	if s.eol {
		return classBlank
	}
	c, _ := m.cell(s.p.Line, s.p.Col)
	return classOf(c)
}

// step moves one slot forward. ok is false at the end of the newest line.
// stop is true when the next line is not shown: its text is not known, so
// a motion ends at its start.
func (m *Model) step(s slot) (next slot, ok, stop bool) {
	if s.eol {
		if s.p.Line >= m.last() {
			return s, false, false
		}
		n := slot{p: Pos{s.p.Line + 1, 0}}
		return n, true, !m.shown(n.p.Line)
	}
	n := s.p.Col + m.width(s.p)
	if n >= m.cols {
		return slot{p: s.p, eol: true}, true, false
	}
	return slot{p: Pos{s.p.Line, n}}, true, false
}

// back moves one slot backward, with ok and stop as for step.
func (m *Model) back(s slot) (prev slot, ok, stop bool) {
	if s.eol {
		return slot{p: s.p}, true, false
	}
	if s.p.Col > 0 {
		return slot{p: Pos{s.p.Line, m.snap(s.p.Line, s.p.Col-1)}}, true, false
	}
	if s.p.Line <= m.first {
		return s, false, false
	}
	line := s.p.Line - 1
	if !m.shown(line) {
		return slot{p: Pos{line, 0}}, true, true
	}
	return slot{p: Pos{line, m.snap(line, m.cols-1)}, eol: true}, true, false
}

// wordForward is the start of the next word (w), or with end the last
// character of this or the next word (e).
func (m *Model) wordForward(end bool) Pos {
	s := slot{p: m.cursor}
	lastChar := s.p
	advance := func() bool {
		n, ok, stop := m.step(s)
		if !ok {
			return false
		}
		s = n
		if !s.eol {
			lastChar = s.p
		}
		return !stop
	}
	if end {
		if !advance() {
			return lastChar
		}
	} else {
		c := m.class(s)
		for c != classBlank && m.class(s) == c {
			if !advance() {
				return lastChar
			}
		}
	}
	for m.class(s) == classBlank {
		if !advance() {
			return lastChar
		}
	}
	if !end {
		return s.p
	}
	c := m.class(s)
	for {
		n, ok, stop := m.step(s)
		if !ok || stop || m.class(n) != c {
			return s.p
		}
		s = n
	}
}

// wordBack is the start of this word, or of the previous one when the
// cursor is at the start already (b).
func (m *Model) wordBack() Pos {
	s := slot{p: m.cursor}
	retreat := func() bool {
		n, ok, stop := m.back(s)
		if !ok {
			return false
		}
		s = n
		return !stop
	}
	if !retreat() {
		return s.p
	}
	for m.class(s) == classBlank {
		if !retreat() {
			return s.p
		}
	}
	c := m.class(s)
	for {
		n, ok, stop := m.back(s)
		if !ok || stop || m.class(n) != c {
			return s.p
		}
		s = n
	}
}
