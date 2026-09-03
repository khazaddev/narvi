// This file (activate.go) implements Activate -- §30.8's own promotion
// graduation gesture: the ONE function in this codebase permitted,
// alongside the postgres package itself and internal/app/seed, to call
// RepoSettingsStore.UpsertLiveEgressEnabled (tools/lint/narvichecks/
// demotionsweep's own allow-list; see that analyzer's doc comment for the
// full "why a promotion is exempt" reasoning, and this package's own
// doc.go).
//
// Activate always promotes. Its own signature carries no boolean --
// there is no argument that could make it call UpsertLiveEgressEnabled
// with false -- so "Activate never demotes" is a fact about this
// function's own type, not a convention a future edit could quietly
// break while still passing demotionsweep's own name-based check.
// TestActivate_IsTheOnlyCallerOfUpsertLiveEgressEnabledInThisPackage
// pins that this file is the sole call site within this package, the
// same "pinned by a test that it is the sole production call site"
// discipline §30.6 already asks of the synthetic-ref constructor.

package shadowoperator

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/auditlog"
)

// ErrUnhandledShadowEraRows is returned when repoFullName still has
// outbox rows this deployment stamped suppressed_in_shadow at enqueue
// that have not yet reached a ledger-terminal state (§30.8's own
// "shadow-era artifact quarantine": "Activate refuses ... while
// unhandled shadow-era rows exist for the repo").
//
// Refuse, chosen over an explicit quarantine mechanism: every such row
// is ALREADY safe by construction regardless of when (or whether)
// Activate runs -- a born-shadow outbox row is terminally shadow
// (§30.8's own epoch discipline, migrations/000103's own doc comment),
// so it can only ever resolve into the ledger, promotion or not. What
// refusing buys is completeness of the operator's OWN evaluation, not
// security: an admin who activates while suppressions are still
// resolving would be graduating a repository before having seen
// everything shadow mode did for it. Refusing needs no new schema or
// state (an explicit quarantine mechanism would); the operator's only
// recourse is to wait for these rows to settle (ordinary outbox retry
// cadence, §5.1) and try again -- there is no failure mode to recover
// from, because nothing about promotion is blocked FOREVER by a row
// that is actively retrying.
type ErrUnhandledShadowEraRows struct {
	RepoFullName string
	// Count is how many outbox rows are still unresolved -- surfaced so
	// an operator sees the number, not just that some exist.
	Count int
}

func (e *ErrUnhandledShadowEraRows) Error() string {
	return fmt.Sprintf("shadowoperator: %d unhandled shadow-era row(s) remain for %s; wait for them to resolve into the ledger before activating", e.Count, e.RepoFullName)
}

// Activate promotes repoFullName from shadow to live (repo_settings.
// live_egress_enabled false->true), applying §30.8's own promotion fence
// (UpsertLiveEgressEnabled's own CASE logic stamps live_egress_promoted_at
// on a genuine transition -- see that query's own doc comment) plus this
// function's own shadow-era artifact quarantine (ErrUnhandledShadowEraRows
// above).
//
// Idempotent on an already-live repository: UpsertLiveEgressEnabled's own
// true->true branch is a no-op on the promotion fence (re-affirming an
// already-live repo must never slide the fence forward), and this
// function still writes a fresh audit_log entry for the call itself --
// §30.6: "a flag flip is an audit_log entry", and an admin re-running
// Activate is itself worth a record even when nothing about the
// repository's own state changes.
//
// actorUserID is the authenticated caller (httpapi's own
// authenticatedUserID) -- Activate is always a human-initiated REST
// action, unlike internal/app/seed's own systemActor()-attributed writes,
// so this never passes an invalid/system UUID.
func Activate(ctx context.Context, reads *postgres.ShadowOperatorReadStore, repoSettings *postgres.RepoSettingsStore, auditLog *postgres.AuditLogStore, repoFullName string, actorUserID pgtype.UUID) (sqlcgen.RepoSetting, error) {
	pending, err := countUnhandledShadowEraRows(ctx, reads, repoFullName)
	if err != nil {
		return sqlcgen.RepoSetting{}, fmt.Errorf("shadowoperator: count unhandled shadow-era rows: %w", err)
	}
	if pending > 0 {
		return sqlcgen.RepoSetting{}, &ErrUnhandledShadowEraRows{RepoFullName: repoFullName, Count: pending}
	}

	updated, err := repoSettings.UpsertLiveEgressEnabled(ctx, repoFullName, true)
	if err != nil {
		return sqlcgen.RepoSetting{}, fmt.Errorf("shadowoperator: activate: upsert live-egress-enabled: %w", err)
	}

	if err := auditlog.Record(ctx, auditLog, actorUserID, "shadow.activated", "repo_settings", repoFullName, map[string]any{
		"liveEgressEnabled": updated.LiveEgressEnabled,
	}); err != nil {
		return sqlcgen.RepoSetting{}, fmt.Errorf("shadowoperator: activate: record audit log: %w", err)
	}

	return updated, nil
}

// countUnhandledShadowEraRows counts this repository's shadow-stamped
// outbox rows that have not settled.
//
// It reads the UNBOUNDED, unsettled-only query rather than the display
// list. The display list's limit is applied across the whole deployment
// before the repo filter runs in Go, so this repository's own OLDEST
// unsettled row -- the one most likely to be genuinely stuck, and exactly
// what a promotion gate exists to notice -- is the first to fall off the
// end. Counting from that list would fail OPEN.
func countUnhandledShadowEraRows(ctx context.Context, reads *postgres.ShadowOperatorReadStore, repoFullName string) (int, error) {
	rows, err := reads.ListUnsettledSuppressedOutboxForRepo(ctx, repoFullName)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		if !isSettled(row) {
			count++
		}
	}
	return count, nil
}
