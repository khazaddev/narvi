//go:build integration

package sessionactor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/githubapi"
	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/adapters/outbound/rwx"
	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/platform"
)

// This file proves §4.1's own ("RWX provider + previews", §4.1.2 point
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
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	// A real, full 40-character lowercase-hex sha -- FIX-2's own
	// pushedShaPattern validation (previewpr.go) rejects anything shorter,
	// so every test in this file that expects a preview to actually be
	// enqueued must use one of these, not an abbreviated placeholder.
	const validSha = "cafef00d00000000000000000000000000000000"

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete",
		Gen:  1,
		Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-preview", validSha),
	})

	wantFriendlyURL, err := rwx.FriendlyPreviewURL("myapp-pr-{pr}", 99, "preview-acme-org")
	if err != nil {
		t.Fatalf("FriendlyPreviewURL: %v", err)
	}

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
	if dispatchPayload.Ref != validSha || dispatchPayload.HeadSHA != validSha {
		t.Errorf("Ref/HeadSHA = %q/%q, want both %q", dispatchPayload.Ref, dispatchPayload.HeadSHA, validSha)
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
	if linkPayload.SHA != validSha {
		t.Errorf("SHA = %q, want %q", linkPayload.SHA, validSha)
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
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
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

// syncLogBuffer is a mutex-guarded io.Writer + String() capture buffer --
// needed here rather than reusing this package's own existing
// captureDefaultLoggerJSON/findLogEntry pair (planrecord_integration_test.go)
// specifically because the log line
// TestHandleSandboxEvent_PushComplete_MalformedSha_SkipsPreviewButCreatesPR
// below asserts on fires from createPRBestEffort/enqueuePreviewBestEffort's
// own POST-COMMIT best-effort continuation (sandboxevent.go's own doc
// comment: the ack reply is sent BEFORE these side effects ever run) --
// i.e. on the Actor's own background goroutine, genuinely concurrently
// with this test's own polling goroutine. Every EXISTING
// captureDefaultLoggerJSON caller in this package asserts on a log line
// that instead fires synchronously INSIDE the transact, before the reply
// channel send establishes a happens-before edge with the test goroutine
// -- a plain *bytes.Buffer is safe for that shape, but not for this one. A
// direct, unsynchronized buf.Bytes() read raced against the actor
// goroutine's own concurrent buf.Write() (via slog) is exactly what -race
// caught while developing this test. Mirrors internal/adapters/inbound/
// slack's and internal/adapters/inbound/linear's own identical
// syncLogBuffer precedent exactly (handler_integration_test.go there,
// commit 557a4fa, "fix(linear): stop the log-buffer assertion racing the
// async actor spawn") -- the same underlying hazard, hit for the first
// time in THIS package by this exact review fix.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureDefaultLoggerJSONSync is captureDefaultLoggerJSON's own race-safe
// variant, backed by syncLogBuffer above -- installed the SAME way, before
// GetOrSpawn, for the SAME reason (platform.Logger(ctx) resolves
// slog.Default() once, at hydrate time, and caches it on the Actor for its
// whole lifetime).
func captureDefaultLoggerJSONSync(t *testing.T) *syncLogBuffer {
	t.Helper()
	buf := &syncLogBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(origLogger) })
	return buf
}

