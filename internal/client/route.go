package client

// Key routing (3.2.4), first match wins: (1) the window is too small, or
// nothing is displayed and no hub is open: only the detach and hub
// bindings act; (2) the hub overlay, but for the prefix chord and the key
// after it, of whose bindings only detach and hub act; (3) the confirm of pane-close; (4)
// copy mode (copy.go); (5) the key table acts; (6) it swallows; (7) the key
// goes to the focused pane as input.key. Then the actions of 5a.11.2.

import (
	"strconv"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/keys"
	"devtools/internal/fleet"
	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/ui"
)

// keyNames are the bubbletea keys that travel by name (4.2.6).
var keyNames = map[rune]string{
	tea.KeyEnter: "enter", tea.KeyKpEnter: "enter", tea.KeyTab: "tab", tea.KeyBackspace: "backspace",
	tea.KeyEscape: "escape", tea.KeySpace: "space",
	tea.KeyUp: "up", tea.KeyDown: "down", tea.KeyLeft: "left", tea.KeyRight: "right",
	tea.KeyHome: "home", tea.KeyEnd: "end", tea.KeyPgUp: "pageup", tea.KeyPgDown: "pagedown",
	tea.KeyInsert: "insert", tea.KeyDelete: "delete",
	tea.KeyLeftShift: "leftshift", tea.KeyRightShift: "rightshift",
	tea.KeyLeftCtrl: "leftctrl", tea.KeyRightCtrl: "rightctrl",
	tea.KeyLeftAlt: "leftalt", tea.KeyRightAlt: "rightalt",
	tea.KeyLeftSuper: "leftsuper", tea.KeyRightSuper: "rightsuper",
	tea.KeyLeftHyper: "lefthyper", tea.KeyRightHyper: "righthyper",
	tea.KeyLeftMeta: "leftmeta", tea.KeyRightMeta: "rightmeta",
	tea.KeyCapsLock: "capslock", tea.KeyNumLock: "numlock", tea.KeyScrollLock: "scrolllock",
}

// modBits maps bubbletea's modifiers to the kitty order of the wire
// (4.2.6): bubbletea keeps xterm's order, where meta and super are swapped.
var modBits = []struct {
	tea  tea.KeyMod
	wire int
}{
	{tea.ModShift, proto.ModShift}, {tea.ModAlt, proto.ModAlt}, {tea.ModCtrl, proto.ModCtrl},
	{tea.ModSuper, proto.ModSuper}, {tea.ModHyper, proto.ModHyper}, {tea.ModMeta, proto.ModMeta},
	{tea.ModCapsLock, proto.ModCaps}, {tea.ModNumLock, proto.ModNum},
}

// keyEvent turns a bubbletea key into the key table's event and the wire's
// input.key: the key without modifiers applied, kitty-order modifier bits,
// the text, and the shifted and base characters the terminal reported.
// ok is false for a key that has no wire name. The text before ctrl and
// alt is the server's to fill (internal/vt/input.go, P1-03; E3 point 2).
func keyEvent(k tea.Key, release bool) (keys.Event, proto.Key, bool) {
	mod := 0
	for _, b := range modBits {
		if k.Mod&b.tea != 0 {
			mod |= b.wire
		}
	}
	name, ok := keyNames[k.Code]
	switch {
	case ok:
	case k.Code >= tea.KeyF1 && k.Code <= tea.KeyF12:
		name = "f" + strconv.Itoa(int(k.Code-tea.KeyF1)+1)
	case k.Code >= tea.KeyExtended || k.Code < 0x20 || k.Code == 0x7f || (k.Code >= 0x80 && k.Code <= 0x9f):
		return keys.Event{}, proto.Key{}, false
	default:
		c := k.Code
		if c >= 'A' && c <= 'Z' {
			// a terminal that reports the shifted letter itself
			c += 'a' - 'A'
			mod |= proto.ModShift
		}
		name = string(c)
	}
	text := k.Text
	if !utf8.ValidString(text) {
		text = ""
	}
	ev := keys.Event{Name: name, Mod: mod, Text: text, Release: release, Repeat: k.IsRepeat}
	wire := proto.Key{Key: name, Mod: mod, Text: text, Release: release, Repeat: k.IsRepeat}
	if k.ShiftedCode > 0 && k.ShiftedCode != k.Code && k.ShiftedCode < tea.KeyExtended {
		wire.Shifted = string(k.ShiftedCode)
	}
	if k.BaseCode > 0 && k.BaseCode != k.Code && k.BaseCode < tea.KeyExtended {
		wire.Base = string(k.BaseCode)
	}
	return ev, wire, true
}

