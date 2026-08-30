// This file (summary.go) is the pure half of this package: grouping
// already-fetched rows into the shape §30.6 asks for -- "summarise
// what was suppressed ... grouped so an operator can see the shape at a
// glance". No I/O here at all (§11's own "no I/O in domain-shaped
// logic" convention, applied to this app-layer package by choice, not
// obligation) -- readmodel.go does every store call and hands this file
// plain data.

package shadowoperator

import (
	"strings"
	"time"
)

// Category labels are deliberately descriptive prose, not a closed enum
// on the wire -- an operator reads them, nothing branches on them. New
// operations/kinds fall into "Other suppressed ..." rather than a build
// failure, matching outboxworker.classifyNotifiers' own EXHAUSTIVE
// posture being the wrong model here: this is a display grouping, not a
// safety gate (unlike that map, an unrecognized value here still shows
// up, just under a catch-all label -- nothing is ever silently dropped
// from the count).
const (
	CategoryPullRequests      = "Pull requests"
	CategoryBranches          = "Branches"
	CategoryMerges            = "Merges"
	CategoryFileContentWrites = "File content writes"
	CategoryPushes            = "Pushes"
	CategoryGitHubNotices     = "GitHub comments & other notifications"
	CategorySlackActivity     = "Slack activity"
	CategoryLinearActivity    = "Linear activity"
	CategorySentinelAutoFix   = "Sentinel auto-fix"
	CategoryCredentialSubst   = "Credential substitutions"
	CategoryOtherSCMWrite     = "Other suppressed writes"
	CategoryOtherNotification = "Other suppressed notifications"
)

// Entry is one suppressed effect, from either half of the §30.6 UNION,
// reduced to what an operator's summary view needs -- see doc.go's own
// "never renders customer content" section for why spec_json/
// heavy_content never reach this type.
type Entry struct {
	// Source is "scm_write" (a shadow_scm_writes row) or "outbox" (a
	// ledger-terminal outbox row) -- which half of the UNION this came
	// from, for a reader who wants to distinguish them.
	Source string
	// Operation is shadow_scm_writes.operation ("create_pr", "http_post",
	// ...) for a scm_write entry, or outbox.kind ("github_verdict", ...)
	// for an outbox entry.
	Operation string
	// Category is the display bucket this entry counts toward -- one of
	// the Category* constants above.
	Category string
	// Target is the specific thing acted on (a branch, a PR number, a
	// path) -- empty for an outbox entry, which carries no single target
	// in this read model. Attacker/customer-influenceable text: a
	// renderer must treat it exactly like any other repo-derived string,
	// never as trusted markup.
	Target string
	// SessionID is the session that would have produced this effect,
	// empty if none (a shadow_scm_writes row's session_id is ON DELETE
	// SET NULL, migrations/000102's own doc comment) -- the "links into
	// the sessions that produced them" §30.6 asks for.
	SessionID string
	CreatedAt time.Time
}

// Category is one bucket in the ledger summary -- a label and how many
// Entries counted toward it, ordered by descending count (ties broken by
// label) so the "shape at a glance" reads biggest-first.
type Category struct {
	Label string
	Count int
}

// Summary is the whole read-model response for one repository.
type Summary struct {
	RepoFullName string

	// LiveEgressEnabled/LiveEgressPromotedAt mirror repo_settings' own
	// current state (§30.8) -- read alongside the ledger so the operator
	// sees the flag and the evidence for its current setting together.
	LiveEgressEnabled    bool
	LiveEgressPromotedAt *time.Time

	// PendingShadowEraCount is §30.8's own "unhandled shadow-era row"
	// count -- outbox rows this deployment stamped suppressed_in_shadow
	// at enqueue that have not yet reached a ledger-terminal state.
	// Activate refuses while this is nonzero; see activate.go.
	PendingShadowEraCount int

	Categories []Category
	TotalCount int

	// LLMSpendComputed/LLMSpendUsd mirror RepoSettings.
	// contradictionRateComputed/contradictionRatePercent's own "no figure
	// available yet" sentinel discipline -- Computed=false means no
	// session naming this repository has recorded a priced turn, never a
	// fabricated $0.00 (§30.1: LLM spend is surfaced, never suppressed).
	LLMSpendComputed bool
	LLMSpendUsd      float64

	// Entries is newest-first, capped at the caller's own limit -- see
	// ListShadowSuppressedOutboxWithSessionRepos/ShadowSCMWriteStore.
	// ListForRepo's own doc comments for why this is a floor, not a
	// promise of completeness, on a deployment large enough to reach it.
	Entries []Entry
}

