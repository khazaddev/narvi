// Package plan holds plan-mode's own pure domain logic (§8.1/§12.2 item
// 3): given a session's own existing plan rows, which version number a
// newly-created plan should carry, and which prior row(s) it must
// supersede. No I/O, no time.Now(), no randomness (§11) -- the actual
// database read/write (internal/app/sessionactor/planrecord.go) happens
// entirely outside this package; this package only decides.
//
// Deliberately its own plain types (ID, Status, Summary), not sqlcgen.Plan/
// sqlcgen.PlanStatus/pgtype.UUID -- domain packages never import adapter
// types (§11's own "no I/O in domain" boundary); callers convert at the
// boundary (see planrecord.go).
package plan

// Status is a plan row's own approval-lifecycle state -- mirrors the
// plan_status Postgres enum (migrations/000034_plan_mode.up.sql) exactly,
// by value, for equality purposes only; this package never imports the
// generated sqlcgen type itself.
type Status string

// The four Status values a plan row can carry, mirroring the plan_status
// Postgres enum exactly: StatusAwaitingApproval is the initial state a
// newly created plan starts in; StatusApproved/StatusRejected are the two
// terminal, permanent verdicts a human (or authorized bot) can render on
// it (first verdict wins -- see ApproveIfAwaitingApproval/
// RejectIfAwaitingApproval); StatusSuperseded is what a later plan
// version's own creation does to any prior awaiting-approval row it
// replaces (ShouldSupersede below).
const (
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusApproved         Status = "approved"
	StatusRejected         Status = "rejected"
	StatusSuperseded       Status = "superseded"
)

// ID is a plan row's identifier, kept as a plain string rather than
// pgtype.UUID/uuid.UUID so this package stays adapter-independent.
type ID string

// Summary is the minimal shape NextVersion/ShouldSupersede need to reason
// about a session's own existing plan rows -- exactly the three columns
// ListPlanSummariesForSession (internal/adapters/outbound/postgres/
// queries/plans.sql) selects.
type Summary struct {
	ID      ID
	Version int
	Status  Status
}

// NextVersion returns the version number a newly created plan row should
// use, given the current set of a session's own plan rows (in any order):
// 1 for the first plan ever (existing empty), otherwise one more than the
// highest existing version -- regardless of that row's own status, since
// version numbers are never reused even for a rejected/superseded plan
// (§12.2 item 3: "v1->v2 history" is a full, permanent record).
func NextVersion(existing []Summary) int {
	next := 1
	for _, s := range existing {
		if s.Version >= next {
			next = s.Version + 1
		}
	}
	return next
}

// ShouldSupersede returns the ids of every existing plan row a newly
// created version must supersede -- only ever a StatusAwaitingApproval
// row. An already-decided row (approved/rejected) is a permanent
// historical record and must NEVER be touched by a later version's
// creation; a row already superseded is likewise left alone (idempotent
// under a defensive re-run). In practice the partial unique index
// (plans_one_awaiting_approval_per_session) guarantees at most one match,
// but this returns every match found rather than assuming that -- the
// caller (planrecord.go) supersedes whatever this names, nothing more.
func ShouldSupersede(existing []Summary) []ID {
	var ids []ID
	for _, s := range existing {
		if s.Status == StatusAwaitingApproval {
			ids = append(ids, s.ID)
		}
	}
	return ids
}
