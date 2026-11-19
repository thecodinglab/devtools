package server

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devtools/internal/proto"
	"devtools/internal/server/persist"
	"devtools/internal/testkit"
)

// saved reads state.json of ts; an absent file is the empty state.
func saved(t *testing.T, ts *testServer) persist.State {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ts.state, persist.FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return persist.Empty()
	}
	if err != nil {
		t.Fatal(err)
	}
	st, err := persist.Load(filepath.Join(ts.state, persist.FileName))
	if err != nil {
		t.Fatalf("state.json %s: %v", b, err)
	}
	return st
}

func TestStateIsSavedWithinSaveDelayOfAChange(t *testing.T) {
	const delay = 200 * time.Millisecond
	ts := newTestServer(t, func(o *Options) { o.SaveDelay = delay })
	c := ts.oneshot()
	start := time.Now()
	s, _, _ := ts.open(c)
	testkit.Eventually(t, 5*time.Second, "state.json holds the session", func() bool {
		st := saved(t, ts)
		return len(st.Sessions) == 1 && st.Sessions[0].ID == s && st.Sessions[0].Dir == ts.dir
	})
	if d := time.Since(start); d > delay+2*time.Second {
		t.Fatalf("saved %v after the change, the delay is %v", d, delay)
	}
	fi, err := os.Stat(filepath.Join(ts.state, persist.FileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.json: %v, mode %v", err, fi.Mode())
	}

	// A later change is saved again; an agent record is not saved.
	var tn proto.TabNewResult
	ok(t, c, proto.TabNew{Session: s}).Msg.Decode(&tn)
	testkit.Eventually(t, 5*time.Second, "state.json holds the new tab", func() bool {
		st := saved(t, ts)
		return len(st.Sessions) == 1 && len(st.Sessions[0].Tabs) == 2 && st.Sessions[0].Current == tn.Tab
	})

	// A new server loads it: same structure, the counters go on.
	ts.do(func(c *core) {})
	b, _ := os.ReadFile(filepath.Join(ts.state, persist.FileName))
	if strings.Contains(string(b), `"agents"`) {
		t.Fatalf("state.json holds agents: %s", b)
	}
	next := newTestServer(t, func(o *Options) { o.StateDir = ts.state; o.SaveDelay = delay })
	c2 := next.oneshot()
	snap := next.stateGet(c2)
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != s || len(snap.Tabs) != 2 {
		t.Fatalf("the restored state is %+v", snap)
	}
	for _, p := range snap.Panes {
		if p.State != proto.PaneDormant {
			t.Fatalf("a restored pane is %s, want dormant", p.State)
		}
	}
	var again proto.TabNewResult
	ok(t, c2, proto.TabNew{Session: s}).Msg.Decode(&again)
	if again.Tab == tn.Tab || again.Pane == tn.Pane || again.Tab == "t1" {
		t.Fatalf("ids were issued again after a restart: %+v", again)
	}
}
