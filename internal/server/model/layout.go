package model

import (
	"slices"

	"devtools/internal/proto"
)

// MinPaneCols and MinPaneRows are the smallest pane (3.3).
const MinPaneCols, MinPaneRows = 4, 2

// Rect is a pane's rectangle inside the pane area, in cells.
type Rect struct{ X, Y, W, H int }

// Layout returns the rectangles of the leaves of a tab's layout tree inside
// cols x rows, in the order of the leaves (the tab's panes). A split shares
// its width (SplitRight) or height (SplitDown), less one divider cell
// between two of its nodes, in proportion to the nodes' weights; the cells
// a share leaves over go to the first nodes, one each. A node never gets
// less than its minimum (minExtent) while another node has more than its
// own: the cells come from the node with the most to spare. A rectangle is
// never smaller than MinPaneCols by MinPaneRows, also when the area is:
// such a pane is cut by the client, never smaller than the minimum.
func Layout(cols, rows int, tree proto.Node) []Rect {
	var rs []Rect
	walk(&tree, Rect{0, 0, cols, rows}, func(n *proto.Node, r Rect) {
		if n.Leaf() {
			r.W = max(r.W, MinPaneCols)
			r.H = max(r.H, MinPaneRows)
			rs = append(rs, r)
		}
	})
	return rs
}

// walk visits n and every node below it, depth first, each with its
// rectangle when n lies at r.
func walk(n *proto.Node, r Rect, f func(*proto.Node, Rect)) {
	f(n, r)
	if n.Leaf() {
		return
	}
	right := n.Split == proto.SplitRight
	ext := r.H
	if right {
		ext = r.W
	}
	at := 0
	for i, size := range shares(n, ext) {
		c := Rect{r.X, r.Y + at, r.W, size}
		if right {
			c = Rect{r.X + at, r.Y, size, r.H}
		}
		walk(&n.Nodes[i], c, f)
		at += size + 1
	}
}

// weight is the weight a node counts with.
func weight(n *proto.Node) int {
	return min(max(n.Weight, 1), proto.MaxWeight)
}

// shares divides ext cells of a split, its dividers included, among its
// nodes.
func shares(n *proto.Node, ext int) []int {
	k := len(n.Nodes)
	avail := max(ext-(k-1), 0)
	right := n.Split == proto.SplitRight
	sizes := make([]int, k)
	sum := 0
	for i := range n.Nodes {
		sum += weight(&n.Nodes[i])
	}
	used := 0
	for i := range n.Nodes {
		sizes[i] = avail * weight(&n.Nodes[i]) / sum
		used += sizes[i]
	}
	for i := 0; used < avail; i = (i + 1) % k {
		sizes[i]++
		used++
	}
	for i := range n.Nodes {
		low := minExtent(&n.Nodes[i], right)
		for sizes[i] < low {
			// take from the node with the most to spare
			from, spare := -1, 0
			for j := range n.Nodes {
				if s := sizes[j] - minExtent(&n.Nodes[j], right); j != i && s > spare {
					from, spare = j, s
				}
			}
			if from < 0 {
				sizes[i] = low // the area is too small: the client cuts
				break
			}
			d := min(spare, low-sizes[i])
			sizes[from] -= d
			sizes[i] += d
		}
	}
	return sizes
}

// minExtent is the smallest width (across) or height of a node: a pane's
// minimum, and for a split the sum of its nodes' along its direction with
// the dividers, the largest of them across it.
func minExtent(n *proto.Node, across bool) int {
	if n.Leaf() {
		if across {
			return MinPaneCols
		}
		return MinPaneRows
	}
	along := (n.Split == proto.SplitRight) == across
	ext := 0
	for i := range n.Nodes {
		e := minExtent(&n.Nodes[i], across)
		if along {
			ext += e
		} else {
			ext = max(ext, e)
		}
	}
	if along {
		ext += len(n.Nodes) - 1
	}
	return ext
}

