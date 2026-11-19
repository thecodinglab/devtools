package integration

// Level 5: keys from the outer terminal to the program, byte exact, through
// both terminals (the client's input parser and the pane's key encoder),
// and the default bindings of 5a.11.2.

import (
	"strings"
	"testing"

	"devtools/internal/testkit/golden"
	"devtools/internal/testkit/termtest"
)

// typeAndExpect types each input into the outer terminal and waits until the
// program has read the bytes of its pane encoding, all inputs so far in
// order.
func typeAndExpect(t *testing.T, x *termtest.Terminal, keys []struct{ name, in, pane string }) {
	t.Helper()
	want := ""
	for _, k := range keys {
		x.Type(k.in)
		want += hexBytes(k.pane)
		waitHex(x, "the bytes of "+k.name+" in the pane", 0, 80, want)
	}
}

func TestShiftEnterArrivesByteExact(t *testing.T) {
	s := newL5(t, "hex")
	x := s.attach(80, 24)
	shows(x, "ready")
	// ghostty sends ESC CR for shift+enter (3.2.4); the client reports
	// alt+enter and the pane's encoder writes ESC CR to a legacy pane.
	typeAndExpect(t, x, []struct{ name, in, pane string }{
		{"shift+enter", "\x1b\r", "\x1b\r"},
	})
}

func TestKeysArriveByteExactThroughBothTerminals(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		s := newL5(t, "hex")
		x := s.attach(80, 24)
		shows(x, "ready")
		typeAndExpect(t, x, []struct{ name, in, pane string }{
			{"ctrl+c", "\x03", "\x03"},
			{"alt+b", "\x1bb", "\x1bb"},
			{"F5", "\x1b[15~", "\x1b[15~"},
			{"ctrl+left", "\x1b[1;5D", "\x1b[1;5D"},
			{"a two-byte character", "é", "é"},
			{"a four-byte character", "😀", "😀"},
			// The prefix twice sends the prefix chord itself (5a.11.2).
			{"ctrl+space twice", keyPrefix + keyPrefix, "\x00"},
		})
	})
	t.Run("kitty press and release", func(t *testing.T) {
		// The program asks for every key as an escape code with its event
		// type (kitty flags 1, 2 and 8); the outer terminal reports a press
		// and a release of a as CSI u.
		s := newL5(t, `modes \e[>11u`)
		x := s.attach(80, 24)
		shows(x, "ready")
		typeAndExpect(t, x, []struct{ name, in, pane string }{
			{"a pressed", "\x1b[97u", "\x1b[97u"},
			{"a released", "\x1b[97;1:3u", "\x1b[97;1:3u"},
		})
	})
}

func TestPrefixCThenTypingReachesTheNewTab(t *testing.T) {
	s := newL5(t, "hex")
	x := s.attach(80, 24)
	shows(x, "ready")
	x.Type("zz")
	waitHex(x, "zz in the first tab", 0, 80, hexBytes("zz"))
	// The prefix, c and the text in one write: the typeahead gate holds the
	// text until the new tab is focused (3.2.4).
	x.Type(keyPrefix + "c" + "abc")
	x.WaitScreen("abc in the second tab", func(s golden.Screen) bool {
		return strings.Contains(statusRow(s), "[2]") && hexOf(s) == hexBytes("abc")
	})
	// None of it reached the first tab.
	press(x, "1")
	x.WaitScreen("the first tab with zz only", func(s golden.Screen) bool {
		return strings.Contains(statusRow(s), "[1") && hexOf(s) == hexBytes("zz")
	})
}

// The columns of two panes side by side in an 80 column window: the left
// pane is 40 columns wide, the divider is column 40, the right pane the 39
// columns after it.
func leftPane(s golden.Screen) string  { return hexIn(s, 0, 40) }
func rightPane(s golden.Screen) string { return hexIn(s, 41, 80) }

// splitRight presses prefix+v and waits until both panes show "ready" and
// the divider stands between them.
func l5SplitRight(t *testing.T, x *termtest.Terminal) golden.Screen {
	t.Helper()
	press(x, "v")
	return x.WaitScreen("two panes side by side", func(s golden.Screen) bool {
		rows := plainRows(s)
		if len(rows) < 2 {
			return false
		}
		top := []rune(rows[0] + strings.Repeat(" ", 80))
		return strings.HasPrefix(rows[0], "ready") && top[40] == '|' && strings.HasPrefix(string(top[41:]), "ready")
	})
}

func TestSplitFocusAndCloseWithTheDefaultBindings(t *testing.T) {
	s := newL5(t, "hex")
	x := s.attach(80, 24)
	shows(x, "ready")
	l5SplitRight(t, x)
	// The new pane has the focus.
	x.Type("a")
	x.WaitScreen("a in the right pane", func(s golden.Screen) bool {
		return rightPane(s) == hexBytes("a") && leftPane(s) == ""
	})
	// ctrl+h moves the focus to the left: hex is no editor.
	x.Type(keyCtrlH)
	x.Type("b")
	x.WaitScreen("b in the left pane", func(s golden.Screen) bool {
		return leftPane(s) == hexBytes("b") && rightPane(s) == hexBytes("a")
	})
	// ctrl+l back to the right.
	x.Type(keyCtrlL)
	x.Type("c")
	x.WaitScreen("c in the right pane", func(s golden.Screen) bool {
		return rightPane(s) == hexBytes("ac") && leftPane(s) == hexBytes("b")
	})
	// prefix+x closes the focused pane; its program is the pane's shell, so
	// nothing is asked. The left pane takes the whole width.
	press(x, "x")
	x.WaitScreen("one pane again", func(s golden.Screen) bool {
		for _, r := range plainRows(s)[:s.Rows-1] {
			if strings.Contains(r, "|") {
				return false
			}
		}
		return hexOf(s) == hexBytes("b")
	})
	x.Type("d")
	waitHex(x, "d in the remaining pane", 0, 80, hexBytes("bd"))
}

func TestVimAwareFocusKeys(t *testing.T) {
	// The first pane runs hex under the name nvim, in the foreground group
	// of its shell: an editor (3.2.1, F13). The pane split from it runs hex.
	s := newL5(t, "once first fg nvim hex")
	x := s.attach(80, 24)
	shows(x, "ready")
	o := oneshot(t, s.srv.socket())
	first := waitEditor(t, o)

	l5SplitRight(t, x)
	// In the plain hex pane ctrl+h moves the focus to the editor's pane.
	x.Type(keyCtrlH)
	x.Type("x")
	x.WaitScreen("x in the editor's pane", func(s golden.Screen) bool {
		return leftPane(s) == hexBytes("x") && rightPane(s) == ""
	})
	// In the editor's pane ctrl+l goes to the program, and the focus stays.
	x.Type(keyCtrlL)
	x.WaitScreen("ctrl+l read by the editor", func(s golden.Screen) bool {
		return leftPane(s) == hexBytes("x\x0c")
	})
	x.Type("y")
	x.WaitScreen("y in the editor's pane", func(s golden.Screen) bool {
		return leftPane(s) == hexBytes("x\x0cy") && rightPane(s) == ""
	})
	if st := stateGet(t, o); !paneOf(st, first).Editor {
		t.Fatalf("pane %s is no longer an editor", first)
	}
}
