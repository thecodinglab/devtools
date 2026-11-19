package fleet

import "devtools/internal/session"

// The history of what the window showed (3.5.4 step 7): the targets of the
// last eight commits, newest first, without duplicates. It is what the
// window falls back to when the displayed session ends (view.ended) or does
// not come back after a reconnect (3.5.5).

// historyLen is how many targets the history keeps.
const historyLen = 8

// histEntry is one committed target. A session is identified by its
// machine and its directory: a directory has at most one session, and its
// id is only valid while the server that gave it runs (welcome.boot).
type histEntry struct {
	machine string
	session string // the server's id, valid while the machine's boot is boot
	dir     string
	boot    string
	label   string
}

// history is owned by the loop.
type history struct {
	entries []histEntry // newest first
}

// same reports whether two entries name the same session.
func (e histEntry) same(o histEntry) bool {
	if e.machine != o.machine {
		return false
	}
	if e.dir != "" && o.dir != "" {
		return e.dir == o.dir
	}
	return e.boot == o.boot && e.session == o.session
}

// push puts e first; an older entry of the same session goes, and the
// history keeps historyLen entries.
func (h *history) push(e histEntry) {
	kept := make([]histEntry, 0, historyLen)
	kept = append(kept, e)
	for _, o := range h.entries {
		if len(kept) == historyLen {
			break
		}
		if !o.same(e) {
			kept = append(kept, o)
		}
	}
	h.entries = kept
}

// remove takes out every entry of the session e names.
func (h *history) remove(e histEntry) {
	kept := h.entries[:0]
	for _, o := range h.entries {
		if !o.same(e) {
			kept = append(kept, o)
		}
	}
	h.entries = kept
}

// fallback is the newest entry that is an existing session of an online
// machine, as a target of that machine (3.5.4: view.ended; 3.5.5: neither
// the session nor its directory came back). An entry of a machine that
// restarted is found by its directory, because its session id is stale.
func (h *history) fallback(f *Fleet) (session.Target, bool) {
	for _, e := range h.entries {
		mc := f.byName[e.machine]
		if mc == nil || mc.dropped || !mc.online() || mc.st == nil {
			continue
		}
		if mc.welcome.Boot == e.boot {
			if _, ok := mc.st.Sessions[e.session]; ok {
				return session.Target{Machine: e.machine, Session: e.session}, true
			}
		}
		if id, ok := sessionOfDir(mc, e.dir); ok {
			return session.Target{Machine: e.machine, Session: id}, true
		}
	}
	return session.Target{}, false
}

// sessionOfDir is the id of mc's session of the directory dir.
func sessionOfDir(mc *machine, dir string) (string, bool) {
	if mc.st == nil || dir == "" {
		return "", false
	}
	for _, id := range mc.order {
		if s, ok := mc.st.Sessions[id]; ok && s.Dir == dir {
			return id, true
		}
	}
	return "", false
}
