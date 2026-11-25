package handover

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func payload(t *testing.T) (*os.File, Header) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "payload")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	var w Writer
	screen, err := w.Add("ghostty-snapshot/1", []byte("SCREEN-BYTES"))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := w.Add(Raw, []byte("typed"))
	if err != nil {
		t.Fatal(err)
	}
	h := Header{
		Boot: "b00t", Socket: "/tmp/s", Attempt: "0123456789abcdef",
		State: State{Counters: Counters{Session: 1, Tab: 1, Pane: 2}, Sessions: []Session{{ID: "s1", Dir: "/d", Tabs: []string{"t1"}, Current: "t1", Cols: 80, Rows: 24, Displayed: true}}},
		Panes: []Pane{
			{ID: "p1", Kind: PaneRunning, Pid: 42, Master: 4096, TTY: "/dev/pts/3", Cols: 80, Rows: 24, Base: 7, Screen: screen, Pending: pending},
			{ID: "p2", Kind: PaneExited, Pid: 0, Master: -1, Cols: 80, Rows: 24, Exit: &Exit{Code: -1, Signal: "SIGKILL"}},
		},
		Ext: map[string]json.RawMessage{"agents": json.RawMessage(`{"x":1}`)},
	}
	if err := w.Write(f, h); err != nil {
		t.Fatal(err)
	}
	return f, h
}

func TestAPayloadRoundTrips(t *testing.T) {
	f, h := payload(t)
	r, err := Open(f)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Header()
	if got.Boot != h.Boot || len(got.Panes) != 2 || got.Panes[0].TTY != "/dev/pts/3" || got.State.Sessions[0].Cols != 80 || string(got.Ext["agents"]) != `{"x":1}` {
		t.Fatalf("header %+v", got)
	}
	b, err := r.Section(got.Panes[0].Screen)
	if err != nil || string(b) != "SCREEN-BYTES" {
		t.Fatalf("screen %q, %v", b, err)
	}
	b, err = r.Section(got.Panes[0].Pending)
	if err != nil || string(b) != "typed" {
		t.Fatalf("pending %q, %v", b, err)
	}
	if b, err := r.Section(got.Panes[1].Screen); err != nil || b != nil {
		t.Fatalf("empty section %q, %v", b, err)
	}
}

// corrupt rewrites the payload's bytes with f and opens it again.
func corrupt(t *testing.T, change func(b []byte) []byte) error {
	t.Helper()
	f, _ := payload(t)
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	b = change(b)
	g, err := os.CreateTemp(t.TempDir(), "bad")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.Write(b)
	r, err := Open(g)
	if err != nil {
		return err
	}
	for _, p := range r.Header().Panes {
		for _, s := range []Section{p.Screen, p.Pending} {
			if _, err := r.Section(s); err != nil {
				return err
			}
		}
	}
	return nil
}

func rewriteHeader(t *testing.T, b []byte, edit func(h map[string]any)) []byte {
	hl := binary.LittleEndian.Uint32(b[12:16])
	var h map[string]any
	if err := json.Unmarshal(b[20:20+hl], &h); err != nil {
		t.Fatal(err)
	}
	edit(h)
	nh, _ := json.Marshal(h)
	base := (20 + int(hl) + 7) &^ 7
	data := b[base:]
	out := append([]byte{}, b[:12]...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(nh)))
	out = binary.LittleEndian.AppendUint32(out, crcOf(nh))
	out = append(out, nh...)
	for len(out)%8 != 0 {
		out = append(out, 0)
	}
	return append(out, data...)
}

