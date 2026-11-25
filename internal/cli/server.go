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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/server"
	"devtools/internal/server/boot"
	"devtools/internal/ui"

	"github.com/spf13/cobra"
)

// runServer is the body of devtools server; restartService restarts the
// service of the server (systemctl or launchctl); startService starts it
// when it is stopped and ends nothing; servicePid asks the service manager
// for the pid of the service. Tests replace them: no test starts a server
// or touches a service manager.
var (
	runServer      = boot.Run
	restartService = platform.Restart
	startService   = platform.Start
	servicePid     = platform.ServicePid
)

// upgradeWait bounds how long server restart waits for the outcome of a
// live upgrade, and upgradePoll how often it asks.
var (
	upgradeWait = 15 * time.Second
	upgradePoll = 100 * time.Millisecond
)

// statusLimit bounds the questions of server status and server restart (a
// variable for the tests).
var statusLimit = 2 * time.Second

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
	cmd.AddCommand(serverStatusCommand(stdout), serverRestartCommand(stdout), upgradeCheckCommand(stdout))
	return cmd
}

// upgradeCheckCommand is devtools server upgrade-check, hidden: what this
// build can take over in a live upgrade (internal/handover), asked by the
// running server of an older build before it hands its programs to this
// one; with --sample, what this build reads from a sample screen of that
// server. It reads no state and no config, reaches no server and starts
// nothing.
func upgradeCheckCommand(stdout io.Writer) *cobra.Command {
	var asJSON bool
	var sample, format string
	cmd := &cobra.Command{
		Use:    "upgrade-check",
		Short:  "Say what this build can take over in a live upgrade of the server",
		Hidden: true,
		Args:   usageNoArgs("usage: devtools server upgrade-check --json [--sample <file> --sample-format <format>]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sample == "" {
				return json.NewEncoder(stdout).Encode(server.LocalCheck(version))
			}
			b, err := os.ReadFile(sample)
			if err != nil {
				return err
			}
			got, err := server.ReadSample(format, b)
			if err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(got)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object")
	cmd.Flags().StringVar(&sample, "sample", "", "a sample screen to read")
	cmd.Flags().StringVar(&format, "sample-format", "", "its format")
	return cmd
}

// serverStatus is what server status reports: the welcome and the counts
// of the snapshot (4.5).
type serverStatus struct {
	Pid      int             `json:"pid"`
	Version  string          `json:"version"`
	Gen      int             `json:"gen"`
	Boot     string          `json:"boot"`
	Sessions int             `json:"sessions"`
	Panes    int             `json:"panes"`
	Clients  int             `json:"clients"`
	Socket   string          `json:"socket"`
	Upgraded *proto.Upgraded `json:"upgraded,omitempty"` // welcome.upgraded: the last live upgrade of the server's process
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
	caps       []string
}

// drift fills the fields that compare the server's build with this one.
func (st *serverStatus) drift() {
	st.CLIVersion = version
	st.Restart = st.Installed != ""
	st.Other = st.Version != version
}

func (st serverStatus) has(c string) bool { return slices.Contains(st.caps, c) }

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
			if u := st.Upgraded; u != nil {
				ago := time.Since(time.UnixMilli(u.At)).Round(time.Second)
				switch {
				case u.Cold:
					fmt.Fprintf(stdout, "the last upgrade failed and the server started cold %s ago: %s\n", ago, u.Error)
				case u.Error != "":
					fmt.Fprintf(stdout, "the last upgrade, to %s, failed %s ago: %s\n", u.To, ago, u.Error)
				default:
					fmt.Fprintf(stdout, "upgraded from %s %s ago, %d programs kept\n", u.From, ago, u.Kept)
				}
			}
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
	var yes, cold, force bool
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Upgrade the devtools server to the installed build, keeping every program; --cold restarts it",
		Args:  usageNoArgs("usage: devtools server restart [--yes] [--cold]"),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := bufio.NewReader(cmd.InOrStdin())
			st, err := askServer()
			if err != nil {
				return noAnswer(stdout, err)
			}
			if cold || !st.has(proto.CapUpgrade) {
				return coldRestart(stdout, in, st, yes)
			}
			return liveRestart(stdout, in, st, yes, force)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask")
	cmd.Flags().BoolVar(&cold, "cold", false, "restart the server and every program instead of upgrading it live")
	cmd.Flags().BoolVar(&force, "force", false, "upgrade live to the running build too (tests)")
	cmd.Flags().MarkHidden("force")
	return cmd
}

// coldRestart is the restart that ends every program: asked first when
// programs run, unless yes, and only of the server the service runs.
func coldRestart(stdout io.Writer, in *bufio.Reader, st serverStatus, yes bool) error {
	if !yes && st.running > 0 {
		fmt.Fprintln(stdout, ui.Message("restart", strconv.Itoa(st.running), strconv.Itoa(st.Sessions)))
		answer, _ := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
		default:
			return &exit{code: 1}
		}
		// The server may have changed while the question was read; one
		// that does not answer now is not restarted blind.
		now, err := askServer()
		if err != nil {
			return noAnswer(stdout, err)
		}
		st = now
	}
	if err := theService(st.Pid); err != nil {
		return err
	}
	return restartService()
}

