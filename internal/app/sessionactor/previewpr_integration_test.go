//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/rwx"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves Step 57's own ("RWX provider + previews", §4.1.2 point
// 1) PR preview enqueue wiring: a real push_complete that already causes
// createPRBestEffort to open (or recover) a PR ALSO writes a "preview"-
// typed artifact row plus the two new outbox rows (rwx_preview_dispatch,
// github_preview_link) when — and only when — the pushed repo's own RWX
// preview setting is fully configured. See previewpr.go's own top comment
// for the full design this exercises.

// getOutboxRowsForSessionByKind fetches every outbox row for sessionID
// whose kind matches -- mirrors outboxenqueue_integration_test.go's own
// getSoleOutboxRowForSession/countOutboxRowsForSession precedent, just
// filtered by kind since THIS Step enqueues two DIFFERENT kinds per
// preview (rwx_preview_dispatch, github_preview_link) in the same
// transaction, unlike that file's own single-kind-per-session tests.
func getOutboxRowsForSessionByKind(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, kind ports.NotificationKind) []sqlcgen.Outbox {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT id, session_id, kind, payload, status, attempts, next_attempt_at, delivered_at, last_error, created_at FROM outbox WHERE session_id = $1 AND kind = $2`,
		sessionID, string(kind))
	if err != nil {
		t.Fatalf("query outbox rows for kind %q: %v", kind, err)
	}
	defer rows.Close()

	var out []sqlcgen.Outbox
	for rows.Next() {
		var row sqlcgen.Outbox
		if err := rows.Scan(&row.ID, &row.SessionID, &row.Kind, &row.Payload, &row.Status, &row.Attempts, &row.NextAttemptAt, &row.DeliveredAt, &row.LastError, &row.CreatedAt); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		out = append(out, row)
	}
	return out
}

// TestHandleSandboxEvent_PushComplete_PreviewSettingsConfigured_EnqueuesPreview
// proves the happy path: a repo whose RWX preview setting is fully
// configured gets a "preview"-typed artifact row (the friendly URL) plus
// exactly one rwx_preview_dispatch row and one github_preview_link row,
// each correctly shaped, alongside the ordinary "pr"-typed artifact
// createPRBestEffort already wrote before this Step existed.
func TestHandleSandboxEvent_PushComplete_PreviewSettingsConfigured_EnqueuesPreview(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	userStore := narvipg.NewUserStore(pool)
	identityStore := narvipg.NewIdentityStore(pool)

	user, err := userStore.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("preview-test-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "Preview Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const plaintextToken = "gh-fake-oauth-token"
	encrypted, err := platform.EncryptToken(testTokenEncryptionKey, []byte(plaintextToken))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	email := user.PrimaryEmail
	if _, err := identityStore.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           fmt.Sprintf("preview-test-external-%d", time.Now().UnixNano()),
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
		"repo1", "https://github.com/preview-acme/repo1.git", "feature-preview")

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	// This repo's own RWX preview setting -- {dispatchKey, endpointTemplate,
	// orgSlug} -- is fully configured (§4.1.2 point 1: "absent = feature
	// off"; here, present = on).
	repoSettingsStore := narvipg.NewRepoSettingsStore(pool)
	if _, err := repoSettingsStore.UpsertPreviewSettings(ctx, "preview-acme/repo1",
		"preview-build", "myapp-pr-{pr}", "preview-acme-org"); err != nil {
		t.Fatalf("upsert rwx preview settings: %v", err)
	}

	sourceControl := &fakeSourceControl{
		nextRef:           ports.PRRef{Number: 99, URL: "https://github.com/preview-acme/repo1/pull/99"},
		defaultBranchName: "main",
	}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete",
		Gen:  1,
		Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-preview", "cafef00d"),
	})

	wantFriendlyURL := rwx.FriendlyPreviewURL("myapp-pr-{pr}", 99, "preview-acme-org")

	artifactStore := narvipg.NewArtifactStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		rows, err := artifactStore.ListForSession(ctx, sessionID)
		return err == nil && len(rows) == 2
	})

	rows, err := artifactStore.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("artifact count = %d, want 2 (one pr, one preview)", len(rows))
	}

	var sawPR, sawPreview bool
	for _, row := range rows {
		switch row.Type {
		case sqlcgen.ArtifactTypePr:
			sawPR = true
		case sqlcgen.ArtifactTypePreview:
			sawPreview = true
			if row.Url != wantFriendlyURL {
				t.Errorf("preview artifact url = %q, want %q", row.Url, wantFriendlyURL)
			}
		}
	}
	if !sawPR {
		t.Error("no \"pr\"-typed artifact found")
	}
	if !sawPreview {
		t.Error("no \"preview\"-typed artifact found")
	}

	dispatchRows := getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindRWXPreviewDispatch)
	if len(dispatchRows) != 1 {
		t.Fatalf("rwx_preview_dispatch outbox row count = %d, want 1", len(dispatchRows))
	}
	var dispatchPayload rwx.PreviewDispatchPayload
	if err := json.Unmarshal(dispatchRows[0].Payload, &dispatchPayload); err != nil {
		t.Fatalf("decode rwx_preview_dispatch payload: %v", err)
	}
	if dispatchPayload.DispatchKey != "preview-build" {
		t.Errorf("DispatchKey = %q, want %q", dispatchPayload.DispatchKey, "preview-build")
	}
	if dispatchPayload.Ref != "cafef00d" || dispatchPayload.HeadSHA != "cafef00d" {
		t.Errorf("Ref/HeadSHA = %q/%q, want both %q", dispatchPayload.Ref, dispatchPayload.HeadSHA, "cafef00d")
	}
	if dispatchPayload.PRNumber != 99 {
		t.Errorf("PRNumber = %d, want 99", dispatchPayload.PRNumber)
	}
	if dispatchPayload.SessionID != sessionID.String() {
		t.Errorf("SessionID = %q, want %q", dispatchPayload.SessionID, sessionID.String())
	}

	linkRows := getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindGitHubPreviewLink)
	if len(linkRows) != 1 {
		t.Fatalf("github_preview_link outbox row count = %d, want 1", len(linkRows))
	}
	var linkPayload githubapi.PreviewLinkPayload
	if err := json.Unmarshal(linkRows[0].Payload, &linkPayload); err != nil {
		t.Fatalf("decode github_preview_link payload: %v", err)
	}
	if linkPayload.Owner != "preview-acme" || linkPayload.Repo != "repo1" {
		t.Errorf("Owner/Repo = %q/%q, want preview-acme/repo1", linkPayload.Owner, linkPayload.Repo)
	}
	if linkPayload.SHA != "cafef00d" {
		t.Errorf("SHA = %q, want %q", linkPayload.SHA, "cafef00d")
	}
	if linkPayload.TargetURL != wantFriendlyURL {
		t.Errorf("TargetURL = %q, want %q", linkPayload.TargetURL, wantFriendlyURL)
	}
	if linkPayload.Description == "" {
		t.Error("Description is empty, want the ephemerality caveat")
	}
}

// TestHandleSandboxEvent_PushComplete_NoPreviewSettings_SkipsPreview proves
// the off-by-default path: a repo with NO repo_settings row at all gets
// its ordinary "pr"-typed artifact (unaffected by this Step) but no
// "preview"-typed artifact and no new outbox rows of either new kind.
func TestHandleSandboxEvent_PushComplete_NoPreviewSettings_SkipsPreview(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	userStore := narvipg.NewUserStore(pool)
	identityStore := narvipg.NewIdentityStore(pool)

	user, err := userStore.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("no-preview-test-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "No Preview Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const plaintextToken = "gh-fake-oauth-token"
	encrypted, err := platform.EncryptToken(testTokenEncryptionKey, []byte(plaintextToken))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	email := user.PrimaryEmail
	if _, err := identityStore.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           fmt.Sprintf("no-preview-test-external-%d", time.Now().UnixNano()),
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}

	// Deliberately NO repo_settings row written for this repo at all --
	// the ordinary, off-by-default case (§4.1.2 point 1).
	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
		"repo1", "https://github.com/no-preview-acme/repo1.git", "feature-x")

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	sourceControl := &fakeSourceControl{
		nextRef:           ports.PRRef{Number: 5, URL: "https://github.com/no-preview-acme/repo1/pull/5"},
		defaultBranchName: "main",
	}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete",
		Gen:  1,
		Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", "abc999"),
	})

	artifactStore := narvipg.NewArtifactStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		rows, err := artifactStore.ListForSession(ctx, sessionID)
		return err == nil && len(rows) == 1
	})

	rows, err := artifactStore.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("artifact count = %d, want exactly 1 (only the ordinary \"pr\" artifact, no preview)", len(rows))
	}
	if rows[0].Type != sqlcgen.ArtifactTypePr {
		t.Errorf("artifact type = %s, want %s", rows[0].Type, sqlcgen.ArtifactTypePr)
	}

	if got := len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindRWXPreviewDispatch)); got != 0 {
		t.Errorf("rwx_preview_dispatch outbox row count = %d, want 0", got)
	}
	if got := len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindGitHubPreviewLink)); got != 0 {
		t.Errorf("github_preview_link outbox row count = %d, want 0", got)
	}
}