func TestEveryCorruptionIsRefused(t *testing.T) {
	cases := map[string]func(b []byte) []byte{
		"magic":           func(b []byte) []byte { b[0] = 'X'; return b },
		"version":         func(b []byte) []byte { b[8] = 9; return b },
		"header checksum": func(b []byte) []byte { b[25] ^= 0x20; return b },
		"truncated":       func(b []byte) []byte { return b[:len(b)-3] },
		"short":           func(b []byte) []byte { return b[:10] },
		"huge header":     func(b []byte) []byte { binary.LittleEndian.PutUint32(b[12:16], MaxHeader+1); return b },
		"section checksum": func(b []byte) []byte {
			b[len(b)-1] ^= 1
			return b
		},
		"unknown field": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) { h["surprise"] = 1 })
		},
		"section beyond the end": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) {
				h["panes"].([]any)[0].(map[string]any)["screen"].(map[string]any)["len"] = 1 << 20
			})
		},
		"overlapping sections": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) {
				p := h["panes"].([]any)[0].(map[string]any)
				p["pending"].(map[string]any)["off"] = 0
			})
		},
		"oversize screen": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) {
				h["panes"].([]any)[0].(map[string]any)["screen"].(map[string]any)["len"] = MaxScreen + 1
			})
		},
		"exited pane with a terminal": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) { h["panes"].([]any)[1].(map[string]any)["master"] = 7 })
		},
		"running pane without a program": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) { h["panes"].([]any)[0].(map[string]any)["pid"] = 0 })
		},
		"pane twice": func(b []byte) []byte {
			return rewriteHeader(t, b, func(h map[string]any) { h["panes"].([]any)[1].(map[string]any)["id"] = "p1" })
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			if err := corrupt(t, change); err == nil {
				t.Fatalf("a payload with a bad %s was accepted", name)
			}
		})
	}
	if err := corrupt(t, func(b []byte) []byte { return b }); err != nil {
		t.Fatalf("the unchanged payload: %v", err)
	}
}

func TestEnvRoundTripsAndRefusesJunk(t *testing.T) {
	e := Env{V: 1, ID: "0123456789abcdef", Attempt: 1, Payload: 4100, Lock: 4097, Listener: 4098, Lo: 4101, Hi: 4104, Exe: 4096, From: "/nix/store/abc-devtools/bin/devtools"}
	got, err := ParseEnv(e.String())
	if err != nil || got != e {
		t.Fatalf("ParseEnv(%q) = %+v, %v", e.String(), got, err)
	}
	none := e
	none.Lo, none.Hi = 4101, 4100
	if got, err := ParseEnv(none.String()); err != nil || got.InRange(4101) {
		t.Fatalf("no panes: %+v, %v", got, err)
	}
	good := e.String()
	for _, junk := range []string{
		"",
		good + ";extra=1",
		strings.Replace(good, "v=1", "v=x", 1),
		strings.Replace(good, "id=0123456789abcdef", "id=XYZ", 1),
		strings.Replace(good, "attempt=1", "attempt=3", 1),
		strings.Replace(good, "fd=4100", "fd=1", 1),
		strings.Replace(good, "fd=4100", "fd=4102", 1), // inside the range
		strings.Replace(good, "lock=4097", "lock=4098", 1),
		strings.Replace(good, "from=/nix", "from=nix", 1),
		strings.Replace(good, "from=/nix/store/abc-devtools/bin/devtools", "from=/a/../b", 1),
		strings.Replace(good, ";ln=4098", "", 1),
		strings.Replace(good, ";exe=4096", "", 1),
		strings.Replace(good, "exe=4096", "exe=4097", 1), // the lock's
		strings.Replace(good, "range=4101-4104", "range=4101-9999", 1),
		good + ";v=1",
	} {
		if _, err := ParseEnv(junk); err == nil {
			t.Errorf("ParseEnv(%q) accepted", junk)
		}
	}
}

func TestChooseNegotiates(t *testing.T) {
	local := Check{Handover: []int{1}, Screens: []string{"ghostty-snapshot/1"}, Gens: []int{1, 2}, GOOS: "linux", GOARCH: "amd64"}
	same := local
	c, err := Choose(local, same, []int{1})
	if err != nil || c != (Choice{Schema: 1, Screen: "ghostty-snapshot/1"}) {
		t.Fatalf("Choose = %+v, %v", c, err)
	}
	newer := local
	newer.Handover = []int{1, 2}
	newer.Screens = []string{"ghostty-snapshot/2", "ghostty-snapshot/1"}
	if c, err := Choose(local, newer, nil); err != nil || c.Schema != 1 || c.Screen != "ghostty-snapshot/1" {
		t.Errorf("with a newer target: %+v, %v", c, err)
	}
	var inc *Incompatible
	for name, target := range map[string]Check{
		"schema 99":       {Handover: []int{99}, Screens: local.Screens, Gens: local.Gens, GOOS: "linux", GOARCH: "amd64"},
		"snapshot bump":   {Handover: []int{1}, Screens: []string{"ghostty-snapshot/2"}, Gens: local.Gens, GOOS: "linux", GOARCH: "amd64"},
		"other system":    {Handover: []int{1}, Screens: local.Screens, Gens: local.Gens, GOOS: "darwin", GOARCH: "arm64"},
		"no generation 1": {Handover: []int{1}, Screens: local.Screens, Gens: []int{2}, GOOS: "linux", GOARCH: "amd64"},
	} {
		if _, err := Choose(local, target, []int{1}); !errors.As(err, &inc) {
			t.Errorf("%s: Choose = %v, want Incompatible", name, err)
		}
	}
}

