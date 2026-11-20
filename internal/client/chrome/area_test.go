package chrome

import (
	"strings"
	"testing"

	"devtools/internal/client/screen"
	"devtools/internal/testkit/golden"
	"devtools/internal/ui"
)

// The tests of the pane-area chrome of the look (P4-03). Goldens are the
// mockups of 5a.9.1 in three layers (5a.17): the text is typed from the
// mockup, the two style layers are written once by -update and read against
// the Styles paragraphs.

// areaTruecolor is the truecolor tier with the dark values of 5a.7.1, the
// five tokens the systems config generates from palette.nix.
func areaTruecolor(t *testing.T) ui.Theme {
	t.Helper()
	th, err := ui.NewTheme(ui.ThemeOptions{
		Tokens: []ui.Token{
			{Name: "strong", Light: 0x111214, Dark: 0xe6e7eb},
			{Name: "muted", Light: 0x55585f, Dark: 0xa6a8b0},
			{Name: "raised", Light: 0xe3e4e8, Dark: 0x30323a},
			{Name: "selection", Light: 0xd6dde9, Dark: 0x3a4150},
			{Name: "warning", Light: 0xb04a00, Dark: 0xec8a3f},
		},
		Dark: true, Known: true, Colors: 1 << 24, UTF8: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// areaPalette is the palette tier with UTF-8 glyphs: the zero theme.
func areaPalette() ui.Theme { return ui.Theme{} }

// areaTiers is both tiers, by name.
func areaTiers(t *testing.T) []struct {
	name  string
	theme ui.Theme
} {
	return []struct {
		name  string
		theme ui.Theme
	}{{"truecolor", areaTruecolor(t)}, {"palette", areaPalette()}}
}

// areaScreen is the golden screen of a canvas, the cursor hidden.
func areaScreen(c *screen.Canvas) golden.Screen {
	w, h := c.Size()
	return golden.Screen{Cols: w, Rows: h, Lines: c.Lines()}
}

// areaStale is a window whose session is on macmini, which is lost: this
// machine desktop, then macmini and macbook in config order.
func areaStale(w, h int) State {
	return State{
		Width: w, Height: h,
		WindowFocused: true,
		Mode:          "normal",
		View:          "stale",
		Session:       "notes-main",
		Machine:       "macmini",
		Summary: Summary{
			Machines: []Machine{
				{Name: "desktop", State: "online"},
				{Name: "macmini", State: "retrying", Problem: "unreachable", Retry: "retrying"},
				{Name: "macbook", State: "online"},
			},
		},
	}
}

// areaLook draws one golden in both tiers and asserts its three layers.
func areaLook(t *testing.T, id string, w, h int, s State, draw func(c *screen.Canvas, s State)) {
	t.Helper()
	var screens [2]golden.Screen
	for i, th := range []ui.Theme{areaTruecolor(t), areaPalette()} {
		c := screen.New(w, h)
		s.Theme = th
		draw(c, s)
		screens[i] = areaScreen(c)
	}
	golden.Look(t, id, screens[0], screens[1])
}

// areaReasons is the state of the lost machine for each banner of 5a.9.1.
var areaReasons = []struct {
	id   string
	edit func(*State)
}{
	{"banner-unreachable-80", func(*State) {}},
	{"banner-ondemand-80", func(s *State) {
		s.Machine, s.Session = "macbook", "blog-main"
		s.Summary.Machines[2] = Machine{Name: "macbook", State: "retrying", Problem: "unreachable", Retry: "next-open"}
	}},
	{"banner-auth-80", func(s *State) {
		s.Summary.Machines[1] = Machine{Name: "macmini", State: "attention", Problem: "auth", Retry: "in-30s"}
	}},
	{"banner-hostkey-80", func(s *State) {
		s.Summary.Machines[1] = Machine{Name: "macmini", State: "attention", Problem: "host-key", Retry: "in-30s"}
	}},
	{"banner-noserver-80", func(s *State) {
		s.Summary.Machines[1] = Machine{Name: "macmini", State: "attention", Problem: "no-server", Retry: "in-30s"}
	}},
	{"banner-incompatible-80", func(s *State) {
		s.Summary.Machines[1] = Machine{Name: "macmini", State: "attention", Problem: "incompatible"}
	}},
	{"banner-local-80", func(s *State) {
		s.Machine, s.Session = "", "devtools-main"
		s.Summary.Machines[0] = Machine{Name: "desktop", State: "retrying", Problem: "no-server", Retry: "retrying"}
	}},
}

func banner(c *screen.Canvas, s State) { NewLook(LookOptions{}).Banner(c, s) }

func TestBannerForEveryReason(t *testing.T) {
	for _, r := range areaReasons {
		t.Run(r.id, func(t *testing.T) {
			s := areaStale(80, 24)
			r.edit(&s)
			areaLook(t, r.id, 80, 1, s, banner)
		})
	}
}

func TestBannerCutOrder(t *testing.T) {
	for _, w := range []int{40, 20} {
		id := "banner-unreachable-" + map[int]string{40: "40", 20: "20"}[w]
		t.Run(id, func(t *testing.T) {
			areaLook(t, id, w, 1, areaStale(w, 24), banner)
		})
	}

	// A machine alias can be 64 characters long (4.6): at 20 columns the
	// phrase is gone and the name is cut at the end, its first letter kept.
	long := "m" + strings.Repeat("x", 63)
	for _, tier := range areaTiers(t) {
		s := areaStale(20, 24)
		s.Theme = tier.theme
		s.Machine = long
		s.Summary.Machines[1].Name = long
		c := screen.New(20, 1)
		if rows := NewLook(LookOptions{}).Banner(c, s); rows != 1 {
			t.Fatalf("%s: rows = %d, want 1", tier.name, rows)
		}
		want := " m" + strings.Repeat("x", 16) + "…"
		if got := golden.PlainRow(c.Lines()[0]); got != want {
			t.Errorf("%s: banner = %q, want %q", tier.name, got, want)
		}
	}

	// Below the room for the name and one cell of the phrase, the name is
	// cut down to its first letter and no further.
	for _, w := range []int{12, 8, 3, 2} {
		s := areaStale(w, 24)
		s.Theme = areaTruecolor(t)
		c := screen.New(w, 1)
		NewLook(LookOptions{}).Banner(c, s)
		got := golden.PlainRow(c.Lines()[0])
		if !strings.HasPrefix(got, " m") {
			t.Errorf("%d columns: banner = %q, want the first letter of the name", w, got)
		}
		if ui.Width(got) > w {
			t.Errorf("%d columns: banner = %q is wider than the row", w, got)
		}
	}
}

func TestTooSmallScreen(t *testing.T) {
	s := areaStale(18, 5)
	s.View = "showing"
	areaLook(t, "chrome-toosmall-18x5", 18, 5, s, func(c *screen.Canvas, s State) {
		c.Fill(screen.Rect{W: 18, H: 5}, screen.Cell{S: "#", W: 1})
		NewLook(LookOptions{}).TooSmall(c, s)
	})
}

func TestPaneMessage(t *testing.T) {
	s := areaStale(80, 24)
	s.View = "showing"
	areaLook(t, "pane-exited-80", 80, 2, s, func(c *screen.Canvas, s State) {
		c.Fill(screen.Rect{W: 80, H: 2}, screen.Cell{S: "x", W: 1})
		NewLook(LookOptions{}).PaneMessage(c, s, ui.PaneKilled("zsh", 9))
	})

	// The rest of the pane is empty, a line that does not fit is cut at the
	// end, and a pane of one row gets the first line only.
	c := screen.New(12, 4)
	c.Fill(screen.Rect{W: 12, H: 4}, screen.Cell{S: "x", W: 1})
	s.Theme = areaTruecolor(t)
	NewLook(LookOptions{}).PaneMessage(c, s, ui.PaneKilled("zsh", 9))
	want := []string{"zsh was kil…", "Press enter…", "", ""}
	for y, line := range c.Lines() {
		if got := golden.PlainRow(line); got != want[y] {
			t.Errorf("row %d = %q, want %q", y, got, want[y])
		}
	}
	c = screen.New(10, 1)
	NewLook(LookOptions{}).PaneMessage(c, s, "ended")
	if got := golden.PlainRow(c.Lines()[0]); got != "ended" {
		t.Errorf("one row = %q", got)
	}
}

func TestPadFill(t *testing.T) {
	s := areaStale(20, 3)
	s.View = "showing"
	areaLook(t, "pad-20x3", 20, 3, s, func(c *screen.Canvas, s State) {
		NewLook(LookOptions{}).Pad(c, s)
	})
}

// areaPanes is a pane area of w by h with a line of styled text in each
// row: what a banner covers.
func areaPanes(w, h int) *screen.Canvas {
	c := screen.New(w, h)
	st := screen.Style{Fg: screen.Color{Kind: 1, V: 2}, Attrs: screen.Bold}
	for y := range h {
		c.Text(0, y, "$ rg -n status docs/", st, w)
	}
	return c
}

func TestBannerCoversOneRowAndResizesNothing(t *testing.T) {
	for _, tier := range areaTiers(t) {
		c := areaPanes(80, 23)
		before := c.Lines()
		s := areaStale(80, 24)
		s.Theme = tier.theme
		if rows := NewLook(LookOptions{}).Banner(c, s); rows != 1 {
			t.Fatalf("%s: rows = %d, want 1", tier.name, rows)
		}
		if w, h := c.Size(); w != 80 || h != 23 {
			t.Errorf("%s: the pane area is %dx%d after the banner, want 80x23", tier.name, w, h)
		}
		after := c.Lines()
		if after[0] == before[0] {
			t.Errorf("%s: row 0 is unchanged", tier.name)
		}
		for y := 1; y < 23; y++ {
			if after[y] != before[y] {
				t.Errorf("%s: row %d changed: %q", tier.name, y, after[y])
			}
		}
		// the ground covers the whole width
		for x := range 80 {
			st := c.At(x, 0).Style
			if st.Bg.Kind == 0 && st.Attrs&screen.Reverse == 0 {
				t.Fatalf("%s: banner cell %d has no ground", tier.name, x)
			}
		}

		// a view that is not stale has no banner and nothing is drawn
		for _, view := range []string{"showing", "none"} {
			c := areaPanes(80, 23)
			s.View = view
			if rows := NewLook(LookOptions{}).Banner(c, s); rows != 0 {
				t.Errorf("%s, view %s: rows = %d, want 0", tier.name, view, rows)
			}
			if got := c.Lines(); got[0] != before[0] {
				t.Errorf("%s, view %s: row 0 = %q", tier.name, view, got[0])
			}
		}
	}
}

func TestBannerUsesNoHue(t *testing.T) {
	for _, tier := range areaTiers(t) {
		for _, r := range areaReasons {
			for _, w := range []int{80, 40, 20} {
				s := areaStale(w, 24)
				r.edit(&s)
				s.Theme = tier.theme
				c := screen.New(w, 1)
				NewLook(LookOptions{}).Banner(c, s)
				for x := range w {
					cell := c.At(x, 0)
					for _, col := range []screen.Color{cell.Style.Fg, cell.Style.Bg, cell.Style.Ul} {
						if col.Kind == 1 || col.Kind == 2 {
							t.Errorf("%s %s at %d: cell %d %q has the palette colour %d", tier.name, r.id, w, x, cell.S, col.V)
						}
					}
					if tier.name == "palette" && (cell.Style.Fg.Kind != 0 || cell.Style.Bg.Kind != 0) {
						t.Errorf("%s %s at %d: cell %d has a colour on a reversed cell", tier.name, r.id, w, x)
					}
				}
			}
		}
	}
	// the failed mark is bold ink on the raised ground, not red
	s := areaStale(80, 24)
	areaReasons[2].edit(&s)
	for _, tier := range areaTiers(t) {
		s.Theme = tier.theme
		c := screen.New(80, 1)
		NewLook(LookOptions{}).Banner(c, s)
		cell := c.At(10, 0)
		if cell.S != "✗" || cell.Style.Fg != (screen.Color{}) || cell.Style.Attrs&screen.Bold == 0 {
			t.Errorf("%s: the failed mark is %q in %+v, want ✗ in bold ink", tier.name, cell.S, cell.Style)
		}
	}
}

func TestBannerAndInactiveFollowTheViewNotTheMode(t *testing.T) {
	l := NewLook(LookOptions{})
	cell := screen.Style{Fg: screen.Color{Kind: 3, V: 0xff0000}, Bg: screen.Color{Kind: 1, V: 4}, Attrs: screen.Bold | screen.Italic}
	for _, tier := range areaTiers(t) {
		s := areaStale(80, 24)
		s.Theme = tier.theme
		c := screen.New(80, 1)
		l.Banner(c, s)
		wantBanner := c.Lines()[0]
		wantInactive := l.Inactive(s, cell)
		d := screen.New(1, 23)
		l.Divider(d, s, true, middle(d, true, true))
		wantDivider := d.Lines()

		for _, mode := range []string{"prefix", "switching", "normal"} {
			m := s
			m.Mode = mode
			if mode == "switching" {
				m.Target = Target{Machine: "macbook", Session: "blog-main"}
			}
			c := screen.New(80, 1)
			if rows := l.Banner(c, m); rows != 1 {
				t.Errorf("%s, mode %s: banner rows = %d, want 1", tier.name, mode, rows)
			}
			if got := c.Lines()[0]; got != wantBanner {
				t.Errorf("%s, mode %s: banner = %q, want %q", tier.name, mode, got, wantBanner)
			}
			if got := l.Inactive(m, cell); got != wantInactive {
				t.Errorf("%s, mode %s: inactive = %+v, want %+v", tier.name, mode, got, wantInactive)
			}
			d := screen.New(1, 23)
			l.Divider(d, m, true, middle(d, true, true))
			if got := d.Lines(); strings.Join(got, "\n") != strings.Join(wantDivider, "\n") {
				t.Errorf("%s, mode %s: the divider changed with the mode", tier.name, mode)
			}
		}

		// a prefix pending in a view that is showing draws no banner
		s.View, s.Mode = "showing", "prefix"
		c = screen.New(80, 1)
		if rows := l.Banner(c, s); rows != 0 || golden.PlainRow(c.Lines()[0]) != "" {
			t.Errorf("%s: a prefix in a showing view drew a banner", tier.name)
		}
	}
}

func TestDividerPointsAtTheFocusedPane(t *testing.T) {
	l := NewLook(LookOptions{})
	comment := screen.Style{Fg: screen.Color{Kind: 1, V: 8}}
	cases := []struct {
		vertical, first bool
		w, h, x, y      int
		line, pointer   string
	}{
		{true, true, 1, 23, 0, 11, "│", "◂"},
		{true, false, 1, 23, 0, 11, "│", "▸"},
		{false, true, 80, 1, 40, 0, "─", "▴"},
		{false, false, 80, 1, 40, 0, "─", "▾"},
		{true, true, 1, 24, 0, 12, "│", "◂"},
		{false, false, 79, 1, 39, 0, "─", "▾"},
	}
	for _, tier := range areaTiers(t) {
		strong := areaStyle(tier.theme.Face(ui.RoleStrong, ui.GroundNone))
		if strong.Attrs&screen.Bold == 0 {
			t.Fatalf("%s: strong is not bold", tier.name)
		}
		for _, tc := range cases {
			for _, focus := range []struct {
				name            string
				focused         bool
				view            string
				pointerIsStrong bool
			}{
				{"focused", true, "showing", true},
				{"unfocused", false, "showing", false},
				{"stale", true, "stale", false},
			} {
				s := areaStale(80, 24)
				s.Theme, s.WindowFocused, s.View = tier.theme, focus.focused, focus.view
				c := screen.New(tc.w, tc.h)
				l.Divider(c, s, tc.vertical, middle(c, tc.vertical, tc.first))
				for y := range tc.h {
					for x := range tc.w {
						cell := c.At(x, y)
						if x == tc.x && y == tc.y {
							want := comment
							if focus.pointerIsStrong {
								want = strong
							}
							if cell.S != tc.pointer || cell.Style != want {
								t.Errorf("%s %s %dx%d: pointer at %d,%d is %q %+v, want %q %+v",
									tier.name, focus.name, tc.w, tc.h, x, y, cell.S, cell.Style, tc.pointer, want)
							}
							continue
						}
						if cell.S != tc.line || cell.Style != comment {
							t.Fatalf("%s %s %dx%d: cell %d,%d is %q %+v, want %q in comment",
								tier.name, focus.name, tc.w, tc.h, x, y, cell.S, cell.Style, tc.line)
						}
					}
				}
			}
		}
	}

	// the ASCII set of 5a.7.2
	s := areaStale(80, 24)
	s.View = "showing"
	th, err := ui.NewTheme(ui.ThemeOptions{UTF8: false})
	if err != nil {
		t.Fatal(err)
	}
	s.Theme = th
	c := screen.New(5, 1)
	l.Divider(c, s, false, middle(c, false, true))
	if got := golden.PlainRow(c.Lines()[0]); got != "--^--" {
		t.Errorf("ASCII divider = %q, want --^--", got)
	}
}

func TestInactiveKeepsCharactersAndDropsColour(t *testing.T) {
	l := NewLook(LookOptions{})
	for _, tier := range areaTiers(t) {
		c := screen.New(30, 3)
		c.Text(0, 0, "$ rg -n 漢字 docs/", screen.Style{Fg: screen.Color{Kind: 3, V: 0x00ff00}, Bg: screen.Color{Kind: 1, V: 4}, Attrs: screen.Bold | screen.Reverse}, 30)
		c.Text(0, 1, "docs/plan.md:12:", screen.Style{Fg: screen.Color{Kind: 2, V: 200}, Attrs: screen.Italic, Underline: 3, Ul: screen.Color{Kind: 1, V: 1}}, 30)
		c.Text(0, 2, "the status row", screen.Style{Attrs: screen.Faint | screen.Strike | screen.Blink}, 30)
		before := make([]string, 3)
		for y, line := range c.Lines() {
			before[y] = golden.PlainRow(line)
		}
		s := areaStale(30, 4)
		s.Theme = tier.theme
		c.Restyle(screen.Rect{W: 30, H: 3}, func(st screen.Style) screen.Style { return l.Inactive(s, st) })
		for y, line := range c.Lines() {
			if got := golden.PlainRow(line); got != before[y] {
				t.Errorf("%s: row %d = %q, want %q", tier.name, y, got, before[y])
			}
		}
		want := screen.Style{Fg: screen.Color{Kind: 1, V: 8}}
		for y := range 3 {
			for x := range 30 {
				if cell := c.At(x, y); cell.W > 0 && cell.Style != want {
					t.Fatalf("%s: cell %d,%d %q is %+v, want comment and nothing else", tier.name, x, y, cell.S, cell.Style)
				}
			}
		}
	}
}

func TestMinSizeIsTwentyBySix(t *testing.T) {
	if cols, rows := NewLook(LookOptions{}).MinSize(); cols != 20 || rows != 6 {
		t.Errorf("MinSize = %d, %d, want 20, 6", cols, rows)
	}
}

func TestSelectionAndCursorStyles(t *testing.T) {
	l := NewLook(LookOptions{})
	cells := []screen.Style{
		{},
		{Fg: screen.Color{Kind: 1, V: 1}, Attrs: screen.Bold},
		{Fg: screen.Color{Kind: 3, V: 0x123456}, Bg: screen.Color{Kind: 2, V: 100}, Attrs: screen.Italic | screen.Reverse},
		{Attrs: screen.Reverse},
		{Fg: screen.Color{Kind: 1, V: 2}, Underline: 1, Ul: screen.Color{Kind: 1, V: 3}, Link: "https://example.com"},
	}

	// truecolor: the selection ground with only ink; the cursor one cell in
	// reverse video
	s := areaStale(80, 24)
	s.Theme = areaTruecolor(t)
	ground := screen.Color{Kind: 3, V: 0x3a4150}
	for _, st := range cells {
		sel := l.Selection(s, st)
		if sel.Bg != ground || sel.Fg != (screen.Color{}) || sel.Ul != (screen.Color{}) || sel.Attrs&screen.Reverse != 0 {
			t.Errorf("truecolor: Selection(%+v) = %+v, want ink on the selection ground", st, sel)
		}
		if sel.Attrs&screen.Bold != st.Attrs&screen.Bold || sel.Link != st.Link {
			t.Errorf("truecolor: Selection(%+v) = %+v lost the bold or the link", st, sel)
		}
		for _, under := range []screen.Style{st, sel} {
			cur := l.CopyCursor(s, under)
			if cur.Attrs&screen.Reverse == 0 || cur.Fg != (screen.Color{}) || cur.Bg != (screen.Color{}) {
				t.Errorf("truecolor: CopyCursor(%+v) = %+v, want reverse video in the default colours", under, cur)
			}
			if cur == sel {
				t.Errorf("truecolor: the copy cursor over %+v looks like the selection", under)
			}
		}
	}

	// palette: the selection is reverse video in the default colours; the
	// cursor is underlined and bold, also inside the selection
	s.Theme = areaPalette()
	for _, st := range cells {
		sel := l.Selection(s, st)
		if sel.Attrs&screen.Reverse == 0 || sel.Fg != (screen.Color{}) || sel.Bg != (screen.Color{}) || sel.Ul != (screen.Color{}) {
			t.Errorf("palette: Selection(%+v) = %+v, want reverse video in the default colours", st, sel)
		}
		if sel.Attrs&screen.Bold != st.Attrs&screen.Bold {
			t.Errorf("palette: Selection(%+v) = %+v did not keep the bold", st, sel)
		}
		cur := l.CopyCursor(s, sel)
		if cur.Underline != 1 || cur.Attrs&screen.Bold == 0 || cur.Attrs&screen.Reverse == 0 {
			t.Errorf("palette: CopyCursor in a selection = %+v, want reverse, underline and bold", cur)
		}
		if cur.Fg.Kind == 3 || cur.Bg.Kind == 3 || cur.Ul.Kind == 3 {
			t.Errorf("palette: CopyCursor = %+v has an RGB colour", cur)
		}
		if cur == sel {
			t.Errorf("palette: the copy cursor inside a selection looks like the selection")
		}
		if out := l.CopyCursor(s, screen.Style{}); out.Underline != 1 || out.Attrs&screen.Bold == 0 {
			t.Errorf("palette: CopyCursor outside a selection = %+v, want underline and bold", out)
		}
	}
}

// middle is the pointer in the middle cell of the divider c, towards the
// pane before it when before is set.
func middle(c *screen.Canvas, vertical, before bool) Pointer {
	w, h := c.Size()
	p := Pointer{Toward: PointAfter, At: w / 2}
	if vertical {
		p.At = h / 2
	}
	if before {
		p.Toward = PointBefore
	}
	return p
}

func TestDividerPointerGoesWhereItIsToldOrNowhere(t *testing.T) {
	l := NewLook(LookOptions{})
	s := areaStale(80, 24)
	s.View, s.WindowFocused = "showing", true
	th, err := ui.NewTheme(ui.ThemeOptions{UTF8: false})
	if err != nil {
		t.Fatal(err)
	}
	s.Theme = th
	for _, tc := range []struct {
		p    Pointer
		want string
	}{
		{Pointer{Toward: PointNone, At: 2}, "-----"},
		{Pointer{Toward: PointBefore, At: 1}, "-^---"},
		{Pointer{Toward: PointAfter, At: 4}, "----v"},
		{Pointer{Toward: PointAfter, At: 9}, "----v"}, // kept inside the line
	} {
		c := screen.New(5, 1)
		l.Divider(c, s, false, tc.p)
		if got := golden.PlainRow(c.Lines()[0]); got != tc.want {
			t.Errorf("look %+v = %q, want %q", tc.p, got, tc.want)
		}
		c = screen.New(5, 1)
		Plain{}.Divider(c, plainState(80, 24), false, tc.p)
		if got := golden.PlainRow(c.Lines()[0]); got != tc.want {
			t.Errorf("plain %+v = %q, want %q", tc.p, got, tc.want)
		}
	}
}
