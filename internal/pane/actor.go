package pane

import (
	"sync/atomic"
	"time"

	"devtools/internal/platform"
	"devtools/internal/vt"
)

// actor holds what only the pane's actor goroutine touches: the terminal
// and the state its callbacks record. The callbacks run inside Write and
// Resize on this goroutine; they copy what they were handed into the actor
// and return, and the actor acts on it after the call (3.2.1).
type actor struct {
	p    *Pane
	term vt.Term

	exited      bool
	spawnFailed bool // or restored exited: Exited is reported once at the start
	notifiedFg  int
	sizeGen     uint64

	// frozen for a live upgrade (handover.go): no input, no exit report,
	// and resizes wait for Thaw.
	frozen      bool
	resizeLater bool

	// recorded by the callbacks during one Write or Resize
	events   []Event
	title    *string
	cwd      *string
	recorded bool

	// synchronized output (mode 2026)
	syncSince time.Time
	pending   []snapReq
	syncTimer *time.Timer
	syncC     <-chan time.Time

	// counters for the tests
	parsed  atomic.Int64 // chunks parsed
	resizes atomic.Int64 // resizes applied to the terminal
}

type snapReq struct {
	top      int64
	scrolled bool
	reply    chan Snap
}

func newActor(p *Pane) *actor {
	a := &actor{p: p}
	a.term = vt.New(a.options())
	return a
}

// options are the terminal options of the pane, with the callbacks that
// record into the actor.
func (a *actor) options() vt.Options {
	o := a.p.o
	return vt.Options{
		Cols:    o.Cols,
		Rows:    o.Rows,
		History: o.History,
		Colors:  o.Colors,
		OnTitle: func(s string) {
			a.title, a.recorded = &s, true
		},
		OnCwd: func(s string) {
			a.cwd, a.recorded = &s, true
		},
		OnClipboard: func(b []byte) {
			a.events = append(a.events, Event{Kind: Clipboard, Clipboard: append([]byte(nil), b...)})
			a.recorded = true
		},
		OnBell: func() {
			a.events = append(a.events, Event{Kind: Bell})
			a.recorded = true
		},
		OnAgent: func(r vt.AgentReport) {
			a.events = append(a.events, Event{Kind: AgentReport, Agent: r})
			a.recorded = true
		},
		OnReflow: o.OnReflow,
	}
}

// run is the actor's loop. It is the only caller of the terminal.
func (a *actor) run() {
	p := a.p
	defer close(p.actorDone)
	defer a.term.Close()
	var tick <-chan time.Time
	if p.master != nil {
		t := time.NewTicker(fgInterval)
		defer t.Stop()
		tick = t.C
	}
	if a.spawnFailed {
		p.notify(Event{Kind: Exited})
	}
	for {
		var in chan input
		if !a.exited && !a.frozen && !p.out.full() {
			in = p.in
		}
		exitc := p.exitc
		if a.frozen {
			exitc = nil
		}
		select {
		case b := <-p.chunks:
			a.write(b)
			p.ack <- struct{}{}
		case ev := <-in:
			a.input(ev)
		case <-p.room:
		case <-p.ops.signal:
			for _, op := range p.ops.take() {
				op(a)
			}
		case ex := <-exitc:
			// A chunk the reader already holds is output from before the
			// exit report: it is parsed first. Bounded, because a process
			// the child left behind may keep writing.
			for n := 0; n < 16; n++ {
				select {
				case b := <-p.chunks:
					a.write(b)
					p.ack <- struct{}{}
					continue
				default:
				}
				break
			}
			a.exit(ex)
		case <-a.syncC:
			a.syncC, a.syncTimer = nil, nil
			a.flushPending()
		case <-tick:
			a.checkFg()
		case <-p.poke:
			a.checkFg()
		case <-p.quit:
			if a.syncTimer != nil {
				a.syncTimer.Stop()
			}
			a.flushPending()
			for _, op := range p.ops.take() {
				op(a)
			}
			return
		}
	}
}

// write parses one chunk of output, moves the replies to the writer queue
// before anything else, then acts on what the callbacks recorded and wakes
// the watchers.
func (a *actor) write(b []byte) {
	a.term.Write(b)
	a.replies()
	a.parsed.Add(1)
	a.afterChange()
}

// replies moves the terminal's replies (DA, DSR, OSC answers) to the writer
// queue; called after every Write and every Resize.
func (a *actor) replies() {
	if r := a.term.Replies(); len(r) > 0 {
		a.p.out.put(r)
	}
}

// afterChange runs after the terminal changed: the synchronized output
// state, the recorded events, the watchers.
func (a *actor) afterChange() {
	a.syncState()
	a.deliver()
	a.p.wakeWatchers()
}

