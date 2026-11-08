package cli

import (
	"io"
	"os"
	"strings"

	"devtools/internal/discovery"
	"devtools/internal/ui"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
)

// isTerminal reports whether w is a terminal, where output is for reading
// rather than for a script.
func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(file.Fd())
}

// colorWriter strips the colours from output that is not a terminal, or
// when NO_COLOR asks for none.
func colorWriter(w io.Writer) io.Writer {
	return colorprofile.NewWriter(w, os.Environ())
}

// treeRow is a line of the grouped lists: a project, which stands for its
// main worktree, a feature indented under it, or a heading for a project
// without a main worktree.
type treeRow struct {
	target  discovery.Target
	heading bool
	child   bool
}

func (r treeRow) name() string {
	switch {
	case r.child:
		return "  " + r.target.Worktree
	case r.heading:
		return r.target.Project
	case r.target.Kind == "worktree":
		return r.target.Project
	}
	return r.target.Label
}

// treeRows lays targets out the way the switcher does: one row per project,
// its features below it. targets are sorted by label.
func treeRows(targets []discovery.Target) []treeRow {
	var rows []treeRow
	for i := 0; i < len(targets); {
		target := targets[i]
		if target.Kind != "worktree" {
			rows = append(rows, treeRow{target: target})
			i++
			continue
		}
		j := i
		main := -1
		for j < len(targets) && targets[j].Kind == "worktree" && targets[j].Project == target.Project {
			if main < 0 && (targets[j].Worktree == "main" || targets[j].Worktree == "master") {
				main = j
			}
			j++
		}
		if main >= 0 {
			rows = append(rows, treeRow{target: targets[main]})
		} else {
			rows = append(rows, treeRow{target: target, heading: true})
		}
		for k := i; k < j; k++ {
			if k != main {
				rows = append(rows, treeRow{target: targets[k], child: true})
			}
		}
		i = j
	}
	return rows
}

// cell is a column of a tree row.
type cell struct {
	text  string
	style lipgloss.Style
}

// printTree writes the rows with their columns aligned, the row you are in
// marked, projects in the strong ink, and empty columns left out.
func printTree(w io.Writer, rows []treeRow, here string, cells func(treeRow) []cell) error {
	nameWidth := 0
	var table [][]cell
	var widths []int
	for _, row := range rows {
		nameWidth = max(nameWidth, lipgloss.Width(row.name()))
		var rowCells []cell
		if !row.heading {
			rowCells = cells(row)
		}
		for i, c := range rowCells {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], lipgloss.Width(c.text))
		}
		table = append(table, rowCells)
	}
	out := colorWriter(w)
	nameStyle := lipgloss.NewStyle().Foreground(ui.Strong)
	for i, row := range rows {
		var b strings.Builder
		if here != "" && !row.heading && row.target.Path == here {
			b.WriteString(ui.AccentText.Render(ui.HereMark) + " ")
		} else {
			b.WriteString("  ")
		}
		style := nameStyle
		if row.child {
			style = ui.Plain
		}
		b.WriteString(style.Render(row.name()))
		b.WriteString(strings.Repeat(" ", nameWidth-lipgloss.Width(row.name())))
		last := len(widths) - 1
		for last >= 0 && widths[last] == 0 {
			last--
		}
		for j := 0; j <= last; j++ {
			if widths[j] == 0 {
				continue
			}
			c := cell{}
			if j < len(table[i]) {
				c = table[i][j]
			}
			b.WriteString("   ")
			if c.text != "" {
				b.WriteString(c.style.Render(c.text))
			}
			if j < last {
				b.WriteString(strings.Repeat(" ", widths[j]-lipgloss.Width(c.text)))
			}
		}
		if _, err := io.WriteString(out, strings.TrimRight(b.String(), " ")+"\n"); err != nil {
			return err
		}
	}
	return nil
}
