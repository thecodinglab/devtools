package testkit

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The import rules of 5.2 that are held as data: rules 1 to 8, 12 and 13.
// Rules 9 to 11 and the second half of rule 13 are source-level tests of
// internal/srccheck (7.4).
//
// A rule is about the imports of the non-test files of a package (files not
// ending in _test.go). A file is looked at when it belongs to a build on
// linux or darwin, with cgo on or off; a file only the tag "tools" or
// "ignore" selects (internal/deps) belongs to no build and is not.

// pattern is a package path: "devtools/internal/server" names that package,
// "devtools/internal/server/..." it and every package below it.
type pattern string

func (p pattern) match(pkg string) bool {
	if base, ok := strings.CutSuffix(string(p), "/..."); ok {
		return pkg == base || strings.HasPrefix(pkg, base+"/")
	}
	return pkg == string(p)
}

func matchAny(ps []pattern, pkg string) bool {
	for _, p := range ps {
		if p.match(pkg) {
			return true
		}
	}
	return false
}

const mod = "devtools"

// testOnly are the test-only packages of rule 8.
var testOnly = []pattern{
	"devtools/internal/proto/prototest/...",
	"devtools/internal/server/servertest/...",
	"devtools/internal/client/clienttest/...",
	"devtools/internal/fleet/fleettest/...",
	"devtools/internal/testkit/...",
	"devtools/internal/integration/...",
	"devtools/internal/e2e/...",
	"devtools/internal/srccheck/...",
}

var bubbletea = []pattern{"charm.land/bubbletea/v2/...", "github.com/charmbracelet/bubbletea/..."}

// rule is one rule of 5.2. In: the packages it is about (Except taken out;
// test-only packages too when NotTestOnly). Then one of three forms:
// Deny, imports In may not have; Only with Of, the packages Of names may be
// imported only by the packages of Only (In is then every package); Allow,
// In imports the standard library and the packages of Allow and nothing
// else (AllowThirdParty: and every package outside the module).
type rule struct {
	ID          string
	Text        string
	In          []pattern
	Except      []pattern
	NotTestOnly bool

	Deny     []pattern
	DenyBut  []pattern // imports Deny matches that are allowed after all
	Of       []pattern
	Only     []pattern
	Allow    []pattern
	AllowAll bool // with Allow: every package outside the module is allowed too
}

var all = []pattern{"devtools/..."}

