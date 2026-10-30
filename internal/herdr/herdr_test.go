package herdr

import (
	"errors"
	"reflect"
	"testing"
)

func TestActive(t *testing.T) {
	tests := []struct {
		name                  string
		env, socket, tmuxPath string
		want                  bool
	}{
		{name: "outside", want: false},
		{name: "pane", env: "1", want: true},
		{name: "custom command", socket: "/tmp/herdr.sock", want: true},
		{name: "tmux inside herdr", env: "1", tmuxPath: "/tmp/tmux,1,0", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HERDR_ENV", tt.env)
			t.Setenv("HERDR_SOCKET_PATH", tt.socket)
			t.Setenv("TMUX", tt.tmuxPath)
			if got := Active(); got != tt.want {
				t.Fatalf("Active() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseWorkspaceList(t *testing.T) {
	got, err := parseWorkspaceList([]byte(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[` +
		`{"active_tab_id":"w1:t1","focused":true,"label":"project-main","number":1,"pane_count":1,"tab_count":1,"workspace_id":"w1"},` +
		`{"active_tab_id":"w2:t1","focused":false,"label":"ops","number":2,"pane_count":3,"tab_count":2,"workspace_id":"w2"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Workspace{
		{ID: "w1", Label: "project-main", Tabs: 1, Focused: true},
		{ID: "w2", Label: "ops", Tabs: 2, Focused: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspaces = %#v, want %#v", got, want)
	}
}

func TestParsePaneList(t *testing.T) {
	got, err := parsePaneList([]byte(`{"id":"cli:pane:list","result":{"type":"pane_list","panes":[` +
		`{"pane_id":"w1:p1","tab_id":"w1:t1"},{"pane_id":"w1:p3","tab_id":"w1:t2"},{"pane_id":"w1:p2","tab_id":"w1:t1"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"w1:t1": {"w1:p1", "w1:p2"},
		"w1:t2": {"w1:p3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("panes = %#v, want %#v", got, want)
	}
}

func TestErrorMessage(t *testing.T) {
	fallback := errors.New("exit status 1")
	tests := map[string]string{
		`{"error":{"code":"workspace_not_found","message":"workspace nope not found"},"id":"cli:workspace:focus"}`: "workspace nope not found",
		"connection refused\n": "connection refused",
		"":                     "exit status 1",
	}
	for stderr, want := range tests {
		if got := errorMessage([]byte(stderr), fallback); got != want {
			t.Fatalf("errorMessage(%q) = %q, want %q", stderr, got, want)
		}
	}
}
