package decisioninbox

import (
	"time"

	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
)

// Item is one decision-inbox row, ready to convert 1:1 into the REST
// response DTO (contracts/rest/v1/dtos.schema.json's own DecisionInboxItem).
// Only the fields relevant to Kind are populated -- Go has no tagged-union
// type, and a flat struct with kind-scoped fields (documented per field
// below) matches this codebase's own established precedent for a
// similarly kind-varying shape (e.g. restdtos' own per-artifact-type
// looseness).
type Item struct {
	Kind decisioninbox.Kind

	// Title is a short, human-readable summary of the row -- a PR's own
	// title, a plan's session title, a session's own title, an
	// automation's own name, or an outbox row's own kind.
	Title string

	// EnteredQueueAt is when this row first became a pending decision --
	// SortIndex's own ranking input, and Age/IsStale's own reference
	// point. For a PR row specifically (KindReadyToMerge/KindNeedsReview,
	// and the handoff sub-case of KindAwaitingApproval) this is an
	// APPROXIMATION -- the PR's own GitHub creation time (pr.CreatedAt),
	// not the instant it became assigned/eligible for THIS actor
	// specifically -- see aggregate.go's buildPROpenItem for the full
	// "why", including why this errs in the OPPOSITE direction from the
	// outbox's own similarly-approximated timestamps (§60 review finding
	// C3: this one can only ever UNDER-state how recently a PR became a
	// decision, which means the stale flag below can OVER-fire on an
	// old-but-recently-assigned PR, not fail safely quiet the way the
	// outbox's own approximation does).
	EnteredQueueAt time.Time
	AgeSeconds     int64
	Stale          bool

	// PR fields (KindReadyToMerge / KindNeedsReview -- including the
	// handoff sub-case, which rides KindAwaitingApproval instead, below).
	RepoFullName string
	PRNumber     int
	HTMLURL      string
	HeadSHA      string
	Provenance   *decisioninbox.Provenance
	RiskLabel    string
	CIGreen      bool
	Findings     int
	IsHandoff    bool
	// HasApprovingReview is the PR's own current review-decision fact
	// (ports.OpenPR.HasApprovingReview) -- display only: §16.1 defines
	// ready_to_merge's own "approval" as the deterministic eligibility
	// engine's auto-approval, never a human GitHub review, so this field
	// feeds NO eligibility computation anywhere in this package (see
	// HasChangesRequested's own doc comment on RevalidateForMerge for the
	// one review-decision fact that DOES gate a merge). Populated
	// unconditionally by buildPROpenItem, mirroring CIGreen/Findings/
	// IsHandoff immediately above (§60 review finding A4: this field used
	// to be fetched from GitHub and then read by nothing at all).
	HasApprovingReview bool

	// Plan fields (KindAwaitingApproval, non-handoff).
	PlanID           string
	SessionID        string
	PlanSessionTitle string

	// Session fields (KindNeedsAttention: a failed, resumable session).
	FailureReason string

	// Automation fields (KindNeedsAttention: an auto-paused automation).
	AutomationID    string
	ArtifactSummary string

	// Outbox fields (KindNeedsAttention: a dead-lettered delivery).
	OutboxID   string
	OutboxKind string
	LastError  string
}
