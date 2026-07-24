// This file (audit.go) holds recordAuditLog -- the one small helper every
// Authorize-gated state change this package makes now calls, right before
// its own transaction commits (§13.3: "audit_log(actor_user_id, action,
// resource_type, resource_id, detail_json, correlation_id, created_at)
// written in the same transaction as the change"). Callers: create.go's
// CreateSessionOnTx, turn.go's CreateTurnCore, decideplan.go's
// DecidePlanOnTx.
//
// Step 39's own second half ("identities + full RBAC", auto-linking)
// moved the actual implementation to internal/app/auditlog.Record, so a
// caller OUTSIDE this package (internal/app/identitylink.Resolve, which
// needs to audit-log a brand-new auto-linked identity from inside its own
// transaction) can call the identical helper without importing this
// (inbound-adapter) package from an app-layer one -- see that package's
// own doc comment for the full reasoning. This is a thin forwarding
// wrapper, kept so every existing call site in THIS package (create.go,
// turn.go, decideplan.go) is untouched.

package httpapi

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/auditlog"
)

// recordAuditLog forwards to internal/app/auditlog.Record -- see that
// function's own doc comment for the complete behavior (NULL-for-bot
// actorUserID convention, correlation-id-from-context, marshal-failure
// fallback, why an INSERT failure is propagated rather than swallowed).
func recordAuditLog(ctx context.Context, auditLog *postgres.AuditLogStore, actorUserID pgtype.UUID, action, resourceType, resourceID string, detail map[string]any) error {
	return auditlog.Record(ctx, auditLog, actorUserID, action, resourceType, resourceID, detail)
}
