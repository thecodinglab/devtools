package server

import (
	"errors"
	"fmt"
	"os"

	"devtools/internal/handover"
	"devtools/internal/pane"
	"devtools/internal/proto"
	"devtools/internal/server/model"
	"devtools/internal/vt"
)

// The new image's half of the live upgrade (upgrade.go): Stage builds the
// server from the payload without serving, reading a terminal, writing to
// one, or reaping a child; Commit starts it. Anything that fails before
// Commit leaves the inherited descriptors and the children exactly as the
// old image left them, so the process can be given back to the old build.

// Staged is a server adopted from a payload that does not run yet.
type Staged struct {
	s      *Server
	header handover.Header
	panes  []stagedPane
	ext    []func() // the extensions' state, applied at Commit
}

type stagedPane struct {
	h      *handle
	staged *pane.Staged
	gen    uint64
}

// Stage builds the server of a payload. masters are the inherited pane
// terminals by their number in the header; the caller keeps them open
// until Commit or, on an error, until it gave the process back.
func Stage(o Options, r *handover.Reader, masters map[int]*os.File) (st *Staged, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("server: staging panicked: %v", p)
		}
	}()
	o = withDefaults(o)
	h := r.Header()
	m, err := model.Adopt(h.State)
	if err != nil {
		return nil, err
	}
	s := newServer(o, h.Boot)
	s.core = newCore(s, m)
	c := s.core
	// The same boot promises the windows the same server: the revisions of
	// the state go on, or a window would take the new snapshot for an old
	// one.
	c.out.rev = h.Rev
	for sid, col := range h.Colors {
		c.colors[sid] = vt.Colors{Fg: col.Fg, Bg: col.Bg, Dark: col.Dark}
	}
	for k, v := range h.HelloEnv {
		c.helloEnv[k] = v
	}
	// staged, not the result st: a "return nil, err" clears st before the
	// deferred functions run, and what was staged must still be freed.
	staged := &Staged{s: s, header: h}
	st = staged
	defer func() {
		if p := recover(); p != nil {
			staged.Discard()
			panic(p) // to the recover above, which makes it the error
		}
		if err != nil {
			staged.Discard()
		}
	}()
	snap := m.Snapshot()
	inModel := map[string]proto.Pane{}
	for _, p := range snap.Panes {
		inModel[p.ID] = p
	}
	seen := map[string]bool{}
	for _, hp := range h.Panes {
		mp, ok := inModel[hp.ID]
		if !ok || mp.State == proto.PaneDormant {
			return nil, fmt.Errorf("server: the payload's pane %s is not a live pane of its model", hp.ID)
		}
		seen[hp.ID] = true
		screen, err := r.Section(hp.Screen)
		if err != nil {
			return nil, err
		}
		if hp.Screen.Len > 0 && !isScreenFormat(hp.Screen.Format) {
			return nil, fmt.Errorf("server: pane %s: a screen of format %q", hp.ID, hp.Screen.Format)
		}
		pending, err := r.Section(hp.Pending)
		if err != nil {
			return nil, err
		}
		hd := newHandle(hp.ID)
		hd.setCols(hp.Cols)
		po, gen := c.paneOptions(hd, hp.ID, mp.Cwd, mp.Session, hp.Cols, hp.Rows)
		info := pane.Info{Title: hp.Title, Cwd: hp.Cwd, Pid: hp.Pid, Shell: hp.Shell, Fg: hp.Fg, TTY: hp.TTY}
		if hp.Exit != nil {
			info.Exit = &pane.Exit{Code: hp.Exit.Code, Signal: hp.Exit.Signal, ByServer: hp.Exit.ByServer, Text: hp.Exit.Text}
		}
		var sp *pane.Staged
		switch hp.Kind {
		case handover.PaneExited:
			info.State = "exited"
			sp, err = pane.StageRestore(po, pane.Restored{Info: info, Screen: screen, First: hp.First, Base: hp.Base})
		default:
			f := masters[hp.Master]
			if f == nil {
				return nil, fmt.Errorf("server: pane %s: no inherited terminal %d", hp.ID, hp.Master)
			}
			kind := pane.FrozenRunning
			if hp.Kind == handover.PaneReaped {
				kind = pane.FrozenReaped
			}
			sp, err = pane.StageAdopt(po, pane.Adoption{Kind: kind, Master: f, Pid: hp.Pid, Info: info, Screen: screen, First: hp.First, Base: hp.Base, Pending: pending})
		}
		if err != nil {
			return nil, err
		}
		staged.panes = append(staged.panes, stagedPane{h: hd, staged: sp, gen: gen})
	}
	for _, p := range snap.Panes {
		if p.State != proto.PaneDormant && !seen[p.ID] {
			return nil, fmt.Errorf("server: pane %s is %s in the model and has no pane object in the payload", p.ID, p.State)
		}
	}
	if ho, ok := c.observer.(HandoverObserver); ok {
		for name, data := range h.Ext {
			apply, err := ho.StageHandover(c, name, data)
			if err != nil {
				return nil, fmt.Errorf("server: extension %s: %w", name, err)
			}
			staged.ext = append(staged.ext, apply)
		}
	} else if len(h.Ext) > 0 {
		return nil, errors.New("server: the payload carries extension state and this build has no extension to take it")
	}
	return staged, nil
}

func isScreenFormat(f string) bool {
	for _, x := range vt.ScreenFormats() {
		if x == f {
			return true
		}
	}
	return false
}

// Discard frees what staging built; the inherited descriptors and the
// children are not touched.
func (st *Staged) Discard() {
	for _, p := range st.panes {
		p.staged.Discard()
	}
	st.panes = nil
}

// Commit starts the adopted server: the extensions' state, then every
// pane's goroutines, then the loop. It cannot fail; after it the old image
// is no longer a valid rollback.
func (st *Staged) Commit() *Server {
	s, c := st.s, st.s.core
	for _, apply := range st.ext {
		apply()
	}
	for _, p := range st.panes {
		c.handles[p.h.id] = p.h
		c.attach(p.h, p.staged.Start(), p.gen)
		// A title or a directory the old image's loop had not applied yet
		// is in the pane's Info: the first thing the loop does is take it.
		id, gen := p.h.id, p.gen
		s.postSoon(func(c *core) { c.paneEvent(pane.Event{Pane: id, Kind: pane.InfoChanged}, gen) })
	}
	if u := st.header.Upgraded; u != nil {
		s.setLast(proto.Upgraded{ID: u.ID, From: u.From, To: u.To, At: u.At, Kept: u.Kept})
	}
	s.start()
	return s
}

// Server is the server Commit returns; before Commit it serves nothing.
func (st *Staged) Server() *Server { return st.s }

// SetUpgraded sets welcome.upgraded: boot says so when the server it
// starts is the old build again after the new one could not adopt, or a
// cold start after neither could.
func (s *Server) SetUpgraded(u proto.Upgraded) { s.setLast(u) }

// Socket is the path of the socket the payload's listener is bound to.
func (st *Staged) Socket() string { return st.header.Socket }

// Panes is the number of pane objects staged.
func (st *Staged) Panes() int { return len(st.panes) }

// Upgraded is welcome.upgraded of the server.
func (s *Server) Upgraded() *proto.Upgraded { return s.lastUpgrade() }