// waitForLogEntry polls buf -- safely, via its own mutex-guarded String()
// -- until a line whose "msg" field equals wantMsg appears, or fails the
// test once timeout elapses. The read-side half of making this test safe
// against the asserted log line's own genuinely asynchronous, post-commit
// arrival (syncLogBuffer's own doc comment); mirrors this file's own
// waitUntil (integration_helpers_test.go) shape exactly, just polling a
// log buffer instead of a database row.
func waitForLogEntry(t *testing.T, buf *syncLogBuffer, timeout time.Duration, wantMsg string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, line := range bytes.Split([]byte(buf.String()), []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				t.Fatalf("unmarshal log line %q: %v", line, err)
			}
			if entry["msg"] == wantMsg {
				return entry
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line with msg %q found within %v; full log output:\n%s", wantMsg, timeout, buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// setUpPreviewTestUserAndIdentity is a small shared-setup helper for the
// remaining tests in this file: creates a user plus a real (encrypted)
// GitHub identity for it, mirroring the boilerplate every test above
// already repeats inline. Returns the created user.
func setUpPreviewTestUserAndIdentity(ctx context.Context, t *testing.T, pool *pgxpool.Pool, label string) sqlcgen.User {
	t.Helper()

	userStore := narvipg.NewUserStore(pool)
	identityStore := narvipg.NewIdentityStore(pool)

	user, err := userStore.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("%s-%d@example.com", label, time.Now().UnixNano()),
		DisplayName:  label,
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	encrypted, err := platform.EncryptToken(testTokenEncryptionKey, []byte("gh-fake-oauth-token-"+label))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	email := user.PrimaryEmail
	if _, err := identityStore.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           fmt.Sprintf("%s-external-%d", label, time.Now().UnixNano()),
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}

	return user
}

// TestHandleSandboxEvent_PushComplete_MalformedSha_SkipsPreviewButCreatesPR
// is FIX-2's own regression test: pushed.Sha comes straight off the
// sandbox-WS wire frame (contracts/sandbox-ws/v1/events.schema.json's own
// "sha": {"type": "string"}, unconstrained) and used to flow, unvalidated,
// into (a) PreviewLinkPayload.SHA -- posted to GitHub's CreateCommitStatus
// by the PLATFORM BOT token -- and (b) PreviewDispatchPayload.Ref/HeadSHA,
// the literal ref RWX itself builds. A malformed sha -- a branch name, an
// abbreviated hex prefix, uppercase hex, or outright injection-shaped
// garbage -- must never reach either payload: previewpr.go's own
// pushedShaPattern check now rejects anything but a full, lowercase,
// 40-character hex string, warn-logging and skipping ONLY the preview
// enqueue for that repo, exactly like an unconfigured preview setting --
// the ordinary "pr" artifact this Step never changes must still be created
// regardless.
func TestHandleSandboxEvent_PushComplete_MalformedSha_SkipsPreviewButCreatesPR(t *testing.T) {
	tests := []struct {
		name string
		sha  string
	}{
		{"branch name instead of a commit sha", "main"},
		{"abbreviated hex prefix", "abc123abc123"},
		// Otherwise a well-formed 40-character sha, just uppercased --
		// isolates the lowercase-only requirement specifically, rather
		// than conflating it with a length problem.
		{"uppercase hex", "CAFEF00D00000000000000000000000000000000"},
		{"shell-injection-shaped garbage", "deadbeef; rm -rf"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)

			user := setUpPreviewTestUserAndIdentity(ctx, t, pool, fmt.Sprintf("malformed-sha-test-%d", i))

			repoOwner := fmt.Sprintf("malformed-sha-acme-%d", i)
			sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
				"repo1", fmt.Sprintf("https://github.com/%s/repo1.git", repoOwner), "feature-x")

			if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
				t.Fatalf("create sandbox: %v", err)
			}

			// Fully-configured preview settings -- the ONLY thing standing
			// between this push and an enqueued preview must be the sha
			// validation itself, never a missing/partial setting.
			repoSettingsStore := narvipg.NewRepoSettingsStore(pool)
			if _, err := repoSettingsStore.UpsertPreviewSettings(ctx, repoOwner+"/repo1",
				"preview-build", "myapp-pr-{pr}", "malformed-sha-org"); err != nil {
				t.Fatalf("upsert rwx preview settings: %v", err)
			}

			sourceControl := &fakeSourceControl{
				nextRef:           ports.PRRef{Number: 11, URL: fmt.Sprintf("https://github.com/%s/repo1/pull/11", repoOwner)},
				defaultBranchName: "main",
			}
			r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			t.Cleanup(func() { _ = r.Shutdown() })

			// Installed BEFORE GetOrSpawn (captureDefaultLoggerJSONSync's
			// own doc comment): platform.Logger resolves slog.Default()
			// once, at hydrate time, and caches it on the Actor for its
			// whole lifetime.
			buf := captureDefaultLoggerJSONSync(t)

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}

			sendSandboxEventForTest(ctx, t, a, SandboxEvent{
				Type: "push_complete",
				Gen:  1,
				Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", tt.sha),
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
				t.Fatalf("artifact count = %d, want exactly 1 (only the ordinary \"pr\" artifact; a malformed sha must never produce a preview)", len(rows))
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

			waitForLogEntry(t, buf, 5*time.Second, "sessionactor: push_complete carried a malformed sha; skipping preview enqueue for this repo")
		})
	}
}

