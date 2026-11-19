package persist

import (
	"os"
	"path/filepath"
	"strconv"

	"devtools/internal/proto"
	"devtools/internal/session"
)

// DefaultCols and DefaultRows are the size of a session that has none
// (3.3).
const DefaultCols, DefaultRows = 120, 40

// Restore prepares a loaded state for a new server (3.9): a session whose
// directory no longer exists is dropped, a session's directory is keyed
// again (session.Key: a changed symlink can move it), and a pane whose
// directory is gone starts in the session directory. Then Repair. That
// every pane is dormant is the model's part: this form has no pane state.
func Restore(s State) State {
	kept := make([]Session, 0, len(s.Sessions))
	for _, sess := range s.Sessions {
		key, err := session.Key(sess.Dir)
		if err != nil || sess.Dir == "" {
			continue
		}
		sess.Dir = key
		tabs := make([]Tab, len(sess.Tabs))
		for i, t := range sess.Tabs {
			panes := make([]Pane, len(t.Panes))
			for j, p := range t.Panes {
				if !isDir(p.Cwd) {
					p.Cwd = key
				}
				panes[j] = p
			}
			t.Panes = panes
			tabs[i] = t
		}
		sess.Tabs = tabs
		kept = append(kept, sess)
	}
	s.Sessions = kept
	return Repair(s)
}

func isDir(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// Repair makes a state hold the invariants of 3.3 without looking at the
// file system, so that a file edited by hand or written by a defect cannot
// put the model into a state it does not expect:
//
//   - an object whose id is not a valid id of its kind, or repeats an id
//     seen before in the file, is dropped;
//   - a session without a directory, or with the directory of an earlier
//     session, is dropped;
//   - a tab keeps at most its first two panes; a tab without panes, and a
//     session without tabs, is dropped;
//   - split is "" with one pane and "right" or "down" with two ("right"
//     when it was anything else); focused is one of the tab's panes (else
//     the first); current is one of the session's tabs (else the first);
//   - the size is clamped to the limits of 4.1, a size of zero is the
//     default 120 by 40; the label is sanitised; a pane without a directory
//     starts in the session directory;
//   - each counter is at least the largest id of its kind in the file,
//     dropped objects included.
//
// The version of the result is Version.
func Repair(s State) State {
	out := State{Version: Version, Counters: s.Counters, Sessions: []Session{}}
	seen := map[string]bool{}
	dirs := map[string]bool{}
	bump := func(c *uint64, id string) {
		if n := idNumber(id); n > *c {
			*c = n
		}
	}
	// Every valid id of the file counts for the counters, also that of an
	// object that is dropped: an id is never issued twice.
	for _, sess := range s.Sessions {
		if validID(sess.ID, 's') {
			bump(&out.Counters.Session, sess.ID)
		}
		for _, t := range sess.Tabs {
			if validID(t.ID, 't') {
				bump(&out.Counters.Tab, t.ID)
			}
			for _, p := range t.Panes {
				if validID(p.ID, 'p') {
					bump(&out.Counters.Pane, p.ID)
				}
			}
		}
	}
	for _, sess := range s.Sessions {
		if !validID(sess.ID, 's') || seen[sess.ID] || sess.Dir == "" || dirs[sess.Dir] {
			continue
		}
		var tabs []Tab
		for _, t := range sess.Tabs {
			if !validID(t.ID, 't') || seen[t.ID] {
				continue
			}
			var panes []Pane
			for _, p := range t.Panes {
				if len(panes) == 2 {
					break
				}
				if !validID(p.ID, 'p') || seen[p.ID] {
					continue
				}
				seen[p.ID] = true
				if p.Cwd == "" {
					p.Cwd = sess.Dir
				}
				panes = append(panes, p)
			}
			if len(panes) == 0 {
				continue
			}
			seen[t.ID] = true
			t.Panes = panes
			switch {
			case len(panes) == 1:
				t.Split = proto.SplitNone
			case t.Split != proto.SplitRight && t.Split != proto.SplitDown:
				t.Split = proto.SplitRight
			}
			if !hasPane(panes, t.Focused) {
				t.Focused = panes[0].ID
			}
			tabs = append(tabs, t)
		}
		if len(tabs) == 0 {
			continue
		}
		seen[sess.ID] = true
		dirs[sess.Dir] = true
		sess.Tabs = tabs
		if !hasTab(tabs, sess.Current) {
			sess.Current = tabs[0].ID
		}
		if sess.Cols == 0 && sess.Rows == 0 {
			sess.Cols, sess.Rows = DefaultCols, DefaultRows
		}
		sess.Cols, sess.Rows = proto.ClampSize(sess.Cols, sess.Rows)
		sess.Label = session.Sanitize(sess.Label, session.MaxLabel)
		if sess.Shown < 0 {
			sess.Shown = 0
		}
		out.Sessions = append(out.Sessions, sess)
	}
	return out
}

func hasPane(ps []Pane, id string) bool {
	for _, p := range ps {
		if p.ID == id {
			return true
		}
	}
	return false
}

func hasTab(ts []Tab, id string) bool {
	for _, t := range ts {
		if t.ID == id {
			return true
		}
	}
	return false
}

func validID(id string, kind byte) bool {
	return session.ValidID(id) && id[0] == kind && idNumber(id) > 0
}

// idNumber is the number of an id ("p52" is 52), 0 when it has none.
func idNumber(id string) uint64 {
	if len(id) < 2 {
		return 0
	}
	n, err := strconv.ParseUint(id[1:], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
