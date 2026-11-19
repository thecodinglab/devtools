package srccheck

// The five source tests that hold the structure of the backends (3.10, 5.2
// rules 9, 10 and 13, 7.4, 7.6): one package runs each multiplexer, one
// method starts each client, callers ask for capabilities, the word herdr
// stays where the backend lives, and the backend packages decide nothing
// from the environment.

import (
	"go/ast"
	"go/scanner"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const (
	pkgBackend = "devtools/internal/backend"
	pkgMux     = "devtools/internal/mux"
	pkgTmux    = "devtools/internal/tmux"
	pkgHerdr   = "devtools/internal/herdr"
	pkgClient  = "devtools/internal/client"
	pkgCLI     = "devtools/internal/cli"
)

// programs are the two multiplexers and the package that alone runs each.
var programs = map[string]string{"tmux": pkgTmux, "herdr": pkgHerdr}

// execRunners are the functions of os/exec whose first program argument
// names the program they run or look up.
var execRunners = map[string]int{"Command": 0, "CommandContext": 1, "LookPath": 0}

// onlyBackendPackagesRunAMultiplexer (N21, rule 13): internal/tmux and
// internal/herdr are imported by internal/backend only; tmux is run only by
// internal/tmux and herdr only by internal/herdr; and inside internal/tmux
// the arguments that start a client (new-session without -d,
// attach-session, attach) occur only in Attach.
func onlyBackendPackagesRunAMultiplexer(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		for _, p := range []string{pkgTmux, pkgHerdr} {
			if f.importsPath(p) && f.Pkg != pkgBackend {
				vs = append(vs, f.Rel+": imports "+p)
			}
		}
		names := f.imports()
		inspectWithFunc(f.File, func(n ast.Node, in *ast.FuncDecl) {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			if path, name, ok := f.pkgSelector(names, call.Fun); ok && path == "os/exec" {
				if i, runs := execRunners[name]; runs && i < len(call.Args) {
					if prog, ok := stringLit(call.Args[i]); ok {
						base := prog[strings.LastIndex(prog, "/")+1:]
						if owner, known := programs[base]; known && f.Pkg != owner {
							vs = append(vs, f.pos(call.Pos())+": runs "+base+" outside "+owner)
						}
					}
				}
			}
			if f.Pkg != pkgTmux || isFunc(in, "Attach") {
				return
			}
			var args []string
			for _, a := range call.Args {
				if s, ok := stringLit(a); ok {
					args = append(args, s)
				}
			}
			for _, a := range args {
				switch {
				case a == "attach-session", a == "attach":
					vs = append(vs, f.pos(call.Pos())+": "+a+" outside Attach")
				case a == "new-session" && !contains(args, "-d"):
					vs = append(vs, f.pos(call.Pos())+": new-session without -d outside Attach")
				}
			}
		})
	}
	return sorted(vs)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestOnlyBackendPackagesRunAMultiplexer (N21).
func TestOnlyBackendPackagesRunAMultiplexer(t *testing.T) {
	holds(t, onlyBackendPackagesRunAMultiplexer, map[string]string{
		"internal/tmux/tmux.go": "package tmux\nimport \"os/exec\"\n" +
			"func NewDetached(s string) error { return exec.Command(\"tmux\", \"new-session\", \"-d\", \"-s\", s).Run() }\n" +
			"func Attach(s string) error { return exec.Command(\"tmux\", \"attach-session\", \"-t\", s).Run() }\n" +
			"func SwitchSession(s string) error { return exec.Command(\"tmux\", \"attach-session\", \"-t\", s).Run() }\n",
		"internal/backend/tmux.go": "package backend\nimport \"devtools/internal/tmux\"\nvar _ = tmux.Attach\n",
	}, "internal/tmux/tmux.go")
	// the other halves of the rule, each in a fixture of its own
	for name, files := range map[string]map[string]string{
		"an import outside the backend": {
			"internal/cli/hub.go": "package cli\nimport \"devtools/internal/herdr\"\nvar _ = herdr.Installed\n",
		},
		"tmux run outside internal/tmux": {
			"internal/remote/ssh.go": "package remote\nimport \"os/exec\"\nvar _ = exec.Command(\"/usr/bin/tmux\", \"detach-client\")\n",
		},
		"herdr found outside internal/herdr": {
			"internal/remote/ssh.go": "package remote\nimport \"os/exec\"\nfunc f() { exec.LookPath(\"herdr\") }\n",
		},
		"new-session without -d outside Attach": {
			"internal/tmux/tmux.go": "package tmux\nfunc run(args ...string) error { return nil }\nfunc Switch(s string) error { return run(\"new-session\", \"-A\", \"-s\", s) }\n",
		},
	} {
		if got := onlyBackendPackagesRunAMultiplexer(fixture(t, files)); len(got) != 1 {
			t.Errorf("%s: the check found %q, want one violation", name, got)
		}
	}
}

// devtoolsGo is the one file that may name client.Run.
const devtoolsGo = "internal/backend/devtools.go"

// runClientSeam returns the two identifiers of the one declaration
// `var runClient = client.Run` at the top level of internal/backend/
// devtools.go (P2-12): the name runClient and the selector client.Run. It is
// the seam the tests of internal/backend replace; every other reference to
// either is checked. Any other file gives an empty set.
func runClientSeam(f srcFile, names map[string]string) map[ast.Node]bool {
	seam := map[ast.Node]bool{}
	if f.Rel != devtoolsGo {
		return seam
	}
	for _, d := range f.File.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			if len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, id := range vs.Names {
				if id.Name != "runClient" {
					continue
				}
				if path, name, ok := f.pkgSelector(names, vs.Values[i]); ok && path == pkgClient && name == "Run" {
					seam[id] = true
					seam[vs.Values[i]] = true
				}
			}
		}
	}
	return seam
}

