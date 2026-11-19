package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestMain keeps the tests hermetic: nothing of the developer's environment
// that names a socket or a state directory reaches them.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-proto-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{EnvSocket, EnvPane, EnvTTY, "XDG_STATE_HOME", "DEVTOOLS_CONFIG", "DEVTOOLS_MULTIPLEXER"} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func frameBytes(body []byte) []byte {
	b := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	copy(b[4:], body)
	return b
}

// countingReader serves a length prefix and then an endless body, and counts
// how many bytes were read.
type countingReader struct {
	prefix [4]byte
	n      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		if r.n < 4 {
			p[i] = r.prefix[r.n]
		} else {
			p[i] = ' '
		}
		r.n++
	}
	return len(p), nil
}

func TestReadMsgRejectsALengthAboveTheCapBeforeAllocating(t *testing.T) {
	for _, n := range []uint32{MaxMessage + 1, 1 << 31, 0xFFFFFFFF} {
		r := &countingReader{}
		binary.BigEndian.PutUint32(r.prefix[:], n)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := ReadMsg(r)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("length %d: err = %v, want ErrTooLarge", n, err)
		}
		if r.n != 4 {
			t.Fatalf("length %d: read %d bytes, want only the 4 of the prefix", n, r.n)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<10 {
			t.Fatalf("length %d: allocated %d bytes before refusing", n, grew)
		}
	}
}

func TestReadMsgAcceptsAMessageOfExactlyTheCap(t *testing.T) {
	body := []byte(`{"t":"ping"}` + strings.Repeat(" ", MaxMessage-len(`{"t":"ping"}`)))
	m, err := ReadMsg(bytes.NewReader(frameBytes(body)))
	if err != nil {
		t.Fatalf("ReadMsg of %d bytes: %v", len(body), err)
	}
	if m.T != TypePing {
		t.Fatalf("T = %q", m.T)
	}
}

