package tmux

import "testing"

func TestSessionName(t *testing.T) {
	tests := map[string]string{
		SessionName("project", "main"):              "project-main",
		SessionName("My Project", "feature/test"):   "my-project-feature-test",
		SessionName("project", "feature.test_more"): "project-feature-test-more",
		SessionName("!!!", "???"):                   "devtools",
	}
	for got, want := range tests {
		if got != want {
			t.Fatalf("SessionName result = %q, want %q", got, want)
		}
	}
}
