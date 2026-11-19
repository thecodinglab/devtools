package guard

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestProbeRefusesDevtoolsAndNamesTheHost(t *testing.T) {
	a := probe(t, answerDevtools)
	if a.XTVersion != "devtools 0.0.0-e6 florian-nixos" || !a.DA1 {
		t.Fatalf("Probe = %+v", a)
	}
	for _, trust := range []bool{false, true} {
		n := a.Nested(trust)
		if n == nil || n.Where != "devtools" || n.Host != "florian-nixos" {
			t.Errorf("Nested(%v) = %+v, want devtools on florian-nixos", trust, n)
		}
	}
	// A name that only starts with devtools, or carries no host, or a host
	// with bytes outside [A-Za-z0-9._-], is refused all the same; the host
	// shown is only ever one the engine could have answered.
	for name, host := range map[string]string{
		"devtools":                                 "",
		"devtools 1.0":                             "",
		"DevTools 1.0 m1":                          "m1",
		"devtoolsx 1.0 m1":                         "m1",
		"  devtools 1.0 mac-mini.local":            "mac-mini.local",
		"devtools 1.0 bad\x1b[2Jhost":              "",
		"devtools 1.0 hé":                          "",
		"devtools 1.0 " + strings.Repeat("a", 300): "",
	} {
		n := Answer{XTVersion: name, DA1: true}.Nested(true)
		if n == nil || n.Where != "devtools" || n.Host != host {
			t.Errorf("Nested of %q = %+v, want devtools with host %q", name, n, host)
		}
	}
}

func TestProbeRefusesATmuxPane(t *testing.T) {
	a := probe(t, answerTmux)
	if a.XTVersion != "tmux 3.8" || !a.DA1 {
		t.Fatalf("Probe = %+v", a)
	}
	for _, trust := range []bool{false, true} {
		if n := a.Nested(trust); n == nil || n.Where != "tmux" || n.Host != "" {
			t.Errorf("Nested(%v) = %+v, want tmux", trust, n)
		}
	}
	if n := (Answer{XTVersion: "TMUX next-3.9"}).Nested(true); n == nil || n.Where != "tmux" {
		t.Errorf("Nested of another tmux version = %+v", n)
	}
}

func TestProbeRefusesAnUnnamedLibghosttyHost(t *testing.T) {
	// The answer of libghostty-vt 1.0.1 without the XTVERSION callback (E6);
	// that a herdr pane answers so is UNVERIFIED, provisional E6.
	a := probe(t, answerLibghostty)
	if a.XTVersion != "libghostty" || !a.DA1 {
		t.Fatalf("Probe = %+v", a)
	}
	for _, trust := range []bool{false, true} {
		if n := a.Nested(trust); n == nil || n.Where != "unknown terminal" {
			t.Errorf("Nested(%v) = %+v, want a refusal", trust, n)
		}
	}
}

// TestProbeRefusesTheNamesE6Recorded: E6 finished its automatable rows and
// added one name to devtools, tmux and libghostty: neovim's :terminal
// answers libvterm(0.3) (docs/replace-herdr/experiments/e6-probe.md).
func TestProbeRefusesTheNamesE6Recorded(t *testing.T) {
	a := probe(t, answerNvim)
	if a.XTVersion != "libvterm(0.3)" || !a.DA1 {
		t.Fatalf("Probe = %+v", a)
	}
	for _, trust := range []bool{false, true} {
		if n := a.Nested(trust); n == nil || n.Where != "unknown terminal" {
			t.Errorf("Nested(%v) = %+v, want a refusal", trust, n)
		}
	}
	var prefixes []string
	for _, r := range refused {
		prefixes = append(prefixes, r.prefix)
	}
	if got := strings.Join(prefixes, " "); got != "devtools tmux libghostty libvterm" {
		t.Errorf("the refused names are %q, want devtools, tmux, libghostty and E6's libvterm", got)
	}
}

func TestProbePassesAnOrdinaryTerminal(t *testing.T) {
	for _, reply := range []string{
		answerTerminal,
		"\x1bP>|WezTerm 20240203\x1b\\\x1b[?65;1;9c",
		"\x1bP>|XTerm(390)\x1b\\\x1b[?64;1;2;6;9;15;16;17;18;21;22;28c",
		"\x1bP>|kitty(0.39.1)\x1b\\\x1b[?62;c",
		// A name that only contains a refused one is not refused.
		"\x1bP>|my-tmux-like 1\x1b\\\x1b[?62c",
	} {
		a := probe(t, reply)
		if a.XTVersion == "" || !a.DA1 {
			t.Errorf("Probe of %q = %+v", reply, a)
			continue
		}
		if n := a.Nested(false); n != nil {
			t.Errorf("Nested of %q = %+v, want nil", a.XTVersion, *n)
		}
	}
	// XTVERSION alone, the DA1 answer still on its way when the time ran out,
	// is an ordinary terminal too.
	a, err := Probe(newFakeTerm(t, nil, []string{"\x1bP>|ghostty 1.3.1\x1b\\"}), 100*time.Millisecond)
	if err != nil || a.XTVersion != "ghostty 1.3.1" || a.DA1 {
		t.Fatalf("Probe without DA1 = %+v, %v", a, err)
	}
	if n := a.Nested(false); n != nil {
		t.Errorf("Nested without DA1 = %+v", *n)
	}
}

