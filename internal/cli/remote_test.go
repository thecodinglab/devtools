package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"devtools/internal/hub"
	"devtools/internal/peer"
	"devtools/internal/ui"
)

// stubDiscover makes the network answer with results.
func stubDiscover(t *testing.T, results ...peer.Result) {
	t.Helper()
	saved := discoverPeers
	discoverPeers = func(ctx context.Context, opts peer.Options) <-chan peer.Result {
		ch := make(chan peer.Result, len(results))
		for _, r := range results {
			ch <- r
		}
		close(ch)
		return ch
	}
	t.Cleanup(func() { discoverPeers = saved })
}

func macmini(items ...peer.Item) peer.Result {
	return peer.Result{
		Announce: peer.Announce{Name: "macmini", Hostname: "mini.local", SSH: "macmini"},
		Snapshot: peer.Snapshot{Name: "macmini", Hostname: "mini.local", SSH: "macmini", Multiplexer: "herdr", HerdrRunning: true, Items: items},
	}
}

var xcloudMain = peer.Item{
	Kind: "worktree", Project: "xcloud", Worktree: "main", Label: "xcloud/main",
	Path: "/Users/f/dev/xcloud/main", Root: "/Users/f/dev", Session: "xcloud-main",
	Git:   ui.Git{Changes: 2},
	State: ui.Session{Open: true, Focused: true, Agent: "blocked", Title: "Approve\x1b[31m rm?"},
}

// installFakeSSH records the arguments of every ssh run, one per line.
func installFakeSSH(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ssh")
	log := filepath.Join(t.TempDir(), "ssh.log")
	script := "#!/bin/sh\nfor arg in \"$@\"; do printf '%s\\n' \"$arg\"; done >> \"$SSH_LOG\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_LOG", log)
	saved := sshCommand
	sshCommand = bin
	t.Cleanup(func() { sshCommand = saved })
	return log
}

// remoteFromHub hands the switcher's items of other machines to pick.
func remoteFromHub(t *testing.T, pick func(hub.Options, []hub.Item) (*hub.Item, error)) {
	t.Helper()
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		var items []hub.Item
		for batch := range opts.Remote {
			items = append(items, batch...)
		}
		return pick(opts, items)
	})
}

