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
		if !isSettled(row) {
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
	// Counted BEFORE truncation. The contract says totalCount may exceed
	// entries.length, and computing it afterwards made that impossible:
	// the two were always equal, the field carried no information, and the
	// card rendered a capped number as the repository's exact total with
	// nothing to say it had been cut. An operator reading "500 suppressed
	// effects" on a repository with 900 is being told a wrong number by a
	// surface whose entire job is telling them what was suppressed.
	totalCount := len(entries)
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
		TotalCount:            totalCount,
		LLMSpendComputed:      computed,
		LLMSpendUsd:           usd,
		Entries:               entries,
	}, nil
}

// isSettled reports whether a shadow-stamped outbox row has reached a
// state nothing will move it out of.
//
// This replaced a predicate requiring delivered AND delivered_to_ledger,
// which quietly made Activate impossible for whole classes of repository.
// Two states it excluded are terminal:
//
//  1. A genuinely-delivered PASS-THROUGH row. blob_delete,
//     sentinel_auto_fix and linear_digest are classified pass-through, so
//     the outbox worker never diverts them to the ledger and never sets
//     delivered_to_ledger -- yet every row is stamped suppressed_in_shadow
//     regardless of kind. One swept upload or one sentinel verdict during
//     an evaluation therefore left a row that was finished, would never
//     change again, and was counted as in flight forever. Activate
//     returned 409 permanently, under a message telling the operator to
//     wait for it to settle.
//
//  2. A dead-lettered row. It will never be retried. Blocking on it
//     forever is not a guarantee, it is a dead end.
//
// So: delivered is settled, whatever it was delivered TO, and
// dead-lettered is settled-though-failed. Only pending still blocks,
// because only pending can still act.
//
// Treating delivered as settled does not weaken §30.8. A SUPPRESS-
// classified row that was suppressed is diverted to the ledger BEFORE
// delivery, so a suppressed row reaching delivered-without-a-ledger-mark
// would mean the stamp check itself failed -- a different bug, in a
// different place, that this gate was never the control for.
func isSettled(row postgres.ShadowOperatorOutboxRow) bool {
	return row.Status != sqlcgen.OutboxStatusPending
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