// deliver applies what the callbacks recorded and hands the events to
// Notify, title and directory as one InfoChanged.
func (a *actor) deliver() {
	if !a.recorded {
		return
	}
	p := a.p
	changed := false
	p.mu.Lock()
	if a.title != nil && *a.title != p.info.Title {
		p.info.Title, changed = *a.title, true
	}
	if a.cwd != nil && *a.cwd != p.info.Cwd {
		p.info.Cwd, changed = *a.cwd, true
	}
	p.mu.Unlock()
	events := a.events
	a.events, a.title, a.cwd, a.recorded = nil, nil, nil, false
	if changed {
		p.notify(Event{Kind: InfoChanged})
	}
	for _, e := range events {
		p.notify(e)
	}
}

// input encodes one input event for the program's modes at this moment and
// queues the bytes.
func (a *actor) input(ev input) {
	var b []byte
	switch ev.kind {
	case inKey:
		b = a.term.EncodeKey(ev.key)
	case inMouse:
		b = a.term.EncodeMouse(ev.mouse)
	case inPaste:
		b = a.term.EncodePaste(ev.text, ev.first, ev.last)
	case inFocus:
		b = a.term.EncodeFocus(ev.focused)
	}
	a.p.out.put(b)
}

// resize applies the newest requested size: the terminal first, its
// replies, then the PTY (3.2.1). Older requests that are still queued find
// nothing newer and do nothing. Options.OnReflow runs inside Term.Resize.
func (a *actor) resize() {
	p := a.p
	p.mu.Lock()
	cols, rows, gen := p.size[0], p.size[1], p.sizeGen
	p.mu.Unlock()
	if gen == a.sizeGen {
		return
	}
	if a.frozen {
		a.resizeLater = true
		return
	}
	a.sizeGen = gen
	if c, r := a.term.Size(); c == cols && r == rows {
		return
	}
	a.term.Resize(cols, rows)
	a.resizes.Add(1)
	a.replies()
	if p.master != nil && !a.exited {
		setSize(p.master, cols, rows)
	}
	p.mu.Lock()
	p.lastSize = [2]int{cols, rows}
	p.mu.Unlock()
	a.afterChange()
}

// syncState follows synchronized output: the moment it opened, and the
// snapshots that waited for it once it closed.
func (a *actor) syncState() {
	if a.term.SyncActive() {
		if a.syncSince.IsZero() {
			a.syncSince = time.Now()
		}
		return
	}
	a.syncSince = time.Time{}
	if a.syncTimer != nil {
		a.syncTimer.Stop()
		a.syncTimer, a.syncC = nil, nil
	}
	a.flushPending()
}

// holding reports whether a snapshot has to wait: synchronized output is
// open and opened less than SyncHold ago.
func (a *actor) holding() bool {
	return !a.syncSince.IsZero() && a.term.SyncActive() && time.Since(a.syncSince) < a.p.o.SyncHold
}

// snapshot answers a snapshot request, or parks it while the program holds
// synchronized output open, at most SyncHold from the moment it opened.
func (a *actor) snapshot(r snapReq) {
	if !a.holding() {
		r.reply <- a.snap(r.top, r.scrolled)
		return
	}
	a.pending = append(a.pending, r)
	if a.syncTimer == nil {
		a.syncTimer = time.NewTimer(time.Until(a.syncSince.Add(a.p.o.SyncHold)))
		a.syncC = a.syncTimer.C
	}
}

func (a *actor) flushPending() {
	for _, r := range a.pending {
		r.reply <- a.snap(r.top, r.scrolled)
	}
	a.pending = nil
}

// snap reads what a viewer shows. Scrolled, top is clamped to [first, base]
// and the rows are the lines from top on; live, they are the screen's rows.
func (a *actor) snap(top int64, scrolled bool) Snap {
	t := a.term
	cols, rows := t.Size()
	first, base := t.Range()
	s := Snap{
		Cols: cols, Rows: rows,
		Lines:    make([]string, rows),
		Cursor:   t.Cursor(),
		Modes:    t.Modes(),
		Scrolled: scrolled,
		First:    first, Base: base,
		Top: base,
	}
	if scrolled {
		s.Top = min(max(top, first), base)
		for i := range rows {
			s.Lines[i], _ = t.Line(s.Top + int64(i))
		}
		return s
	}
	for i := range rows {
		s.Lines[i] = t.Row(i)
	}
	return s
}

// exit records how the program ended and reports it, exactly once.
func (a *actor) exit(ex Exit) {
	p := a.p
	a.exited = true
	p.mu.Lock()
	p.info.State = "exited"
	p.info.Exit = &ex
	p.mu.Unlock()
	close(p.exited)
	p.notify(Event{Kind: Exited})
}

// checkFg reads the foreground process group of the PTY and reports a
// change as InfoChanged.
func (a *actor) checkFg() {
	p := a.p
	if p.master == nil || a.exited {
		return
	}
	fg, err := platform.ForegroundGroup(p.master)
	p.mu.Lock()
	if err == nil {
		p.info.Fg = fg
	} else {
		fg = p.info.Fg
	}
	p.mu.Unlock()
	if fg != a.notifiedFg {
		a.notifiedFg = fg
		p.notify(Event{Kind: InfoChanged})
	}
}
