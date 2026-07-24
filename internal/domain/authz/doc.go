// Package authz implements Step 39's ("identities + full RBAC", §13.3)
// own central deliverable: a table-driven Authorize(actor, action,
// resource) error the matrix below lives in as DATA, with exhaustive
// tests over every (role, action) pair — no I/O, no time.Now(), no
// randomness (§11), so this package is safe to call from anywhere
// (an HTTP handler, a session actor mailbox command, a future Slack/
// Linear entry point once Step 39's own auto-linking resolves a real
// user_id for those channels) without dragging in any adapter dependency.
//
// # The matrix (§13.3, verbatim)
//
//	| Permission                                                 | admin | maintainer | member | viewer |
//	|------------------------------------------------------------|-------|------------|--------|--------|
//	| View sessions / analytics                                  |  ✓    |     ✓      |   ✓    | ✓ (ro) |
//	| Create sessions, prompt, approve plans on own/joined        |  ✓    |     ✓      |   ✓    |   —    |
//	| Stop/resume ANY session; approve ANY plan                   |  ✓    |     ✓      |   —    |   —    |
//	| Manage automations, environments, repo/env secrets           |  ✓    |     ✓      |   —    |   —    |
//	| Edit review verdicts; re-trigger reviews; auto-approve cfg   |  ✓    |     ✓      |   —    |   —    |
//	| Integrations, global secrets, template activation, members  |  ✓    |     —      |   —    |   —    |
//	  & roles, sentinel auto-fix toggle
//
// # Design: one map, not two parallel checks
//
// Every row above collapses to exactly one of two shapes: a fixed set of
// roles allowed unconditionally (allow), plus — for the two actions the
// spec's own "own/joined" carve-out actually names (approving a plan,
// prompting/creating a turn on an existing session) — an ADDITIONAL set
// of roles allowed only when the caller also proves resource.OwnedOrJoined
// (allowIfOwned). "Stop/resume ANY session" has no own/joined carve-out
// at all for member — the matrix's own row 3 never mentions a member
// escape hatch the way row 2 does for create/prompt/approve, so a member
// who created their own session still cannot stop/resume it; only
// admin/maintainer can. This asymmetry is deliberate, not a gap — see
// action.go's own doc comments on ActionStopSession/ActionResumeSession.
//
// # What resource ownership resolution is NOT this package's job
//
// Resource.OwnedOrJoined is a single pre-computed bool the CALLER derives
// (session.CreatedBy == actor, or a participants row exists) before ever
// calling Authorize — exactly mirroring how internal/domain/plan.Summary
// is a plain, already-fetched snapshot handed in by its own caller, never
// something this package queries for itself. Domain purity (§11) forbids
// this package from touching Postgres to answer that question itself.
//
// # Callers (as of this Step; more arrive as later Steps land the
// features these Actions already reserve a name for)
//
//   - internal/adapters/inbound/httpapi's CreateSession (ActionCreateSession),
//     CreateTurn (ActionPromptSession), ApprovePlan/RejectPlan via
//     canActOnPlan (ActionApprovePlan) — see planauthz.go's own doc
//     comment for why that predicate is now a thin Authorize wrapper,
//     not a second, parallel rule set.
//   - internal/adapters/inbound/httpapi's members.go (ActionManageMembers):
//     ListMembers, UpdateMemberRole, LinkMemberIdentity,
//     UnlinkMemberIdentity, ListAuditLog — every one of the members API's
//     own endpoints (§13.2/§13.3's own "members API" deliverable).
//   - Every other Action below (automations, environments, secrets,
//     review verdicts, integrations, sentinel auto-fix) has NO caller
//     yet — those features don't exist as of this Step (Phase 3 ingress
//     work only; automations/reviews/sentinel land in later Phase 3.5/6
//     Steps per docs/IMPLEMENTATION_PLAN.md) — they are defined here now
//     so this package's own shape never has to change out from under
//     them: a future Step calls Authorize with the right
//     Action constant and gets the exact matrix row §13.3 already
//     specifies, with zero changes to this package.
package authz
