package server

import (
	"regexp"
	"time"

	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/proto"
)

// The process scan (3.2.1). Each running pane's foreground process group is
// read once per ProcInterval, the focused pane of a displayed tab's every
// FgInterval (Pane.Info, one ioctl). A pane is busy while its foreground
// group is not its shell's. While any pane is busy, or still an agent
// (scanDue), the server reads the process table once per ProcInterval for
// all panes, on a goroutine of its own, and once at once whenever a pane's
// foreground group changed. The scan sets the pane's program (the group leader's name) and editor (some
// process of the group is vim, neovim or fzf), and hands each pane's group
// to the observer, at most once per ProcInterval per pane.

// editorName is the expression herdr-navigate uses without its tmux
// alternative (3.2.1): view, vim, nvim, lvim, vimx, vimdiff, nvimdiff, fzf, a
// leading g included.
var editorName = regexp.MustCompile(`^g?(view|l?n?vim?x?|fzf)(diff)?$`)

// isEditor reports whether a process of the group runs an editor.
func isEditor(group []platform.Proc) bool {
	for _, p := range group {
		if editorName.MatchString(p.Name) {
			return true
		}
	}
	return false
}

// groupOf returns the processes of the process group pgid and the name of
// its leader ("" when the leader is not among them).
func groupOf(procs []platform.Proc, pgid int) (group []platform.Proc, leader string) {
	for _, p := range procs {
		if p.Pgid != pgid {
			continue
		}
		group = append(group, p)
		if p.Pid == pgid {
			leader = p.Name
		}
	}
	return group, leader
}

// ticker posts the tick of the scan to the loop every FgInterval until the
// server ends. A tick that finds the mailbox full is skipped.
func (s *Server) ticker() {
	defer s.wg.Done()
	t := time.NewTicker(s.o.FgInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			select {
			case s.mail <- func(c *core) { c.tick(time.Now()) }:
			default:
			}
		case <-s.quit:
			return
		}
	}
}

// tick reads the foreground groups that are due: those of the focused panes
// of displayed tabs on every tick, every pane's once per ProcInterval. It
// starts the periodic scan while a pane is busy.
func (c *core) tick(now time.Time) {
	all := now.Sub(c.lastFgAll) >= c.s.o.ProcInterval
	if all {
		c.lastFgAll = now
	}
	focused := map[string]bool{}
	if !all {
		for _, cn := range c.reg.list() {
			if cn.view == "" {
				continue
			}
			if sess, ok := c.known.Sessions[cn.view]; ok {
				focused[c.known.Tabs[sess.Current].Focused] = true
			}
		}
	}
	for _, id := range c.paneIDs() {
		if all || focused[id] {
			ref := c.panes[id]
			c.foreground(id, ref, ref.p.Info())
		}
	}
	if c.scanDue() && now.Sub(c.lastScan) >= c.s.o.ProcInterval {
		c.scanNow()
	}
}

// foreground takes a pane's foreground group from info: a change sets busy
// at once (a structural change, 4.2.2) and starts a scan at once.
func (c *core) foreground(id string, ref paneRef, info pane.Info) {
	if info.State != proto.PaneRunning || info.Fg <= 0 {
		return
	}
	if c.fg[id] == info.Fg {
		return
	}
	c.fg[id] = info.Fg
	busy := info.Fg != info.Shell
	c.apply(c.model.SetPane(id, func(p *proto.Pane) {
		p.Busy = busy
		if !busy {
			p.Editor = false
		}
	}), true)
	c.scanNow()
}

// scanDue reports whether the periodic scan runs: while a running pane's
// foreground group is not its shell's, or a running pane is still an agent.
// The second keeps the polls coming after an agent hands the foreground
// back to the shell, until the tracker has seen it gone (3.6.1 counts the
// polls without it): else the pane would stay an agent, and its
// conversation would be resumed after a restart (resume.go).
func (c *core) scanDue() bool {
	for id := range c.panes {
		p, ok := c.known.Panes[id]
		if !ok || p.State != proto.PaneRunning {
			continue
		}
		if _, agent := c.known.Agents[id]; p.Busy || agent {
			return true
		}
	}
	return false
}

// scanNow starts a scan of the process table, or one more after the one
// that runs.
func (c *core) scanNow() {
	if c.scanning {
		c.scanAgain = true
		return
	}
	c.scanning = true
	c.lastScan = time.Now()
	read, s := c.procs, c.s
	go func() {
		procs, err := read()
		s.postSoon(func(c *core) { c.scanned(procs, err) })
	}()
}

// scanned applies a scan to every running pane: program, editor and busy
// from its foreground group, and the group to the observer.
func (c *core) scanned(procs []platform.Proc, err error) {
	c.scanning = false
	if err != nil {
		c.s.log.Warn("reading the process table failed", "err", err)
	} else {
		now := time.Now()
		for _, id := range c.paneIDs() {
			ref := c.panes[id]
			info := ref.p.Info()
			if info.State != proto.PaneRunning || info.Fg <= 0 {
				continue
			}
			group, leader := groupOf(procs, info.Fg)
			c.fg[id] = info.Fg
			busy, editor := info.Fg != info.Shell, isEditor(group)
			c.apply(c.model.SetPane(id, func(p *proto.Pane) {
				if leader != "" {
					p.Program = leader
				}
				p.Busy, p.Editor = busy, editor
			}), false)
			// An empty group is handed over only for an agent: its polls without
			// the agent end it, also when its group can no longer be read.
			_, isAgent := c.known.Agents[id]
			if c.observer != nil && (len(group) > 0 || isAgent) && now.Sub(c.lastObserved[id]) >= c.s.o.ProcInterval {
				c.lastObserved[id] = now
				c.observer.Foreground(c, id, group)
			}
		}
	}
	if c.scanAgain {
		c.scanAgain = false
		c.scanNow()
	}
}
