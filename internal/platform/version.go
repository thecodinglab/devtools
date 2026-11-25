package platform

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"time"
)

// BuildVersion runs "<path> version" in the environment env and returns
// the first line it printed: the version of the devtools build at path.
// The server asks the build installed on the profile with it
// (internal/server/update.go); ctx bounds the run.
func BuildVersion(ctx context.Context, path string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = env
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	line, _, _ := bufio.NewReader(&out).ReadLine()
	return string(line), nil
}
