package proto

import (
	"fmt"
	"sort"
)

// Message types of 4.2. The state messages are in state.go; the messages of
// 4.2.10, 4.3 and event.agent are in cross.go.
const (
	TypeHello   = "hello"
	TypeWelcome = "welcome"
	TypeRefuse  = "refuse"

	TypeViewSet    = "view.set"
	TypeViewOK     = "view.ok"
	TypeViewResize = "view.resize"
	TypeViewColors = "view.colors"
	TypeViewClear  = "view.clear"
	TypeViewEnded  = "view.ended"

	TypeFrame = "frame"

	TypeInputKey   = "input.key"
	TypeInputMouse = "input.mouse"
	TypeInputPaste = "input.paste"
	TypeInputFocus = "input.focus"

	TypeSessionOpen  = "session.open"
	TypeSessionClose = "session.close"
	TypeTabNew       = "tab.new"
	TypeTabSelect    = "tab.select"
	TypeTabClose     = "tab.close"
	TypePaneSplit    = "pane.split"
	TypePaneFocus    = "pane.focus"
	TypePaneResize   = "pane.resize"
	TypePaneClose    = "pane.close"
	TypePaneText     = "pane.text"
	TypePaneScroll   = "pane.scroll"

	TypeEventReflow    = "event.reflow"
	TypeEventClipboard = "event.clipboard"
	TypeEventBell      = "event.bell"

	TypePing = "ping"
	TypePong = "pong"
)

// Connection kinds of hello.kind.
const (
	KindInteractive = "interactive"
	KindOneshot     = "oneshot"
)

// registry maps every message type of the package to a constructor of its
// struct. cross.go (P2-00) adds its types with register from an init
// function; the wire tests read the registry.
var registry = map[string]func() Typed{}

// register adds a message type. A duplicate or a constructor whose Type does
// not match panics: both are programming errors.
func register(f func() Typed) {
	t := f().Type()
	if _, dup := registry[t]; dup {
		panic(fmt.Sprintf("proto: message type %s registered twice", t))
	}
	registry[t] = f
}

