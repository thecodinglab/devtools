package herdr

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The herdr client picks which machine its window shows, and the socket API
// reaches only a server, so nothing devtools can call switches the window
// to another machine. A herdr carrying the activation request patch (see
// the systems repo) watches a request file in its state directory instead,
// activates the machine and workspace named there the way a sidebar click
// does, and acknowledges the request's nonce.

// ActivationWait is how long ShowWorkspace waits for a client to take the
// request; the client looks every 100ms.
var ActivationWait = time.Second

type activationRequest struct {
	Nonce     string `json:"nonce"`
	Machine   string `json:"machine"`
	Workspace string `json:"workspace"`
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

// ShowWorkspace asks the herdr clients on this machine to show a
// workspace, on the saved machine with the profile id, or on Local when it
// is empty. It reports whether a client took the request: one without the
// patch never does.
func ShowWorkspace(machine, workspace string) (bool, error) {
	dir, err := stateDir()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return false, err
	}
	request := activationRequest{Nonce: hex.EncodeToString(nonce), Machine: machine, Workspace: workspace}
	data, err := json.Marshal(request)
	if err != nil {
		return false, err
	}
	// written whole, so a client never reads half a request
	path := filepath.Join(dir, "activate-request.json")
	temp, err := os.CreateTemp(dir, "activate-request-*.json")
	if err != nil {
		return false, err
	}
	_, err = temp.Write(data)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), path)
	}
	if err != nil {
		os.Remove(temp.Name())
		return false, err
	}

	ack := filepath.Join(dir, "activate-ack.json")
	deadline := time.Now().Add(ActivationWait)
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		data, err := os.ReadFile(ack)
		if err != nil {
			continue
		}
		var got struct {
			Nonce string `json:"nonce"`
		}
		if json.Unmarshal(data, &got) == nil && got.Nonce == request.Nonce {
			return true, nil
		}
	}
	return false, nil
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
