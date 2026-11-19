package fleet

import (
	"fmt"
	"testing"

	"devtools/internal/config"
	"devtools/internal/proto"
	"devtools/internal/session"
)

func entry(m string, n int, boot string) histEntry {
	return histEntry{machine: m, session: fmt.Sprintf("s%d", n), dir: fmt.Sprintf("/d/%d", n), boot: boot}
}

func TestTheHistoryKeepsEightEntriesWithoutDuplicates(t *testing.T) {
	var h history
	for n := 1; n <= 10; n++ {
		h.push(entry("", n, "b"))
	}
	h.push(entry("", 5, "b"))
	if len(h.entries) != historyLen {
		t.Fatalf("%d entries", len(h.entries))
	}
	want := []int{5, 10, 9, 8, 7, 6, 4, 3}
	for i, n := range want {
		if h.entries[i].session != fmt.Sprintf("s%d", n) {
			t.Fatalf("entry %d is %+v, want s%d", i, h.entries[i], n)
		}
	}
}

func TestAHistoryEntryIsItsMachineAndDirectory(t *testing.T) {
	var h history
	h.push(entry("M", 1, "b1"))
	h.push(histEntry{machine: "M", session: "s9", dir: "/d/1", boot: "b2"})
	h.push(entry("K", 1, "b1"))
	if len(h.entries) != 2 {
		t.Fatalf("entries %+v: a session of the same directory on the same machine is one entry", h.entries)
	}
	h.remove(entry("M", 1, "b2"))
	if len(h.entries) != 1 || h.entries[0].machine != "K" {
		t.Fatalf("entries %+v", h.entries)
	}
}

func TestTheFallbackIsTheNewestSessionOfAnOnlineMachine(t *testing.T) {
	f := New(Options{Machines: []config.Machine{{Name: "M"}, {Name: "K"}}, Clock: stillClock{}})
	t.Cleanup(func() { f.updates.close() })
	online := func(name, boot string, ss ...proto.Session) {
		mc := f.byName[name]
		mc.status = "online"
		mc.welcome.Boot = boot
		mc.st = proto.NewState(proto.Snapshot{Sessions: ss})
		mc.order = nil
		for _, s := range ss {
			mc.order = append(mc.order, s.ID)
		}
	}
	var h history
	h.push(entry("", 1, "bl"))
	h.push(entry("M", 2, "bm"))
	h.push(entry("K", 3, "bk"))
	if _, ok := h.fallback(f); ok {
		t.Fatal("a fallback while no machine is online")
	}
	online("", "bl", proto.Session{ID: "s1", Dir: "/d/1"})
	// K is off; M restarted and its session of /d/2 has a new id
	online("M", "bm2", proto.Session{ID: "s7", Dir: "/d/2"})
	got, ok := h.fallback(f)
	if !ok || got != (session.Target{Machine: "M", Session: "s7"}) {
		t.Fatalf("fallback %+v %v, want M's session of the directory", got, ok)
	}
	// the same id on a restarted server is another session: not it
	online("M", "bm2", proto.Session{ID: "s2", Dir: "/d/other"})
	got, ok = h.fallback(f)
	if !ok || got.Machine != "" || got.Session != "s1" {
		t.Fatalf("fallback %+v %v, want the local session", got, ok)
	}
}
