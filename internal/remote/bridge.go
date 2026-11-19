package remote

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"devtools/internal/session"
)

// Injection points of the tests: where the refusal of a terminal goes, and
// the clock of the 60 s silence.
var (
	bridgeStderr io.Writer = os.Stderr
	bridgeClock  clock     = realClock{}
)

// Bridge is the body of "devtools bridge" (3.5.2, 4.4) and returns its exit
// status. It connects to socket, the state socket of this machine and
// nothing else: no byte it reads ever names where it connects (SEC2). Then
// it writes the ready line and copies bytes both ways untouched.
//
//   - stdin is a terminal: a line on stderr, 2;
//   - socket cannot be reached: "devtools-bridge-error no-server <text>", 1;
//   - end of stdin or of the socket: 0;
//   - 60 s without a byte on stdin (the client pings every 5 s): 1.
func Bridge(stdin io.Reader, stdout io.Writer, socket string, stdinIsTerminal bool) int {
	if stdinIsTerminal {
		fmt.Fprintln(bridgeStderr, "devtools bridge is run by devtools over ssh, not by hand")
		return 2
	}
	if !filepath.IsAbs(socket) {
		return bridgeError(stdout, "the state socket "+socket+" is not an absolute path")
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return bridgeError(stdout, "no devtools server answers at "+socket+": "+rootCause(err))
	}
	defer conn.Close()
	if _, err := io.WriteString(stdout, readyLine+"\n"); err != nil {
		return 1
	}

	clk := bridgeClock
	var lastIn atomic.Int64
	lastIn.Store(clk.Now().UnixNano())
	result := make(chan int, 2)

	go func() { // the client to the server
		buf := make([]byte, 32<<10)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				lastIn.Store(clk.Now().UnixNano())
				if _, werr := conn.Write(buf[:n]); werr != nil {
					result <- 0 // the socket closed
					return
				}
			}
			if err != nil {
				result <- 0 // end of stdin, or the client is gone
				return
			}
		}
	}()
	go func() { // the server to the client
		buf := make([]byte, 32<<10)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if _, werr := stdout.Write(buf[:n]); werr != nil {
					result <- 1 // nobody reads what the server sends
					return
				}
			}
			if err != nil {
				result <- 0 // the socket closed
				return
			}
		}
	}()

	for {
		since := clk.Now().Sub(time.Unix(0, lastIn.Load()))
		if since >= bridgeSilence {
			return 1
		}
		select {
		case code := <-result:
			return code
		case <-clk.After(bridgeSilence - since):
		}
	}
}

// bridgeError writes the error line of 4.4 and returns 1.
func bridgeError(stdout io.Writer, text string) int {
	fmt.Fprintf(stdout, "%s%s %s\n", errorPrefix, NoServer, session.Sanitize(text, session.MaxText))
	return 1
}
