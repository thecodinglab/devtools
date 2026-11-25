package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// crossTypes are the message types whose structs live in cross.go (P2-00).
// Their examples are under testdata/wire with the others. Until cross.go
// registers a type, its examples are checked as envelopes only; from then on
// they get the full round trip below.
var crossTypes = map[string]bool{
	"event.agent": true, "ui.hub": true, "ui.switch": true,
	"catalog.watch": true, "catalog.item": true, "catalog.git": true,
	"catalog.error": true, "catalog.done": true, "catalog.stop": true,
	"event.update": true,
}

type wireExample struct {
	name string // file name
	body []byte // compact JSON
}

func readWireExamples(t *testing.T) []wireExample {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "wire", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no wire examples under testdata/wire")
	}
	var out []wireExample
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			t.Fatalf("%s: not JSON: %v", f, err)
		}
		out = append(out, wireExample{name: filepath.Base(f), body: buf.Bytes()})
	}
	return out
}

// dropZero removes, recursively, every object member whose value is a zero
// value (false, 0, "", null, [] or {}). Two encodings that differ only in an
// omitted zero field are the same message to a tolerant receiver.
func dropZero(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			e = dropZero(e)
			if !isZero(e) {
				out[k] = e
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = dropZero(e)
		}
		return out
	}
	return v
}

func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case float64:
		return x == 0
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("%s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return reflect.DeepEqual(dropZero(va), dropZero(vb))
}

