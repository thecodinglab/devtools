package pane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/vt"
)

// Defaults of Options and fixed values of 3.2.1.
const (
	defaultKillGrace = 250 * time.Millisecond
	defaultSyncHold  = 150 * time.Millisecond

	// readSize is what the reader hands the actor at most per chunk.
	readSize = 8 << 10
	// maxQueue is the writer queue above which the actor takes no new input;
	// replies are queued even over it.
	maxQueue = 1 << 20
	// eofWait is how long the waiter waits for the reader's end of file
	// before it reports the exit, so the last output is parsed first.
	eofWait = 250 * time.Millisecond
)

// fgInterval is how often a running pane reads its foreground process group
// on its own (3.2.1: once a second). The server reads it more often for the
// focused pane of a displayed tab by calling Info, which reads it too.
// Tests shorten it.
var fgInterval = time.Second

// Pane is one pane: a child program on a PTY, parsed by a vt.Term (3.2.1).
// Four goroutines serve a running pane: the actor, the only caller of the
// terminal; the reader of the PTY master; the writer of the input queue;
// the waiter of the child.
type Pane struct {
	o      Options
	id     string
	master *os.File // nil when the program did not start
	pid    int      // 0 when the program did not start

	a *actor

	in     chan input    // input events, taken by the actor while the queue has room
	chunks chan []byte   // output read from the PTY
	ack    chan struct{} // the actor parsed the chunk
	exitc  chan Exit     // the waiter's report, buffered
	room   chan struct{} // the writer made room in the queue
	poke   chan struct{} // Info saw a new foreground group
	ops    opQueue
	out    outQueue

	mu       sync.Mutex
	info     Info
	size     [2]int // newest requested cols, rows
	sizeGen  uint64
	watchers map[int]*watcher
	nextW    int
	shut     bool // Close ended the watchers
	lastSize [2]int

	procMu  sync.Mutex
	reaped  bool
	reapedc chan struct{}
	closing atomic.Bool

	exited     chan struct{} // closed once the exit is known
	quit       chan struct{} // closed by Close: the actor ends
	actorDone  chan struct{}
	readerDone chan struct{}
	writerDone chan struct{}
	waiterDone chan struct{}
	closeOnce  sync.Once
	closed     chan struct{}
}

type inputKind int

const (
	inKey inputKind = iota
	inMouse
	inPaste
	inFocus
)

// input is one input event on its way to the actor, which encodes it.
type input struct {
	kind        inputKind
	key         vt.Key
	mouse       vt.Mouse
	text        string
	first, last bool
	focused     bool
}

// Start never fails: when the program cannot be started the pane is already
// exited and Info().Exit.Text says why.
func Start(o Options) *Pane {
	if o.KillGrace <= 0 {
		o.KillGrace = defaultKillGrace
	}
	if o.SyncHold <= 0 {
		o.SyncHold = defaultSyncHold
	}
	o.Cols, o.Rows = max(o.Cols, 1), max(o.Rows, 1)
	p := &Pane{
		o:          o,
		id:         o.ID,
		in:         make(chan input),
		chunks:     make(chan []byte),
		ack:        make(chan struct{}),
		exitc:      make(chan Exit, 1),
		room:       make(chan struct{}, 1),
		poke:       make(chan struct{}, 1),
		watchers:   map[int]*watcher{},
		reapedc:    make(chan struct{}),
		exited:     make(chan struct{}),
		quit:       make(chan struct{}),
		actorDone:  make(chan struct{}),
		readerDone: make(chan struct{}),
		writerDone: make(chan struct{}),
		waiterDone: make(chan struct{}),
		closed:     make(chan struct{}),
	}
	p.ops.init()
	p.out.init()
	p.size = [2]int{o.Cols, o.Rows}
	p.lastSize = p.size
	p.info = Info{State: "running"}
	p.a = newActor(p)

	if err := p.spawn(); err != nil {
		p.info = Info{State: "exited", Exit: &Exit{Code: -1, Text: err.Error()}}
		close(p.exited)
		close(p.reapedc)
		close(p.readerDone)
		close(p.writerDone)
		close(p.waiterDone)
		p.a.exited, p.a.spawnFailed = true, true
		go p.a.run()
		return p
	}
	go p.a.run()
	go p.read()
	go p.write()
	go p.wait()
	return p
}