// TestHandleSandboxEvent_PushComplete_PartialPreviewSettings_TreatedAsOff
// is FIX-4(a)'s own regression test: UpsertPreviewSettings (the only store
// method every OTHER test in this file calls) always writes all three RWX
// preview columns together, so it can never by itself produce the
// partial-row shape §4.1.2 point 1's own "all three are required TOGETHER"
// rule exists to guard against. This test goes around it with a direct SQL
// insert, leaving one column NULL, to prove readPreviewSettings' own
// ANY-missing-field-means-off check (previewpr.go) really does treat that
// row identically to no row at all: no preview artifact, no dead-link URL
// ever rendered from a template with no orgSlug to pair it with, and none
// of the new outbox rows -- while the ordinary "pr" artifact is unaffected.
func TestHandleSandboxEvent_PushComplete_PartialPreviewSettings_TreatedAsOff(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	user := setUpPreviewTestUserAndIdentity(ctx, t, pool, "partial-preview-test")

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
		"repo1", "https://github.com/partial-preview-acme/repo1.git", "feature-partial")

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	// Deliberately going around UpsertPreviewSettings (which always writes
	// all three columns together): a direct SQL insert leaving
	// rwx_preview_endpoint_template NULL, with dispatch key and org slug
	// both set, is the only way to produce this partial-row shape.
	if _, err := pool.Exec(ctx,
		`INSERT INTO repo_settings (repo_full_name, rwx_preview_dispatch_key, rwx_preview_org_slug) VALUES ($1, $2, $3)`,
		"partial-preview-acme/repo1", "preview-build", "partial-preview-org",
	); err != nil {
		t.Fatalf("seed partial repo_settings row: %v", err)
	}

	sourceControl := &fakeSourceControl{
		nextRef:           ports.PRRef{Number: 77, URL: "https://github.com/partial-preview-acme/repo1/pull/77"},
		defaultBranchName: "main",
	}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
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
		Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-partial", "abcdef0123456789abcdef0123456789abcdef01"),
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
		t.Fatalf("artifact count = %d, want exactly 1 (only the ordinary \"pr\" artifact; a PARTIALLY-configured preview setting must be treated as OFF, same as no row at all)", len(rows))
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

