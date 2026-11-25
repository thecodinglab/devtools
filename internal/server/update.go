package server

import (
	"context"
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
// (Options.Self, which boot takes from the kernel and not from the
// symlink, platform.SelfExecutable). Another file is another build, which
// waits for a restart: the watch asks it its version ("<path> version",
// bounded by versionLimit) for a label, and welcome.installed says it to
// every new connection and event.update to the connected ones that have
// CapUpdate. A label equal to this build's version (two dirty builds of
// one commit print the same stamp) or none is installedUnknown. The answer
// is asked again only when Program names another file again.
//
// The same look feeds the automatic upgrade (autoupgrade.go): what the
// watch found installed is its candidate, so the build the windows are
// told about and the build the server upgrades to are decided once, by
// the same file identity. A live upgrade starts the new image with a new
// server whose Self is the new file: it begins with nothing installed, so
// the windows that reconnect lose ↑ update at once.

// versionLimit bounds "<path> version".
const versionLimit = 3 * time.Second

// maxInstalled bounds the version the installed build prints.
const maxInstalled = 64

// installedUnknown is welcome.installed of an installed build whose
// version does not tell it from the running one: an older devtools without
// the version command, or another build that prints this build's version.
const installedUnknown = "unknown"

// watchInstall runs the watch until the server ends. Without a Program or
// the identity of the running image it watches nothing.
func (s *Server) watchInstall() {
	defer s.wg.Done()
	if s.o.Program == "" || s.o.Self.Path == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.quit
		cancel()
	}()
	auto := s.newAutoUpgrade()
	var last *platform.Identity // the file Program named when the installed build was last decided
	t := time.NewTicker(s.o.UpdateInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.quit:
			return
		}
		id, err := platform.FileIdentity(s.o.Program)
		found := installedCandidate(s.o.Program, id, err, s.o.Self)
		// a profile in the middle of a switch keeps what was decided: it is
		// looked at again next time
		if err == nil && (last == nil || !sameCandidate(*last, id)) {
			last = &id
			installed := ""
			if found != nil {
				installed = s.installedVersion(ctx, id.Path)
			}
			if ctx.Err() != nil {
				return
			}
			s.postSoon(func(c *core) { c.setInstalled(installed) })
		}
		if auto != nil {
			auto.look(found)
		}
	}
}

// installedCandidate is what one look of the watch found: the build behind
// program when it is another file than the running image self, with the
// identity it has now; nil when it is the running one or cannot be read.
func installedCandidate(program string, id platform.Identity, err error, self platform.Identity) *candidate {
	if err != nil || id.Same(self) {
		return nil
	}
	return &candidate{program: program, id: id}
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
