package fleet_test

import (
	"testing"

	"devtools/internal/fleet"
	"devtools/internal/fleet/fleettest"
	"devtools/internal/hub"
	"devtools/internal/proto"
)

// The version of every machine's server comes from its welcome, and what
// it says of an installed update from the welcome and then event.update:
// both reach the summary and the hub.
func TestTheFleetCarriesTheBuildOfEveryServer(t *testing.T) {
	r := fleettest.New(t, machines("macmini connect")...)
	w := r.M("").Welcome()
	w.Version, w.Installed = "0.1.0+04c5fef", "0.1.0+abcdef0"
	r.M("").SetWelcome(w)
	w = r.M("macmini").Welcome()
	w.Version = "0.1.0+6ee3170\x1b[31m" // what another machine sends is cleaned
	r.M("macmini").SetWelcome(w)
	r.M("").Online(fleettest.Snapshot(here))
	r.M("macmini").Online(fleettest.Snapshot())
	s := r.Settle(func(s fleet.SummaryData) bool {
		return fleettest.StateOf(s, "") == "online" && fleettest.StateOf(s, "macmini") == "online"
	})
	if l := fleettest.MachineOf(t, s, ""); l.Version != "0.1.0+04c5fef" || l.Installed != "0.1.0+abcdef0" {
		t.Fatalf("this machine: %+v", l)
	}
	if m := fleettest.MachineOf(t, s, "macmini"); m.Version != "0.1.0+6ee3170[31m" || m.Installed != "" {
		t.Fatalf("macmini: %q %q", m.Version, m.Installed)
	}

	feed := r.OpenHub()
	if ev := feed.Next("macmini", hub.Online); ev.Version != "0.1.0+6ee3170[31m" || ev.Installed != "" {
		t.Fatalf("the hub's online event of macmini: %+v", ev)
	}

	r.M("macmini").Push(proto.Encode(proto.UpdateEvent{Installed: "0.1.0+04c5fef"}))
	r.Settle(func(s fleet.SummaryData) bool {
		return fleettest.MachineOf(t, s, "macmini").Installed == "0.1.0+04c5fef"
	})
	if ev := feed.Next("macmini", hub.Build); ev.Installed != "0.1.0+04c5fef" || ev.Version != "0.1.0+6ee3170[31m" {
		t.Fatalf("the hub's build event of macmini: %+v", ev)
	}

	r.M("").Push(proto.Encode(proto.UpdateEvent{}))
	r.Settle(func(s fleet.SummaryData) bool { return fleettest.MachineOf(t, s, "").Installed == "" })
}