func TestProbeRefusesATerminalWithoutXTVERSIONButWithDA1(t *testing.T) {
	for _, reply := range []string{
		answerScreen,
		"\x1b[?62;22c",
		// An empty or blank name is no name.
		"\x1bP>|\x1b\\\x1b[?62;22c",
		"\x1bP>|  \x1b\\\x1b[?62;22c",
		// A name that never ends with ST is no answer.
		"\x1bP>|ghostty 1.3.1\x1b[?62;22c",
	} {
		a := probe(t, reply)
		if !a.DA1 || strings.TrimSpace(a.XTVersion) != "" {
			t.Errorf("Probe of %q = %+v, want DA1 and no name", reply, a)
			continue
		}
		if n := a.Nested(false); n == nil || n.Where != "unknown terminal" {
			t.Errorf("Nested of %q = %+v, want a refusal", reply, n)
		}
	}
}

func TestTrustTerminalAcceptsADA1OnlyTerminal(t *testing.T) {
	a := probe(t, answerScreen)
	if n := a.Nested(true); n != nil {
		t.Errorf("Nested(true) of a DA1-only terminal = %+v, want nil", *n)
	}
	// Trust never overrides a refused name.
	for _, name := range []string{"devtools 1 h", "tmux 3.8", "libghostty", "libvterm(0.3)"} {
		for _, da1 := range []bool{false, true} {
			if n := (Answer{XTVersion: name, DA1: da1}).Nested(true); n == nil {
				t.Errorf("Nested(true) of %q admitted it", name)
			}
		}
	}
	// Trust never admits a terminal that did not answer at all.
	if (Answer{}).Nested(true) == nil {
		t.Error("Nested(true) of no answer admitted it")
	}
	// Trust never overrides the environment check.
	_, n, err := Check(newFakeTerm(t, nil, []string{answerScreen}), []string{"ZELLIJ=0"}, nil, time.Second, true)
	if err != nil || n == nil || n.Where != "zellij" {
		t.Errorf("Check with ZELLIJ and trust = %+v, %v; want zellij", n, err)
	}
}

func TestProbeFailsClosedWithoutAnyAnswer(t *testing.T) {
	check := func(t *testing.T, a Answer, err error, elapsed, timeout time.Duration) {
		t.Helper()
		if err != nil {
			t.Fatalf("Probe of a silent terminal: %v", err)
		}
		if a != (Answer{}) {
			t.Errorf("Probe of a silent terminal = %+v", a)
		}
		for _, trust := range []bool{false, true} {
			if n := a.Nested(trust); n == nil || n.Where != "unknown terminal" {
				t.Errorf("Nested(%v) of no answer = %+v, want a refusal", trust, n)
			}
		}
		if elapsed < timeout || elapsed > timeout+time.Second {
			t.Errorf("Probe returned after %v, want the timeout %v", elapsed, timeout)
		}
	}
	t.Run("scripted", func(t *testing.T) {
		start := time.Now()
		a, err := Probe(newFakeTerm(t, []string{"typed"}, nil), 150*time.Millisecond)
		check(t, a, err, time.Since(start), 150*time.Millisecond)
	})
	t.Run("with a read deadline", func(t *testing.T) {
		p := newPipeTerm(t)
		p.answers.WriteString("typed\x1b[A")
		start := time.Now()
		a, err := Probe(p, 150*time.Millisecond)
		check(t, a, err, time.Since(start), 150*time.Millisecond)
		// The deadline is gone afterwards: the next read waits for its bytes.
		go func() {
			time.Sleep(50 * time.Millisecond)
			p.answers.WriteString("k")
		}()
		buf := make([]byte, 1)
		if n, err := p.Read(buf); err != nil || n != 1 || buf[0] != 'k' {
			t.Errorf("read after Probe = %d, %q, %v", n, buf[:n], err)
		}
	})
	t.Run("a timeout that is not positive", func(t *testing.T) {
		for _, d := range []time.Duration{0, -time.Second} {
			f := newFakeTerm(t, nil, []string{answerTerminal})
			if _, err := Probe(f, d); err == nil {
				t.Errorf("Probe with timeout %v: no error", d)
			}
			if w, _ := f.Written(); w != "" {
				t.Errorf("Probe with timeout %v wrote %q", d, w)
			}
		}
		if _, err := Probe(nil, time.Second); err == nil {
			t.Error("Probe(nil): no error")
		}
	})
	t.Run("a terminal that cannot be written", func(t *testing.T) {
		if _, err := Probe(failingTerm{}, time.Second); err == nil {
			t.Error("Probe of a failing terminal: no error")
		}
		if _, err := Probe(shortWriter{}, time.Second); !errors.Is(err, io.ErrShortWrite) {
			t.Errorf("Probe of a short writer: %v", err)
		}
	})
}

