//go:build integration

// This file proves §30.4's own demotion fix, wired through seedRepoSetting
// (reposettings.go): a genuine repo_settings.live_egress_enabled
// true->false transition flags every currently-live sandbox of that repo
// for real termination (internal/app/repodemotion.Sweep) and cancels any
// push/PR decision currently outstanding on it -- both via internal/app/
// seed, the ONLY writer of that column today.
package seed_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/seed"
	"github.com/khazaddev/narvi/internal/domain/seedmanifest"
)

// createSessionAndLiveSandbox seeds a minimal session (naming repoURL) and
// a sandbox row already in a LIVE status (StatusReady), the exact
// precondition internal/app/repodemotion.Sweep's own ListLiveWithSessionRepos
// query requires. Returns the session id.
func createSessionAndLiveSandbox(ctx context.Context, t *testing.T, sessionStore *narvipg.SessionStore, sandboxStore *narvipg.SandboxStore, repoName, repoURL string) pgtype.UUID {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{"name": repoName, "url": repoURL, "branch": "main"}})
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	session, err := sessionStore.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		Repos:       raw,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := sandboxStore.Create(ctx, session.ID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: session.ID, Status: sqlcgen.SandboxStatusReady,
	}); err != nil {
		t.Fatalf("move sandbox to ready: %v", err)
	}
	return session.ID
}

// TestRun_RepoSettings_Demotion_FlagsLiveSandboxTermination proves the
// full wiring: promote a repo to live, seed a live sandbox for it, then
// demote the repo via a second manifest run -- the sandbox must come out
// flagged for real termination.
func TestRun_RepoSettings_Demotion_FlagsLiveSandboxTermination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)
	const repo = "example-org/demotion-repo"
	const repoURL = "https://github.com/example-org/demotion-repo.git"

	sessionStore := narvipg.NewSessionStore(deps.Pool)
	sandboxStore := narvipg.NewSandboxStore(deps.Pool)
	sessionID := createSessionAndLiveSandbox(ctx, t, sessionStore, sandboxStore, "demotion-repo", repoURL)

	trueVal, falseVal := true, false

	// Promote first -- a fresh (never-promoted) repo has nothing this
	// fix protects against (§30.8's own "shadow-by-default-at-onboarding"
	// scope), so a genuine true->false TRANSITION requires a real prior
	// promotion.
	report1, err := seed.Run(ctx, deps, &seedmanifest.Manifest{RepoSettings: []seedmanifest.RepoSetting{
		{RepoFullName: repo, LiveEgressEnabled: &trueVal},
	}}, false)
	if err != nil {
		t.Fatalf("Run() (promote) error = %v", err)
	}
	requireNoItemErrors(t, report1)

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox after promotion: %v", err)
	}
	if row.DemotionTerminateRequestedAt.Valid {
		t.Fatal("demotion_terminate_requested_at set after a PROMOTION, want unset (only a demotion flags termination)")
	}

	// Now demote -- the real transition this fix exists for.
	report2, err := seed.Run(ctx, deps, &seedmanifest.Manifest{RepoSettings: []seedmanifest.RepoSetting{
		{RepoFullName: repo, LiveEgressEnabled: &falseVal},
	}}, false)
	if err != nil {
		t.Fatalf("Run() (demote) error = %v", err)
	}
	requireNoItemErrors(t, report2)

	row, err = sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox after demotion: %v", err)
	}
	if !row.DemotionTerminateRequestedAt.Valid {
		t.Error("demotion_terminate_requested_at is unset after a genuine live->shadow demotion, want it set (§30.4)")
	}

	found := false
	for _, item := range report2.Items {
		if item.Kind == "repo_setting" && item.Key == repo {
			found = true
			if !strings.Contains(item.Detail, "demotion sweep flagged 1 live sandbox") {
				t.Errorf("report item detail = %q, want it to name the demotion sweep's own flagged count", item.Detail)
			}
		}
	}
	if !found {
		t.Fatal("no repo_setting report item found for the demoted repo")
	}
}

// TestRun_RepoSettings_Demotion_CancelsInFlightPushSignal proves the
// second half: a sandbox with a push/PR decision already persisted live
// has that decision cancelled by the SAME demotion sweep.
func TestRun_RepoSettings_Demotion_CancelsInFlightPushSignal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)
	const repo = "example-org/demotion-push-repo"
	const repoURL = "https://github.com/example-org/demotion-push-repo.git"

	sessionStore := narvipg.NewSessionStore(deps.Pool)
	sandboxStore := narvipg.NewSandboxStore(deps.Pool)
	sessionID := createSessionAndLiveSandbox(ctx, t, sessionStore, sandboxStore, "demotion-push-repo", repoURL)

	live := false
	if _, err := sandboxStore.SetPendingPush(ctx, sqlcgen.SetSandboxPendingPushParams{
		SessionID: sessionID, PendingPushSuppressedInShadow: &live,
	}); err != nil {
		t.Fatalf("seed persisted push decision: %v", err)
	}

	trueVal, falseVal := true, false
	if _, err := seed.Run(ctx, deps, &seedmanifest.Manifest{RepoSettings: []seedmanifest.RepoSetting{
		{RepoFullName: repo, LiveEgressEnabled: &trueVal},
	}}, false); err != nil {
		t.Fatalf("Run() (promote) error = %v", err)
	}

	report2, err := seed.Run(ctx, deps, &seedmanifest.Manifest{RepoSettings: []seedmanifest.RepoSetting{
		{RepoFullName: repo, LiveEgressEnabled: &falseVal},
	}}, false)
	if err != nil {
		t.Fatalf("Run() (demote) error = %v", err)
	}
	requireNoItemErrors(t, report2)

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !row.PendingPushCancelled {
		t.Error("pending_push_cancelled = false after a demotion sweep, want true (§30.4: demotion must cancel in-flight push signals)")
	}
}
