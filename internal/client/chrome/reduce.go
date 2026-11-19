package chrome

import "devtools/internal/ui"

// The reduction of the status row (5a.6.2) and the cut rules of the rows a
// mode draws on its own (5a.6.3), as data: the order of the thirteen steps
// is the table reduction, and the order of every mode's cuts is the table
// modeCuts. reduce and cutRow only walk the tables; what a step removes is
// read by the builders of status.go from the plan or the modeRow.

// plan is what the row of the address shape shows after reduction: the
// normal row, the prefix row before its hints (the block counted as the
// address), and the right group the switching row keeps.
type plan struct {
	steps int // the last step of reduction applied; 0 none

	urgent   bool // "in {place}" after the count of the most urgent agent
	date     bool
	words    bool // the words of the roll-up
	online   bool // names of online machines in the fleet strip
	clock    bool
	labels   bool // labels of tabs other than the current one
	counts   bool // the working and done counts
	reasons  bool // reason words in the fleet strip
	local    bool // this machine's name
	others   bool // tabs other than the current one; false draws "+{n}"
	fleet    bool // the fleet strip
	plus     bool // "+{n}"
	blocked  bool // the blocked count; only the switching row removes it
	session  int  // cells of the session label: -1 whole (at most 32), 0 removed
	current  int  // cells of the current tab's label: -1 whole (at most 16)
	chip     int  // cells of the chip's name: -1 whole; never below 1
	notice   int  // the notice: 0 whole, 1 cut at the end keeping noticeFloor cells, 2 cut to what is left
	blockSep bool // the prefix: sep, not gap, after the block
}

// fullPlan shows everything: the row at 200 columns.
func fullPlan() plan {
	return plan{
		urgent: true, date: true, words: true, online: true, clock: true,
		labels: true, counts: true, reasons: true, local: true, others: true,
		fleet: true, plus: true, blocked: true,
		session: -1, current: -1, chip: -1,
	}
}

// step is one row of the table of 5a.6.2. apply makes one change to the
// plan and reports whether the step can shrink further: the steps that cut
// (9, 11, 13) are applied cell by cell until the row fits, every other step
// is all or nothing.
type step struct {
	n     int
	what  string // the words of 5a.6.2
	right bool   // it changes the right group only
	apply func(p *plan, s State) (more bool)
}

// sessionShort is the width step 9 cuts the session label to first.
const sessionShort = 12

// reduction is the table of 5a.6.2, in its order.
var reduction = []step{
	{1, "the date", true, func(p *plan, _ State) bool { p.date = false; return false }},
	{2, "the words of the roll-up", true, func(p *plan, _ State) bool { p.words = false; return false }},
	{3, "names of online machines in the fleet strip", true, func(p *plan, _ State) bool { p.online = false; return false }},
	{4, "the clock", true, func(p *plan, _ State) bool { p.clock = false; return false }},
	{5, "labels of tabs other than the current one", false, func(p *plan, _ State) bool { p.labels = false; return false }},
	{6, "the working and done counts; reason words in the fleet strip", true, func(p *plan, _ State) bool {
		p.counts, p.reasons = false, false
		return false
	}},
	{7, "this machine's name", false, func(p *plan, _ State) bool { p.local = false; return false }},
	{8, "tabs other than the current one, replaced by +{n}", false, func(p *plan, _ State) bool { p.others = false; return false }},
	{9, "the session label: cut to 12 cells, then removed", false, func(p *plan, s State) bool {
		if p.session < 0 && ui.Width(sessionLabel(s)) > sessionShort {
			p.session = sessionShort
			return true
		}
		p.session = 0
		return false
	}},
	{10, "the fleet strip", true, func(p *plan, _ State) bool { p.fleet = false; return false }},
	{11, "the current tab's label, cut at the end, down to nothing", false, func(p *plan, s State) bool {
		if p.current < 0 {
			p.current = ui.Width(currentLabel(s))
		}
		if p.current > 0 {
			p.current--
		}
		return p.current > 0
	}},
	{12, "+{n}", false, func(p *plan, _ State) bool { p.plus = false; return false }},
	{13, "the chip's name, cut at the end until the row fits; its first letter always stays", false, func(p *plan, s State) bool {
		if s.Machine == "" {
			return false
		}
		if p.chip < 0 {
			p.chip = ui.Width(s.Machine)
		}
		if p.chip > 1 {
			p.chip--
		}
		return p.chip > 1
	}},
}