// spawn opens the PTY and starts the program on it: IUTF8 set on the slave
// first, the child in a session of its own with the slave as its
// controlling terminal, the slave closed in the server right after, the
// master close-on-exec (3.2.1).
func (p *Pane) spawn() error {
	o := p.o
	if len(o.Argv) == 0 || o.Argv[0] == "" {
		return errors.New("no program to start")
	}
	path, err := lookPath(o.Argv[0], o.Env)
	if err != nil {
		return err
	}
	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	fail := func(err error) error {
		master.Close()
		slave.Close()
		return err
	}
	if err := setSize(master, o.Cols, o.Rows); err != nil {
		return fail(err)
	}
	if err := platform.SetIUTF8(slave); err != nil {
		return fail(err)
	}
	tty, err := platform.TTYName(slave)
	if err != nil {
		tty = slave.Name()
	}
	argv := append([]string(nil), o.Argv...)
	if o.Arg0 != "" {
		argv[0] = o.Arg0
	}
	fd := slave.Fd()
	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{
		Dir:   startDir(o.Dir, o.Env),
		Env:   withTTY(o.Env, tty),
		Files: []uintptr{fd, fd, fd},
		Sys:   &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0},
	})
	slave.Close()
	if err != nil {
		master.Close()
		return &os.PathError{Op: "start", Path: path, Err: err}
	}
	p.master, p.pid = master, pid
	p.info.Pid, p.info.Shell, p.info.TTY = pid, pid, tty
	if fg, err := platform.ForegroundGroup(master); err == nil {
		p.info.Fg = fg
	}
	p.a.notifiedFg = p.info.Fg
	return nil
}

// lookPath finds the program: a name with a slash is used as it is, any
// other is looked up in the PATH of the pane's environment.
func lookPath(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, executable(name)
	}
	path, _ := lookEnv(env, "PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		if p := filepath.Join(dir, name); executable(p) == nil {
			return p, nil
		}
	}
	return "", errors.New(name + ": not found in PATH")
}

func executable(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() || st.Mode()&0o111 == 0 {
		return &os.PathError{Op: "start", Path: path, Err: syscall.EACCES}
	}
	return nil
}

// startDir is the pane's directory if it is one, else the HOME of its
// environment, else the root. The server passes the pane's directory, or
// the session directory for a pane without one (3.2.1).
func startDir(dir string, env []string) string {
	isDir := func(d string) bool {
		st, err := os.Stat(d)
		return d != "" && err == nil && st.IsDir()
	}
	if isDir(dir) {
		return dir
	}
	if home, _ := lookEnv(env, "HOME"); isDir(home) {
		return home
	}
	return "/"
}

// ID is the pane's id.
func (p *Pane) ID() string { return p.id }

// send hands an input event to the actor. It blocks while the pane's input
// queue is full and returns false when the pane has exited.
func (p *Pane) send(ev input) bool {
	select {
	case <-p.exited:
		return false
	default:
	}
	select {
	case p.in <- ev:
		return true
	case <-p.exited:
		return false
	}
}

// Key types a key. The actor encodes it with Term.EncodeKey for the modes
// the program has at that moment.
func (p *Pane) Key(k vt.Key) bool { return p.send(input{kind: inKey, key: k}) }

// Mouse reports a mouse event; Term.EncodeMouse drops what the program did
// not ask for.
func (p *Pane) Mouse(e vt.Mouse) bool { return p.send(input{kind: inMouse, mouse: e}) }

// Paste sends one chunk of a paste; first and last place the brackets of
// mode 2004.
func (p *Pane) Paste(text string, first, last bool) bool {
	return p.send(input{kind: inPaste, text: text, first: first, last: last})
}

// Focus reports a focus change; nothing reaches a program without mode 1004.
func (p *Pane) Focus(focused bool) bool { return p.send(input{kind: inFocus, focused: focused}) }

// Resize never blocks; the newest size wins.
func (p *Pane) Resize(cols, rows int) {
	p.mu.Lock()
	p.size = [2]int{max(cols, 1), max(rows, 1)}
	p.sizeGen++
	p.mu.Unlock()
	p.ops.push(func(a *actor) { a.resize() })
}

// SetColors sets the colours the terminal answers OSC 10 and 11 with. It
// does not block.
func (p *Pane) SetColors(c vt.Colors) {
	p.ops.push(func(a *actor) { a.term.SetColors(c) })
}

// Follow and Unfollow are Term.Follow and Term.Unfollow, run on the pane's
// goroutine. Calls are counted: a line followed twice needs two Unfollow.
// Neither blocks.
func (p *Pane) Follow(abs int64) { p.ops.push(func(a *actor) { a.term.Follow(abs) }) }

// Unfollow ends one Follow of a line.
func (p *Pane) Unfollow(abs int64) { p.ops.push(func(a *actor) { a.term.Unfollow(abs) }) }

// Snapshot returns the rows a viewer shows: the live screen, or rows starting
// at absolute line top when scrolled; top is clamped to the retained range.
// It waits up to SyncHold while the program holds synchronized output open.
func (p *Pane) Snapshot(top int64, scrolled bool) Snap {
	reply := make(chan Snap, 1)
	p.ops.push(func(a *actor) { a.snapshot(snapReq{top: top, scrolled: scrolled, reply: reply}) })
	select {
	case s := <-reply:
		return s
	case <-p.actorDone:
		return p.blankSnap(scrolled)
	}
}