// clientStartsAreInsideEnterOnly (N11, rule 10): client.Run is referenced
// only in internal/backend/devtools.go, and there only inside an Enter
// method or as the value of the seam `var runClient = client.Run`;
// runClient, tmux.Attach and herdr.Attach only inside an Enter method of
// internal/backend, never at the top level (a package-level closure or alias
// would run outside Enter); Attach inside internal/tmux and internal/herdr
// by nothing but its own declaration, at the top level too; a method Enter
// is selected, called or taken as a value, only in internal/cli/entry.go;
// and internal/herdr has no Visit.
func clientStartsAreInsideEnterOnly(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		names := f.imports()
		seam := runClientSeam(f, names)
		inspectWithFunc(f.File, func(n ast.Node, in *ast.FuncDecl) {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if f.Pkg == pkgHerdr && x.Recv == nil && x.Name.Name == "Visit" {
					vs = append(vs, f.pos(x.Pos())+": herdr.Visit exists")
				}
			case *ast.SelectorExpr:
				path, name, ok := f.pkgSelector(names, x)
				if !ok {
					// x.Enter, m.Enter, mux.Multiplexer.Enter: the method of
					// a multiplexer, called or not
					if x.Sel.Name == "Enter" && f.Rel != "internal/cli/entry.go" {
						vs = append(vs, f.pos(x.Pos())+": a method Enter selected outside internal/cli/entry.go")
					}
					// copymode.Enter and the like are functions of another
					// package, not the method of a multiplexer
					return
				}
				switch {
				case path == pkgClient && name == "Run":
					switch {
					case f.Rel != devtoolsGo:
						vs = append(vs, f.pos(x.Pos())+": client.Run outside "+devtoolsGo)
					case !seam[x] && !isMethod(in, "Enter"):
						vs = append(vs, f.pos(x.Pos())+": client.Run outside an Enter method and the runClient seam")
					}
				case (path == pkgTmux || path == pkgHerdr) && name == "Attach":
					if f.Pkg != pkgBackend || !isMethod(in, "Enter") {
						vs = append(vs, f.pos(x.Pos())+": "+path+".Attach outside an Enter method of internal/backend")
					}
				}
			case *ast.Ident:
				switch {
				case f.Pkg == pkgBackend && x.Name == "runClient":
					if !seam[x] && !isMethod(in, "Enter") {
						vs = append(vs, f.pos(x.Pos())+": runClient outside an Enter method")
					}
				case (f.Pkg == pkgTmux || f.Pkg == pkgHerdr) && x.Name == "Attach":
					switch {
					case in == nil:
						vs = append(vs, f.pos(x.Pos())+": Attach referenced at the top level")
					case in.Name != x:
						vs = append(vs, f.pos(x.Pos())+": Attach referenced by "+in.Name.Name)
					}
				}
			}
		})
	}
	return sorted(vs)
}

