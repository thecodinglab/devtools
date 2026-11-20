package keys

// The binding table of the devtools window (5a.11.2) and its start errors.

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"devtools/internal/config"
	"devtools/internal/proto"
)

// binding is one row of the table.
type binding struct {
	chord  chord
	action Action
	line   int // of the bind line; 0 for a default
}

// Table is the binding table: the defaults of 5a.11.2 with the `unbind`
// and `bind` lines of the config applied. A Table is not changed after New
// returns it, so the client and the look may read it from any goroutine.
type Table struct {
	rows []binding // in the order of 5a.11.2, then the order of the bind lines
}

// defaults is the table of 5a.11.2, in its order. `hub new` has no default
// chord (F5); `esc` and `?` after the prefix are built in and not rows.
var defaults = []struct{ chord, action, arg string }{
	{"ctrl+space", "prefix", ""},
	{"prefix+o", "hub", ""},
	{"prefix+s", "hub", "sessions"},
	{"prefix+a", "hub", "agents"},
	{"prefix+c", "tab-new", ""},
	{"prefix+n", "tab-next", ""},
	{"prefix+p", "tab-prev", ""},
	{"prefix+1", "tab-select", "1"},
	{"prefix+2", "tab-select", "2"},
	{"prefix+3", "tab-select", "3"},
	{"prefix+4", "tab-select", "4"},
	{"prefix+5", "tab-select", "5"},
	{"prefix+6", "tab-select", "6"},
	{"prefix+7", "tab-select", "7"},
	{"prefix+8", "tab-select", "8"},
	{"prefix+9", "tab-select", "9"},
	{"prefix+%", "split-right", ""},
	{"prefix+v", "split-right", ""},
	{`prefix+"`, "split-down", ""},
	{"prefix+-", "split-down", ""},
	{"prefix+x", "pane-close", ""},
	{"prefix+[", "copy-mode", ""},
	{"prefix+(", "session-prev", ""},
	{"prefix+)", "session-next", ""},
	{"prefix+d", "detach", ""},
	{"prefix+q", "detach", ""},
	{"ctrl+h", "focus-left", "vim"},
	{"ctrl+j", "focus-down", "vim"},
	{"ctrl+k", "focus-up", "vim"},
	{"ctrl+l", "focus-right", "vim"},
	{"prefix+H", "resize-left", "5"},
	{"prefix+J", "resize-down", "5"},
	{"prefix+K", "resize-up", "5"},
	{"prefix+L", "resize-right", "5"},
	{"prefix+ctrl+left", "resize-left", ""},
	{"prefix+ctrl+down", "resize-down", ""},
	{"prefix+ctrl+up", "resize-up", ""},
	{"prefix+ctrl+right", "resize-right", ""},
}

// vocabulary is the closed action vocabulary: every action a chord can be
// bound to, with the arguments it takes. An action that is not here (the
// DROP rows of section 2 among them: rename, zoom, last pane) is a start
// error (R10).
var vocabulary = map[string]func(arg string) bool{
	"prefix":       none,
	"hub":          oneOf("", "sessions", "agents", "new"),
	"tab-new":      none,
	"tab-next":     none,
	"tab-prev":     none,
	"tab-select":   tabNumber,
	"split-right":  none,
	"split-down":   none,
	"pane-close":   none,
	"copy-mode":    none,
	"session-prev": none,
	"session-next": none,
	"detach":       none,
	"focus-left":   oneOf("", "vim"),
	"focus-down":   oneOf("", "vim"),
	"focus-up":     oneOf("", "vim"),
	"focus-right":  oneOf("", "vim"),
	"resize-left":  cells,
	"resize-down":  cells,
	"resize-up":    cells,
	"resize-right": cells,
}

// argHelp says what each action takes, for the start error of a wrong
// argument.
var argHelp = map[string]string{
	"hub":        "no argument or one of sessions, agents, new",
	"tab-select": "a tab number from 1 to 9",
	"focus-left": "no argument or vim", "focus-down": "no argument or vim",
	"focus-up": "no argument or vim", "focus-right": "no argument or vim",
	"resize-left": cellsHelp, "resize-down": cellsHelp,
	"resize-up": cellsHelp, "resize-right": cellsHelp,
}

