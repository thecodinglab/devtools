// Package persist is state.json, the structure of a server that survives a
// restart (3.9 of the plan): the id counters, and per session its id,
// directory, label, current tab, last-shown time and size, and per tab its
// layout, its focused pane and each pane's id and directory. Screen contents,
// history, titles, agent state and anything per client are not saved.
//
// Save writes atomically: a temp file in the same directory, fsynced,
// renamed over the old file, the directory fsynced; the file has mode 0600.
// Load reads the file back and restores it (Restore): a file that does not
// parse or has an unknown version is set aside as state.json.bad-<unix
// seconds> and the server starts empty.
//
// The package imports the standard library and internal/session only. The
// model (internal/server/model) converts between this form and its own.
package persist

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"devtools/internal/proto"
)

// FileName is the name of the state file inside the state directory.
const FileName = "state.json"

// Version is the version of the file format this package writes. It reads
// it and version 1, the format before tabs had a layout tree; a file with
// any other version is set aside. A server of version 1 sets a file of
// version 2 aside rather than cut its tabs to two panes.
const Version = 2

// State is the content of state.json.
type State struct {
	Version  int       `json:"version"`
	Counters Counters  `json:"counters"`
	Sessions []Session `json:"sessions"`
}

// Counters are the numbers of the last ids issued, per kind: the next
// session is "s" followed by Session+1. They never go back, also across
// restarts (3.3).
type Counters struct {
	Session uint64 `json:"session"`
	Tab     uint64 `json:"tab"`
	Pane    uint64 `json:"pane"`
}

// Session is one session: its tabs in index order.
type Session struct {
	ID      string `json:"id"`
	Dir     string `json:"dir"` // canonical directory, the key
	Label   string `json:"label"`
	Current string `json:"current"` // tab id
	Shown   int64  `json:"shown"`   // unix ms a view was last set on it, 0 if never
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	Tabs    []Tab  `json:"tabs"`
}

// Tab is one tab: its panes in the order of the leaves of its layout, the
// first top left. A file without a layout (written before tabs had more
// than two panes) lays its panes out in one split of direction Split.
type Tab struct {
	ID      string      `json:"id"`
	Split   string      `json:"split"` // "", "right", "down": the direction of the layout's root
	Focused string      `json:"focused"`
	Panes   []Pane      `json:"panes"`
	Layout  *proto.Node `json:"layout,omitempty"`
}

// Pane is one pane: its id and its last known directory.
type Pane struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

// SetAside is the error of Load when the file did not parse or had an
// unknown version. The file was renamed to Bad and the State Load returned
// with it is empty: the server logs the error in one line and starts.
type SetAside struct {
	Bad    string // the new name of the file
	Reason error
}

func (e *SetAside) Error() string {
	return fmt.Sprintf("state file set aside as %s: %v", e.Bad, e.Reason)
}

func (e *SetAside) Unwrap() error { return e.Reason }

// Empty returns the state of a server that has nothing yet.
func Empty() State { return State{Version: Version} }

// now is the clock of the name of a set-aside file.
var now = time.Now

// Load reads the state file at path and restores it (Restore).
//
// A missing file is the empty state and no error. A file that does not
// parse or has an unknown version is renamed path.bad-<unix seconds>; Load
// then returns the empty state and a *SetAside, which the caller logs and
// otherwise ignores. Any other error (the file cannot be read, the bad file
// cannot be renamed) is returned with the empty state.
func Load(path string) (State, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return Empty(), err
	}
	s, reason := decode(b)
	if reason != nil {
		bad, err := setAside(path)
		if err != nil {
			return Empty(), fmt.Errorf("state file %s: %v; setting it aside: %w", path, reason, err)
		}
		return Empty(), &SetAside{Bad: bad, Reason: reason}
	}
	return Restore(s), nil
}

func decode(b []byte) (State, error) {
	var s State
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&s); err != nil {
		return State{}, err
	}
	if dec.More() {
		return State{}, errors.New("data after the state object")
	}
	if s.Version != Version && s.Version != 1 {
		return State{}, fmt.Errorf("unknown version %d", s.Version)
	}
	return s, nil
}

// setAside renames path to path.bad-<unix seconds>, with a counter appended
// when such a file exists already, so an earlier bad file is never replaced.
func setAside(path string) (string, error) {
	base := path + ".bad-" + strconv.FormatInt(now().Unix(), 10)
	bad := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(bad); errors.Is(err, fs.ErrNotExist) {
			break
		}
		bad = base + "-" + strconv.Itoa(i)
	}
	if err := os.Rename(path, bad); err != nil {
		return "", err
	}
	return bad, nil
}

// Save writes s to path atomically with mode 0600: a temp file beside path
// is written and fsynced, renamed to path, and the directory is fsynced. On
// an error the old file is unchanged and no temp file is left behind. The
// version written is always Version.
func Save(path string, s State) (err error) {
	s.Version = Version
	if s.Sessions == nil {
		s.Sessions = []Session{}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