// TestHandleSandboxEvent_PushComplete_RedeliveredFrame_DoesNotDuplicatePreview
// is FIX-1's own regression test: push_complete is an at-least-once wire
// event (internal/sandboxagent/wsbridge's own doc.go/buffer.go -- resent
// verbatim, identical MessageId and identical pushed.Sha, until acked), so
// delivering the exact SAME frame twice through the real handler path must
// enqueue exactly ONE of everything createPRBestEffort/
// enqueuePreviewBestEffort together produce -- never two.
//
// Modeled on the existing TestHandoffSentinel_Idempotent_RunningTwiceDoesNotDuplicate
// (handoffsentinel_integration_test.go), which proves the same "redelivery
// must not duplicate a push_complete side effect" shape for a different
// side effect -- but that test calls pushCompleteRaw twice, which mints a
// FRESH MessageId each time, so it actually relies on CreatePR's OWN
// idempotency plus recordPRArtifact's URL-based dedup, neither of which
// previewpr.go's own enqueue ever had. THIS test instead reuses the exact
// same raw bytes AND the exact same SandboxEvent.MessageID for both
// deliveries (pushCompleteRawWithMessageID, pushpr_integration_test.go),
// so it genuinely exercises appendRawEvent's (session_id, messageID)
// dedup and the eventInserted-gated fix itself (sandboxevent.go), not a
// downstream coincidence. See
// TestHandleSandboxEvent_PushComplete_TwoDistinctPushes_EnqueuesTwoIndependentPreviews
// below (FIX-4(b)) -- designed together with this test, per the review's
// own instruction -- for the other half of the same guarantee: two
// legitimately DIFFERENT pushes must NOT be suppressed by this same gate.
func TestHandleSandboxEvent_PushComplete_RedeliveredFrame_DoesNotDuplicatePreview(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	user := setUpPreviewTestUserAndIdentity(ctx, t, pool, "redelivered-push-test")

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
		"repo1", "https://github.com/redelivered-push-acme/repo1.git", "feature-redelivered")

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	repoSettingsStore := narvipg.NewRepoSettingsStore(pool)
	if _, err := repoSettingsStore.UpsertPreviewSettings(ctx, "redelivered-push-acme/repo1",
		"preview-build", "myapp-pr-{pr}", "redelivered-push-acme-org"); err != nil {
		t.Fatalf("upsert rwx preview settings: %v", err)
	}

	sourceControl := &fakeSourceControl{
		nextRef:           ports.PRRef{Number: 200, URL: "https://github.com/redelivered-push-acme/repo1/pull/200"},
		defaultBranchName: "main",
	}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	// ONE frame, reused verbatim for both deliveries -- same MessageId,
	// same Sha -- exactly mirroring a real sandbox-WS reconnect resending
	// its own unacked push_complete (internal/sandboxagent/wsbridge).
	const messageID = "11111111-1111-1111-1111-111111111111"
	const sha = "3333333333333333333333333333333333333333"
	raw := pushCompleteRawWithMessageID(t, messageID, sessionID.String(), 1, "repo1", "feature-redelivered", sha)

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{Type: "push_complete", Gen: 1, MessageID: messageID, Raw: raw})
	waitUntil(t, 5*time.Second, func() bool {
		return sourceControl.callCount() == 1
	})

	// Second, wire-identical redelivery of the EXACT SAME frame.
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{Type: "push_complete", Gen: 1, MessageID: messageID, Raw: raw})

	// Prove a negative (mirrors this package's own established "give the
	// post-commit-triggered goroutine every reasonable chance to have
	// already run before asserting it did not" precedent, e.g.
	// TestHandleSandboxEvent_PushComplete_UnsupportedRepoHost_SkipsPRCreation):
	// give the second delivery's own (should-be-suppressed)
	// createPRBestEffort call every reasonable chance to have run before
	// asserting the counts below never moved past one.
	time.Sleep(300 * time.Millisecond)

	if got := sourceControl.callCount(); got != 1 {
		t.Errorf("CreatePR called %d times, want exactly 1 (a wire-level redelivery of the identical push_complete frame must never re-run createPRBestEffort)", got)
	}

	artifactStore := narvipg.NewArtifactStore(pool)
	rows, err := artifactStore.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("artifact count = %d, want exactly 2 (one pr, one preview)", len(rows))
	}
	var prCount, previewCount int
	for _, row := range rows {
		switch row.Type {
		case sqlcgen.ArtifactTypePr:
			prCount++
		case sqlcgen.ArtifactTypePreview:
			previewCount++
		}
	}
	if prCount != 1 {
		t.Errorf("pr artifact count = %d, want 1", prCount)
	}
	if previewCount != 1 {
		t.Errorf("preview artifact count = %d, want 1 (the redelivery must never enqueue a second one)", previewCount)
	}

	if got := len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindRWXPreviewDispatch)); got != 1 {
		t.Errorf("rwx_preview_dispatch outbox row count = %d, want exactly 1", got)
	}
	if got := len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindGitHubPreviewLink)); got != 1 {
		t.Errorf("github_preview_link outbox row count = %d, want exactly 1", got)
	}
}

