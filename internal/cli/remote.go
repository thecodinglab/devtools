package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"devtools/internal/herdr"
	"devtools/internal/hub"
	"devtools/internal/mux"
	"devtools/internal/peer"
	"devtools/internal/tmux"
	"devtools/internal/ui"

	"github.com/spf13/cobra"
)

// discoverPeers finds devtools on the other machines; tests replace it.
var discoverPeers = peer.Discover

// sshCommand is the ssh binary switching to another machine runs.
var sshCommand = "ssh"

// remoteHost is what switching to an item on another machine needs to
// know about it.
type remoteHost struct {
	name         string
	hostname     string
	ssh          string
	multiplexer  string
	herdrRunning bool
}

// remotes are the other machines that answered, by the name the switcher
// shows them under.
type remotes struct {
	mu    sync.Mutex
	hosts map[string]remoteHost
}

// add records a machine and returns the name it goes by, made unique when
// two machines call themselves the same. A machine without a name to show
// is not added: an item without a machine is one on this machine.
func (r *remotes) add(snapshot peer.Snapshot) (remoteHost, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := strings.TrimSpace(sanitize(snapshot.Name))
	if name == "" {
		name = strings.TrimSpace(sanitize(snapshot.Hostname))
	}
	if name == "" {
		return remoteHost{}, false
	}
	if taken, ok := r.hosts[name]; ok && taken.hostname != snapshot.Hostname {
		name += " (" + sanitize(snapshot.Hostname) + ")"
	}
	host := remoteHost{
		name:         name,
		hostname:     snapshot.Hostname,
		ssh:          snapshot.SSH,
		multiplexer:  snapshot.Multiplexer,
		herdrRunning: snapshot.HerdrRunning,
	}
	r.hosts[name] = host
	return host, true
}