func none(arg string) bool { return arg == "" }

func oneOf(args ...string) func(string) bool {
	return func(arg string) bool {
		for _, a := range args {
			if a == arg {
				return true
			}
		}
		return false
	}
}

// cellsHelp says what a resize takes.
const cellsHelp = "no argument (one cell) or a number of cells from 1 to 99"

// cells is the argument of a resize: none for one cell, or 1 to 99.
func cells(arg string) bool {
	if arg == "" {
		return true
	}
	n, err := strconv.Atoi(arg)
	return err == nil && n >= 1 && n <= 99 && arg == strconv.Itoa(n)
}

func tabNumber(arg string) bool {
	n, err := strconv.Atoi(arg)
	return err == nil && n >= 1 && n <= 9 && arg == strconv.Itoa(n)
}

// focusActions are the actions the vim rule applies to (F13).
var focusActions = map[string]bool{
	"focus-left": true, "focus-down": true, "focus-up": true, "focus-right": true,
}

var defaultTable = func() *Table {
	t := &Table{}
	for _, d := range defaults {
		c, err := parseChord(d.chord)
		if err != nil {
			panic("keys: default chord " + d.chord + ": " + err.Error())
		}
		t.rows = append(t.rows, binding{chord: c, action: Action{Name: d.action, Arg: d.arg}})
	}
	if err := t.check(); err != nil {
		panic("keys: the defaults: " + err.Error())
	}
	return t
}()

// Defaults returns the table of 5a.11.2.
func Defaults() *Table { return defaultTable.clone() }

func (t *Table) clone() *Table {
	return &Table{rows: append([]binding(nil), t.rows...)}
}

// New applies unbinds, then binds, to the defaults. Later lines win. Every
// error is a start error that names the line and the chord: a chord that
// does not parse, an action outside the vocabulary or with a wrong
// argument, a binding of a built-in key (`prefix+escape`, `prefix+?`), the
// action prefix after the prefix, the prefix chord after the prefix, a
// direct binding on a plain character, a second prefix, and bindings after
// the prefix when no chord is the prefix. An `unbind` of a chord that has
// no default binding changes nothing: it may make room for a bind line of
// the other config file.
func New(binds []config.Bind, unbinds []config.Unbind) (*Table, error) {
	t := Defaults()
	for _, u := range unbinds {
		c, err := parseChord(u.Chord)
		if err != nil {
			return nil, fmt.Errorf("line %d: unbind %s: %v", u.Line, u.Chord, err)
		}
		if i := t.find(c); i >= 0 {
			t.rows = append(t.rows[:i], t.rows[i+1:]...)
		}
	}
	for _, b := range binds {
		row, err := parseBind(b)
		if err != nil {
			return nil, fmt.Errorf("line %d: bind %s: %v", b.Line, b.Chord, err)
		}
		row.line = b.Line
		if i := t.find(row.chord); i >= 0 {
			t.rows[i] = row
		} else {
			t.rows = append(t.rows, row)
		}
	}
	if err := t.check(); err != nil {
		return nil, err
	}
	return t, nil
}

// parseBind checks one bind line against the grammar, the vocabulary and
// the rules of 5a.11.1.
func parseBind(b config.Bind) (binding, error) {
	c, err := parseChord(b.Chord)
	if err != nil {
		return binding{}, err
	}
	valid, ok := vocabulary[b.Action]
	if !ok {
		return binding{}, fmt.Errorf("unknown action %q", b.Action)
	}
	if !valid(b.Arg) {
		help := argHelp[b.Action]
		if help == "" {
			help = "no argument"
		}
		return binding{}, fmt.Errorf("action %s takes %s, not %q", b.Action, help, b.Arg)
	}
	if c.prefix {
		switch {
		case c.key == "?" && c.mod&^proto.ModShift == 0:
			return binding{}, fmt.Errorf("? after the prefix is built in (more hints) and cannot be bound")
		case !c.text && c.mod == 0 && c.key == "escape":
			return binding{}, fmt.Errorf("esc after the prefix is built in (cancel) and cannot be bound")
		case b.Action == "prefix":
			return binding{}, fmt.Errorf("the prefix cannot be bound after the prefix")
		}
	} else if plain(c) {
		return binding{}, fmt.Errorf("a binding without the prefix on a plain character would take it from every program; use prefix+%s", c)
	}
	return binding{chord: c, action: Action{Name: b.Action, Arg: b.Arg}}, nil
}