var rules = []rule{
	{
		ID: "1", Text: "nothing under internal/server imports the client, the hub, the fleet or remote",
		In: []pattern{"devtools/internal/server/..."}, NotTestOnly: true,
		Deny: []pattern{"devtools/internal/client/...", "devtools/internal/hub/...", "devtools/internal/fleet/...", "devtools/internal/remote/..."},
	},
	{
		ID: "2", Text: "nothing under internal/client imports the server, internal/pane or internal/vt",
		In: []pattern{"devtools/internal/client/..."}, NotTestOnly: true,
		Deny: []pattern{"devtools/internal/server/...", "devtools/internal/pane/...", "devtools/internal/vt/..."},
	},
	{
		ID: "3", Text: "bubbletea is imported by internal/client, internal/client/chrome and internal/hub only",
		In: all, NotTestOnly: true,
		Of:   bubbletea,
		Only: []pattern{"devtools/internal/client", "devtools/internal/client/chrome", "devtools/internal/hub"},
	},
	{
		ID: "4", Text: "internal/fleet imports no bubbletea, os/exec, server, pane, vt or client",
		In: []pattern{"devtools/internal/fleet/..."}, NotTestOnly: true,
		Deny: append([]pattern{"os/exec", "devtools/internal/server/...", "devtools/internal/pane/...", "devtools/internal/vt/...", "devtools/internal/client/..."}, bubbletea...),
	},
	{
		ID: "5", Text: "cgo (the pseudo-package C) is imported only in internal/vt",
		In: all, Of: []pattern{"C"}, Only: []pattern{"devtools/internal/vt"},
	},
	{
		ID: "5", Text: "creack/pty is imported by internal/pane and test packages only",
		In: all, Of: []pattern{"github.com/creack/pty/..."}, Only: append([]pattern{"devtools/internal/pane"}, testOnly...),
	},
	{
		ID: "6", Text: "internal/proto and internal/session import the standard library only",
		In: []pattern{"devtools/internal/proto", "devtools/internal/session"}, Allow: []pattern{},
	},
	{
		ID: "6", Text: "internal/agent imports the standard library and internal/session only",
		In: []pattern{"devtools/internal/agent"}, Allow: []pattern{"devtools/internal/session"},
	},
	{
		ID: "6", Text: "internal/ui imports no devtools package",
		In: []pattern{"devtools/internal/ui/..."}, Allow: []pattern{}, AllowAll: true,
	},
	{
		ID: "7", Text: "the look goes hub -> chrome -> (screen, session, ui): chrome imports neither the fleet nor agent nor the hub",
		In:   []pattern{"devtools/internal/client/chrome/..."},
		Deny: []pattern{"devtools/internal/fleet/...", "devtools/internal/agent/...", "devtools/internal/hub/..."},
	},
	{
		ID: "8", Text: "no non-test package imports a test-only package",
		In: all, Except: testOnly,
		Deny: testOnly,
	},
	{
		ID: "12", Text: "internal/mux imports no implementation (internal/client/guard is allowed)",
		In:      []pattern{"devtools/internal/mux/..."},
		Deny:    []pattern{"devtools/internal/backend/...", "devtools/internal/tmux/...", "devtools/internal/herdr/...", "devtools/internal/client/..."},
		DenyBut: []pattern{"devtools/internal/client/guard"},
	},
	{
		ID: "12", Text: "the server, the client, the fleet, remote, the hub, pane, vt and config import neither internal/mux nor internal/backend",
		In: []pattern{
			"devtools/internal/server/...", "devtools/internal/client/...", "devtools/internal/fleet/...",
			"devtools/internal/remote/...", "devtools/internal/hub/...", "devtools/internal/pane/...",
			"devtools/internal/vt/...", "devtools/internal/config/...",
		},
		Deny: []pattern{"devtools/internal/mux/...", "devtools/internal/backend/..."},
	},
	{
		ID: "13", Text: "internal/tmux and internal/herdr are imported only by internal/backend",
		In: all, Of: []pattern{"devtools/internal/tmux/...", "devtools/internal/herdr/..."},
		Only: []pattern{"devtools/internal/backend"},
	},
}

// exception is one listed exception of 5.2: in Pkg (and, when Files is set,
// only in those files) the imports of Imports break no rule. Each line is
// removed by the package that removes the import; an exception that no
// import uses fails the test.
type exception struct {
	Pkg     string
	Files   []string
	Imports []string
	Until   string
}

var exceptions = []exception{}

// goFile is one non-test Go file and its imports.
type goFile struct {
	Pkg     string // import path
	Path    string // relative to the module root
	Name    string // base name
	Imports []string
}

// isStd reports whether an import path is of the standard library: its
// first element has no dot and it is not of this module. "C" is not.
func isStd(path string) bool {
	if path == "C" {
		return false
	}
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".") && first != mod
}

// scan parses the import lists of every non-test Go file under root, a
// module named mod. testdata, vendor and hidden directories are left out.
func scan(root string) ([]goFile, error) {
	var files []goFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			// A directory with its own go.mod is another module.
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			return err
		}
		if !inSomeBuild(name, f.Comments, f.Package) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dir := filepath.ToSlash(filepath.Dir(rel))
		pkg := mod
		if dir != "." {
			pkg = mod + "/" + dir
		}
		g := goFile{Pkg: pkg, Path: filepath.ToSlash(rel), Name: name}
		for _, im := range f.Imports {
			p, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: %v", rel, err)
			}
			g.Imports = append(g.Imports, p)
		}
		files = append(files, g)
		return nil
	})
	return files, err
}

// builds are the configurations a file must belong to one of.
var builds = []map[string]bool{
	{"linux": true, "unix": true, "cgo": true},
	{"linux": true, "unix": true},
	{"darwin": true, "unix": true, "cgo": true},
	{"darwin": true, "unix": true},
}

var knownOS = []string{"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos"}

// inSomeBuild reports whether a file belongs to a build on linux or darwin,
// with cgo on or off, judged by its //go:build line and its file name.
func inSomeBuild(name string, comments []*ast.CommentGroup, pkgPos token.Pos) bool {
	var expr constraint.Expr
	for _, cg := range comments {
		if cg.Pos() > pkgPos {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				if e, err := constraint.Parse(c.Text); err == nil {
					expr = e
				}
			}
		}
	}
	base := strings.TrimSuffix(name, ".go")
	for _, b := range builds {
		if !nameMatches(base, b) {
			continue
		}
		if expr == nil || expr.Eval(func(tag string) bool {
			return b[tag] || strings.HasPrefix(tag, "go1.") || tag == "gc" || tag == "amd64" || tag == "arm64"
		}) {
			return true
		}
	}
	return false
}