func TestSwitcherListsOtherMachinesAndSwitchesOverSSH(t *testing.T) {
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	sshLog := installFakeSSH(t)
	evil := macmini(xcloudMain)
	evil.Snapshot.Name, evil.Snapshot.Hostname, evil.Snapshot.SSH = "evil", "evil", "-oProxyCommand=sh"
	stubDiscover(t, macmini(xcloudMain), evil)
	remoteFromHub(t, func(opts hub.Options, items []hub.Item) (*hub.Item, error) {
		if len(items) != 1 {
			t.Fatalf("remote items = %+v, want the one on macmini", items)
		}
		item := items[0]
		if item.Host != "macmini" || item.Kind != hub.KindWorktree || item.Git.Changes != 2 || item.State.Agent != "blocked" {
			t.Fatalf("item = %+v", item)
		}
		if item.State.Title != "Approve[31m rm?" || item.State.Focused {
			t.Fatalf("state = %+v, want control characters and focus dropped", item.State)
		}
		return &item, nil
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
	want := "-t\nmacmini\ndevtools session-open --session xcloud-main -- /Users/f/dev/xcloud/main"
	if got := readLog(t, sshLog); got != want {
		t.Fatalf("ssh args =\n%s\nwant\n%s", got, want)
	}
}

func TestSwitchInsideHerdrSavesAnUnknownMachineInHerdrAndShowsIt(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	took := patchedHerdrClient(t)
	stubDiscover(t, macmini(xcloudMain))
	var note string
	remoteFromHub(t, func(opts hub.Options, items []hub.Item) (*hub.Item, error) {
		if !opts.SwitchInPlace {
			t.Fatal("inside herdr the switcher switches in place")
		}
		var err error
		note, err = opts.Backend.Switch(items[0])
		return nil, err
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
	got := herdrCalls(t, log)
	for _, want := range []string{
		"machine add macmini --label macmini",
		"--machine 0123456789abcdef0123456789abcdef workspace create --cwd /Users/f/dev/xcloud/main --label xcloud-main --focus",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("herdr args =\n%s\nwant a line\n%s", got, want)
		}
	}
	assertNoNesting(t, got)
	if requests := took(); len(requests) != 1 || requests[0].Machine != "0123456789abcdef0123456789abcdef" || requests[0].Workspace != "w9" {
		t.Fatalf("requests = %+v", requests)
	}
	if note != "" {
		t.Fatalf("note = %q", note)
	}
}

// assertNoNesting fails when devtools ran a herdr in a herdr: a local
// workspace running ssh to the other machine.
func assertNoNesting(t *testing.T, herdrCalls string) {
	t.Helper()
	if strings.Contains(herdrCalls, "pane run") || strings.Contains(herdrCalls, " -t macmini") {
		t.Fatalf("herdr args =\n%s\nwant no ssh in a pane here", herdrCalls)
	}
}

func TestSwitchInsideHerdrTurnsADisabledMachineBackOn(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	t.Setenv("HERDR_MACHINES", `{"id":"ad61d571470a4ff1010dece8eaf53b5c","label":"macmini","target":"macmini","session":"default","enabled":false,"selected":false}`)
	patchedHerdrClient(t)
	stubDiscover(t, macmini(xcloudMain))
	remoteFromHub(t, func(opts hub.Options, items []hub.Item) (*hub.Item, error) {
		_, err := opts.Backend.Switch(items[0])
		return nil, err
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
	got := herdrCalls(t, log)
	if !strings.Contains(got, "machine enable ad61d571470a4ff1010dece8eaf53b5c") || strings.Contains(got, "machine add") {
		t.Fatalf("herdr args =\n%s", got)
	}
}

func TestSwitchInsideAnUnpatchedHerdrNeverNestsHerdr(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	t.Setenv("HERDR_MACHINES", `{"id":"m1","label":"mini","target":"macmini","session":"default","enabled":true,"selected":false}`)
	stubDiscover(t, macmini(xcloudMain))
	var note string
	remoteFromHub(t, func(opts hub.Options, items []hub.Item) (*hub.Item, error) {
		var err error
		note, err = opts.Backend.Switch(items[0])
		return nil, err
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
	// no client takes the request: the window says where the workspace is,
	// and the switcher closes like after any switch
	got := herdrCalls(t, log)
	if !strings.Contains(got, "notification show xcloud/main is open on macmini --body") {
		t.Fatalf("herdr args =\n%s\nwant a notification", got)
	}
	assertNoNesting(t, got)
	if note != "" {
		t.Fatalf("note = %q, want the switcher to close", note)
	}
}

func TestSwitchInsideTmuxHandsTheTerminalToSSH(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "tmux.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TMUX_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_LOG", log)
	t.Setenv("TMUX", "/tmp/tmux-1/default,1,0")

	b := hubBackend{backend: "tmux", remotes: &remotes{hosts: map[string]remoteHost{}}}
	b.remotes.add(macmini().Snapshot)
	items := remoteItems("macmini", []peer.Item{xcloudMain})
	if _, err := b.Switch(items[0]); err != nil {
		t.Fatal(err)
	}
	// the tmux client gives way to ssh: nothing over there runs in tmux here
	want := "detach-client -E " + sshCommand + " -t macmini 'devtools session-open --session xcloud-main -- /Users/f/dev/xcloud/main'"
	if got := readLog(t, log); got != want {
		t.Fatalf("tmux args =\n%s\nwant\n%s", got, want)
	}
}

func TestSnapshotHoldsWhatTheSwitcherShows(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "small")
	makeRepo(t, project)
	t.Setenv("DEVTOOLS_ROOT", root)
	installFakeTmuxWithSessions(t, "small-main\t1\t1\nops\t2\t0")

	snapshot, err := (&app{}).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Multiplexer != "tmux" || len(snapshot.Items) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	small, ops := snapshot.Items[0], snapshot.Items[1]
	if small.Kind != "repo" || small.Path != project || small.Session != "small-main" || !small.State.Open || small.Git.Branch != "main" {
		t.Fatalf("small = %+v", small)
	}
	if ops.Kind != "workspace" || ops.Session != "ops" || !ops.State.Open {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestSessionOpenSwitchesToTheSessionInDir(t *testing.T) {
	log := installFakeTmux(t)
	dir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"session-open", "--session", "xcloud-main", "--", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("session-open returned %d, stderr=%s", code, stderr.String())
	}
	if got, want := strings.Join(strings.Fields(readLog(t, log)), " "), "new-session -A -s xcloud-main -c "+dir; got != want {
		t.Fatalf("tmux args = %q, want %q", got, want)
	}
}

func TestSwitchQueryNothingHereMatchesLooksOnOtherMachines(t *testing.T) {
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	var query string
	stubHub(t, func(opts hub.Options) (*hub.Item, error) {
		query = opts.Query
		return nil, nil
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "xcloud"}, &stdout, &stderr); code != 0 {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
	if query != "xcloud" {
		t.Fatalf("switcher query = %q", query)
	}

	// with discovery off nothing is looked for elsewhere
	if err := os.WriteFile(os.Getenv("DEVTOOLS_CONFIG"), []byte("discovery off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(os.Getenv("DEVTOOLS_CONFIG")) })
	if code := Run([]string{"switch", "xcloud"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "no target matches") {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
}

func TestHostsListsEveryMachineThatAnswered(t *testing.T) {
	self, _ := os.Hostname()
	asleep := peer.Result{Announce: peer.Announce{Name: "macbook", Hostname: "book", SSH: "macbook"}, Addr: "192.168.48.11", Err: context.DeadlineExceeded}
	here := macmini(xcloudMain)
	here.Announce.Name, here.Announce.Hostname, here.Addr = "desktop", self, "192.168.48.10"
	stubDiscover(t, here, asleep)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"hosts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("hosts returned %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"desktop (this machine)", "1 items", "macbook", "192.168.48.11", "context deadline exceeded"} {
		if !strings.Contains(out, want) {
			t.Fatalf("hosts output lacks %q:\n%s", want, out)
		}
	}
}

func TestMachineWithoutANameIsNeverTakenForThisOne(t *testing.T) {
	installFakeTmux(t)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	nameless := macmini(xcloudMain)
	nameless.Snapshot.Name, nameless.Snapshot.Hostname = "\x1b\x07", "\x1b"
	stubDiscover(t, nameless)
	remoteFromHub(t, func(opts hub.Options, items []hub.Item) (*hub.Item, error) {
		if len(items) != 0 {
			t.Fatalf("items = %+v, want none from a machine without a name", items)
		}
		return nil, nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
}

func TestHostsDropsControlCharactersFromErrors(t *testing.T) {
	stubDiscover(t, peer.Result{
		Announce: peer.Announce{Name: "evil", Hostname: "evil", SSH: "evil"},
		Err:      errors.New("500: \x1b]52;c;aGk=\x07"),
	})
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"hosts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("hosts returned %d", code)
	}
	if strings.ContainsAny(stdout.String(), "\x1b\x07") {
		t.Fatalf("hosts output carries control characters: %q", stdout.String())
	}
}

type activation struct {
	Nonce     string `json:"nonce"`
	Machine   string `json:"machine"`
	Workspace string `json:"workspace"`
}

// patchedHerdrClient takes every request to show a workspace the way a
// herdr client with the activation patch does, and returns what it took.
func patchedHerdrClient(t *testing.T) func() []activation {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "herdr", "client")
	var mu sync.Mutex
	var took []activation
	done := make(chan struct{})
	go func() {
		seen := ""
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			data, err := os.ReadFile(filepath.Join(dir, "activate-request.json"))
			var request activation
			if err != nil || json.Unmarshal(data, &request) != nil || request.Nonce == seen {
				continue
			}
			seen = request.Nonce
			mu.Lock()
			took = append(took, request)
			mu.Unlock()
			ack, _ := json.Marshal(map[string]string{"nonce": request.Nonce})
			os.WriteFile(filepath.Join(dir, "activate-ack.json"), ack, 0o600)
		}
	}()
	t.Cleanup(func() { close(done) })
	return func() []activation {
		mu.Lock()
		defer mu.Unlock()
		return append([]activation(nil), took...)
	}
}

func TestSwitchInsideHerdrShowsTheSavedMachineInThisWindow(t *testing.T) {
	log := installFakeHerdr(t, testWorkspaces)
	t.Setenv("DEVTOOLS_ROOT", t.TempDir())
	t.Setenv("HERDR_MACHINES", `{"id":"ad61d571470a4ff1010dece8eaf53b5c","label":"mini","target":"macmini","session":"default","enabled":true,"selected":false}`)
	took := patchedHerdrClient(t)
	stubDiscover(t, macmini(xcloudMain))
	var note string
	remoteFromHub(t, func(opts hub.Options, items []hub.Item) (*hub.Item, error) {
		var err error
		note, err = opts.Backend.Switch(items[0])
		return nil, err
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"pick"}, &stdout, &stderr); code != 0 {
		t.Fatalf("pick returned %d, stderr=%s", code, stderr.String())
	}
	if note != "" {
		t.Fatalf("note = %q, the window shows the machine itself", note)
	}
	got := took()
	if len(got) != 1 || got[0].Machine != "ad61d571470a4ff1010dece8eaf53b5c" || got[0].Workspace != "w9" {
		t.Fatalf("requests = %+v, want the workspace created on the machine", got)
	}
	if !strings.Contains(herdrCalls(t, log), "--machine ad61d571470a4ff1010dece8eaf53b5c workspace create") {
		t.Fatalf("herdr args = %s", herdrCalls(t, log))
	}
}

func TestLocalSwitchBringsTheWindowBackFromAMachine(t *testing.T) {
	root := t.TempDir()
	makeRepo(t, filepath.Join(root, "small"))
	t.Setenv("DEVTOOLS_ROOT", root)
	installFakeHerdr(t, `{"label":"small-main","workspace_id":"w2"}`)
	t.Setenv("HERDR_MACHINES", `{"id":"ad61d571470a4ff1010dece8eaf53b5c","label":"mini","target":"macmini","session":"default","enabled":true,"selected":true}`)
	took := patchedHerdrClient(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
	if got := took(); len(got) != 1 || got[0].Machine != "" || got[0].Workspace != "w2" {
		t.Fatalf("requests = %+v, want Local's w2", got)
	}

	// a window that shows Local already is left alone
	t.Setenv("HERDR_MACHINES", `{"id":"ad61d571470a4ff1010dece8eaf53b5c","label":"mini","target":"macmini","session":"default","enabled":true,"selected":false}`)
	if code := Run([]string{"switch", "small"}, &stdout, &stderr); code != 0 {
		t.Fatalf("switch returned %d, stderr=%s", code, stderr.String())
	}
	if got := took(); len(got) != 1 {
		t.Fatalf("requests = %+v, want no second one", got)
	}
}
