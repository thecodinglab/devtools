// Package srccheck holds the source-level tests of 7.4 of the plan: they read
// the non-test Go files of the module and fail when what the cutover removed
// comes back (P2-09). It is a test-only package (import rule 8) and has no
// non-test file. Each check is a function from the files to the violations
// it finds; every test runs its check on the module and on a fixture tree
// with exactly one violation, so a check that can never fail is noticed.
package srccheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestMain keeps the tests hermetic (10.1): nothing of the developer's
// terminal, multiplexer, config or home reaches the command line that
// TestRemovedCommandsAreGone asks.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "devtools-srccheck-test")
	if err != nil {
		panic(err)
	}
	for _, name := range []string{
		"DEVTOOLS_SOCKET", "DEVTOOLS_PANE", "DEVTOOLS_TTY", "DEVTOOLS_MULTIPLEXER", "DEVTOOLS_ROOT",
		"DEVTOOLS_TRUST_TERMINAL", "XDG_CONFIG_HOME", "TMUX", "TMUX_PANE", "STY", "ZELLIJ",
		"ZELLIJ_SESSION_NAME", "HERDR_ENV", "HERDR_SOCKET_PATH", "HERDR_BIN_PATH",
	} {
		os.Unsetenv(name)
	}
	os.Setenv("HOME", home)
	os.Setenv("DEVTOOLS_CONFIG", filepath.Join(home, "config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

const mod = "devtools"

// testOnly are the test-only packages of import rule 8: harness code that
// no product package imports. The checks that are about product code leave
// them out.
var testOnly = []string{
	"devtools/internal/proto/prototest",
	"devtools/internal/server/servertest",
	"devtools/internal/client/clienttest",
	"devtools/internal/fleet/fleettest",
	"devtools/internal/testkit",
	"devtools/internal/integration",
	"devtools/internal/e2e",
	"devtools/internal/srccheck",
}

// under reports whether pkg is base or a package below it.
func under(pkg, base string) bool {
	return pkg == base || strings.HasPrefix(pkg, base+"/")
}

func isTestOnly(pkg string) bool {
	for _, p := range testOnly {
		if under(pkg, p) {
			return true
		}
	}
	return false
}

// srcFile is one non-test Go file of the module.
type srcFile struct {
	Pkg  string // import path of its package
	Rel  string // path relative to the module root, with slashes
	Src  []byte
	File *ast.File
	Fset *token.FileSet
}

// imports maps the name a file uses for each import to its path. A blank or
// dot import has no name and is left out.
func (f srcFile) imports() map[string]string {
	names := map[string]string{}
	for _, im := range f.File.Imports {
		path, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if im.Name != nil {
			name = im.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		names[name] = path
	}
	return names
}

// importsPath reports whether the file imports path, under any name.
func (f srcFile) importsPath(path string) bool {
	for _, im := range f.File.Imports {
		if p, err := strconv.Unquote(im.Path.Value); err == nil && p == path {
			return true
		}
	}
	return false
}

// pkgSelector returns the import path and the selected name of x when x is
// pkg.Name with pkg an import of the file; ok is false otherwise.
func (f srcFile) pkgSelector(names map[string]string, x ast.Expr) (path, name string, ok bool) {
	sel, isSel := x.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	id, isID := sel.X.(*ast.Ident)
	if !isID {
		return "", "", false
	}
	path, ok = names[id.Name]
	if !ok || id.Obj != nil {
		// an identifier declared in the file shadows the import
		return "", "", false
	}
	return path, sel.Sel.Name, true
}

func (f srcFile) pos(p token.Pos) string {
	return fmt.Sprintf("%s:%d", f.Rel, f.Fset.Position(p).Line)
}

// moduleRoot is the directory of go.mod above the working directory.
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

// load parses every non-test Go file under root, a module named mod.
// testdata, vendor and hidden directories and nested modules are left out.
func load(t *testing.T, root string) []srcFile {
	t.Helper()
	var files []srcFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		pkg := mod
		if dir := filepath.ToSlash(filepath.Dir(rel)); dir != "." {
			pkg = mod + "/" + dir
		}
		files = append(files, srcFile{Pkg: pkg, Rel: rel, Src: src, File: f, Fset: fset})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// tree is the module the tests run in. A tree of fewer than 100 files means
// the walk went wrong, and every check would pass on nothing.
func tree(t *testing.T) []srcFile {
	t.Helper()
	files := load(t, moduleRoot(t))
	if len(files) < 100 {
		t.Fatalf("read %d non-test Go files of the module, want the whole module", len(files))
	}
	return files
}

// fixture writes files (path relative to the root: source) into a new
// directory with a go.mod of the module and loads them.
func fixture(t *testing.T, files map[string]string) []srcFile {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module devtools\n")
	for rel, content := range files {
		write(rel, content)
	}
	return load(t, root)
}

// check is one source-level check: the violations it finds in files.
type check func(files []srcFile) []string

// holds runs c on the module, where it must find nothing, and on a fixture
// tree with one violation in the file named bad, where it must find exactly
// that one.
func holds(t *testing.T, c check, fixtureFiles map[string]string, bad string) {
	t.Helper()
	for _, v := range c(tree(t)) {
		t.Error(v)
	}
	got := c(fixture(t, fixtureFiles))
	if len(got) != 1 || !strings.HasPrefix(got[0], bad+":") {
		t.Errorf("on the fixture with one violation in %s the check found %q, want exactly that one", bad, got)
	}
}

func sorted(vs []string) []string {
	sort.Strings(vs)
	return vs
}

// stringLit returns the value of a string literal, ok false for anything
// else.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// funcDecls calls fn for every node of the file with the declaration of the
// function it is in, nil at the top level.
func inspectWithFunc(f *ast.File, fn func(n ast.Node, in *ast.FuncDecl)) {
	for _, d := range f.Decls {
		fd, _ := d.(*ast.FuncDecl)
		ast.Inspect(d, func(n ast.Node) bool {
			if n != nil {
				fn(n, fd)
			}
			return true
		})
	}
}

// isMethod reports whether fd is a method named name.
func isMethod(fd *ast.FuncDecl, name string) bool {
	return fd != nil && fd.Recv != nil && fd.Name.Name == name
}

// isFunc reports whether fd is a function (not a method) named one of names.
func isFunc(fd *ast.FuncDecl, names ...string) bool {
	if fd == nil || fd.Recv != nil {
		return false
	}
	for _, n := range names {
		if fd.Name.Name == n {
			return true
		}
	}
	return false
}