// shortWriter accepts one byte of every write and returns no error.
type shortWriter struct{}

func (shortWriter) Read([]byte) (int, error)    { select {} }
func (shortWriter) Write(b []byte) (int, error) { return 1, nil }

func TestProbeReadsAnswersSplitAcrossReads(t *testing.T) {
	for _, reply := range []string{answerDevtools, answerTmux, answerScreen, answerNvim, answerTerminal} {
		want := probe(t, reply)
		// Every split into two reads.
		for i := 1; i < len(reply); i++ {
			if got := probe(t, reply[:i], reply[i:]); got != want {
				t.Errorf("Probe of %q split at %d = %+v, want %+v", reply, i, got, want)
			}
		}
		// One byte per read.
		var bytewise []string
		for i := range len(reply) {
			bytewise = append(bytewise, reply[i:i+1])
		}
		if got := probe(t, bytewise...); got != want {
			t.Errorf("Probe of %q byte by byte = %+v, want %+v", reply, got, want)
		}
	}
	// The same through a pipe with a read deadline, the answer written in
	// pieces with pauses between them.
	p := newPipeTerm(t)
	go func() {
		buf := make([]byte, len(queries))
		p.queries.Read(buf)
		for _, piece := range []string{"\x1bP", ">|tmux", " 3.8\x1b", "\\\x1b[?1;", "2;4c"} {
			p.answers.WriteString(piece)
			time.Sleep(10 * time.Millisecond)
		}
	}()
	a, err := Probe(p, 2*time.Second)
	if err != nil || a.XTVersion != "tmux 3.8" || !a.DA1 {
		t.Errorf("Probe through a pipe = %+v, %v", a, err)
	}
}

func TestProbeDiscardsTypedBytes(t *testing.T) {
	typed := []string{
		"ls -l\r",
		"\x1b[A\x1b[1;5C\x1bOP",        // arrows, a function key
		"\x1b[>1;0;0c\x1b[0n\x1b[2;3R", // DA2, DSR, a cursor report: no DA1
		"\x1bPq#0;2;0;0;0\x1b\\",       // another DCS string
		"\x1b]11;rgb:0000/0000/0000\x1b\\",
		"\x03\x1b",
	}
	f := newFakeTerm(t, typed, []string{answerTmux, "after the answer"})
	a, err := Probe(f, 2*time.Second)
	if err != nil || a.XTVersion != "tmux 3.8" || !a.DA1 {
		t.Fatalf("Probe with typed bytes before the answer = %+v, %v", a, err)
	}
	if n := a.Nested(false); n == nil || n.Where != "tmux" {
		t.Errorf("Nested = %+v", n)
	}
	// Typed bytes inside the answer's gap, and a typed ESC [ ? that never
	// becomes a DA1 answer.
	a = probe(t, "x\x1b[?1", "\x1bP>|ghostty 1.3.1\x1b\\", "q\x1b[?62;22c", "rest")
	if a.XTVersion != "ghostty 1.3.1" || !a.DA1 || a.Nested(false) != nil {
		t.Errorf("Probe with typed bytes between the answers = %+v", a)
	}
	// A second, pasted XTVERSION answer can only make the verdict a refusal.
	for _, reply := range []string{
		"\x1bP>|ghostty 1.3.1\x1b\\\x1bP>|tmux 3.8\x1b\\\x1b[?62c",
		"\x1bP>|tmux 3.8\x1b\\\x1bP>|ghostty 1.3.1\x1b\\\x1b[?62c",
	} {
		if a := probe(t, reply); a.Nested(true) == nil {
			t.Errorf("Probe of two names %q = %+v passed", reply, a)
		}
	}
	// A very long name is cut and keeps its verdict.
	a = probe(t, "\x1bP>|tmux "+strings.Repeat("9", 10000)+"\x1b\\\x1b[?1;2;4c")
	if len(a.XTVersion) != maxName || a.Nested(true) == nil {
		t.Errorf("Probe of a long name kept %d bytes, Nested %+v", len(a.XTVersion), a.Nested(true))
	}
}

