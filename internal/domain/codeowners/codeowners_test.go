package codeowners_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/codeowners"
)

func TestParse(t *testing.T) {
	t.Parallel()

	content := `# top comment, ignored
* @global-owner

/docs/ @doctocat @org/docs-team

*.go @gopher docs@example.com

  # indented comment
/generated/
`
	rules := codeowners.Parse(content)

	want := []codeowners.Rule{
		{Pattern: "*", Owners: []codeowners.OwnerRef{{Kind: codeowners.OwnerKindUser, Login: "global-owner"}}, Line: 2},
		{Pattern: "/docs/", Owners: []codeowners.OwnerRef{
			{Kind: codeowners.OwnerKindUser, Login: "doctocat"},
			{Kind: codeowners.OwnerKindTeam, OrgSlug: "org", TeamSlug: "docs-team"},
		}, Line: 4},
		{Pattern: "*.go", Owners: []codeowners.OwnerRef{
			{Kind: codeowners.OwnerKindUser, Login: "gopher"},
			{Kind: codeowners.OwnerKindEmail, Login: "docs@example.com"},
		}, Line: 6},
		{Pattern: "/generated/", Owners: nil, Line: 9},
	}

	if len(rules) != len(want) {
		t.Fatalf("Parse() returned %d rules, want %d: %+v", len(rules), len(want), rules)
	}
	for i, w := range want {
		g := rules[i]
		if g.Pattern != w.Pattern || g.Line != w.Line || len(g.Owners) != len(w.Owners) {
			t.Errorf("rule[%d] = %+v, want %+v", i, g, w)
			continue
		}
		for j, wo := range w.Owners {
			if g.Owners[j] != wo {
				t.Errorf("rule[%d].Owners[%d] = %+v, want %+v", i, j, g.Owners[j], wo)
			}
		}
	}
}

func TestParse_MalformedTokensSkipped(t *testing.T) {
	t.Parallel()

	// "@" alone and "@org/" (empty team slug) are malformed owner tokens
	// -- skipped, never producing a bogus OwnerRef, but the line itself
	// still becomes a Rule (with only its OTHER, valid owner).
	rules := codeowners.Parse("*.md @ @org/ @realowner\n")
	if len(rules) != 1 {
		t.Fatalf("Parse() returned %d rules, want 1: %+v", len(rules), rules)
	}
	if len(rules[0].Owners) != 1 || rules[0].Owners[0].Login != "realowner" {
		t.Errorf("Owners = %+v, want exactly one OwnerRef{Login: realowner}", rules[0].Owners)
	}
}

func TestMatch_LastPatternWins(t *testing.T) {
	t.Parallel()

	rules := codeowners.Parse(`* @global-owner
/apps/web/ @web-team
/apps/web/legacy/ @legacy-owner
`)

	tests := []struct {
		path        string
		wantPattern string
		wantFound   bool
	}{
		{"README.md", "*", true},
		{"apps/web/index.ts", "/apps/web/", true},
		{"apps/web/legacy/old.ts", "/apps/web/legacy/", true},
		{"apps/api/main.go", "*", true}, // only the catch-all matches
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			got, found := codeowners.Compile(rules).Match(tc.path)
			if found != tc.wantFound {
				t.Fatalf("Match(%q) found = %v, want %v", tc.path, found, tc.wantFound)
			}
			if found && got.Pattern != tc.wantPattern {
				t.Errorf("Match(%q).Pattern = %q, want %q", tc.path, got.Pattern, tc.wantPattern)
			}
		})
	}
}

func TestMatch_LastPatternWins_BroadPatternLast(t *testing.T) {
	t.Parallel()

	// The NARROWER, more specific pattern appears FIRST here and the
	// BROADER catch-all appears LAST -- the opposite fixture ordering from
	// TestMatch_LastPatternWins above (and every other fixture in this
	// file), which happens to be broad-first/narrow-last throughout. That
	// shared ordering means a bugged "most-specific/longest-pattern-wins"
	// implementation would satisfy every OTHER test in this file by
	// coincidence alone -- Match must pick the LAST matching rule in FILE
	// order, never the most specific one, and this is the one fixture
	// shape that actually pins that (pairs with
	// C2's own compilePattern fix immediately above).
	rules := codeowners.Parse(`/apps/web/legacy/ @legacy-owner
* @global-owner
`)

	got, found := codeowners.Compile(rules).Match("apps/web/legacy/old.ts")
	if !found {
		t.Fatal("Match() found = false, want true")
	}
	if got.Pattern != "*" {
		t.Errorf("Match().Pattern = %q, want %q (the LAST matching rule, even though an EARLIER rule is more specific)", got.Pattern, "*")
	}
}

