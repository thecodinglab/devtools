package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"devtools/internal/handover"
	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/server/persist"
	"devtools/internal/vt"
)

// The live upgrade (internal/handover, PLAN-live-upgrade.md). The server
// replaces its image with the build installed behind its program path
// (Options.Program, the profile's symlink) in the same process, so the
// programs of the panes stay its children and keep running:
//
//  1. preflight: the installed build says what it can take over
//     (devtools server upgrade-check), and decodes a sample screen;
//  2. the handover state: the loop holds the model (core.held), accepting
//     pauses, the input gate closes, the saver stops;
//  3. every pane freezes at an exact cut and its terminal is encoded;
//  4. the model, the panes and the extensions' state go into the payload,
//     state.json is saved, and boot (Options.Exec) duplicates the kept
//     descriptors and execs the new build;
//  5. a failure at any step before the exec rolls everything back.
//
// The new image adopts what the payload names (adopt.go).

// Limits of the live upgrade.
const (
	checkLimit    = 64 << 10        // output of upgrade-check
	checkTimeout  = 5 * time.Second // each run of upgrade-check
	barrierWait   = 2 * time.Second // the input gate and the panes that are closing
	freezeWait    = time.Second     // one pane's Freeze
	freezeAtOnce  = 4               // panes frozen at the same time
	ackWait       = 200 * time.Millisecond
	upgradeAbort  = 3 * time.Second // Shutdown waits this long for an attempt to roll back
	sampleCols    = 20
	sampleRows    = 5
	sampleTitle   = "devtools-sample"
	sampleTail    = "-end"
	sampleRowText = "devtools sample"
)

// Handover is what the server hands boot (Options.Exec) once everything is
// frozen: the header and the sections of the payload, the masters to keep,
// and Accept, which tells the requester the attempt is going ahead.
type Handover struct {
	Attempt string
	Target  platform.Identity // the build to exec
	Header  handover.Header   // Panes[i].Master is filled by boot
	Data    *handover.Writer
	Masters []*os.File // per Header.Panes; nil for an exited pane
	// Accept replies ok to the requester and waits, at most ackWait, for
	// its acknowledgement. Boot calls it right before the exec.
	Accept func()
	// Verify reports whether Target is still the file the preflight checked
	// and, for the automatic upgrade, still the installed build; boot calls
	// it before Accept and again right before the exec. An error of the
	// first call is an attempt that did not begin.
	Verify func() error
	// Auto is set for an attempt of the automatic upgrade (the journal).
	Auto bool
}

// ErrHandedOver is what an Exec of the tests returns when another server
// of the same process took over: the old server then gives up its panes
// and its connections without touching the programs.
var ErrHandedOver = errors.New("server: handed over in this process")

// upgradeState is the attempt in progress and the outcome of the last one.
type upgradeState struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	acked   chan struct{} // closed by server.upgrade.ack
	ackID   string
	last    *proto.Upgraded
	refused map[string]bool // targets the preflight refused
}

// upgradeReq is one attempt: from server.upgrade (cn set) or the automatic
// trigger (token set).
type upgradeReq struct {
	cn    *conn
	id    uint64
	force bool
	gens  []int
	token *candidate // the automatic trigger's candidate, rechecked under hold
	// idle is the time without input the automatic trigger waited for,
	// checked again when the model is held (input may have come during
	// the preflight); 0 after the hour of waiting, and for server.upgrade.
	idle time.Duration
}

// upgradeError is the outcome of an attempt that changed nothing, with its
// code for the requester.
type upgradeError struct{ code, text string }

func (e *upgradeError) Error() string { return e.code + ": " + e.text }

func upgradeErr(code, format string, args ...any) error {
	return &upgradeError{code: code, text: fmt.Sprintf(format, args...)}
}

// canUpgrade reports whether the server can upgrade itself live.
func (s *Server) canUpgrade() bool { return s.o.Exec != nil && s.o.Program != "" }

// lastUpgrade is welcome.upgraded.
func (s *Server) lastUpgrade() *proto.Upgraded {
	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	if s.up.last == nil {
		return nil
	}
	u := *s.up.last
	return &u
}

func (s *Server) setLast(u proto.Upgraded) {
	s.up.mu.Lock()
	s.up.last = &u
	s.up.mu.Unlock()
}

