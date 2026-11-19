package server

import (
	"os"
	"path/filepath"

	"devtools/internal/proto"
	"devtools/internal/server/model"
	"devtools/internal/session"
)

// Requests (4.2.7) and the dispatch of everything the reader posts to the
// loop. The core answers session.open, session.close, tab.*, pane.split,
// pane.focus, pane.close and state.get here and the view messages in
// view.go; input, pane.scroll and pane.text never reach the loop (input.go);
// a registered extension answers its own types; any other request is
// answered unsupported and any other notification is ignored (4.1).

// request is the handler of one request type of the core, on the loop. It
// returns the ok result (nil for none) or an error.
type request func(c *core, cn *conn, m proto.Msg) (result any, err *model.Error)

// requests are the request types the core answers. Their names are also
// the types an extension cannot take over.
var requests = map[string]request{
	proto.TypeSessionOpen:  (*core).sessionOpen,
	proto.TypeSessionClose: (*core).sessionClose,
	proto.TypeTabNew:       (*core).tabNew,
	proto.TypeTabSelect:    (*core).tabSelect,
	proto.TypeTabClose:     (*core).tabClose,
	proto.TypePaneSplit:    (*core).paneSplit,
	proto.TypePaneFocus:    (*core).paneFocus,
	proto.TypePaneClose:    (*core).paneClose,
}

// handle runs one message of a connection on the loop.
func (c *core) handle(cn *conn, m proto.Msg) {
	if m.T == proto.TypeStateGet {
		if m.ID != 0 {
			c.send(cn, proto.Request(m.ID, c.snapshot()))
		}
		return
	}
	if c.viewMsg(cn, m) {
		return
	}
	if h, ok := requests[m.T]; ok {
		if m.ID == 0 {
			return // a request sent as a notification: nobody waits for an answer
		}
		result, err := h(c, cn, m)
		if err != nil {
			c.Fail(cn, m.ID, err.Code, err.Text)
			return
		}
		c.Reply(cn, m.ID, result)
		return
	}
	if h, ok := extension(m.T); ok {
		h(c, cn, m)
		return
	}
	if m.ID != 0 {
		c.Fail(cn, m.ID, proto.CodeUnsupported, "this server does not implement "+m.T)
	}
}

func invalid(text string) *model.Error { return &model.Error{Code: proto.CodeInvalid, Text: text} }

// decode decodes the body of m into v; a body that does not decode is
// invalid.
func decode(m proto.Msg, v any) *model.Error {
	if err := m.Decode(v); err != nil {
		return invalid("the request " + m.T + " does not decode")
	}
	return nil
}

// existingDir returns the session key of dir (session.Key): absolute,
// cleaned, symlinks resolved, and a directory. Relative paths are invalid;
// a path that is not a directory on this machine is no-dir.
func existingDir(dir string) (string, *model.Error) {
	if dir == "" {
		return "", invalid("dir is missing")
	}
	if !filepath.IsAbs(dir) {
		return "", invalid("dir must be an absolute path")
	}
	key, err := session.Key(dir)
	if err != nil {
		return "", &model.Error{Code: proto.CodeNoDir, Text: "directory " + dir + " does not exist"}
	}
	fi, err := os.Stat(key)
	if err != nil || !fi.IsDir() {
		return "", &model.Error{Code: proto.CodeNoDir, Text: dir + " is not a directory"}
	}
	return key, nil
}

// optionalDir checks the cwd of tab.new and pane.split: empty, or an
// absolute path. A directory that does not exist (any more) is not an
// error: the pane then starts in the session directory (3.3, 3.9).
func optionalDir(dir string) (string, *model.Error) {
	if dir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		return "", invalid("cwd must be an absolute path")
	}
	if key, err := session.Key(dir); err == nil {
		return key, nil
	}
	return filepath.Clean(dir), nil
}

// openSession opens or finds the session of a directory; it displays
// nothing.
func (c *core) openSession(dir, label string) (string, bool, *model.Error) {
	key, err := existingDir(dir)
	if err != nil {
		return "", false, err
	}
	id, created, r := c.model.OpenSession(key, session.Sanitize(label, session.MaxLabel))
	c.apply(r, true)
	return id, created, nil
}

func (c *core) sessionOpen(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.SessionOpen
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	id, created, err := c.openSession(q.Dir, q.Name)
	if err != nil {
		return nil, err
	}
	return proto.SessionOpenResult{Session: id, Created: created}, nil
}

func (c *core) sessionClose(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.SessionClose
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	id := q.Session
	if id == "" {
		if q.Dir == "" {
			return nil, invalid("session.close needs a session or a dir")
		}
		id = c.sessionOfDir(q.Dir)
	}
	return proto.SessionCloseResult{Closed: id != "" && c.CloseSession(id)}, nil
}

// sessionOfDir returns the session of a directory, "" when there is none.
// The directory is resolved when it still exists; a session whose
// directory was removed meanwhile is found by the cleaned path.
func (c *core) sessionOfDir(dir string) string {
	want := filepath.Clean(dir)
	if key, err := session.Key(dir); err == nil {
		want = key
	}
	for id, sess := range c.known.Sessions {
		if sess.Dir == want {
			return id
		}
	}
	return ""
}

func (c *core) tabNew(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.TabNew
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	cwd, err := optionalDir(q.Cwd)
	if err != nil {
		return nil, err
	}
	tab, paneID, r, err := c.model.NewTab(q.Session, cwd)
	if err != nil {
		return nil, err
	}
	c.apply(r, true)
	return proto.TabNewResult{Tab: tab, Pane: paneID}, nil
}

func (c *core) tabSelect(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.TabSelect
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	if q.Step < -1 || q.Step > 1 {
		return nil, invalid("step is +1 or -1")
	}
	id, r, err := c.model.SelectTab(q.Session, q.Tab, q.Index, q.Step)
	if err != nil {
		return nil, err
	}
	c.apply(r, true)
	return proto.TabSelectResult{Tab: id}, nil
}

func (c *core) tabClose(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.TabClose
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	r, err := c.model.CloseTab(q.Tab)
	if err != nil {
		return nil, err
	}
	c.apply(r, true)
	return nil, nil
}

func (c *core) paneSplit(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.PaneSplit
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	cwd, err := optionalDir(q.Cwd)
	if err != nil {
		return nil, err
	}
	id, r, err := c.model.Split(q.Pane, q.Direction, cwd)
	if err != nil {
		return nil, err
	}
	c.apply(r, true)
	return proto.PaneSplitResult{Pane: id}, nil
}

// paneFocus focuses the pane, or with a direction moves the focus of its
// tab from it; at an edge moved is false and the pane is the one named
// (4.2.7).
func (c *core) paneFocus(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.PaneFocus
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	if q.Direction == "" {
		r, err := c.model.Focus(q.Pane)
		if err != nil {
			return nil, err
		}
		c.apply(r, true)
		return proto.PaneFocusResult{Moved: true, Pane: q.Pane}, nil
	}
	target, moved, r, err := c.model.FocusDir(q.Pane, q.Direction)
	if err != nil {
		return nil, err
	}
	c.apply(r, true)
	return proto.PaneFocusResult{Moved: moved, Pane: target}, nil
}

func (c *core) paneClose(cn *conn, m proto.Msg) (any, *model.Error) {
	var q proto.PaneClose
	if err := decode(m, &q); err != nil {
		return nil, err
	}
	r, err := c.model.ClosePane(q.Pane)
	if err != nil {
		return nil, err
	}
	c.apply(r, true)
	return nil, nil
}
