package autoapproval

import (
	"reflect"
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
)

// TestClassifyChangedPaths_EmptyInput proves a nil/empty input returns
// nil, never a zero-length-but-non-nil slice (ClassifyChangedPaths' own
// doc comment).
func TestClassifyChangedPaths_EmptyInput(t *testing.T) {
	if got := ClassifyChangedPaths(nil); got != nil {
		t.Errorf("ClassifyChangedPaths(nil) = %v, want nil", got)
	}
	if got := ClassifyChangedPaths([]string{}); got != nil {
		t.Errorf("ClassifyChangedPaths([]string{}) = %v, want nil", got)
	}
}

// TestClassifyChangedPaths_SingleTagCases proves every one of the eight
// review.Tag values is reachable from at least one realistic path, and
// that an unrelated path reaches none of them.
func TestClassifyChangedPaths_SingleTagCases(t *testing.T) {
	tests := []struct {
		name string
		path string
		want review.Tag // "" means "expect a nil/empty result"
	}{
		{"migrations directory", "migrations/000072_turns_review_head_sha.up.sql", review.TagMigrations},
		{"rails-style db/migrate", "db/migrate/20240101000000_create_widgets.rb", review.TagMigrations},
		{"auth package segment", "internal/domain/authz/rbac.go", review.TagAuth},
		{"authn segment", "services/authn/middleware.go", review.TagAuth},
		{"auth-prefixed filename", "internal/platform/authtoken.go", review.TagAuth},
		// Deliberately NOT contracts/rest/v1/... -- this repo's own real
		// layout, but its "rest" segment ALSO matches matchesPublicAPI,
		// which would make this case assert two tags under a single-tag
		// table -- see TestClassifyChangedPaths_MultipleTagsFromOnePath
		// for that legitimately-multi-tag shape instead.
		{"contracts directory", "contracts/session-config/v1/session-config.schema.json", review.TagContracts},
		{"secret in filename", "internal/platform/secretstore.go", review.TagSecrets},
		{"credential in filename", "internal/adapters/outbound/githubapi/credential_cache.go", review.TagSecrets},
		{"dotenv file", ".env.production", review.TagSecrets},
		{"pem extension", "certs/server.pem", review.TagSecrets},
		{"ssh private key", "keys/id_rsa", review.TagSecrets},
		{"github actions workflow", ".github/workflows/ci.yml", review.TagInfra},
		{"dockerfile", "Dockerfile", review.TagInfra},
		{"docker-compose file", "docker-compose.dev.yml", review.TagInfra},
		{"terraform file", "infra/main.tf", review.TagInfra},
		{"helm segment", "deploy/helm/values.yaml", review.TagInfra},
		{"api directory", "internal/adapters/inbound/api/router.go", review.TagPublicAPI},
		{"openapi filename", "openapi.yaml", review.TagPublicAPI},
		{"swagger filename", "docs/swagger.json", review.TagPublicAPI},
		{"models directory", "internal/models/user.go", review.TagDataLayer},
		{"repository directory", "internal/repository/userrepo.go", review.TagDataLayer},
		{"go.mod", "go.mod", review.TagDependencies},
		{"go.sum", "go.sum", review.TagDependencies},
		{"package-lock.json", "frontend/package-lock.json", review.TagDependencies},
		{"gemfile canonical case", "Gemfile", review.TagDependencies},
		{"gemfile lowercase still matches (case-insensitive)", "gemfile", review.TagDependencies},
		{"cargo.toml", "rust-service/Cargo.toml", review.TagDependencies},
		{"unrelated go file", "internal/domain/review/tag.go", ""},
		{"unrelated markdown", "README.md", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyChangedPaths([]string{tc.path})
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("ClassifyChangedPaths([%q]) = %v, want empty/nil", tc.path, got)
				}
				return
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("ClassifyChangedPaths([%q]) = %v, want [%s]", tc.path, got, tc.want)
			}
		})
	}
}

// TestClassifyChangedPaths_NoMatchesReturnsNilNotEmptySlice proves that
// when paths is non-empty but touches NO tag at all, the result is nil
// specifically (Go's own `== nil`), never a zero-length-but-non-nil
// slice -- ClassifyChangedPaths' own doc comment states this explicitly;
// TestClassifyChangedPaths_SingleTagCases' own negative cases only check
// len()==0, which a stray empty-but-non-nil slice would ALSO satisfy, so
// this is its own, separate, stronger assertion.
func TestClassifyChangedPaths_NoMatchesReturnsNilNotEmptySlice(t *testing.T) {
	got := ClassifyChangedPaths([]string{"README.md", "docs/CHANGELOG.md"})
	if got != nil {
		t.Errorf("ClassifyChangedPaths([\"README.md\", \"docs/CHANGELOG.md\"]) = %#v, want nil exactly (not an empty-but-non-nil slice)", got)
	}
}

// TestClassifyChangedPaths_CaseInsensitive proves matching ignores case
// for the directory/filename-convention heuristics (this file's own top
// doc comment: "nothing about §21.2's own sensitive-path concern is
// case-sensitive").
func TestClassifyChangedPaths_CaseInsensitive(t *testing.T) {
	got := ClassifyChangedPaths([]string{"MIGRATIONS/000001_Init.SQL"})
	if len(got) != 1 || got[0] != review.TagMigrations {
		t.Errorf("ClassifyChangedPaths([\"MIGRATIONS/000001_Init.SQL\"]) = %v, want [migrations]", got)
	}
}

