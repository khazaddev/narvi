package upload

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

// This file (deliberately an INTERNAL test, package upload, exactly like
// its sibling placeholders_internal_test.go) implements F1's own "go one
// step further than the finding" mechanism (adversarial review, Step 61):
// TestPlaceholderTokensMatchReviewPackage/TestPlaceholderTokensMatchTurnPackage
// (placeholders_internal_test.go) each require a maintainer to remember,
// by hand, to add a new import-and-assert block whenever some FUTURE
// domain package grows its own secret-substitution placeholder family --
// which is exactly the "remembering" failure mode that let turn's own
// three EPISTEMIC_OUTCOME_TOOL_* literals ship unregistered in the first
// place (they were added to internal/domain/turn, but nobody added a
// matching entry to placeholderTokens here).
//
// # Why a source scan, not a shared registration package
//
// The natural alternative -- have every placeholder family's token
// constants live in (or be re-exported from) one shared place
// placeholderTokens then derives from automatically -- does not fit this
// codebase's layering. Every domain package that currently defines a
// placeholder family declares, in its own doc.go, a hard import ceiling
// review/doc.go: "zero external imports"; turn/doc.go: pure, "this
// package does not import internal/platform... domain has zero external
// dependencies"; upload/doc.go (this package): "imports only
// internal/app/ports... and the standard library". None of the three may
// import a sibling, and none may import a brand-new shared "placeholder
// vocabulary" leaf package either without widening that ceiling -- and
// review.VerdictToolURLPlaceholder/turn.EpistemicOutcomeToolURLPlaceholder
// are already real, exported, referenced-elsewhere names
// (cmd/sandbox-agent, this package's own tests); relocating them would be
// a wider-blast-radius rename this fix does not need to make.
//
// A source scan sidesteps the whole question: it needs no new production
// import anywhere. Test files are ALREADY exempt from the production-import
// ceiling (placeholders_internal_test.go's own doc comment establishes
// this precedent, importing review/turn directly), so a test that reads
// the other packages' own .go SOURCE TEXT (go/parser, never a Go import
// statement) stays entirely within that existing exemption while covering
// every domain package at once, including ones that do not exist yet --
// the next family is discovered wherever it is declared, with no second
// per-package test to remember to add alongside it.
//
// # Verified false-positive-free today
//
// Grepping internal/domain for a literal `"{{` match (a real Go string
// literal, not a comment) finds EXACTLY this package's own six raw
// literals (its three, plus review's three duplicated), review's own
// three real constants (context.go), and turn's own three real constants
// (epistemicpreamble.go) -- nothing else. internal/domain/intent and
// internal/domain/workflow both document a DIFFERENT, lowercase
// "{{variable_name}}"/"{{prompt}}" template syntax (§18.6), but only ever
// as prose INSIDE doc comments, never as an actual matching string
// literal in real code -- go/ast, not a plain text grep, is what makes
// that distinction safe: only *ast.BasicLit STRING nodes are inspected
// below, so a comment merely mentioning a "{{...}}"-shaped example can
// never be mistaken for a real token declaration.
//
// placeholderTokenLiteralPattern deliberately matches only the
// ALL-CAPS-WITH-UNDERSCORES shape every real secret-substitution
// placeholder in this codebase actually uses (e.g.
// "{{UPLOAD_TOOL_BEARER}}") -- distinct by construction from intent/
// workflow's lowercase "{{variable_name}}" template syntax, so even a
// future real (non-comment) use of THAT syntax could never false-positive
// here.
var placeholderTokenLiteralPattern = regexp.MustCompile(`^\{\{[A-Z0-9_]+\}\}$`)

// TestPlaceholderTokens_DiscoversEveryDomainPlaceholderLiteral scans every
// non-test .go file under internal/domain for a string-literal constant
// matching placeholderTokenLiteralPattern, and asserts each one it finds is
// already present in placeholderTokens (prompt.go) -- see this file's own
// top doc comment for the full "why a scan" reasoning. A literal found here
// but absent from placeholderTokens means sanitizeUntrustedField will not
// strip it from untrusted upload metadata, reopening the exact
// bearer-token-exfiltration class F1 closed for turn's own three tokens --
// this test is what makes the NEXT such omission fail CI on its own,
// without anyone needing to remember to extend this file.
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

	// A defensive sanity check on the scan itself, mirroring this
	// codebase's own "meta-vigilance" precedent (e.g. decideplan.go's own
	// domain-transition sanity checks): this package's own six raw
	// literals (BaseURLPlaceholder et al., plus the review duplicates)
	// live in prompt.go, which this scan always walks -- finding zero
	// means the scan itself is broken (wrong directory, pattern typo, ...),
	// not that the codebase suddenly has no placeholder literals at all.
	if len(found) == 0 {
		t.Fatalf("scan of %s found zero \"{{ALL_CAPS}}\"-shaped literals -- the scan itself is almost certainly broken (this package's own tokens in prompt.go should always be found)", domainDir)
	}

	known := make(map[string]bool, len(placeholderTokens))
	for _, tok := range placeholderTokens {
		known[tok] = true
	}
	for lit, files := range found {
		if !known[lit] {
			t.Errorf("found placeholder-shaped literal %q declared in %v, but it is NOT in placeholderTokens (prompt.go) -- sanitizeUntrustedField will not strip it from untrusted upload metadata, silently reopening the secret-substitution exfiltration F1 closed; add it to placeholderTokens", lit, files)
		}
	}
}

// domainRootForTest resolves the absolute path to internal/domain
// regardless of the working directory `go test` happens to run from --
// runtime.Caller(0) gives this file's own compile-time absolute path
// (.../internal/domain/upload/placeholderdrift_internal_test.go); internal/
// domain is two directories up.
func domainRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate internal/domain for the source scan")
	}
	return filepath.Dir(filepath.Dir(thisFile))
}
