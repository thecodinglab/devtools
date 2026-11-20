package panes

import (
	"fmt"
	"slices"

	"devtools/internal/client/screen"
	"devtools/internal/proto"
)

// Set is the panes of the tab the client displays, in the tab's order (the
// leaves of its layout: the first is top left), and the tab's layout. The
// zero value displays nothing and refuses every frame.
type Set struct {
	tab     string
	tree    proto.Node
	order   []string
	screens map[string]*Screen
}

// Reset starts a new view (view.ok, 4.2.3): the panes of v.Tab, each
// without content and expecting seq 1. view.ok carries no layout, so the
// client calls Retab with the tab from its state right after Reset (the
// state already holds it, W2); until then the panes are placed side by
// side.
func (s *Set) Reset(v proto.ViewOK) {
	s.tab = v.Tab
	s.order = slices.Clone(v.Panes)
	s.tree = proto.Node{}
	if len(v.Panes) > 0 {
		s.tree = proto.Row(s.order, proto.SplitRight)
	}
	s.screens = make(map[string]*Screen, len(v.Panes))
	for _, id := range v.Panes {
		s.screens[id] = &Screen{}
	}
}

// Retab follows the displayed tab as the state says it is now: the
// session's current tab changed, or its panes did (a split, a closed pane).
// When the tab or its list of panes changed, every pane of t starts again
// at seq 1 and its first frame must be full: that is what the server does
// whenever the set of panes it shows a connection changes (4.2.3, 4.2.4;
// a new displayed set restarts every slot). A pane that stays keeps the
// cells it has, so it is drawn until its full frame replaces them. When
// only the layout differs (the call after Reset, a resize), the layout is
// taken and nothing restarts; a tab that is unchanged, or a change of its
// focused pane alone, changes nothing, so the client may call Retab on
// every put of the displayed tab.
func (s *Set) Retab(t proto.Tab) {
	if t.ID == s.tab && slices.Equal(t.Panes, s.order) {
		s.tree = t.Tree().Clone()
		return
	}
	old := s.screens
	s.tab = t.ID
	s.tree = t.Tree().Clone()
	s.order = slices.Clone(t.Panes)
	s.screens = make(map[string]*Screen, len(t.Panes))
	for _, id := range t.Panes {
		sc := old[id]
		if sc == nil {
			sc = &Screen{}
		}
		sc.restart()
		s.screens[id] = sc
	}
}

// restart makes the screen expect seq 1 and a full frame again. The cells
// stay until that frame arrives, and every row is drawn again.
func (s *Screen) restart() {
	s.seq = 0
	s.pend = nil
	for y := range s.dirty {
		s.dirty[y] = true
	}
}

// Apply applies a frame or a piece of one to the screen of its pane. A
// frame for a pane that is not one of the displayed tab's is an error, as
// is everything Screen.Apply refuses; the caller drops the link (3.2.4).
func (s *Set) Apply(f proto.Frame) error {
	sc := s.screens[f.Pane]
	if sc == nil {
		return fmt.Errorf("panes: frame %d of pane %s: %w (tab %q)", f.Seq, f.Pane, errNotShown, s.tab)
	}
	_, err := sc.Apply(f)
	return err
}

// Screen returns the screen of a displayed pane, nil for any other.
func (s *Set) Screen(pane string) *Screen { return s.screens[pane] }

// Layout is where the panes of the displayed tab lie in the pane area, in
// canvas coordinates. Every rectangle is clipped to the area. A pane that
// lies wholly outside it, or has no complete frame yet, has an empty
// rectangle. Dividers are the one-cell lines between the nodes of the
// splits that lie inside the area, none with one pane. Pad is the cells of
// the area that belong to no pane and to no divider: the area a smaller
// session leaves free (3.2.3), or a pane without a frame yet.
type Layout struct {
	Panes    map[string]screen.Rect
	Dividers []Divider
	Pad      []screen.Rect
}

// Divider is a line between two nodes of a split: Vertical between nodes
// side by side, else between stacked ones.
type Divider struct {
	Rect     screen.Rect
	Vertical bool
}

