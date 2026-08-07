package authz

import (
	"errors"
	"fmt"
)

// Actor is who is asking. UserID is carried purely for the caller's own
// convenience (e.g. attaching it to an audit-log row, or to a log line)
// — Authorize's own verdict depends ONLY on Role; two Actors with the
// same Role and a different UserID always get the identical verdict for
// the same (action, resource). Kept a plain string (not pgtype.UUID),
// mirroring internal/domain/plan.ID's own "adapter-independent" precedent
// (§11) — callers convert at the boundary.
type Actor struct {
	UserID string
	Role   Role
}

// Resource is the state-changing command's target, as far as Authorize
// needs to know to render a verdict — deliberately minimal. OwnedOrJoined
// is the ONE bit of context the matrix's own "own/joined" carve-out
// (§13.3 row 2) depends on: true iff the resource is a session the actor
// either created or has joined (a participants row exists) — the CALLER
// resolves this (a plain Postgres lookup, e.g. sessionRow.CreatedBy ==
// actor.UserID || participants.Exists(...)) before ever calling Authorize;
// this package does no I/O of its own (§11) and so cannot answer that
// question itself. Every Action without an own/joined carve-out ignores
// this field entirely — its zero value (false) is always the correct,
// safe thing to pass for those.
type Resource struct {
	OwnedOrJoined bool
}

// ErrForbidden is the sentinel every Authorize rejection wraps — mirrors
// internal/domain/turn.ErrIllegalTransition's own sentinel-plus-detail-
// struct shape exactly, so a caller can either check errors.Is(err,
// ErrForbidden) for a plain yes/no, or errors.As into *ForbiddenError for
// the full (actor, action) detail.
var ErrForbidden = errors.New("authz: forbidden")

// ForbiddenError is the detailed error Authorize returns for any (actor,
// action, resource) it rejects. Actor/Action are carried verbatim (never
// Resource — nothing this package's own callers do with the error needs
// to know OwnedOrJoined, and the (actor, action) pair alone is already
// enough to explain "why" in a log line or an HTTP 403 body).
type ForbiddenError struct {
	Actor  Actor
	Action Action
}

func (e *ForbiddenError) Error() string {
	return fmt.Sprintf("authz: forbidden: role %s may not %s", e.Actor.Role, e.Action)
}

func (e *ForbiddenError) Unwrap() error { return ErrForbidden }

// ErrUnknownAction is returned by Authorize for an Action not present in
// the matrix below — a caller bug (a typo'd/undeclared Action constant),
// never a legitimate "no" verdict, so it is a DISTINCT error, never
// wrapped by ErrForbidden: a caller that only checks errors.Is(err,
// ErrForbidden) to decide "403 vs 500" must not mistake this for a normal
// permission denial.
var ErrUnknownAction = errors.New("authz: unknown action")

// roleSet is a small set-of-Role helper, used only to build the matrix
// table below readably (roles(RoleAdmin, RoleMaintainer) reads as a list,
// not a map literal).
type roleSet map[Role]bool

func roles(rs ...Role) roleSet {
	set := make(roleSet, len(rs))
	for _, r := range rs {
		set[r] = true
	}
	return set
}

// actionRule is one matrix row: allow is the set of roles permitted
// unconditionally; allowIfOwned is an ADDITIONAL set of roles permitted
// only when the caller's own Resource.OwnedOrJoined is true — nil for
// every action with no own/joined carve-out (which is most of them; only
// ActionPromptSession/ActionApprovePlan, per §13.3 row 2, and
// ActionDecideWorkflowStep, per §25.11's "same row as ActionApprovePlan",
// set it).
type actionRule struct {
	allow        roleSet
	allowIfOwned roleSet
}

