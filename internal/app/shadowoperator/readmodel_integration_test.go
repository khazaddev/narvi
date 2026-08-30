//go:build integration

// Integration tests for BuildSummary against a REAL Postgres instance --
// proving the actual UNION (shadow_scm_writes + marked outbox rows,
// joined against sessions.repos in Go, exactly as ListShadowSuppressedOutboxWithSessionRepos/
// ShadowSCMWriteStore.ListForRepo's own doc comments describe) rather
// than trusting the pure grouping logic (summary_test.go) alone to prove
// the read model end to end. Run via `make test-integration`.
package shadowoperator_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/shadowoperator"
)

// createTestSessionWithRepo creates a session naming EXACTLY one repo --
// the shape postgres.OutboxStore.Create's own §30.8 per-repo resolution
// requires (outbox_shadow.go's own doc comment). Mirrors internal/app/
// outboxworker's own createShadowEpochTestSession/internal/app/
// sessionactor's own createTestSessionWithRepos -- duplicated rather than
// imported, both are unexported in different packages (this codebase's
// established per-package test-helper convention).
func createTestSessionWithRepo(ctx context.Context, t *testing.T, pool *pgxpool.Pool, repoFullName string) pgtype.UUID {
	t.Helper()
	reposJSON, err := json.Marshal([]map[string]any{
		{"name": repoFullName, "url": "https://github.com/" + repoFullName, "branch": nil},
	})
	if err != nil {
		t.Fatalf("marshal test repos: %v", err)
	}
	session, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		Repos:       reposJSON,
	})
	if err != nil {
		t.Fatalf("create test session with repo: %v", err)
	}
	return session.ID
}

// TestBuildSummary_GroupsBothHalvesOfTheUnion proves a shadow_scm_writes
// row and a ledger-terminal outbox row for the SAME repository both
// surface in one Summary, correctly categorized and counted -- the UNION
// §30.6 asks for, exercised against the real join.
func TestBuildSummary_GroupsBothHalvesOfTheUnion(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/union-repo"

	sessionID := createTestSessionWithRepo(ctx, t, pool, repoFullName)

	ledger := narvipg.NewShadowSCMWriteStore(pool)
	if _, err := ledger.Create(ctx, sqlcgen.CreateShadowSCMWriteParams{
		Operation:    "create_pr",
		RepoFullName: repoFullName,
		Target:       ptr("feature/x"),
		SpecJson:     []byte(`{"owner":"acme","repo":"union-repo"}`),
		SessionID:    sessionID,
	}); err != nil {
		t.Fatalf("create shadow_scm_writes row: %v", err)
	}

	outbox := narvipg.NewOutboxStore(pool, false)
	row, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: sessionID,
		Kind:      "github_verdict",
		Payload:   []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create outbox row: %v", err)
	}
	if !row.SuppressedInShadow {
		t.Fatalf("outbox row SuppressedInShadow = false, want true for an unenrolled (shadow-by-default) repo")
	}
	if _, err := outbox.MarkDeliveredToLedger(ctx, row.ID); err != nil {
		t.Fatalf("mark delivered to ledger: %v", err)
	}

	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	summary, err := shadowoperator.BuildSummary(ctx, ledger, reads, repoSettings, repoFullName, 0)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}

	if summary.TotalCount != 2 {
		t.Fatalf("summary.TotalCount = %d, want 2 (one scm_write, one outbox)", summary.TotalCount)
	}
	if summary.PendingShadowEraCount != 0 {
		t.Errorf("summary.PendingShadowEraCount = %d, want 0 (nothing left pending)", summary.PendingShadowEraCount)
	}
	if summary.LiveEgressEnabled {
		t.Errorf("summary.LiveEgressEnabled = true, want false for an unenrolled repo")
	}

	byCategory := map[string]int{}
	for _, c := range summary.Categories {
		byCategory[c.Label] = c.Count
	}
	if byCategory[shadowoperator.CategoryPullRequests] != 1 {
		t.Errorf("category %q = %d, want 1", shadowoperator.CategoryPullRequests, byCategory[shadowoperator.CategoryPullRequests])
	}
	if byCategory[shadowoperator.CategoryGitHubNotices] != 1 {
		t.Errorf("category %q = %d, want 1", shadowoperator.CategoryGitHubNotices, byCategory[shadowoperator.CategoryGitHubNotices])
	}

	foundTarget := false
	for _, e := range summary.Entries {
		if e.Source == "scm_write" && e.Target == "feature/x" {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Errorf("summary.Entries = %+v, want an scm_write entry with target %q", summary.Entries, "feature/x")
	}
}

// TestBuildSummary_PendingOutboxRowCountsAsUnhandled proves a still-
// pending (never delivered) shadow-stamped outbox row is counted toward
// PendingShadowEraCount and never appears as a ledger Entry -- it has no
// evidence yet.
func TestBuildSummary_PendingOutboxRowCountsAsUnhandled(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/pending-repo"

	sessionID := createTestSessionWithRepo(ctx, t, pool, repoFullName)
	outbox := narvipg.NewOutboxStore(pool, false)
	if _, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: sessionID,
		Kind:      "slack_digest",
		Payload:   []byte(`{}`),
	}); err != nil {
		t.Fatalf("create outbox row: %v", err)
	}

	ledger := narvipg.NewShadowSCMWriteStore(pool)
	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	summary, err := shadowoperator.BuildSummary(ctx, ledger, reads, repoSettings, repoFullName, 0)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if summary.PendingShadowEraCount != 1 {
		t.Errorf("summary.PendingShadowEraCount = %d, want 1", summary.PendingShadowEraCount)
	}
	if summary.TotalCount != 0 {
		t.Errorf("summary.TotalCount = %d, want 0 (a pending row is not evidence yet)", summary.TotalCount)
	}
}

