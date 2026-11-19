package integration

// Level 3: what the engine hands the server and the server hands the
// viewer: history text, the clipboard, the answers to a program's queries,
// and synchronized output.

import (
	"encoding/hex"
	"strings"
	"testing"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
	"devtools/internal/server/servertest"
	"devtools/internal/testkit/golden"
)

func TestHistoryTextOverTheWire(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("lines", "200"))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitFor(p, "line 200")
	f := lastFrame(t, c, p)
	if f.Base-f.First < 150 {
		t.Fatalf("the frame says %d lines of history (first %d, base %d)", f.Base-f.First, f.First, f.Base)
	}
	text := func(from, to proto.Pos, block bool) string {
		t.Helper()
		r := c.Request(proto.PaneText{Pane: p, From: from, To: to, Block: block})
		if !r.OK {
			t.Fatalf("pane.text: %s: %s", r.Code, r.Text)
		}
		var res proto.PaneTextResult
		r.Msg.Decode(&res)
		if res.Cut {
			t.Fatalf("a short text was cut: %+v", res)
		}
		return res.Text
	}
	// The oldest line of history is "line 1": nothing was trimmed.
	if got := text(proto.Pos{Line: f.First, Col: 0}, proto.Pos{Line: f.First + 2, Col: 79}, false); got != "line 1\nline 2\nline 3" {
		t.Fatalf("the first three lines of history are %q", got)
	}
	// A range that ends inside a line, given backwards.
	if got := text(proto.Pos{Line: f.First + 99, Col: 5}, proto.Pos{Line: f.First + 98, Col: 2}, false); got != "ne 99\nline 1" {
		t.Fatalf("the range across lines 99 and 100 is %q", got)
	}
	// A block takes the same columns of every line.
	if got := text(proto.Pos{Line: f.First + 9, Col: 0}, proto.Pos{Line: f.First + 11, Col: 5}, true); got != "line 1\nline 1\nline 1" {
		t.Fatalf("the block over lines 10 to 12 is %q", got)
	}
	// Lines of the live screen are addressed the same way.
	row := -1
	for y, s := range plainRows(c.Screen(p)) {
		if s == "line 200" {
			row = y
		}
	}
	if row < 0 {
		t.Fatal("line 200 is not on the screen")
	}
	if got := text(proto.Pos{Line: f.Base + int64(row)}, proto.Pos{Line: f.Base + int64(row), Col: 79}, false); got != "line 200" {
		t.Fatalf("the screen row of line 200 reads %q", got)
	}
	if r := c.Request(proto.PaneText{Pane: "p999"}); r.OK || r.Code != proto.CodeNoPane {
		t.Fatalf("pane.text of an unknown pane was answered %+v", r)
	}
}

func TestAClipboardEventReachesTheViewer(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("osc", "52;c;aGVsbG8gY2xpcGJvYXJk"))
	viewer := prototest.Dial(t, socket, prototest.Interactive())
	v := viewer.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	idle := prototest.Dial(t, socket, prototest.Interactive())
	elsewhere := prototest.Dial(t, socket, prototest.Interactive())
	ve := elsewhere.View(proto.ViewSet{Dir: work(t, home, "other"), Name: "other", Cols: 80, Rows: 24})

	clips := func(c *prototest.Client) []proto.Clipboard {
		var out []proto.Clipboard
		for _, m := range c.Received() {
			if m.T == proto.TypeEventClipboard {
				var e proto.Clipboard
				if err := m.Decode(&e); err != nil {
					t.Fatal(err)
				}
				out = append(out, e)
			}
		}
		return out
	}
	viewer.WaitMessage(proto.TypeEventClipboard, 0)
	elsewhere.WaitMessage(proto.TypeEventClipboard, 0)
	for _, e := range clips(viewer) {
		if e.Pane != v.Panes[0] || string(e.Data) != "hello clipboard" {
			t.Fatalf("the viewer got %+v", e)
		}
	}
	// Each connection hears only the panes of the tab it displays.
	for _, e := range clips(elsewhere) {
		if e.Pane != ve.Panes[0] {
			t.Fatalf("a viewer of another session got the clipboard of %s", e.Pane)
		}
	}
	// A message after both events: idle has read everything the server sent
	// it before, and none of it is a clipboard event.
	idle.Request(proto.StateGet{})
	if got := clips(idle); len(got) != 0 {
		t.Fatalf("a connection without a view got %+v", got)
	}
}