// plain reports whether a chord types a character: a character with no
// modifier but shift, or space.
func plain(c chord) bool {
	if c.text {
		return true
	}
	if c.mod&^proto.ModShift != 0 {
		return false
	}
	return c.key == "space" || utf8.RuneCountInString(c.key) == 1
}

// check holds the rules that concern the table as a whole.
func (t *Table) check() error {
	p := t.prefixes()
	if len(p) > 1 {
		// the later one is a bind line: the defaults have one prefix
		return fmt.Errorf("%s: %s is already the prefix and there is one prefix; unbind %s first", p[1].where(), p[0].chord, p[0].chord)
	}
	if len(p) == 1 {
		for _, r := range t.rows {
			if r.chord.prefix && r.chord.same(p[0].chord) {
				return fmt.Errorf("%s: the prefix twice sends the prefix to the pane, so it cannot be bound after the prefix", r.where())
			}
		}
		return nil
	}
	for _, r := range t.rows {
		if r.chord.prefix {
			return fmt.Errorf("%s: no chord is bound to prefix, and this binding needs it", r.where())
		}
	}
	return nil
}

// where names a row in a start error: its bind line, or the default.
func (r binding) where() string {
	if r.line == 0 {
		return "default " + r.chord.String()
	}
	return fmt.Sprintf("line %d: bind %s", r.line, r.chord)
}

func (t *Table) prefixes() []binding {
	var p []binding
	for _, r := range t.rows {
		if r.action.Name == "prefix" {
			p = append(p, r)
		}
	}
	return p
}

// find is the index of the row with exactly this chord, prefix included;
// -1 when there is none.
func (t *Table) find(c chord) int {
	for i, r := range t.rows {
		if r.chord.prefix == c.prefix && r.chord.same(c) {
			return i
		}
	}
	return -1
}

// lookup is the action of the first row that matches the event, after the
// prefix or not.
func (t *Table) lookup(e Event, afterPrefix bool) (Action, bool) {
	for _, r := range t.rows {
		if r.chord.prefix == afterPrefix && r.chord.matches(e) {
			return r.action, true
		}
	}
	return Action{}, false
}

// isPrefix reports whether the event is the prefix chord.
func (t *Table) isPrefix(e Event) bool {
	a, ok := t.lookup(e, false)
	return ok && a.Name == "prefix"
}

// Chords returns the chords bound to action with arg, for the hint strip,
// in the order of the table: the defaults in the order of 5a.11.2, then the
// bind lines in their order. The strip shows the first. For tab-select,
// whose every binding carries a number, arg "" asks for the chords of every
// number, which is how ui.HintOrder names that hint. nil when the action
// has no chord.
func (t *Table) Chords(action, arg string) []string {
	var out []string
	for _, r := range t.rows {
		if r.action.Name != action {
			continue
		}
		if r.action.Arg == arg || (arg == "" && action == "tab-select") {
			out = append(out, r.chord.String())
		}
	}
	return out
}

// Bindings returns every row of the table, in the order of Chords: chord,
// action, argument. It is what `devtools keys` prints for the client
// (4.5).
func (t *Table) Bindings() [][3]string {
	out := make([][3]string, len(t.rows))
	for i, r := range t.rows {
		out[i] = [3]string{r.chord.String(), r.action.Name, r.action.Arg}
	}
	return out
}
