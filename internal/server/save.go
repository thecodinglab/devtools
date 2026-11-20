package server

import (
	"devtools/internal/proto"
	"devtools/internal/server/persist"
)

// Saving state.json (3.9). The loop marks the state unsaved on every change
// of what the file holds and, at most SaveDelay after the first unsaved
// change, hands a copy of the structure to the saver goroutine, which writes
// it through persist. The saver writes the newest copy only: one it has not
// started on is replaced by a newer one. The synchronous save at shutdown
// (shutdown.go) is the last write: nothing is handed to the saver after the
// loop gave the state to the stop, and the saver writes nothing after it.

// changed marks the state unsaved when changes touch what state.json holds:
// anything but agent records (3.9).
func (c *core) changed(changes []proto.Change) {
	for _, ch := range changes {
		if ch.Kind != proto.KindAgent {
			c.unsaved()
			return
		}
	}
}

// unsaved marks the state unsaved and schedules its save: a change of the
// wire state (changed) or of what only state.json holds (SetResume).
func (c *core) unsaved() {
	if c.s.o.StateDir == "" {
		return
	}
	c.saveDirty = true
	if !c.saveArmed {
		c.saveArmed = true
		c.after(c.s.o.SaveDelay, func(c *core) {
			c.saveArmed = false
			c.saveSoon()
		})
	}
}

// saveSoon hands the structure to the saver when it is unsaved.
func (c *core) saveSoon() {
	if !c.saveDirty || c.final {
		return
	}
	c.saveDirty = false
	st := c.model.Save()
	select {
	case <-c.s.saves: // the saver had not started on the older copy
	default:
	}
	c.s.saves <- st
}

// saver writes what the loop hands it until the server ends.
func (s *Server) saver() {
	defer s.wg.Done()
	for {
		select {
		case st := <-s.saves:
			s.saveMu.Lock()
			if !s.saveDone {
				if err := persist.Save(s.statePath(), st); err != nil {
					s.log.Error("saving the state failed", "file", s.statePath(), "err", err)
				}
			}
			s.saveMu.Unlock()
		case <-s.quit:
			return
		}
	}
}
