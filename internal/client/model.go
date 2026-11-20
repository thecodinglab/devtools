package client

// The model of the client program (3.2.4, 5.3.7): what is displayed, the
// state of the displayed server, the modes of the status row and what the
// chrome is given. Update (update.go) feeds it the fleet's updates, route.go
// the keys, resize.go the window size, term.go what the outer terminal
// answers; compose.go draws it.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"devtools/internal/client/chrome"
	"devtools/internal/client/keys"
	"devtools/internal/client/panes"
	"devtools/internal/client/screen"
	"devtools/internal/config"
	"devtools/internal/fleet"
	"devtools/internal/proto"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// Hooks are what the model needs of time and of waiting. Run gives it real
// time and commands that block; clienttest gives it a fake clock and
// delivers the timers and the replies itself, so that a test runs the
// model's commands synchronously.
type Hooks struct {
	// Now is the time the model reads.
	Now func() time.Time
	// After returns a command that yields msg once d has passed.
	After func(d time.Duration, msg tea.Msg) tea.Cmd
	// Await returns a command that yields wrap of the one message reply
	// delivers.
	Await func(reply <-chan proto.Msg, wrap func(proto.Msg) tea.Msg) tea.Cmd
}

// RealHooks are the hooks Run uses: the wall clock, tea.Tick and a command
// that blocks on the reply.
func RealHooks() Hooks {
	return Hooks{
		Now: time.Now,
		After: func(d time.Duration, msg tea.Msg) tea.Cmd {
			return tea.Tick(d, func(time.Time) tea.Msg { return msg })
		},
		Await: func(reply <-chan proto.Msg, wrap func(proto.Msg) tea.Msg) tea.Cmd {
			return func() tea.Msg {
				m, ok := <-reply
				if !ok {
					m = proto.ErrReply(0, proto.CodeLost, "no reply")
				}
				return wrap(m)
			}
		},
	}
}

// UpdatesMsg carries updates of the fleet into the model: everything that
// was ready when the reader took them, at most maxBatch, so that one
// composition serves a burst (3.2.4).
type UpdatesMsg []fleet.Update

// maxBatch is the most updates one UpdatesMsg carries.
const maxBatch = 64

// fleetEnded: the fleet's updates channel was closed; the program ends.
type fleetEnded struct{}

// The model's timer messages. Each carries the generation of what armed it,
// so a timer of something that ended since does nothing.
type (
	hintsDue  struct{ gen int } // the prefix has been pending for ui.HintDelay
	repeatDue struct{ gen int } // keys.RepeatTime passed since the last resize of the repeat
	noticeDue struct{ gen int } // a notice's ui.NoticeLife has passed
	minuteDue struct{}          // the clock of the status row reached a new minute
	resizeDue struct{}          // the debounce of the window size ended
	gateDue   struct{ gen int } // the typeahead gate gave up (2 s)
	replyMsg  struct {          // the reply to a request the client sent
		kind  string // the request's type
		gated bool   // the request holds the typeahead gate
		msg   proto.Msg
	}
)

// Model is the client program. It satisfies tea.Model; Run runs it and
// clienttest drives it.
type Model struct {
	o      Options
	hk     Hooks
	fleet  Fleet
	chrome chrome.Chrome
	table  *keys.Table
	keys   *keys.Machine
	tokens []ui.Token
	utf8   bool

	// the window
	w, h     int
	sized    bool
	canvas   *screen.Canvas
	focused  bool // the outer terminal has the focus
	quitting bool

	// what is displayed (3.5.4): view is "none", "showing" or "stale"
	view    string
	target  session.Target
	ok      proto.ViewOK
	panes   panes.Set
	broken  bool // a frame of this view was invalid; nothing more is applied until the next Shown
	summary fleet.SummaryData

	// the state of the displayed server
	state    *proto.State
	refresh  bool        // a state.get of this view is in flight
	viewGen  int         // counts the committed views (Shown); a state read carries the one it was asked in
	stateGen int         // the viewGen in which state was last set
	held     []heldFrame // frames of panes the state does not show yet
	bells    map[string]bool

	// the modes of the status row (5a.10.1)
	prefix    bool
	prefixGen int
	repeatGen int // of the repeat timer
	hints     bool
	hintPage  int
	confirm   *confirmPane
	copy      *copyState   // copy mode (copy.go)
	scroll    *scrollState // the scrolled view (scroll.go)
	pending   bool         // Summary.Mode is "switching": the old view is drawn inactive (F14)
	switching bool         // a switch has been pending for ui.Slow: the status row says so
	switchGen int
	unscroll  string // the pane whose copy mode a switch ended: it is told to show live when the switch rolls back
	notice    string
	lastKey   string // the chord of the last key, for the notice of an unbound key
	noticeAt  time.Time
	noticeGen int
	now       time.Time

	// the typeahead gate (gate.go)
	gate gate

	// the key names of presses the hub, the confirm, copy mode or the key
	// table consumed, whose releases and repeats are consumed too (route.go)
	taken map[string]taker

	// the window size towards the fleet (resize.go)
	rs resizer

	// the outer terminal's colours and the theme (term.go)
	colors colors
	theme  ui.Theme

	// the hub overlay (overlay.go)
	overlay chrome.Overlay

	// the mouse button held since a press in a pane (mouse.go)
	press *pressState

	// composition (compose.go)
	comp composition
	ids  uint64 // request ids of the client
}

