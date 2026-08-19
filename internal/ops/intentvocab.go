package ops

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// IntentVocabulary is every legal value §18.4's per-session routing
// decision record (IntentDecisionRecord) can actually carry — read
// mechanically from the SAME Go source the runtime code itself resolves
// against, exactly like ScanRegisteredInstruments/ScanRegisteredRoutes
// (this file's own sibling scanners — see routes.go's doc comment for the
// shared "never itself drift" reasoning this repeats over a third,
// different source). A guide command claiming a Surface/Target/Mode/
// (record-level) Source the code cannot actually produce is exactly the
// class of drift this package exists to catch.
type IntentVocabulary struct {
	// Surfaces are sessions.spawn_source's real values (§18.1: "Surface
	// matches sessions.spawn_source's existing enum exactly") — scanned
	// from the sqlcgen SessionSpawnSource* constants, the generated code
	// this codebase's own Postgres adapter resolves against at runtime.
	Surfaces map[string]bool
	// Targets are every classification category's own Target vocabulary
	// (review/request, release/feature, amend/answer — §18.6: "the
	// classifier serves multiple independent categories ... through the
	// SAME contract, rubric, and record shape") — scanned from every
	// Target*-prefixed string constant in internal/domain/intent.
	Targets map[string]bool
	// Modes are IntentDecisionRecord.Mode's real values — scanned from
	// every Mode*-prefixed string constant in internal/domain/intent.
	Modes map[string]bool
	// Sources are IntentDecisionRecord.Source's real values (§18.4: wider
	// than ports.IntentDecision.Source — "explicit" covers a surface that
	// already deterministically knows the answer without ever calling
	// Classify) — scanned from every RecordSource*-prefixed string
	// constant in internal/domain/intent.
	Sources map[string]bool
}

// constPrefixBuckets maps an identifier-name prefix to the IntentVocabulary
// field it feeds — see scanConstPrefixes's own doc comment for how a
// matched constant's own string VALUE (never its identifier name) is what
// actually lands in the bucket.
type constPrefixBuckets map[string]map[string]bool

// ScanIntentVocabulary parses every .go file (skipping _test.go, same
// convention as this package's other two scanners) under intentRoot
// (internal/domain/intent, in production use) for Target/Mode/RecordSource-
// prefixed string constants, and under sqlcgenRoot (internal/adapters/
// outbound/postgres/sqlcgen, in production use) for SessionSpawnSource-
// prefixed ones.
func ScanIntentVocabulary(intentRoot, sqlcgenRoot string) (IntentVocabulary, error) {
	vocab := IntentVocabulary{
		Surfaces: map[string]bool{},
		Targets:  map[string]bool{},
		Modes:    map[string]bool{},
		Sources:  map[string]bool{},
	}

	if err := scanConstPrefixes(intentRoot, constPrefixBuckets{
		"Target":       vocab.Targets,
		"Mode":         vocab.Modes,
		"RecordSource": vocab.Sources,
	}); err != nil {
		return IntentVocabulary{}, err
	}
	if err := scanConstPrefixes(sqlcgenRoot, constPrefixBuckets{
		"SessionSpawnSource": vocab.Surfaces,
	}); err != nil {
		return IntentVocabulary{}, err
	}

	return vocab, nil
}

// scanConstPrefixes walks every .go file under root, and for every
// top-level `const` block's own name=value pair (a *ast.ValueSpec — one
// per line in this codebase's own established one-const-per-line style,
// confirmed against every real const block this scanner targets), checks
// the constant's own IDENTIFIER (never its value) against every prefix in
// buckets; on a match, records the constant's own STRING VALUE into that
// bucket. A constant whose value is not a string literal (an iota-typed
// enum tag, an int constant such as intent.MaxReasoningLength) is silently
// skipped, mirroring ScanRegisteredInstruments's identical "a non-literal
// value is silently skipped, never an error" convention — this scanner
// has no reason to reject a codebase's own unrelated int/iota constants
// living in the same file or even the same const block.
func scanConstPrefixes(root string, buckets constPrefixBuckets) error {
	fset := token.NewFileSet()

	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		file, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return fmt.Errorf("ops: parse %s: %w", p, perr)
		}

		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						// An iota-style spec with no explicit value on
						// this line -- never this codebase's own shape
						// for the constants this scanner targets, but
						// skipped defensively rather than panicking.
						continue
					}
					lit, ok := stringLiteralValue(vs.Values[i])
					if !ok {
						continue
					}
					for prefix, bucket := range buckets {
						if strings.HasPrefix(name.Name, prefix) {
							bucket[lit] = true
						}
					}
				}
			}
		}

		return nil
	})
}
