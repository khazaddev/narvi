package ports

import (
	"context"
	"encoding/json"
)

// NotificationKind discriminates which outbound channel a Notification
// targets -- a thin, faithful mirror of the outbox table's own `kind TEXT`
// column (migrations/000010_outbox.up.sql), which this package's own
// doc.go note ("kind is kept TEXT -- the set of notifier kinds grows with
// the PR-32/33/34/35 ingress work") already anticipated. Declared as its
// own named string type (rather than a bare string) so a caller/
// implementation can never pass an arbitrary, untyped string where a kind
// is expected, mirroring how sqlcgen.SessionSpawnSource is its own named
// type over the sessions.spawn_source column rather than a bare string.
type NotificationKind string

const (
	// NotificationKindSlack routes to internal/adapters/outbound/slackapi
	// (a chat.postMessage reply into the session's own originating
	// thread).
	NotificationKindSlack NotificationKind = "slack"
	// NotificationKindLinear routes to internal/adapters/outbound/
	// linearapi (an outcome-shaped AgentActivity posted to the session's
	// own originating agent session).
	NotificationKindLinear NotificationKind = "linear"
	// NotificationKindGitHub routes to internal/adapters/outbound/githubapi
	// (an issue comment posted to the session's own originating PR).
	NotificationKindGitHub NotificationKind = "github"

	// NotificationKindSlackPlanApproval is Step 38's ("plan mode,
	// cross-channel", §8.1/§13.3) own addition -- routes to internal/app/
	// outboxworker's own Slack plan-approval notifier (wrapping
	// internal/adapters/outbound/slackapi), which posts the REAL
	// interactive Block Kit approval-request message (chat.postMessage,
	// numbered steps + Approve/Request changes/Reject buttons) and, on
	// success, persists the message's own channel+ts back onto the plans
	// row (PlanStore.SetSlackMessageRef) -- distinct from
	// NotificationKindSlack (a plain-text chat.postMessage reply) because
	// this one carries plan-specific content/buttons and has a real,
	// durable side effect (the persisted message ref) beyond delivery
	// itself.
	NotificationKindSlackPlanApproval NotificationKind = "slack_plan_approval"

	// NotificationKindSlackPlanDecided is Step 38's own addition -- routes
	// to the SAME Slack plan-approval notifier, but calls chat.update
	// (never chat.postMessage) against an EXISTING message (channel+ts
	// already known, carried in the payload itself -- see slackapi.
	// PlanDecidedPayload) to reflect a plan's final approved/rejected
	// outcome, whichever entry point (Slack itself, Linear, or the web
	// REST endpoints) actually rendered the decision -- §16.1/§13.3's own
	// "first verdict wins + notify the other channels". An audit-fix batch
	// (completeness/observability, internal/app/sessionactor/planrecord.go)
	// reuses this SAME kind for a third case: a plan SUPERSEDED by a newer
	// plan-mode turn's completion (rather than decided) -- chat.update's own
	// "omit blocks to strip them" behavior is exactly right there too, to
	// clear the now-stale Approve/Reject buttons.
	NotificationKindSlackPlanDecided NotificationKind = "slack_plan_decided"

	// NotificationKindLinearProgress is an audit-fix batch's own addition
	// (finding M16, "completeness", internal/adapters/outbound/linearapi/
	// doc.go): that package's own top comment explicitly deferred a
	// richer, asynchronous, retried "progressive" AgentActivity update
	// (the "progressive" half of §8.10) to "a future Step" once the real
	// Notifier/outbox consumer existed -- Step 35 shipped that consumer,
	// but nothing ever enqueued a mid-turn update through it until now.
	// Routes to the SAME internal/app/outboxworker linearNotifier already
	// registered under NotificationKindLinear (reused, not a second
	// client/credential-lookup path), but posts a "thought"-shaped
	// AgentActivity (linearapi.Client.CreateThoughtActivity) instead of an
	// outcome-shaped response/error one. A NEW, distinct Kind -- rather
	// than a payload-level discriminator squeezed onto
	// NotificationKindLinear -- mirroring NotificationKindSlackPlanApproval/
	// NotificationKindSlackPlanDecided's own established precedent just
	// above for "more than one shape needs to travel under one channel":
	// each of those is its own Kind with its own distinct payload struct,
	// even though both are delivered by the SAME planSlackNotifier
	// instance; linearNotifier's own Deliver now dispatches on
	// notification.Kind the exact same way planSlackNotifier's already
	// does.
	NotificationKindLinearProgress NotificationKind = "linear_progress"

	// NotificationKindGitHubVerdict is Step 47's own addition ("server-side
	// verdict", §8.2/§5.2/§21.2): routes to internal/adapters/outbound/
	// githubapi's own verdict notifier, which submits a FORMAL pull request
	// review (the "formal-review gate") or, deliberately, never a plain
	// PostIssueComment call, plus syncs the review:*-risk label vocabulary
	// (internal/domain/reviewpost.ComputeLabelSync) -- both in ONE Deliver
	// call, since a caller only ever posts a verdict as one coherent unit
	// (text + labels together), never independently. Distinct from
	// NotificationKindGitHub (the generic outcome-text comment every OTHER
	// github-origin turn used to post, now BLOCKED for a review session,
	// internal/app/sessionactor/outboxenqueue.go) -- see that Kind's own
	// doc comment for why a review session never uses it any more.
	NotificationKindGitHubVerdict NotificationKind = "github_verdict"

	// NotificationKindSentinelAutoFix is Step 48's own addition
	// ("sentinels + suggestions", §17.2): routes to internal/app/
	// outboxworker's own sentinel-auto-fix notifier, which spawns a
	// child session (httpapi.SpawnChildSession) pre-loaded with the
	// origin diff and the specific coverage/doc-drift finding(s) that
	// triggered it -- see SentinelAutoFixPayload's own doc comment for
	// the payload shape, and reviewverdict.go for the ONE place this Kind
	// is ever enqueued (inside the same transaction as the triggering
	// verdict's own findings-upsert write).
	NotificationKindSentinelAutoFix NotificationKind = "sentinel_auto_fix"

	// NotificationKindHandoffSentinel is Step 49's own addition
	// ("handoff-readiness sentinel", §14.4): routes to internal/adapters/
	// outbound/githubapi's own handoff notifier, which posts the
	// sentinel's already-rendered summary as a plain issue comment
	// (PostIssueComment) AND syncs the fixed "handoff" label (AddLabels)
	// -- both in ONE Deliver call, mirroring NotificationKindGitHubVerdict's
	// own identical "text + labels together, one outbox row" precedent.
	// Distinct from that Kind: this sentinel never computes or posts a
	// risk verdict at all (§14.4: "alongside or INSTEAD OF a normal risk
	// verdict") -- see internal/app/sessionactor/handoffsentinel.go for
	// the ONE place this Kind is ever enqueued, inside the same
	// transaction as this PR's own idempotency claim
	// (internal/adapters/outbound/postgres.HandoffSentinelStore.Claim).
	NotificationKindHandoffSentinel NotificationKind = "handoff_sentinel"

	// NotificationKindReleaseManifest is Step 50's own addition ("release
	// PR review", §15.2): routes to internal/adapters/outbound/githubapi's
	// own release-manifest notifier, which posts the manifest check's
	// already-rendered comment (internal/domain/reviewpost.
	// RenderManifestComment) as a plain issue comment (PostIssueComment)
	// -- NEVER a formal review (the manifest check is "an audit, not a
	// risk verdict", §15.2's own words: it has no RiskLevel/Shippable of
	// its own to gate a formal-review event on, unlike
	// NotificationKindGitHubVerdict). Distinct from that Kind for exactly
	// the same reason NotificationKindHandoffSentinel already is (that
	// Kind's own doc comment): a mechanical, always-runs check that never
	// computes or posts a risk verdict. See internal/app/releasereview
	// for the ONE place this Kind is ever enqueued.
	NotificationKindReleaseManifest NotificationKind = "release_manifest"
)

