package fleet

import (
	"context"
	"strconv"
	"time"

	"devtools/internal/hub"
	"devtools/internal/proto"
	"devtools/internal/remote"
	"devtools/internal/session"
	"devtools/internal/ui"
)

// Machines, their links and the supervision of 3.5.1 and 3.5.2.

const (
	backoffMin   = 500 * time.Millisecond
	backoffMax   = 120 * time.Second
	backoffReset = 60 * time.Second // online this long: the next failure starts the backoff again
	focusedRetry = 2 * time.Second  // while the terminal has focus, a transient failure is retried at least this often
	attentionGap = 30 * time.Second // a reason that needs the user is retried this often, without a login (3.5.2)
	idleClose    = 10 * time.Minute // an on-demand machine nobody uses is closed (3.5.1)
)

// machine is one machine of the fleet: the local one ("") or a configured
// alias. It is owned by the loop.
type machine struct {
	name    string // the config alias; "" is this machine
	local   bool
	connect bool // permanent: held for as long as the client runs
	index   int  // 0 the local machine, then config order
	dropped bool // the alias turned out to be this machine (3.5.1); left out for this run

	status  string // "off", "connecting", "online", "retrying", "attention"
	problem hub.Problem
	host    string // welcome.host, sanitised
	// the build of its server: welcome.version, and the build installed
	// there that waits for a restart (welcome.installed, event.update);
	// sanitised, kept after the link is gone
	version   string
	installed string

	// the link and its attempt; gen changes with every attempt, so what an
	// older link or dial still delivers is recognised and dropped
	gen      int
	link     remote.Link
	welcome  proto.Welcome
	dialing  bool
	cancel   context.CancelFunc
	attempt  *timer
	retry    *timer
	idle     *timer
	backoff  time.Duration
	lastTry  time.Time
	failedAt time.Time // the transient failure the scheduled retry is for
	retryAt  time.Time // when it dials
	onlineAt time.Time
	nextID   uint64

	// logging in (3.5.2): a remote dial may start a new ssh login, and so a
	// prompt of the ssh agent, only on a grant of the user's presence
	grant    grant
	loginTry bool // the last attempt could log in
	authHeld bool // its last login failed as auth: focus grants nothing until the hub or a switch does

	// what the machine reported, cleaned (clean.go); kept after the link
	// is gone until the next snapshot replaces it
	st     *proto.State
	order  []string          // session ids in snapshot order, new ones appended
	boot   string            // welcome.boot of the last online link
	busy   int               // agents not idle in the last state that was online
	atDrop map[string]string // pane → blocked or done, when the link dropped
	rung   map[string]string // pane → the status its bell was written for

	cat catalogState
}

// grant is what lets the next dial of a machine log in.
type grant int

const (
	noGrant    grant = iota
	focusGrant       // the terminal gained focus
	userGrant        // the fleet started, the hub opened or a switch goes there
)

// online reports whether the machine has a link with its snapshot.
func (mc *machine) online() bool { return mc.status == "online" }

// newID is the next request id on mc's link; ids start at 1 per link.
func (mc *machine) newID() uint64 {
	mc.nextID++
	return mc.nextID
}

// send sends m on mc's link; false when there is none or it is over.
func (f *Fleet) send(mc *machine, m proto.Msg) bool {
	if mc.link == nil {
		return false
	}
	return mc.link.Send(m) == nil
}

// supervised reports whether mc's link is kept up (3.5.2): the local link,
// permanent machines, and a machine whose session the window holds.
func (f *Fleet) supervised(mc *machine) bool {
	return mc.local || mc.connect || f.view.holds(mc.name)
}

// list is every machine that takes part: local first, then config order,
// without the ones left out.
func (f *Fleet) list() []*machine {
	out := make([]*machine, 0, len(f.machines))
	for _, mc := range f.machines {
		if !mc.dropped {
			out = append(out, mc)
		}
	}
	return out
}