// Types returns every registered message type, sorted. The reply types "ok"
// and "err" have no struct and are not among them.
func Types() []string {
	ts := make([]string, 0, len(registry))
	for t := range registry {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return ts
}

// NewMessage returns a pointer to a new struct of the message type t, false when t
// is not a type of this package.
func NewMessage(t string) (Typed, bool) {
	f, ok := registry[t]
	if !ok {
		return nil, false
	}
	return f(), true
}

func init() {
	for _, f := range []func() Typed{
		func() Typed { return &Hello{} },
		func() Typed { return &Welcome{} },
		func() Typed { return &Refuse{} },
		func() Typed { return &Snapshot{} },
		func() Typed { return &Delta{} },
		func() Typed { return &StateGet{} },
		func() Typed { return &ViewSet{} },
		func() Typed { return &ViewOK{} },
		func() Typed { return &ViewResize{} },
		func() Typed { return &ViewColors{} },
		func() Typed { return &ViewClear{} },
		func() Typed { return &ViewEnded{} },
		func() Typed { return &Frame{} },
		func() Typed { return &Key{} },
		func() Typed { return &Mouse{} },
		func() Typed { return &Paste{} },
		func() Typed { return &Focus{} },
		func() Typed { return &SessionOpen{} },
		func() Typed { return &SessionClose{} },
		func() Typed { return &TabNew{} },
		func() Typed { return &TabSelect{} },
		func() Typed { return &TabClose{} },
		func() Typed { return &PaneSplit{} },
		func() Typed { return &PaneFocus{} },
		func() Typed { return &PaneResize{} },
		func() Typed { return &PaneClose{} },
		func() Typed { return &PaneText{} },
		func() Typed { return &PaneScroll{} },
		func() Typed { return &Reflow{} },
		func() Typed { return &Clipboard{} },
		func() Typed { return &Bell{} },
		func() Typed { return &Ping{} },
		func() Typed { return &Pong{} },
	} {
		register(f)
	}
}

// --- 4.2.1 Handshake ---

// Hello is the first message of a client.
type Hello struct {
	Gen     int      `json:"gen"`
	Version string   `json:"version"`
	Caps    []string `json:"caps,omitempty"`
	Kind    string   `json:"kind"` // KindInteractive, KindOneshot
	Remote  bool     `json:"remote"`
	// TTY and Ancestors: the terminal and the parent chain of the client,
	// for the nesting check of 3.4 d. A one-shot with a pane carries
	// Ancestors too, its own pid first.
	TTY       string `json:"tty,omitempty"`
	Ancestors []int  `json:"ancestors,omitempty"`
	// Pane is DEVTOOLS_PANE of a one-shot caller, "" outside a pane.
	Pane   string            `json:"pane,omitempty"`
	Env    map[string]string `json:"env,omitempty"`    // local interactive only
	Colors *Colors           `json:"colors,omitempty"` // absent: #d0d0d0 on #000000, dark
}

// Colors are the colours of the outer terminal.
type Colors struct {
	Fg   string `json:"fg"` // "#rrggbb"
	Bg   string `json:"bg"`
	Dark bool   `json:"dark"`
}

// Welcome accepts a hello.
type Welcome struct {
	Gen     int      `json:"gen"`
	Version string   `json:"version"`
	Caps    []string `json:"caps,omitempty"`
	Boot    string   `json:"boot"` // random per server start
	Host    string   `json:"host"`
	Pid     int      `json:"pid"`
	Client  string   `json:"client"`  // "c" and a number
	Clients int      `json:"clients"` // interactive connections, this one included
	// Installed is the version of the build installed where the service
	// starts the server, when that is another build than the one running:
	// an update waits for a restart of the server. "unknown" when the
	// installed build did not say its version; absent when none waits. A
	// receiver that does not know the field ignores it (CapUpdate).
	Installed string `json:"installed,omitempty"`
}

// Refuse refuses a hello; the server closes the connection after it.
type Refuse struct {
	Code string `json:"code"` // CodeGeneration, CodeInvalid, CodeNested, CodeNoPane
	Text string `json:"text"`
}

func (Hello) Type() string   { return TypeHello }
func (Welcome) Type() string { return TypeWelcome }
func (Refuse) Type() string  { return TypeRefuse }

// --- 4.2.3 View ---

// ViewSet is view.set, a request: exactly one of Session and Dir.
type ViewSet struct {
	Session string `json:"session,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Name    string `json:"name,omitempty"` // the label of a session opened by Dir
	Tab     string `json:"tab,omitempty"`
	Pane    string `json:"pane,omitempty"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
}

// ViewOK is the named success reply of view.set.
type ViewOK struct {
	Session string   `json:"session"`
	Dir     string   `json:"dir"`
	Tab     string   `json:"tab"`
	Panes   []string `json:"panes"`
}

// ViewResize is view.resize, a notification: the pane area changed.
type ViewResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// ViewColors is view.colors, a notification: the outer terminal's colours
// changed.
type ViewColors struct {
	Fg   string `json:"fg"`
	Bg   string `json:"bg"`
	Dark bool   `json:"dark"`
}

// ViewClear is view.clear, a notification: the connection displays nothing.
type ViewClear struct{}

// ViewEnded is view.ended: the displayed session ended.
type ViewEnded struct {
	Session string `json:"session"`
	Dir     string `json:"dir"`
}

func (ViewSet) Type() string    { return TypeViewSet }
func (ViewOK) Type() string     { return TypeViewOK }
func (ViewResize) Type() string { return TypeViewResize }
func (ViewColors) Type() string { return TypeViewColors }
func (ViewClear) Type() string  { return TypeViewClear }
func (ViewEnded) Type() string  { return TypeViewEnded }

// --- 4.2.4 Frames ---

// Frame is one frame, or one piece of a frame split at FramePiece.
type Frame struct {
	Pane   string `json:"pane"`
	Seq    uint64 `json:"seq"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
	Full   bool   `json:"full"`
	More   bool   `json:"more,omitempty"` // every piece but the last
	Lines  []Line `json:"lines"`
	Cursor Cursor `json:"cursor"`
	Modes  Modes  `json:"modes"`
	Top    *int64 `json:"top,omitempty"` // only while this connection is scrolled in the pane
	First  int64  `json:"first"`         // oldest retained history line
	Base   int64  `json:"base"`          // line in row 0 of the live screen
}

// Line is one row of a frame in the grammar of 4.2.5.
type Line struct {
	Y int    `json:"y"`
	S string `json:"s"`
}

// Cursor is the cursor of a frame.
type Cursor struct {
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Visible bool   `json:"visible"`
	Shape   string `json:"shape"` // "block", "underline", "bar"
	Blink   bool   `json:"blink"`
}

// Modes are the modes of a frame that the client acts on.
type Modes struct {
	Alt   bool   `json:"alt"`
	Mouse string `json:"mouse"` // "", "press", "drag", "all"
	Keys  int    `json:"keys"`  // kitty keyboard flags, 0 for legacy
}

func (Frame) Type() string { return TypeFrame }

// --- 4.2.6 Input ---

// Key is input.key.
type Key struct {
	Pane    string `json:"pane"`
	Key     string `json:"key"` // ValidKey
	Mod     int    `json:"mod"` // Mod* bits
	Text    string `json:"text,omitempty"`
	Shifted string `json:"shifted,omitempty"`
	Base    string `json:"base,omitempty"`
	Repeat  bool   `json:"repeat,omitempty"`
	Release bool   `json:"release,omitempty"`
}

// Mouse is input.mouse.
type Mouse struct {
	Pane   string `json:"pane"`
	Kind   string `json:"kind"`   // "press", "release", "motion", "wheel"
	Button string `json:"button"` // "left", "middle", "right", "none"; wheel: "up", "down", "left-wheel", "right-wheel"
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Mod    int    `json:"mod"`
}

// Paste is input.paste: one chunk of at most MaxPaste bytes.
type Paste struct {
	Pane string `json:"pane"`
	Text string `json:"text"`
	More bool   `json:"more,omitempty"` // every chunk but the last
}

// Focus is input.focus: the outer terminal gained or lost focus.
type Focus struct {
	Focused bool `json:"focused"`
}

func (Key) Type() string   { return TypeInputKey }
func (Mouse) Type() string { return TypeInputMouse }
func (Paste) Type() string { return TypeInputPaste }
func (Focus) Type() string { return TypeInputFocus }

// --- 4.2.7 Requests ---

// SessionOpen opens or finds the session of a directory.
type SessionOpen struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
}

// SessionOpenResult is the ok of session.open.
type SessionOpenResult struct {
	Session string `json:"session"`
	Created bool   `json:"created"`
}

// SessionClose ends a session, named by Session or Dir.
type SessionClose struct {
	Session string `json:"session,omitempty"`
	Dir     string `json:"dir,omitempty"`
}

// SessionCloseResult is the ok of session.close.
type SessionCloseResult struct {
	Closed bool `json:"closed"` // false when there was none
}

// TabNew opens a tab; Cwd absent: 3.3.
type TabNew struct {
	Session string `json:"session"`
	Cwd     string `json:"cwd,omitempty"`
}

// TabNewResult is the ok of tab.new.
type TabNewResult struct {
	Tab  string `json:"tab"`
	Pane string `json:"pane"`
}

// TabSelect selects a tab by one of Tab, Index (from 1) and Step (+1, -1;
// wraps).
type TabSelect struct {
	Session string `json:"session"`
	Tab     string `json:"tab,omitempty"`
	Index   int    `json:"index,omitempty"`
	Step    int    `json:"step,omitempty"`
}

// TabSelectResult is the ok of tab.select.
type TabSelectResult struct {
	Tab string `json:"tab"`
}

// TabClose closes a tab.
type TabClose struct {
	Tab string `json:"tab"`
}

// PaneSplit splits a pane; Direction is SplitRight or SplitDown.
type PaneSplit struct {
	Pane      string `json:"pane"`
	Direction string `json:"direction"`
	Cwd       string `json:"cwd,omitempty"`
}

// PaneSplitResult is the ok of pane.split: the new pane.
type PaneSplitResult struct {
	Pane string `json:"pane"`
}

// PaneFocus focuses a pane, or with Direction ("left", "right", "up",
// "down") moves the focus of the pane's tab from Pane.
type PaneFocus struct {
	Pane      string `json:"pane"`
	Direction string `json:"direction,omitempty"`
}

// PaneFocusResult is the ok of pane.focus.
type PaneFocusResult struct {
	Moved bool   `json:"moved"`
	Pane  string `json:"pane"`
}

// PaneResize moves a border of the pane by Cells (at least 1) in
// Direction ("left", "right", "up", "down"): the border on that side of
// the pane when it has a neighbour there, which grows the pane, else the
// border on the other side, which shrinks it. Each side keeps its minimum,
// so the border may move less than asked, or not at all.
type PaneResize struct {
	Pane      string `json:"pane"`
	Direction string `json:"direction"`
	Cells     int    `json:"cells"`
}

// PaneResizeResult is the ok of pane.resize: whether a border moved.
type PaneResizeResult struct {
	Moved bool `json:"moved"`
}

// PaneClose closes a pane.
type PaneClose struct {
	Pane string `json:"pane"`
}

// PaneText asks for the text between two positions.
type PaneText struct {
	Pane  string `json:"pane"`
	From  Pos    `json:"from"`
	To    Pos    `json:"to"`
	Block bool   `json:"block"`
}

// PaneTextResult is the ok of pane.text; Cut is set when the text was cut
// at MaxText.
type PaneTextResult struct {
	Text string `json:"text"`
	Cut  bool   `json:"cut"`
}

// Pos is a position: an absolute line number and a cell.
type Pos struct {
	Line int64 `json:"line"`
	Col  int   `json:"col"`
}

// PaneScroll is pane.scroll, a notification of interactive connections. Top
// nil means live.
type PaneScroll struct {
	Pane  string `json:"pane"`
	Top   *int64 `json:"top"`
	Marks []Pos  `json:"marks,omitempty"` // at most two
}

// Reflow is event.reflow: where a resize moved the scroll position and the
// marks of a connection. Top nil: the connection is live again; a mark with
// Line -1: its row is gone.
type Reflow struct {
	Pane  string `json:"pane"`
	Top   *int64 `json:"top"`
	Marks []Pos  `json:"marks,omitempty"`
}

func (SessionOpen) Type() string  { return TypeSessionOpen }
func (SessionClose) Type() string { return TypeSessionClose }
func (TabNew) Type() string       { return TypeTabNew }
func (TabSelect) Type() string    { return TypeTabSelect }
func (TabClose) Type() string     { return TypeTabClose }
func (PaneSplit) Type() string    { return TypePaneSplit }
func (PaneFocus) Type() string    { return TypePaneFocus }
func (PaneResize) Type() string   { return TypePaneResize }
func (PaneClose) Type() string    { return TypePaneClose }
func (PaneText) Type() string     { return TypePaneText }
func (PaneScroll) Type() string   { return TypePaneScroll }
func (Reflow) Type() string       { return TypeEventReflow }

// --- 4.2.8 Events and liveness ---

// Clipboard is event.clipboard: a program in the pane set the clipboard.
// Data travels as base64 and is at most MaxClip bytes decoded, never empty.
type Clipboard struct {
	Pane string `json:"pane"`
	Data []byte `json:"data"`
}

// Bell is event.bell: a program in the pane rang.
type Bell struct {
	Pane string `json:"pane"`
}

// Ping and Pong keep a connection alive (4.2.8).
type Ping struct{}
type Pong struct{}

func (Clipboard) Type() string { return TypeEventClipboard }
func (Bell) Type() string      { return TypeEventBell }
func (Ping) Type() string      { return TypePing }
func (Pong) Type() string      { return TypePong }
