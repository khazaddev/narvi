package ports

import "github.com/khazaddev/narvi/internal/domain/review"

// DescriptionAutofixPayload is NotificationKindGitHubDescriptionAutofix's
// own outbox payload shape (Step 67, "review digest: description
// adequacy + graduated remediation", §26.2) -- constructed by
// internal/adapters/inbound/httpapi/reviewverdict.go (the only writer,
// inside the SAME transaction as the triggering verdict's own
// review_verdicts insert/github_verdict outbox write) whenever a posted
// verdict's own Digest.ProposedBody is non-blank, and consumed by
// internal/app/outboxworker's own description-autofix notifier (the only
// reader).
//
// Defined HERE, in ports, mirroring SentinelAutoFixPayload's own
// identical "shared, dependency-free layer both the enqueuing and the
// consuming package already import" precedent (that type's own doc
// comment) -- httpapi and outboxworker do not import each other for this
// type either.
//
// Deliberately NOT carrying anything about authorship or the repo's own
// descriptionAutofix flag: §26.2's own central requirement is that BOTH
// checks are enforced SERVER-SIDE AT DELIVERY TIME (§5.2: "never
// prompt-only, never trusting the agent to self-enforce") -- the notifier
// re-derives both, fresh, from Postgres/the artifacts table at Deliver
// time, never from anything this payload claims. Enqueuing this row at
// all is therefore NOT itself a decision that a write will happen -- it
// is only ever a CANDIDATE, re-evaluated independently downstream (mirrors
// VerdictPayload's own "the real GitHub network calls happen entirely at
// delivery time" doc comment, one layer further: here, even the
// eligibility decision itself is deferred, not merely the network call).
//
// DescriptionAdequacy (adversarial-review fix, §26.2/Step 67's own
// follow-up), by contrast, DOES travel here, carried rather than
// re-derived -- a deliberately different treatment than the authorship/
// flag checks above, following this codebase's own general rule: a fact
// whose answer can change between enqueue and delivery (a repo's flag can
// be flipped; a PR's authorship is a live Postgres lookup either way) is
// re-verified fresh at delivery time, but a fact FIXED at the instant the
// verdict was posted travels in the payload, because there is nothing
// live left to re-derive it FROM -- DescriptionAdequacy is exactly that:
// a property of the verdict itself (VerdictInput.Digest.DescriptionAdequacy,
// already validated to one of review's own three closed enum values by
// reviewpost.ValidateVerdictInput before httpapi ever enqueues this row),
// not a property of the PR's current live state a later Postgres/GitHub
// read could refresh. Deliver (descriptionautofix.go) re-asserts this
// value as a THIRD delivery-time check, defense-in-depth against
// httpapi's own enqueue-time gate (reviewverdict.go: only enqueue when
// DescriptionAdequacy != review.DescriptionAdequacyOK) ever regressing --
// never a live re-derivation, since none is possible for this field.
type DescriptionAutofixPayload struct {
	// Owner/Repo/PRNumber identify the pull request this proposed body
	// belongs to -- the SAME review session's own PR identity
	// (github_pr_sessions) VerdictPayload already carries, forwarded
	// verbatim rather than re-derived from RepoFullName a second way.
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	PRNumber int    `json:"prNumber"`
	// ProposedBody is the agent's own rewrite proposal
	// (VerdictInput.Digest.ProposedBody, already non-blank -- one of the
	// TWO preconditions httpapi checks before ever enqueuing this row at
	// all, alongside DescriptionAdequacy below). Composed into the PR's
	// own new body text at delivery time
	// (internal/domain/reviewpost.RenderAutofixBody), alongside a FRESH
	// re-fetch of the PR's own current body -- never a body this payload
	// itself carries, since the agent's own view of the current body may
	// already be stale by delivery time.
	ProposedBody string `json:"proposedBody"`
	// DescriptionAdequacy is the SAME verdict's own
	// Digest.DescriptionAdequacy, forwarded verbatim -- see this type's
	// own doc comment above for why this ONE field travels here rather
	// than being re-derived at delivery time the way authorship/the flag
	// are. Deliver treats anything other than review.
	// DescriptionAdequacyDrift/DescriptionAdequacyMisleading (including
	// this field's own zero value, e.g. an older, pre-this-fix outbox row)
	// as a confirmed "do not rewrite" -- fail-safe, matching every other
	// confirmed-negative check in this notifier.
	DescriptionAdequacy review.DescriptionAdequacy `json:"descriptionAdequacy"`
}