// nameMatches applies the _GOOS suffix of a file name.
func nameMatches(base string, b map[string]bool) bool {
	parts := strings.Split(base, "_")
	for i := len(parts) - 1; i >= 1 && i >= len(parts)-2; i-- {
		for _, o := range knownOS {
			if parts[i] == o {
				return b[o]
			}
		}
	}
	return true
}

// violation is one import that breaks a rule.
type violation struct {
	Rule string
	Text string
	File string
	Pkg  string
	Imp  string
}

func (v violation) String() string {
	return fmt.Sprintf("rule %s: %s (%s) imports %s: %s", v.Rule, v.Pkg, v.File, v.Imp, v.Text)
}

// check applies the rules to the files. It returns every violation and
// every exception that no import uses.
func check(files []goFile, rules []rule, exceptions []exception) (vs []violation, unused []exception) {
	used := make([]bool, len(exceptions))
	excepted := func(f goFile, imp string) bool {
		hit := false
		for i, e := range exceptions {
			if e.Pkg != f.Pkg {
				continue
			}
			if len(e.Files) > 0 && !contains(e.Files, f.Name) {
				continue
			}
			if contains(e.Imports, imp) {
				used[i] = true
				hit = true
			}
		}
		return hit
	}
	for _, f := range files {
		for _, imp := range f.Imports {
			var broken []rule
			for _, r := range rules {
				if !matchAny(r.In, f.Pkg) || matchAny(r.Except, f.Pkg) || (r.NotTestOnly && matchAny(testOnly, f.Pkg)) {
					continue
				}
				bad := false
				switch {
				case r.Of != nil:
					bad = matchAny(r.Of, imp) && !matchAny(r.Only, f.Pkg)
				case r.Allow != nil:
					switch {
					case isStd(imp):
					case matchAny(r.Allow, imp):
					case r.AllowAll && imp != "C" && !strings.HasPrefix(imp, mod+"/") && imp != mod:
					default:
						bad = true
					}
				default:
					bad = matchAny(r.Deny, imp) && !matchAny(r.DenyBut, imp)
				}
				if bad {
					broken = append(broken, r)
				}
			}
			if len(broken) == 0 || excepted(f, imp) {
				continue
			}
			for _, r := range broken {
				vs = append(vs, violation{Rule: r.ID, Text: r.Text, File: f.Path, Pkg: f.Pkg, Imp: imp})
			}
		}
	}
	for i, e := range exceptions {
		if !used[i] {
			unused = append(unused, e)
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].String() < vs[j].String() })
	return vs, unused
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// moduleRoot finds the directory of go.mod above the working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func TestImportRules(t *testing.T) {
	root := moduleRoot(t)
	files, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 20 {
		t.Fatalf("found only %d Go files under %s", len(files), root)
	}
	vs, unused := check(files, rules, exceptions)
	for _, v := range vs {
		t.Error(v)
	}
	for _, e := range unused {
		t.Errorf("the exception for %s importing %v (until %s) is used by no import: remove its line", e.Pkg, e.Imports, e.Until)
	}
}

