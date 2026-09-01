// Command contractspatch post-processes the Go files go-jsonschema writes
// under contracts/gen/go (see the Makefile's contracts-generate target),
// turning every *defined* pointer type it generates for a nullable property
// into a *type alias* for the same pointer type.
//
// # Why this exists
//
// For a nullable, non-enum property such as
//
//	"lastRunAt": {"type": ["string", "null"], "format": "date-time"}
//
// go-jsonschema (v0.23.x) emits a named type plus a field that uses it:
//
//	type AutomationLastRunAt *time.Time
//	type Automation struct {
//	    LastRunAt AutomationLastRunAt `json:"lastRunAt" ...`
//	}
//
// AutomationLastRunAt is a *defined* type, so its method set is empty and --
// unlike *time.Time itself -- it does not implement json.Unmarshaler. Go
// forbids fixing that in place: a defined type whose underlying type is a
// pointer cannot be a method receiver at all ("invalid receiver type ...
// (pointer or interface type)"), so no hand-written UnmarshalJSON can be
// attached to it. encoding/json therefore never dispatches to
// time.Time.UnmarshalJSON and decoding a populated value fails with
//
//	json: cannot unmarshal string into Go struct field Plain.lastRunAt of type time.Time
//
// A JSON null still decodes fine (no method dispatch is needed to leave a
// pointer nil), which is why the defect stayed invisible: nothing on the Go
// side had ever round-tripped a REAL populated value for one of these
// fields. Marshaling is unaffected -- encoding/json dereferences the pointer
// and finds time.Time's own MarshalJSON on the element type.
//
// Rewriting the declaration to an alias
//
//	type AutomationLastRunAt = *time.Time
//
// makes the name denote *time.Time itself, so the field inherits
// time.Time's Marshaler/Unmarshaler by the ordinary pointer method-set rule.
// The exported name and the wire shape are unchanged, and existing
// conversions such as restdtos.AutomationLastRunAt(&t) stay valid (they
// become identity conversions).
//
// # What gets patched
//
// Only defined pointer types whose pointee is NOT a predeclared basic type.
// A *string/*int/*bool/*float64 pointee can never carry methods of its own,
// so encoding/json handles those natively and the defined-type form is
// harmless; leaving them alone keeps the generated diff minimal. Every other
// pointee -- time.Time today, a generated struct with its own validating
// UnmarshalJSON tomorrow -- is affected by exactly the same method-set rule
// and is aliased.
//
// # Contract
//
// Edits are byte-level insertions of "= " at the pointer type's own start
// offset, so the rest of each generated file stays byte-identical to
// go-jsonschema's output. The rewrite is idempotent (an existing alias is
// skipped), which is what lets `make contracts-check` regenerate and
// diff against the committed tree without spurious drift.
//
// Usage:
//
//	go run ./tools/contractspatch contracts/gen/go/restdtos/restdtos.go ...
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
)

// basicPointees are the predeclared type names that can never have methods
// of their own, and so are already decoded correctly by encoding/json
// through a defined pointer type. Everything else is aliased -- see the
// package comment.
var basicPointees = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true,
}

func main() {
	paths := os.Args[1:]
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: contractspatch FILE ...")
		os.Exit(2)
	}

	for _, path := range paths {
		patched, err := patchFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "contractspatch: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("patched %d nullable pointer type(s) in %s\n", patched, path)
	}
}

// patchFile rewrites every eligible defined pointer type in the file at path
// into a type alias, returning how many declarations it changed. The file is
// rewritten only when there is at least one change.
func patchFile(path string) (int, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}

	// Byte offsets at which to insert "= ", collected in source order.
	var offsets []int
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, s := range gen.Specs {
			spec, ok := s.(*ast.TypeSpec)
			if !ok || spec.Assign.IsValid() { // already an alias: idempotent no-op
				continue
			}
			star, ok := spec.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if ident, ok := star.X.(*ast.Ident); ok && basicPointees[ident.Name] {
				continue
			}
			offsets = append(offsets, fset.Position(star.Pos()).Offset)
		}
	}
	if len(offsets) == 0 {
		return 0, nil
	}

	// Apply from last to first so earlier offsets stay valid.
	sort.Sort(sort.Reverse(sort.IntSlice(offsets)))
	out := src
	for _, off := range offsets {
		if off < 0 || off > len(out) {
			return 0, fmt.Errorf("patch %s: offset %d out of range", path, off)
		}
		patched := make([]byte, 0, len(out)+2)
		patched = append(patched, out[:off]...)
		patched = append(patched, "= "...)
		patched = append(patched, out[off:]...)
		out = patched
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, out, info.Mode().Perm()); err != nil {
		return 0, err
	}
	return len(offsets), nil
}
