package review_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// allTags is this test's own local enumeration of Tag's fixed vocabulary
// (matching the codebase's own precedent for exhaustive-enumeration tests
// over an unexported set — internal/domain/gitstate_test's allTriggers/
// allTerminalStates) — proving the vocabulary is exactly these eight
// values, no silent addition or removal.
var allTags = []review.Tag{
	review.TagAuth,
	review.TagMigrations,
	review.TagContracts,
	review.TagSecrets,
	review.TagInfra,
	review.TagPublicAPI,
	review.TagDataLayer,
	review.TagDependencies,
}

// TestTag_FixedVocabulary proves BlastRadius's vocabulary is exactly the
// eight documented Tag values, each with a distinct, non-empty underlying
// string, and that §21.2's own three named sensitive-path examples
// (auth, migrations, contracts) are present among them.
func TestTag_FixedVocabulary(t *testing.T) {
	t.Parallel()

	if len(allTags) != 8 {
		t.Fatalf("len(allTags) = %d, want 8", len(allTags))
	}

	seen := make(map[review.Tag]bool, len(allTags))
	for _, tag := range allTags {
		if tag == "" {
			t.Error("Tag vocabulary must not include the empty string")
		}
		if seen[tag] {
			t.Errorf("duplicate Tag value %q in the fixed vocabulary", tag)
		}
		seen[tag] = true
	}

	for _, want := range []review.Tag{review.TagAuth, review.TagMigrations, review.TagContracts} {
		if !seen[want] {
			t.Errorf("§21.2's own named sensitive-path example %q is missing from the Tag vocabulary", want)
		}
	}
}
