package review

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// This file (deliberately an INTERNAL test, package review, exactly like
// its sibling placeholders_internal_test.go, and a byte-for-byte-in-spirit
// port of internal/domain/upload's OWN identical
// placeholderdrift_internal_test.go) implements the SAME "go one step
// further than the finding" mechanism that file's own top doc comment
// describes: TestPlaceholderTokensMatchTurnPackage
// (placeholders_internal_test.go) requires a maintainer to remember, by
// hand, to add a new import-and-assert block whenever some FUTURE domain
// package grows its own secret-substitution placeholder family -- exactly
// the "remembering" failure mode that let upload's own three UPLOAD_TOOL_*
// literals go unregistered here otherwise, and the one that already once
// bit internal/domain/upload for turn's own three EPISTEMIC_OUTCOME_TOOL_*
// literals (upload/prompt.go's own doc comment, "F1").
//
// # Why a source scan, not a shared registration package
//
// The natural alternative -- have every placeholder family's token
// constants live in (or be re-exported from) one shared place
// placeholderTokens then derives from automatically -- does not fit this
// codebase's layering, for the SAME reason upload/placeholderdrift_internal_test.go
// gives: every domain package that currently defines a placeholder family
// declares its own hard import ceiling (review/doc.go: "zero external
// imports"; turn/doc.go: "domain has zero external dependencies";
// upload/doc.go: "imports only internal/app/ports... and the standard
// library"), and none may import a sibling. Here that layering rule is not
// merely inconvenient but load-bearing: internal/domain/upload imports
// internal/app/ports, which itself imports internal/domain/review
// (internal/app/ports/descriptionautofixpayload.go) -- so this package
// importing upload directly, even from a test, would close a genuine
// import cycle (review -> upload -> ports -> review), which
// placeholders_internal_test.go's own doc comment documents `go test`
// correctly refusing to build (verified: attempting exactly that import
// produces "import cycle not allowed in test").
//
// A source scan sidesteps the whole question: it needs no new production
// OR test import of upload (or of any future placeholder-defining package,
// whatever its own layering constraints turn out to be) anywhere. It reads
// the other packages' own .go SOURCE TEXT (go/parser, never a Go import
// statement), so it cannot participate in an import cycle no matter what
// any future package imports -- while still covering every domain package
// at once, including ones that do not exist yet.
//
// # Verified false-positive-free today
//
// Grepping internal/domain for a literal `"{{` match (a real Go string
// literal, not a comment) finds EXACTLY: this package's own four real
// constants (context.go) plus its own six raw literals duplicating turn's/
// upload's (sanitize.go); upload's own three real constants plus upload's
// own six raw literals duplicating review's/turn's (upload/prompt.go); and
// turn's own three real constants (turn/epistemicpreamble.go) -- ten
// DISTINCT literal values in total, each appearing multiple times across
// files, nothing else. internal/domain/intent and internal/domain/workflow
// both document a DIFFERENT, lowercase "{{variable_name}}"/"{{prompt}}"
// template syntax (§18.6), but only ever as prose INSIDE doc comments,
// never as an actual matching string literal in real code -- go/ast, not a
// plain text grep, is what makes that distinction safe: only *ast.BasicLit
// STRING nodes are inspected below, so a comment merely mentioning a
// "{{...}}"-shaped example can never be mistaken for a real token
// declaration.
//
// placeholderTokenLiteralPattern deliberately matches only the
// ALL-CAPS-WITH-UNDERSCORES shape every real secret-substitution
// placeholder in this codebase actually uses (e.g.
// "{{REVIEW_VERDICT_TOOL_BEARER}}") -- distinct by construction from
// intent/workflow's lowercase "{{variable_name}}" template syntax, so even
// a future real (non-comment) use of THAT syntax could never false-positive
// here.
var placeholderTokenLiteralPattern = regexp.MustCompile(`^\{\{[A-Z0-9_]+\}\}$`)

// TestPlaceholderTokens_DiscoversEveryDomainPlaceholderLiteral scans every
// non-test .go file under internal/domain for a string-literal constant
// matching placeholderTokenLiteralPattern, and asserts each one it finds is
// already present in placeholderTokens (sanitize.go) -- see this file's own
// top doc comment for the full "why a scan" reasoning, and for why this is
// the ONLY way this package's own tests can ever check upload's three
// literals stay in sync (a direct import would close an cycle). A literal
// found here but absent from placeholderTokens means
// StripPlaceholderTokens will not strip it from an untrusted diff/title/
// body, reopening the exact bearer-token-exfiltration class the Phase 5
// audit's CRITICAL finding closed -- this test is what makes the NEXT such
// omission fail CI on its own, without anyone needing to remember to
// extend this file.
func TestPlaceholderTokens_DiscoversEveryDomainPlaceholderLiteral(t *testing.T) {
	t.Parallel()

	domainDir := domainRootForTest(t)

	found := map[string][]string{} // literal -> declaring file(s), for the failure message
	fset := token.NewFileSet()
	err := filepath.WalkDir(domainDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, relErr := filepath.Rel(domainDir, path)
		if relErr != nil {
			rel = path
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if placeholderTokenLiteralPattern.MatchString(val) {
				found[val] = append(found[val], rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning internal/domain (%s) for placeholder token literals: %v", domainDir, err)
	}

	// A defensive sanity check on the scan itself, mirroring
	// internal/domain/upload's own identical precedent
	// (placeholderdrift_internal_test.go): this package's own ten literals
	// (four real constants in context.go, six raw duplicates in
	// sanitize.go) live under domainDir, which this scan always walks --
	// finding zero means the scan itself is broken (wrong directory,
	// pattern typo, ...), not that the codebase suddenly has no
	// placeholder literals at all.
	if len(found) == 0 {
		t.Fatalf("scan of %s found zero \"{{ALL_CAPS}}\"-shaped literals -- the scan itself is almost certainly broken (this package's own tokens in sanitize.go/context.go should always be found)", domainDir)
	}

	known := make(map[string]bool, len(placeholderTokens))
	for _, tok := range placeholderTokens {
		known[tok] = true
	}
	for lit, files := range found {
		if !known[lit] {
			t.Errorf("found placeholder-shaped literal %q declared in %v, but it is NOT in placeholderTokens (sanitize.go) -- StripPlaceholderTokens will not strip it from an untrusted diff/title/body, silently reopening the Phase 5 audit's CRITICAL bearer-token-exfiltration finding; add it to placeholderTokens", lit, files)
		}
	}
}

// domainRootForTest resolves the absolute path to internal/domain
// regardless of the working directory `go test` happens to run from --
// runtime.Caller(0) gives this file's own compile-time absolute path
// (.../internal/domain/review/placeholderdrift_internal_test.go); internal/
// domain is two directories up.
func domainRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate internal/domain for the source scan")
	}
	return filepath.Dir(filepath.Dir(thisFile))
}