// The steps a row adds to the table of 5a.6.2. They have no number there
// and are numbered below zero here; plan.steps counts only the table.
const (
	stepNoticeCut = -1 // a notice: the text cut at the end, noticeFloor cells kept
	stepNoticeAll = -2 // a notice: the text cut to what is left
	stepBlockSep  = -3 // the prefix: the space after the block shrinks to sep
)

// extraSteps are the steps of stepNoticeCut, stepNoticeAll and
// stepBlockSep.
var extraSteps = map[int]step{
	stepNoticeCut: {stepNoticeCut, "the notice, cut at the end", false, func(p *plan, _ State) bool { p.notice = 1; return false }},
	stepNoticeAll: {stepNoticeAll, "the notice, cut to what is left", false, func(p *plan, _ State) bool { p.notice = 2; return false }},
	stepBlockSep:  {stepBlockSep, "the space after the prefix block shrinks to sep", false, func(p *plan, _ State) bool { p.blockSep = true; return false }},
}

// noticeOrder is the reduction of a row whose tabs a notice replaced: the
// right group through step 6 (step 5 has no tabs to act on), then the text
// is cut (5a.6.3). The address stays as long as the text keeps noticeFloor
// cells; only then the steps of the address follow, and at last the text
// gives up its floor. The floor and what follows the cut are not in
// 5a.6.3, which describes the row down to 80 columns; they keep a notice
// readable down to the minimum width.
var noticeOrder = []int{1, 2, 3, 4, 6, stepNoticeCut, 7, 9, 10, 13, stepNoticeAll}

// noticeFloor is the least a cut notice keeps before the address gives way.
const noticeFloor = 12

// prefixOrder is the reduction of the prefix row before its hints: the
// table, the block counted as the address (5a.6.3). At the minimum width a
// block over an address that is gone is wider than the address was, so
// the space after it shrinks to sep as a last step; the tabs then move,
// which they otherwise never do.
var prefixOrder = []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, stepBlockSep}

// stepByNumber returns the row n of reduction, or an extra step.
func stepByNumber(n int) step {
	if n < 0 {
		return extraSteps[n]
	}
	return reduction[n-1]
}

// reduce applies the steps of 5a.6.2 in order until the row of s fits in
// width cells: first the place of the most urgent agent leaves, then step
// after step of reduction. The row is the normal row, or the prefix row
// before its hints when s.Mode is "prefix" (prefixOrder). With a notice in
// the normal row the order is noticeOrder. When nothing fits the plan of
// the last step is returned and the canvas clips at the edge.
func reduce(s State, width int) plan {
	p := fullPlan()
	if addressNeed(s, p, width) <= width {
		return p
	}
	p.urgent = false
	if addressNeed(s, p, width) <= width {
		return p
	}
	order := prefixOrder[:len(reduction)]
	switch {
	case s.Mode == "prefix":
		order = prefixOrder
	case showsNotice(s):
		order = noticeOrder
	}
	for _, n := range order {
		st := stepByNumber(n)
		for {
			more := st.apply(&p, s)
			if st.n > 0 {
				p.steps = st.n
			}
			if addressNeed(s, p, width) <= width {
				return p
			}
			if !more {
				break
			}
		}
	}
	return p
}

// modeRow is what a row that a mode draws on its own shows after its cuts
// (5a.6.3). Each mode reads the fields of its own cuts.
type modeRow struct {
	plan plan // switching: the right group, as the normal row has it

	to       bool // switching: the word "to"
	machine  bool // switching: the machine's name
	session  int  // switching: cells of "{session}…", -1 whole
	position int  // copy, scrolled: 0 "line {n} of {total}", 1 "line {n}", 2 "{n}"
	hints    int  // copy: hints left out from the left
	phrase   bool // copy: the selecting phrase
	anyKey   bool // scrolled: "any key returns"
	notice   int  // scrolled: cells of the notice, -1 whole
	question int  // confirm: 0 the question, 1 "Close {program}?", 2 "Close?"
	sep      bool // confirm: the gaps shrunk to sep
	labels   bool // confirm: the labels of the two hints
	program  int  // confirm: cells of the program's name, -1 whole
	label    int  // prefix with hints: cells of the hint labels of the page, -1 whole
	more     bool // prefix with hints: the label of "? more"
}

// fullRow is a mode row before any cut.
func fullRow(p plan) modeRow {
	return modeRow{
		plan: p, to: true, machine: true, session: -1, phrase: true, anyKey: true,
		notice: -1, labels: true, program: -1, label: -1, more: true,
	}
}

