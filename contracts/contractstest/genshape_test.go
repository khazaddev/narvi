package contractstest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// genGoFiles are the go-jsonschema outputs, relative to this package's own
// directory. Kept in sync with the Makefile's contracts-generate target.
var genGoFiles = []string{
	"../gen/go/sandboxws/sandboxws.go",
	"../gen/go/clientws/clientws.go",
	"../gen/go/sessionconfig/sessionconfig.go",
	"../gen/go/restdtos/restdtos.go",
}

// basicPointees mirrors ./tools/contractspatch's own list: the predeclared
// types that can never carry methods, and so decode correctly even through
// a defined pointer type.
var basicPointees = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true,
}

// TestGeneratedNullablePointerTypesAreAliases pins, across every generated
// Go file at once, the invariant ./tools/contractspatch establishes: a
// nullable property whose pointee is not a predeclared basic type must be
// declared as an ALIAS, never as a defined type.
//
// A defined type whose underlying type is a pointer has an empty method set
// and cannot be given one (Go rejects a pointer receiver base type), so it
// never inherits the pointee's json.Unmarshaler. For *time.Time that means
// a populated value fails to decode outright; for a generated struct with
// its own validating UnmarshalJSON it is worse -- validation is silently
// skipped. Either way a JSON null still decodes fine, so the per-DTO
// round-trip tests only catch it where a fixture happens to populate the
// field.
//
// This test needs no such fixture: it fails for EVERY affected declaration,
// including one added by a schema change nobody wrote a round-trip test for
// -- which is the case that produced the original defect. A failure here
// almost always means contracts/gen was regenerated without the patch step;
// re-run `make contracts-generate`.
func TestGeneratedNullablePointerTypesAreAliases(t *testing.T) {
	for _, rel := range genGoFiles {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			src, err := os.ReadFile(rel)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", rel, err)
			}

			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, s := range gen.Specs {
					spec, ok := s.(*ast.TypeSpec)
					if !ok || spec.Assign.IsValid() {
						continue // an alias: correct
					}
					star, ok := spec.Type.(*ast.StarExpr)
					if !ok {
						continue
					}
					if ident, ok := star.X.(*ast.Ident); ok && basicPointees[ident.Name] {
						continue // pointee can never carry methods
					}
					t.Errorf("%s: type %s is a DEFINED pointer type, not an alias -- "+
						"it cannot inherit its pointee's json.Unmarshaler, so a populated "+
						"value will fail to decode (or skip validation). Run `make contracts-generate`.",
						fset.Position(spec.Pos()), spec.Name.Name)
				}
			}
		})
	}
}
