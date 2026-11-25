package handover

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// JournalName is the journal of the live upgrades in the state directory.
const JournalName = "upgrade.json"

// journalKeep is the number of attempts the journal keeps.
const journalKeep = 10

// Results of an attempt in the journal.
const (
	Pending   = "pending"   // the old image is about to exec
	Committed = "committed" // the new image adopted everything
	OK        = "ok"        // the new image served for the health interval
	Stopped   = "stopped"   // the new image was stopped before the health interval
	Crashed   = "crashed"   // a cold start found the attempt pending or committed
	Failed    = "failed"    // the exec failed; the old image rolled back
	Fallback  = "fallback"  // the new image could not adopt; the old one took over again
	Cold      = "cold"      // neither could adopt; the server started cold
	// Refused is a build the preflight of the automatic upgrade refused:
	// nothing was frozen or exec'd. It is no handover, so ColdStart looks
	// past it.
	Refused = "refused"
)

// Attempt is one live upgrade.
type Attempt struct {
	ID     string `json:"id"`
	Target string `json:"target"` // the store path of the new build
	From   string `json:"from"`   // the version of the old build
	To     string `json:"to"`     // the version of the new build
	Pid    int    `json:"pid"`
	At     int64  `json:"at"` // unix ms of the last change
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
	// Auto is set for an attempt of the automatic upgrade, Started is when
	// its exec was about to happen (unix ms): the automatic upgrade of the
	// next image waits its interval from there.
	Auto    bool  `json:"auto,omitempty"`
	Started int64 `json:"started,omitempty"`
}

// Journal is the content of upgrade.json: the last attempts, oldest first.
type Journal struct {
	Attempts []Attempt `json:"attempts"`
}

// LoadJournal reads the journal; a missing or unreadable one is empty.
func LoadJournal(dir string) Journal {
	b, err := os.ReadFile(filepath.Join(dir, JournalName))
	if err != nil {
		return Journal{}
	}
	var j Journal
	if json.Unmarshal(b, &j) != nil {
		return Journal{}
	}
	return j
}

// Save writes the journal atomically, mode 0600.
func (j Journal) Save(dir string) error {
	if len(j.Attempts) > journalKeep {
		j.Attempts = j.Attempts[len(j.Attempts)-journalKeep:]
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, JournalName))
}

// Put records a or replaces the attempt of the same id.
func (j *Journal) Put(a Attempt) {
	for i := range j.Attempts {
		if j.Attempts[i].ID == a.ID {
			j.Attempts[i] = a
			return
		}
	}
	j.Attempts = append(j.Attempts, a)
}

// Get returns the attempt of an id.
func (j Journal) Get(id string) (Attempt, bool) {
	for _, a := range j.Attempts {
		if a.ID == id {
			return a, true
		}
	}
	return Attempt{}, false
}

// Last returns the newest attempt.
func (j Journal) Last() (Attempt, bool) {
	if len(j.Attempts) == 0 {
		return Attempt{}, false
	}
	return j.Attempts[len(j.Attempts)-1], true
}

// Blocked reports whether the automatic upgrade must not try target again:
// the newest attempt to it crashed, failed, fell back, ended cold or was
// refused by the preflight.
func (j Journal) Blocked(target string) bool {
	for i := len(j.Attempts) - 1; i >= 0; i-- {
		a := j.Attempts[i]
		if a.Target != target {
			continue
		}
		switch a.Result {
		case Crashed, Failed, Fallback, Cold, Refused:
			return true
		}
		return false
	}
	return false
}

// LastAuto is when the newest automatic attempt was about to exec (unix
// ms), 0 when the journal has none.
func (j Journal) LastAuto() int64 {
	var last int64
	for _, a := range j.Attempts {
		if a.Auto && a.Started > last {
			last = a.Started
		}
	}
	return last
}

// ColdStart is what a server that starts without a handover does with the
// journal: an attempt left pending or committed did not reach its health
// interval, so the process ended: it crashed.
func (j *Journal) ColdStart(now int64) (Attempt, bool) {
	// the newest handover: a refusal of the preflight recorded after it (a
	// later candidate, while the adopted image ran) is none
	var a Attempt
	ok := false
	for i := len(j.Attempts) - 1; i >= 0; i-- {
		if j.Attempts[i].Result != Refused {
			a, ok = j.Attempts[i], true
			break
		}
	}
	if !ok || (a.Result != Pending && a.Result != Committed) {
		return Attempt{}, false
	}
	a.Reason = "the server ended " + map[string]string{Pending: "during the exec", Committed: "in its first minute"}[a.Result]
	a.Result = Crashed
	a.At = now
	j.Put(a)
	return a, true
}

// Update loads the journal of dir, applies f and saves it.
func Update(dir string, f func(*Journal)) error {
	j := LoadJournal(dir)
	f(&j)
	err := j.Save(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Failure reports whether a result says the attempt did not keep the
// programs in the new build.
func Failure(result string) bool {
	return strings.Contains("|"+Crashed+"|"+Failed+"|"+Fallback+"|"+Cold+"|"+Refused+"|", "|"+result+"|")
}