// dial starts one attempt unless one runs or the link is up. The attempt
// runs in its own goroutine; the loop never waits on it.
func (f *Fleet) dial(mc *machine) {
	if mc.dropped || mc.dialing || mc.link != nil || f.ctx == nil || f.ctx.Err() != nil {
		return
	}
	mc.retry.stop()
	// only a grant lets a remote dial log in (3.5.2); the local socket
	// ignores the flag. The grant is used up by this dial.
	login := mc.local || mc.grant != noGrant
	if mc.grant == userGrant {
		mc.authHeld = false
	}
	mc.grant, mc.loginTry = noGrant, login
	mc.gen++
	gen := mc.gen
	ctx, cancel := context.WithCancel(f.ctx)
	mc.dialing, mc.cancel = true, cancel
	mc.lastTry = f.clock.Now()
	if mc.status == "off" {
		// a retry keeps its state: the status row says "retrying" until
		// the machine is online (3.5.2)
		mc.status = "connecting"
	}
	f.dirty = true
	mc.attempt = f.after(attemptLimit, func() {
		if mc.gen != gen {
			return
		}
		f.endAttempt(mc)
		f.failed(mc, &remote.Failure{Reason: remote.Unreachable, Text: "no answer within 30 s"})
	})
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Connecting})
	dialer, name := f.o.Dialer, mc.name
	go func() {
		link, fail := dialer.Dial(ctx, name, login)
		if link != nil && ctx.Err() != nil {
			// given up meanwhile: nobody will read it
			link.Close()
			link, fail = nil, &remote.Failure{Reason: remote.Unreachable, Text: "given up"}
		}
		f.post(func() { f.dialed(mc, gen, link, fail) })
	}()
}

// endAttempt forgets the running attempt and its link, if any.
func (f *Fleet) endAttempt(mc *machine) {
	mc.gen++
	mc.attempt.stop()
	mc.attempt = nil
	if mc.cancel != nil {
		mc.cancel()
		mc.cancel = nil
	}
	mc.dialing = false
	if mc.link != nil {
		mc.link.Close()
		mc.link = nil
	}
}

// dialed is the end of a dial.
func (f *Fleet) dialed(mc *machine, gen int, link remote.Link, fail *remote.Failure) {
	if mc.gen != gen || !mc.dialing {
		if link != nil {
			link.Close()
		}
		return
	}
	if fail != nil || link == nil {
		f.endAttempt(mc)
		if fail == nil {
			fail = &remote.Failure{Reason: remote.Protocol, Text: "no link"}
		}
		f.failed(mc, fail)
		return
	}
	w := link.Welcome()
	if f.isSelf(mc, w.Boot) {
		link.Close()
		f.endAttempt(mc)
		f.drop(mc)
		return
	}
	mc.link, mc.welcome, mc.nextID = link, w, 0
	mc.host = cleanHost(w.Host)
	mc.version, mc.installed = cleanVersion(w.Version), cleanVersion(w.Installed)
	f.pump(mc, gen, link)
	// online once the first snapshot is read (3.5.2); the attempt limit
	// runs until then
}

// pump forwards the messages of link to the loop, one at a time.
func (f *Fleet) pump(mc *machine, gen int, link remote.Link) {
	stop := f.stop
	ch := f.linkCh
	go func() {
		for m := range link.Recv() {
			select {
			case ch <- linkMsg{mc: mc, gen: gen, msg: m}:
			case <-stop:
				return
			}
		}
		select {
		case ch <- linkMsg{mc: mc, gen: gen, end: true}:
		case <-stop:
		}
	}()
}