// cut is one row of the cut rules of a mode. Like a step it makes one
// change and reports whether it can shrink further.
type cut struct {
	what  string // the words of 5a.6.3
	apply func(r *modeRow, s State) (more bool)
}

// copyHints is the number of hints at the right end of the copy row.
const copyHints = 3

// modeCuts is the second table of 5a.6.3: per mode, what leaves or shrinks,
// in order, when the mode's row does not fit. The prefix before its hints
// is reduced by the table of 5a.6.2 (reduce), the block counted as the
// address.
var modeCuts = map[string][]cut{
	"prefix": { // with hints: a page holds at least one hint, and "? more" stays
		{"the label of the page's hint, cut at the end", func(r *modeRow, s State) bool {
			if r.label < 0 {
				r.label = longestHintLabel()
			}
			if r.label > 0 {
				r.label--
			}
			return r.label > 0
		}},
		{"the label of ? more", func(r *modeRow, _ State) bool { r.more = false; return false }},
	},
	"switching": {
		{"the right group by its steps down to the blocked count", func(r *modeRow, s State) bool {
			for _, st := range reduction {
				if st.right && st.n > r.plan.steps {
					st.apply(&r.plan, s)
					r.plan.steps = st.n
					r.plan.urgent = false
					return nextRightStep(r.plan.steps)
				}
			}
			r.plan.urgent = false
			return false
		}},
		{"to", func(r *modeRow, _ State) bool { r.to = false; return false }},
		{"the blocked count", func(r *modeRow, _ State) bool { r.plan.blocked = false; return false }},
		{"the machine name", func(r *modeRow, _ State) bool { r.machine = false; return false }},
		{"the session label, cut at the end", func(r *modeRow, s State) bool {
			if r.session < 0 {
				r.session = ui.Width(switchingSession(s))
			}
			if r.session > 1 {
				r.session--
			}
			return r.session > 1
		}},
	},
	"copy": {
		{"the hints from the left", func(r *modeRow, _ State) bool {
			if r.hints < copyHints {
				r.hints++
			}
			return r.hints < copyHints
		}},
		{"the selecting phrase", func(r *modeRow, _ State) bool { r.phrase = false; return false }},
		{"of {total}", func(r *modeRow, _ State) bool { r.position = 1; return false }},
		{"line", func(r *modeRow, _ State) bool { r.position = 2; return false }},
	},
	"scrolled": {
		{"any key returns", func(r *modeRow, _ State) bool { r.anyKey = false; return false }},
		// A notice is shown in the scrolled view too (5a.10.1); 5a.6.3 has
		// no rule for it, so it is cut after the hint, before the position.
		{"the notice, cut at the end", func(r *modeRow, s State) bool {
			if r.notice < 0 {
				r.notice = ui.Width(s.Notice)
			}
			if r.notice > 0 {
				r.notice--
			}
			return r.notice > 0
		}},
		{"of {total}", func(r *modeRow, _ State) bool { r.position = 1; return false }},
		{"line", func(r *modeRow, _ State) bool { r.position = 2; return false }},
	},
	"confirm": {
		{"the question becomes Close {program}?", func(r *modeRow, _ State) bool { r.question = 1; return false }},
		{"the gaps shrink to sep", func(r *modeRow, _ State) bool { r.sep = true; return false }},
		{"the hint labels (the keys stay)", func(r *modeRow, _ State) bool { r.labels = false; return false }},
		{"the program name, cut at the end with …, then dropped", func(r *modeRow, s State) bool {
			if r.program < 0 {
				r.program = ui.Width(confirmProgram(s))
			}
			if r.program > 0 {
				r.program--
			}
			if r.program == 0 {
				r.question = 2
				return false
			}
			return true
		}},
	},
}

// nextRightStep reports whether a step of the right group follows step n.
func nextRightStep(n int) bool {
	for _, st := range reduction {
		if st.right && st.n > n {
			return true
		}
	}
	return false
}

// cutRow applies the cuts of mode to r until need reports that the row fits
// in width cells, and returns the row as cut.
func cutRow(mode string, s State, r modeRow, width int, need func(modeRow) int) modeRow {
	if need(r) <= width {
		return r
	}
	for _, c := range modeCuts[mode] {
		for {
			more := c.apply(&r, s)
			if need(r) <= width {
				return r
			}
			if !more {
				break
			}
		}
	}
	return r
}