// strictDecode decodes the fields of body other than "t" and "id" into v and
// fails on a field v does not have: an example must not carry a field the
// contract lacks.
func strictDecode(body []byte, v any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	delete(fields, "t")
	delete(fields, "id")
	rest, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func TestWireExamplesDecode(t *testing.T) {
	for _, ex := range readWireExamples(t) {
		t.Run(ex.name, func(t *testing.T) {
			// Through the framing, as a peer would send it.
			var framed bytes.Buffer
			if err := WriteMsg(&framed, Msg{Body: ex.body}); err != nil {
				t.Fatal(err)
			}
			m, err := ReadMsg(&framed)
			if err != nil {
				t.Fatalf("ReadMsg: %v", err)
			}
			if ex.name != m.T+".json" && !strings.HasPrefix(ex.name, m.T+"-") {
				t.Fatalf("file %s holds a message of type %s", ex.name, m.T)
			}

			switch m.T {
			case TypeOK, TypeErr:
				checkReplyExample(t, m)
				return
			}
			v, ok := NewMessage(m.T)
			if !ok {
				if crossTypes[m.T] {
					t.Logf("%s: struct not registered yet (cross.go, P2-00); checked as an envelope", m.T)
					return
				}
				t.Fatalf("type %s has no struct", m.T)
			}
			if err := strictDecode(m.Body, v); err != nil {
				t.Fatalf("strict decode into %T: %v", v, err)
			}
			tolerant, _ := NewMessage(m.T)
			if err := m.Decode(tolerant); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if d, ok := v.(*Delta); ok {
				for i, c := range d.Changes {
					if c.Op != OpPut {
						continue
					}
					if err := strictDecode(c.Value, kindStruct(t, c.Kind)); err != nil {
						t.Fatalf("change %d: value of %s: %v", i, c.Kind, err)
					}
				}
				if err := NewState(Snapshot{Rev: d.Rev - 1}).Apply(*d); err != nil {
					t.Fatalf("Apply: %v", err)
				}
			}

			again := Request(m.ID, v)
			if again.T != m.T || again.ID != m.ID {
				t.Fatalf("encoded as %s id %d", again.T, again.ID)
			}
			if !sameJSON(t, m.Body, again.Body) {
				t.Fatalf("round trip changed the message\n  file: %s\n  back: %s", m.Body, again.Body)
			}
		})
	}
}

func kindStruct(t *testing.T, kind string) any {
	switch kind {
	case KindSession:
		return &Session{}
	case KindTab:
		return &Tab{}
	case KindPane:
		return &Pane{}
	case KindAgent:
		return &Agent{}
	}
	t.Fatalf("unknown kind %q", kind)
	return nil
}

func checkReplyExample(t *testing.T, m Msg) {
	t.Helper()
	r, ok := AsReply(m)
	if !ok || m.ID == 0 {
		t.Fatalf("%s is not a reply with an id", m.Body)
	}
	var again Msg
	if m.T == TypeErr {
		var b errBody
		if err := strictDecode(m.Body, &b); err != nil {
			t.Fatalf("err fields: %v", err)
		}
		if r.OK || r.Code == "" || r.Text == "" {
			t.Fatalf("err reply %+v", r)
		}
		again = ErrReply(m.ID, r.Code, r.Text)
	} else {
		var fields map[string]any
		if err := json.Unmarshal(m.Body, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, "t")
		delete(fields, "id")
		again = OKReply(m.ID, fields)
	}
	if !sameJSON(t, m.Body, again.Body) {
		t.Fatalf("round trip changed the reply\n  file: %s\n  back: %s", m.Body, again.Body)
	}
}

func TestEveryMessageTypeHasAWireExample(t *testing.T) {
	have := map[string]bool{}
	for _, ex := range readWireExamples(t) {
		var h struct {
			T string `json:"t"`
		}
		if err := json.Unmarshal(ex.body, &h); err != nil {
			t.Fatalf("%s: %v", ex.name, err)
		}
		have[h.T] = true
	}
	types := Types()
	if len(types) < 32 {
		t.Fatalf("only %d message types registered", len(types))
	}
	for _, typ := range append(types, TypeOK, TypeErr) {
		if !have[typ] {
			t.Errorf("message type %s has no example under testdata/wire", typ)
		}
	}
	for typ := range crossTypes {
		if !have[typ] {
			t.Errorf("message type %s of cross.go has no example under testdata/wire", typ)
		}
	}
}

func TestEveryMessageTypeRoundTripsThroughTheFraming(t *testing.T) {
	for _, typ := range Types() {
		v, _ := NewMessage(typ)
		if v.Type() != typ {
			t.Fatalf("struct %T says %s, registered as %s", v, v.Type(), typ)
		}
		var buf bytes.Buffer
		if err := WriteMsg(&buf, Request(5, v)); err != nil {
			t.Fatal(err)
		}
		m, err := ReadMsg(&buf)
		if err != nil || m.T != typ || m.ID != 5 {
			t.Fatalf("%s: got %+v, %v", typ, m, err)
		}
	}
}

func TestResultStructsDecodeFromOK(t *testing.T) {
	cases := []any{
		&SessionOpenResult{Session: "s1", Created: true},
		&SessionCloseResult{Closed: true},
		&TabNewResult{Tab: "t2", Pane: "p3"},
		&TabSelectResult{Tab: "t2"},
		&PaneSplitResult{Pane: "p4"},
		&PaneFocusResult{Moved: true, Pane: "p4"},
		&PaneTextResult{Text: "a\nb", Cut: true},
	}
	for _, want := range cases {
		m := OKReply(9, want)
		got := reflect.New(reflect.TypeOf(want).Elem()).Interface()
		if err := m.Decode(got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%T: got %+v, want %+v", want, got, want)
		}
	}
}

// --- Example rows of 4.2.5 ---

func readRows(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "rows", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []string
	seen := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.TrimSpace(line) != line {
			t.Fatalf("%s:%d: empty line or surrounding blanks", name, n)
		}
		row, err := strconv.Unquote(line)
		if err != nil || line[0] != '"' {
			t.Fatalf("%s:%d: not a Go-quoted string: %v", name, n, err)
		}
		if prev, dup := seen[row]; dup {
			t.Fatalf("%s:%d: the row of line %d again", name, n, prev)
		}
		seen[row] = n
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestExampleRowsAreWellFormedFiles(t *testing.T) {
	valid := readRows(t, "valid.txt")
	invalid := readRows(t, "invalid.txt")
	if len(valid) < 20 || len(invalid) < 30 {
		t.Fatalf("%d valid and %d invalid rows; want at least 20 and 30", len(valid), len(invalid))
	}
	inValid := map[string]bool{}
	for i, r := range valid {
		inValid[r] = true
		if err := checkRow(r); err != nil {
			t.Errorf("valid.txt:%d %q: %v", i+1, r, err)
		}
	}
	for i, r := range invalid {
		if inValid[r] {
			t.Errorf("invalid.txt:%d %q is also in valid.txt", i+1, r)
		}
		if checkRow(r) == nil {
			t.Errorf("invalid.txt:%d %q is inside the grammar of 4.2.5", i+1, r)
		}
	}
}

// checkRow is a reference reading of the grammar of 4.2.5, kept in the test
// so the example files are checked against the text of the plan and not
// against the filter they serve.
func checkRow(row string) error {
	if len(row) > MaxRow {
		return fmt.Errorf("longer than %d bytes", MaxRow)
	}
	if !utf8.ValidString(row) {
		return fmt.Errorf("not UTF-8")
	}
	for i := 0; i < len(row); {
		switch {
		case strings.HasPrefix(row[i:], "\x1b[0"):
			n, err := sgrLen(row[i:])
			if err != nil {
				return fmt.Errorf("at %d: %v", i, err)
			}
			i += n
		case strings.HasPrefix(row[i:], "\x1b]8;;"):
			end := strings.Index(row[i+5:], "\x1b\\")
			if end < 0 {
				return fmt.Errorf("at %d: link without ESC \\", i)
			}
			uri := row[i+5 : i+5+end]
			if len(uri) > 2048 {
				return fmt.Errorf("at %d: uri longer than 2048", i)
			}
			for j := 0; j < len(uri); j++ {
				if uri[j] < 0x21 || uri[j] > 0x7e {
					return fmt.Errorf("at %d: byte %#x in a uri", i, uri[j])
				}
			}
			i += 5 + end + 2
		default:
			r, size := utf8.DecodeRuneInString(row[i:])
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				return fmt.Errorf("at %d: control character %U", i, r)
			}
			i += size
		}
	}
	return nil
}

var simpleAttrs = map[string]bool{
	"1": true, "2": true, "3": true, "5": true, "7": true, "8": true, "9": true,
	"4": true, "4:2": true, "4:3": true, "4:4": true, "4:5": true,
}

func sgrLen(s string) (int, error) {
	end := strings.IndexByte(s, 'm')
	if end < 0 {
		return 0, fmt.Errorf("sgr without m")
	}
	params := s[2:end]
	for _, c := range params {
		if !(c >= '0' && c <= '9') && c != ';' && c != ':' {
			return 0, fmt.Errorf("sgr with %q", c)
		}
	}
	parts := strings.Split(params, ";")
	if parts[0] != "0" {
		return 0, fmt.Errorf("sgr does not start with 0")
	}
	for i := 1; i < len(parts); {
		p := parts[i]
		switch {
		case simpleAttrs[p]:
			i++
		case len(p) == 2 && (p[0] == '3' || p[0] == '4' || p[0] == '9') && p[1] >= '0' && p[1] <= '7':
			i++
		case len(p) == 3 && p[:2] == "10" && p[2] >= '0' && p[2] <= '7':
			i++
		case p == "38" || p == "48" || p == "58":
			if i+1 >= len(parts) {
				return 0, fmt.Errorf("colour %s without form", p)
			}
			want := 0
			switch parts[i+1] {
			case "5":
				want = 1
			case "2":
				want = 3
			default:
				return 0, fmt.Errorf("colour %s with form %q", p, parts[i+1])
			}
			if i+2+want > len(parts) {
				return 0, fmt.Errorf("colour %s cut short", p)
			}
			for _, n := range parts[i+2 : i+2+want] {
				if len(n) < 1 || len(n) > 3 {
					return 0, fmt.Errorf("colour value %q", n)
				}
				if v, err := strconv.Atoi(n); err != nil || v > 255 {
					return 0, fmt.Errorf("colour value %q", n)
				}
			}
			i += 2 + want
		default:
			return 0, fmt.Errorf("attribute %q", p)
		}
	}
	return end + 1, nil
}

func TestTypesAreSorted(t *testing.T) {
	ts := Types()
	if !sort.StringsAreSorted(ts) {
		t.Fatalf("Types() not sorted: %v", ts)
	}
}
