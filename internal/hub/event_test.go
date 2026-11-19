package hub

import (
	"testing"

	"devtools/internal/session"
	"devtools/internal/ui"
)

// TestModelOptionsHasEveryFieldOfTheContract compiles a ModelOptions with
// every field 5.3.8 names, with the types it names. A missing field or a
// field of another type is a compile error of this test file, so the
// contract of event.go cannot fall behind the plan unnoticed.
func TestModelOptionsHasEveryFieldOfTheContract(t *testing.T) {
	events := make(chan Event)
	var actions Actions
	o := ModelOptions{
		Mode:     ModeAgents,
		Query:    "devtools",
		Here:     session.Target{Machine: "", Dir: "/tmp/x"},
		Events:   events,
		Actions:  actions,
		Theme:    ui.Theme{},
		Features: Features{Agents: true, Machines: true},
		Name:     "desktop",
		Tokens:   []ui.Token{{Name: "accent", Light: 0x0000ff, Dark: 0x8080ff}},
	}
	var _ <-chan Event = o.Events
	var _ ui.Theme = o.Theme
	var _ string = o.Name
	var _ []ui.Token = o.Tokens
	if len(o.Tokens) != 1 || o.Tokens[0].Name != "accent" {
		t.Fatalf("ModelOptions did not keep its tokens: %+v", o.Tokens)
	}
	if o.Mode != ModeAgents || o.Query != "devtools" || o.Here.Dir != "/tmp/x" || o.Name != "desktop" {
		t.Fatalf("ModelOptions did not keep its fields: %+v", o)
	}
	if !o.Features.Agents || !o.Features.Machines {
		t.Fatalf("Features did not keep its fields: %+v", o.Features)
	}
}
