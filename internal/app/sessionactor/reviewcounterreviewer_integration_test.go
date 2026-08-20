//go:build integration

package sessionactor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// This file proves B2's own fix (adversarial review of §26.4)
// against a REAL Postgres instance: reviewCounterReviewerModel's own new
// credential-gating step (reviewCredentialedProviders, sessionconfig.go)
// genuinely reaches provider_credentials, not merely the pure ranking
// logic countermodel_test.go already covers in isolation. Mirrors
// sessionconfig_pathscope_integration_test.go's own
// newDispatchTestRegistry/fakeSpawnProvider/sendEnsureDispatched/waitUntil
// conventions exactly.

// reviewCounterReviewerFixture seeds an "authoring session" (Narvi-
// authored PR, BuildModelID + a matching pr-type artifact -- the SAME
// shape TestResolveProvenance_NarviAuthored, compute_integration_test.go,
// already establishes) plus a SEPARATE "review session" with a
// github_pr_sessions row pointing at the same repo/PR -- the review
// session is the one this file's own tests actually spawn and inspect.
type reviewCounterReviewerFixture struct {
	pool            *pgxpool.Pool
	reviewSessionID pgtype.UUID
	repoFullName    string
	prNumber        int32
}

func newReviewCounterReviewerFixture(ctx context.Context, t *testing.T, pool *pgxpool.Pool, authoringModel string) *reviewCounterReviewerFixture {
	t.Helper()

	sessions := narvipg.NewSessionStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	authoringSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource:  sqlcgen.SessionSpawnSourceWeb,
		BuildModelID: &authoringModel,
	})
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}

	const repoFullName = "acme/counter-reviewer-credential-gating"
	const prNumber int32 = 11
	htmlURL := "https://github.com/" + repoFullName + "/pull/11"
	if _, err := artifacts.Create(ctx, sqlcgen.CreateArtifactParams{
		SessionID: authoringSession.ID, Type: sqlcgen.ArtifactTypePr, Url: htmlURL, Metadata: []byte("{}"),
	}); err != nil {
		t.Fatalf("create pr artifact: %v", err)
	}

	reviewSessionID := createTestSessionWithSpawnSource(ctx, t, pool, sqlcgen.SessionSpawnSourceGithub)

	prSessions := narvipg.NewGitHubPRSessionStore(pool)
	if err := prSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		t.Fatalf("ensure github pr session row: %v", err)
	}
	if err := prSessions.SetSessionID(ctx, repoFullName, prNumber, reviewSessionID); err != nil {
		t.Fatalf("set github pr session id: %v", err)
	}

	return &reviewCounterReviewerFixture{
		pool:            pool,
		reviewSessionID: reviewSessionID,
		repoFullName:    repoFullName,
		prNumber:        prNumber,
	}
}

// spawnAndGetReviewCounterReviewerModel drives a real fresh spawn for
// f's own review session through EnsureDispatched, exactly like
// sessionconfig_pathscope_integration_test.go's own tests, and returns
// the resulting SessionConfig.ReviewCounterReviewerModel.
func (f *reviewCounterReviewerFixture) spawnAndGetReviewCounterReviewerModel(ctx context.Context, t *testing.T) *string {
	t.Helper()

	turnStore := narvipg.NewTurnStore(f.pool)
	createPendingTurn(ctx, t, turnStore, f.reviewSessionID, "review this PR")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-counter-reviewer-credential-gating-" + f.repoFullName}}
	r := newDispatchTestRegistry(t, ctx, f.pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, f.reviewSessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	spec := provider.lastSpec()
	return (*string)(spec.SessionConfig.ReviewCounterReviewerModel)
}

// TestReviewCounterReviewerModel_NoCredentialedProvider_NoOverride is B2's
// own core regression test: a Narvi-authored PR (real, resolvable
// authoring model) with ZERO provider_credentials rows configured for
// this session -- the overwhelming common case, per cmd/sandbox-agent/
// main.go's own doc comment ("the overwhelming common case (nothing
// configured for this session) resolves to nil") -- must resolve
// ReviewCounterReviewerModel to nil, never a guessed pin at an
// uncredentialed provider. Before the B2 fix, ResolveCounterReviewerModel
// had no credential awareness at all and would have picked openai's own
// best candidate here regardless.
func TestReviewCounterReviewerModel_NoCredentialedProvider_NoOverride(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	f := newReviewCounterReviewerFixture(ctx, t, pool, "anthropic/claude-opus-4-5")

	got := f.spawnAndGetReviewCounterReviewerModel(ctx, t)
	if got != nil {
		t.Errorf("ReviewCounterReviewerModel = %v, want nil (no provider_credentials row exists for this session at all)", *got)
	}
}

// TestReviewCounterReviewerModel_OneCredentialedProvider_PinsIt proves the
// positive case: a single GLOBAL-scoped openai credential (the always-
// available fallback scope, §25.3) makes openai eligible, and it wins
// the pin -- anthropic (the authoring family) stays excluded regardless,
// and google was never configured so it is correctly never picked either.
func TestReviewCounterReviewerModel_OneCredentialedProvider_PinsIt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	f := newReviewCounterReviewerFixture(ctx, t, pool, "anthropic/claude-opus-4-5")

	credentials := narvipg.NewProviderCredentialStore(pool)
	if _, err := credentials.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderOpenai, []byte("test-ciphertext-not-real")); err != nil {
		t.Fatalf("create global openai provider credential: %v", err)
	}

	got := f.spawnAndGetReviewCounterReviewerModel(ctx, t)
	if got == nil {
		t.Fatal("ReviewCounterReviewerModel = nil, want a real openai/<model> pin (openai has a global credential configured)")
	}
	if !strings.HasPrefix(*got, "openai/") {
		t.Errorf("ReviewCounterReviewerModel = %q, want an \"openai/...\" pin", *got)
	}
}