// TestClientStartsAreInsideEnterOnly (N11).
func TestClientStartsAreInsideEnterOnly(t *testing.T) {
	holds(t, clientStartsAreInsideEnterOnly, map[string]string{
		"internal/backend/devtools.go": "package backend\nimport \"devtools/internal/client\"\n" +
			"var runClient = client.Run\ntype D struct{}\nfunc (D) Enter() error { return runClient() }\n",
		"internal/backend/tmux.go": "package backend\nimport \"devtools/internal/tmux\"\n" +
			"type T struct{}\nfunc (T) Enter() error { return tmux.Attach() }\nfunc (T) Show() error { return tmux.Attach() }\n",
		"internal/cli/entry.go": "package cli\ntype m interface{ Enter() error }\nfunc enter(x m) error { return x.Enter() }\n",
	}, "internal/backend/tmux.go")
	for name, files := range map[string]map[string]string{
		"client.Run outside devtools.go": {
			"internal/backend/herdr.go": "package backend\nimport \"devtools/internal/client\"\nvar run = client.Run\n",
		},
		"Enter called outside entry.go": {
			"internal/cli/hub.go": "package cli\ntype m interface{ Enter() error }\nfunc open(x m) error { return x.Enter() }\n",
		},
		"herdr.Visit exists": {
			"internal/herdr/server.go": "package herdr\nfunc Visit(focus func() error) error { return focus() }\n",
		},
		"Attach called inside internal/herdr": {
			"internal/herdr/server.go": "package herdr\nfunc Attach() error { return nil }\nfunc open() error { return Attach() }\n",
		},
		"runClient outside Enter": {
			"internal/backend/devtools.go": "package backend\nimport \"devtools/internal/client\"\n" +
				"var runClient = client.Run\nfunc start() error { return runClient() }\n",
		},
		// W6 review (SOL-01): devtools.go is the file of the seam, not a
		// licence for client.Run anywhere in it
		"client.Run outside Enter in devtools.go": {
			"internal/backend/devtools.go": "package backend\nimport \"devtools/internal/client\"\n" +
				"func start(o client.Options) error { return client.Run(o) }\n",
		},
		"client.Run in a second package-level variable of devtools.go": {
			"internal/backend/devtools.go": "package backend\nimport \"devtools/internal/client\"\n" +
				"var runClient = client.Run\nvar start = client.Run\n" +
				"type D struct{}\nfunc (D) Enter() error { return runClient() }\n",
		},
		// W6 review (SEC6-W6-2): package-level references, aliases and
		// method values
		"runClient in a package-level closure": {
			"internal/backend/devtools.go": "package backend\nimport \"devtools/internal/client\"\n" +
				"var runClient = client.Run\nvar open = func() error { return runClient() }\n",
		},
		"runClient aliased at package level": {
			"internal/backend/devtools.go": "package backend\nimport \"devtools/internal/client\"\n" +
				"var runClient = client.Run\nvar start = runClient\nfunc show() error { return start() }\n",
		},
		"Enter taken as a method value outside entry.go": {
			"internal/cli/hub.go": "package cli\ntype m interface{ Enter() error }\nfunc open(x m) error { f := x.Enter; return f() }\n",
		},
		"Enter taken as a method expression outside entry.go": {
			"internal/cli/hub.go": "package cli\ntype m interface{ Enter() error }\nvar open = m.Enter\n",
		},
		"Attach aliased at package level in internal/tmux": {
			"internal/tmux/tmux.go": "package tmux\nfunc Attach() error { return nil }\n" +
				"var start = Attach\nfunc Switch() error { return start() }\n",
		},
		"tmux.Attach in a package-level closure of internal/backend": {
			"internal/backend/tmux.go": "package backend\nimport \"devtools/internal/tmux\"\n" +
				"var open = func() error { return tmux.Attach() }\n",
		},
	} {
		if got := clientStartsAreInsideEnterOnly(fixture(t, files)); len(got) != 1 {
			t.Errorf("%s: the check found %q, want one violation", name, got)
		}
	}
}