// TestBuildSummary_LLMSpendNotComputedWithNoPricedTurn proves §30.1's own
// "surfaced, not suppressed" honesty: a repo with a session but no priced
// turn reports Computed=false, never a fabricated $0.
func TestBuildSummary_LLMSpendNotComputedWithNoPricedTurn(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/no-spend-repo"
	createTestSessionWithRepo(ctx, t, pool, repoFullName)

	ledger := narvipg.NewShadowSCMWriteStore(pool)
	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	summary, err := shadowoperator.BuildSummary(ctx, ledger, reads, repoSettings, repoFullName, 0)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if summary.LLMSpendComputed {
		t.Errorf("summary.LLMSpendComputed = true, want false: no turn has recorded a cost yet")
	}
	if summary.LLMSpendUsd != 0 {
		t.Errorf("summary.LLMSpendUsd = %v, want 0 (unused when Computed is false)", summary.LLMSpendUsd)
	}
}

// TestBuildSummary_LLMSpendSumsAcrossSessionsForTheRepo proves the spend
// line reuses turns.cost_usd (via reviewtriage.NumericToFloat64) and sums
// it across every session naming the repository -- never a second cost
// path.
func TestBuildSummary_LLMSpendSumsAcrossSessionsForTheRepo(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/spend-repo"

	turns := narvipg.NewTurnStore(pool)
	for range 2 {
		sessionID := createTestSessionWithRepo(ctx, t, pool, repoFullName)
		turn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: sessionID, Status: sqlcgen.TurnStatusProcessing})
		if err != nil {
			t.Fatalf("create turn: %v", err)
		}
		if _, err := turns.RecordStepCostUSD(ctx, sessionID, "step-"+turn.ID.String(), 1.5); err != nil {
			t.Fatalf("record step cost: %v", err)
		}
	}

	ledger := narvipg.NewShadowSCMWriteStore(pool)
	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	summary, err := shadowoperator.BuildSummary(ctx, ledger, reads, repoSettings, repoFullName, 0)
	if err != nil {
		t.Fatalf("BuildSummary: %v", err)
	}
	if !summary.LLMSpendComputed {
		t.Fatalf("summary.LLMSpendComputed = false, want true")
	}
	if summary.LLMSpendUsd != 3.0 {
		t.Errorf("summary.LLMSpendUsd = %v, want 3.0 (two sessions at $1.50 each)", summary.LLMSpendUsd)
	}
}

func ptr(s string) *string { return &s }

// TestBuildSummary_ShowsADemotionSuppressedRowAndHidesAnExecutedPassThrough
// covers the two ways the operator's ledger told them the wrong thing.
//
//  1. A row born LIVE and suppressed at delivery — the demotion half of
//     §30.8's suppress-wins rule — kept a false suppressed_in_shadow
//     stamp, while this read is keyed on exactly that column. A
//     suppression that genuinely happened was invisible on the one
//     surface built to show it.
//
//  2. A PASS-THROUGH row that really executed carries the shadow stamp
//     anyway, because every row is stamped regardless of kind, and was
//     displayed as a suppressed effect. That is the opposite of what
//     happened.
func TestBuildSummary_ShowsADemotionSuppressedRowAndHidesAnExecutedPassThrough(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/ledger-truth"

	sessionID := createTestSessionWithRepo(ctx, t, pool, repoFullName)
	outbox := narvipg.NewOutboxStore(pool, false)

	// (1) Born LIVE, then suppressed at delivery.
	if _, err := narvipg.NewRepoSettingsStore(pool).UpsertLiveEgressEnabled(ctx, repoFullName, true); err != nil {
		t.Fatalf("arm live egress: %v", err)
	}
	demoted, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: sessionID, Kind: "github_verdict", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create born-live row: %v", err)
	}
	if demoted.SuppressedInShadow {
		t.Fatalf("premise broken: the row was stamped shadow at enqueue, so this test cannot exercise the demotion half")
	}
	if _, err := outbox.MarkDeliveredToLedger(ctx, demoted.ID); err != nil {
		t.Fatalf("mark delivered to ledger: %v", err)
	}

	// (2) A pass-through row that really executed.
	executed, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: sessionID, Kind: "blob_delete", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create pass-through row: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox SET status = 'delivered', delivered_at = now(), suppressed_in_shadow = true WHERE id = $1`, executed.ID); err != nil {
		t.Fatalf("mark the pass-through row genuinely delivered: %v", err)
	}

	reads := narvipg.NewShadowOperatorReadStore(pool)
	rows, err := reads.ListSuppressedOutboxForRepo(ctx, repoFullName, 100)
	if err != nil {
		t.Fatalf("ListSuppressedOutboxForRepo: %v", err)
	}

	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r.Kind)
	}
	if !slices.Contains(kinds, "github_verdict") {
		t.Errorf("kinds = %v, want the demotion-suppressed github_verdict row: it was suppressed for real and the ledger is the only place that shows it", kinds)
	}
	if slices.Contains(kinds, "blob_delete") {
		t.Errorf("kinds = %v, want NO blob_delete: that pass-through row really executed, and showing it as a suppressed effect tells the operator the opposite of what happened", kinds)
	}
}
