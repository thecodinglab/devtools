package srccheck

import "testing"

// execAllowed are the packages whose non-test files may import os/exec
// (import rule 9, SEC6, N10): ssh, git, the pane's program, the service
// restart, tmux and herdr.
var execAllowed = []string{
	"devtools/internal/remote",
	"devtools/internal/devgit",
	"devtools/internal/pane",
	"devtools/internal/platform",
	"devtools/internal/tmux",
	"devtools/internal/herdr",
}

// onlyAllowedImportOsExec finds a non-test file of a product package outside
// execAllowed that imports os/exec. Test-only packages (rule 8) start
// programs to test them and are not product code.
func onlyAllowedImportOsExec(files []srcFile) []string {
	var vs []string
	for _, f := range files {
		if !f.importsPath("os/exec") || isTestOnly(f.Pkg) {
			continue
		}
		allowed := false
		for _, p := range execAllowed {
			if f.Pkg == p {
				allowed = true
			}
		}
		if !allowed {
			vs = append(vs, f.Rel+": imports os/exec")
		}
	}
	return sorted(vs)
}

// TestOnlyAllowedPackagesImportOsExec (N10): os/exec is imported by the six
// packages that run a program and by no other non-test file. The fzf picker
// of internal/picker and the ssh switch of internal/cli/remote.go are what
// it keeps out.
func TestOnlyAllowedPackagesImportOsExec(t *testing.T) {
	holds(t, onlyAllowedImportOsExec, map[string]string{
		"internal/devgit/git.go":      "package devgit\nimport \"os/exec\"\nvar _ = exec.Command\n",
		"internal/testkit/pty.go":     "package testkit\nimport \"os/exec\"\nvar _ = exec.Command\n",
		"internal/picker/picker.go":   "package picker\nimport \"os/exec\"\nvar _ = exec.Command\n",
		"internal/backend/backend.go": "package backend\nimport \"os\"\nvar _ = os.Args\n",
	}, "internal/picker/picker.go")
}
