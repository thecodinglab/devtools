package picker

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"devtools/internal/discovery"
)

type Option struct {
	Label string
	Value string
}

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
	line, err := runFZF(input.String())
	if err != nil {
		return discovery.Target{}, err
	}
	target, ok := byLine[line]
	if !ok {
		return discovery.Target{}, fmt.Errorf("unknown fzf selection %q", line)
	}
	return target, nil
}

func SelectOption(options []Option, emptyMessage string) (Option, error) {
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
	line, err := runFZF(input.String())
	if err != nil {
		return Option{}, err
	}
	option, ok := byLine[line]
	if !ok {
		return Option{}, fmt.Errorf("unknown fzf selection %q", line)
	}
	return option, nil
}

func runFZF(input string) (string, error) {
	cmd := exec.Command("fzf", "--with-nth=1")
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
