package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRecordedSessionsReplayToTheExpectedStatuses replays every session of
// testdata/replay through a Set, one timed input per line, and compares the
// status after each line with the line's last column.
//
// provisional (E4): the three files are the synthetic fixtures of
// e4-agent-reports.md ("Fixtures for P2-05"), built from scripted pane runs
// that follow the Claude Code 2.1.319 bundle and the Codex 0.167.0 binary.
// They are replaced by the real recordings of E4's [GO] part.
//
// Format: "# comment", or "ms kind value expected" where kind is proc (a
// name and its argv in brackets, or "(no match)"), title (a quoted string),
// report (state/kind as the engine delivered them) or tick; expected is a
// status or "gone". Between lines the timer the Set asks for fires at its
// time, as the server's Core.After would.
func TestRecordedSessionsReplayToTheExpectedStatuses(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "replay", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Fatalf("found %d replay files, want the three of E4", len(files))
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) { replay(t, file) })
	}
}

func replay(t *testing.T, file string) {
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const pane = "p1"
	clock := t0
	s := NewSet(func() time.Time { return clock })
	var next time.Time
	lines := 0
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			t.Fatalf("line %d: %q has fewer than four columns", n, line)
		}
		ms, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("line %d: time %q: %v", n, fields[0], err)
		}
		kind, want := fields[1], fields[len(fields)-1]
		value := strings.TrimSpace(line[strings.Index(line, kind)+len(kind):])
		value = strings.TrimSpace(strings.TrimSuffix(value, want))
		now := at(ms)

		// The timer fires first if it is due by now.
		for !next.IsZero() && !next.After(now) {
			clock = next
			_, next = s.Tick()
		}
		clock = now
		switch kind {
		case "proc":
			s.Procs(pane, parseProc(t, n, value))
		case "title":
			title, err := strconv.Unquote(value)
			if err != nil {
				t.Fatalf("line %d: title %s: %v", n, value, err)
			}
			s.Title(pane, title)
		case "report":
			state, k, ok := strings.Cut(value, "/")
			if !ok {
				t.Fatalf("line %d: report %q is not state/kind", n, value)
			}
			s.Report(pane, Report{State: state, Kind: k})
		case "tick":
		default:
			t.Fatalf("line %d: unknown input %q", n, kind)
		}
		_, next = s.Tick()

		st, ok := s.All()[pane]
		got := string(st.Status)
		if !ok {
			got = "gone"
		}
		if got != want {
			t.Errorf("line %d (%d ms, %s %s): status %s, want %s", n, ms, kind, value, got, want)
		}
		lines++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if lines == 0 {
		t.Fatal("no input lines")
	}
}

// parseProc reads "name [argv ...]" or "(no match)", which stands for the
// shell having the foreground again.
func parseProc(t *testing.T, n int, v string) []Proc {
	if v == "(no match)" {
		return []Proc{shell}
	}
	name, args, ok := strings.Cut(v, " [")
	if !ok || !strings.HasSuffix(args, "]") {
		t.Fatalf("line %d: proc %q is not name [argv]", n, v)
	}
	return []Proc{{Name: name, Args: strings.Fields(strings.TrimSuffix(args, "]"))}}
}