// backendNames are the names of the three backends as plain strings.
var backendNames = map[string]bool{"devtools": true, "tmux": true, "herdr": true}

// callersAskForCapabilities (3.10.2, 7.6): the constants mux.Tmux, mux.Herdr
// and mux.Devtools occur in non-test Go only in internal/mux and
// internal/backend, and no file outside those two compares the result of a
// Name() with a backend, by == or != or in a switch.
func callersAskForCapabilities(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		if f.Pkg == pkgMux || f.Pkg == pkgBackend {
			continue
		}
		names := f.imports()
		isBackend := func(e ast.Expr) bool {
			e = unconvert(f, names, e)
			if s, ok := stringLit(e); ok {
				return backendNames[s]
			}
			path, name, ok := f.pkgSelector(names, e)
			return ok && path == pkgMux && (name == "Tmux" || name == "Herdr" || name == "Devtools")
		}
		isName := func(e ast.Expr) bool {
			call, ok := unconvert(f, names, e).(*ast.CallExpr)
			if !ok || len(call.Args) != 0 {
				return false
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			return ok && sel.Sel.Name == "Name"
		}
		ast.Inspect(f.File, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if path, name, ok := f.pkgSelector(names, x); ok && path == pkgMux && (name == "Tmux" || name == "Herdr" || name == "Devtools") {
					vs = append(vs, f.pos(x.Pos())+": mux."+name)
				}
			case *ast.BinaryExpr:
				if (x.Op == token.EQL || x.Op == token.NEQ) && (isName(x.X) && isBackend(x.Y) || isName(x.Y) && isBackend(x.X)) {
					vs = append(vs, f.pos(x.Pos())+": compares Name() with a backend")
				}
			case *ast.SwitchStmt:
				if x.Tag == nil || !isName(x.Tag) {
					return true
				}
				for _, s := range x.Body.List {
					for _, e := range s.(*ast.CaseClause).List {
						if isBackend(e) {
							vs = append(vs, f.pos(x.Pos())+": switches on Name() over backends")
							return true
						}
					}
				}
			}
			return true
		})
	}
	return sorted(vs)
}

// unconvert strips the conversions string(...) and mux.Backend(...) and
// parentheses around e.
func unconvert(f srcFile, names map[string]string, e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
			continue
		case *ast.CallExpr:
			if len(x.Args) == 1 {
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "string" {
					e = x.Args[0]
					continue
				}
				if path, name, ok := f.pkgSelector(names, x.Fun); ok && path == pkgMux && name == "Backend" {
					e = x.Args[0]
					continue
				}
			}
		}
		return e
	}
}

// TestCallersAskForCapabilitiesNotBackends: a caller asks for a capability
// (mux.Need, Caps().Has), never for a backend; switchSession and
// hubBackend.Switch grew their herdr branches that way.
func TestCallersAskForCapabilitiesNotBackends(t *testing.T) {
	holds(t, callersAskForCapabilities, map[string]string{
		"internal/backend/herdr.go": "package backend\nimport \"devtools/internal/mux\"\nvar _ = mux.Herdr\n",
		"internal/cli/entry.go": "package cli\nimport \"devtools/internal/mux\"\n" +
			"func ok(m mux.Multiplexer) bool { return m.Caps().Has(mux.CapAgents) }\n",
		"internal/cli/hub.go": "package cli\nimport \"devtools/internal/mux\"\n" +
			"func herdrOnly(m mux.Multiplexer) bool { return string(m.Name()) == \"herdr\" }\n",
	}, "internal/cli/hub.go")
	for name, files := range map[string]map[string]string{
		"a backend constant outside mux and backend": {
			"internal/cli/sessions.go": "package cli\nimport \"devtools/internal/mux\"\nvar b mux.Backend = mux.Tmux\n",
		},
		"a switch on Name()": {
			"internal/cli/sessions.go": "package cli\ntype n interface{ Name() string }\n" +
				"func f(m n) int { switch m.Name() { case \"tmux\": return 1 }; return 0 }\n",
		},
	} {
		if got := callersAskForCapabilities(fixture(t, files)); len(got) != 1 {
			t.Errorf("%s: the check found %q, want one violation", name, got)
		}
	}
}

