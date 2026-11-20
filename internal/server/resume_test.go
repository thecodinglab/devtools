package server

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"devtools/internal/agent"
	"devtools/internal/agent/resume"
	"devtools/internal/proto"
	"devtools/internal/server/persist"
	"devtools/internal/testkit"
)

const convID = "2ef64223-3735-4270-b86b-9f035f7e32e0"

func TestShellWordsQuoteOnlyWhatNeedsIt(t *testing.T) {
	for _, c := range []struct {
		words []string
		want  string
		ok    bool
	}{
		{[]string{"claude", "--resume", convID}, "claude --resume " + convID, true},
		{[]string{"/nix/store/x-zsh-5.9/bin/zsh", "-l"}, "/nix/store/x-zsh-5.9/bin/zsh -l", true},
		{[]string{"/Applications/My Shell/zsh"}, "'/Applications/My Shell/zsh'", true},
		{[]string{"a$b", "c;d", "e*"}, "'a$b' 'c;d' 'e*'", true},
		{[]string{"it's"}, "", false},
		{[]string{`back\slash`}, "", false},
		{[]string{"new\nline"}, "", false},
		{[]string{""}, "", false},
		{nil, "", false},
	} {
		got, ok := shellWords(c.words)
		if got != c.want || ok != c.ok {
			t.Errorf("shellWords(%q) = %q, %v; want %q, %v", c.words, got, ok, c.want, c.ok)
		}
	}
}

func TestResumeArgvRunsTheAgentThenTheShellAgain(t *testing.T) {
	claude := &persist.Resume{Kind: "claude", ID: convID}
	argv, ok := resumeArgv("/bin/zsh", claude)
	want := []string{"/bin/zsh", "-i", "-c",
		"if typeset -f _direnv_hook >/dev/null 2>&1; then _direnv_hook || true; fi; claude --resume " + convID + " || true; exec /bin/zsh -l"}
	if !ok || !slices.Equal(argv, want) {
		t.Fatalf("zsh: %q, %v\nwant %q", argv, ok, want)
	}

	argv, ok = resumeArgv("/usr/bin/fish", claude)
	want = []string{"/usr/bin/fish", "-i", "-c",
		"functions -q __direnv_export_eval; and __direnv_export_eval; claude --resume " + convID + " || true; exec /usr/bin/fish -l"}
	if !ok || !slices.Equal(argv, want) {
		t.Fatalf("fish: %q, %v\nwant %q", argv, ok, want)
	}

	argv, ok = resumeArgv("/bin/sh", claude)
	if !ok || argv[3] != "claude --resume "+convID+" || true; exec /bin/sh -l" {
		t.Fatalf("sh has no prelude: %q", argv)
	}

	for name, c := range map[string]struct {
		shell string
		r     *persist.Resume
	}{
		"no record":        {"/bin/zsh", nil},
		"no shell":         {"", claude},
		"codex":            {"/bin/zsh", &persist.Resume{Kind: "codex", ID: convID}},
		"unknown kind":     {"/bin/zsh", &persist.Resume{Kind: "aider", ID: convID}},
		"not a claude id":  {"/bin/zsh", &persist.Resume{Kind: "claude", ID: "latest"}},
		"unquotable shell": {"/home/o'brien/zsh", claude},
	} {
		if argv, ok := resumeArgv(c.shell, c.r); ok {
			t.Errorf("%s: resumed with %q", name, argv)
		}
	}
}

// recordingResumer resumes by running a script that records its arguments.
type recordingResumer struct{ program string }

func (recordingResumer) Lookup(resume.Process) (string, bool) { return "", false }
func (recordingResumer) Valid(id string) bool                 { return resume.Claude{}.Valid(id) }
func (r recordingResumer) Command(id string) []string {
	return []string{r.program, "--resume", id}
}