// noAnswer is server restart when no server answers at the socket. Only a
// socket that does not exist or refuses the connection is a server that
// does not run, and then the service is started, which ends nothing, and
// only when the service manager runs no server either (a socket that is
// not the service's, a scratch one, leaves a running service alone). A
// question that timed out or ended early is a server that is busy (a live
// upgrade pauses its accepting for a moment) or hung: nothing is restarted.
func noAnswer(stdout io.Writer, err error) error {
	if !notListening(err) {
		fmt.Fprintln(stdout, ui.Upgrade("no-answer", err.Error()))
		return &exit{code: 1}
	}
	sp, perr := servicePid()
	switch {
	case perr != nil:
		fmt.Fprintln(stdout, ui.Upgrade("no-service", perr.Error()))
		return &exit{code: 1}
	case sp != 0:
		fmt.Fprintln(stdout, ui.Upgrade("other-server", strconv.Itoa(sp)))
		return &exit{code: 1}
	}
	return startService()
}

// notListening reports whether err says that nothing listens at the
// socket: it does not exist, or it refuses the connection.
func notListening(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// theService checks that the server of pid is the one the service manager
// runs: a restart of the service would end another one, and a server
// started by hand (or by a test) would go on.
func theService(pid int) error {
	sp, err := servicePid()
	if err != nil || sp != pid {
		return words(ui.Upgrade("not-the-service", strconv.Itoa(pid)))
	}
	return nil
}

// liveRestart asks the server to upgrade itself live and reports the
// outcome of the attempt. Only an explicit refusal before anything was
// frozen leads to the cold restart; an ambiguous end of the connection
// never does.
func liveRestart(stdout io.Writer, in *bufio.Reader, st serverStatus, yes, force bool) error {
	res, code, text, err := requestUpgrade(force)
	switch {
	case err != nil:
		fmt.Fprintln(stdout, ui.Upgrade("unknown"))
		return &exit{code: 1}
	case code == proto.CodeCurrent:
		fmt.Fprintln(stdout, ui.Upgrade("current", strconv.Itoa(st.running)))
		return nil
	case code == proto.CodeIncompatible:
		fmt.Fprintln(stdout, ui.Upgrade("incompatible", text))
		return coldRestart(stdout, in, st, yes)
	case code == proto.CodeBusy:
		fmt.Fprintln(stdout, ui.Upgrade("busy", text))
		return &exit{code: 1}
	case code != "":
		fmt.Fprintln(stdout, ui.Upgrade("failed", code+": "+text))
		return &exit{code: 1}
	}
	fmt.Fprintln(stdout, ui.Upgrade("upgrading", res.From, res.To, strconv.Itoa(res.Panes)))
	deadline := time.Now().Add(upgradeWait)
	for time.Now().Before(deadline) {
		time.Sleep(upgradePoll)
		now, err := askServer()
		if err != nil || now.Upgraded == nil || now.Upgraded.ID != res.ID {
			continue
		}
		u := now.Upgraded
		switch {
		case u.Cold:
			fmt.Fprintln(stdout, ui.Upgrade("cold", u.Error))
			return &exit{code: 1}
		case u.Back:
			fmt.Fprintln(stdout, ui.Upgrade("fallback", u.Error))
			return &exit{code: 1}
		case u.Error != "":
			fmt.Fprintln(stdout, ui.Upgrade("failed", u.Error))
			return &exit{code: 1}
		}
		fmt.Fprintln(stdout, ui.Upgrade("upgraded", now.Version, strconv.Itoa(u.Kept)))
		return nil
	}
	if _, err := askServer(); err != nil {
		fmt.Fprintln(stdout, ui.Upgrade("unknown"))
	} else {
		fmt.Fprintln(stdout, ui.Upgrade("pending"))
	}
	return &exit{code: 1}
}

// requestUpgrade sends server.upgrade on a one-shot connection and, on
// ok, acknowledges it: the server waits for that before it replaces its
// image, which ends the connection. code is the error code of a refusal;
// err an answer that never came.
func requestUpgrade(force bool) (res proto.ServerUpgradeResult, code, text string, err error) {
	socket, err := proto.SocketPath()
	if err != nil {
		return res, "", "", err
	}
	conn, err := net.DialTimeout("unix", socket, statusLimit)
	if err != nil {
		return res, "", "", err
	}
	defer conn.Close()
	// The preflight runs the installed build twice and the panes freeze:
	// a few seconds.
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	hello := proto.Hello{Gen: proto.Gen, Version: version, Caps: []string{proto.CapUpgrade}, Kind: proto.KindOneshot}
	if err := proto.WriteMsg(conn, proto.Encode(hello)); err != nil {
		return res, "", "", err
	}
	sent := false
	for {
		msg, err := proto.ReadMsg(conn)
		if err != nil {
			return res, "", "", err
		}
		switch {
		case msg.T == proto.TypeRefuse:
			var r proto.Refuse
			msg.Decode(&r)
			return res, "", "", errors.New(r.Text)
		case msg.T == proto.TypePing:
			proto.WriteMsg(conn, proto.Encode(proto.Pong{}))
		case msg.T == proto.TypeWelcome && !sent:
			if err := proto.WriteMsg(conn, proto.Request(1, proto.ServerUpgrade{Force: force})); err != nil {
				return res, "", "", err
			}
			sent = true
		case msg.ID == 1 && msg.T == proto.TypeOK:
			if err := msg.Decode(&res); err != nil {
				return res, "", "", err
			}
			proto.WriteMsg(conn, proto.Encode(proto.ServerUpgradeAck{ID: res.ID}))
			return res, "", "", nil
		case msg.ID == 1 && msg.T == proto.TypeErr:
			r, _ := proto.AsReply(msg)
			return res, r.Code, r.Text, nil
		}
	}
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
				Installed: w.Installed, Upgraded: w.Upgraded, caps: w.Caps,
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
