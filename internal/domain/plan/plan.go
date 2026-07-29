// Package plan holds plan-mode's own pure domain logic (§8.1/§12.2 item
// 3): given a session's own existing plan rows, which version number a
// newly-created plan should carry, which prior row(s) it must supersede,
// and (audit-fix batch, L10) the single authority for whether a plan may
// move from one Status to another at all. No I/O, no time.Now(), no
// randomness (§11) -- the actual database read/write
// (internal/app/sessionactor/planrecord.go, internal/adapters/inbound/
// httpapi/decideplan.go) happens entirely outside this package; this
// package only decides.
//
// Deliberately its own plain types (ID, Status, Summary), not sqlcgen.Plan/
// sqlcgen.PlanStatus/pgtype.UUID -- domain packages never import adapter
// types (§11's own "no I/O in domain" boundary); callers convert at the
// boundary (see planrecord.go).
package plan

import (
	"errors"
	"fmt"
)

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

// Trigger names the kind of event/command being applied to a plan row --
// mirrors internal/domain/turn.Trigger's own shape exactly (audit-fix
// batch, L10): no Trigger here carries a payload, so Trigger is usable
// directly as a map key with no wrapping struct needed.
//
// Trigger.String() is available for the same structured-logging use turn.
// Trigger's own String() serves.
type Trigger int

const (
	// TriggerApprove is a human (or authorized bot) approving the plan:
	// AwaitingApproval -> Approved. Backed by ApprovePlanIfAwaitingApproval
	// (internal/adapters/outbound/postgres/queries/plans.sql).
	TriggerApprove Trigger = iota
	// TriggerReject is a human (or authorized bot) rejecting the plan:
	// AwaitingApproval -> Rejected. Backed by RejectPlanIfAwaitingApproval.
	TriggerReject
	// TriggerSupersede is a later plan version's own creation superseding
	// this still-undecided row: AwaitingApproval -> Superseded. Backed by
	// SupersedePlan.
	TriggerSupersede
)

var triggerNames = [...]string{"approve", "reject", "supersede"}

func (t Trigger) String() string {
	if t < 0 || int(t) >= len(triggerNames) {
		return fmt.Sprintf("Trigger(%d)", int(t))
	}
	return triggerNames[t]
}

// ErrIllegalTransition is the sentinel error Transition returns for any
// (from, trigger) pair not in the transitions table, wrapped by
// IllegalTransitionError so callers/tests can tell it apart via errors.Is
// while still getting full structured detail via errors.As. Mirrors
// internal/domain/turn's own identical ErrIllegalTransition/
// IllegalTransitionError shape exactly.
var ErrIllegalTransition = errors.New("plan: illegal transition")

// IllegalTransitionError reports a (from, trigger) combination Transition
// rejected because it is not a legal edge in the state machine.
type IllegalTransitionError struct {
	From    Status
	Trigger Trigger
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("plan: illegal transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalTransitionError) Unwrap() error { return ErrIllegalTransition }

// transitions is the explicit Transition(from, trigger) (to, error) table
// §11 requires -- mirroring internal/domain/turn's own transitions table
// shape exactly, just with a much simpler machine: exactly ONE non-terminal
// state (StatusAwaitingApproval) with three possible triggers, each landing
// on one of the three terminal states. Every (from, trigger) edge the
// machine allows is an entry here; anything not listed -- including every
// edge out of the three terminal states, which are absent from this map
// entirely, matching turn's own "terminal states absent from the map"
// convention -- is illegal.
var transitions = map[Status]map[Trigger]Status{
	StatusAwaitingApproval: {
		TriggerApprove:   StatusApproved,
		TriggerReject:    StatusRejected,
		TriggerSupersede: StatusSuperseded,
	},
}

// Transition is the single authority for whether a plan may move from
// status `from` via `trig` -- and, if so, what status it lands in. Every
// illegal combination returns a typed *IllegalTransitionError, never a
// zero-value Status silently accepted. Mirrors internal/domain/turn.
// Transition's own exact shape.
func Transition(from Status, trig Trigger) (Status, error) {
	byTrigger, ok := transitions[from]
	if !ok {
		return "", &IllegalTransitionError{From: from, Trigger: trig}
	}

	to, ok := byTrigger[trig]
	if !ok {
		return "", &IllegalTransitionError{From: from, Trigger: trig}
	}

	return to, nil
}

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
