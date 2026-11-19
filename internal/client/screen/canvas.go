package screen

// The canvas of 5.3.5: one grid of cells per client, drawn on in painter's
// order. A Sub canvas shares the cells of its parent. Every write goes
// through put, which keeps the one invariant of the grid: a cell of width 2
// is always followed by its second cell (S "", W 0) and a second cell is
// always preceded by its first. A write that cuts a wide character turns
// the half that is left into a blank of the same style.

// blank is the cell Clear writes: a space in the default style.
var blank = Cell{S: " ", W: 1}

// Contains reports whether the cell x, y lies inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// buffer is the grid a canvas and all its Sub canvases share.
type buffer struct {
	w, h  int
	cells []Cell // row-major, w*h
	rows  []row  // one per row: the encoded form and whether it is stale

	encodes int // rows encoded since the buffer was made; read by tests
}

// row is the cache of one encoded row.
type row struct {
	dirty bool
	enc   string
}

// Canvas is a grid of cells. There is one per client and no layer tree.
// A canvas returned by Sub is a window on the cells of its parent.
type Canvas struct {
	b      *buffer
	root   bool
	ox, oy int // where cell 0, 0 of this canvas is in the buffer
	w, h   int // the size of this canvas; cells outside clip are not drawn
	// clip is the part of the buffer this canvas may write, in buffer
	// coordinates: x0, y0 inclusive, x1, y1 exclusive.
	clip [4]int
	// pclip is the clip of the parent, so a Resize of a Sub stays inside it.
	pclip [4]int
}

// New returns a canvas of w by h blank cells. A negative size is 0.
func New(w, h int) *Canvas {
	w, h = max(w, 0), max(h, 0)
	b := &buffer{}
	b.alloc(w, h)
	c := &Canvas{b: b, root: true}
	c.setRootBounds()
	return c
}

func (b *buffer) alloc(w, h int) {
	b.w, b.h = w, h
	b.cells = make([]Cell, w*h)
	for i := range b.cells {
		b.cells[i] = blank
	}
	b.rows = make([]row, h)
	for i := range b.rows {
		b.rows[i].dirty = true
	}
}

func (c *Canvas) setRootBounds() {
	c.ox, c.oy = 0, 0
	c.w, c.h = c.b.w, c.b.h
	c.clip = [4]int{0, 0, c.b.w, c.b.h}
	c.pclip = c.clip
}

// Resize changes the size of the canvas and keeps the cells of the top-left
// part that both sizes share; new cells are blank. A wide character the new
// right edge cuts becomes a blank. The Resize of a Sub canvas changes only
// the window, which stays inside the parent's.
func (c *Canvas) Resize(w, h int) {
	w, h = max(w, 0), max(h, 0)
	if !c.root {
		c.w, c.h = w, h
		c.clip = intersect(c.pclip, [4]int{c.ox, c.oy, c.ox + w, c.oy + h})
		return
	}
	old := *c.b
	c.b.alloc(w, h)
	c.b.encodes = old.encodes
	cw, ch := min(w, old.w), min(h, old.h)
	for y := range ch {
		copy(c.b.cells[y*w:y*w+cw], old.cells[y*old.w:y*old.w+cw])
		if cw > 0 && cw < old.w {
			// the cell after the kept part was the second half of the
			// last kept cell: that wide character is cut.
			if last := &c.b.cells[y*w+cw-1]; last.W == 2 {
				*last = Cell{S: " ", W: 1, Style: last.Style}
			}
		}
	}
	c.setRootBounds()
}

// Size returns the size of the canvas.
func (c *Canvas) Size() (w, h int) { return c.w, c.h }

// Clear blanks the canvas with the default style.
func (c *Canvas) Clear() { c.Fill(Rect{0, 0, c.w, c.h}, blank) }

// bounds is the clip of c inside the current buffer.
func (c *Canvas) bounds() [4]int {
	return intersect(c.clip, [4]int{0, 0, c.b.w, c.b.h})
}

func intersect(a, b [4]int) [4]int {
	r := [4]int{max(a[0], b[0]), max(a[1], b[1]), min(a[2], b[2]), min(a[3], b[3])}
	if r[2] < r[0] {
		r[2] = r[0]
	}
	if r[3] < r[1] {
		r[3] = r[1]
	}
	return r
}

// Set writes one cell; outside the canvas it is a no-op. It repairs a wide
// character it cuts: the half that is left becomes a blank of its style. A
// cell of width 2 that does not fit before the right edge is written as a
// blank of its style; a cell of width 0 (a second half on its own) is
// written as a blank; an empty S is a space.
func (c *Canvas) Set(x, y int, cell Cell) {
	bd := c.bounds()
	gx, gy := c.ox+x, c.oy+y
	if x < 0 || y < 0 || x >= c.w || y >= c.h || gx < bd[0] || gx >= bd[2] || gy < bd[1] || gy >= bd[3] {
		return
	}
	c.b.put(gx, gy, bd[2], cell)
}