// confirmPane is the pending question of pane-close (C4).
type confirmPane struct {
	pane, program string
}

// NewModel returns the model Run runs. It touches neither the terminal nor
// the fleet; an error is a `color` line that ui.NewTheme refuses.
func NewModel(o Options, h Hooks) (*Model, error) {
	if h.Now == nil || h.After == nil || h.Await == nil {
		rh := RealHooks()
		if h.Now == nil {
			h.Now = rh.Now
		}
		if h.After == nil {
			h.After = rh.After
		}
		if h.Await == nil {
			h.Await = rh.Await
		}
	}
	tokens, err := themeTokens(o.Colors)
	if err != nil {
		return nil, err
	}
	m := &Model{
		o:       o,
		hk:      h,
		fleet:   o.Fleet,
		chrome:  o.Chrome,
		table:   o.Keys,
		tokens:  tokens,
		utf8:    utf8Locale(o.Env),
		canvas:  screen.New(0, 0),
		focused: true,
		view:    "none",
		bells:   map[string]bool{},
	}
	if m.chrome == nil {
		m.chrome = chrome.Plain{}
	}
	if m.table == nil {
		m.table = keys.Defaults()
	}
	m.keys = keys.NewMachine(m.table)
	m.colors.profile = 16
	if err := m.rebuildTheme(); err != nil {
		return nil, err
	}
	m.now = h.Now().Truncate(time.Minute)
	return m, nil
}

// Init asks the terminal for its colours, starts the clock of the status
// row and, when the client starts in the hub, opens it.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.askColors(), m.nextMinute()}
	if m.o.Hub != nil {
		cmds = append(cmds, m.openHub(*m.o.Hub))
	}
	return tea.Batch(cmds...)
}

// Update is the one entry of every message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case UpdatesMsg:
		cmd = m.updates(msg)
	case fleetEnded:
		cmd = m.quit()
	case tea.WindowSizeMsg:
		cmd = m.resize(msg.Width, msg.Height)
	case resizeDue:
		cmd = m.resizeTimer()
	case tea.KeyPressMsg:
		cmd = m.key(tea.Key(msg), false, msg)
	case tea.KeyReleaseMsg:
		cmd = m.key(tea.Key(msg), true, msg)
	case tea.PasteMsg:
		cmd = m.paste(msg)
	case tea.FocusMsg:
		cmd = m.focus(true)
	case tea.BlurMsg:
		cmd = m.focus(false)
	case tea.BackgroundColorMsg:
		cmd = m.background(msg)
	case tea.ForegroundColorMsg:
		cmd = m.foreground(msg)
	case tea.ColorProfileMsg:
		cmd = m.profile(msg)
	case replyMsg:
		cmd = m.reply(msg)
	case stateReply:
		cmd = m.stateRead(msg)
	case hintsDue:
		if m.prefix && msg.gen == m.prefixGen {
			m.hints = true
		}
	case repeatDue:
		if msg.gen == m.repeatGen {
			m.keys.EndRepeat()
		}
	case noticeDue:
		if msg.gen == m.noticeGen {
			m.notice = ""
		}
	case minuteDue:
		m.now = m.hk.Now().Truncate(time.Minute)
		cmd = m.nextMinute()
	case gateDue:
		cmd = m.gateTimeout(msg.gen)
	case switchDue:
		m.switchSlow(msg)
	case tea.MouseMsg:
		cmd = m.mouse(msg)
	default:
		cmd = m.toOverlay(msg)
	}
	return m, cmd
}

// quit ends the program.
func (m *Model) quit() tea.Cmd {
	m.quitting = true
	return tea.Quit
}

// nextMinute arms the timer of the next full minute (C2).
func (m *Model) nextMinute() tea.Cmd {
	now := m.hk.Now()
	next := now.Truncate(time.Minute).Add(time.Minute)
	return m.hk.After(next.Sub(now), minuteDue{})
}

// setNotice shows a line in the status row for ui.NoticeLife (C12).
func (m *Model) setNotice(text string) tea.Cmd {
	text = session.Sanitize(text, session.MaxText)
	if text == "" {
		return nil
	}
	m.noticeGen++
	m.notice = text
	m.noticeAt = m.hk.Now()
	return m.hk.After(ui.NoticeLife, noticeDue{gen: m.noticeGen})
}

