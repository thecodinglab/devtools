package integration

// Level 3: the versioning and tolerance rules of 4.1 against the real
// server.

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"devtools/internal/proto"
	"devtools/internal/proto/prototest"
)

// A hello of a generation the server speaks is welcomed in that
// generation, whatever its caps; only one it cannot read is refused.
func TestAnOlderGenerationIsWelcomedAndAnUnknownOneRefused(t *testing.T) {
	_, socket := startServer(t)
	for _, gen := range []int{1, 2} {
		r := dialRaw(t, socket)
		r.send(fmt.Sprintf(`{"t":"hello","gen":%d,"version":"0.1.0","kind":"interactive"}`, gen))
		m := r.next()
		var w proto.Welcome
		if m.T != proto.TypeWelcome || m.Decode(&w) != nil || w.Gen != gen {
			t.Fatalf("a hello of generation %d was answered %s", gen, m.Body)
		}
	}
	for _, gen := range []int{3, 0} {
		r := dialRaw(t, socket)
		r.send(fmt.Sprintf(`{"t":"hello","gen":%d,"version":"9.0.0","kind":"interactive"}`, gen))
		m := r.next()
		if m.T != proto.TypeRefuse {
			t.Fatalf("a hello of generation %d was answered %s", gen, m.Body)
		}
		var ref proto.Refuse
		m.Decode(&ref)
		if ref.Code != proto.CodeGeneration {
			t.Fatalf("the refuse of generation %d has the code %q", gen, ref.Code)
		}
		// The text names both generations, for a human.
		if !strings.Contains(ref.Text, fmt.Sprint(proto.Gen)) || !strings.Contains(ref.Text, fmt.Sprint(gen)) {
			t.Fatalf("the refuse text %q does not name generations %d and %d", ref.Text, proto.Gen, gen)
		}
		// After refuse the server closes the connection.
		if _, err := proto.ReadMsg(r.nc); !errors.Is(err, io.EOF) {
			t.Fatalf("after refuse the connection gave %v, not its end", err)
		}
	}
	// The server goes on serving.
	prototest.Dial(t, socket, prototest.Interactive())
}

func TestUnknownFieldsAndTypesAreIgnored(t *testing.T) {
	home, socket := startServer(t)
	dir := work(t, home)
	r := dialRaw(t, socket)
	// A hello with fields a later version may add is accepted.
	r.send(helloJSON(proto.KindOneshot, `"future":{"x":1}`, `"caps":["catalog","teleport"]`))
	if m := r.next(); m.T != proto.TypeWelcome {
		t.Fatalf("the hello was answered %s", m.Body)
	}
	// A notification of an unknown type is ignored.
	r.send(`{"t":"future.notice","x":[1,2,3]}`)
	// A request with an unknown field is answered as if it were not there.
	r.send(fmt.Sprintf(`{"t":"session.open","id":1,"dir":%q,"name":"work","future":true,"also":{"deep":[null]}}`, dir))
	m := r.reply(1)
	var res proto.SessionOpenResult
	if m.T != proto.TypeOK || m.Decode(&res) != nil || !res.Created {
		t.Fatalf("session.open with unknown fields was answered %s", m.Body)
	}
	// An unknown value of an enumerated field without "unknown" is invalid,
	// and the connection goes on.
	r.send(`{"t":"pane.split","id":2,"pane":"p1","direction":"diagonal","zzz":1}`)
	if rep, _ := proto.AsReply(r.reply(2)); rep.OK || rep.Code != proto.CodeInvalid {
		t.Fatalf("a split in an unknown direction was answered %s", rep.Msg.Body)
	}
	r.send(`{"t":"state.get","id":3}`)
	m = r.reply(3)
	var snap proto.Snapshot
	if m.T != proto.TypeStateSnapshot || m.Decode(&snap) != nil || len(snap.Sessions) != 1 {
		t.Fatalf("state.get after the unknown messages was answered %s", m.Body)
	}

	// An interactive connection: an unknown notification changes nothing,
	// frames go on.
	c := prototest.Dial(t, socket, prototest.Interactive())
	v := c.View(proto.ViewSet{Session: res.Session, Cols: 80, Rows: 24})
	c.Send(proto.Msg{T: "future.notice"})
	c.Type(v.Panes[0], "k")
	c.WaitFor(v.Panes[0], "6b")
	if c.Closed() {
		t.Fatalf("the connection ended: %v", c.Err())
	}
}

func TestUnknownRequestIsAnsweredUnsupported(t *testing.T) {
	_, socket := startServer(t)
	for _, hello := range []proto.Hello{prototest.Oneshot(""), prototest.Interactive()} {
		c := prototest.Dial(t, socket, hello)
		r := c.Request(proto.Msg{T: "teleport.now"})
		if r.OK || r.Code != proto.CodeUnsupported {
			t.Fatalf("%s: an unknown request was answered %+v", hello.Kind, r)
		}
		if r.Text == "" {
			t.Fatalf("%s: the err reply has no text", hello.Kind)
		}
		// The connection goes on.
		if r := c.Request(proto.StateGet{}); !r.OK {
			t.Fatalf("%s: state.get after it: %+v", hello.Kind, r)
		}
	}
}
