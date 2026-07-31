package github

// Step 47's ("server-side verdict", §8.2/§5.2) own load-bearing proof: the
// re-run phrasing internal/domain/reviewpost.RerunGuidance renders
// server-side must actually be recognized by THIS package's own real
// mention detector (compileMentionPattern, payload.go) -- §5.2's own
// words: "that exact phrasing must be recognizable by the intent
// classifier's deterministic fail-open fallback... not only by its
// model-based path." A review session exists only via THIS package's own
// ingress (github_pr_sessions is the sole mechanism creating one), and
// this package's own mention detection is ALREADY fully deterministic,
// never touching the classifier's model-based path at all (coalesce.go's
// own doc comment: "there is no model-based path for either trigger to
// depend on in the first place") -- so proving RerunGuidance's own output
// matches compileMentionPattern is the concrete, executable version of
// that requirement, not merely an assertion by inspection.

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestRerunGuidance_MatchesRealMentionPattern proves RerunGuidance's own
// rendered text is matched by compileMentionPattern for a representative
// set of real-looking bot handles, including one containing a hyphen
// (compileMentionPattern's own doc comment calls out hyphenated handles as
// a case its negative-class boundary check specifically has to get right).
// If this test ever fails, RerunGuidance's own rendering drifted out of
// sync with this package's real detector -- exactly the invisible-until-
// the-classifier-degrades mismatch §5.2 warns about, caught here instead,
// deterministically, in CI.
func TestRerunGuidance_MatchesRealMentionPattern(t *testing.T) {
	handles := []string{"narvi-bot", "narvi", "acme-review-bot", "n"}

	for _, handle := range handles {
		t.Run(handle, func(t *testing.T) {
			re := compileMentionPattern(handle)
			guidance := reviewpost.RerunGuidance(handle)

			if !re.MatchString(guidance) {
				t.Errorf("compileMentionPattern(%q) does not match RerunGuidance(%q) = %q -- the server-rendered re-run phrasing is NOT recognized by this package's own deterministic mention detector", handle, handle, guidance)
			}
		})
	}
}

// TestRerunGuidance_NeverFalsePositivesOnADifferentHandle proves the
// converse: guidance rendered for one handle must NOT accidentally match
// a DIFFERENT deployment's own mention pattern -- otherwise a multi-tenant
// mixup (unlikely, but cheap to rule out) could make one bot react to text
// meant to summon a different one.
func TestRerunGuidance_NeverFalsePositivesOnADifferentHandle(t *testing.T) {
	guidance := reviewpost.RerunGuidance("narvi-bot")
	re := compileMentionPattern("some-other-bot")

	if re.MatchString(guidance) {
		t.Errorf("compileMentionPattern(%q) unexpectedly matched guidance rendered for a different handle: %q", "some-other-bot", guidance)
	}
}