// chordName is a key as the notice of an unbound key names it: the
// modifiers, then the text of a printable key or its name ("z", "%",
// "ctrl+z", "f5").
func chordName(ev keys.Event) string {
	name := ev.Name
	mod := ev.Mod &^ (proto.ModCaps | proto.ModNum)
	if ev.Text != "" && mod&^proto.ModShift == 0 {
		return ev.Text
	}
	prefix := ""
	for _, p := range []struct {
		bit  int
		name string
	}{{proto.ModCtrl, "ctrl"}, {proto.ModAlt, "alt"}, {proto.ModShift, "shift"}, {proto.ModSuper, "super"}, {proto.ModHyper, "hyper"}, {proto.ModMeta, "meta"}} {
		if mod&p.bit != 0 {
			prefix += p.name + "+"
		}
	}
	if name == "escape" {
		name = "esc"
	}
	return prefix + name
}

// key is a key press or release from the outer terminal.
func (m *Model) key(k tea.Key, release bool, msg tea.Msg) tea.Cmd {
	if m.queued(msg) {
		return nil
	}
	return m.routeKey(k, release, msg)
}

// input routes a key or paste event that waited behind the gate.
func (m *Model) input(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.key(tea.Key(msg), false, msg)
	case tea.KeyReleaseMsg:
		return m.key(tea.Key(msg), true, msg)
	case tea.PasteMsg:
		return m.paste(msg)
	}
	return nil
}

