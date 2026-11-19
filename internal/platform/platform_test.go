package platform

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"devtools/internal/testkit"
)

// TestMain turns the test binary into a helper program when asked to, and
// keeps the tests hermetic: nothing of the developer's environment that
// names a home, a socket, a state directory, a service or a multiplexer
// reaches them.
func TestMain(m *testing.M) {
	testkit.RegisterHelper("block", helperBlock)
	testkit.RegisterHelper("lockhold", helperLockhold)
	testkit.RegisterHelper("fgchild", helperFgchild)
	testkit.RunHelper()

	home, err := os.MkdirTemp("", "devtools-platform-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_CONFIG",
		"DEVTOOLS_MULTIPLEXER", "DEVTOOLS_SERVICE", "XDG_STATE_HOME", "XDG_CONFIG_HOME",
		"TMUX", "TMUX_PANE", "HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_BIN_PATH",
		"STY", "ZELLIJ", "ZELLIJ_SESSION_NAME",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// helperBlock reads its standard input until it ends, then exits 0.
func helperBlock(args []string) int {
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// helperLockhold takes the lock at args[0], prints "locked" and holds it
// until its standard input ends.
func helperLockhold(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "lockhold <path>")
		return 2
	}
	f, err := Lock(args[0])
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	defer f.Close()
	fmt.Println("locked")
	io.Copy(io.Discard, os.Stdin)
	return 0
}

// helperFgchild runs on a PTY as a shell would: it prints "ready", waits for
// one line, then starts the helper "block" in a process group of its own,
// puts that group in the foreground of the terminal and prints
// "fg <pid>". It ends when the child ends.
func helperFgchild(args []string) int {
	fmt.Print("ready\r\n")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 1
	}
	argv, env := testkit.HelperCommand("block")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(withoutHelperVar(os.Environ()), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		fmt.Print("error ", err, "\r\n")
		return 1
	}
	fmt.Printf("fg %d\r\n", cmd.Process.Pid)
	cmd.Wait()
	return 0
}

func withoutHelperVar(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, testkit.HelperVar+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// startHelper starts the helper name with args as a child of the test, with
// a clean environment and a pipe as its standard input; the child is killed
// when the test ends. It returns the command and a reader of its standard
// output.
func startHelper(t *testing.T, name string, attr *syscall.SysProcAttr, dir string, args ...string) (*exec.Cmd, *bufio.Reader, io.WriteCloser) {
	t.Helper()
	argv, env := testkit.HelperCommand(name, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = testkit.CleanEnv(testkit.ShortTempDir(t), env...)
	cmd.SysProcAttr = attr
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper %s: %v", name, err)
	}
	waited := make(chan struct{})
	go func() { cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		<-waited
	})
	return cmd, bufio.NewReader(stdout), stdin
}
