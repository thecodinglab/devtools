package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"devtools/internal/platform"
	"devtools/internal/proto"
	"devtools/internal/session"
)

// The installed update (CapUpdate). The service starts the server through
// the devtools of the profile (nix/service.nix), a symlink that follows the
// active generation, so an update changes where it points and not the
// running server. Once per UpdateInterval the watch resolves that path
// (Options.Program) and compares the file it names with the running image
// (Options.Executable, which boot takes from the kernel and not from the
// symlink, platform.RunningExecutable). Another file is another build,
// which waits for a restart: the watch asks it its version ("<path>
// version", bounded by versionLimit) for a label, and welcome.installed
// says it to every new connection and event.update to the connected ones
// that have CapUpdate. A label equal to this build's version (two dirty
// builds of one commit print the same stamp) or none is installedUnknown.
// The answer is asked again only when the path changes again.

// versionLimit bounds "<path> version".
const versionLimit = 3 * time.Second

// maxInstalled bounds the version the installed build prints.
const maxInstalled = 64

// installedUnknown is welcome.installed of an installed build whose
// version does not tell it from the running one: an older devtools without
// the version command, or another build that prints this build's version.
const installedUnknown = "unknown"

// watchInstall runs the watch until the server ends. Without a Program or
// an Executable it watches nothing.
func (s *Server) watchInstall() {
	defer s.wg.Done()
	if s.o.Program == "" || s.o.Executable == "" {
		return
	}
	self, err := os.Stat(s.o.Executable)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.quit
		cancel()
	}()
	last := "" // what Program resolved to when the installed build was last decided
	t := time.NewTicker(s.o.UpdateInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.quit:
			return
		}
		resolved, err := filepath.EvalSymlinks(s.o.Program)
		if err != nil || resolved == last {
			// a profile in the middle of a switch: looked at again next time
			continue
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		last = resolved
		installed := ""
		if !os.SameFile(fi, self) {
			installed = s.installedVersion(ctx, resolved)
		}
		if ctx.Err() != nil {
			return
		}
		s.postSoon(func(c *core) { c.setInstalled(installed) })
	}
}

// installedVersion asks the build at path, another file than the running
// one, for its version: installedUnknown when it gave none or this build's.
func (s *Server) installedVersion(ctx context.Context, path string) string {
	ask := s.o.InstalledVersion
	if ask == nil {
		ask = s.runVersion
	}
	ctx, cancel := context.WithTimeout(ctx, versionLimit)
	defer cancel()
	v, err := ask(ctx, path)
	v = session.Sanitize(strings.TrimSpace(v), maxInstalled)
	if err != nil || v == "" || v == s.o.Version {
		return installedUnknown
	}
	return v
}

// runVersion runs "<path> version" in the server's environment and returns
// the first line it printed.
func (s *Server) runVersion(ctx context.Context, path string) (string, error) {
	return platform.BuildVersion(ctx, path, s.o.Env)
}

// setInstalled takes what the watch decided, on the loop: a change is told
// to every interactive connection that has CapUpdate.
func (c *core) setInstalled(v string) {
	if v == c.installed {
		return
	}
	c.installed = v
	if v != "" {
		c.s.log.Info("another build of devtools is installed; a restart of the server runs it", "running", c.s.o.Version, "installed", v)
	} else {
		c.s.log.Info("the installed build of devtools is the running one again", "running", c.s.o.Version)
	}
	m := proto.Encode(proto.UpdateEvent{Installed: v})
	for _, cn := range c.reg.list() {
		if cn.kind == proto.KindInteractive && cn.Has(proto.CapUpdate) {
			c.send(cn, m)
		}
	}
}
