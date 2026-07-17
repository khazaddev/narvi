// Package notimeliteral implements a static-analysis check enforcing the
// convention from technical plan §5.4 and §11: every timeout/interval lives
// in platform/timeouts.go, so no time.Duration literal selector may appear
// anywhere else in the codebase.
package notimeliteral

import (
	"go/ast"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const doc = `report time.Duration unit literals outside platform/timeouts.go

The convention (technical plan §5.4/§11) is a single timeout hierarchy in
platform/timeouts.go. Report any use of time.Nanosecond, time.Microsecond,
time.Millisecond, time.Second, time.Minute, or time.Hour found outside
internal/platform.`

// Analyzer reports any *ast.SelectorExpr selecting one of the time.Duration
// unit constants (time.Nanosecond ... time.Hour) in a file whose path does
// not contain "/internal/platform/" and is not a _test.go file.
var Analyzer = &analysis.Analyzer{
	Name: "notimeliteral",
	Doc:  doc,
	Run:  run,
}

var forbiddenSelectors = map[string]bool{
	"Nanosecond":  true,
	"Microsecond": true,
	"Millisecond": true,
	"Second":      true,
	"Minute":      true,
	"Hour":        true,
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if skipFile(filename) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "time" {
				return true
			}
			if !forbiddenSelectors[sel.Sel.Name] {
				return true
			}
			pass.Reportf(sel.Pos(),
				"timeout/interval literals belong in platform/timeouts.go (§5.4): found time.%s", sel.Sel.Name)
			return true
		})
	}
	return nil, nil
}

func skipFile(filename string) bool {
	if strings.HasSuffix(filename, "_test.go") {
		return true
	}
	clean := filepath.ToSlash(filename)
	return strings.Contains(clean, "/internal/platform/") ||
		strings.HasPrefix(clean, "internal/platform/")
}
