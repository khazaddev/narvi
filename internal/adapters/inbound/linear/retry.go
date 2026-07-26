// This file (retry.go) implements the HIGH audit-fix's own bounded retry
// for AgentSessions.SetSessionID (webhook.go's own handleCreated): a
// confirmed finding that releasing the linear_agent_sessions claim after a
// SetSessionID failure is unsafe, because by that point
// httpapi.CreateSessionCore has ALREADY committed the real session (with a
// Pending turn) AND fired TriggerDispatch -- releasing the claim and
// answering non-2xx would let Linear redeliver the identical `created`
// event, running handleCreated a SECOND time and spawning a duplicate,
// independently-dispatched session/turn for the exact same
// agent_session_id, while the first (real, already-running) session
// becomes permanently unreachable by any future Linear event for it. See
// setSessionIDWithRetry's own doc comment below for the fix.
package linear

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/platform"
)

// sessionIDSetter is the narrow slice of LinearAgentSessionStore's own real
// surface (SetSessionID) setSessionIDWithRetry needs -- a small, locally-
// defined interface (mirrors internal/adapters/inbound/github's own
// identical PullRequestResolver precedent, headresolve.go) so this
// package's own tests can inject a fake that fails a controlled number of
// times before delegating to a real store, with no real dropped Postgres
// connection needed to prove the retry actually works.
// *postgres.LinearAgentSessionStore satisfies this exactly, with no
// adapter-side change.
type sessionIDSetter interface {
	SetSessionID(ctx context.Context, agentSessionID string, sessionID pgtype.UUID) error
}

// setSessionIDWithRetry calls deps.AgentSessions.SetSessionID (or, when
// set, deps.SessionIDSetter -- see that field's own doc comment) with a
// bounded, backed-off retry via platform.Retry (internal/platform/retry.go)
// -- reused here exactly like internal/app/identitylink.FetchEmailWithRetry
// already reuses it for its own inline, request-path-bound retry need (see
// that file's own doc comment for why this shape -- foreground, in-process
// sleep, bounded by the caller's own webhook-response budget -- is
// deliberately distinct from domain/outbox's persisted-schedule backoff).
//
// Retrying this specific call is always safe: by the time this ever runs,
// httpapi.CreateSessionCore has ALREADY committed the real session (with
// its own Pending turn) and fired TriggerDispatch (handleCreated's own top
// doc comment) -- SetSessionID is a plain, idempotent UPDATE against a row
// THIS SAME request already won the first-writer-wins claim on
// (AgentSessions.Claim), so no other writer can ever race in and no retry
// attempt can ever collide with a different outcome.
//
// Returns the last attempt's own error once
// Timeouts.LinearSetSessionIDMaxAttempts is exhausted with no success --
// the caller (handleCreated) must NOT treat that as a release-the-claim
// failure (see this function's own top-of-file doc comment for why).
func (deps Deps) setSessionIDWithRetry(ctx context.Context, agentSessionID string, sessionID pgtype.UUID) error {
	setter := sessionIDSetter(deps.AgentSessions)
	if deps.SessionIDSetter != nil {
		setter = deps.SessionIDSetter
	}

	return platform.Retry(ctx, deps.Timeouts.LinearSetSessionIDMaxAttempts, deps.Timeouts.LinearSetSessionIDRetryBaseDelay, deps.Timeouts.LinearSetSessionIDRetryMaxDelay, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.LinearSetSessionIDTimeout)
		defer cancel()
		return setter.SetSessionID(attemptCtx, agentSessionID, sessionID)
	})
}
