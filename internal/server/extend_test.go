package server

import (
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"devtools/internal/pane"
	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/vt"
)

// The registries are process-wide, as init would fill them, so every test
// here uses message types and capabilities of its own.

func mustPanic(t *testing.T, what string, f func()) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("%s did not panic", what)
			}
			msg, _ = r.(string)
		}()
		f()
	}()
	return msg
}

// runs numbers the runs of a test: -count runs it again in the same process,
// where the registries keep what an earlier run registered.
var runs atomic.Int32

func TestExtendPanicsOnADuplicateType(t *testing.T) {
	dup := fmt.Sprintf("test.dup.%d", runs.Add(1))
	h := func(Core, Conn, proto.Msg) {}
	Extend(dup, "", h)
	msg := mustPanic(t, "a second Extend of test.dup", func() { Extend(dup, "", h) })
	if !strings.Contains(msg, dup) {
		t.Fatalf("panic %q does not name the type", msg)
	}
	mustPanic(t, "Extend with an empty type", func() { Extend("", "", h) })
	mustPanic(t, "Extend with a nil handler", func() { Extend("test.nil", "", nil) })
	if _, ok := extension("test.nil"); ok {
		t.Fatal("a refused registration was kept")
	}
}

func TestExtendAddsItsCapability(t *testing.T) {
	run := runs.Add(1)
	typ := func(s string) string { return fmt.Sprintf("test.cap.%s.%d", s, run) }
	testcap := fmt.Sprintf("testcap%d", run)
	called := ""
	h := func(_ Core, _ Conn, m proto.Msg) { called = m.T }
	before := len(extensionCaps())
	Extend(typ("a"), testcap, h)
	Extend(typ("b"), testcap, h)
	Extend(typ("c"), "", h)
	caps := extensionCaps()
	n := 0
	for _, c := range caps {
		if c == testcap {
			n++
		}
		if c == "" {
			t.Fatal("an empty capability was added")
		}
	}
	if n != 1 || len(caps) != before+1 {
		t.Fatalf("caps %v: want testcap exactly once, one more than %d", caps, before)
	}
	caps[len(caps)-1] = "changed"
	if extensionCaps()[len(caps)-1] != testcap {
		t.Fatal("extensionCaps returns the registry's own slice")
	}
	got, ok := extension(typ("b"))
	if !ok {
		t.Fatal("no handler for test.cap.b")
	}
	got(nil, nil, proto.Msg{T: typ("b")})
	if called != typ("b") {
		t.Fatalf("the registered handler was not returned (called %q)", called)
	}
	if _, ok := extension("test.cap.missing"); ok {
		t.Fatal("a handler for a type nobody registered")
	}
}

type testObserver struct{}

func (testObserver) Title(Core, string, string)               {}
func (testObserver) Report(Core, string, vt.AgentReport)      {}
func (testObserver) Foreground(Core, string, []platform.Proc) {}
func (testObserver) Seen(Core, string)                        {}
func (testObserver) Gone(Core, string)                        {}

func TestObserveTakesOneObserver(t *testing.T) {
	mustPanic(t, "Observe(nil)", func() { Observe(nil) })
	// cross.go (P2-10) registers the observer of the build from init; this
	// test registers its own only while there is none.
	if extensionObserver() == nil {
		Observe(testObserver{})
		if _, ok := extensionObserver().(testObserver); !ok {
			t.Fatal("the observer was not kept")
		}
	}
	kept := fmt.Sprintf("%T", extensionObserver())
	mustPanic(t, "a second Observe", func() { Observe(testObserver{}) })
	if fmt.Sprintf("%T", extensionObserver()) != kept {
		t.Fatal("a refused observer replaced the first")
	}
}

func TestDefaultsAreTheValuesOfTheContract(t *testing.T) {
	o := Defaults()
	if o.History != 10000 || o.FrameInterval.Milliseconds() != 16 || o.CosmeticDelay.Milliseconds() != 100 ||
		o.SyncHold.Milliseconds() != 150 || o.SaveDelay.Seconds() != 5 || o.StallTimeout.Seconds() != 30 ||
		o.PingInterval.Seconds() != 5 || o.HelloTimeout.Seconds() != 4 || o.KillGrace.Milliseconds() != 250 ||
		o.ProcInterval.Seconds() != 1 || o.FgInterval.Milliseconds() != 100 {
		t.Fatalf("Defaults() = %+v", o)
	}
	if o.NewPane != nil || o.Shell != nil || o.StateDir != "" {
		t.Fatal("Defaults fills what the caller fills")
	}
}

// TestPaneSatisfiesTheServersInterface pins "exactly the exported methods
// of 5.3.3, no more and no fewer": the compile-time assertion in options.go
// catches a missing method, this test also an extra one on *pane.Pane.
func TestPaneSatisfiesTheServersInterface(t *testing.T) {
	iface := reflect.TypeOf((*Pane)(nil)).Elem()
	impl := reflect.TypeOf((*pane.Pane)(nil))
	if !impl.Implements(iface) {
		t.Fatal("*pane.Pane does not implement server.Pane")
	}
	want := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		want[iface.Method(i).Name] = true
	}
	for i := 0; i < impl.NumMethod(); i++ {
		if name := impl.Method(i).Name; !want[name] {
			t.Errorf("*pane.Pane has the method %s, which server.Pane lacks", name)
		}
	}
	if impl.NumMethod() != iface.NumMethod() {
		t.Errorf("*pane.Pane has %d exported methods, server.Pane %d", impl.NumMethod(), iface.NumMethod())
	}
}
