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
//	| Create sessions, prompt, approve plans, decide workflow     |  ✓    |     ✓      |   ✓    |   —    |
//	  steps (§25.11/Step 54) on own/joined
//	| Stop/resume ANY session; approve ANY plan                   |  ✓    |     ✓      |   —    |   —    |
//	| Manage automations, environments, repo/env secrets,          |  ✓    |     ✓      |   —    |   —    |
//	  workflow definitions (§25.11/Step 54)
//	| Edit review verdicts; re-trigger reviews; auto-approve cfg   |  ✓    |     ✓      |   —    |   —    |
//	| Integrations, global secrets, template activation, members  |  ✓    |     —      |   —    |   —    |
//	  & roles, sentinel auto-fix toggle, blockOnHighRisk (§8.2/Step 47),
//	  workflow binding activation (§25.11/Step 54)
//
// # Design: one map, not two parallel checks
//
// Every row above collapses to exactly one of two shapes: a fixed set of
// roles allowed unconditionally (allow), plus — for the three actions an
// "own/joined" carve-out is actually specified for (approving a plan and
// prompting/creating a turn on an existing session, §13.3 row 2;
// deciding a workflow run's HITL step, §25.11's "same row as
// ActionApprovePlan") — an ADDITIONAL set of roles allowed only when the
// caller also proves resource.OwnedOrJoined (allowIfOwned). "Stop/resume ANY session" has no own/joined carve-out
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
//   - internal/adapters/inbound/httpapi's reposettings.go
//     (ActionConfigureBlockOnHighRisk, §8.2/Step 47): GetRepoSettings/
//     PutRepoSettings, gating admin-only read/write of a repo's own
//     blockOnHighRisk formal-review-gate policy flag.
//   - internal/adapters/inbound/httpapi's cloudidentitybindings.go
//     (ActionManageCloudIdentityBindings, Step 73a, §27.3): binding
//     create/list/update/delete, both environment and global scope.
//   - internal/adapters/inbound/httpapi's cloudidentitykeys.go
//     (ActionManageCloudIdentityKeys, Step 73a, §27.3): the admin-only
//     signing-key rotation trigger.
//   - Every other Action below (automations, environments, secrets,
//     review verdicts, integrations, sentinel auto-fix) has NO caller
//     yet — those features don't exist as of this Step (Phase 3 ingress
//     work only; automations/reviews/sentinel land in later Phase 3.5/6
//     Steps per docs/IMPLEMENTATION_PLAN.md) — they are defined here now
//     so this package's own shape never has to change out from under
//     them: a future Step calls Authorize with the right
//     Action constant and gets the exact matrix row §13.3 already
//     specifies, with zero changes to this package. The three Step 54
//     workflow actions (ActionManageWorkflowDefinitions,
//     ActionActivateWorkflowBinding, ActionDecideWorkflowStep — §25.11)
//     follow the same reserved-name discipline deliberately: Step 54 is
//     dark (schema/contracts/RBAC only), and Steps 55-56 mount the
//     first handlers that call Authorize with them.
package authz