// fits reports whether every pane of the tree can have its minimum inside
// cols x rows.
func fits(tree *proto.Node, cols, rows int) bool {
	return minExtent(tree, true) <= cols && minExtent(tree, false) <= rows
}

// pin sets the weight of every node below a split to its share at cols x
// rows, so that the layout at that size stays as it is while an operation
// moves cells between nodes.
func pin(tree *proto.Node, cols, rows int) {
	walk(tree, Rect{0, 0, cols, rows}, func(n *proto.Node, r Rect) {
		if n.Leaf() {
			return
		}
		ext := r.H
		if n.Split == proto.SplitRight {
			ext = r.W
		}
		for i, size := range shares(n, ext) {
			n.Nodes[i].Weight = max(size, 1)
		}
	})
}

// rects returns the rectangle of every node of the tree at cols x rows.
func rects(tree *proto.Node, cols, rows int) map[*proto.Node]Rect {
	out := map[*proto.Node]Rect{}
	walk(tree, Rect{0, 0, cols, rows}, func(n *proto.Node, r Rect) { out[n] = r })
	return out
}

// pathTo returns the nodes from the root to the leaf of pane, nil when the
// tree has no such leaf.
func pathTo(tree *proto.Node, pane string) []*proto.Node {
	if tree.Leaf() {
		if tree.Pane == pane {
			return []*proto.Node{tree}
		}
		return nil
	}
	for i := range tree.Nodes {
		if p := pathTo(&tree.Nodes[i], pane); p != nil {
			return append([]*proto.Node{tree}, p...)
		}
	}
	return nil
}

// indexOf is the place of child among the nodes of parent.
func indexOf(parent, child *proto.Node) int {
	for i := range parent.Nodes {
		if &parent.Nodes[i] == child {
			return i
		}
	}
	return -1
}

// axis is a direction of focus and resize: which split it crosses and
// which way along it.
type axis struct {
	split   string // proto.SplitRight for left and right, proto.SplitDown for up and down
	forward bool   // right or down
}

func axisOf(direction string) (axis, bool) {
	switch direction {
	case "left":
		return axis{proto.SplitRight, false}, true
	case "right":
		return axis{proto.SplitRight, true}, true
	case "up":
		return axis{proto.SplitDown, false}, true
	case "down":
		return axis{proto.SplitDown, true}, true
	}
	return axis{}, false
}

// neighbour finds, on the path to a leaf, the closest split along a whose
// node on the path has a node next to it in direction forward: the split,
// the index of the node on the path and of that neighbour. ok is false when
// the leaf lies at that edge of the tab.
func neighbour(path []*proto.Node, a axis, forward bool) (split *proto.Node, at, next int, ok bool) {
	for j := len(path) - 2; j >= 0; j-- {
		p := path[j]
		if p.Split != a.split {
			continue
		}
		i := indexOf(p, path[j+1])
		n := i - 1
		if forward {
			n = i + 1
		}
		if n >= 0 && n < len(p.Nodes) {
			return p, i, n, true
		}
	}
	return nil, 0, 0, false
}

// nearest descends from n to the leaf next to the rectangle from, coming
// from direction a: in a split along a the node on the side from lies on,
// in a split across a the node that shares the most cells with from across
// a, the first on a tie.
func nearest(n *proto.Node, from Rect, a axis, at map[*proto.Node]Rect) *proto.Node {
	for !n.Leaf() {
		if n.Split == a.split {
			if a.forward {
				n = &n.Nodes[0]
			} else {
				n = &n.Nodes[len(n.Nodes)-1]
			}
			continue
		}
		best, most := 0, -1
		for i := range n.Nodes {
			r := at[&n.Nodes[i]]
			var o int
			if a.split == proto.SplitRight {
				o = min(r.Y+r.H, from.Y+from.H) - max(r.Y, from.Y)
			} else {
				o = min(r.X+r.W, from.X+from.W) - max(r.X, from.X)
			}
			if o > most {
				best, most = i, o
			}
		}
		n = &n.Nodes[best]
	}
	return n
}