// queryReplies decodes what the helper query printed once it printed "end":
// one reply per line in hex.
func queryReplies(t *testing.T, c *prototest.Client, pane string) []string {
	t.Helper()
	s := c.WaitFor(pane, "end")
	var out []string
	for _, row := range plainRows(s) {
		if row == "end" {
			break
		}
		if row == "" {
			continue
		}
		b, err := hex.DecodeString(strings.ReplaceAll(row, " ", ""))
		if err != nil {
			t.Fatalf("pane %s printed %q: %v", pane, row, err)
		}
		out = append(out, string(b))
	}
	return out
}

func TestQueriesAreAnsweredWithTheViewersColours(t *testing.T) {
	home, socket := startServer(t, servertest.WithHelper("query"))
	hello := prototest.Interactive()
	hello.Colors = &proto.Colors{Fg: "#c0caf5", Bg: "#1a1b26", Dark: true}
	c := prototest.Dial(t, socket, hello)
	// Wide enough that no reply wraps.
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 300, Rows: 30})
	got := queryReplies(t, c, v.Panes[0])
	want := []struct{ what, prefix, contains string }{
		{"DA1", "\x1b[?", "c"},
		{"XTVERSION", "\x1bP>|", "devtools " + servertest.Version + " " + servertest.Host},
		{"OSC 10", "\x1b]10;", "rgb:c0c0/caca/f5f5"},
		{"OSC 11", "\x1b]11;", "rgb:1a1a/1b1b/2626"},
		{"kitty query", "\x1b[?", "u"},
	}
	if len(got) != len(want) {
		t.Fatalf("%d replies, want %d: %q", len(got), len(want), got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w.prefix) || !strings.Contains(got[i], w.contains) {
			t.Errorf("reply %d (%s) is %q, want %q ... %q", i, w.what, got[i], w.prefix, w.contains)
		}
	}

	// The outer terminal's colours change: a program started afterwards is
	// answered with the new ones.
	c.Send(proto.ViewColors{Fg: "#112233", Bg: "#ddeeff", Dark: false})
	c.Request(proto.StateGet{})
	r := c.Request(proto.PaneSplit{Pane: v.Panes[0], Direction: proto.SplitDown})
	if !r.OK {
		t.Fatalf("pane.split: %s: %s", r.Code, r.Text)
	}
	var sp proto.PaneSplitResult
	r.Msg.Decode(&sp)
	got = queryReplies(t, c, sp.Pane)
	if len(got) != 5 || !strings.Contains(got[2], "rgb:1111/2222/3333") || !strings.Contains(got[3], "rgb:dddd/eeee/ffff") {
		t.Fatalf("after view.colors the replies are %q", got)
	}
}

func TestSynchronizedOutputNeverShowsAHalfDrawnScreen(t *testing.T) {
	// The helper holds mode 2026 open for 100 ms between the two halves;
	// the server holds frames for at most SyncHold (150 ms).
	home, socket := startServer(t, servertest.WithHelper("sync", "100"))
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Dir: work(t, home), Name: "work", Cols: 80, Rows: 24})
	p := v.Panes[0]
	c.WaitScreen(p, "both halves", func(s golden.Screen) bool {
		return s.Contains("sync 00 top") && s.Contains("sync 23 bottom")
	})
	screens := screensOf(t, c.Received(), p)
	for i, s := range screens {
		top, bottom := s.Contains(" top"), s.Contains(" bottom")
		if top != bottom {
			t.Fatalf("frame %d shows half a drawing:\n%s", i+1, s.Plain())
		}
		if top {
			for y, row := range plainRows(s) {
				want := "sync "
				if y < 10 {
					want += "0"
				}
				if !strings.HasPrefix(row, want) {
					t.Fatalf("frame %d row %d is %q:\n%s", i+1, y, row, s.Plain())
				}
			}
		}
	}
	if len(screens) == 0 {
		t.Fatal("no frame arrived")
	}
}