// serverUpgrade is server.upgrade, on the loop: a local one-shot asks; the
// attempt runs on a goroutine of its own and answers it.
func (c *core) serverUpgrade(cn *conn, m proto.Msg) {
	if m.ID == 0 {
		return
	}
	s := c.s
	if !s.canUpgrade() {
		c.Fail(cn, m.ID, proto.CodeUnsupported, "this server cannot upgrade itself live")
		return
	}
	if cn.remote {
		c.Fail(cn, m.ID, proto.CodeUnsupported, "server.upgrade is for a local connection only")
		return
	}
	var q proto.ServerUpgrade
	if m.Decode(&q) != nil {
		c.Fail(cn, m.ID, proto.CodeInvalid, "server.upgrade does not decode")
		return
	}
	req := upgradeReq{cn: cn, id: m.ID, force: q.Force && s.o.UpgradeForce, gens: c.connectedGens(cn)}
	if _, err := s.beginUpgrade(req); err != nil {
		var ue *upgradeError
		errors.As(err, &ue)
		c.Fail(cn, m.ID, ue.code, ue.text)
	}
}

// connectedGens are the protocol generations of every interactive
// connection and of the requester: the new build must speak each, or
// those windows could not come back.
func (c *core) connectedGens(req *conn) []int {
	var gens []int
	for _, cn := range c.reg.list() {
		if cn.kind == proto.KindInteractive || cn == req {
			if !slices.Contains(gens, cn.hello.Gen) {
				gens = append(gens, cn.hello.Gen)
			}
		}
	}
	return gens
}

// unspoken are the generations of gens that speaks does not list.
func unspoken(speaks, gens []int) []int {
	var missing []int
	for _, g := range gens {
		if !slices.Contains(speaks, g) && !slices.Contains(missing, g) {
			missing = append(missing, g)
		}
	}
	return missing
}

// beginUpgrade starts an attempt unless one runs or the server stops; the
// channel is closed when this attempt ended.
func (s *Server) beginUpgrade(req upgradeReq) (<-chan struct{}, error) {
	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	if s.stopping.Load() {
		return nil, upgradeErr(proto.CodeBusy, "the server is stopping")
	}
	if s.up.running {
		return nil, upgradeErr(proto.CodeBusy, "an upgrade is in progress")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.up.running, s.up.cancel, s.up.done = true, cancel, make(chan struct{})
	s.up.acked, s.up.ackID = make(chan struct{}), ""
	done := s.up.done
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		defer cancel()
		s.upgrade(ctx, req)
		s.up.mu.Lock()
		s.up.running = false
		s.up.mu.Unlock()
	}()
	return done, nil
}

// abortUpgrade cancels an attempt in progress and waits until it rolled
// back; Shutdown calls it first, so a stop never meets frozen panes.
func (s *Server) abortUpgrade() {
	s.up.mu.Lock()
	running, cancel, done := s.up.running, s.up.cancel, s.up.done
	s.up.mu.Unlock()
	if !running {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(upgradeAbort):
		s.log.Error("upgrade: the attempt did not roll back in time")
	}
}

// upgradeAcked is server.upgrade.ack, from the reader of the requester.
func (s *Server) upgradeAcked(cn *conn, m proto.Msg) {
	var a proto.ServerUpgradeAck
	if m.Decode(&a) != nil {
		return
	}
	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	if s.up.running && a.ID != "" && a.ID == s.up.ackID {
		select {
		case <-s.up.acked:
		default:
			close(s.up.acked)
		}
	}
}

// reply answers the requester of an attempt, on the loop.
func (s *Server) reply(req upgradeReq, result any, err error) {
	if req.cn == nil {
		return
	}
	s.onLoop(func(c *core) {
		if req.cn.gone {
			return
		}
		if err != nil {
			var ue *upgradeError
			if !errors.As(err, &ue) {
				ue = &upgradeError{code: proto.CodeInternal, text: err.Error()}
			}
			c.Fail(req.cn, req.id, ue.code, ue.text)
			return
		}
		c.Reply(req.cn, req.id, result)
	})
}