// categoryForSCMOperation maps a shadow_scm_writes.operation value (the
// literal strings internal/app/shadowscm/decorator.go,
// internal/app/shadowslack, internal/app/shadowlinear,
// internal/app/readonlymint/mint.go, internal/app/outboxworker/
// sentinelautofix.go, and internal/adapters/outbound/githubapi/
// shadowgate.go's own Operation fields write) to a display Category.
func categoryForSCMOperation(op string) string {
	switch op {
	case "create_pr", "update_pr_body", "register_pr_stack":
		return CategoryPullRequests
	case "create_branch":
		return CategoryBranches
	case "merge_pr":
		return CategoryMerges
	case "update_file_content":
		return CategoryFileContentWrites
	case "push":
		return CategoryPushes
	case "sentinel_auto_fix":
		return CategorySentinelAutoFix
	case "scm_credential_mint_refused", "scm_credential_substituted":
		return CategoryCredentialSubst
	case "slack_post_ack", "slack_post_ephemeral", "slack_post_identity_link_notice", "slack_update_message", "slack_open_view":
		return CategorySlackActivity
	case "linear_create_thought_activity", "linear_create_response_activity":
		return CategoryLinearActivity
	}
	// shadowgate.go's own Transport spec records "http_" + verb for every
	// mutating request the typed layer never saw -- overwhelmingly
	// PostIssueComment/CreateReview/AddLabels/RemoveLabel/
	// CreateCommitStatus (§30.1's own 5 concrete-adapter-only methods),
	// which is why this bucket is labeled for comments/notifications
	// rather than left as raw HTTP verbs an operator would have to
	// decode.
	if strings.HasPrefix(op, "http_") {
		return CategoryGitHubNotices
	}
	return CategoryOtherSCMWrite
}

// categoryForOutboxKind maps an outbox.kind value (a ports.
// NotificationKind string) to a display Category, by provider-name
// prefix -- ports/notifier.go's own established naming convention
// ("github_verdict", "slack_plan_approval", "linear_progress", ...),
// the SAME fragility GetLatestOutboxEntryByKindPrefix's own doc comment
// already names ("a kind that does not literally begin with its own
// provider's name ... silently never matches"): a kind this misses
// simply falls into the "Other" catch-all rather than being dropped, so
// the fragility costs granularity, never completeness.
func categoryForOutboxKind(kind string) string {
	switch {
	case strings.HasPrefix(kind, "github"):
		return CategoryGitHubNotices
	case strings.HasPrefix(kind, "slack"):
		return CategorySlackActivity
	case strings.HasPrefix(kind, "linear"):
		return CategoryLinearActivity
	default:
		return CategoryOtherNotification
	}
}

// summarizeCategories groups entries into Categories, ordered by
// descending count then label -- pure, and the one piece of this
// package worth testing without a database.
func summarizeCategories(entries []Entry) []Category {
	counts := make(map[string]int)
	var order []string
	for _, e := range entries {
		if _, seen := counts[e.Category]; !seen {
			order = append(order, e.Category)
		}
		counts[e.Category]++
	}
	categories := make([]Category, 0, len(order))
	for _, label := range order {
		categories = append(categories, Category{Label: label, Count: counts[label]})
	}
	// Stable, deterministic order: descending count, then label
	// alphabetically -- a plain insertion sort is fine, this list is
	// never more than a dozen-odd categories long.
	for i := 1; i < len(categories); i++ {
		for j := i; j > 0; j-- {
			a, b := categories[j-1], categories[j]
			if a.Count > b.Count || (a.Count == b.Count && a.Label <= b.Label) {
				break
			}
			categories[j-1], categories[j] = categories[j], categories[j-1]
		}
	}
	return categories
}
