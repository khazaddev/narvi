package shadowoperator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestActivate_IsTheOnlyCallerOfUpsertLiveEgressEnabledInThisPackage pins
// activate.go's own top doc comment: this package's promotion-only
// property is structural because Activate is the sole call site of
// UpsertLiveEgressEnabled within it. Mirrors this codebase's own
// established "pinned by a test that it is the sole production call
// site" idiom (§30.6's synthetic-ref constructor) -- a plain AST walk
// over this package's own source files, so a future file added here that
// calls the same method a second time (even with the correct
// argument) fails this test loudly rather than silently widening what
// this package's own doc.go and tools/lint/narvichecks/demotionsweep's
// own allow-list comment both assert.
func TestActivate_IsTheOnlyCallerOfUpsertLiveEgressEnabledInThisPackage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}

	fset := token.NewFileSet()
	var callSites []string
	for _, path := range files {
		if filepath.Ext(path) != ".go" {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "UpsertLiveEgressEnabled" {
				return true
			}
			callSites = append(callSites, path)
			return true
		})
	}

	if len(callSites) != 1 || callSites[0] != "activate.go" {
		t.Fatalf("UpsertLiveEgressEnabled called from %v, want exactly one call site: [activate.go]", callSites)
	}
}