// herdrPlaces are where the word herdr may be written in code: the packages
// that implement, select or detect the backend, the marker list of the
// guard, the environment filter of the pane and the words (R1).
var herdrPackages = []string{pkgHerdr, pkgBackend, pkgMux}
var herdrFiles = []string{"internal/client/guard/guard.go", "internal/pane/env.go", "internal/ui/words.go"}

// herdrNamedOnlyWhereTheBackendLives finds the word herdr, in any case, in
// an identifier, a string or a character of a non-test file of a product
// package outside herdrPackages and herdrFiles. Comments are not code and
// may say where a behaviour came from; test-only packages (rule 8) are the
// harness and fake herdr there.
func herdrNamedOnlyWhereTheBackendLives(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		if isTestOnly(f.Pkg) || contains(herdrFiles, f.Rel) {
			continue
		}
		allowed := false
		for _, p := range herdrPackages {
			if f.Pkg == p {
				allowed = true
			}
		}
		if allowed {
			continue
		}
		var s scanner.Scanner
		fset := token.NewFileSet()
		file := fset.AddFile(f.Rel, -1, len(f.Src))
		s.Init(file, f.Src, nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if (tok == token.IDENT || tok == token.STRING || tok == token.CHAR) && strings.Contains(strings.ToLower(lit), "herdr") {
				vs = append(vs, f.Rel+":"+strconv.Itoa(fset.Position(pos).Line)+": "+lit)
			}
		}
	}
	return sorted(vs)
}

// TestHerdrIsNamedOnlyWhereTheBackendLives (R1): it replaces "git grep -il
// herdr prints nothing" of revision 1, which the backend kept in the code
// (F37) makes impossible.
func TestHerdrIsNamedOnlyWhereTheBackendLives(t *testing.T) {
	holds(t, herdrNamedOnlyWhereTheBackendLives, map[string]string{
		"internal/client/guard/guard.go": "package guard\nvar markers = []string{\"HERDR_ENV\"}\n",
		"internal/hub/hub.go":            "package hub\n// the herdr popup of today is gone\nvar x = 1\n",
		"internal/cli/sessions.go":       "package cli\nfunc herdrSessions() {}\n",
	}, "internal/cli/sessions.go")
	if got := herdrNamedOnlyWhereTheBackendLives(fixture(t, map[string]string{
		"internal/cli/hub.go": "package cli\nvar note = \"select the machine in the Herdr sidebar\"\n",
	})); len(got) != 1 {
		t.Errorf("a string naming Herdr: the check found %q, want one violation", got)
	}
}

// environmentReaders are the functions that read the environment.
var environmentReaders = map[string]map[string]bool{
	"os":      {"Getenv": true, "LookupEnv": true, "Environ": true},
	"syscall": {"Getenv": true, "Environ": true},
}

// backendPackagesDoNotReadTheEnvironment (N18, N19): no non-test file of
// internal/tmux reads the environment, and in internal/herdr only bin and
// Installed do, for HERDR_BIN_PATH. Where a command runs is decided once,
// by mux.Detect over backend.Facts.
func backendPackagesDoNotReadTheEnvironment(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		if f.Pkg != pkgTmux && f.Pkg != pkgHerdr {
			continue
		}
		names := f.imports()
		inspectWithFunc(f.File, func(n ast.Node, in *ast.FuncDecl) {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return
			}
			path, name, ok := f.pkgSelector(names, sel)
			if !ok || !environmentReaders[path][name] {
				return
			}
			if f.Pkg == pkgHerdr && isFunc(in, "bin", "Installed") {
				return
			}
			vs = append(vs, f.pos(sel.Pos())+": "+path+"."+name)
		})
	}
	return sorted(vs)
}

