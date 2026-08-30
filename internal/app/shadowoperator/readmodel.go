package shadowoperator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/reviewtriage"
)

// DefaultEntryLimit bounds how many raw rows each half of the §30.6
// UNION reads per BuildSummary call -- see ListShadowSuppressedOutboxWithSessionRepos/
// ShadowSCMWriteStore.ListForRepo's own doc comments for why this is a
// floor for a deployment large enough to reach it, which a dedicated
// shadow-evaluation deployment (§30.8) is not expected to be.
const DefaultEntryLimit = 500

// BuildSummary reads both halves of §30.6's own UNION read model for
// repoFullName -- shadow_scm_writes (via ledger) and the outbox's own
// §30.8 epoch stamps (via reads) -- plus the current repo_settings row
// (via repoSettings) and the §30.1 LLM-spend line, and groups the result
// into Summary. entryLimit <= 0 uses DefaultEntryLimit.
//
// A repository with no repo_settings row at all (never enrolled, or
// enrolled but never written) resolves to the table's own established
// "absent row = every flag off" default (Get's own doc comment,
// migrations/000044's own doc comment) -- LiveEgressEnabled=false,
// LiveEgressPromotedAt=nil -- rather than an error, matching every other
// reader of this table.
func BuildSummary(ctx context.Context, ledger *postgres.ShadowSCMWriteStore, reads *postgres.ShadowOperatorReadStore, repoSettings *postgres.RepoSettingsStore, repoFullName string, entryLimit int32) (Summary, error) {
	if entryLimit <= 0 {
		entryLimit = DefaultEntryLimit
	}

	settings, err := repoSettings.Get(ctx, repoFullName)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Summary{}, fmt.Errorf("shadowoperator: read repo settings: %w", err)
	}
	// A pgx.ErrNoRows leaves settings as the zero sqlcgen.RepoSetting --
	// LiveEgressEnabled false, LiveEgressPromotedAt an invalid (zero)
	// pgtype.Timestamptz, exactly the "never promoted" reading this
	// function wants for an unenrolled repo.

	scmRows, err := ledger.ListForRepo(ctx, repoFullName, entryLimit)
	if err != nil {
		return Summary{}, fmt.Errorf("shadowoperator: list shadow_scm_writes: %w", err)
	}
	outboxRows, err := reads.ListSuppressedOutboxForRepo(ctx, repoFullName, entryLimit)
	if err != nil {
		return Summary{}, fmt.Errorf("shadowoperator: list suppressed outbox rows: %w", err)
	}

	var entries []Entry
	for _, row := range scmRows {
		entries = append(entries, Entry{
			Source:    "scm_write",
			Operation: row.Operation,
			Category:  categoryForSCMOperation(row.Operation),
			Target:    stringOrEmpty(row.Target),
			SessionID: uuidOrEmpty(row.SessionID),
			CreatedAt: row.CreatedAt.Time,
		})
	}

	pending := 0
	for _, row := range outboxRows {
		if !isLedgerTerminal(row) {
			pending++
			continue
		}
		entries = append(entries, Entry{
			Source:    "outbox",
			Operation: row.Kind,
			Category:  categoryForOutboxKind(row.Kind),
			SessionID: uuidOrEmpty(row.SessionID),
			CreatedAt: row.CreatedAt.Time,
		})
	}

	sortEntriesNewestFirst(entries)
	if int32(len(entries)) > entryLimit {
		entries = entries[:entryLimit]
	}

	usd, computed, err := sumSessionCostForRepo(ctx, reads, repoFullName)
	if err != nil {
		return Summary{}, fmt.Errorf("shadowoperator: sum session cost: %w", err)
	}

	var promotedAt *time.Time
	if settings.LiveEgressPromotedAt.Valid {
		t := settings.LiveEgressPromotedAt.Time
		promotedAt = &t
	}

	return Summary{
		RepoFullName:          repoFullName,
		LiveEgressEnabled:     settings.LiveEgressEnabled,
		LiveEgressPromotedAt:  promotedAt,
		PendingShadowEraCount: pending,
		Categories:            summarizeCategories(entries),
		TotalCount:            len(entries),
		LLMSpendComputed:      computed,
		LLMSpendUsd:           usd,
		Entries:               entries,
	}, nil
}

// isLedgerTerminal reports whether row is a genuine ledger entry (the
// outbox worker delivered it into shadow_scm_writes' own sibling terminal
// mark, migrations/000103's own delivered_to_ledger column) rather than
// still in flight or dead-lettered without ever having been recorded.
// See queries/outbox.sql's own ListShadowSuppressedOutboxWithSessionRepos
// doc comment for why both states are returned undifferentiated and
// bucketed here instead.
func isLedgerTerminal(row postgres.ShadowOperatorOutboxRow) bool {
	return row.Status == sqlcgen.OutboxStatusDelivered && row.DeliveredToLedger
}

// sumSessionCostForRepo sums every matching session's own running cost
// total with reviewtriage.NumericToFloat64 -- the SAME pgtype.Numeric
// conversion httpapi/workflowruns.go's own per-step cost display already
// uses (§30.1: reuse the existing cost path, never build a second one).
// computed is false only when zero sessions naming repoFullName have any
// priced turn at all -- never conflated with a real $0.00.
func sumSessionCostForRepo(ctx context.Context, reads *postgres.ShadowOperatorReadStore, repoFullName string) (usd float64, computed bool, err error) {
	rows, err := reads.ListSessionCostsForRepo(ctx, repoFullName)
	if err != nil {
		return 0, false, err
	}
	var total float64
	var foundAny bool
	for _, row := range rows {
		v, ok := reviewtriage.NumericToFloat64(row.TotalCostUsd)
		if !ok {
			continue
		}
		total += v
		foundAny = true
	}
	return total, foundAny, nil
}

func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// uuidOrEmpty renders a pgtype.UUID as its string form, or "" when it
// carries no real value (Valid == false) -- pgtype.UUID's own String()
// otherwise renders the zero value as
// "00000000-0000-0000-0000-000000000000", which is not the same fact as
// "no session".
func uuidOrEmpty(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

// sortEntriesNewestFirst is a plain insertion sort -- entries here never
// exceed 2*DefaultEntryLimit before truncation, and each half of the
// UNION already arrives newest-first from its own ORDER BY, so this is
// closer to a merge than a real sort in the common case.
func sortEntriesNewestFirst(entries []Entry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j-1].CreatedAt.Before(entries[j].CreatedAt); j-- {
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}
