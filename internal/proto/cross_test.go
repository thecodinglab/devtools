package proto

import (
	"bytes"
	"reflect"
	"testing"
)

// crossSamples holds one filled struct of every message type of cross.go,
// with the id it travels with (0: a notification or a routed copy).
func crossSamples() []struct {
	id uint64
	v  Typed
} {
	return []struct {
		id uint64
		v  Typed
	}{
		{1, &UIHub{Mode: HubAll, Query: "dev", Here: "/home/florian/dev/devtools/main"}},
		{0, &UIHub{Mode: HubAgents, Here: "/home/florian/dev/devtools/main", Pane: "p52"}},
		{2, &UISwitch{Session: "s4"}},
		{3, &UISwitch{Dir: "/home/florian/dev/x/main", Name: "x-main", Tab: "t17", Pane: "p52"}},
		{0, &UISwitch{Dir: "/home/florian/dev/x/main", Name: "x-main"}},
		{7, &CatalogWatch{}},
		{7, &CatalogItem{Items: []CatalogEntry{
			{Kind: CatalogWorktree, Project: "devtools", Worktree: "main", Label: "devtools/main",
				Path: "/home/florian/dev/devtools/main", Root: "/home/florian/dev", Session: "devtools-main"},
			{Kind: CatalogRepo, Project: "notes", Label: "notes", Path: "/home/florian/dev/notes",
				Root: "/home/florian/dev", Session: "notes"},
			{Kind: CatalogBookmark, Label: "work", Path: "/home/florian/work", Session: "work"},
		}}},
		{7, &CatalogGit{Path: "/home/florian/dev/devtools/main",
			Git: Git{Changes: 2, Ahead: 3, Behind: 1, HasUpstream: true, Detached: true, Branch: "main"}}},
		{7, &CatalogError{Path: "/home/florian/work", Text: "permission denied"}},
		{7, &CatalogDone{}},
		{7, &CatalogStop{}},
		{0, &AgentEvent{Session: "s4", Tab: "t17", Pane: "p52", Kind: AgentClaude,
			Status: StatusBlocked, Title: "fix the tests", Shown: true}},
		{0, &AgentEvent{Session: "s4", Tab: "t18", Pane: "p53", Kind: AgentCodex, Status: StatusDone}},
		{0, &UpdateEvent{Installed: "0.1.0+04c5fef"}},
		{0, &UpdateEvent{}},
	}
}

func TestCrossMessagesRoundTrip(t *testing.T) {
	covered := map[string]bool{}
	for _, s := range crossSamples() {
		typ := s.v.Type()
		covered[typ] = true
		if !crossTypes[typ] {
			t.Errorf("%s is not a message type of cross.go", typ)
		}
		var buf bytes.Buffer
		if err := WriteMsg(&buf, Request(s.id, s.v)); err != nil {
			t.Fatalf("%s: WriteMsg: %v", typ, err)
		}
		m, err := ReadMsg(&buf)
		if err != nil {
			t.Fatalf("%s: ReadMsg: %v", typ, err)
		}
		if m.T != typ || m.ID != s.id {
			t.Fatalf("%s: read back as %s id %d", typ, m.T, m.ID)
		}
		fresh, ok := NewMessage(m.T)
		if !ok {
			t.Fatalf("%s is not registered", typ)
		}
		if reflect.TypeOf(fresh) != reflect.TypeOf(s.v) {
			t.Fatalf("%s registered as %T, sent as %T", typ, fresh, s.v)
		}
		if err := m.Decode(fresh); err != nil {
			t.Fatalf("%s: Decode: %v", typ, err)
		}
		if !reflect.DeepEqual(fresh, s.v) {
			t.Errorf("%s: round trip changed the message\n  sent: %+v\n  got:  %+v", typ, s.v, fresh)
		}
		if err := strictDecode(m.Body, newMessageOf(t, typ)); err != nil {
			t.Errorf("%s: the encoding carries a field the struct lacks: %v", typ, err)
		}
	}
	for typ := range crossTypes {
		if !covered[typ] {
			t.Errorf("message type %s of cross.go has no sample in this test", typ)
		}
		if _, ok := NewMessage(typ); !ok {
			t.Errorf("message type %s of cross.go is not registered", typ)
		}
	}
	for _, c := range []string{CapCatalog, CapAgents, CapRoute, CapStateGet} {
		if c == "" {
			t.Errorf("empty capability name")
		}
	}
	if got := []string{CapCatalog, CapAgents, CapRoute, CapStateGet}; !reflect.DeepEqual(got,
		[]string{"catalog", "agents", "route", "stateget"}) {
		t.Errorf("capability names = %q", got)
	}
}