// TestBackendPackagesDoNotReadTheEnvironment: a wrapper that read TMUX or
// HERDR_ENV would decide the context a second time, beside mux.Detect.
func TestBackendPackagesDoNotReadTheEnvironment(t *testing.T) {
	holds(t, backendPackagesDoNotReadTheEnvironment, map[string]string{
		"internal/herdr/herdr.go": "package herdr\nimport \"os\"\n" +
			"func bin() string { return os.Getenv(\"HERDR_BIN_PATH\") }\n" +
			"func Inside() bool { return os.Getenv(\"HERDR_ENV\") == \"1\" }\n",
		"internal/remote/ssh.go": "package remote\nimport \"os\"\nvar _ = os.Getenv(\"SSH_AUTH_SOCK\")\n",
	}, "internal/herdr/herdr.go")
	if got := backendPackagesDoNotReadTheEnvironment(fixture(t, map[string]string{
		"internal/tmux/tmux.go": "package tmux\nimport \"os\"\nfunc inside() bool { _, ok := os.LookupEnv(\"TMUX\"); return ok }\n",
	})); len(got) != 1 {
		t.Errorf("TMUX read in internal/tmux: the check found %q, want one violation", got)
	}
}

// everyBackendHelperHasACaller (7.1.3, the first limit on "stays"): a
// function or method of internal/tmux or internal/herdr that no non-test file
// references does not stay; a test that calls it is not a reason to keep it
// (7.6). A reference is the name used anywhere but in its own declaration: a
// plain identifier in the same package, pkg.Name in another, and for a
// method any selector .Name, whatever its receiver.
func everyBackendHelperHasACaller(files []srcFile) []string {
	type key struct{ pkg, name string }
	const anyRecv = "*"
	used := map[key]bool{}
	for _, f := range files {
		names := f.imports()
		sels := map[*ast.Ident]bool{}
		ast.Inspect(f.File, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				sels[x.Name] = true // the declaration is not a reference
			case *ast.SelectorExpr:
				sels[x.Sel] = true
				if path, name, ok := f.pkgSelector(names, x); ok {
					used[key{path, name}] = true
				} else {
					used[key{anyRecv, x.Sel.Name}] = true
				}
			case *ast.Ident:
				if !sels[x] {
					used[key{f.Pkg, x.Name}] = true
				}
			}
			return true
		})
	}
	var vs []string
	for _, f := range files {
		if f.Pkg != pkgTmux && f.Pkg != pkgHerdr {
			continue
		}
		for _, d := range f.File.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name == "init" || fd.Name.Name == "_" {
				continue
			}
			if used[key{f.Pkg, fd.Name.Name}] || fd.Recv != nil && used[key{anyRecv, fd.Name.Name}] {
				continue
			}
			vs = append(vs, f.pos(fd.Pos())+": "+fd.Name.Name+" has no caller in non-test code")
		}
	}
	return sorted(vs)
}

// TestEveryBackendHelperHasACaller (7.1.3): the fzf previews fed
// tmux.ListWindows, herdr.ListTabs, herdr.TabPanes and herdr.ReadAgent; with
// the previews gone they went too, and no helper of the two packages is kept
// because it is harmless (W6 review, SOL-02).
func TestEveryBackendHelperHasACaller(t *testing.T) {
	holds(t, everyBackendHelperHasACaller, map[string]string{
		"internal/tmux/tmux.go": "package tmux\nfunc Has(s string) bool { return list(s) != nil }\n" +
			"func list(s string) []string { return nil }\nfunc ListWindows(s string) []string { return list(s) }\n",
		"internal/herdr/herdr.go": "package herdr\ntype Client struct{}\n" +
			"func (Client) ListAgents() error { return nil }\n",
		"internal/backend/tmux.go": "package backend\nimport (\n\"devtools/internal/herdr\"\n\"devtools/internal/tmux\"\n)\n" +
			"var _ = tmux.Has\nvar _ = herdr.Client{}.ListAgents\n",
	}, "internal/tmux/tmux.go")
	if got := everyBackendHelperHasACaller(fixture(t, map[string]string{
		"internal/herdr/agents.go": "package herdr\ntype Client struct{}\n" +
			"func (Client) ReadAgent(target string) (string, error) { return \"\", nil }\n",
		"internal/cli/hub.go": "package cli\nfunc ListAgents() {}\n",
	})); len(got) != 1 {
		t.Errorf("a method of internal/herdr nobody calls: the check found %q, want one violation", got)
	}
}
