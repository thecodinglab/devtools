package cli

// The identity of this build: what devtools version prints, what the hellos
// and the welcome of the server carry, and what server status compares.

import (
	"fmt"
	"io"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// buildVersion is set at link time, by the Nix package of flake.nix:
//
//	go build -ldflags "-X devtools/internal/cli.buildVersion=0.1.0+04c5fef"
//
// It is empty in a plain go build.
var buildVersion string

// version is the version of this build, for the hellos, server status and
// devtools version.
var version = buildIdentity(buildVersion, debug.ReadBuildInfo)

// buildIdentity is the version of a build: the one stamped at link time;
// else the module version of the build info when it is a real one (go
// install of a tagged module); else the commit of a checkout built with go
// build, short, with "-dirty" when it had uncommitted changes; else "dev".
func buildIdentity(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "" {
		return stamped
	}
	info, ok := read()
	if !ok || info == nil {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	revision, modified := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if modified {
		revision += "-dirty"
	}
	return revision
}

// versionCommand is devtools version: the version of this build, one line.
// It reads nothing and reaches no server, so a server can run it on the
// build that is installed to learn its version (internal/server/update.go).
func versionCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of this build of devtools",
		Args:  usageNoArgs("usage: devtools version"),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintln(stdout, version)
			return err
		},
	}
}
