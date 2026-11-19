//go:build cgo

package vt

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"devtools/internal/testkit/golden"
)

// The replay group C16: recordings in the advanced timing format of
// util-linux script under testdata/replay (<name>.tm and <name>.raw) are
// fed to a terminal with their resizes; the screen at every pause of 50 ms
// or more, after every resize and at the end is compared with
// testdata/golden/replay-<name>.txt, which the owned binding made.
//
// provisional (E1): the recordings of the user's real sessions (Claude
// Code, nvim, zsh) are pending [GO], and committing a recording needs the
// user's go-ahead (9.6). Until they exist the directory holds a synthetic
// stream written by synthRecording below, a scripted program that draws
// what a full-screen program draws; it contains no recorded content.

// replayEvent is one entry of a recording.
type replayEvent struct {
	delay      float64
	data       []byte // output; nil for a resize
	cols, rows int
}

// loadRecording reads a timing log and its output log.
func loadRecording(timing, output string) (cols, rows int, evs []replayEvent, err error) {
	raw, err := os.ReadFile(output)
	if err != nil {
		return 0, 0, nil, err
	}
	off := 0
	if bytes.HasPrefix(raw, []byte("Script started")) {
		off = bytes.IndexByte(raw, '\n') + 1
	}
	f, err := os.Open(timing)
	if err != nil {
		return 0, 0, nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 3 {
			continue
		}
		d, _ := strconv.ParseFloat(fs[1], 64)
		switch fs[0] {
		case "H":
			if len(fs) > 3 && fs[2] == "COLUMNS" {
				cols, _ = strconv.Atoi(fs[3])
			}
			if len(fs) > 3 && fs[2] == "LINES" {
				rows, _ = strconv.Atoi(fs[3])
			}
		case "O":
			n, _ := strconv.Atoi(fs[2])
			if off+n > len(raw) {
				return 0, 0, nil, fmt.Errorf("%s: the output log ends before the timing log", output)
			}
			evs = append(evs, replayEvent{delay: d, data: raw[off : off+n]})
			off += n
		case "S":
			if fs[2] != "SIGWINCH" {
				continue
			}
			e := replayEvent{delay: d}
			for _, kv := range fs[3:] {
				if v, ok := strings.CutPrefix(kv, "ROWS="); ok {
					e.rows, _ = strconv.Atoi(v)
				}
				if v, ok := strings.CutPrefix(kv, "COLS="); ok {
					e.cols, _ = strconv.Atoi(v)
				}
			}
			evs = append(evs, e)
		}
	}
	if cols == 0 || rows == 0 {
		return 0, 0, nil, fmt.Errorf("%s: no COLUMNS and LINES header", timing)
	}
	return cols, rows, evs, sc.Err()
}

// replay feeds a recording and returns the screens it shows at its
// comparison points.
func replay(t *testing.T, cols, rows int, evs []replayEvent) string {
	tm := termAt(t, cols, rows, func(o *Options) {
		o.History = 10000
		o.Colors = Colors{Fg: "#c0caf5", Bg: "#1a1b26", Dark: true}
	})
	var out strings.Builder
	n := 0
	snap := func(reason string) {
		for y := range tm.rows {
			if err := checkRow(tm.Row(y)); err != nil {
				t.Errorf("snapshot %d row %d: %v", n, y, err)
			}
		}
		fmt.Fprintf(&out, "@ %d %s alt=%v\n%s", n, reason, tm.Modes().Alt, screenOf(tm).Runs())
		n++
	}
	for i, e := range evs {
		if e.delay >= 0.05 && i > 0 {
			snap("pause")
		}
		if e.data == nil {
			tm.Resize(e.cols, e.rows)
			_ = tm.Replies()
			snap(fmt.Sprintf("resize %dx%d", e.cols, e.rows))
			continue
		}
		tm.Write(e.data)
		_ = tm.Replies()
	}
	snap("end")
	return out.String()
}

func TestReplayGoldenScreens(t *testing.T) {
	timings, err := filepath.Glob("testdata/replay/*.tm")
	if err != nil {
		t.Fatal(err)
	}
	if len(timings) == 0 {
		t.Fatal("testdata/replay holds no recording (E1 is a condition of K0)")
	}
	for _, tmf := range timings {
		name := strings.TrimSuffix(filepath.Base(tmf), ".tm")
		t.Run(name, func(t *testing.T) {
			cols, rows, evs, err := loadRecording(tmf, strings.TrimSuffix(tmf, ".tm")+".raw")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "replay-"+name, replay(t, cols, rows, evs))
		})
	}
}