// upgrade runs one attempt.
func (s *Server) upgrade(ctx context.Context, req upgradeReq) {
	target, err := s.resolveTarget(req)
	if err != nil {
		s.reply(req, nil, err)
		return
	}
	check, choice, err := s.preflight(ctx, target.Path, req.gens)
	if err != nil {
		s.log.Warn("upgrade: preflight refused", "target", target.Path, "err", err.Error())
		s.noteRefusal(target.Path, err)
		if req.token != nil {
			s.autoRefused(ctx, target, check, err)
		}
		s.reply(req, nil, err)
		return
	}
	id := newAttemptID()
	s.log.Info("upgrade: handing over", "attempt", id, "target", target.Path, "to", check.Version)
	h, rollback, err := s.prepare(ctx, req, id, target, check, choice)
	if err != nil {
		s.log.Warn("upgrade: not now", "attempt", id, "err", err.Error())
		s.reply(req, nil, err)
		return
	}
	h.Auto = req.token != nil
	h.Verify = func() error { return s.verifyTarget(req, target) }
	replied := false
	h.Accept = func() {
		replied = true
		s.up.mu.Lock()
		s.up.ackID = id
		acked := s.up.acked
		s.up.mu.Unlock()
		s.reply(req, proto.ServerUpgradeResult{ID: id, From: s.o.Version, To: check.Version, Panes: h.Header.Upgraded.Kept}, nil)
		if req.cn == nil {
			return
		}
		select {
		case <-acked:
		case <-time.After(ackWait):
			s.log.Info("upgrade: the requester did not acknowledge", "attempt", id)
		}
	}
	err = s.o.Exec(h)
	if errors.Is(err, ErrHandedOver) {
		s.abandon()
		return
	}
	rollback()
	var changed *upgradeError
	if errors.As(err, &changed) && !replied {
		// Verify before Accept: the attempt did not begin.
		s.log.Warn("upgrade: not now", "attempt", id, "err", err.Error())
		s.reply(req, nil, err)
		return
	}
	if err == nil {
		err = errors.New("the exec returned")
	}
	s.log.Error("upgrade: the exec failed; the server goes on", "attempt", id, "err", err.Error())
	s.setLast(proto.Upgraded{ID: id, From: s.o.Version, To: check.Version, At: time.Now().UnixMilli(), Error: err.Error()})
	if !replied {
		s.reply(req, nil, upgradeErr(proto.CodeBusy, "%v", err))
	}
}

// resolveTarget is the build of an attempt: for the automatic upgrade the
// exact candidate the watch settled on, as long as it is unchanged and
// still the installed build; else the installed build now (target).
func (s *Server) resolveTarget(req upgradeReq) (platform.Identity, error) {
	c := req.token
	if c == nil {
		return s.target(req.force)
	}
	now, err := platform.FileIdentity(c.id.Path)
	if err != nil || !sameCandidate(now, c.id) || !c.still() {
		return platform.Identity{}, upgradeErr(proto.CodeBusy, "the installed build changed again")
	}
	return c.id, nil
}

// verifyTarget checks that target is still the file the preflight checked
// (a local build replaced since is refused, not exec'd) and, for the
// automatic upgrade, that Program still resolves to it.
func (s *Server) verifyTarget(req upgradeReq, target platform.Identity) error {
	now, err := platform.FileIdentity(target.Path)
	if err != nil || !sameCandidate(now, target) {
		return upgradeErr(proto.CodeBusy, "the build %s changed after it was checked", target.Path)
	}
	if req.token != nil && !req.token.still() {
		return upgradeErr(proto.CodeBusy, "the installed build changed again")
	}
	return nil
}

// target is the installed build: Options.Program resolved. The running
// build itself is CodeCurrent unless force.
func (s *Server) target(force bool) (platform.Identity, error) {
	id, err := platform.FileIdentity(s.o.Program)
	if err != nil {
		return platform.Identity{}, upgradeErr(proto.CodeIncompatible, "the installed devtools %s cannot be read: %v", s.o.Program, err)
	}
	if id.Same(s.o.Self) && !force {
		return platform.Identity{}, upgradeErr(proto.CodeCurrent, "the server runs the installed build already")
	}
	return id, nil
}