func TestReadMsgRejectsATruncatedMessage(t *testing.T) {
	body := []byte(`{"t":"view.resize","cols":80,"rows":24}`)
	full := frameBytes(body)
	cases := map[string][]byte{
		"prefix cut short": full[:2],
		"body cut short":   full[:len(full)-5],
		"prefix only":      full[:4],
	}
	for name, in := range cases {
		_, err := ReadMsg(bytes.NewReader(in))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := ReadMsg(bytes.NewReader(nil)); err != io.EOF {
		t.Errorf("empty stream: err = %v, want io.EOF", err)
	}
}

// A present id must be a positive integer: null is a malformed id, not the
// absent id of a notification.
func TestReadMsgRejectsLengthZeroAndBodiesThatAreNoEnvelope(t *testing.T) {
	bodies := []string{
		``, `[]`, `"t"`, `{}`, `{"t":""}`, `{"t":1}`, `{"t":"ok","id":0}`,
		`{"t":"ok","id":-1}`, `{"t":"ok","id":1.5}`, `{"t":"ok","id":"7"}`, `{"t":"ok"`, `nonsense`,
		`{"t":"ok","id":null}`, `{"t":"session.open","id":null,"dir":"/tmp"}`,
	}
	for _, b := range bodies {
		var in []byte
		if b == "" {
			in = []byte{0, 0, 0, 0}
		} else {
			in = frameBytes([]byte(b))
		}
		if _, err := ReadMsg(bytes.NewReader(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("body %q: err = %v, want ErrMalformed", b, err)
		}
	}
}

type countingWriter struct {
	writes int
	buf    bytes.Buffer
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}

func TestWriteMsgRefusesAnOversizedMessage(t *testing.T) {
	body := []byte(`{"t":"ping"}` + strings.Repeat(" ", MaxMessage+1-len(`{"t":"ping"}`)))
	w := &countingWriter{}
	err := WriteMsg(w, Msg{T: TypePing, Body: body})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if w.writes != 0 || w.buf.Len() != 0 {
		t.Fatalf("wrote %d times, %d bytes; want nothing", w.writes, w.buf.Len())
	}
}

func TestWriteMsgWritesOneMessageInOneCall(t *testing.T) {
	w := &countingWriter{}
	big := Encode(Paste{Pane: "p1", Text: strings.Repeat("x", MaxPaste)})
	if err := WriteMsg(w, big); err != nil {
		t.Fatal(err)
	}
	if w.writes != 1 {
		t.Fatalf("WriteMsg used %d Write calls", w.writes)
	}
	m, err := ReadMsg(&w.buf)
	if err != nil {
		t.Fatal(err)
	}
	var p Paste
	if err := m.Decode(&p); err != nil || len(p.Text) != MaxPaste {
		t.Fatalf("decoded %d bytes, err %v", len(p.Text), err)
	}
}

func TestWriteMsgOfAnEnvelopeWithoutBody(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMsg(&buf, Msg{T: TypeStateGet, ID: 3}); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.T != TypeStateGet || m.ID != 3 || string(m.Body) != `{"t":"state.get","id":3}` {
		t.Fatalf("got %+v %s", m, m.Body)
	}
}

func TestUnknownTypeDecodesAsAnEnvelope(t *testing.T) {
	for _, tc := range []struct {
		body string
		id   uint64
	}{
		{`{"t":"future.request","id":3,"x":[1,2,{"y":null}]}`, 3},
		{`{"x":1,"t":"future.notice"}`, 0},
	} {
		m, err := ReadMsg(bytes.NewReader(frameBytes([]byte(tc.body))))
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if !strings.HasPrefix(m.T, "future.") || m.ID != tc.id || string(m.Body) != tc.body {
			t.Fatalf("%s: got T=%q ID=%d body=%s", tc.body, m.T, m.ID, m.Body)
		}
		if _, known := NewMessage(m.T); known {
			t.Fatalf("%s is known", m.T)
		}
	}
}

func TestUnknownFieldsAreIgnoredInCoreMessages(t *testing.T) {
	body := `{"t":"hello","gen":1,"version":"9.9.9","kind":"oneshot","remote":false,"pane":"p1",` +
		`"future":{"deep":[1,2,3]},"caps":["route","teleport"]}`
	m, err := ReadMsg(bytes.NewReader(frameBytes([]byte(body))))
	if err != nil {
		t.Fatal(err)
	}
	var h Hello
	if err := m.Decode(&h); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if h.Gen != 1 || h.Kind != KindOneshot || h.Pane != "p1" || len(h.Caps) != 2 {
		t.Fatalf("got %+v", h)
	}

	// An unknown value of an enumerated field with an "unknown" value.
	snap := `{"t":"state.snapshot","rev":1,"sessions":[],"tabs":[],` +
		`"panes":[{"id":"p1","tab":"t1","session":"s1","state":"zombie","colour":"red"}],` +
		`"agents":[{"pane":"p1","tab":"t1","session":"s1","kind":"gemini","status":"thinking"}]}`
	m, err = ReadMsg(bytes.NewReader(frameBytes([]byte(snap))))
	if err != nil {
		t.Fatal(err)
	}
	var s Snapshot
	if err := m.Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.Panes[0].State != Unknown || s.Agents[0].Kind != Unknown || s.Agents[0].Status != Unknown {
		t.Fatalf("enumerations not read as unknown: %+v %+v", s.Panes[0], s.Agents[0])
	}
}

func TestReplyKeepsTheRequestID(t *testing.T) {
	req := Request(7, SessionOpen{Dir: "/tmp/x", Name: "x"})
	if req.ID != 7 || req.T != TypeSessionOpen {
		t.Fatalf("request: %+v", req)
	}
	var buf bytes.Buffer
	for _, m := range []Msg{
		OKReply(req.ID, SessionOpenResult{Session: "s4", Created: true}),
		OKReply(req.ID, nil),
		ErrReply(req.ID, CodeNoDir, "/tmp/x does not exist"),
		Request(req.ID, ViewOK{Session: "s4", Dir: "/tmp/x", Tab: "t1", Panes: []string{"p1"}}),
		Request(req.ID, Snapshot{Rev: 3}),
	} {
		if err := WriteMsg(&buf, m); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		m, err := ReadMsg(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if m.ID != 7 {
			t.Fatalf("reply %d (%s) has id %d", i, m.T, m.ID)
		}
		r, ok := AsReply(m)
		if !ok {
			t.Fatalf("reply %d (%s) is no reply", i, m.T)
		}
		switch i {
		case 0:
			var res SessionOpenResult
			if !r.OK || r.Msg.Decode(&res) != nil || res.Session != "s4" || !res.Created {
				t.Fatalf("ok reply: %+v %+v", r, res)
			}
		case 1:
			if !r.OK || string(m.Body) != `{"t":"ok","id":7}` {
				t.Fatalf("empty ok: %s", m.Body)
			}
		case 2:
			if r.OK || r.Code != CodeNoDir || r.Text != "/tmp/x does not exist" {
				t.Fatalf("err reply: %+v", r)
			}
		default:
			if !r.OK {
				t.Fatalf("named reply %s is not ok", m.T)
			}
		}
	}
	if _, ok := AsReply(Encode(Snapshot{Rev: 1})); ok {
		t.Fatal("a state.snapshot without an id is no reply")
	}
	if _, ok := AsReply(Encode(Ping{})); ok {
		t.Fatal("a ping is no reply")
	}
}

func TestEncodePutsTypeAndIDFirst(t *testing.T) {
	m := Request(12, TabSelect{Session: "s1", Step: -1})
	if string(m.Body) != `{"t":"tab.select","id":12,"session":"s1","step":-1}` {
		t.Fatalf("body %s", m.Body)
	}
	m = Encode(ViewClear{})
	if string(m.Body) != `{"t":"view.clear"}` {
		t.Fatalf("body %s", m.Body)
	}
	var v map[string]any
	if err := json.Unmarshal(OKReply(1, map[string]int{"a": 1}).Body, &v); err != nil || v["a"] != 1.0 {
		t.Fatalf("map result: %v %v", v, err)
	}
}

func FuzzReadMsg(f *testing.F) {
	for _, s := range []string{
		`{"t":"ping"}`, `{"t":"ok","id":1}`, `{"t":"frame","pane":"p1","lines":[{"y":0,"s":"x"}]}`,
		`{"t":"x","id":18446744073709551615}`, `{}`, `[]`, `{"t":"ok","id":-0}`,
	} {
		f.Add(frameBytes([]byte(s)))
	}
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, '{'})
	f.Add([]byte{0, 0, 0, 9, '{', '"', 't'})
	f.Fuzz(func(t *testing.T, in []byte) {
		m, err := ReadMsg(bytes.NewReader(in))
		if err != nil {
			if err != io.EOF && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			return
		}
		if m.T == "" || len(m.Body) == 0 || len(m.Body) > MaxMessage {
			t.Fatalf("accepted %+v", m)
		}
		var buf bytes.Buffer
		if err := WriteMsg(&buf, m); err != nil {
			t.Fatalf("WriteMsg of an accepted message: %v", err)
		}
		again, err := ReadMsg(&buf)
		if err != nil {
			t.Fatalf("ReadMsg of a written message: %v", err)
		}
		if again.T != m.T || again.ID != m.ID || !bytes.Equal(again.Body, m.Body) {
			t.Fatalf("round trip changed %+v into %+v", m, again)
		}
		if typed, ok := NewMessage(m.T); ok {
			_ = m.Decode(typed) // must not panic
		}
		var d Delta
		if m.Decode(&d) == nil {
			s := NewState(Snapshot{Rev: d.Rev - 1})
			_ = s.Apply(d) // must not panic
		}
	})
}
