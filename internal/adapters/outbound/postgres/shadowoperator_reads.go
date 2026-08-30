// This file (shadowoperator_reads.go) backs the shadow-operator surface's
// own read model (§30.6: "a read model, not new state" -- the in-plan precedent
// §16.2 states outright). Everything here is a SELECT composing existing
// tables (outbox, sessions, turns); there is no new writer and no new
// table. internal/app/shadowoperator is the one caller, and does every
// bit of business logic (grouping, summing, the promotion/quarantine
// decision) itself -- this file's own job stops at "resolve which rows
// belong to repoFullName" and handing back plain data.
//
// Repo resolution reuses sessionRepoFullNames (outbox_shadow.go, this
// SAME package) rather than a second copy of it: both queries below join
// sessions purely to read sessions.repos JSONB, exactly the shape that
// helper already exists for, and it is unexported so nothing outside this
// package could call it anyway.

package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ShadowOperatorOutboxRow is one outbox row already resolved to a single
// repository -- ListSuppressedOutboxForRepo's own return shape. Deliberately
// narrower than sqlcgen.Outbox: this read model never needs the row's own
// payload (a customer-visible outbox kind carries no ledger-relevant
// detail beyond what already produced this row, unlike shadow_scm_writes'
// own typed specs), and omitting it means this file never has to reason
// about rendering arbitrary per-kind JSON safely.
type ShadowOperatorOutboxRow struct {
	ID                pgtype.UUID
	SessionID         pgtype.UUID
	Kind              string
	Status            sqlcgen.OutboxStatus
	DeliveredToLedger bool
	CreatedAt         pgtype.Timestamptz
}

// SessionCostRow is one session's own running LLM-spend total (turns.
// cost_usd, summed across that session's turns) -- ListSessionCostsForRepo's
// own return shape. TotalCostUsd is handed back as the raw pgtype.Numeric
// rather than a float64 so the caller converts it with the SAME
// reviewtriage.NumericToFloat64 httpapi/workflowruns.go's own per-step
// cost display already uses (internal/app/shadowoperator is an app-layer
// package, the natural place for that conversion to live, not this
// adapter).
type SessionCostRow struct {
	SessionID    pgtype.UUID
	TotalCostUsd pgtype.Numeric
}

// ShadowOperatorReadStore backs the shadow-operator surface's own read
// model (§30.6). A
// separate small store rather than folding these methods onto
// OutboxStore/TurnStore: both queries here join sessions purely to
// resolve owner/repo in Go, and neither belongs to either store's own
// existing single-table contract.
type ShadowOperatorReadStore struct {
	q *sqlcgen.Queries
}

// NewShadowOperatorReadStore builds a ShadowOperatorReadStore over pool.
func NewShadowOperatorReadStore(pool *pgxpool.Pool) *ShadowOperatorReadStore {
	return &ShadowOperatorReadStore{q: sqlcgen.New(pool)}
}

// ListSuppressedOutboxForRepo returns every outbox row this deployment
// has ever stamped suppressed_in_shadow=true for repoFullName -- both a
// still-pending (in-flight) row and a ledger-terminal one
// (delivered_to_ledger=true), undifferentiated; the caller buckets by
// (Status, DeliveredToLedger). limit bounds the underlying scan --
// ListShadowSuppressedOutboxWithSessionRepos' own doc comment (queries/
// outbox.sql) explains why this is a floor, not a guarantee, for a
// deployment large enough to ever reach it.
//
// A row whose own session names repositories this cannot read (malformed
// JSON, an unsupported host) is skipped -- it cannot be attributed to
// repoFullName on the evidence available, mirroring repodemotion.Sweep's
// own identical "skip, do not guess" posture for the same underlying
// parse failure (sweep.go's own doc comment).
func (s *ShadowOperatorReadStore) ListSuppressedOutboxForRepo(ctx context.Context, repoFullName string, limit int32) ([]ShadowOperatorOutboxRow, error) {
	rows, err := s.q.ListShadowSuppressedOutboxWithSessionRepos(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ShadowOperatorOutboxRow, 0, len(rows))
	for _, row := range rows {
		names, ok := sessionRepoFullNames(row.Repos)
		if !ok || !containsRepoFullName(names, repoFullName) {
			continue
		}
		out = append(out, ShadowOperatorOutboxRow{
			ID:                row.ID,
			SessionID:         row.SessionID,
			Kind:              row.Kind,
			Status:            row.Status,
			DeliveredToLedger: row.DeliveredToLedger,
			CreatedAt:         row.CreatedAt,
		})
	}
	return out, nil
}

// ListSessionCostsForRepo returns every session's own running cost total
// for every session naming repoFullName -- LIVE and shadow sessions
// alike, §30.1's own "surfaced, not suppressed" LLM-spend posture. A
// session with no priced turn at all is simply absent from the
// underlying query (queries/turns.sql's own ListSessionCostTotalsWithRepos,
// an INNER JOIN on turns) -- there is nothing to distinguish "no session
// named this repo" from "a session named it but nothing is priced yet"
// at THIS layer; the caller (internal/app/shadowoperator) reports
// "not computed" only when it sums zero rows, exactly mirroring
// RepoSettings.contradictionRateComputed's own "no figure available"
// sentinel discipline.
func (s *ShadowOperatorReadStore) ListSessionCostsForRepo(ctx context.Context, repoFullName string) ([]SessionCostRow, error) {
	rows, err := s.q.ListSessionCostTotalsWithRepos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SessionCostRow, 0, len(rows))
	for _, row := range rows {
		names, ok := sessionRepoFullNames(row.Repos)
		if !ok || !containsRepoFullName(names, repoFullName) {
			continue
		}
		out = append(out, SessionCostRow{SessionID: row.SessionID, TotalCostUsd: row.TotalCostUsd})
	}
	return out, nil
}

// containsRepoFullName is a plain linear search -- names is always a
// session's own repo list, per §3.4 bounded and small, never worth a map.
func containsRepoFullName(names []string, repoFullName string) bool {
	for _, name := range names {
		if name == repoFullName {
			return true
		}
	}
	return false
}