func TestJournalRules(t *testing.T) {
	dir := t.TempDir()
	if err := Update(dir, func(j *Journal) {
		j.Put(Attempt{ID: "a1", Target: "/nix/store/b", Result: Pending})
	}); err != nil {
		t.Fatal(err)
	}
	j := LoadJournal(dir)
	if a, ok := j.Get("a1"); !ok || a.Result != Pending {
		t.Fatalf("journal %+v", j)
	}
	if j.Blocked("/nix/store/b") {
		t.Errorf("a pending attempt blocks")
	}
	// A cold start finds it pending: crashed, and the automatic trigger
	// does not try that target again.
	a, ok := j.ColdStart(5)
	if !ok || a.Result != Crashed {
		t.Fatalf("ColdStart = %+v, %v", a, ok)
	}
	if !j.Blocked("/nix/store/b") || j.Blocked("/nix/store/c") {
		t.Errorf("Blocked after a crash: b %v, c %v", j.Blocked("/nix/store/b"), j.Blocked("/nix/store/c"))
	}
	// committed without ok is a crash too; ok is not.
	j.Put(Attempt{ID: "a2", Target: "/nix/store/c", Result: Committed})
	if a, ok := j.ColdStart(6); !ok || a.ID != "a2" || a.Result != Crashed {
		t.Errorf("committed then cold: %+v %v", a, ok)
	}
	j.Put(Attempt{ID: "a3", Target: "/nix/store/d", Result: OK})
	if _, ok := j.ColdStart(7); ok {
		t.Errorf("ok then cold counted as a crash")
	}
	for _, r := range []string{Failed, Fallback, Cold} {
		j.Put(Attempt{ID: "x" + r, Target: "/t/" + r, Result: r})
		if !j.Blocked("/t/" + r) {
			t.Errorf("%s does not block", r)
		}
	}
	for i := range 20 {
		j.Put(Attempt{ID: "n" + string(rune('a'+i)), Result: OK})
	}
	if err := j.Save(dir); err != nil {
		t.Fatal(err)
	}
	if n := len(LoadJournal(dir).Attempts); n != journalKeep {
		t.Errorf("the journal keeps %d attempts, want %d", n, journalKeep)
	}
	if fi, err := os.Stat(filepath.Join(dir, JournalName)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("journal mode %v, %v", fi.Mode(), err)
	}
}

// The writer refuses what the reader would refuse: a section above its
// limit (pending input beyond MaxPending) never reaches an exec.
func TestTheWriterRefusesWhatTheReaderRefuses(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "payload")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var w Writer
	pending, err := w.Add(Raw, make([]byte, MaxPending+1))
	if err != nil {
		t.Fatal(err)
	}
	h := Header{Attempt: "0123456789abcdef", Panes: []Pane{{ID: "p1", Kind: PaneRunning, Pid: 42, Master: 4096, Cols: 80, Rows: 24, Pending: pending}}}
	if err := w.Write(f, h); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("Write of %d pending bytes = %v, want the reader's refusal", MaxPending+1, err)
	}
}

// Every value of the inventory the writer can produce is one ParseEnv
// takes back; one it cannot (a path with ";") is refused by ParseEnv, and
// boot checks the round trip before the exec.
func TestTheInventoryRoundTripsOrIsRefused(t *testing.T) {
	e := Env{V: 1, ID: "0123456789abcdef", Attempt: 1, Payload: 4100, Lock: 4096, Listener: 4097, Lo: 4098, Hi: 4099, Exe: 4101, From: "/nix/store/abc-devtools/bin/devtools"}
	got, err := ParseEnv(e.String())
	if err != nil || got != e {
		t.Fatalf("ParseEnv(%q) = %+v, %v", e.String(), got, err)
	}
	e.From = "/home/u/a;b/devtools"
	if _, err := ParseEnv(e.String()); err == nil {
		t.Fatalf("a path with ; round-tripped: %q", e.String())
	}
}
