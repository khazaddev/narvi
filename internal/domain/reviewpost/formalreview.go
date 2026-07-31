package reviewpost

import "github.com/khazaddev/narvi/internal/domain/review"

// FormalReviewEvent is the GitHub "create a pull request review" event
// (POST /repos/{owner}/{repo}/pulls/{number}/reviews' own "event" field)
// this Step's formal-review gate submits -- §8.2/Step 47's "submitting an
// actual GitHub PR review rather than a comment". Deliberately only two
// of GitHub's three real event values are ever produced here: APPROVE is
// never returned by ComputeFormalReviewEvent below, on purpose -- §21.2's
// criteria-driven auto-approval eligibility engine (Step 58) is the
// future, dedicated machinery for approving a PR; this Step never
// approves anything itself (IMPLEMENTATION_PLAN.md's own Step 47 row:
// "auto-approval is criteria-driven, never label-triggered... the
// eligibility engine itself ships in Step 58").
type FormalReviewEvent string

const (
	// FormalReviewEventComment submits a formal, non-blocking review
	// (GitHub's own "Comment" review type) -- this Step's own default for
	// every verdict that isn't a hard block.
	FormalReviewEventComment FormalReviewEvent = "COMMENT"
	// FormalReviewEventRequestChanges submits a formal, BLOCKING review
	// (GitHub's own "Request changes" review type) -- reserved for
	// Shippable == block, or (when a repo's blockOnHighRisk setting is on)
	// RiskLevel == high.
	FormalReviewEventRequestChanges FormalReviewEvent = "REQUEST_CHANGES"
)

// ComputeFormalReviewEvent decides which FormalReviewEvent the verdict-
// posting tool submits, given the server-computed shippable (review.
// ComputeShippable's own return value -- see BuildVerdict, validate.go),
// the reviewer's own overall risk, and this repo's own blockOnHighRisk
// admin setting (§21.2, an admin, per-repo, strict-boolean flag persisted
// in repo_settings, migrations/000044_repo_settings.up.sql).
//
//   - shippable == block: FormalReviewEventRequestChanges, unconditionally
//     -- review.PremiseFloor's own doc comment already frames Block as "a
//     hard floor breach", so this is always the gate's strictest event
//     regardless of blockOnHighRisk.
//   - blockOnHighRisk AND risk == high (REGARDLESS of what shippable
//     itself computed to): FormalReviewEventRequestChanges. This is the
//     WHOLE effect blockOnHighRisk has -- it never grants a new
//     permission and never changes Shippable's own computation (still
//     exactly review.ComputeShippable's result, untouched); it only picks
//     a stricter EVENT for the SAME already-existing formal-review
//     submission call. This closes a real gap review.RiskLevel's own doc
//     comment names explicitly: "RiskLevel alone (with both floors clean)
//     never reaches ShippableBlock" -- a PR the reviewer itself flagged
//     high-risk, with neither floor raised, would otherwise only ever
//     produce a NON-blocking review; an operator who wants "reviewer says
//     high risk" to be a hard stop, without waiting for §21.2's own future
//     eligibility engine, gets that here.
//   - anything else (shippable is auto or needs_human, and EITHER
//     blockOnHighRisk is off OR risk is not high): FormalReviewEventComment
//     -- a formal review is still submitted (§8.2/Step 47's own "rather
//     than a comment"), it simply does not block.
//
// shippable is expected to already be one of review's three legal
// Shippable consts (BuildVerdict's own contract guarantees this: it is
// always review.ComputeShippable's return value, which itself only ever
// returns one of exactly three legal values) and risk is expected to have
// already passed ValidateVerdictInput -- this function still fails
// conservative (returns the STRICTEST event) for any value it does not
// recognize on either axis, mirroring review's own uniform "unrecognized
// ranks with the worst known legitimate value" policy (review/doc.go),
// purely as defense in depth for a caller that skipped validation.
func ComputeFormalReviewEvent(shippable review.Shippable, risk review.RiskLevel, blockOnHighRisk bool) FormalReviewEvent {
	switch shippable {
	case review.ShippableAuto, review.ShippableNeedsHuman:
		// Falls through to the risk/blockOnHighRisk check below.
	default:
		// review.ShippableBlock, or (defense in depth) any value this
		// function does not recognize at all.
		return FormalReviewEventRequestChanges
	}

	switch risk {
	case review.RiskLevelLow, review.RiskLevelMedium:
		return FormalReviewEventComment
	default:
		// review.RiskLevelHigh, or (defense in depth) any unrecognized
		// value -- mirrors review.baselineFromRisk's own "unrecognized
		// ranks with RiskLevelHigh" precedent.
		if blockOnHighRisk {
			return FormalReviewEventRequestChanges
		}
		return FormalReviewEventComment
	}
}