// localCheck is what this build can take over: the same answer
// upgrade-check gives for it.
func LocalCheck(version string) handover.Check {
	return handover.Check{
		Handover: handover.Versions,
		Screens:  vt.ScreenFormats(),
		Gens:     proto.Gens(),
		Version:  version,
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		Engine:   "libghostty-vt",
	}
}

// preflight asks the target what it reads, negotiates, and has it decode a
// sample screen of the chosen format.
func (s *Server) preflight(ctx context.Context, path string, gens []int) (handover.Check, handover.Choice, error) {
	run := s.o.Check
	if run == nil {
		env := s.checkEnv()
		run = func(ctx context.Context, path string, args []string) ([]byte, error) {
			return platform.RunCheck(ctx, path, args, env, checkLimit)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	out, err := run(cctx, path, []string{"server", "upgrade-check", "--json"})
	cancel()
	if err != nil {
		return handover.Check{}, handover.Choice{}, upgradeErr(proto.CodeIncompatible, "it does not answer upgrade-check (%v)", err)
	}
	var target handover.Check
	if err := json.Unmarshal(out, &target); err != nil {
		return handover.Check{}, handover.Choice{}, upgradeErr(proto.CodeIncompatible, "its upgrade-check printed no answer (%v)", err)
	}
	choice, err := handover.Choose(LocalCheck(s.o.Version), target, gens)
	if err != nil {
		return target, handover.Choice{}, upgradeErr(proto.CodeIncompatible, "%v", err)
	}
	sample, want, err := MakeSample()
	if err != nil {
		return target, handover.Choice{}, upgradeErr(proto.CodeInternal, "the sample screen: %v", err)
	}
	f, err := os.CreateTemp(s.sampleDir(), "upgrade-sample-*")
	if err != nil {
		return target, handover.Choice{}, upgradeErr(proto.CodeInternal, "the sample screen: %v", err)
	}
	defer os.Remove(f.Name())
	f.Write(sample)
	f.Close()
	cctx, cancel = context.WithTimeout(ctx, checkTimeout)
	out, err = run(cctx, path, []string{"server", "upgrade-check", "--json", "--sample", f.Name(), "--sample-format", choice.Screen})
	cancel()
	if err != nil {
		return target, handover.Choice{}, upgradeErr(proto.CodeIncompatible, "it cannot read this server's screens (%v)", err)
	}
	var got handover.Sample
	if err := json.Unmarshal(out, &got); err != nil || got != want {
		return target, handover.Choice{}, upgradeErr(proto.CodeIncompatible, "it reads this server's screens differently (%s)", strings.TrimSpace(string(out)))
	}
	return target, choice, nil
}

// checkEnv is the environment of upgrade-check: the server's, without the
// inventory of a handover.
func (s *Server) checkEnv() []string {
	var env []string
	for _, kv := range s.o.Env {
		if !strings.HasPrefix(kv, handover.EnvVar+"=") {
			env = append(env, kv)
		}
	}
	return env
}

func (s *Server) sampleDir() string {
	if s.o.StateDir != "" {
		return s.o.StateDir
	}
	return os.TempDir()
}

// MakeSample is the sample screen of the preflight: a small terminal with
// a title, bracketed paste, the alternate screen and a title sequence left
// unfinished, encoded, and what a reader must find in it.
func MakeSample() ([]byte, handover.Sample, error) {
	t := vt.New(vt.Options{Cols: sampleCols, Rows: sampleRows, History: 100})
	defer t.Close()
	t.Write([]byte("primary\r\n\x1b[?2004h\x1b[?1049h\x1b[H" + sampleRowText + "\x1b]2;" + sampleTitle))
	b, err := vt.Encode(t)
	if err != nil {
		return nil, handover.Sample{}, err
	}
	return b, handover.Sample{Title: sampleTitle + sampleTail, Paste: true, Alt: true, Row: sampleRowText}, nil
}

// ReadSample is the other half: the target restores the sample, finishes
// the title and says what it found (upgrade-check --sample).
func ReadSample(format string, data []byte) (handover.Sample, error) {
	if !slices.Contains(vt.ScreenFormats(), format) {
		return handover.Sample{}, fmt.Errorf("this build does not read screens as %q", format)
	}
	var title string
	t, err := vt.Restore(vt.Options{Cols: sampleCols, Rows: sampleRows, History: 100, OnTitle: func(s string) { title = s }}, data, 0, 0)
	if err != nil {
		return handover.Sample{}, err
	}
	defer t.Close()
	t.Write([]byte(sampleTail + "\x07"))
	text, _, _ := t.Text(0)
	m := t.Modes()
	return handover.Sample{Title: title, Paste: m.Paste, Alt: m.Alt, Row: text}, nil
}

func newAttemptID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("server: random attempt id: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// frozenPane is one pane of the handover.
type frozenPane struct {
	id string
	p  Pane
	f  pane.Frozen
}

// prepare enters the handover state, freezes the panes and collects the
// payload. On an error everything is rolled back already; on success the
// rollback is returned for a failed exec.
func (s *Server) prepare(ctx context.Context, req upgradeReq, id string, target platform.Identity, check handover.Check, choice handover.Choice) (*Handover, func(), error) {
	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		undo = nil
	}
	fail := func(err error) (*Handover, func(), error) {
		rollback()
		return nil, nil, err
	}

	// The model holds still: the loop takes nothing but the handover's own
	// functions from here on.
	var refs map[string]paneRef
	var holdErr error
	if !s.onLoop(func(c *core) {
		if s.stopping.Load() {
			holdErr = upgradeErr(proto.CodeBusy, "the server is stopping")
			return
		}
		if req.token != nil && !req.token.still() {
			holdErr = upgradeErr(proto.CodeBusy, "the installed build changed again")
			return
		}
		if req.idle > 0 {
			for _, cn := range c.reg.list() {
				if cn.kind == proto.KindInteractive && time.Since(cn.lastInput) < req.idle {
					holdErr = upgradeErr(proto.CodeBusy, "a window had input again")
					return
				}
			}
		}
		// The windows that are connected now are the ones that must come
		// back: held, the loop admits no other until the rollback or the
		// exec.
		if missing := unspoken(check.Gens, c.connectedGens(req.cn)); len(missing) > 0 {
			holdErr = upgradeErr(proto.CodeIncompatible, "it does not speak protocol %v, which connected windows use", missing)
			return
		}
		c.held = true
		refs = make(map[string]paneRef, len(c.panes))
		for id, ref := range c.panes {
			refs[id] = ref
		}
	}) {
		return fail(upgradeErr(proto.CodeBusy, "the server is stopping"))
	}
	if holdErr != nil {
		return fail(holdErr)
	}
	undo = append(undo, func() { s.onLoop(func(c *core) { c.held = false }) })

	// Panes that are closing (core.release) still have children; the
	// handover waits for them, or does not happen now.
	if err := s.waitRetired(ctx, refs); err != nil {
		return fail(err)
	}

	// The input gate: after it no input or paste reaches a pane.
	if err := s.closeGate(ctx); err != nil {
		return fail(err)
	}
	undo = append(undo, s.openGate)

	// Connections wait in the listen backlog.
	s.pauseAccept()
	undo = append(undo, s.resumeAccept)

	// The saver writes nothing more; the save below is the last.
	s.saveMu.Lock()
	s.savePaused = true
	s.saveMu.Unlock()
	undo = append(undo, func() {
		s.saveMu.Lock()
		s.savePaused = false
		s.saveMu.Unlock()
	})

	frozen, err := s.freezeAll(ctx, refs)
	undo = append(undo, func() {
		for _, fp := range frozen {
			fp.p.Thaw()
		}
	})
	if err != nil {
		return fail(err)
	}
	if s.o.UpgradeDelay != nil {
		if err := s.o.UpgradeDelay(ctx, "freeze"); err != nil {
			return fail(upgradeErr(proto.CodeBusy, "%v", err))
		}
	}

	h := &Handover{Attempt: id, Target: target, Data: &handover.Writer{}}
	var st persist.State
	var collectErr error
	s.onLoop(func(c *core) {
		h.Header = handover.Header{
			From:     handover.Build{Version: s.o.Version, Path: s.o.Self.Path, Pid: os.Getpid(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
			Created:  time.Now().UnixMilli(),
			Boot:     s.boot,
			Rev:      c.out.rev,
			Socket:   s.socketPath(),
			Attempt:  id,
			State:    c.model.Handover(),
			HelloEnv: c.helloEnvCopy(),
			Colors:   map[string]handover.Colors{},
			Ext:      map[string]json.RawMessage{},
		}
		for sid, col := range c.colors {
			h.Header.Colors[sid] = handover.Colors{Fg: col.Fg, Bg: col.Bg, Dark: col.Dark}
		}
		if ho, ok := c.observer.(HandoverObserver); ok {
			name, data, err := ho.SaveHandover(c)
			if err != nil {
				collectErr = err
			} else if name != "" {
				h.Header.Ext[name] = data
			}
		}
		st = c.model.Save()
		// Every running and exited pane of the model has its object.
		for _, p := range h.Header.State.Panes {
			if p.State != proto.PaneDormant {
				if _, ok := refs[p.ID]; !ok {
					collectErr = fmt.Errorf("pane %s is %s without a pane object", p.ID, p.State)
				}
			}
		}
	})
	if collectErr != nil {
		return fail(upgradeErr(proto.CodeBusy, "%v", collectErr))
	}
	kept := 0
	for _, fp := range frozen {
		// What the reader of the payload refuses, the attempt refuses now,
		// while the panes can still be thawed: a program that does not read
		// while its terminal's replies pile up has more pending than a
		// payload carries.
		if n := len(fp.f.Pending); n > handover.MaxPending {
			return fail(upgradeErr(proto.CodeBusy, "pane %s has %d bytes of input its program has not read", fp.id, n))
		}
		if n := len(fp.f.Screen); n > handover.MaxScreen {
			return fail(upgradeErr(proto.CodeBusy, "the screen of pane %s is %d bytes", fp.id, n))
		}
		hp := handover.Pane{
			ID: fp.id, Kind: fp.f.Kind, Pid: fp.f.Pid, Shell: fp.f.Info.Shell, Fg: fp.f.Info.Fg, Master: -1,
			TTY: fp.f.Info.TTY, Cols: fp.f.Cols, Rows: fp.f.Rows, Title: fp.f.Info.Title, Cwd: fp.f.Info.Cwd,
			First: fp.f.First, Base: fp.f.Base,
		}
		if ex := fp.f.Info.Exit; ex != nil {
			hp.Exit = &handover.Exit{Code: ex.Code, Signal: ex.Signal, ByServer: ex.ByServer, Text: ex.Text}
		}
		if hp.Screen, err = h.Data.Add(choice.Screen, fp.f.Screen); err != nil {
			return fail(upgradeErr(proto.CodeBusy, "%v", err))
		}
		if hp.Pending, err = h.Data.Add(handover.Raw, fp.f.Pending); err != nil {
			return fail(upgradeErr(proto.CodeBusy, "%v", err))
		}
		if fp.f.Kind == pane.FrozenExited {
			hp.Pid = max(hp.Pid, 0)
			h.Masters = append(h.Masters, nil)
		} else {
			h.Masters = append(h.Masters, fp.f.Master)
			kept++
		}
		h.Header.Panes = append(h.Header.Panes, hp)
	}
	if err := platform.RoomFor(len(h.Masters) + 4); err != nil {
		return fail(upgradeErr(proto.CodeIncompatible, "%v", err))
	}
	h.Header.Upgraded = &handover.Upgraded{ID: id, From: s.o.Version, To: check.Version, At: time.Now().UnixMilli(), Kept: kept}

	// The last save before the exec: what a cold start would resume.
	if s.o.StateDir != "" {
		s.saveMu.Lock()
		err := persist.Save(s.statePath(), st)
		s.saveMu.Unlock()
		if err != nil {
			s.log.Error("upgrade: saving the state failed", "err", err.Error())
		}
	}
	return h, rollback, nil
}

// waitRetired waits until every pane object the server still tracks is one
// of the model's; a pane that is closing has a child the payload would not
// name.
func (s *Server) waitRetired(ctx context.Context, refs map[string]paneRef) error {
	inModel := make(map[Pane]bool, len(refs))
	for _, r := range refs {
		inModel[r.p] = true
	}
	deadline := time.Now().Add(barrierWait)
	for {
		s.mu.Lock()
		retiring := 0
		for p := range s.live {
			if !inModel[p] {
				retiring++
			}
		}
		s.mu.Unlock()
		if retiring == 0 {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return upgradeErr(proto.CodeBusy, "%d panes are still closing", retiring)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// closeGate closes the input gate once no reader is applying input, and
// refuses while a paste is open on any connection: its closing bracket
// would be lost with the connection.
func (s *Server) closeGate(ctx context.Context) error {
	deadline := time.Now().Add(barrierWait)
	for !s.inputMu.TryLock() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return upgradeErr(proto.CodeBusy, "input is still being delivered")
		}
		time.Sleep(2 * time.Millisecond)
	}
	defer s.inputMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for cn := range s.conns {
		if cn.pasteOpen.Load() {
			return upgradeErr(proto.CodeBusy, "a paste is in progress")
		}
	}
	s.gated = true
	return nil
}

func (s *Server) openGate() {
	s.inputMu.Lock()
	s.gated = false
	s.inputMu.Unlock()
}

// deadliner is a listener whose Accept a deadline ends (*net.UnixListener,
// boot's peer listener).
type deadliner interface{ SetDeadline(time.Time) error }

// pauseAccept makes Accept stop: connections that arrive wait in the
// listen backlog.
func (s *Server) pauseAccept() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumed = make(chan struct{})
	s.paused.Store(true)
	if d, ok := s.ln.(deadliner); ok {
		d.SetDeadline(time.Unix(1, 0))
	}
}

func (s *Server) resumeAccept() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.paused.Load() {
		return
	}
	s.paused.Store(false)
	if d, ok := s.ln.(deadliner); ok {
		d.SetDeadline(time.Time{})
	}
	close(s.resumed)
}

// waitResumed is Serve's wait during a pause; false when the server ends.
func (s *Server) waitResumed() bool {
	s.mu.Lock()
	ch := s.resumed
	s.mu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-s.quit:
		<-s.stopped
		return false
	}
}

// freezeAll freezes every pane object of the model, a few at a time, in
// the order of their ids. On an error the panes frozen so far are
// returned, for the rollback to thaw.
func (s *Server) freezeAll(ctx context.Context, refs map[string]paneRef) ([]frozenPane, error) {
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sortIDs(ids)
	out := make([]frozenPane, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, freezeAtOnce)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fctx, cancel := context.WithTimeout(ctx, freezeWait)
			defer cancel()
			f, err := refs[id].p.Freeze(fctx)
			out[i], errs[i] = frozenPane{id: id, p: refs[id].p, f: f}, err
		}()
	}
	wg.Wait()
	var done []frozenPane
	var first error
	for i := range out {
		if errs[i] == nil {
			done = append(done, out[i])
		} else if first == nil {
			first = errs[i]
		}
	}
	if first != nil {
		return done, upgradeErr(proto.CodeBusy, "%v", first)
	}
	sort.Slice(done, func(i, j int) bool { return idLess(done[i].id, done[j].id) })
	return done, nil
}

// abandon ends this server after another server of the same process took
// its panes over (the in-process tests): the frozen panes are abandoned,
// the connections closed and the goroutines ended, without closing a
// program, the listener or the socket.
func (s *Server) abandon() {
	s.stopOnce.Do(func() {
		defer close(s.stopped)
		s.mu.Lock()
		s.stopping.Store(true)
		panes := make([]Pane, 0, len(s.live))
		for p := range s.live {
			panes = append(panes, p)
		}
		conns := make([]*conn, 0, len(s.conns))
		for cn := range s.conns {
			conns = append(conns, cn)
		}
		s.mu.Unlock()
		for _, p := range panes {
			p.Abandon()
		}
		for _, cn := range conns {
			cn.lane.Close(0)
			cn.nc.Close()
		}
		s.end()
	})
}

// HandoverObserver is what an Observer implements to carry its state
// across a live upgrade: SaveHandover runs on the old image's loop with the
// model held; StageHandover runs in the new image before it serves and
// returns what Commit applies. A state that StageHandover refuses makes the
// new image give the process back.
type HandoverObserver interface {
	SaveHandover(core Core) (name string, data json.RawMessage, err error)
	StageHandover(core Core, name string, data json.RawMessage) (apply func(), err error)
}
