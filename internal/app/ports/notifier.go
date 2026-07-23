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
