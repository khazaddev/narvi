// Package decisioninbox holds the decision inbox's own pure decision
// functions (Step 60, "decision inbox: read model + API", §16) -- item
// taxonomy classification helpers, ranking, staleness, assignment-
// provenance rendering, and the decision-latency median. No I/O, no
// time.Now(), no randomness (CLAUDE.md/§11): every function here is a pure
// transform of already-fetched data, exactly like internal/domain/review's
// own Shippable computation. The actual READ MODEL -- aggregating Postgres
// (plans, review sessions, sessions, automations, outbox) plus live
// SourceControl data -- lives one layer up, in internal/app/decisioninbox,
// which calls these functions but never duplicates their logic.
//
// This package originally also held an interim, label-driven auto-
// approval-eligibility heuristic (eligibility.go) — deleted whole by Step
// 62 (§21.2), which replaced it with a real, deterministic engine living
// in its own sibling package, internal/domain/autoapproval, since that
// engine's own criteria (verdict-driven, not label-driven) no longer have
// anything to do with THIS package's own item-taxonomy/ranking/staleness
// concerns. internal/app/decisioninbox.computeRealEligibility is the one
// app-layer call site that bridges the two.
//
// # Why this package exists at all, distinct from internal/app/decisioninbox
//
// Every OTHER read model in this codebase computes its own classification
// logic inline, at the app layer, because that logic is trivial (a single
// status comparison). This one is not: the four-way taxonomy (§16.1),
// decision-cost ranking, and staleness are all real decision logic worth
// testing in isolation, exhaustively, without spinning up Postgres or a
// fake GitHub server -- the same reason domain/review carved Shippable out
// of reviewpost's own posting-endpoint code.
package decisioninbox

// Kind is one of the four §16.1 item taxonomy values -- a decision inbox
// row is always EXACTLY one of these, never more than one, never zero
// (every row that does not match one of the first three inclusion
// criteria and is not the §17 structural exclusion falls, if anywhere,
// into NeedsAttention -- see the app-layer aggregator's own doc comment
// for the full per-kind inclusion criteria this type intentionally does
// NOT restate here, since deciding WHETHER a given PR/plan/session/
// automation/outbox row qualifies for a kind at all requires the
// aggregated data this pure package never sees).
type Kind string

const (
	// KindReadyToMerge is an open PR authored by a platform session,
	// auto-approved (internal/domain/autoapproval.ComputeEligible, Step
	// 62/§21.2), CI green at head, and assigned to the user.
	KindReadyToMerge Kind = "ready_to_merge"
	// KindNeedsReview is a PR where the user is requested reviewer/code
	// owner and the verdict is >= medium risk or a formal review is
	// gated; also release cuts with manifest flags (§15).
	KindNeedsReview Kind = "needs_review"
	// KindAwaitingApproval is a plan-mode plan the user is entitled to
	// approve, or a handoff item (§14.4) sitting in the engineering queue.
	KindAwaitingApproval Kind = "awaiting_approval"
	// KindNeedsAttention is a failed-with-resume-available session, an
	// auto-paused automation, or a dead-lettered outbox delivery -- ADMIN
	// ONLY (§16.1's own parenthetical).
	KindNeedsAttention Kind = "needs_attention"
)

// AllKinds is every recognized Kind, in the SAME order §16.1 lists them
// and the mockup (docs/design/mockups.html) renders them section-by-
// section -- exported so a caller (both this package's own exhaustive
// tests and the app-layer aggregator, when it needs to iterate every
// section in display order) never hand-maintains a second list.
var AllKinds = []Kind{KindReadyToMerge, KindNeedsReview, KindAwaitingApproval, KindNeedsAttention}

// decisionCost ranks each Kind by how cheap the pending decision itself
// is -- §16.1: "Ranking: by decision cost then age -- quick confirmations
// (ready_to_merge) first". A LOWER value sorts first. This is deliberately
// NOT the same order as AllKinds' own general "how §16.1 documents them"
// order by coincidence alone -- it happens to match here because §16.1
// already lists them cost-ascending, but DecisionCost is the one function
// ranking.go's own sort actually calls, never AllKinds' own slice
// position, so a future reordering of AllKinds (e.g. for a different
// display purpose) could never silently change ranking behavior too.
var decisionCost = map[Kind]int{
	KindReadyToMerge:     0, // a single "Merge" click -- the cheapest possible confirmation.
	KindNeedsReview:      1, // requires reading a diff/manifest and forming a judgment.
	KindAwaitingApproval: 2, // approve/reject a plan, or triage a handoff -- a design decision.
	KindNeedsAttention:   3, // recovery/operational triage -- the least routine, priced highest.
}

// DecisionCost returns kind's own ranking weight (decisionCost above). An
// unrecognized Kind (should be unreachable -- every producer in this
// package's own caller constructs a Kind from one of the four consts
// above) fails conservative to the HIGHEST cost rather than panicking or
// silently sorting first, mirroring this codebase's own established
// "unrecognized enum value ranks as the least permissive/most attention-
// worthy option" convention (e.g. review.baselineFromRisk's identical
// choice for an unrecognized RiskLevel).
func DecisionCost(kind Kind) int {
	if c, ok := decisionCost[kind]; ok {
		return c
	}
	return len(decisionCost)
}
