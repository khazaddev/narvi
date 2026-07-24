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
	ActionStopSession,
	ActionResumeSession,
	ActionManageAutomations,
	ActionManageEnvironments,
	ActionManageRepoSecrets,
	ActionManageEnvSecrets,
	ActionEditReviewVerdict,
	ActionRetriggerReview,
	ActionConfigureAutoApprove,
	ActionManageIntegrations,
	ActionManageGlobalSecrets,
	ActionActivatePromptTemplate,
	ActionManageMembers,
	ActionToggleSentinelAutoFix,
}
