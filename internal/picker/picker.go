package picker

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"devtools/internal/discovery"
)

func Select(targets []discovery.Target) (discovery.Target, error) {
	if len(targets) == 0 {
		return discovery.Target{}, errors.New("no projects found")
	}
	var input strings.Builder
	byLine := make(map[string]discovery.Target, len(targets))
	for _, target := range targets {
		line := target.Label + "\t" + target.Path
		byLine[line] = target
		input.WriteString(line)
		input.WriteByte('\n')
	}
	cmd := exec.Command("fzf", "--with-nth=1")
	cmd.Stdin = strings.NewReader(input.String())
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return discovery.Target{}, fmt.Errorf("fzf: %s", msg)
	}
	line := strings.TrimSpace(out.String())
	target, ok := byLine[line]
	if !ok {
		return discovery.Target{}, fmt.Errorf("unknown fzf selection %q", line)
	}
	return target, nil
}