func TestMatch_EmptyOwnersStillWins(t *testing.T) {
	t.Parallel()

	// A later pattern with NO owners explicitly un-owns a path a broader
	// earlier pattern already covered -- a real, documented CODEOWNERS
	// idiom (this package's own doc comment) -- Match must still report a
	// match (zero owners), never fall through to the earlier pattern.
	rules := codeowners.Parse(`* @global-owner
/generated/
`)

	got, found := codeowners.Compile(rules).Match("generated/schema.go")
	if !found {
		t.Fatal("Match() found = false, want true (the un-owning pattern still matches)")
	}
	if len(got.Owners) != 0 {
		t.Errorf("Owners = %+v, want empty (explicitly un-owned)", got.Owners)
	}
}

func TestMatch_NoRuleMatches(t *testing.T) {
	t.Parallel()

	rules := codeowners.Parse("/docs/ @doctocat\n")
	if _, found := codeowners.Compile(rules).Match("internal/domain/review/verdict.go"); found {
		t.Error("Match() found = true, want false for a path no rule covers")
	}
}

// TestMatch_PatternSyntax exercises compilePattern's own documented rules
// (anchoring, *, **, ? -- see that function's own doc comment) directly
// through the public Match entry point, one pattern at a time.
func TestMatch_PatternSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"unanchored basename matches at root", "docs", "docs", true},
		{"unanchored basename matches at depth", "docs", "a/b/docs", true},
		{"unanchored basename rejects partial name", "docs", "a/mydocs", false},
		{"leading slash anchors to root", "/docs", "docs", true},
		{"leading slash rejects nested match", "/docs", "a/docs", false},
		{"middle slash anchors to root", "apps/web", "apps/web", true},
		{"middle slash rejects nested match", "apps/web", "a/apps/web", false},
		{"trailing slash matches directory contents", "/apps/", "apps/web/index.ts", true},
		{"trailing-slash-only pattern is unanchored", "apps/", "a/apps/index.ts", true},
		{"star matches within one segment", "*.go", "verdict.go", true},
		{"star does not cross a slash", "*.go", "a/verdict.go", true}, // unanchored basename match at depth
		{"star does not match a deeper segment boundary itself", "a/*.go", "a/b/verdict.go", false},
		{"question mark matches exactly one char", "file?.go", "file1.go", true},
		{"question mark rejects two chars", "file?.go", "file12.go", false},
		{"double-star leading matches any depth", "**/vendor", "a/b/vendor", true},
		{"double-star leading matches zero depth too", "**/vendor", "vendor", true},
		{"double-star trailing matches everything beneath", "apps/**", "apps/web/src/index.ts", true},
		{"double-star middle matches zero segments", "a/**/b", "a/b", true},
		{"double-star middle matches multiple segments", "a/**/b", "a/x/y/b", true},
		{"catch-all star matches everything", "*", "any/deep/path.txt", true},
		// a bare trailing "*" must stay within the
		// one path segment it is written in -- GitHub's own documentation
		// gives "docs/build-app/troubleshooting.md" as the canonical
		// NON-match for "docs/*" (a nested file one directory deeper than
		// the pattern's own single wildcard segment).
		{"bare trailing star matches a direct child", "docs/*", "docs/readme.md", true},
		{"bare trailing star does not cross into a nested directory", "docs/*", "docs/build-app/troubleshooting.md", false},
		// An explicit trailing slash after the star is a DELIBERATE
		// directory-pattern signal (dirOnly), unlike the bare-star case
		// immediately above -- it still gets the "everything beneath"
		// allowance.
		{"trailing slash after a star still matches everything beneath", "docs/*/", "docs/build-app/troubleshooting.md", true},
		// A trailing "**" already means "everything beneath" via its own
		// ".*" translation -- unaffected by the bare-single-star exception.
		{"trailing double-star still matches everything beneath", "docs/**", "docs/build-app/troubleshooting.md", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rules := codeowners.Parse(tc.pattern + " @owner\n")
			_, got := codeowners.Compile(rules).Match(tc.path)
			if got != tc.want {
				t.Errorf("pattern %q vs path %q: matched = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}
