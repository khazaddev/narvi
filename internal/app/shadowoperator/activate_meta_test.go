package shadowoperator

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
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

			// The DIRECTION, not only the call site. Pinning "one caller,
			// in activate.go" is not the guarantee this package needs:
			// what earns its place on demotionsweep's allow-list is that
			// it only ever PROMOTES. A false here would be a demotion
			// with no sweep -- the repo's sandboxes keeping write
			// credentials for the ScmCredentialTTL window -- and it would
			// pass both the analyzer (the package is allowed) and the
			// call-site count (still one, still activate.go).
			if len(call.Args) != 3 {
				t.Errorf("%s: UpsertLiveEgressEnabled called with %d args, want 3 -- this test reads the third to check the direction, and cannot if the signature moved", path, len(call.Args))
				return true
			}
			lit, ok := call.Args[2].(*ast.Ident)
			if !ok || lit.Name != "true" {
				t.Errorf("%s: UpsertLiveEgressEnabled's enabled argument is %s, want the literal true -- this package is on demotionsweep's allow-list ONLY because it promotes and therefore owes no sweep; anything else must pair with repodemotion.Sweep first", path, exprText(fset, call.Args[2]))
			}
			return true
		})
	}

	if len(callSites) != 1 || callSites[0] != "activate.go" {
		t.Fatalf("UpsertLiveEgressEnabled called from %v, want exactly one call site: [activate.go]", callSites)
	}
}

// exprText renders an expression back to source, so a failure names what
// was actually written rather than an AST node address.
func exprText(fset *token.FileSet, e ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, e); err != nil {
		return "<unprintable>"
	}
	return buf.String()
}
