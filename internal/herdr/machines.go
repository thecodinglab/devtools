package herdr

import (
	"encoding/json"
	"fmt"
)

// Machine is a saved ssh machine profile, as herdr machine list --json
// reports it.
type Machine struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Target   string `json:"target"`
	Session  string `json:"session"`
	Enabled  bool   `json:"enabled"`
	Selected bool   `json:"selected"`
}

func ListMachines() ([]Machine, error) {
	out, err := output("machine", "list", "--json")
	if err != nil {
		return nil, err
	}
	return parseMachineList(out)
}

func parseMachineList(out []byte) ([]Machine, error) {
	var machines []Machine
	if err := json.Unmarshal(out, &machines); err != nil {
		return nil, fmt.Errorf("unexpected herdr machine list output: %w", err)
	}
	return machines, nil
}

// FindMachine resolves a profile id or label the way herdr --machine does.
func FindMachine(selector string) (Machine, error) {
	machines, err := ListMachines()
	if err != nil {
		return Machine{}, err
	}
	for _, machine := range machines {
		if machine.ID == selector {
			return machine, nil
		}
	}
	var found []Machine
	for _, machine := range machines {
		if machine.Label == selector {
			found = append(found, machine)
		}
	}
	switch len(found) {
	case 0:
		return Machine{}, fmt.Errorf("no saved herdr machine %q", selector)
	case 1:
		return found[0], nil
	}
	return Machine{}, fmt.Errorf("herdr machine label %q is ambiguous, use its id", selector)
}

// RemoteArgs are the herdr arguments that attach a client to the machine's
// server. The session is explicit even when it is the default, so an
// inherited HERDR_SESSION can't point the client at another remote server.
func (m Machine) RemoteArgs() []string {
	args := []string{"--remote", m.Target}
	if m.Session != "" {
		args = append(args, "--session", m.Session)
	}
	return args
}