// maxShare bounds the weights a split or a close leaves; above it the
// weights of the tree are pinned to cells.
const maxShare = 1 << 16

// insertSplit splits the leaf of pane in direction: the new pane comes
// right of it or below it and the two share what the leaf had. When the
// leaf's split has that direction already the new pane joins it with half
// the leaf's weight; else the leaf becomes a split of the two with equal
// weights.
func insertSplit(tree *proto.Node, pane, id, direction string, cols, rows int) {
	path := pathTo(tree, pane)
	leaf := path[len(path)-1]
	if len(path) >= 2 {
		if parent := path[len(path)-2]; parent.Split == direction {
			i := indexOf(parent, leaf)
			w := weight(leaf)
			if w%2 == 1 {
				for j := range parent.Nodes {
					parent.Nodes[j].Weight = 2 * weight(&parent.Nodes[j])
				}
				w *= 2
			}
			parent.Nodes[i].Weight = w / 2
			parent.Nodes = slices.Insert(parent.Nodes, i+1, proto.Node{Pane: id, Weight: w / 2})
			reduce(tree, parent, cols, rows)
			return
		}
	}
	*leaf = proto.Node{Split: direction, Weight: leaf.Weight, Nodes: []proto.Node{
		{Pane: pane, Weight: 1},
		{Pane: id, Weight: 1},
	}}
}

// reduce divides the weights of split's nodes by their greatest common
// divisor; when one is still above maxShare, every weight of the tree is
// pinned to its cells at cols x rows.
func reduce(tree, split *proto.Node, cols, rows int) {
	g := 0
	for i := range split.Nodes {
		g = gcd(g, weight(&split.Nodes[i]))
	}
	big := false
	for i := range split.Nodes {
		split.Nodes[i].Weight = weight(&split.Nodes[i]) / g
		big = big || split.Nodes[i].Weight > maxShare
	}
	if big {
		pin(tree, cols, rows)
	}
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// removeLeaf takes the leaf of pane out of the tree and returns the leaf
// that takes the focus: of the node that took its space, the leaf next to
// it. The node before it in its split takes its weight (the node after it
// when it was the first). A split left with one node is replaced by that
// node; when that node is a split in the direction of the split above it,
// its nodes join that split, their weights scaled to its. The tree has
// another leaf.
func removeLeaf(tree *proto.Node, pane string, cols, rows int) string {
	at := rects(tree, cols, rows)
	path := pathTo(tree, pane)
	leaf := path[len(path)-1]
	parent := path[len(path)-2]
	i := indexOf(parent, leaf)
	nb := i - 1
	if i == 0 {
		nb = 1
	}
	focus := nearest(&parent.Nodes[nb], at[leaf], axis{split: parent.Split, forward: nb > i}, at).Pane
	parent.Nodes[nb].Weight = weight(&parent.Nodes[nb]) + weight(leaf)
	parent.Nodes = slices.Delete(parent.Nodes, i, i+1)
	reduce(tree, parent, cols, rows)
	if len(parent.Nodes) > 1 {
		return focus
	}
	only := parent.Nodes[0]
	only.Weight = parent.Weight
	*parent = only
	if len(path) < 3 {
		return focus
	}
	grand := path[len(path)-3]
	if parent.Leaf() || parent.Split != grand.Split {
		return focus
	}
	// parent's nodes share parent's weight in grand: scale both to one unit
	j := indexOf(grand, parent)
	sum := 0
	for k := range parent.Nodes {
		sum += weight(&parent.Nodes[k])
	}
	w := weight(parent)
	merged := make([]proto.Node, 0, len(grand.Nodes)+len(parent.Nodes)-1)
	for k := range grand.Nodes {
		if k != j {
			n := grand.Nodes[k]
			n.Weight = weight(&n) * sum
			merged = append(merged, n)
			continue
		}
		for _, n := range parent.Nodes {
			n.Weight = weight(&n) * w
			merged = append(merged, n)
		}
	}
	grand.Nodes = merged
	reduce(tree, grand, cols, rows)
	return focus
}