func TestProbeWritesExactlyTheTwoQueries(t *testing.T) {
	f := newFakeTerm(t, nil, []string{answerTerminal})
	if _, err := Probe(f, time.Second); err != nil {
		t.Fatal(err)
	}
	written, writes := f.Written()
	if written != "\x1b[>q\x1b[c" || writes != 1 {
		t.Errorf("Probe wrote %q in %d writes, want CSI > q then CSI c in one", written, writes)
	}
	// The same on a silent terminal: nothing is written again or added.
	f = newFakeTerm(t, nil, nil)
	if _, err := Probe(f, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if written, writes := f.Written(); written != queries || writes != 1 {
		t.Errorf("Probe of a silent terminal wrote %q in %d writes", written, writes)
	}
	// And through a pipe.
	p := newPipeTerm(t)
	p.answerAfterQueries(t, answerTerminal)
	if _, err := Probe(p, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	p.w.Close()
	var all bytes.Buffer
	buf := make([]byte, 64)
	for {
		n, err := p.queries.Read(buf)
		all.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if all.Len() != 0 {
		t.Errorf("Probe wrote %q after the queries", all.String())
	}
}

// FuzzProbeAnswer feeds arbitrary bytes as the terminal's reply. Probe never
// panics, does not depend on how the bytes are split into reads, keeps a
// bounded name that it really read, and its verdict obeys the rules of 3.4
// c: trust only ever admits more for a DA1 answer without a name, and a
// tmux answer after typed bytes without ESC is always found and refused.
func FuzzProbeAnswer(f *testing.F) {
	for _, s := range []string{
		answerDevtools, answerLibghostty, answerTmux, answerScreen, answerNvim, answerTerminal,
		"", "\x1b", "\x1bP>|", "\x1bP>|x\x1b", "\x1b[?", "\x1b[?c", "\x1b[?1;2c",
		"typed\x1b[A\x1bP>|tmux 3.8\x1b\\\x1b[?62c",
		"\x1bP>|ghostty\x1b\\\x1bP>|tmux\x1b\\",
	} {
		f.Add([]byte(s), uint8(1))
	}
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		whole, errWhole := probeAll(t, [][]byte{data})
		size := int(step%16) + 1
		var pieces [][]byte
		for i := 0; i < len(data); i += size {
			pieces = append(pieces, data[i:min(i+size, len(data))])
		}
		split, errSplit := probeAll(t, pieces)
		if whole != split || (errWhole == nil) != (errSplit == nil) {
			t.Fatalf("whole %+v (%v), split by %d %+v (%v)", whole, errWhole, size, split, errSplit)
		}
		if len(whole.XTVersion) > maxName {
			t.Fatalf("name of %d bytes kept", len(whole.XTVersion))
		}
		if whole.XTVersion != "" && !bytes.Contains(data, []byte(whole.XTVersion)) {
			t.Fatalf("name %q is not in the reply", whole.XTVersion)
		}
		if whole.DA1 && errWhole != nil {
			t.Fatalf("DA1 found and an error: %v", errWhole)
		}
		strict, trusted := whole.Nested(false), whole.Nested(true)
		if strict == nil && trusted != nil {
			t.Fatalf("trust refused what the strict check passed: %+v", whole)
		}
		if strict != nil && trusted == nil && (strings.TrimSpace(whole.XTVersion) != "" || !whole.DA1) {
			t.Fatalf("trust admitted %+v", whole)
		}
		if (whole == Answer{}) && trusted == nil {
			t.Fatal("no answer was admitted")
		}
		if _, on := refusedName(whole.XTVersion); on && (strict == nil || trusted == nil) {
			t.Fatalf("a refused name passed: %q", whole.XTVersion)
		}
		if !bytes.ContainsRune(data, 0x1b) {
			withTmux := append(append([]byte(nil), data...), answerTmux...)
			a, err := probeAll(t, [][]byte{withTmux})
			if err != nil || a.XTVersion != "tmux 3.8" || !a.DA1 {
				t.Fatalf("tmux after %q: %+v, %v", data, a, err)
			}
			if n := a.Nested(true); n == nil || n.Where != "tmux" {
				t.Fatalf("tmux after %q passed", data)
			}
		}
	})
}

// probeAll runs Probe on a terminal that replies with pieces and then hangs
// up, so a reply without DA1 ends at once and not at the timeout.
func probeAll(t *testing.T, pieces [][]byte) (Answer, error) {
	t.Helper()
	reply := make([]string, 0, len(pieces))
	for _, p := range pieces {
		reply = append(reply, string(p))
	}
	ft := newFakeTerm(t, nil, reply)
	ft.eof = true
	return Probe(ft, 5*time.Second)
}