// newMessageOf is NewMessage for a type the test knows is registered.
func newMessageOf(t *testing.T, typ string) Typed {
	t.Helper()
	v, ok := NewMessage(typ)
	if !ok {
		t.Fatalf("%s is not registered", typ)
	}
	return v
}

func TestUnknownFieldsAreIgnored(t *testing.T) {
	tests := []struct {
		body string
		want Typed
	}{
		{`{"t":"ui.hub","id":1,"mode":"open","query":"x","here":"/a","future":{"deep":[1]},"machine":"evil"}`,
			&UIHub{Mode: HubOpen, Query: "x", Here: "/a"}},
		{`{"t":"ui.switch","session":"s4","machine":"evil","later":true}`,
			&UISwitch{Session: "s4"}},
		{`{"t":"catalog.watch","id":7,"path":"/etc","root":"/"}`,
			&CatalogWatch{}},
		{`{"t":"catalog.item","id":7,"items":[{"kind":"repo","path":"/p","label":"p","extra":[1,2],"stars":5}],"more":1}`,
			&CatalogItem{Items: []CatalogEntry{{Kind: CatalogRepo, Path: "/p", Label: "p"}}}},
		{`{"t":"catalog.git","id":7,"path":"/p","git":{"changes":1,"stash":4,"branch":"b"},"x":null}`,
			&CatalogGit{Path: "/p", Git: Git{Changes: 1, Branch: "b"}}},
		{`{"t":"catalog.error","id":7,"path":"/p","text":"t","errno":13}`,
			&CatalogError{Path: "/p", Text: "t"}},
		{`{"t":"catalog.done","id":7,"count":12}`, &CatalogDone{}},
		{`{"t":"catalog.stop","id":7,"why":"closed"}`, &CatalogStop{}},
		{`{"t":"event.agent","session":"s4","tab":"t17","pane":"p52","kind":"claude","status":"done","title":"x","shown":true,"tokens":9}`,
			&AgentEvent{Session: "s4", Tab: "t17", Pane: "p52", Kind: AgentClaude, Status: StatusDone, Title: "x", Shown: true}},
		// An unknown value of an enumerated field with an "unknown" is read as it.
		{`{"t":"event.agent","session":"s4","tab":"t17","pane":"p52","kind":"gemini","status":"thinking","title":"","shown":false}`,
			&AgentEvent{Session: "s4", Tab: "t17", Pane: "p52", Kind: Unknown, Status: Unknown}},
	}
	for _, tc := range tests {
		m, err := ReadMsg(bytes.NewReader(frameBytes([]byte(tc.body))))
		if err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		got := newMessageOf(t, m.T)
		if err := m.Decode(got); err != nil {
			t.Fatalf("%s: Decode: %v", tc.body, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s\n  got:  %+v\n  want: %+v", tc.body, got, tc.want)
		}
	}
}

// A welcome without installed, the one of a server from before CapUpdate,
// reads as no update waiting; one with it reads the version, and an older
// receiver's tolerant decode skips the field.
func TestWelcomeInstalledIsOptional(t *testing.T) {
	var old Welcome
	if err := (Msg{T: TypeWelcome, Body: []byte(`{"t":"welcome","gen":1,"version":"dev","boot":"b","host":"h","pid":1,"client":"c1","clients":1}`)}).Decode(&old); err != nil || old.Installed != "" {
		t.Fatalf("old welcome: %+v, %v", old, err)
	}
	m := Encode(Welcome{Gen: Gen, Version: "0.1.0+6ee3170"})
	if bytes.Contains(m.Body, []byte("installed")) {
		t.Fatalf("a welcome without an update carries installed: %s", m.Body)
	}
	m = Encode(Welcome{Gen: Gen, Version: "0.1.0+6ee3170", Installed: "0.1.0+04c5fef"})
	var older struct {
		Version string `json:"version"`
	}
	if err := m.Decode(&older); err != nil || older.Version != "0.1.0+6ee3170" {
		t.Fatalf("older receiver: %+v, %v", older, err)
	}
}