func (m *Model) routeKey(k tea.Key, release bool, msg tea.Msg) tea.Cmd {
	ev, wire, ok := keyEvent(k, release)
	if m.overlay != nil || m.copy != nil || m.confirm != nil || m.pending || m.view != "showing" || m.tooSmall() {
		// a resize repeats only over the panes it resizes
		m.keys.EndRepeat()
	}
	if ok && !release {
		m.lastKey = chordName(ev)
	}
	if ok && !release && !k.IsRepeat {
		// a new press: whatever was remembered for this key is stale
		delete(m.taken, ev.Name)
	}
	// (1) too small, or nothing to show, no hub and no switch that the keys
	// are held for
	if m.tooSmall() || (m.view == "none" && m.overlay == nil && !m.pending) {
		if !ok {
			return nil
		}
		r := m.keys.Handle(ev, m.keyContext())
		m.tableTook(ev, r)
		cmd := m.prefixResult(r)
		if r.Verdict == keys.Act && (r.Action.Name == "detach" || r.Action.Name == "hub") {
			return tea.Batch(cmd, m.act(r.Action))
		}
		return cmd
	}
	// (2) the hub: the prefix works while it is open (5a.10.1: the prefix
	// holds with every view, and with view none the hub is always open,
	// golden status-prefix-none-80). The prefix chord and the key after it
	// are the prefix machine's, and of the bindings only detach and hub act,
	// as under (1); every other key, a direct binding too (ctrl+j is the
	// hub's next row), is the overlay's. A repeat of a press the key table
	// consumed (the prefix chord, the key after it, the key that opened the
	// hub) is consumed too, until its release or a new press (3.2.4): held
	// esc after prefix+esc does not close the hub, held c after prefix+c
	// types nothing into it. The repeats of a key the hub got are the hub's.
	if m.overlay != nil {
		if release {
			if ok {
				// the machine forgets a press it consumed
				m.keys.Handle(ev, m.keyContext())
			}
			m.untake(ev, ok)
			return nil
		}
		if ok {
			if k.IsRepeat && m.taken[ev.Name] == byTable {
				return nil
			}
			was := m.prefix
			r := m.keys.Handle(ev, m.keyContext())
			if was || r.Pending {
				m.take(ev, byTable)
				cmd := m.prefixResult(r)
				if r.Verdict == keys.Act && (r.Action.Name == "detach" || r.Action.Name == "hub") {
					return tea.Batch(cmd, m.act(r.Action))
				}
				return cmd
			}
		}
		if ok {
			m.take(ev, byMode)
		}
		return m.toOverlay(msg)
	}
	if !ok {
		return nil
	}
	// (3) the confirm of pane-close
	if m.confirm != nil {
		if release || k.IsRepeat {
			m.untake(ev, true)
			return nil
		}
		m.take(ev, byMode)
		return m.confirmKey(ev)
	}
	// (4) copy mode: the prefix chord still starts the prefix, so that the
	// bindings (the hub, detach) act; every other key is copy mode's
	if m.copy != nil && !m.prefix {
		if release {
			m.untake(ev, true)
			return nil
		}
		if r := m.keys.Handle(ev, m.keyContext()); r.Pending {
			m.tableTook(ev, r)
			return m.prefixResult(r)
		}
		m.take(ev, byMode)
		return m.copyKey(ev)
	}
	// a press that the hub, the confirm or copy mode consumed consumes its
	// release and repeats after that mode ended (3.2.4), as the key table
	// does for the presses it consumed; those the key table consumed it
	// decides itself, so that it forgets them
	if by := m.taken[ev.Name]; by != 0 && (release || k.IsRepeat) {
		if release {
			delete(m.taken, ev.Name)
		}
		if by == byMode {
			return nil
		}
	}
	// (5), (6) the key table
	r := m.keys.Handle(ev, m.keyContext())
	m.tableTook(ev, r)
	cmd := m.prefixResult(r)
	switch r.Verdict {
	case keys.Act:
		return tea.Batch(cmd, m.act(r.Action))
	case keys.Swallow:
		return cmd
	case keys.SendPrefix:
		// the prefix chord itself goes to the pane; in copy mode, which owns
		// the pane's position, it does nothing
		if m.copy != nil {
			return cmd
		}
		return tea.Batch(cmd, m.sendKey(wire))
	}
	// (7) the focused pane
	return tea.Batch(cmd, m.sendKey(wire))
}

// maxTaken bounds the presses the modes remember, as keys.Machine bounds
// its own: a terminal that never reports releases leaves an entry until
// the next press of the same key.
const maxTaken = 32

// taker is what consumed a press.
type taker uint8

const (
	// byMode: the hub, the confirm or copy mode
	byMode taker = iota + 1
	// byTable: the key table (the prefix chord, the key after it, a
	// binding), which consumes the release and the repeats itself; the
	// client remembers it for the hub, which would otherwise get the repeats
	byTable
)

// take remembers a press that a mode or the key table consumed: once the
// mode or the prefix has ended, its release and repeats still never reach
// the pane or the hub. keyEvent lower-cases a shifted letter, so a press
// and its release share one name.
func (m *Model) take(ev keys.Event, by taker) {
	if ev.Release || ev.Repeat {
		return
	}
	if m.taken == nil || len(m.taken) >= maxTaken {
		m.taken = map[string]taker{}
	}
	m.taken[ev.Name] = by
}

// tableTook remembers a press the key table consumed.
func (m *Model) tableTook(ev keys.Event, r keys.Result) {
	if r.Verdict != keys.Forward {
		m.take(ev, byTable)
	}
}

// untake forgets a consumed press whose release a mode received.
func (m *Model) untake(ev keys.Event, ok bool) {
	if ok && ev.Release {
		delete(m.taken, ev.Name)
	}
}

// keyContext is what the key table knows of the focused pane.
func (m *Model) keyContext() keys.Context {
	p, _ := m.pane(m.focusedPane())
	return keys.Context{Program: p.Program, Editor: p.Editor}
}