// linkMessage handles one message, or the end, of a link.
func (f *Fleet) linkMessage(lm linkMsg) {
	mc := lm.mc
	if mc.gen != lm.gen || mc.link == nil {
		return
	}
	if lm.end {
		fail := mc.link.Err()
		f.lost(mc, fail)
		return
	}
	m := lm.msg
	if len(m.Body) > proto.MaxMessage {
		f.lost(mc, &remote.Failure{Reason: remote.Protocol, Text: "a message above 4 MiB"})
		return
	}
	if !mc.online() {
		if m.T != proto.TypeStateSnapshot || m.ID != 0 {
			f.lost(mc, &remote.Failure{Reason: remote.Protocol, Text: "the server sent " + cleanText(m.T) + " before state.snapshot"})
			return
		}
		var s proto.Snapshot
		if err := m.Decode(&s); err != nil {
			f.lost(mc, &remote.Failure{Reason: remote.Protocol, Text: "unreadable state.snapshot"})
			return
		}
		f.cameOnline(mc, s)
		return
	}
	switch m.T {
	case proto.TypeStateSnapshot:
		if m.ID != 0 {
			f.view.receive(mc, m)
			return
		}
		var s proto.Snapshot
		if err := m.Decode(&s); err != nil {
			f.lost(mc, &remote.Failure{Reason: remote.Protocol, Text: "unreadable state.snapshot"})
			return
		}
		f.replaceState(mc, s)
		f.knownStatuses(mc)
		f.stateChanged(mc)
	case proto.TypeStateDelta:
		var d proto.Delta
		if err := m.Decode(&d); err != nil {
			f.lost(mc, &remote.Failure{Reason: remote.Protocol, Text: "unreadable state.delta"})
			return
		}
		if err := f.applyDelta(mc, d); err != nil {
			f.lost(mc, &remote.Failure{Reason: remote.Protocol, Text: "state out of order"})
			return
		}
		f.stateChanged(mc)
	case proto.TypeEventAgent:
		var e proto.AgentEvent
		if m.Decode(&e) == nil {
			f.agentEvent(mc, e)
		}
	case proto.TypeEventUpdate:
		var e proto.UpdateEvent
		if m.Decode(&e) == nil && cleanVersion(e.Installed) != mc.installed {
			mc.installed = cleanVersion(e.Installed)
			f.dirty = true
			f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Build, Version: mc.version, Installed: mc.installed})
		}
	case proto.TypeCatalogItem, proto.TypeCatalogGit, proto.TypeCatalogError, proto.TypeCatalogDone:
		f.catalogMessage(mc, m)
	case proto.TypePing, proto.TypePong:
		// the link answers these itself
	default:
		f.view.receive(mc, m)
	}
}

// cameOnline is the first snapshot of a link: the link is online (3.5.2).
func (f *Fleet) cameOnline(mc *machine, s proto.Snapshot) {
	mc.attempt.stop()
	mc.attempt = nil
	mc.dialing = false
	mc.cancel = nil
	first := mc.boot == ""
	sameBoot := !first && mc.boot == mc.welcome.Boot
	mc.boot = mc.welcome.Boot
	f.replaceState(mc, s)
	mc.status, mc.problem = "online", hub.Problem{}
	mc.grant, mc.authHeld = noGrant, false
	mc.onlineAt = f.clock.Now()
	f.dirty = true
	if first || !sameBoot {
		f.newServer(mc)
	} else {
		f.reconnectBells(mc)
	}
	mc.atDrop = nil
	if f.colors != nil {
		f.send(mc, *f.colors)
	}
	if mc.local {
		for _, other := range f.machines {
			if !other.local && other.link != nil && f.isSelf(other, other.welcome.Boot) {
				f.endAttempt(other)
				f.drop(other)
			}
		}
	}
	if f.hub != nil {
		f.hubOnline(mc)
	} else if !mc.local && !mc.connect && !f.view.holds(mc.name) {
		f.startIdle(mc)
	}
	f.view.linkUp(mc, sameBoot)
}

// isSelf reports whether a configured machine answered with the boot id of
// the local server (3.5.1).
func (f *Fleet) isSelf(mc *machine, boot string) bool {
	if mc.local || boot == "" {
		return false
	}
	local := f.machines[0]
	return local.boot != "" && local.boot == boot
}