// TestHandleSandboxEvent_PushComplete_TwoDistinctPushes_EnqueuesTwoIndependentPreviews
// is FIX-4(b)'s own regression test, designed together with FIX-1's own
// TestHandleSandboxEvent_PushComplete_RedeliveredFrame_DoesNotDuplicatePreview
// above as the other half of the SAME guarantee: that test's own
// eventInserted gate (sandboxevent.go) exists to stop a wire-level
// REDELIVERY of the identical push_complete frame (same MessageId) from
// double-enqueuing a preview -- but two DIFFERENT, legitimate pushes to
// the same session/PR (different MessageIds, different shas, e.g. two
// commits pushed moments apart) must each still get their own preview
// artifact + outbox pair (previewpr.go's own top comment: "each push
// carries a genuinely NEW sha... a fresh preview artifact + outbox pair
// per push is the CORRECT behavior"). A fix that wrongly keyed the gate on
// repo/PR/session instead of the wire message id itself would pass that
// test while silently failing this one.
func TestHandleSandboxEvent_PushComplete_TwoDistinctPushes_EnqueuesTwoIndependentPreviews(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	user := setUpPreviewTestUserAndIdentity(ctx, t, pool, "two-push-preview-test")

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
		"repo1", "https://github.com/two-push-acme/repo1.git", "feature-two-push")

	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	repoSettingsStore := narvipg.NewRepoSettingsStore(pool)
	if _, err := repoSettingsStore.UpsertPreviewSettings(ctx, "two-push-acme/repo1",
		"preview-build", "myapp-pr-{pr}", "two-push-acme-org"); err != nil {
		t.Fatalf("upsert rwx preview settings: %v", err)
	}

	// The fake returns the SAME nextRef regardless of input -- simulating
	// two pushes landing on the SAME still-open PR, the realistic case
	// (recordPRArtifact's own URL-based dedup keeps that "pr" artifact
	// singular; unrelated to what this test actually asserts on).
	sourceControl := &fakeSourceControl{
		nextRef:           ports.PRRef{Number: 123, URL: "https://github.com/two-push-acme/repo1/pull/123"},
		defaultBranchName: "main",
	}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	const messageID1 = "22222222-2222-2222-2222-222222222222"
	const messageID2 = "44444444-4444-4444-4444-444444444444"
	const sha1 = "1111111111111111111111111111111111111111"
	const sha2 = "5555555555555555555555555555555555555555"

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete", Gen: 1, MessageID: messageID1,
		Raw: pushCompleteRawWithMessageID(t, messageID1, sessionID.String(), 1, "repo1", "feature-two-push", sha1),
	})
	waitUntil(t, 5*time.Second, func() bool {
		return len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindRWXPreviewDispatch)) == 1
	})

	// A second, genuinely DIFFERENT push: a fresh MessageId AND a
	// different sha.
	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete", Gen: 1, MessageID: messageID2,
		Raw: pushCompleteRawWithMessageID(t, messageID2, sessionID.String(), 1, "repo1", "feature-two-push", sha2),
	})
	waitUntil(t, 5*time.Second, func() bool {
		return len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindRWXPreviewDispatch)) == 2
	})

	dispatchRows := getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindRWXPreviewDispatch)
	if len(dispatchRows) != 2 {
		t.Fatalf("rwx_preview_dispatch outbox row count = %d, want 2 (one per distinct push)", len(dispatchRows))
	}
	gotShas := make(map[string]bool, 2)
	for _, row := range dispatchRows {
		var payload rwx.PreviewDispatchPayload
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			t.Fatalf("decode rwx_preview_dispatch payload: %v", err)
		}
		gotShas[payload.Ref] = true
	}
	if !gotShas[sha1] || !gotShas[sha2] {
		t.Errorf("rwx_preview_dispatch shas seen = %v, want both %q and %q represented", gotShas, sha1, sha2)
	}

	if got := len(getOutboxRowsForSessionByKind(ctx, t, pool, sessionID, ports.NotificationKindGitHubPreviewLink)); got != 2 {
		t.Errorf("github_preview_link outbox row count = %d, want 2 (one per distinct push)", got)
	}

	artifactStore := narvipg.NewArtifactStore(pool)
	rows, err := artifactStore.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	var previewCount int
	for _, row := range rows {
		if row.Type == sqlcgen.ArtifactTypePreview {
			previewCount++
		}
	}
	if previewCount != 2 {
		t.Errorf("preview artifact count = %d, want 2 (one per distinct push, never suppressed by the redelivery gate)", previewCount)
	}
}