// sendKey sends input.key to the focused pane, returning a scrolled view to
// live first. Releases and repeats go only to a pane whose program asked
// for them (modes.keys bit 2, 4.2.4). While a switch runs the key goes to
// the fleet, which holds it for the switch's target and addresses it there
// (3.5.4 step 3); nothing goes to the old view. Otherwise nothing is sent
// while the view is stale or nothing is shown.
func (m *Model) sendKey(k proto.Key) tea.Cmd {
	if m.pending {
		if k.Release {
			return nil
		}
		k.Repeat = false
		k.Pane = m.focusedPane()
		m.fleet.Input(proto.Encode(k))
		return nil
	}
	if m.view != "showing" {
		return nil
	}
	id := m.focusedPane()
	sc := m.panes.Screen(id)
	if sc == nil {
		return nil
	}
	if (k.Release || k.Repeat) && sc.Modes().Keys&2 == 0 {
		if k.Release {
			return nil
		}
		k.Repeat = false
	}
	if !k.Release {
		m.endScroll()
	}
	k.Pane = id
	m.fleet.Input(proto.Encode(k))
	return nil
}

// paste sends a paste to the focused pane in chunks of at most
// proto.MaxPaste bytes, cut at rune boundaries, `more` on every chunk but
// the last (4.2.6).
func (m *Model) paste(msg tea.PasteMsg) tea.Cmd {
	if m.queued(msg) {
		return nil
	}
	if m.overlay != nil {
		return m.toOverlay(msg)
	}
	if m.tooSmall() || m.confirm != nil || m.copy != nil || msg.Content == "" {
		return nil
	}
	id := m.focusedPane()
	switch {
	case m.pending:
		// held by the fleet for the switch's target, as keys are (3.5.4)
	case m.view != "showing" || id == "":
		return nil
	default:
		m.endScroll()
	}
	for _, chunk := range chunks(msg.Content, proto.MaxPaste) {
		m.fleet.Input(proto.Encode(proto.Paste{Pane: id, Text: chunk.text, More: chunk.more}))
	}
	return nil
}

type chunk struct {
	text string
	more bool
}

// chunks cuts s into pieces of at most n bytes without splitting a rune.
// Bytes that are not UTF-8 travel as they are, one byte being one rune.
func chunks(s string, n int) []chunk {
	var out []chunk
	for len(s) > n {
		cut := n
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = n
		}
		out = append(out, chunk{text: s[:cut], more: true})
		s = s[cut:]
	}
	return append(out, chunk{text: s})
}

// prefixResult follows the prefix in the status row (5a.10.1, C3): the
// hint strip after ui.HintDelay or at once on "?", the next page with each
// further "?", and the notice of an unbound key.
func (m *Model) prefixResult(r keys.Result) tea.Cmd {
	var cmds []tea.Cmd
	if r.Repeat {
		m.repeatGen++
		cmds = append(cmds, m.hk.After(keys.RepeatTime, repeatDue{gen: m.repeatGen}))
	}
	was := m.prefix
	m.prefix = r.Pending
	switch {
	case m.prefix && !was:
		m.prefixGen++
		m.hints, m.hintPage = false, 0
		cmds = append(cmds, m.hk.After(ui.HintDelay, hintsDue{gen: m.prefixGen}))
	case !m.prefix && was:
		m.prefixGen++
		m.hints, m.hintPage = false, 0
	}
	if r.More {
		if m.hints {
			m.hintPage++
		} else {
			m.hints = true
		}
	}
	if r.Unbound {
		cmds = append(cmds, m.setNotice(ui.Notice("unbound", m.lastKey)))
	}
	return tea.Batch(cmds...)
}

// confirmKey is a key while the confirm of pane-close is open (5a.11.4):
// enter closes the pane, esc keeps it, everything else is swallowed.
func (m *Model) confirmKey(ev keys.Event) tea.Cmd {
	if ev.Mod&^(proto.ModCaps|proto.ModNum) != 0 {
		return nil
	}
	switch ev.Name {
	case "enter":
		c := m.confirm
		m.confirm = nil
		return m.request(proto.PaneClose{Pane: c.pane})
	case "escape", "esc":
		m.confirm = nil
	}
	return nil
}

