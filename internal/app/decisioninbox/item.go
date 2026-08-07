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
	// point.
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