// put writes cell at gx, gy of the buffer; limit is the first column the
// writing canvas may not use.
func (b *buffer) put(gx, gy, limit int, cell Cell) {
	switch {
	case cell.W == 0:
		cell = Cell{S: " ", W: 1, Style: cell.Style}
	case cell.W > 2:
		cell.W = 2
	}
	if cell.S == "" {
		cell = Cell{S: " ", W: 1, Style: cell.Style}
	}
	if cell.W == 2 && gx+1 >= limit {
		cell = Cell{S: " ", W: 1, Style: cell.Style}
	}
	b.unhook(gx, gy)
	if cell.W == 2 {
		b.unhook(gx+1, gy)
	}
	b.write(gx, gy, cell)
	if cell.W == 2 {
		b.write(gx+1, gy, Cell{S: "", W: 0, Style: cell.Style})
	}
}

// unhook prepares cell gx, gy for being overwritten: the other half of a
// wide character it belongs to becomes a blank of that character's style.
func (b *buffer) unhook(gx, gy int) {
	if gx >= b.w {
		return
	}
	cur := b.cells[gy*b.w+gx]
	switch {
	case cur.W == 0 && gx > 0:
		if lead := b.cells[gy*b.w+gx-1]; lead.W == 2 {
			b.write(gx-1, gy, Cell{S: " ", W: 1, Style: lead.Style})
		}
	case cur.W == 2 && gx+1 < b.w:
		if next := b.cells[gy*b.w+gx+1]; next.W == 0 {
			b.write(gx+1, gy, Cell{S: " ", W: 1, Style: next.Style})
		}
	}
}

// write stores one cell and marks its row stale when it changed.
func (b *buffer) write(gx, gy int, cell Cell) {
	p := &b.cells[gy*b.w+gx]
	if *p == cell {
		return
	}
	*p = cell
	b.rows[gy].dirty = true
}

// At returns the cell at x, y; outside the canvas the zero Cell.
func (c *Canvas) At(x, y int) Cell {
	bd := c.bounds()
	gx, gy := c.ox+x, c.oy+y
	if x < 0 || y < 0 || x >= c.w || y >= c.h || gx < bd[0] || gx >= bd[2] || gy < bd[1] || gy >= bd[3] {
		return Cell{}
	}
	return c.b.cells[gy*c.b.w+gx]
}

// Fill writes cell into every cell of r that lies inside the canvas. A
// wide cell is written into every second column.
func (c *Canvas) Fill(r Rect, cell Cell) {
	step := 1
	if cell.W == 2 {
		step = 2
	}
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, c.w), min(r.Y+r.H, c.h)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x += step {
			c.Set(x, y, cell)
		}
	}
}

// Cells writes one row of cells starting at x, y, clipped at the right
// edge. The second cell of a wide character in cells is skipped, because
// writing its first cell writes both; a second cell on its own is a blank.
func (c *Canvas) Cells(x, y int, cells []Cell) {
	col := x
	for i, cl := range cells {
		if col >= c.w {
			return
		}
		if cl.W == 0 && i > 0 && cells[i-1].W >= 2 {
			continue
		}
		c.Set(col, y, cl)
		if cl.W >= 2 {
			col += 2
		} else {
			col++
		}
	}
}

// Restyle changes the style of every cell in r (selection, stale panes).
// The text stays as it is.
func (c *Canvas) Restyle(r Rect, f func(Style) Style) {
	bd := c.bounds()
	x0, y0 := max(r.X, 0), max(r.Y, 0)
	x1, y1 := min(r.X+r.W, c.w), min(r.Y+r.H, c.h)
	for y := y0; y < y1; y++ {
		gy := c.oy + y
		if gy < bd[1] || gy >= bd[3] {
			continue
		}
		for x := x0; x < x1; x++ {
			gx := c.ox + x
			if gx < bd[0] || gx >= bd[2] {
				continue
			}
			cell := c.b.cells[gy*c.b.w+gx]
			cell.Style = f(cell.Style)
			c.b.write(gx, gy, cell)
		}
	}
}

// Sub returns a canvas that shares the cells of c inside r: its cell 0, 0
// is cell r.X, r.Y of c, its size is r.W by r.H, and it writes only the
// part of r that lies inside c.
func (c *Canvas) Sub(r Rect) *Canvas {
	w, h := max(r.W, 0), max(r.H, 0)
	ox, oy := c.ox+r.X, c.oy+r.Y
	pclip := intersect(c.clip, [4]int{c.ox, c.oy, c.ox + c.w, c.oy + c.h})
	return &Canvas{
		b:     c.b,
		ox:    ox,
		oy:    oy,
		w:     w,
		h:     h,
		clip:  intersect(pclip, [4]int{ox, oy, ox + w, oy + h}),
		pclip: pclip,
	}
}
