package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parsePackage parses every .go file of this directory, tests included.
func parsePackage(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil || len(names) == 0 {
		t.Fatalf("list the package's files: %v (%d files)", err, len(names))
	}
	fset := token.NewFileSet()
	files := make(map[string]*ast.File)
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	return fset, files
}

// TestOnlyCheckMakesAValidPass: the field ok is set in Check and nowhere
// else in the package (N20, 3.4 j). A Pass literal with any element, an
// assignment to a field named ok, or taking its address counts as setting
// it; a literal without elements makes an invalid Pass and is allowed.
func TestOnlyCheckMakesAValidPass(t *testing.T) {
	fset, files := parsePackage(t)
	var inCheck int
	for name, f := range files {
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			owner := "package level"
			if fn != nil {
				owner = fn.Name.Name
				if fn.Recv != nil {
					owner = "method " + owner
				}
			}
			isCheck := fn != nil && fn.Recv == nil && fn.Name.Name == "Check" && !strings.HasSuffix(name, "_test.go")
			ast.Inspect(decl, func(n ast.Node) bool {
				var at token.Pos
				switch x := n.(type) {
				case *ast.CompositeLit:
					if isPassType(x.Type) && len(x.Elts) > 0 {
						at = x.Pos()
					}
				case *ast.AssignStmt:
					for _, lhs := range x.Lhs {
						if isOkField(lhs) {
							at = lhs.Pos()
						}
					}
				case *ast.UnaryExpr:
					if x.Op == token.AND && isOkField(x.X) {
						at = x.Pos()
					}
				case *ast.IncDecStmt:
					if isOkField(x.X) {
						at = x.Pos()
					}
				}
				if at.IsValid() {
					if isCheck {
						inCheck++
					} else {
						t.Errorf("%s: the field ok of Pass is set in %s, not in Check", fset.Position(at), owner)
					}
				}
				return true
			})
		}
	}
	if inCheck != 1 {
		t.Errorf("Check sets the field ok %d times, want once", inCheck)
	}
}

func isPassType(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "Pass"
	case *ast.SelectorExpr:
		return x.Sel.Name == "Pass"
	}
	return false
}

func isOkField(e ast.Expr) bool {
	s, ok := e.(*ast.SelectorExpr)
	return ok && s.Sel.Name == "ok"
}

// TestTheGuardImportsTheStandardLibraryOnly: the package (its non-test
// files) imports nothing of this module and nothing from outside the
// standard library, and nothing that could forge a Pass behind the
// compiler's back (unsafe, reflect).
func TestTheGuardImportsTheStandardLibraryOnly(t *testing.T) {
	_, files := parsePackage(t)
	for name, f := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || first == "devtools" || path == "C" {
				t.Errorf("%s imports %s, which is not the standard library", name, path)
			}
			if path == "unsafe" || path == "reflect" {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
}
