package server

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"devtools/internal/agent"
	"devtools/internal/agent/resume"
	"devtools/internal/pane"
	"devtools/internal/server/model"
	"devtools/internal/server/persist"
)

// Resuming an agent after a restart (3.9; internal/agent/resume). A pane
// whose saved record names an agent conversation starts its shell as
//
//	<shell> -i -c '<prelude><agent command> || true; exec <shell> -l'
//
// so the shell's startup files set PATH and the environment first, the
// agent runs in its own process group in the foreground (an interactive
// shell has job control) where the trackers find it, and when it ends the
// pane is a fresh interactive login shell of the same process. The prelude
// runs direnv's hook when the startup files installed it: the hook runs
// before a prompt, and a -c shell draws none.
//
// Neither the agent nor the hook failing may end the shell before its exec:
// startup files can turn errexit on (set -e, setopt err_exit), and a shell
// that ends with a status closes its pane (events.go). A command left of
// ||, or the condition of an if, does not trip errexit in sh, bash or zsh;
// fish has no errexit and reads || from 3.0 on.
//
// The command is put together from words this package quotes; a word it
// cannot quote for every shell, an agent without a Resumer or an id that is
// not one of its ids leaves the pane a plain shell.

// resumeFor is the Resumer of a kind; the tests replace it.
var resumeFor = resume.For

// resumeArgv returns the argument vector of a pane's login shell, the
// program shell, that first resumes r.
func resumeArgv(shell string, r *persist.Resume) ([]string, bool) {
	if shell == "" || r == nil {
		return nil, false
	}
	res, ok := resumeFor(agent.Kind(r.Kind))
	if !ok || !res.Valid(r.ID) {
		return nil, false
	}
	cmd, ok := shellWords(res.Command(r.ID))
	if !ok {
		return nil, false
	}
	exec, ok := shellWords([]string{shell, "-l"})
	if !ok {
		return nil, false
	}
	script := direnvPrelude(shell) + cmd + " || true; exec " + exec
	return []string{shell, "-i", "-c", script}, true
}

// direnvPrelude runs direnv's hook, when the shell's startup files defined
// it, for the shells direnv has a hook for.
func direnvPrelude(shell string) string {
	switch filepath.Base(shell) {
	case "zsh", "bash":
		return "if typeset -f _direnv_hook >/dev/null 2>&1; then _direnv_hook || true; fi; "
	case "fish":
		return "functions -q __direnv_export_eval; and __direnv_export_eval; "
	}
	return ""
}