// newID is the id of the next request of the client; the fleet replies
// under it.
func (m *Model) newID() uint64 {
	m.ids++
	return m.ids
}

// --- the state of the displayed server ---

// sessionView is the displayed session in the state, false before the
// state is known.
func (m *Model) sessionView() (proto.SessionView, bool) {
	if m.state == nil || m.ok.Session == "" {
		return proto.SessionView{}, false
	}
	return m.state.View(m.ok.Session)
}

// shownTab is the tab whose panes are on screen: the session's current tab
// in the state, else the tab of view.ok.
func (m *Model) shownTab() (proto.Tab, bool) {
	if m.state != nil {
		if s, ok := m.state.Sessions[m.ok.Session]; ok {
			if t, ok := m.state.Tabs[s.Current]; ok {
				return t, true
			}
		}
		if t, ok := m.state.Tabs[m.ok.Tab]; ok {
			return t, true
		}
	}
	if m.ok.Tab == "" {
		return proto.Tab{}, false
	}
	return proto.Tab{ID: m.ok.Tab, Session: m.ok.Session, Panes: m.ok.Panes, Focused: first(m.ok.Panes)}, true
}

// focusedPane is the pane that gets the keys: the focused pane of the shown
// tab, "" when nothing is displayed.
func (m *Model) focusedPane() string {
	if m.view == "none" {
		return ""
	}
	t, ok := m.shownTab()
	if !ok {
		return ""
	}
	if t.Focused != "" && m.panes.Screen(t.Focused) != nil {
		return t.Focused
	}
	for _, id := range t.Panes {
		if m.panes.Screen(id) != nil {
			return id
		}
	}
	return ""
}

// pane is the pane object of the state, false when unknown.
func (m *Model) pane(id string) (proto.Pane, bool) {
	if m.state == nil || id == "" {
		return proto.Pane{}, false
	}
	p, ok := m.state.Panes[id]
	return p, ok
}

