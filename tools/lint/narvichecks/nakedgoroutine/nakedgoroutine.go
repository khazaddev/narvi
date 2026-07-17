// Package nakedgoroutine implements a static-analysis check enforcing the
// convention from technical plan §11 and CLAUDE.md: all concurrency goes
// through errgroup.Group plus context — no naked `go` statements.
package nakedgoroutine

import (
	"go/ast"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const doc = `report naked "go" statements

The convention (technical plan §11) is errgroup+context for all concurrency;
no naked goroutines. errgroup.Group.Go is an ordinary method call, so launch
goroutines through it instead of a bare "go" statement. Unlike the timeout-
literal rule (§5.4/§11), §11 grants no "...and tests" carve-out for this one,
so test files are in scope too.`

// Analyzer reports every *ast.GoStmt found anywhere in the module, including
// test files (§11 states this rule with no test exemption, unlike the
// timeout-literal rule), skipping only the narvichecks tool tree itself.
var Analyzer = &analysis.Analyzer{
	Name: "nakedgoroutine",
	Doc:  doc,
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if skipFile(filename) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			pass.Reportf(goStmt.Pos(),
				"naked `go` statement forbidden — launch goroutines via errgroup.Group.Go (see technical plan §11)")
			return true
		})
	}
	return nil, nil
}

func skipFile(filename string) bool {
	clean := filepath.ToSlash(filename)
	if strings.Contains(clean, "/testdata/") {
		// analysistest fixtures necessarily live under this tree; they
		// represent hypothetical target-repo files, not narvichecks' own
		// source, so they are never exempt.
		return false
	}
	return strings.Contains(clean, "/tools/lint/narvichecks/") ||
		strings.HasPrefix(clean, "tools/lint/narvichecks/")
}
