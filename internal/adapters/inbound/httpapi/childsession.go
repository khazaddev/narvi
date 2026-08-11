// This file (childsession.go) implements Step 48's own ("sentinels +
// suggestions", §17.2) "child session" mechanism -- the plan text at
// §14.4/§17.2 describes this as an "existing mechanism", but it is not:
// this Step is the first one that actually builds it (see this Step's own
// PR description for the "what's already there vs. what the plan assumes"
// writeup, and migrations/000045_sessions_child_sessions.up.sql's own doc
// comment).
//
// SpawnChildSession is EXPORTED specifically so a package that can import
// httpapi -- but that httpapi itself must never import back, avoiding a
// cycle -- can call it: internal/app/outboxworker's own sentinel-auto-fix
// notifier (sentinelautofix.go), mirroring the EXACT precedent internal/
// adapters/inbound/github's own coalesce.go already establishes for
// CreateSessionOnTx/CreateTurnForBot ("already callable from outside
// httpapi by design").

package httpapi

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// SpawnChildSession is CreateSessionCore's own child-session-aware sibling
// -- validates req exactly the same way, but threads parentSessionID/
// spawnDepth/provenanceTag through to CreateSessionOnTx via
// ChildSessionOptions (create.go), rather than leaving them at their
// zero-value defaults the way CreateSessionCore always does. Every other
// step (single transaction, TriggerDispatch once committed and a prompt
// was supplied) is identical to CreateSessionCore -- this is NOT a
// parallel reimplementation of session creation, just CreateSessionCore's
// own sequencing with one additional argument threaded through.
//
// provenanceTag is REQUIRED (never empty) -- a child session, by
// definition, always carries SOME provenance tag identifying why it
// exists (today, always provenance.SentinelAutoFix); a caller with no
// provenance tag to set should call CreateSessionCore instead, not this
// function with an empty string.
//
// epistemicCheckDefault (F6, adversarial review, Step 61) mirrors
// CreateSessionOnTx's own identical required parameter -- see that
// function's own doc comment. internal/app/outboxworker's own
// sentinelAutoFixNotifier (this function's one real caller) passes the
// real, operator-configured platform.Config.EpistemicCheckDefault: a
// sentinel-auto-fix child session is an ordinary build session (it edits
// test/doc files to fix a finding), never a review session, so no F7-style
// hardcoded-false carve-out applies here.
func SpawnChildSession(
	ctx context.Context,
	pool *pgxpool.Pool,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	environments *postgres.EnvironmentStore,
	auditLog *postgres.AuditLogStore,
	registry *sessionactor.Registry,
	req restdtos.CreateSessionRequest,
	parentSessionID pgtype.UUID,
	spawnDepth int32,
	provenanceTag string,
	epistemicCheckDefault bool,
) (sqlcgen.Session, *CreateSessionError) {
	logger := platform.Logger(ctx)

	if _, verr := validateCreateSessionRequest(req); verr != nil {
		return sqlcgen.Session{}, verr
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: begin spawn-child-session tx failed", "error", err)
		return sqlcgen.Session{}, &CreateSessionError{http.StatusInternalServerError, "internal error"}
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag := provenanceTag
	created, hasPrompt, cerr := CreateSessionOnTx(ctx, tx, sessions, turns, environments, auditLog, req, pgtype.UUID{}, epistemicCheckDefault, ChildSessionOptions{
		ParentSessionID: parentSessionID,
		SpawnDepth:      spawnDepth,
		ProvenanceTag:   &tag,
	})
	if cerr != nil {
		return sqlcgen.Session{}, cerr
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: commit spawn-child-session tx failed", "error", err)
		return sqlcgen.Session{}, &CreateSessionError{http.StatusInternalServerError, "internal error"}
	}

	if hasPrompt {
		TriggerDispatch(ctx, registry, created.ID)
	}

	return created, nil
}