func TestImportRulesCatchAViolation(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module devtools\n")
	// Allowed: the server imports proto, a test file imports what it likes,
	// a test-only package imports the PTY library, a tools file is in no
	// build, and testdata is not looked at.
	write("internal/server/a.go", "package server\nimport _ \"devtools/internal/proto\"\n")
	write("internal/server/a_test.go", "package server\nimport _ \"devtools/internal/client\"\n")
	write("internal/testkit/p.go", "package testkit\nimport _ \"github.com/creack/pty\"\n")
	write("internal/deps/deps.go", "//go:build tools\n\npackage deps\nimport _ \"github.com/creack/pty\"\n")
	write("internal/hub/testdata/x.go", "package x\nimport _ \"devtools/internal/server\"\n")
	write("internal/mux/m.go", "package mux\nimport _ \"devtools/internal/client/guard\"\n")
	// The one forbidden import: the server names the client (rule 1).
	write("internal/server/b.go", "package server\nimport (\n\t\"fmt\"\n\t\"devtools/internal/client/screen\"\n)\nvar _ = fmt.Sprint\nvar _ = screen.X\n")

	files, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	vs, unused := check(files, rules, nil)
	if len(unused) != 0 {
		t.Fatalf("unused exceptions without any: %v", unused)
	}
	if len(vs) != 1 {
		t.Fatalf("got %d violations, want 1: %v", len(vs), vs)
	}
	v := vs[0]
	if v.Rule != "1" || v.File != "internal/server/b.go" || v.Imp != "devtools/internal/client/screen" {
		t.Fatalf("violation %v", v)
	}
	if s := v.String(); !strings.Contains(s, "devtools/internal/client/screen") || !strings.Contains(s, "internal/server/b.go") {
		t.Fatalf("the failure does not name the offending import: %s", s)
	}

	// An exception covers its import, and one that no import uses is named.
	ex := []exception{
		{Pkg: "devtools/internal/server", Files: []string{"b.go"}, Imports: []string{"devtools/internal/client/screen"}, Until: "test"},
		{Pkg: "devtools/internal/cli", Imports: []string{"devtools/internal/tmux"}, Until: "gone"},
	}
	vs, unused = check(files, rules, ex)
	if len(vs) != 0 {
		t.Fatalf("an excepted import is still a violation: %v", vs)
	}
	if len(unused) != 1 || unused[0].Until != "gone" {
		t.Fatalf("unused exceptions: %v", unused)
	}

	// Every other kind of rule catches its import too.
	for _, c := range []struct{ rel, src, rule string }{
		{"internal/client/x/x.go", "package x\nimport _ \"devtools/internal/vt\"\n", "2"},
		{"internal/picker/p.go", "package picker\nimport _ \"charm.land/bubbletea/v2\"\n", "3"},
		{"internal/fleet/f.go", "package fleet\nimport _ \"os/exec\"\n", "4"},
		{"internal/pane/c.go", "package pane\n// #include <stdio.h>\nimport \"C\"\n", "5"},
		{"internal/server/pty.go", "package server\nimport _ \"github.com/creack/pty\"\n", "5"},
		{"internal/proto/x.go", "package proto\nimport _ \"github.com/charmbracelet/x/ansi\"\n", "6"},
		{"internal/agent/a.go", "package agent\nimport _ \"devtools/internal/proto\"\n", "6"},
		{"internal/ui/u.go", "package ui\nimport _ \"devtools/internal/session\"\n", "6"},
		{"internal/client/chrome/c.go", "package chrome\nimport _ \"devtools/internal/agent\"\n", "7"},
		{"internal/cli/t.go", "package cli\nimport _ \"devtools/internal/proto/prototest\"\n", "8"},
		{"internal/mux/b.go", "package mux\nimport _ \"devtools/internal/backend\"\n", "12"},
		{"internal/hub/m.go", "package hub\nimport _ \"devtools/internal/mux\"\n", "12"},
		{"internal/remote/r.go", "package remote\nimport _ \"devtools/internal/herdr\"\n", "13"},
	} {
		dir := t.TempDir()
		root = dir
		write("go.mod", "module devtools\n")
		write(c.rel, c.src)
		files, err := scan(dir)
		if err != nil {
			t.Fatal(err)
		}
		vs, _ := check(files, rules, nil)
		found := false
		for _, v := range vs {
			if v.Rule == c.rule && v.File == c.rel {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: rule %s not reported; got %v", c.rel, c.rule, vs)
		}
	}
}

// TestNoFileOutsideTheEngineSpellsTheCgoImport is the checkpoint command
// "cgo lives in one package" of K0 (6.7), a git grep -l for the cgo import
// under internal without internal/vt, which must print nothing. It reads
// the text of every file, test files and testdata included, so a comment or
// a string that spells the import fails it as it fails the command. The
// needle is built here so that this file does not spell it either.
func TestNoFileOutsideTheEngineSpellsTheCgoImport(t *testing.T) {
	root := moduleRoot(t)
	needle := []byte("import " + `"` + "C" + `"`)
	internal := filepath.Join(root, "internal")
	engine := filepath.Join(internal, "vt")
	err := filepath.WalkDir(internal, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == engine {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(b, needle) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s spells the cgo import outside internal/vt", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