// act runs an action of the key table (5a.11.2).
func (m *Model) act(a keys.Action) tea.Cmd {
	switch a.Name {
	case "detach":
		return m.quit()
	case "hub":
		return m.openHub(fleet.HubRequest{Mode: hubMode(a.Arg), Here: m.here()})
	case "session-next":
		m.fleet.Step(1)
		return nil
	case "session-prev":
		m.fleet.Step(-1)
		return nil
	case "copy-mode":
		return m.enterCopy()
	}
	if m.view != "showing" {
		return nil
	}
	sess := m.ok.Session
	pane := m.focusedPane()
	switch a.Name {
	case "tab-new":
		return m.request(proto.TabNew{Session: sess})
	case "tab-next":
		return m.request(proto.TabSelect{Session: sess, Step: 1})
	case "tab-prev":
		return m.request(proto.TabSelect{Session: sess, Step: -1})
	case "tab-select":
		n, err := strconv.Atoi(a.Arg)
		if err != nil || n < 1 {
			return nil
		}
		return m.request(proto.TabSelect{Session: sess, Index: n})
	case "split-right":
		return m.request(proto.PaneSplit{Pane: pane, Direction: proto.SplitRight})
	case "split-down":
		return m.request(proto.PaneSplit{Pane: pane, Direction: proto.SplitDown})
	case "pane-close":
		p, ok := m.pane(pane)
		if ok && p.Busy && p.State == proto.PaneRunning {
			// C4: a running program is asked about first
			m.confirm = &confirmPane{pane: pane, program: programName(p.Program)}
			return nil
		}
		return m.request(proto.PaneClose{Pane: pane})
	case "focus-left", "focus-right", "focus-up", "focus-down":
		return m.request(proto.PaneFocus{Pane: pane, Direction: a.Name[len("focus-"):]})
	case "resize-left", "resize-right", "resize-up", "resize-down":
		n := 1
		if a.Arg != "" {
			if v, err := strconv.Atoi(a.Arg); err == nil && v > 0 {
				n = v
			}
		}
		return m.request(proto.PaneResize{Pane: pane, Direction: a.Name[len("resize-"):], Cells: n})
	}
	return nil
}

// programName is a program's name as the confirm shows it.
func programName(p string) string {
	if p == "" {
		return "a program"
	}
	return p
}

// hubMode is the mode of the hub action's argument.
func hubMode(arg string) hub.Mode {
	switch arg {
	case "sessions":
		return hub.ModeSessions
	case "agents":
		return hub.ModeAgents
	case "new":
		return hub.ModeNew
	}
	return hub.ModeAll
}

// request sends a request to the displayed server; one that can change the
// focus opens the typeahead gate.
func (m *Model) request(v proto.Typed) tea.Cmd {
	if m.view != "showing" {
		return nil
	}
	id := m.newID()
	kind := v.Type()
	g := gated[kind]
	reply := m.fleet.Request(proto.Request(id, v))
	wait := m.hk.Await(reply, func(msg proto.Msg) tea.Msg { return replyMsg{kind: kind, gated: g, msg: msg} })
	if g {
		return tea.Batch(m.arm(), wait)
	}
	return wait
}

// reply is the answer to a request: a refusal is a notice; the result is
// read from the state, which holds it (W2).
func (m *Model) reply(r replyMsg) tea.Cmd {
	var cmds []tea.Cmd
	rep, ok := proto.AsReply(r.msg)
	if ok && !rep.OK && rep.Code != proto.CodeLost {
		cmds = append(cmds, m.setNotice(ui.Notice("refused", m.machineName(m.target.Machine), rep.Text)))
	}
	if r.kind == proto.TypePaneText {
		// the text of copy mode's copy; it changes no state
		if ok && rep.OK {
			cmds = append(cmds, m.copied(r.msg))
		}
		return tea.Batch(cmds...)
	}
	if r.gated {
		cmds = append(cmds, m.gateReplied())
	} else if m.view == "showing" {
		cmds = append(cmds, m.refreshState())
	}
	return tea.Batch(cmds...)
}