func TestTheSyntheticRecordingIsCurrent(t *testing.T) {
	tm, raw := synthRecording()
	for _, f := range []struct {
		path string
		data []byte
	}{{"testdata/replay/synthetic-tui.tm", tm}, {"testdata/replay/synthetic-tui.raw", raw}} {
		if golden.Updating() {
			if err := os.WriteFile(f.path, f.data, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		have, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(have, f.data) {
			t.Errorf("%s differs from synthRecording; run go test ./internal/vt -update", f.path)
		}
	}
}

// synthRecording writes a recording of a scripted program in the format of
// script --logging-format advanced: a shell session with colours, wide
// characters and a link, then a full-screen program on the alternate
// screen with a frame, a status row, a scroll region, synchronized output
// and the cursor shapes, redrawn after each of four resizes, then the shell
// again. provisional (E1): it stands in for the user's recordings.
func synthRecording() (timing, raw []byte) {
	var tm, out bytes.Buffer
	cols, rows := 100, 30
	fmt.Fprintf(&tm, "H 0.000000 COLUMNS %d\nH 0.000000 LINES %d\nH 0.000000 COMMAND synthetic (provisional E1)\n", cols, rows)
	emit := func(delay float64, s string) {
		fmt.Fprintf(&tm, "O %.6f %d\n", delay, len(s))
		out.WriteString(s)
	}
	resize := func(delay float64, c, r int) {
		cols, rows = c, r
		fmt.Fprintf(&tm, "S %.6f SIGWINCH ROWS=%d COLS=%d\n", delay, r, c)
	}
	prompt := "\x1b[90m\U000F0B9F \x1b[0m \x1b[1;36mwork\x1b[0m \x1b[32m➜\x1b[0m "
	emit(0.06, prompt)
	emit(0.2, "ls -l\r\n")
	for i, name := range []string{"colors.txt", "notes 漢字.md", "sample.go", "\U0001F44D\U0001F3FD-thumbs", "\U0001F1E9\U0001F1EA-flag"} {
		emit(0.001, fmt.Sprintf("-rw-r--r-- 1 user users %5d Nov 18 15:%02d \x1b[38;5;%dm%s\x1b[0m\r\n", 1000*(i+3), 10+i, 30+40*i, name))
	}
	emit(0.001, "see \x1b]8;;https://example.com/docs\x1b\\the docs\x1b]8;;\x1b\\ and \x1b[4:3;58;2;255;0;0mthis\x1b[0m\r\n"+prompt)
	for i := range 40 {
		emit(0.0005, fmt.Sprintf("\x1b[3%dmline %02d of a long listing that wraps at narrow widths, with é and 字 in it\x1b[0m\r\n", i%8, i))
	}
	emit(0.3, prompt+"tui\r\n")
	frame := func(step int) {
		var b strings.Builder
		b.WriteString("\x1b[?2026h\x1b[H\x1b[2J")
		b.WriteString("\x1b[1;1H\x1b[7m" + pad(fmt.Sprintf(" synthetic tui  step %d  %dx%d", step, cols, rows), cols) + "\x1b[0m")
		b.WriteString("\x1b[2;1H┌" + strings.Repeat("─", cols-2) + "┐")
		for y := 3; y < rows-1; y++ {
			fmt.Fprintf(&b, "\x1b[%d;1H│\x1b[%d;%dH│", y, y, cols)
		}
		b.WriteString(fmt.Sprintf("\x1b[%d;1H└", rows-1) + strings.Repeat("─", cols-2) + "┘")
		for y := 3; y < rows-1 && y < 12; y++ {
			fmt.Fprintf(&b, "\x1b[%d;3H\x1b[38;2;%d;%d;200m%s\x1b[0m", y, 20*y%256, 40*step%256, cut(fmt.Sprintf("item %d.%d  ● ○ ◆ ✓  中文 \U0001F468‍\U0001F469‍\U0001F467", step, y), cols-4))
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[44;97m%s\x1b[0m", rows, pad(" NORMAL  ▸ status row", cols))
		fmt.Fprintf(&b, "\x1b[%d q\x1b[5;4H\x1b[?2026l", 1+step%6)
		emit(0.02, b.String())
	}
	emit(0.01, "\x1b[?1049h\x1b[?25l\x1b[?1002h\x1b[?1006h\x1b[?2004h")
	frame(0)
	// scroll inside a region
	emit(0.08, fmt.Sprintf("\x1b[3;%dr\x1b[%d;1H", rows-2, rows-2)+strings.Repeat("\n\x1b[3Gscrolled \x1b[1mbold\x1b[0m", 4)+"\x1b[r\x1b[?25h")
	for step, size := range [][2]int{{120, 40}, {60, 20}, {132, 43}, {100, 30}} {
		resize(0.1, size[0], size[1])
		frame(step + 1)
	}
	emit(0.2, "\x1b[?2004l\x1b[?1006l\x1b[?1002l\x1b[0 q\x1b[?1049l")
	emit(0.06, "done\r\n"+prompt+"\x1b]0;user@synthetic: ~\x07")
	return tm.Bytes(), out.Bytes()
}

func pad(s string, n int) string {
	s = cut(s, n)
	if w := rowWidth(s); w < n {
		s += strings.Repeat(" ", n-w)
	}
	return s
}

func cut(s string, n int) string {
	for rowWidth(s) > n {
		r := []rune(s)
		s = string(r[:len(r)-1])
	}
	return s
}