func (r *remotes) get(name string) (remoteHost, bool) {
	if r == nil {
		return remoteHost{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	host, ok := r.hosts[name]
	return host, ok
}

// discoveryOptions are where to look for other machines, ok false when
// the config turns looking off.
func discoveryOptions() (peer.Options, bool, error) {
	cfg, err := loadConfig()
	if err != nil || cfg.NoDiscovery {
		return peer.Options{}, false, err
	}
	self, _ := os.Hostname()
	return peer.Options{Port: cfg.Port, Peers: cfg.Peers, Self: self}, true, nil
}

// discover asks the other machines for their items, sending each machine's
// as one batch once it answers. A machine that fails to answer is left out:
// devtools hosts says why.
func discover(ctx context.Context) (<-chan []hub.Item, *remotes) {
	opts, ok, err := discoveryOptions()
	if err != nil || !ok {
		return nil, nil
	}
	results := discoverPeers(ctx, opts)
	known := &remotes{hosts: map[string]remoteHost{}}
	out := make(chan []hub.Item)
	go func() {
		defer close(out)
		for result := range results {
			if result.Err != nil || result.Snapshot.SSH == "" {
				continue
			}
			if peer.ValidSSH(result.Snapshot.SSH) != nil {
				continue
			}
			host, ok := known.add(result.Snapshot)
			if !ok {
				continue
			}
			items := remoteItems(host.name, result.Snapshot.Items)
			if len(items) == 0 {
				continue
			}
			select {
			case out <- items:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, known
}

var itemKinds = map[string]hub.Kind{
	"worktree":  hub.KindWorktree,
	"repo":      hub.KindRepo,
	"bookmark":  hub.KindBookmark,
	"workspace": hub.KindWorkspace,
}

func kindName(kind hub.Kind) string {
	for name, k := range itemKinds {
		if k == kind {
			return name
		}
	}
	return ""
}

// remoteItems turns a machine's snapshot into switcher items. What another
// machine sends is drawn in this terminal, so control characters are
// dropped from everything shown.
func remoteItems(host string, items []peer.Item) []hub.Item {
	out := make([]hub.Item, 0, len(items))
	for _, item := range items {
		kind, ok := itemKinds[item.Kind]
		if !ok {
			continue
		}
		git := item.Git
		git.Branch = sanitize(git.Branch)
		state := item.State
		state.Agent = sanitize(state.Agent)
		state.Title = sanitize(state.Title)
		// a switcher here never focuses a workspace over there
		state.Focused = false
		out = append(out, hub.Item{
			Kind:        kind,
			Project:     sanitize(item.Project),
			Worktree:    sanitize(item.Worktree),
			Label:       sanitize(item.Label),
			Path:        item.Path,
			Root:        item.Root,
			Session:     item.Session,
			WorkspaceID: item.WorkspaceID,
			Host:        host,
			Git:         git,
			State:       state,
		})
	}
	return out
}

// maxShown is the most of a string from another machine that is shown;
// the rest could only slow the switcher down.
const maxShown = 200

// sanitize drops control characters, which could drive the terminal, and
// cuts s to maxShown runes.
func sanitize(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		if n == maxShown {
			b.WriteRune('…')
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// snapshot reads this machine the way its switcher shows it: every
// worktree, repo and bookmark with its git state and session, and the open
// workspaces that belong to none of them. It never starts a herdr server.
func (a *app) snapshot(ctx context.Context) (peer.Snapshot, error) {
	g, err := a.globals()
	if err != nil {
		return peer.Snapshot{}, err
	}
	targets, err := scanTargets(g)
	if err != nil {
		return peer.Snapshot{}, err
	}
	items := hubItems(g, withBookmarks(targets, g.bookmarks))
	backend, err := a.multiplexer()
	if err != nil {
		return peer.Snapshot{}, err
	}
	b := hubBackend{app: a, backend: backend}

	snapshot := peer.Snapshot{Multiplexer: string(backend)}
	var sessions map[string]ui.Session
	var others []hub.Item
	sessionsDone := make(chan struct{})
	go func() {
		defer close(sessionsDone)
		// a multiplexer that is not running has no sessions to show
		sessions, others, _ = b.Sessions()
		if backend == mux.Herdr {
			snapshot.HerdrRunning = herdr.Inside()
			if !snapshot.HerdrRunning {
				snapshot.HerdrRunning, _ = herdr.ServerRunning()
			}
		}
	}()

	gits := make([]ui.Git, len(items))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for i, item := range items {
		if item.Kind == hub.KindBookmark {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			if git, err := b.Status(item.Path); err == nil {
				gits[i] = git
			}
		}()
	}
	wg.Wait()
	<-sessionsDone
	if err := ctx.Err(); err != nil {
		return peer.Snapshot{}, err
	}

	known := make(map[string]bool, len(items))
	for i, item := range items {
		known[item.Session] = true
		snapshot.Items = append(snapshot.Items, peerItem(item, gits[i], sessions[item.Session]))
	}
	for _, other := range others {
		if !known[other.Session] {
			snapshot.Items = append(snapshot.Items, peerItem(other, ui.Git{}, other.State))
		}
	}
	return snapshot, nil
}

func peerItem(item hub.Item, git ui.Git, state ui.Session) peer.Item {
	return peer.Item{
		Kind:        kindName(item.Kind),
		Project:     item.Project,
		Worktree:    item.Worktree,
		Label:       item.Label,
		Path:        item.Path,
		Root:        item.Root,
		Session:     item.Session,
		WorkspaceID: item.WorkspaceID,
		Git:         git,
		State:       state,
	}
}

func serveCommand(a *app, stderr io.Writer) *cobra.Command {
	var name, ssh, listen string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Answer devtools on the other machines with this machine's projects",
		Args:  usageNoArgs("usage: devtools serve [--name <name>] [--ssh <destination>] [--listen <addr>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			hostname, err := os.Hostname()
			if err != nil {
				return err
			}
			if name == "" {
				name = shortHostname(hostname)
			}
			if ssh == "" {
				ssh = name
			}
			if err := peer.ValidSSH(ssh); err != nil {
				return fmt.Errorf("--ssh: %w", err)
			}
			if listen == "" {
				listen = peer.Addr("", cfg.Port)
			}
			udp, tcp, err := peer.Listen(listen)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			server := &peer.Server{
				Announce: peer.Announce{Name: name, Hostname: hostname, SSH: ssh},
				Snapshot: a.snapshot,
				Fresh:    2 * time.Second,
				Logf: func(format string, args ...any) {
					fmt.Fprintf(stderr, format+"\n", args...)
				},
			}
			fmt.Fprintf(stderr, "devtools serve: %s on %s, reached over ssh as %s\n", name, tcp.Addr(), ssh)
			return server.Serve(ctx, udp, tcp)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "what other machines call this one (default: the short hostname)")
	cmd.Flags().StringVar(&ssh, "ssh", "", "the ssh destination other machines reach this one by (default: the name)")
	cmd.Flags().StringVar(&listen, "listen", "", "the address to listen on (default: every address, at the config port)")
	return cmd
}

// shortHostname is the hostname up to its first dot, lower case, the way
// ssh configs tend to name machines.
func shortHostname(hostname string) string {
	short, _, _ := strings.Cut(hostname, ".")
	return strings.ToLower(short)
}

func hostsCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "hosts",
		Short: "List the machines devtools finds on the network",
		Args:  usageNoArgs("usage: devtools hosts"),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, ok, err := discoveryOptions()
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("discovery is off in the config")
			}
			self := opts.Self
			// this machine's own devtools serve is listed too, to show it
			// answers
			opts.Self = ""
			opts.Listen = 1500 * time.Millisecond
			var rows [][]string
			for result := range discoverPeers(cmd.Context(), opts) {
				name := sanitize(result.Announce.Name)
				if name == "" {
					name = "?"
				}
				if result.Announce.Hostname == self {
					name += " (this machine)"
				}
				state := fmt.Sprintf("%d items in %s", len(result.Snapshot.Items), result.Elapsed.Round(time.Millisecond))
				if result.Err != nil {
					state = sanitize(result.Err.Error())
				}
				rows = append(rows, []string{name, result.Addr, "ssh " + sanitize(result.Announce.SSH), state})
			}
			if len(rows) == 0 {
				fmt.Fprintln(stdout, "No machine answered. Run devtools serve on each machine, and let UDP and TCP port", portOrDefault(opts.Port), "through their firewalls.")
				return nil
			}
			for _, line := range alignColumns(rows) {
				fmt.Fprintln(stdout, line)
			}
			return nil
		},
	}
}

func portOrDefault(port int) int {
	if port == 0 {
		return peer.DefaultPort
	}
	return port
}

// sessionOpenCommand is what another machine runs over ssh to switch to a
// session on this one.
func sessionOpenCommand(a *app) *cobra.Command {
	var session, workspace string
	cmd := &cobra.Command{
		Use:    "session-open --session <name> [--workspace <id>] [dir]",
		Short:  "Open or focus a session on this machine for devtools on another one",
		Hidden: true,
		Args:   usageMaximumArgs(1, "usage: devtools session-open --session <name> [--workspace <id>] [dir]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if session == "" {
				return errors.New("session-open needs --session")
			}
			if len(args) == 1 {
				return a.switchSession(args[0], session)
			}
			backend, err := a.multiplexer()
			if err != nil {
				return err
			}
			if backend == mux.Herdr && workspace != "" {
				return herdr.Visit(func() error { return herdr.Client{}.FocusWorkspace(workspace) })
			}
			return tmux.SwitchSession(session)
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "the session name")
	cmd.Flags().StringVar(&workspace, "workspace", "", "the herdr workspace id, for a workspace without a directory")
	return cmd
}

// remoteOpen is the command that opens an item's session on its machine.
func remoteOpen(item hub.Item) string {
	args := []string{"devtools", "session-open", "--session", item.Session}
	if item.Kind == hub.KindWorkspace {
		if item.WorkspaceID != "" {
			args = append(args, "--workspace", item.WorkspaceID)
		}
	} else {
		args = append(args, "--", item.Path)
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// switchRemote lands on an item on another machine. With herdr, a machine
// saved in herdr with its server running is driven through herdr, which
// shows it in its sidebar. Anything else gets a session here that runs ssh
// to the machine and opens the item's session over there, or, from a plain
// terminal, ssh in the terminal itself. Only this asks for an ssh login.
func (b hubBackend) switchRemote(item hub.Item) (string, error) {
	host, ok := b.remotes.get(item.Host)
	if !ok {
		return "", fmt.Errorf("no machine %s answered", item.Host)
	}
	command := remoteOpen(item)
	switch {
	case b.backend == mux.Herdr && herdr.Inside():
		if host.multiplexer == string(mux.Herdr) && host.herdrRunning {
			if machine, ok := savedMachine(host); ok {
				return focusOnMachine(machine, item)
			}
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		// a connection that ends closes its workspace, so picking the item
		// again connects anew; one that fails stays open to show why
		return "", herdr.Client{}.SwitchRunning(home, item.Host+":"+item.Label, sshLine(host, command)+" && exit")
	case b.backend == mux.Tmux && os.Getenv("TMUX") != "":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return "", tmux.SwitchRunning(home, tmux.SessionName(item.Host, item.Session), sshLine(host, command))
	}
	return "", runSSH(host, command)
}

// savedMachine finds the machine among herdr's saved ssh machines by its
// name or its ssh destination.
func savedMachine(host remoteHost) (herdr.Machine, bool) {
	machines, err := herdr.ListMachines()
	if err != nil {
		return herdr.Machine{}, false
	}
	for _, machine := range machines {
		if machine.Enabled && (machine.Label == host.name || machine.Target == host.ssh) {
			return machine, true
		}
	}
	return herdr.Machine{}, false
}

func focusOnMachine(machine herdr.Machine, item hub.Item) (string, error) {
	client := herdr.Client{Machine: machine.ID}
	workspace := item.WorkspaceID
	var err error
	if item.Kind == hub.KindWorkspace && workspace != "" {
		err = client.FocusWorkspace(workspace)
	} else {
		workspace, err = client.SwitchWorkspace(item.Path, item.Session)
	}
	if err != nil || machine.Selected {
		return "", err
	}
	// a patched herdr client switches its window over; without the patch
	// only the user can
	if shown, err := herdr.ShowWorkspace(machine.ID, workspace); err == nil && shown {
		return "", nil
	}
	return "Opened " + item.Label + " on " + item.Host + ". Select " + machine.Label + " in the herdr sidebar to see it.", nil
}

// showLocal brings the herdr window back to Local when it shows another
// machine, so the local workspace devtools focused is in sight.
func showLocal(workspace string) {
	if herdr.Inside() && workspace != "" && herdr.ShowsMachine() {
		herdr.ShowWorkspace("", workspace)
	}
}

// sshLine is the shell command that runs command on the machine in a
// terminal of its own.
func sshLine(host remoteHost, command string) string {
	return shellQuote(sshCommand) + " -t " + shellQuote(host.ssh) + " " + shellQuote(command)
}

func runSSH(host remoteHost, command string) error {
	cmd := exec.Command(sshCommand, "-t", host.ssh, command)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s: %w", host.ssh, err)
	}
	return nil
}

// remoteQueryFallback reports whether a switch query nothing here matches
// is worth looking for on the other machines.
func remoteQueryFallback() bool {
	_, ok, err := discoveryOptions()
	return err == nil && ok
}