// TestClassifyChangedPaths_MultipleTagsFromOnePath proves a single path
// can legitimately touch more than one tag at once (e.g. an auth-flavored
// change under contracts/).
func TestClassifyChangedPaths_MultipleTagsFromOnePath(t *testing.T) {
	got := ClassifyChangedPaths([]string{"contracts/auth/schema.json"})
	want := []review.Tag{review.TagAuth, review.TagContracts}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ClassifyChangedPaths([\"contracts/auth/schema.json\"]) = %v, want %v (fixed tagClassifiers order, not input/discovery order)", got, want)
	}
}

// TestClassifyChangedPaths_AggregatesAcrossMultiplePaths proves the result
// is the UNION of every path's own tags, deduplicated, in the classifier's
// own fixed order regardless of which path contributed which tag first.
func TestClassifyChangedPaths_AggregatesAcrossMultiplePaths(t *testing.T) {
	paths := []string{
		"README.md",                     // touches nothing
		"go.mod",                        // dependencies
		"migrations/000001_init.up.sql", // migrations
		"migrations/000002_more.up.sql", // migrations again -- must not duplicate
		"internal/domain/authz/rbac.go", // auth
	}
	got := ClassifyChangedPaths(paths)
	want := []review.Tag{review.TagMigrations, review.TagAuth, review.TagDependencies}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ClassifyChangedPaths(%v) = %v, want %v", paths, got, want)
	}
}

// TestClassifyChangedPaths_AllEightTags proves a diff touching one
// representative path per tag reports every one of the eight, in the
// fixed tagClassifiers order -- a single test asserting the full
// vocabulary is reachable at once (each tag ALSO gets its own isolated
// case above; this one additionally pins the combined/ordered output
// shape).
func TestClassifyChangedPaths_AllEightTags(t *testing.T) {
	paths := []string{
		"migrations/000001_init.up.sql",
		"internal/domain/authz/rbac.go",
		"contracts/rest/v1/dtos.schema.json",
		"internal/platform/secretstore.go",
		"Dockerfile",
		"internal/adapters/inbound/api/router.go",
		"internal/models/user.go",
		"go.mod",
	}
	got := ClassifyChangedPaths(paths)
	want := []review.Tag{
		review.TagMigrations, review.TagAuth, review.TagContracts, review.TagSecrets,
		review.TagInfra, review.TagPublicAPI, review.TagDataLayer, review.TagDependencies,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ClassifyChangedPaths(%v) = %v, want all eight tags %v", paths, got, want)
	}
}

// TestClassifyChangedPaths_DocumentedFalsePositive_ErrsInclusive pins this
// file's own top-doc-comment-documented, DELIBERATE tradeoff: a filename
// that merely happens to start with "auth" (never itself auth/authn/authz
// code) still classifies as TagAuth -- over-inclusion is the accepted,
// safe direction (a human reviews one extra PR) versus under-inclusion
// (a genuinely sensitive change silently auto-merges). This test exists so
// a future reader who "fixes" matchesAuth to stop matching "author.go"
// does so on purpose, having broken a pinned test, not by accident.
func TestClassifyChangedPaths_DocumentedFalsePositive_ErrsInclusive(t *testing.T) {
	got := ClassifyChangedPaths([]string{"internal/domain/book/author.go"})
	if len(got) != 1 || got[0] != review.TagAuth {
		t.Errorf("ClassifyChangedPaths([\"internal/domain/book/author.go\"]) = %v, want [auth] (documented over-inclusive tradeoff)", got)
	}
}

// TestClassifyChangedPaths_BareDBSegmentExcludedFromDataLayer proves a
// bare "db" path segment alone does NOT trigger TagDataLayer (matchesDataLayer's
// own doc comment: too broad/ambiguous, would double-count every Rails-style
// db/migrate/ file as data_layer too) -- only migrations/migrate does, via
// the separate TagMigrations classifier.
func TestClassifyChangedPaths_BareDBSegmentExcludedFromDataLayer(t *testing.T) {
	got := ClassifyChangedPaths([]string{"db/schema.rb"})
	if len(got) != 0 {
		t.Errorf("ClassifyChangedPaths([\"db/schema.rb\"]) = %v, want empty (bare \"db\" segment is not, alone, a data_layer signal)", got)
	}
}

// TestClassifyChangedPaths_Deterministic proves two calls over the same
// (even differently-ordered) input produce byte-identical output --
// ClassifyChangedPaths' own doc comment.
func TestClassifyChangedPaths_Deterministic(t *testing.T) {
	a := []string{"go.mod", "internal/domain/authz/rbac.go", "migrations/1.sql"}
	b := []string{"migrations/1.sql", "go.mod", "internal/domain/authz/rbac.go"}

	got1 := ClassifyChangedPaths(a)
	got2 := ClassifyChangedPaths(a)
	got3 := ClassifyChangedPaths(b)

	if !reflect.DeepEqual(got1, got2) {
		t.Errorf("two calls over the identical input diverged: %v vs %v", got1, got2)
	}
	if !reflect.DeepEqual(got1, got3) {
		t.Errorf("differently-ordered input produced a different result: %v vs %v", got1, got3)
	}
}

// TestClassifyChangedPaths_LeadingSlashTrimmed proves a defensively
// leading-"/"-prefixed path still matches -- normalizeChangedPath's own
// doc comment.
func TestClassifyChangedPaths_LeadingSlashTrimmed(t *testing.T) {
	got := ClassifyChangedPaths([]string{"/migrations/000001_init.up.sql"})
	if len(got) != 1 || got[0] != review.TagMigrations {
		t.Errorf("ClassifyChangedPaths([\"/migrations/000001_init.up.sql\"]) = %v, want [migrations]", got)
	}
}