// shellWords joins words into one command line that sh, bash, zsh and fish
// all read as those words. A word of only safe characters stays as it is
// (so a command name can still be an alias or function of the shell); any
// other is put in single quotes. A word holding a single quote, a
// backslash or a control character, and an empty word, cannot be written
// for all of them: ok is false.
func shellWords(words []string) (string, bool) {
	if len(words) == 0 {
		return "", false
	}
	out := make([]string, len(words))
	for i, w := range words {
		if w == "" || strings.ContainsAny(w, `'\`) {
			return "", false
		}
		plain := true
		for j := 0; j < len(w); j++ {
			c := w[j]
			if c < 0x20 || c == 0x7f {
				return "", false
			}
			if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_./=:,+@%", c) >= 0) {
				plain = false
			}
		}
		if plain {
			out[i] = w
		} else {
			out[i] = "'" + w + "'"
		}
	}
	return strings.Join(out, " "), true
}

// resumeRefresh is how often the conversation of a running agent is looked
// up again: a conversation can change under the same process (/clear).
var resumeRefresh = 5 * time.Second

// resumeGrace is how long a record handed to a start stays saved without
// the resumed agent being found again; then it is dropped, so a
// conversation that cannot be resumed is not tried at every restart.
var resumeGrace = 30 * time.Second

// startResumed makes the start of a pane (its options po, its effect e)
// resume e.Resume. The pane's shell must be $SHELL (Options.Shell nil): a
// configured shell is started as configured, with its own arguments, and
// not resumed. Whatever is not resumed has its record dropped.
func (c *core) startResumed(po *pane.Options, e model.Effect) {
	r := *e.Resume
	argv, ok := []string(nil), false
	if len(c.s.o.Shell) == 0 {
		argv, ok = resumeArgv(po.Argv[0], &r)
	}
	if !ok {
		c.s.log.Warn("the agent of the pane is not resumed", "pane", e.Pane, "kind", r.Kind)
		if c.model.DropHanded(e.Pane, r) {
			c.unsaved()
		}
		return
	}
	c.s.log.Info("resuming the agent of the pane", "pane", e.Pane, "kind", r.Kind, "conversation", r.ID)
	po.Argv = argv
	c.after(resumeGrace, func(c *core) {
		if c.model.DropHanded(e.Pane, r) {
			c.s.log.Info("the resumed agent was not seen; its conversation is not resumed again", "pane", e.Pane)
			c.unsaved()
		}
	})
}

// resumeWatch is what the server knows of the conversation of one agent
// pane. It belongs to the loop.
type resumeWatch struct {
	kind    agent.Kind
	seen    []int     // the agent processes of the latest poll
	pid     int       // the agent process id is of; 0 while none was found
	id      string    // the conversation recorded with SetResume; "" none
	at      time.Time // when the last lookup started; zero: look up at the next poll
	running bool      // a lookup runs on its goroutine
}

// lookUpConversation keeps the saved conversation of a pane's agent up to
// date, from one poll of its foreground group: when the pane runs an agent
// that can be resumed, its conversation is looked up off the loop at most
// once per resumeRefresh, and at once when the agent process changed. A
// conversation found is recorded with Core.SetResume; an agent process
// that holds none known has the record of an earlier process cleared, and
// so has a pane whose agent became another kind. A lookup whose processes
// are no longer the pane's when it ends counts for nothing; the next poll
// looks again.
func (st *crossState) lookUpConversation(core Core, pane string, group []agent.Proc) {
	kind := st.agents.Kind(pane)
	if w := st.resumes[pane]; w != nil && w.kind != kind {
		st.forgetConversation(core, pane)
	}
	res, ok := resumeFor(kind)
	if !ok {
		return
	}
	var procs []resume.Process
	var pids []int
	for _, p := range agent.Processes(kind, group) {
		if p.Pid > 0 {
			procs = append(procs, resume.Process{Pid: p.Pid, Args: p.Args})
			pids = append(pids, p.Pid)
		}
	}
	if len(pids) == 0 {
		return
	}
	w := st.resumes[pane]
	if w == nil {
		w = &resumeWatch{kind: kind}
		st.resumes[pane] = w
	}
	w.seen = pids
	now := agentClock()
	moved := w.pid != 0 && !slices.Contains(pids, w.pid)
	if w.running || !w.at.IsZero() && now.Sub(w.at) < resumeRefresh && !moved {
		return
	}
	cwd := paneCwd(core, pane)
	for i := range procs {
		procs[i].Cwd = cwd
	}
	w.running, w.at = true, now
	go func() {
		id, pid := "", 0
		for _, p := range procs {
			if got, ok := res.Lookup(p); ok {
				id, pid = got, p.Pid
				break
			}
		}
		core.Post(func(core Core) {
			w.running = false
			if st.resumes[pane] != w {
				return // the pane went, or its agent ended or changed, meanwhile
			}
			if !slices.Equal(w.seen, pids) {
				w.at = time.Time{} // about processes that are not the pane's now
				return
			}
			switch {
			case id != "":
				w.pid = pid
				if id != w.id {
					w.id = id
					core.SetResume(pane, string(kind), id)
				}
			case w.pid != 0 && !slices.Contains(pids, w.pid):
				// Another process now: the conversation of the one before
				// is not this agent's.
				w.pid, w.id = 0, ""
				core.SetResume(pane, "", "")
			}
		})
	}()
}

// paneCwd is the directory of a pane, "" when it is gone.
func paneCwd(core Core, pane string) string {
	for _, p := range core.State().Panes {
		if p.ID == pane {
			return p.Cwd
		}
	}
	return ""
}

// forgetConversation clears the saved conversation of a pane that is no
// longer an agent, or another one: its shell is plain again after a restart.
func (st *crossState) forgetConversation(core Core, pane string) {
	delete(st.resumes, pane)
	core.SetResume(pane, "", "")
}
