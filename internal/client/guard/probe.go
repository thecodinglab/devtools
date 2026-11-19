package guard

// The in-band probe of 3.4 c: XTVERSION (CSI > q), then DA1 (CSI c).

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// queries is everything Probe writes: XTVERSION first, then DA1. A terminal
// answers in order, so the DA1 answer marks the end of what it has to say.
const queries = "\x1b[>q\x1b[c"

// maxName bounds the XTVERSION name kept from an answer. A longer answer is
// cut, which keeps its prefix and so its verdict.
const maxName = 512

// deadliner is a terminal whose reads can time out, as an *os.File of a
// terminal or pipe opened by the program is.
type deadliner interface {
	SetReadDeadline(t time.Time) error
}

// Probe asks the terminal on rw for XTVERSION and DA1 and waits up to
// timeout for the answers.
//
// It writes the two queries in one write and reads until the DA1 answer
// arrives or the timeout ends; a terminal that is silent is not an error,
// it is an Answer without anything in it, which Answer.Nested refuses.
// Every byte that is not one of the two answers (keys typed meanwhile,
// other answers) is read and discarded, also what follows the DA1 answer in
// the same read. The error is for a terminal that could not be asked: a
// failed write, a read that failed other than by the timeout, or a timeout
// that is not positive.
//
// When rw has SetReadDeadline and it works, the reads use it and the
// deadline is cleared before Probe returns. Otherwise one goroutine reads,
// and after a timeout it stays blocked in its read until the next bytes
// arrive, which it then drops; a caller that reads rw afterwards passes a
// terminal it opened itself (os.OpenFile of /dev/tty), whose deadline
// works.
func Probe(rw io.ReadWriter, timeout time.Duration) (Answer, error) {
	if rw == nil {
		return Answer{}, errors.New("guard: no terminal to probe")
	}
	if timeout <= 0 {
		return Answer{}, fmt.Errorf("guard: probe timeout %v is not positive", timeout)
	}
	if n, err := io.WriteString(rw, queries); err != nil {
		return Answer{}, fmt.Errorf("guard: probe the terminal: %w", err)
	} else if n != len(queries) {
		return Answer{}, fmt.Errorf("guard: probe the terminal: %w", io.ErrShortWrite)
	}
	deadline := time.Now().Add(timeout)
	if d, ok := rw.(deadliner); ok && d.SetReadDeadline(deadline) == nil {
		defer d.SetReadDeadline(time.Time{})
		return readWithDeadline(rw)
	}
	return readWithTimer(rw, timeout)
}

// readWithDeadline reads rw, whose read deadline is set, until the DA1
// answer or the deadline.
func readWithDeadline(r io.Reader) (Answer, error) {
	var p answerParser
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		if p.feed(buf[:n]) {
			return p.answer(), nil
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return p.answer(), nil
		}
		if err != nil {
			return p.answer(), fmt.Errorf("guard: read the terminal's answer: %w", err)
		}
	}
}

// readWithTimer reads r in a goroutine until the DA1 answer or the timeout.
func readWithTimer(r io.Reader, timeout time.Duration) (Answer, error) {
	type chunk struct {
		b   []byte
		err error
	}
	chunks := make(chan chunk)
	done := make(chan struct{})
	defer close(done)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			c := chunk{b: append([]byte(nil), buf[:n]...), err: err}
			select {
			case chunks <- c:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var p answerParser
	for {
		select {
		case c := <-chunks:
			if p.feed(c.b) {
				return p.answer(), nil
			}
			if c.err != nil {
				return p.answer(), fmt.Errorf("guard: read the terminal's answer: %w", c.err)
			}
		case <-timer.C:
			return p.answer(), nil
		}
	}
}

// state is where the parser is in the byte stream.
type state int

const (
	ground      state = iota
	escape            // after ESC
	dcsStart          // after ESC P
	dcsGT             // after ESC P >
	dcsName           // inside ESC P > | ..., collecting the name
	dcsNameEsc        // ESC inside the name: ST, or the start of something else
	dcsOther          // inside another DCS string, until ST
	dcsOtherEsc       // ESC inside another DCS string
	csiStart          // after ESC [
	csiDA1            // after ESC [ ? and parameters
	csiOther          // inside another CSI sequence, until its final byte
)

// answerParser finds the XTVERSION answer (DCS > | name ST) and the DA1 answer
// (CSI ? params c) in a stream that may hold anything else, split across
// reads at any byte. It keeps O(maxName) memory whatever it is fed.
type answerParser struct {
	st        state
	name      []byte // the name being collected
	xtversion string // the XTVERSION answer kept so far
	haveXT    bool
	da1       bool
	params    int // parameter bytes after ESC [ ?
}

func (p *answerParser) answer() Answer { return Answer{XTVersion: p.xtversion, DA1: p.da1} }

// feed parses b and reports whether the DA1 answer arrived; the bytes after
// it are not looked at.
func (p *answerParser) feed(b []byte) bool {
	for _, c := range b {
		if p.step(c) {
			p.da1 = true
			return true
		}
	}
	return false
}

// step takes one byte and reports whether it ended a DA1 answer.
func (p *answerParser) step(c byte) bool {
	const esc = 0x1b
	switch p.st {
	case ground:
		if c == esc {
			p.st = escape
		}
	case escape:
		switch c {
		case 'P':
			p.st = dcsStart
		case '[':
			p.st = csiStart
		case esc:
			p.st = escape
		default:
			p.st = ground
		}
	case dcsStart:
		switch c {
		case '>':
			p.st = dcsGT
		case esc:
			p.st = escape
		default:
			p.st = dcsOther
		}
	case dcsGT:
		switch c {
		case '|':
			p.st = dcsName
			p.name = p.name[:0]
		case esc:
			p.st = escape
		default:
			p.st = dcsOther
		}
	case dcsName:
		if c == esc {
			p.st = dcsNameEsc
		} else if len(p.name) < maxName {
			p.name = append(p.name, c)
		}
	case dcsNameEsc:
		if c == '\\' {
			p.st = ground
			p.keepName(string(p.name))
			return false
		}
		// The string ended without ST: it is no answer, and the ESC starts
		// whatever comes next.
		p.st = escape
		return p.step(c)
	case dcsOther:
		if c == esc {
			p.st = dcsOtherEsc
		}
	case dcsOtherEsc:
		if c == '\\' {
			p.st = ground
			return false
		}
		p.st = escape
		return p.step(c)
	case csiStart:
		switch {
		case c == '?':
			p.st = csiDA1
			p.params = 0
		case c == esc:
			p.st = escape
		case c >= 0x40 && c <= 0x7e:
			p.st = ground
		default:
			p.st = csiOther
		}
	case csiDA1:
		switch {
		case c >= '0' && c <= '9' || c == ';':
			p.params++
		case c == 'c' && p.params > 0:
			p.st = ground
			return true
		case c == esc:
			p.st = escape
		case c >= 0x40 && c <= 0x7e:
			p.st = ground
		default:
			p.st = csiOther
		}
	case csiOther:
		switch {
		case c == esc:
			p.st = escape
		case c >= 0x40 && c <= 0x7e:
			p.st = ground
		}
	}
	return false
}

// keepName records a complete XTVERSION answer. The first one is kept,
// unless a later one is on the refused list and the kept one is not: two
// answers can only mean that one of them was typed or pasted, and the guard
// fails closed.
func (p *answerParser) keepName(name string) {
	if !p.haveXT {
		p.xtversion, p.haveXT = name, true
		return
	}
	if _, kept := refusedName(p.xtversion); kept {
		return
	}
	if _, now := refusedName(name); now {
		p.xtversion = name
	}
}
