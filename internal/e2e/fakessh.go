package e2e

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// blockFailure is how ssh ends for one reason of 3.5.2: its exit status,
// what it writes to stderr, and for the reasons that pass the transport,
// what the far side writes to stdout first.
type blockFailure struct {
	Exit   int
	Stderr string // {alias} is the alias
	Stdout string // {alias} is the alias
	Noise  int    // bytes of login banner written before Stdout
}

// blockFailures are the failures Block reproduces, one per reason of 3.5.2.
//
// provisional E5: the exit statuses and the stderr texts are OpenSSH's as
// documented (https://man.openbsd.org/ssh#EXIT_STATUS) and as 3.5.2 lists
// them; E5, which records them on the three machines, is pending [GO]
// (D1). When E5 has run, these texts are replaced by the recorded ones.
var blockFailures = map[string]blockFailure{
	// ssh exit 255 and "Permission denied" (a locked 1Password agent).
	"auth": {Exit: 255, Stderr: "florian@{alias}: Permission denied (publickey).\r\n"},
	// ssh exit 255 and "Host key verification failed".
	"host-key": {Exit: 255, Stderr: "No ED25519 host key is known for {alias} and you have requested strict checking.\r\nHost key verification failed.\r\n"},
	// ssh exit 255 and "Connection refused" (the host is down).
	"unreachable": {Exit: 255, Stderr: "ssh: connect to host {alias} port 22: Connection refused\r\n"},
	// the remote shell does not find devtools: exit 127.
	"no-devtools": {Exit: 127, Stderr: "zsh:1: command not found: devtools\n"},
	// the bridge cannot reach the server there (4.4).
	"no-server": {Exit: 1, Stdout: "devtools-bridge-error no-server cannot reach the devtools server on {alias}\n"},
	// an old build without the bridge command.
	"incompatible": {Exit: 1, Stderr: "unknown command \"bridge\" for \"devtools\"\n"},
	// more than 64 KiB before the marker (3.5.2).
	"protocol": {Exit: 0, Noise: 65<<10 + 1, Stdout: "devtools-bridge-ready 2\n"},
}

// sshArgOptions are the options of ssh that take an argument
// (https://man.openbsd.org/ssh, SYNOPSIS).
const sshArgOptions = "BbcDEeFIiJLlmOoPpRSWw"

// parseSSH splits the arguments of ssh into the destination and the
// remote command. ok is false when there is no destination.
func parseSSH(args []string) (dest string, command []string, ok bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		// A cluster of flags; the first one that takes an argument takes
		// the rest of the word or the next word.
		for j := 1; j < len(a); j++ {
			if strings.IndexByte(sshArgOptions, a[j]) >= 0 {
				if j == len(a)-1 {
					i++
				}
				break
			}
		}
		i++
	}
	if i >= len(args) {
		return "", nil, false
	}
	return args[i], args[i+1:], true
}

// runFakeSSH is the fake ssh: the test binary started as "ssh" from the bin
// directory of a machine. It appends its argument list to the machine's
// ssh log, then, for an alias that Connect made, either fails like the
// reason Block set or becomes `devtools bridge` of the other machine, with
// that machine's environment: the pid stays the same, so Cut ends the
// bridge. It never contacts a host.
func runFakeSSH(args []string) int {
	root, err := selfRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake ssh: %v\n", err)
		return 255
	}
	if err := logArgs(filepath.Join(root, "f", "ssh.log"), args); err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake ssh: the log: %v\n", err)
		return 255
	}
	dest, command, ok := parseSSH(args)
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: ssh [options] destination [command]")
		return 255
	}
	var link linkFile
	if validAlias(dest) {
		link, err = readLink(root, dest)
	}
	if !validAlias(dest) || err != nil {
		fmt.Fprintf(os.Stderr, "ssh: Could not resolve hostname %s: Name or service not known\r\n", dest)
		return 255
	}
	// The pid is written first, so that Cut also ends an attempt that is
	// still delayed.
	if err := appendLine(pidsPath(root, dest), []byte(strconv.Itoa(os.Getpid()))); err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake ssh: %v\n", err)
		return 255
	}
	if link.DelayMs > 0 {
		time.Sleep(time.Duration(link.DelayMs) * time.Millisecond)
	}
	if f, ok := blockFailures[link.Block]; ok {
		return fail(f, dest)
	}
	if strings.Join(command, " ") != "devtools bridge" {
		fmt.Fprintf(os.Stderr, "e2e fake ssh: only \"devtools bridge\" is run, not %q\n", strings.Join(command, " "))
		return 255
	}
	if err := appendLine(bridgesPath(root, dest), []byte(strconv.Itoa(os.Getpid()))); err != nil {
		fmt.Fprintf(os.Stderr, "e2e fake ssh: %v\n", err)
		return 255
	}
	bin := filepath.Join(link.Root, "b", devtools)
	err = syscall.Exec(bin, []string{bin, "bridge"}, machineEnv(link.Root))
	fmt.Fprintf(os.Stderr, "e2e fake ssh: run the bridge: %v\n", err)
	return 255
}

// fail ends the fake ssh like f.
func fail(f blockFailure, alias string) int {
	if f.Noise > 0 {
		line := strings.Repeat("welcome to the e2e host ", 3) + "\n"
		for n := 0; n < f.Noise; n += len(line) {
			os.Stdout.WriteString(line)
		}
	}
	if f.Stdout != "" {
		os.Stdout.WriteString(strings.ReplaceAll(f.Stdout, "{alias}", alias))
	}
	if f.Stderr != "" {
		os.Stderr.WriteString(strings.ReplaceAll(f.Stderr, "{alias}", alias))
	}
	if f.Noise > 0 {
		// The far side keeps the connection open until the client gives
		// up, as a bridge would.
		io.Copy(io.Discard, os.Stdin)
	}
	return f.Exit
}