// drop leaves mc out for this run, with one notice.
func (f *Fleet) drop(mc *machine) {
	if mc.dropped {
		return
	}
	mc.dropped = true
	mc.retry.stop()
	mc.idle.stop()
	mc.status, mc.problem = "off", hub.Problem{}
	mc.st, mc.order, mc.busy = nil, nil, 0
	f.dirty = true
	f.notice(ui.Notice("self", mc.name))
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Closed})
}

// lost ends mc's link: it dropped, sent something it must not, or fell
// silent (fail says which; nil when it just ended).
func (f *Fleet) lost(mc *machine, fail *remote.Failure) {
	wasOnline := mc.online()
	if wasOnline {
		mc.atDrop = urgentPanes(mc.st)
		mc.busy = notIdle(mc.st)
	}
	f.endAttempt(mc)
	f.stopCatalog(mc, false)
	if fail == nil {
		fail = &remote.Failure{Reason: "lost", Text: "the connection ended"}
	}
	if wasOnline && f.clock.Now().Sub(mc.onlineAt) >= backoffReset {
		mc.backoff = backoffMin
	}
	f.failed(mc, fail)
}

// transient reports whether a reason is retried with backoff rather than
// waiting for the user (the table of 3.5.2).
func transient(mc *machine, r remote.Reason) bool {
	switch r {
	case remote.Auth, remote.HostKey, remote.NoDevtools, remote.Incompatible:
		return false
	case remote.NoServer:
		// locally the service manager restarts the server
		return mc.local
	}
	return true
}

// failed puts mc in the state a failure leaves it in (3.5.2): retrying or
// attention when supervised, off otherwise.
func (f *Fleet) failed(mc *machine, fail *remote.Failure) {
	p := hub.Problem{Reason: string(fail.Reason), Text: cleanText(fail.Text)}
	if fail.Reason == remote.Auth && !mc.local {
		// a dismissed or locked ssh agent is not asked again by itself (3.5.2)
		mc.authHeld, mc.grant = true, noGrant
	}
	switch {
	case !f.supervised(mc):
		mc.status, p.Retry = "off", "next-open"
	case fail.Reason == remote.NeedsLogin && !mc.local:
		// waiting for the user, not for a fix: the check is local and
		// cheap, and another window may have logged in meanwhile (3.5.2)
		mc.status, p.Retry = "attention", "login"
		f.backOff(mc)
	case transient(mc, fail.Reason):
		mc.status, p.Retry = "retrying", "retrying"
		f.backOff(mc)
	default:
		mc.status, p.Retry = "attention", "in-30s"
		f.scheduleRetry(mc, attentionGap)
	}
	mc.problem = p
	f.dirty = true
	f.hubEvent(hub.Event{Machine: mc.name, Kind: hub.Failed, Err: p})
	f.view.linkDown(mc, p)
}

// backOff schedules the retry of a transient failure: the backoff, at most
// focusedRetry while the terminal has focus.
func (f *Fleet) backOff(mc *machine) {
	d := mc.backoff
	mc.backoff = min(mc.backoff*2, backoffMax)
	if f.focused {
		d = min(d, focusedRetry)
	}
	mc.failedAt = f.clock.Now()
	f.scheduleRetry(mc, d)
}

// backingOff reports whether mc waits for a retry on the backoff schedule.
func backingOff(mc *machine) bool {
	return mc.status == "retrying" || (mc.status == "attention" && mc.problem.Retry == "login")
}

// loginHelps reports whether mc's problem is one a new ssh login may
// solve: the hub dials it again at once instead of waiting attentionGap.
func loginHelps(mc *machine) bool {
	return mc.problem.Reason == string(remote.Auth) || mc.problem.Reason == string(remote.NeedsLogin)
}