// Layout places the panes in area from the sizes of their frames and the
// tab's layout (4.2.4): a split of direction right puts each node after the
// one before plus one divider column, a split down each below the one
// before plus one divider row. A node is as wide and high as its frames
// make it: a pane its frame's size, a split the sum of its nodes along its
// direction with the dividers and the largest of them across it, which is
// also the length of its dividers. A session larger than the area is
// clipped at its right and bottom edge (the top left part shows); a smaller
// one leaves pad. Nothing after a pane without a frame yet can be placed:
// the rest of its split, and of every split it lies in, is empty.
func (s *Set) Layout(area screen.Rect) Layout {
	area.W, area.H = max(area.W, 0), max(area.H, 0)
	l := Layout{Panes: make(map[string]screen.Rect, len(s.order))}
	var placed []screen.Rect
	empty := screen.Rect{X: area.X, Y: area.Y}
	var place func(n proto.Node, x, y int, blocked bool) (w, h int, known bool)
	place = func(n proto.Node, x, y int, blocked bool) (int, int, bool) {
		if n.Leaf() {
			sc := s.screens[n.Pane]
			if blocked || sc == nil || (sc.seq == 0 && len(sc.lines) == 0) {
				l.Panes[n.Pane] = empty
				return 0, 0, false
			}
			r := clip(screen.Rect{X: x, Y: y, W: sc.cols, H: sc.rows}, area)
			l.Panes[n.Pane] = r
			placed = append(placed, r)
			return sc.cols, sc.rows, true
		}
		right := n.Split != proto.SplitDown
		var ends []int // where a divider starts, after each node but the last
		w, h, known := 0, 0, !blocked
		for i, c := range n.Nodes {
			if i > 0 && known {
				if right {
					ends = append(ends, x+w)
					w++
				} else {
					ends = append(ends, y+h)
					h++
				}
			}
			cx, cy := x, y
			if right {
				cx += w
			} else {
				cy += h
			}
			cw, ch, ok := place(c, cx, cy, !known)
			if right {
				w, h = w+cw, max(h, ch)
			} else {
				w, h = max(w, cw), h+ch
			}
			known = known && ok
		}
		for _, at := range ends {
			d := Divider{Rect: screen.Rect{X: at, Y: y, W: 1, H: h}, Vertical: true}
			if !right {
				d = Divider{Rect: screen.Rect{X: x, Y: at, W: w, H: 1}}
			}
			if d.Rect = clip(d.Rect, area); d.Rect.W > 0 && d.Rect.H > 0 {
				l.Dividers = append(l.Dividers, d)
				placed = append(placed, d.Rect)
			}
		}
		return w, h, known
	}
	if len(s.order) > 0 {
		place(s.tree, area.X, area.Y, false)
	}
	l.Pad = uncovered(area, placed)
	return l
}

// clip is the part of r inside area; an r wholly outside is empty and lies
// at the area's origin.
func clip(r, area screen.Rect) screen.Rect {
	x0, y0 := max(r.X, area.X), max(r.Y, area.Y)
	x1, y1 := min(r.X+r.W, area.X+area.W), min(r.Y+r.H, area.Y+area.H)
	if x1 <= x0 || y1 <= y0 {
		return screen.Rect{X: area.X, Y: area.Y}
	}
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// uncovered returns the cells of area that no rectangle of rs covers, as
// rectangles: the area is cut into bands at every top and bottom edge, each
// band into runs at every left and right edge, and the free runs of a band
// are joined when they touch. rs lie inside area and do not overlap.
func uncovered(area screen.Rect, rs []screen.Rect) []screen.Rect {
	if area.W == 0 || area.H == 0 {
		return nil
	}
	ys := []int{area.Y, area.Y + area.H}
	xs := []int{area.X, area.X + area.W}
	for _, r := range rs {
		if r.W == 0 || r.H == 0 {
			continue
		}
		ys = append(ys, r.Y, r.Y+r.H)
		xs = append(xs, r.X, r.X+r.W)
	}
	slices.Sort(ys)
	ys = slices.Compact(ys)
	slices.Sort(xs)
	xs = slices.Compact(xs)
	covered := func(x, y int) bool {
		for _, r := range rs {
			if r.Contains(x, y) {
				return true
			}
		}
		return false
	}
	var out []screen.Rect
	for i := 0; i+1 < len(ys); i++ {
		y0, y1 := ys[i], ys[i+1]
		run := -1 // start of the free run, -1 when none is open
		for j := 0; j+1 < len(xs); j++ {
			free := !covered(xs[j], y0)
			switch {
			case free && run < 0:
				run = xs[j]
			case !free && run >= 0:
				out = append(out, screen.Rect{X: run, Y: y0, W: xs[j] - run, H: y1 - y0})
				run = -1
			}
		}
		if run >= 0 {
			out = append(out, screen.Rect{X: run, Y: y0, W: xs[len(xs)-1] - run, H: y1 - y0})
		}
	}
	return out
}

// Hit returns the pane under cell x, y of the canvas and the cell's
// coordinates inside that pane. The divider, the pad and every cell outside
// the area are no pane.
func (l Layout) Hit(x, y int) (pane string, px, py int, ok bool) {
	for id, r := range l.Panes {
		if r.Contains(x, y) {
			return id, x - r.X, y - r.Y, true
		}
	}
	return "", 0, 0, false
}

// Draw copies the panes onto c at the rectangles of l, clipped to them:
// every row when all is set (the layout, the mode, the overlay or the
// window size changed, 3.2.4), else only the rows that changed since the
// last Draw. It draws pane cells only; the divider and the pad are the
// chrome's (Chrome.Divider, Chrome.Pad).
func (s *Set) Draw(c *screen.Canvas, l Layout, all bool) {
	for _, id := range s.order {
		r, ok := l.Panes[id]
		sc := s.screens[id]
		if !ok || sc == nil || r.W == 0 || r.H == 0 {
			continue
		}
		sc.draw(c.Sub(r), all)
	}
}