// matrix is the literal §13.3 permission table, as data — see doc.go for
// the full table and design rationale. Every Action in action.go's
// AllActions has exactly one entry here (proven by
// TestMatrix_CoversEveryAction in authorize_test.go); Authorize's own
// ErrUnknownAction path is a defensive fallback for an Action that is
// somehow NOT in AllActions (should be unreachable in practice, mirrors
// internal/domain/turn's own "default case is unreachable dead-code
// protection" precedent), not something a well-formed caller ever hits.
var matrix = map[Action]actionRule{
	// Row 1: view sessions/analytics -- everyone, including viewer
	// (read-only, but there is no separate write verb for either of
	// these to withhold from a viewer).
	ActionViewSessions:  {allow: roles(RoleAdmin, RoleMaintainer, RoleMember, RoleViewer)},
	ActionViewAnalytics: {allow: roles(RoleAdmin, RoleMaintainer, RoleMember, RoleViewer)},

	// Row 2: create/prompt/approve-plan on own/joined -- viewer excluded
	// entirely; member gated by ownership on the two actions that name an
	// EXISTING resource (prompt, approve), unconditional for create
	// (there is no existing resource to own yet).
	ActionCreateSession: {allow: roles(RoleAdmin, RoleMaintainer, RoleMember)},
	ActionPromptSession: {allow: roles(RoleAdmin, RoleMaintainer), allowIfOwned: roles(RoleMember)},
	ActionApprovePlan:   {allow: roles(RoleAdmin, RoleMaintainer), allowIfOwned: roles(RoleMember)},
	// Step 54 (§25.11): deciding a workflow run's HITL-gated step is
	// own/joined-aware, the SAME row as plan approval by that section's
	// explicit instruction -- see action.go's own doc comment.
	ActionDecideWorkflowStep: {allow: roles(RoleAdmin, RoleMaintainer), allowIfOwned: roles(RoleMember)},
	// Step 58 (§28.5): uploading to a session is own/joined-aware, the
	// SAME row as prompting by that section's own explicit instruction --
	// see action.go's own doc comment.
	ActionUploadToSession: {allow: roles(RoleAdmin, RoleMaintainer), allowIfOwned: roles(RoleMember)},

	// Row 3: stop/resume ANY session -- admin/maintainer only, no member
	// own/joined escape hatch (see action.go's own doc comment on why).
	ActionStopSession:   {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionResumeSession: {allow: roles(RoleAdmin, RoleMaintainer)},

	// Row 4: automations/environments/repo+env secrets -- admin/maintainer.
	// Step 54 (§25.11) adds workflow-definition authoring (an unbound
	// draft) to this SAME row, per that section's explicit "same row as
	// ActionManageAutomations" instruction.
	ActionManageAutomations:         {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionManageEnvironments:        {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionManageRepoSecrets:         {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionManageEnvSecrets:          {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionManageWorkflowDefinitions: {allow: roles(RoleAdmin, RoleMaintainer)},

	// Row 5: review verdicts/re-trigger/auto-approve config --
	// admin/maintainer.
	ActionEditReviewVerdict:    {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionRetriggerReview:      {allow: roles(RoleAdmin, RoleMaintainer)},
	ActionConfigureAutoApprove: {allow: roles(RoleAdmin, RoleMaintainer)},

	// Row 6: integrations/global secrets/template activation/members &
	// roles/sentinel toggle/blockOnHighRisk -- admin only. Step 54
	// (§25.11) adds workflow-binding activation to this SAME row, per
	// that section's explicit "same row as ActionActivatePromptTemplate"
	// instruction (see action.go for why activation is a system-posture
	// change, not row-4 authoring).
	ActionManageIntegrations:       {allow: roles(RoleAdmin)},
	ActionManageGlobalSecrets:      {allow: roles(RoleAdmin)},
	ActionActivatePromptTemplate:   {allow: roles(RoleAdmin)},
	ActionManageMembers:            {allow: roles(RoleAdmin)},
	ActionToggleSentinelAutoFix:    {allow: roles(RoleAdmin)},
	ActionConfigureBlockOnHighRisk: {allow: roles(RoleAdmin)},
	ActionActivateWorkflowBinding:  {allow: roles(RoleAdmin)},
}

// Authorize renders the §13.3 verdict for actor attempting action against
// resource: nil if permitted, *ForbiddenError (wrapping ErrForbidden) if
// not, ErrUnknownAction if action names nothing in the matrix at all.
//
// Every state-changing actor command in this codebase — the session actor
// mailbox, plan approval, and (once later Steps land them) verdict edits,
// automation toggles — calls this identically, so a Slack approval
// renders the exact same verdict a web one would (§13.3's own "channel-
// agnostic" requirement), once a later Step resolves a channel actor to a
// real user_id (§13.2's own auto-linking scope, NOT this Step's).
func Authorize(actor Actor, action Action, resource Resource) error {
	rule, ok := matrix[action]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownAction, action)
	}

	if rule.allow[actor.Role] {
		return nil
	}
	if resource.OwnedOrJoined && rule.allowIfOwned[actor.Role] {
		return nil
	}

	return &ForbiddenError{Actor: actor, Action: action}
}
