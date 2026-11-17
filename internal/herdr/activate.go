package herdr

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The herdr client picks which machine its window shows, and the socket API
// reaches only a server, so nothing devtools can call switches the window
// to another machine. A herdr carrying the activation request patch (see
// the systems repo) watches a request file in its state directory instead:
// it says it received a request at once, waits until the machine it names
// is connected, activates the machine and workspace the way a sidebar click
// does, and says it is done.

var (
	// ReceiveWait is how long ShowWorkspace waits for a client to say it
	// received the request; a client looks every 100ms.
	ReceiveWait = time.Second
	// DoneWait is how long it then waits for the switch, which takes as
	// long as connecting to a machine saved a moment ago.
	DoneWait = 30 * time.Second
)

// ErrNoClient is returned when no herdr client takes the request: none is
// attached, or the one that is predates the activation patch.
var ErrNoClient = errors.New("no herdr client took the request")

type activationRequest struct {
	Nonce     string `json:"nonce"`
	Machine   string `json:"machine"`
	Workspace string `json:"workspace"`
}

type activationAck struct {
	Nonce string `json:"nonce"`
	// State is received, then done; a client of the first patch only ever
	// wrote the nonce, once it switched.
	State string `json:"state"`
}

// stateDir is the client state directory of herdr on unix.
func stateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "herdr", "client"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "herdr", "client"), nil
}

// ShowWorkspace has the herdr clients on this machine show a workspace, on
// the saved machine with the profile id, or on Local when it is empty. It
// returns ErrNoClient when no client takes the request.
func ShowWorkspace(machine, workspace string) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	request := activationRequest{Nonce: hex.EncodeToString(nonce), Machine: machine, Workspace: workspace}
	if err := writeRequest(dir, request); err != nil {
		return err
	}

	ack := filepath.Join(dir, "activate-ack.json")
	state := func() string {
		data, err := os.ReadFile(ack)
		if err != nil {
			return ""
		}
		var got activationAck
		if json.Unmarshal(data, &got) != nil || got.Nonce != request.Nonce {
			return ""
		}
		if got.State == "" {
			return "done"
		}
		return got.State
	}
	received := time.Now().Add(ReceiveWait)
	for state() == "" {
		if time.Now().After(received) {
			return ErrNoClient
		}
		time.Sleep(25 * time.Millisecond)
	}
	done := time.Now().Add(DoneWait)
	for state() != "done" {
		if time.Now().After(done) {
			return fmt.Errorf("herdr did not switch to the workspace within %s", DoneWait)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil
}

// writeRequest replaces the request file whole, so a client never reads
// half a request.
func writeRequest(dir string, request activationRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "activate-request-*.json")
	if err != nil {
		return err
	}
	_, err = temp.Write(data)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), filepath.Join(dir, "activate-request.json"))
	}
	if err != nil {
		os.Remove(temp.Name())
	}
	return err
}

// ShowsMachine reports whether the herdr window shows a saved machine
// rather than Local.
func ShowsMachine() bool {
	machines, err := ListMachines()
	if err != nil {
		return false
	}
	for _, machine := range machines {
		if machine.Selected {
			return true
		}
	}
	return false
}