// TestTheResumeCommandRunsInRealShells runs the argument vector of a
// resumed pane in each shell this machine has. The startup files turn
// errexit on and the agent fails: the agent still gets its arguments after
// the startup files and direnv's hook ran, and the shell after it still
// starts (it runs what stdin holds).
func TestTheResumeCommandRunsInRealShells(t *testing.T) {
	found := 0
	for _, name := range []string{"sh", "bash", "zsh", "fish"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		found++
		t.Run(name, func(t *testing.T) {
			home := testkit.ShortTempDir(t)
			out := filepath.Join(home, "out")
			agentPath := filepath.Join(home, "agent")
			script := "#!/bin/sh\n{ test -e '" + home + "/hooked' && echo hooked; echo \"$@\"; } >> '" + out + "'\nexit 1\n"
			if err := os.WriteFile(agentPath, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			hook := "_direnv_hook() { : > '" + home + "/hooked'; return 1; }\n"
			files := map[string]string{
				".shrc":                    "set -e\n",
				".bash_profile":            hook + "set -e\n", // a pane's bash is a login shell
				".zshrc":                   hook + "setopt err_exit\n",
				".config/fish/config.fish": "function __direnv_export_eval; touch '" + home + "/hooked'; end\n",
			}
			for rel, body := range files {
				p := filepath.Join(home, rel)
				mkdir(t, filepath.Dir(p))
				if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			old := resumeFor
			resumeFor = func(k agent.Kind) (resume.Resumer, bool) { return recordingResumer{agentPath}, k == agent.Claude }
			t.Cleanup(func() { resumeFor = old })

			argv, ok := resumeArgv(path, &persist.Resume{Kind: "claude", ID: convID})
			if !ok {
				t.Fatal("not resumed")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Args[0] = "-" + name // a login shell, as a pane starts it
			cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"PATH=" + os.Getenv("PATH"), "SHELL=" + path, "TERM=dumb", "ENV=" + filepath.Join(home, ".shrc")}
			cmd.Dir = home
			var stderr bytes.Buffer
			after := filepath.Join(home, "after")
			cmd.Stdin = strings.NewReader("echo after > '" + after + "'\n") // read by the shell after the agent only
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil && ctx.Err() != nil {
				t.Fatalf("%s did not end: %s", name, stderr.String())
			}
			b, _ := os.ReadFile(out)
			want := "--resume " + convID + "\n"
			if name != "sh" {
				want = "hooked\n" + want
			}
			if string(b) != want {
				t.Fatalf("the agent recorded %q, want %q; stderr:\n%s", b, want, stderr.String())
			}
			if b, _ := os.ReadFile(after); string(b) != "after\n" {
				t.Fatalf("no shell ran after the failed agent; stderr:\n%s", stderr.String())
			}
		})
	}
	if found == 0 {
		t.Skip("no shell found")
	}
}

// claudeConfig points CLAUDE_CONFIG_DIR at a new directory and returns a
// function that writes the session file of a Claude Code process.
func claudeConfig(t *testing.T) func(pid int, id string) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "sessions"))
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return func(pid int, id string) {
		t.Helper()
		body := `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"` + id + `","kind":"interactive"}`
		if err := os.WriteFile(filepath.Join(dir, "sessions", strconv.Itoa(pid)+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// savedResume is the record of pane p in state.json of ts, nil for none.
func savedResume(t *testing.T, ts *testServer, p string) *persist.Resume {
	t.Helper()
	for _, s := range saved(t, ts).Sessions {
		for _, tab := range s.Tabs {
			for _, pp := range tab.Panes {
				if pp.ID == p {
					return pp.Resume
				}
			}
		}
	}
	return nil
}

func waitSavedResume(t *testing.T, ts *testServer, p, id string) {
	t.Helper()
	testkit.Eventually(t, 5*time.Second, "state.json resumes "+strconv.Quote(id)+" in "+p, func() bool {
		r := savedResume(t, ts, p)
		if id == "" {
			return r == nil
		}
		return r != nil && r.Kind == "claude" && r.ID == id
	})
}

// shortGrace makes resumeGrace d for the test.
func shortGrace(t *testing.T, d time.Duration) {
	old := resumeGrace
	resumeGrace = d
	t.Cleanup(func() { resumeGrace = old })
}

// claudePgid is the process group of the Claude Code a test runs first.
const claudePgid = fakePidBase + 9000

// recordAndRestart runs Claude Code holding convID in a pane of a new
// server, stops that server (which saves the state) and starts another on
// its state; opts change the second one's options. It returns the second
// server, the session and the pane.
func recordAndRestart(t *testing.T, writeSession func(int, string), opts ...func(*Options)) (*testServer, string, string) {
	t.Helper()
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	writeSession(claudePgid, convID)
	runClaude(t, ts, a, p, claudePgid)
	waitSavedResume(t, ts, p, convID)
	if strings.Contains(strings.Join(ts.pane(p).o.Argv, " "), "--resume") {
		t.Fatal("a pane that never had a record started resumed")
	}
	ts.s.Shutdown() // the stop saves the state; the agent is still running
	base := func(o *Options) { o.StateDir = ts.state; o.ProcInterval = 50 * time.Millisecond }
	next := newTestServer(t, append([]func(*Options){base}, opts...)...)
	next.state = ts.state
	if r := savedResume(t, next, p); r == nil || r.ID != convID {
		t.Fatalf("the restored state resumes %+v", r)
	}
	return next, s, p
}

func TestAClaudeConversationIsResumedAfterARestart(t *testing.T) {
	shortGrace(t, time.Second)
	writeSession := claudeConfig(t)
	next, s, p := recordAndRestart(t, writeSession)
	b := next.interactive()
	if st := b.State(); st.Panes[p].State != proto.PaneDormant {
		t.Fatalf("pane %s is %s before it is displayed", p, st.Panes[p].State)
	}
	next.view(b, s, 80, 24)
	f := next.pane(p)
	want := []string{"/bin/sh", "-i", "-c", "claude --resume " + convID + " || true; exec /bin/sh -l"}
	if !slices.Equal(f.o.Argv, want) || f.o.Arg0 != "-sh" {
		t.Fatalf("the restored pane runs %q as %q, want %q as -sh", f.o.Argv, f.o.Arg0, want)
	}
	// Until the resumed agent is found the record stays saved: a stop now
	// would resume it again.
	if r := savedResume(t, next, p); r == nil || r.ID != convID {
		t.Fatalf("after the start state.json resumes %+v", r)
	}
	// The resumed agent holds the conversation: the record is its own now,
	// and the grace does not drop it.
	writeSession(claudePgid+1, convID)
	runClaude(t, next, b, p, claudePgid+1)
	time.Sleep(resumeGrace + 300*time.Millisecond)
	next.do(func(c *core) {})
	if r := savedResume(t, next, p); r == nil || r.ID != convID {
		t.Fatalf("after the grace state.json resumes %+v", r)
	}
}

func TestAResumedAgentThatIsNotSeenIsNotResumedAgain(t *testing.T) {
	shortGrace(t, 200*time.Millisecond)
	next, s, p := recordAndRestart(t, claudeConfig(t))
	b := next.interactive()
	next.view(b, s, 80, 24)
	if f := next.pane(p); !slices.Contains(f.o.Argv, "-c") {
		t.Fatalf("the pane did not resume: %q", f.o.Argv)
	}
	waitSavedResume(t, next, p, "")
}

func TestAConfiguredShellIsNotResumed(t *testing.T) {
	next, s, p := recordAndRestart(t, claudeConfig(t), func(o *Options) { o.Shell = []string{"/bin/bash", "--norc"} })
	b := next.interactive()
	next.view(b, s, 80, 24)
	if f := next.pane(p); !slices.Equal(f.o.Argv, []string{"/bin/bash", "--norc"}) {
		t.Fatalf("the configured shell runs %q", f.o.Argv)
	}
	waitSavedResume(t, next, p, "")
}

func TestTheRecordFollowsTheConversationAndEndsWithTheAgent(t *testing.T) {
	old := resumeRefresh
	resumeRefresh = 50 * time.Millisecond
	t.Cleanup(func() { resumeRefresh = old })
	writeSession := claudeConfig(t)
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	pgid := fakePidBase + 9200

	// An agent without a session file is not recorded until it writes one.
	f := runClaude(t, ts, a, p, pgid)
	time.Sleep(200 * time.Millisecond)
	if r := savedResume(t, ts, p); r != nil {
		t.Fatalf("recorded %+v without a session file", r)
	}
	writeSession(pgid, convID)
	waitSavedResume(t, ts, p, convID)

	// /clear: the same process now holds another conversation.
	const second = "0d5d7a35-2a52-4a3b-9b56-3a1e47c1c0aa"
	writeSession(pgid, second)
	waitSavedResume(t, ts, p, second)

	// Another Claude Code in the pane that holds no known conversation: the
	// first one's is not its own.
	setProcs(t, append(group(pgid+50, "claude"), group(f.Info().Shell, "zsh")...)...)
	f.setFg(pgid + 50)
	waitSavedResume(t, ts, p, "")

	// Claude Code leaves the pane for its idle shell: nothing is resumed.
	writeSession(pgid+50, convID)
	waitSavedResume(t, ts, p, convID)
	setProcs(t, group(f.Info().Shell, "zsh")...)
	f.setFg(f.Info().Shell)
	a.WaitState("the agent is gone", func(st proto.State) bool { _, ok := st.Agents[p]; return !ok })
	waitSavedResume(t, ts, p, "")
}

func TestAnotherKindOfAgentInThePaneClearsTheRecord(t *testing.T) {
	writeSession := claudeConfig(t)
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	pgid := fakePidBase + 9300
	writeSession(pgid, convID)
	f := runClaude(t, ts, a, p, pgid)
	waitSavedResume(t, ts, p, convID)

	setProcs(t, append(group(pgid+1, "codex"), group(f.Info().Shell, "zsh")...)...)
	f.setFg(pgid + 1)
	a.WaitState("the pane runs codex", func(st proto.State) bool { return st.Agents[p].Kind == proto.AgentCodex })
	waitSavedResume(t, ts, p, "")
}

func TestAnAgentWhoseGroupCannotBeReadEnds(t *testing.T) {
	writeSession := claudeConfig(t)
	ts := agentServer(t)
	c := ts.oneshot()
	s, _, p := ts.open(c)
	a := ts.interactive()
	ts.view(a, s, 80, 24)
	pgid := fakePidBase + 9400
	writeSession(pgid, convID)
	f := runClaude(t, ts, a, p, pgid)
	waitSavedResume(t, ts, p, convID)

	// The foreground group is one the process table has no process of.
	setProcs(t, group(f.Info().Shell, "zsh")...)
	f.setFg(pgid + 77)
	a.WaitState("the agent is gone", func(st proto.State) bool { _, ok := st.Agents[p]; return !ok })
	waitSavedResume(t, ts, p, "")
}