// Notification is what Notifier.Deliver needs to deliver ONE outbox entry
// -- Kind/Payload are a thin, faithful mirror of the outbox table's own
// `kind TEXT`/`payload JSONB` columns (migrations/000010_outbox.up.sql):
// Payload is deliberately kept as opaque json.RawMessage here, never a
// union of every possible provider-specific Go struct, so this port
// itself never needs to change shape as a fourth/fifth notifier kind is
// added later -- each concrete Notifier implementation (internal/adapters/
// outbound/{slackapi,linearapi,githubapi}) owns unmarshaling Payload into
// its OWN kind-specific shape, and is only ever asked to Deliver a
// Notification whose Kind it already knows how to handle (the delivery
// worker, internal/app/outboxworker, is what routes by Kind -- see that
// package's own doc comment for the kind->Notifier map this implies).
type Notification struct {
	Kind    NotificationKind
	Payload json.RawMessage
}

// Notifier is the port that delivers ONE outbound notification to an
// external channel (§4.3: "Notifier (Slack/Linear/GitHub comment
// delivery -- consumed via outbox only)"). Three real implementations
// exist, one per NotificationKind above (internal/adapters/outbound/
// slackapi, linearapi, githubapi) -- this interface exists specifically so
// each can satisfy the SAME contract (CLAUDE.md: "don't couple a port to a
// single adapter... interfaces in /internal/app/ports must hold for more
// than one implementation"), even though (unlike SourceControl/
// SandboxProvider, where two adapters implement the exact same operation
// against different providers) each of THESE three implementations is
// only ever asked to Deliver its own matching Kind in practice -- the
// delivery worker is what enforces that routing, not this interface
// itself, which is why Deliver takes no Kind-specific method of its own
// and instead threads Kind through the single Notification value: a
// uniform shape every implementation can be exercised through
// identically in tests (httptest.Server-based, mirroring every other
// outbound adapter's own existing test convention) and wired into the
// SAME kind->Notifier routing map in production (cmd/control-plane/
// main.go), without this port needing to know how many concrete kinds
// exist.
//
// Deliver is called EXACTLY ONCE per delivery attempt, always OUTSIDE any
// Postgres transaction (§5.1's own "a retry worker delivers with
// exponential backoff" -- a real outbound network call must never hold a
// transaction open, the same discipline every other real network call in
// this codebase already follows, e.g. SourceControl.CreatePR/
// SandboxProvider.CreateSandbox). A non-nil error means this attempt
// failed; the caller (internal/app/outboxworker) is what decides whether
// to retry (with backoff) or dead-letter -- this port never classifies its
// own errors as transient/permanent (mirroring SourceControl's own
// identical "no typed classification" precedent), since no caller needs
// one: outboxworker's own domain/outbox.EvaluateBackoff decision is driven
// purely by attempt count, not by inspecting the error's own shape.
type Notifier interface {
	Deliver(ctx context.Context, n Notification) error
}