// scheduleRetry dials mc again after d, if it is still supervised then.
func (f *Fleet) scheduleRetry(mc *machine, d time.Duration) {
	mc.retry.stop()
	mc.retryAt = f.clock.Now().Add(d)
	mc.retry = f.after(d, func() {
		mc.retry = nil
		if mc.dropped || mc.link != nil || mc.dialing {
			return
		}
		if !f.supervised(mc) && f.hub == nil {
			mc.status = "off"
			if mc.problem.Retry != "" {
				mc.problem.Retry = "next-open"
			}
			f.dirty = true
			return
		}
		f.dial(mc)
	})
}

// grantLogin lets the next dial of mc log in (3.5.2). A grant of the user
// (the hub, a switch) ends an attempt that cannot log in and has no link
// yet, so the dial that follows can. Focus grants nothing to a machine
// whose last login failed as auth.
func (f *Fleet) grantLogin(mc *machine, g grant) {
	if mc.local || mc.dropped || mc.online() || (mc.dialing && mc.loginTry) {
		return
	}
	if g == focusGrant && mc.authHeld {
		return
	}
	mc.grant = max(mc.grant, g)
	if g == userGrant && mc.dialing && mc.link == nil {
		f.endAttempt(mc)
	}
}

// focusGrants is the terminal gaining focus: every supervised machine that
// is not online may log in on its next dial.
func (f *Fleet) focusGrants() {
	for _, mc := range f.list() {
		if f.supervised(mc) {
			f.grantLogin(mc, focusGrant)
		}
	}
}

// hurryRetries brings the retries of transient failures, and of a needed
// login, forward when the terminal gains focus: none dials later than
// focusedRetry after its failure, and one that is overdue by that dials
// now.
func (f *Fleet) hurryRetries() {
	now := f.clock.Now()
	for _, mc := range f.list() {
		if !backingOff(mc) || !mc.retry.active() {
			continue
		}
		due := mc.failedAt.Add(focusedRetry)
		if due.Before(mc.retryAt) {
			f.scheduleRetry(mc, max(due.Sub(now), 0))
		}
	}
}

// disconnect closes mc's link and stops whatever runs for it.
func (f *Fleet) disconnect(mc *machine) {
	if mc.online() {
		mc.busy = notIdle(mc.st)
	}
	f.endAttempt(mc)
	mc.retry.stop()
	mc.idle.stop()
	mc.cat.silence.stop()
}

// startIdle starts the 10-minute timer of an on-demand machine (3.5.1).
// It does nothing for the local and permanent machines, and while the hub
// is open or the window holds the machine.
func (f *Fleet) startIdle(mc *machine) {
	if mc.local || mc.connect || mc.dropped || f.hub != nil || f.view.holds(mc.name) {
		return
	}
	if mc.link == nil && !mc.dialing && !mc.retry.active() {
		return
	}
	if mc.idle.active() {
		return
	}
	mc.idle = f.after(idleClose, func() {
		mc.idle = nil
		if f.hub != nil || f.view.holds(mc.name) || mc.dropped {
			return
		}
		had := mc.link != nil
		f.disconnect(mc)
		mc.status, mc.problem = "off", hub.Problem{}
		f.dirty = true
		if had {
			f.notice(ui.Notice("closed-idle", mc.name))
		}
	})
}

// stopIdle stops the idle timer of every machine.
func (f *Fleet) stopIdle() {
	for _, mc := range f.machines {
		mc.idle.stop()
		mc.idle = nil
	}
}

// cleanHost is welcome.host as it may be shown (SEC7, SEC8): secondary
// text, never a key.
func cleanHost(h string) string { return session.Sanitize(h, maxHost) }

// cleanVersion is a version another machine reports, as it may be shown:
// secondary text, never a key.
func cleanVersion(v string) string { return session.Sanitize(v, maxVersion) }

// itoa formats a bound for a note.
func itoa(n int) string { return strconv.Itoa(n) }
