package authz

// Action names one state-changing (or read) command Authorize can render
// a verdict on — always a plain, discrete capability, never a resource
// type by itself (Resource, in authorize.go, carries whatever per-call
// context a specific Action's own matrix row needs, today just
// OwnedOrJoined). Grouped below by which §13.3 matrix row each belongs to.
type Action string

const (
	// -- Row 1: "View sessions / analytics" — admin, maintainer, member,
	// viewer (viewer read-only; there is no separate "write" verb for
	// either of these two, so read-only is simply the ONLY thing a
	// viewer, or anyone else, does with them).

	// ActionViewSessions is read access to session state/history.
	ActionViewSessions Action = "view_sessions"
	// ActionViewAnalytics is read access to analytics/reporting views.
	ActionViewAnalytics Action = "view_analytics"

	// -- Row 2: "Create sessions, prompt, approve plans on own/joined
	// sessions" — admin, maintainer, member (never viewer). Prompting and
	// approving a plan each carry the row's own "own/joined" carve-out
	// for member (Resource.OwnedOrJoined) — admin/maintainer bypass it
	// entirely, per row 3 below. Creating a session has no ownership
	// concept at all (there is no pre-existing resource yet to own) — a
	// member is simply allowed, unconditionally.

	// ActionCreateSession is starting a brand new coding-agent session.
	ActionCreateSession Action = "create_session"
	// ActionPromptSession is enqueuing a new turn on an EXISTING session
	// (the web UI's "send a prompt" / the relaunch-and-resume REST API,
	// internal/adapters/inbound/httpapi/turn.go) — admin/maintainer on
	// any session, member only on one they created or joined.
	ActionPromptSession Action = "prompt_session"
	// ActionApprovePlan is rendering an approve/reject verdict on a
	// plan-mode plan (§8.1) — admin/maintainer on any session's plan,
	// member only on one they created or joined.
	ActionApprovePlan Action = "approve_plan"
	// ActionDecideWorkflowStep is rendering an approve/reject/revise
	// verdict on a workflow run's HITL-gated step (§25.9/§25.11 — Step
	// 54) — own/joined-aware, the SAME row shape as ActionApprovePlan
	// above by §25.11's explicit instruction ("same row as
	// ActionApprovePlan"): admin/maintainer on any run, member only on a
	// session they created or joined. No caller exists yet — the decide
	// endpoint (POST /api/workflow-runs/:runId/steps/:stepRunId/decide)
	// is Step 56's; reserved here so that Step's call site needs no
	// shape change, exactly like every other reserved Action below.
	ActionDecideWorkflowStep Action = "decide_workflow_step"

	// -- Row 3: "Stop/resume ANY session; approve ANY plan" — admin,
	// maintainer only. Deliberately no member own/joined carve-out here,
	// unlike row 2 — the matrix names this explicitly as "ANY session",
	// contrasted with row 2's "own/joined" qualifier; a member who
	// created a session still cannot stop or resume it themselves.
	// ActionApprovePlan above already covers "approve ANY plan" (the
	// SAME action, just satisfied unconditionally for admin/maintainer
	// via allow rather than allowIfOwned) — no separate constant needed.

	// ActionStopSession halts a running session. No caller exists yet —
	// this feature isn't built as of this Step (see doc.go) — reserved so
	// a future Step's call site needs no shape change here.
	ActionStopSession Action = "stop_session"
	// ActionResumeSession resumes a stopped session. Same "no caller yet"
	// note as ActionStopSession.
	ActionResumeSession Action = "resume_session"

	// -- Row 4: "Manage automations, environments, repo/env secrets" —
	// admin, maintainer only. No caller exists yet for any of these three
	// (automations land in a later Phase 3.5 Step per
	// docs/IMPLEMENTATION_PLAN.md; environments/secrets management UI is
	// Phase 7) — reserved names only.

	// ActionManageAutomations covers creating/editing/deleting automation
	// rules.
	ActionManageAutomations Action = "manage_automations"
	// ActionManageEnvironments covers creating/editing/deleting
	// Environment scoping config (§14.1).
	ActionManageEnvironments Action = "manage_environments"
	// ActionManageRepoSecrets covers per-repo secret management.
	ActionManageRepoSecrets Action = "manage_repo_secrets"
	// ActionManageEnvSecrets covers per-environment secret management.
	ActionManageEnvSecrets Action = "manage_env_secrets"
	// ActionManageWorkflowDefinitions covers creating/editing/deleting a
	// CUSTOM workflow definition — an unbound draft (§25.11 — Step 54),
	// maintainer+ in this SAME row as ActionManageAutomations by
	// §25.11's explicit instruction. Deliberately NOT the action that
	// makes a definition live anywhere (that is
	// ActionActivateWorkflowBinding, admin-only, row 6) — and NOT the
	// gate on built-in definitions at all: PUT/DELETE on an is_built_in
	// row is refused unconditionally, even for an admin, as a
	// STRUCTURAL invariant (§25.4), never an RBAC verdict this matrix
	// could express. No caller exists yet — Steps 55-56 own the first
	// handlers.
	ActionManageWorkflowDefinitions Action = "manage_workflow_definitions"

	// -- Row 5: "Edit review verdicts; re-trigger reviews; label-driven
	// auto-approve config" — admin, maintainer only. No caller exists yet
	// (§15's own "release PR review" capability is a later Step).

	// ActionEditReviewVerdict covers overriding a rendered review
	// verdict.
	ActionEditReviewVerdict Action = "edit_review_verdict"
	// ActionRetriggerReview covers re-running an automated review.
	ActionRetriggerReview Action = "retrigger_review"
	// ActionConfigureAutoApprove covers label-driven auto-approve rule
	// configuration.
	ActionConfigureAutoApprove Action = "configure_auto_approve"

	// -- Row 6: "Integrations, global secrets, prompt-template
	// activation, members & roles, sentinel auto-fix toggle" — admin
	// only. §13.3's own parenthetical singles out the sentinel toggle as
	// "stricter than label-driven auto-approve since it ends in an
	// unattended merge, not a human Merge click" — exactly why it sits in
	// this admin-only row and ActionConfigureAutoApprove above sits one
	// row up, at maintainer. No caller exists yet for four of these five
	// (integrations/global-secrets/template-activation/sentinel are later
	// Steps) — ActionManageMembers is the exception: this SAME Step's own
	// "members API" deliverable (internal/adapters/inbound/httpapi/
	// members.go) gates every one of its endpoints (list members,
	// role-change, manual link/unlink, and the audit-log read endpoint)
	// behind this exact Action, per §13.3's own single, bundled "members &
	// roles" row (no separate read-vs-write Action was invented for it).

	// ActionManageIntegrations covers connecting/disconnecting a
	// third-party integration (Slack/Linear workspace, etc).
	ActionManageIntegrations Action = "manage_integrations"
	// ActionManageGlobalSecrets covers org-wide (non-repo/env-scoped)
	// secret management.
	ActionManageGlobalSecrets Action = "manage_global_secrets"
	// ActionActivatePromptTemplate covers activating/deactivating a
	// prompt template.
	ActionActivatePromptTemplate Action = "activate_prompt_template"
	// ActionManageMembers covers inviting/removing members and changing
	// a member's role.
	ActionManageMembers Action = "manage_members"
	// ActionToggleSentinelAutoFix covers §17's own sentinel auto-fix
	// on/off toggle.
	ActionToggleSentinelAutoFix Action = "toggle_sentinel_auto_fix"
	// ActionConfigureBlockOnHighRisk covers §8.2/Step 47's own
	// blockOnHighRisk admin, per-repo, strict-boolean setting
	// (repo_settings, migrations/000044_repo_settings.up.sql) --
	// internal/adapters/inbound/httpapi/reposettings.go's own GET/PUT
	// routes both gate on this. Placed in this SAME admin-only row as
	// ActionToggleSentinelAutoFix above, not row 5's maintainer-level
	// ActionConfigureAutoApprove: blockOnHighRisk changes what runs
	// UNATTENDED on a repo's own PRs (which formal-review event a
	// verdict submits, up to and including a hard REQUEST_CHANGES block)
	// exactly like the sentinel toggle's own row-6 placement is justified
	// (§13.3's own parenthetical on that row), never a per-PR human
	// judgment call the way row 5's actions are.
	ActionConfigureBlockOnHighRisk Action = "configure_block_on_high_risk"
	// ActionActivateWorkflowBinding covers binding a (repo, lane) — or
	// the global (org-wide, repo_full_name = NULL) scope; the SAME
	// action gates both, per §25.11 — to a specific workflow definition
	// (workflow_bindings, migrations/000057_workflows.up.sql). Admin
	// only, in this SAME row as ActionActivatePromptTemplate by
	// §25.11's explicit instruction — activation changes what runs on
	// 100% of a lane's production traffic (§25.6), a system-posture
	// change like template activation, not a per-draft authoring step
	// like row 4's ActionManageWorkflowDefinitions. No caller exists
	// yet (Step 54 is dark; Steps 55-56 own the first handlers).
	ActionActivateWorkflowBinding Action = "activate_workflow_binding"
)

// AllActions is every recognized Action, in this file's own declaration
// order (matrix row order) — exported so tests can exhaustively range
// over every (role, action) pair without hand-maintaining a second list.
var AllActions = []Action{
	ActionViewSessions,
	ActionViewAnalytics,
	ActionCreateSession,
	ActionPromptSession,
	ActionApprovePlan,
	ActionDecideWorkflowStep,
	ActionStopSession,
	ActionResumeSession,
	ActionManageAutomations,
	ActionManageEnvironments,
	ActionManageRepoSecrets,
	ActionManageEnvSecrets,
	ActionManageWorkflowDefinitions,
	ActionEditReviewVerdict,
	ActionRetriggerReview,
	ActionConfigureAutoApprove,
	ActionManageIntegrations,
	ActionManageGlobalSecrets,
	ActionActivatePromptTemplate,
	ActionManageMembers,
	ActionToggleSentinelAutoFix,
	ActionConfigureBlockOnHighRisk,
	ActionActivateWorkflowBinding,
}
