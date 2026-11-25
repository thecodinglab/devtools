package cli

// devtools server, server status [--json] and server restart [--yes] (4.5,
// 3.9 of the plan, P2-08b). They address the devtools server whatever
// multiplexer the caller sits in: the feature row cmd.server is not gated,
// and none of them selects a backend or constructs a Multiplexer (5.3.8).

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/server/boot"
	"devtools/internal/ui"

	"github.com/spf13/cobra"
)

// runServer is the body of devtools server; restartService restarts the
// service of the server (systemctl or launchctl). Tests replace both: no
// test starts a server or touches a service manager.
var (
	runServer      = boot.Run
	restartService = platform.Restart
)

// statusLimit bounds the questions of server status and server restart.
const statusLimit = 2 * time.Second

func serverCommand(a *app, stdout io.Writer) *cobra.Command {
	cmd := feature(featServer, &cobra.Command{
		Use:   "server",
		Short: "Run the devtools server (the service manager runs it)",
		Args:  usageNoArgs("usage: devtools server"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if code := runServer(version); code != 0 {
				return &exit{code: code}
			}
			return nil
		},
	})
	cmd.AddCommand(serverStatusCommand(stdout), serverRestartCommand(stdout))
	return cmd
}

// serverStatus is what server status reports: the welcome and the counts
// of the snapshot (4.5).
type serverStatus struct {
	Pid      int    `json:"pid"`
	Version  string `json:"version"`
	Gen      int    `json:"gen"`
	Boot     string `json:"boot"`
	Sessions int    `json:"sessions"`
	Panes    int    `json:"panes"`
	Clients  int    `json:"clients"`
	Socket   string `json:"socket"`
	// What this devtools is and what the server says is installed. Only
	// the server tells whether a restart starts another build: it compares
	// the build the service starts with its own image (internal/server/
	// update.go). This devtools may be another build that is not
	// installed (one of a worktree, say), and a server from before the
	// watch says nothing, so a version of this devtools other than the
	// server's is reported as such and is no pending restart.
	CLIVersion string `json:"cli_version"`
	Installed  string `json:"installed"`       // welcome.installed: another build installed, as the server saw it; "" none
	Restart    bool   `json:"restart_pending"` // the server says another build is installed
	Other      bool   `json:"cli_differs"`     // this devtools is another version than the server's
	running    int    // panes whose program runs
}

// drift fills the fields that compare the server's build with this one.
func (st *serverStatus) drift() {
	st.CLIVersion = version
	st.Restart = st.Installed != ""
	st.Other = st.Version != version
}

func serverStatusCommand(stdout io.Writer) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Say whether the devtools server runs; exit 0 when it does, 1 when not",
		Args:  usageNoArgs("usage: devtools server status [--json]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := askServer()
			if err != nil {
				return words(ui.Message("noserver"))
			}
			if asJSON {
				return json.NewEncoder(stdout).Encode(st)
			}
			fmt.Fprintf(stdout, "running, pid %d, version %s, protocol %d\n", st.Pid, st.Version, st.Gen)
			fmt.Fprintf(stdout, "%d sessions, %d panes, %d windows\n", st.Sessions, st.Panes, st.Clients)
			fmt.Fprintf(stdout, "socket %s\n", st.Socket)
			switch {
			case st.Restart:
				fmt.Fprintln(stdout, ui.Message("drift", st.Version, st.Installed))
			case st.Other:
				fmt.Fprintln(stdout, ui.Message("otherbuild", st.Version, st.CLIVersion))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	return cmd
}

func serverRestartCommand(stdout io.Writer) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the devtools server; asks first when programs are running",
		Args:  usageNoArgs("usage: devtools server restart [--yes]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				if st, err := askServer(); err == nil && st.running > 0 {
					fmt.Fprintln(stdout, ui.Message("restart", strconv.Itoa(st.running), strconv.Itoa(st.Sessions)))
					answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
					switch strings.ToLower(strings.TrimSpace(answer)) {
					case "y", "yes":
					default:
						return &exit{code: 1}
					}
				}
			}
			return restartService()
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask")
	return cmd
}

// askServer asks the server at the socket of this environment for its
// welcome and its state, as a one-shot without a pane.
func askServer() (serverStatus, error) {
	socket, err := proto.SocketPath()
	if err != nil {
		return serverStatus{}, err
	}
	conn, err := net.DialTimeout("unix", socket, statusLimit)
	if err != nil {
		return serverStatus{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(statusLimit))
	hello := proto.Hello{Gen: proto.Gen, Version: version, Caps: []string{proto.CapStateGet}, Kind: proto.KindOneshot}
	if err := proto.WriteMsg(conn, proto.Encode(hello)); err != nil {
		return serverStatus{}, err
	}
	var w proto.Welcome
	var snap proto.Snapshot
	sent := false
	for {
		msg, err := proto.ReadMsg(conn)
		if err != nil {
			return serverStatus{}, err
		}
		switch {
		case msg.T == proto.TypeRefuse:
			var r proto.Refuse
			msg.Decode(&r)
			return serverStatus{}, errors.New(r.Text)
		case msg.T == proto.TypePing:
			proto.WriteMsg(conn, proto.Encode(proto.Pong{}))
		case msg.T == proto.TypeWelcome && !sent:
			if err := msg.Decode(&w); err != nil {
				return serverStatus{}, err
			}
			if err := proto.WriteMsg(conn, proto.Request(1, proto.StateGet{})); err != nil {
				return serverStatus{}, err
			}
			sent = true
		case msg.ID == 1 && msg.T == proto.TypeStateSnapshot:
			if err := msg.Decode(&snap); err != nil {
				return serverStatus{}, err
			}
			st := serverStatus{
				Pid: w.Pid, Version: w.Version, Gen: w.Gen, Boot: w.Boot,
				Sessions: len(snap.Sessions), Panes: len(snap.Panes), Clients: w.Clients, Socket: socket,
				Installed: w.Installed,
			}
			st.drift()
			for _, p := range snap.Panes {
				if p.State == proto.PaneRunning {
					st.running++
				}
			}
			return st, nil
		case msg.ID == 1 && msg.T == proto.TypeErr:
			r, _ := proto.AsReply(msg)
			return serverStatus{}, errors.New(r.Text)
		}
	}
}

// bridgeCommand is devtools bridge (4.4), run by ssh on this machine for
// the client of another one: hidden, and never typed by hand.
func bridgeCommand() *cobra.Command {
	return feature(featServer, &cobra.Command{
		Use:    "bridge",
		Short:  "Connect standard input and output to the devtools server (run over ssh)",
		Hidden: true,
		Args:   usageNoArgs("usage: devtools bridge"),
		RunE: func(cmd *cobra.Command, args []string) error {
			socket, err := proto.SocketPath()
			if err != nil {
				return err
			}
			code := runBridge(terminalIn, terminalOut, socket, isTerminalFile(terminalIn))
			if code != 0 {
				return &exit{code: code}
			}
			return nil
		},
	})
}

// isTerminalFile reports whether f is a terminal.
func isTerminalFile(f *os.File) bool {
	_, _, err := platform.Winsize(f)
	return err == nil
}