// blankSnap is the snapshot of a closed pane: its last size, blank rows.
func (p *Pane) blankSnap(scrolled bool) Snap {
	p.mu.Lock()
	cols, rows := p.lastSize[0], p.lastSize[1]
	p.mu.Unlock()
	return Snap{Cols: cols, Rows: rows, Lines: make([]string, rows), Scrolled: scrolled}
}

// Text returns the text between two positions (pane.text, 4.2.7), at most
// proto.MaxText bytes; cut is true when it was cut.
func (p *Pane) Text(from, to vt.Pos, block bool) (text string, cut bool) {
	type result struct {
		text string
		cut  bool
	}
	reply := make(chan result, 1)
	p.ops.push(func(a *actor) {
		t, c := vt.Extract(a.term, from, to, block, proto.MaxText)
		reply <- result{t, c}
	})
	select {
	case r := <-reply:
		return r.text, r.cut
	case <-p.actorDone:
		return "", false
	}
}

// Info returns a copy; safe from any goroutine. For a running pane it reads
// the foreground process group of the PTY at the call (one ioctl), so a
// caller that asks often sees a change at once; a change also reaches
// Notify as InfoChanged.
func (p *Pane) Info() Info {
	p.mu.Lock()
	running := p.info.State == "running"
	p.mu.Unlock()
	var fg int
	var fgErr error = errNoFg
	if running && p.master != nil {
		fg, fgErr = platform.ForegroundGroup(p.master)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if fgErr == nil && p.info.State == "running" && fg != p.info.Fg {
		p.info.Fg = fg
		select {
		case p.poke <- struct{}{}:
		default:
		}
	}
	info := p.info
	if info.Exit != nil {
		e := *info.Exit
		info.Exit = &e
	}
	return info
}

var errNoFg = errors.New("pane: no foreground group")

// watcher is one Watch: a goroutine that runs wake, and a signal of at most
// one pending wake, so the actor never waits for a watcher.
type watcher struct {
	c    chan struct{}
	quit chan struct{}
}

// Watch registers wake, which runs after output changed what Snapshot
// returns: at most once per parsed chunk, from a goroutine of the watcher's
// own, so a slow wake never holds up the PTY side. cancel ends it; Close
// ends every watcher.
func (p *Pane) Watch(wake func()) (cancel func()) {
	w := &watcher{c: make(chan struct{}, 1), quit: make(chan struct{})}
	p.mu.Lock()
	if p.shut {
		p.mu.Unlock()
		return func() {}
	}
	id := p.nextW
	p.nextW++
	p.watchers[id] = w
	p.mu.Unlock()
	go func() {
		for {
			select {
			case <-w.c:
				select {
				case <-w.quit:
					return
				default:
				}
				wake()
			case <-w.quit:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if _, ok := p.watchers[id]; ok {
				delete(p.watchers, id)
				close(w.quit)
			}
			p.mu.Unlock()
		})
	}
}

// wakeWatchers signals every watcher without waiting.
func (p *Pane) wakeWatchers() {
	p.mu.Lock()
	for _, w := range p.watchers {
		select {
		case w.c <- struct{}{}:
		default:
		}
	}
	p.mu.Unlock()
}

// notify hands an event to Options.Notify.
func (p *Pane) notify(e Event) {
	if p.o.Notify == nil {
		return
	}
	e.Pane = p.id
	p.o.Notify(e)
}

// Close ends the pane: SIGHUP to the child's process group, KillGrace,
// SIGTERM, KillGrace, SIGKILL, each step skipped once the child is reaped;
// it returns after the child is reaped, the last Exited event was given to
// Notify, every goroutine of the pane ended and the terminal was freed.
// Calling it again waits for the first call. It must not be called from
// Notify or from a wake of Watch.
func (p *Pane) Close() {
	p.closeOnce.Do(p.close)
	<-p.closed
}

func (p *Pane) close() {
	defer close(p.closed)
	if p.master != nil {
		p.closing.Store(true)
	steps:
		for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGKILL} {
			if !p.signal(sig) || sig == syscall.SIGKILL {
				break
			}
			t := time.NewTimer(p.o.KillGrace)
			select {
			case <-p.reapedc:
				t.Stop()
				break steps
			case <-t.C:
			}
		}
		<-p.reapedc
		// Closing the master ends the reader (and with it the waiter's
		// wait for the end of file) and a write that waits for room.
		p.master.Close()
		<-p.readerDone
		<-p.waiterDone
		<-p.exited
		p.out.shut()
		<-p.writerDone
	}
	close(p.quit)
	<-p.actorDone
	p.mu.Lock()
	p.shut = true
	for id, w := range p.watchers {
		delete(p.watchers, id)
		close(w.quit)
	}
	p.mu.Unlock()
}

// signal sends sig to the child's process group while the waiter has not
// collected the child; false when it has.
func (p *Pane) signal(sig syscall.Signal) bool {
	p.procMu.Lock()
	defer p.procMu.Unlock()
	if p.reaped {
		return false
	}
	syscall.Kill(-p.pid, sig)
	return true
}