func first(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// --- what the chrome is given ---

// mode is the mode of the status row by the precedence of 5a.10.1:
// confirm, copy, prefix, switching, scrolled, normal.
func (m *Model) mode() string {
	switch {
	case m.confirm != nil:
		return "confirm"
	case m.copy != nil:
		return "copy"
	case m.prefix:
		return "prefix"
	case m.switching:
		return "switching"
	case m.scroll != nil:
		return "scrolled"
	}
	return "normal"
}

// chromeState is everything the chrome may show (5.3.5).
func (m *Model) chromeState() chrome.State {
	mode := m.mode()
	st := chrome.State{
		Width:         m.w,
		Height:        m.h,
		Theme:         m.theme,
		WindowFocused: m.focused,
		Mode:          mode,
		View:          m.view,
		Hints:         m.prefix && m.hints,
		HintPage:      m.hintPage,
		Summary:       m.chromeSummary(),
		Now:           m.now,
	}
	if m.view != "none" {
		st.Machine = session.Sanitize(m.target.Machine, session.MaxLabel)
		st.Session = m.sessionLabel()
		st.Tabs = m.chromeTabs()
	}
	// a notice is shown in normal and scrolled only; in the other modes it
	// waits, and is dropped if its time passes meanwhile (5a.10.1)
	if mode == "normal" || mode == "scrolled" {
		st.Notice = m.notice
	}
	if m.confirm != nil {
		st.Confirm = &chrome.Confirm{Program: m.confirm.program}
	}
	st.Copy = m.copyIndicator()
	if m.switching {
		st.Target = chrome.Target{Machine: m.machineName(m.summary.Target.Machine), Session: m.targetLabel(m.summary.Target)}
	}
	return st
}

// sessionLabel is the label of the displayed session, sanitised; the
// target's name or id until the state is known.
func (m *Model) sessionLabel() string {
	if v, ok := m.sessionView(); ok && v.Session.Label != "" {
		return session.Sanitize(v.Session.Label, session.MaxLabel)
	}
	return m.targetLabel(m.target)
}

// targetLabel is what a target is called before its session's label is
// known.
func (m *Model) targetLabel(t session.Target) string {
	if t.Machine == m.target.Machine && m.state != nil {
		if s, ok := m.state.Sessions[t.Session]; ok && s.Label != "" {
			return session.Sanitize(s.Label, session.MaxLabel)
		}
	}
	if t.Name != "" {
		return session.Sanitize(t.Name, session.MaxLabel)
	}
	return session.Sanitize(t.Session, session.MaxLabel)
}

// machineName is the name of a machine as displayed: its alias, or for
// this machine the host name the summary carries.
func (m *Model) machineName(name string) string {
	if name != "" {
		return session.Sanitize(name, session.MaxLabel)
	}
	if len(m.summary.Machines) > 0 {
		return session.Sanitize(m.summary.Machines[0].Host, session.MaxLabel)
	}
	return ""
}

// chromeTabs are the tabs of the displayed session (C10, C11).
func (m *Model) chromeTabs() []chrome.Tab {
	v, ok := m.sessionView()
	if !ok {
		return nil
	}
	agents := map[string]proto.Agent{}
	for _, a := range v.Agents {
		agents[a.Pane] = a
	}
	tabs := make([]chrome.Tab, 0, len(v.Tabs))
	for _, t := range v.Tabs {
		ct := chrome.Tab{ID: t.ID, Index: t.Index, Current: t.ID == v.Session.Current, Bell: m.bells[t.ID]}
		if a, ok := agents[t.Focused]; ok && (a.Kind == proto.AgentClaude || a.Kind == proto.AgentCodex) {
			ct.Title = a.Kind
		} else if p, ok := m.state.Panes[t.Focused]; ok {
			ct.Title = session.Sanitize(p.Program, session.MaxLabel)
		}
		for _, id := range t.Panes {
			if a, ok := agents[id]; ok && rank(a.Status) > rank(ct.Agent) {
				ct.Agent = a.Status
			}
		}
		if ct.Agent == proto.StatusIdle || rank(ct.Agent) == 0 {
			ct.Agent = ""
		}
		tabs = append(tabs, ct)
	}
	return tabs
}

// rank orders agent statuses: blocked, done, working, idle, unknown.
func rank(s string) int {
	switch s {
	case proto.StatusBlocked:
		return 4
	case proto.StatusDone:
		return 3
	case proto.StatusWorking:
		return 2
	case proto.StatusIdle:
		return 1
	}
	return 0
}

// chromeSummary copies the fleet's summary with plain strings (rule 7 of
// 5.2). Machines[0] is this machine and carries the name to display (C9).
func (m *Model) chromeSummary() chrome.Summary {
	s := m.summary
	out := chrome.Summary{Top: string(s.Top), Counts: map[string]int{}}
	for k, v := range s.Counts {
		out.Counts[string(k)] = v
	}
	for i, ms := range s.Machines {
		cm := chrome.Machine{
			Name:    session.Sanitize(ms.Name, session.MaxLabel),
			Host:    session.Sanitize(ms.Host, session.MaxLabel),
			State:   ms.State,
			Problem: ms.Problem.Reason,
			Retry:   ms.Problem.Retry,
			Reason:  session.Sanitize(ms.Problem.Text, session.MaxText),
			Agents:  map[string]int{},
			Busy:    ms.Busy,
		}
		if i == 0 && cm.Name == "" {
			cm.Name = cm.Host
		}
		for k, v := range ms.Agents {
			cm.Agents[string(k)] = v
		}
		out.Machines = append(out.Machines, cm)
	}
	if u := s.Urgent; u.Session != "" {
		out.Urgent = chrome.Target{Machine: m.machineName(u.Machine), Session: m.targetLabel(u)}
	}
	return out
}

// --- the theme (5a.16.1) ---

// rebuildTheme calls ui.NewTheme with the four inputs as they are now and
// tells an open overlay.
func (m *Model) rebuildTheme() error {
	t, err := ui.NewTheme(ui.ThemeOptions{
		Tokens: m.tokens,
		Dark:   m.colors.dark,
		Known:  m.colors.known,
		Colors: m.colors.profile,
		UTF8:   m.utf8,
	})
	if err != nil {
		return err
	}
	m.theme = t
	return nil
}

// themeTokens reads the `color` lines (4.6): "#rrggbb" for light and dark.
func themeTokens(colors []config.Color) ([]ui.Token, error) {
	tokens := make([]ui.Token, 0, len(colors))
	for _, c := range colors {
		light, err := parseRGB(c.Light)
		if err != nil {
			return nil, fmt.Errorf("line %d: color %s: %v", c.Line, c.Token, err)
		}
		dark, err := parseRGB(c.Dark)
		if err != nil {
			return nil, fmt.Errorf("line %d: color %s: %v", c.Line, c.Token, err)
		}
		tokens = append(tokens, ui.Token{Name: c.Token, Light: light, Dark: dark})
	}
	return tokens, nil
}

func parseRGB(s string) (uint32, error) {
	if len(s) != 7 || s[0] != '#' {
		return 0, fmt.Errorf("%q is not of the form #rrggbb", s)
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not of the form #rrggbb", s)
	}
	return uint32(v), nil
}

// utf8Locale reads the locale of env as the C library does: LC_ALL, then
// LC_CTYPE, then LANG; the first that is set decides.
func utf8Locale(env []string) bool {
	vals := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vals[k] = v
		}
	}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := vals[name]; v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}
