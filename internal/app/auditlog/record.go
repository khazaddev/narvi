// Package auditlog holds Record -- the one small helper EVERY
// Authorize-gated state change in this codebase calls, right before its
// own transaction commits (§13.3: "audit_log(actor_user_id, action,
// resource_type, resource_id, detail_json, correlation_id, created_at)
// written in the same transaction as the change").
//
// This started life as internal/adapters/inbound/httpapi's own
// unexported recordAuditLog (Step 39's first half); it moved here,
// unchanged in behavior, so a caller OUTSIDE httpapi -- specifically
// internal/app/identitylink.Resolve, which needs to audit-log a brand-new
// auto-linked identity from inside its own transaction -- can call the
// SAME helper without httpapi (an INBOUND adapter) ever being imported
// FROM an app-layer package, which would invert this codebase's own
// dependency direction (inbound adapters depend on app/domain, never the
// reverse). httpapi's own audit.go now simply forwards to this package
// (see that file's own doc comment) so every existing call site there
// keeps compiling, and behaving, identically.
package auditlog

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// Record inserts one audit_log row via store (already WithTx-scoped to
// the caller's own open transaction -- this function itself never
// begins/commits anything). actorUserID is passed straight through as-is:
// Valid for a real authenticated caller, an explicit invalid
// pgtype.UUID{} for a bot/webhook-attributed change or a system-driven
// one (e.g. an auto-linked identity -- see internal/app/identitylink's
// own doc comment for why that specific case is NULL, not the newly
// matched user's own id) -- mirrors sessions.created_by/plans.decided_by's
// own identical NULL-for-bot convention (§17.5's own allowance:
// "actor_user_id NULL... for actions with no human actor", no separate
// system-actor row ever needed). correlationID is read from ctx
// (platform.CorrelationIDFromContext) if present, else stored as NULL.
//
// detail is marshaled to JSON for detail_json -- a marshal failure here is
// defensively treated as an empty object ('{}', the column's own schema
// default) rather than ever failing the caller's whole transaction over a
// logging nicety's own encoding error; every call site today passes a
// small, fixed map[string]any of plain strings/numbers, so this branch
// should be unreachable in practice.
//
// Any OTHER error (the INSERT itself failing) is propagated to the
// caller: the audit row is not best-effort, it is transactionally bound
// to the change it describes -- a failure here means the caller's own
// tx.Commit must never be reached, so the state change and its audit
// record either both land or neither does.
func Record(ctx context.Context, store *postgres.AuditLogStore, actorUserID pgtype.UUID, action, resourceType, resourceID string, detail map[string]any) error {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		detailJSON = []byte("{}")
	}

	var correlationID *string
	if id, ok := platform.CorrelationIDFromContext(ctx); ok && id != "" {
		correlationID = &id
	}

	_, err = store.Record(ctx, sqlcgen.CreateAuditLogEntryParams{
		ActorUserID:   actorUserID,
		Action:        action,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		DetailJson:    detailJSON,
		CorrelationID: correlationID,
	})
	return err
}
