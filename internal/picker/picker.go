package picker

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Option struct {
	Label string
	Value string
}

func SelectOption(options []Option, emptyMessage, preview string) (Option, error) {
	if len(options) == 0 {
		return Option{}, errors.New(emptyMessage)
	}
	var input strings.Builder
	byLine := make(map[string]Option, len(options))
	for _, option := range options {
		line := option.Label
		byLine[line] = option
		input.WriteString(line)
		input.WriteByte('\n')
	}
	var extraArgs []string
	if preview != "" {
		extraArgs = append(extraArgs, "--preview", preview)
	}
	line, err := runFZF(input.String(), extraArgs...)
	if err != nil {
		return Option{}, err
	}
	option, ok := byLine[line]
	if !ok {
		return Option{}, fmt.Errorf("unknown fzf selection %q", line)
	}
	return option, nil
}

func runFZF(input string, extraArgs ...string) (string, error) {
	cmd := exec.Command("fzf", append([]string{"--with-nth=1", "--delimiter=\t"}, extraArgs...)...)
	cmd.Stdin = strings.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("fzf: %s", msg)
	}
	return strings.TrimSpace(out.String()), nil
}
