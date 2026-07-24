//go:build integration

// Integration tests proving Step 39's ("identities + full RBAC", §13.2)
// own auto-linking wiring actually fires from a REAL POST /webhooks/linear
// request -- mirrors webhook_integration_test.go's own conventions
// exactly (testcontainers Postgres, a real linear.NewWebhookHandler,
// synthetic real-shaped payloads), plus a real httptest.Server standing in
// for Linear's own GraphQL API (the user(id) { email } query
// GetUserEmail calls).
package linear_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/platform"
)

// newLinearGraphQLStub stands in for Linear's real GraphQL API, answering
// EVERY request's own "user(id) { email }" query with email regardless of
// the requested id -- these tests only ever ask about one id at a time,
// so this stub does not bother inspecting the request body.
func newLinearGraphQLStub(t *testing.T, email string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"user": map[string]any{"email": email}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// installLinearFixture upserts a linear_installations row for
// organizationID so decryptLinearAccessToken (identity.go) succeeds --
// mirrors internal/adapters/inbound/linear/callback.go's own real
// EncryptToken-then-Upsert sequencing.
func installLinearFixture(ctx context.Context, t *testing.T, pool *pgxpool.Pool, organizationID string, tokenEncryptionKey []byte) {
	t.Helper()
	encrypted, err := platform.EncryptToken(tokenEncryptionKey, []byte("fake-access-token"))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	installations := narvipg.NewLinearInstallationStore(pool)
	if _, err := installations.Upsert(ctx, sqlcgen.UpsertLinearInstallationParams{
		OrganizationID:       organizationID,
		AppUserID:            "app-user-1",
		AccessTokenEncrypted: encrypted,
		ExpiresAt:            pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("Upsert installation: %v", err)
	}
}

// agentSessionCreatedPayloadWithCreator mirrors agentSessionCreatedPayload
// (webhook_integration_test.go) but also sets creatorId -- the field this
// Step's own auto-linking wiring reads (payload.go's own
// AgentSession.CreatorID).
func agentSessionCreatedPayloadWithCreator(agentSessionID, organizationID, creatorID string) []byte {
	body := fmt.Sprintf(`{
		"action": "created",
		"type": "AgentSessionEvent",
		"organizationId": %q,
		"webhookTimestamp": %d,
		"agentSession": {
			"id": %q,
			"creatorId": %q,
			"issue": {"identifier": "ENG-1", "title": "A fixture issue"},
			"url": "https://linear.app/narvi/issue/ENG-1"
		},
		"promptContext": "context"
	}`, organizationID, time.Now().UnixMilli(), agentSessionID, creatorID)
	return []byte(body)
}

// agentSessionPromptedPayloadWithUser mirrors handlePrompted's own real
// wire shape, setting agentActivity.userId -- the field this Step's own
// auto-linking wiring reads for a REPLY (payload.go's own
// AgentActivity.UserID), distinct from the session's own creatorId.
func agentSessionPromptedPayloadWithUser(agentSessionID, organizationID, userID, body string) []byte {
	payload := fmt.Sprintf(`{
		"action": "prompted",
		"type": "AgentSessionEvent",
		"organizationId": %q,
		"webhookTimestamp": %d,
		"agentSession": {"id": %q},
		"agentActivity": {
			"userId": %q,
			"content": {"type": "prompt", "body": %q}
		}
	}`, organizationID, time.Now().UnixMilli(), agentSessionID, userID, body)
	return []byte(payload)
}

// TestWebhookHandler_Created_AutoLinksUniqueEmailMatch proves a `created`
// event whose creatorId's fetched profile email matches EXACTLY one
// existing user auto-links it and attributes the new session's own
// created_by to that user (not bot attribution) -- §13.2 steps 1-3.
func TestWebhookHandler_Created_AutoLinksUniqueEmailMatch(t *testing.T) {
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)

	organizationID := "org-autolink-1"
	tokenEncryptionKey := deps.TokenEncryptionKey
	installLinearFixture(context.Background(), t, pool, organizationID, tokenEncryptionKey)

	graphqlStub := newLinearGraphQLStub(t, "matched@example.com")
	deps.LinearClient = linearapi.New(graphqlStub.Client(), graphqlStub.URL)

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(context.Background(), sqlcgen.CreateUserParams{
		PrimaryEmail: "matched@example.com", DisplayName: "Matched", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	deps.IdentityLink = identitylink.Deps{
		Pool:          pool,
		Users:         users,
		Identities:    narvipg.NewIdentityStore(pool),
		LinkPrompts:   narvipg.NewIdentityLinkPromptStore(pool),
		AuditLog:      deps.AuditLog,
		PublicBaseURL: "https://narvi.example.com",
		PromptTTL:     time.Hour,
	}

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-autolink-1"
	body := agentSessionCreatedPayloadWithCreator(agentSessionID, organizationID, "linear-creator-1")

	rec := postWebhook(t, handler, body, "delivery-autolink-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ctx := context.Background()
	var createdBy string
	if err := pool.QueryRow(ctx,
		`SELECT created_by::text FROM sessions WHERE spawn_source = 'linear' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&createdBy); err != nil {
		t.Fatalf("query session created_by: %v", err)
	}
	if createdBy != matchedUser.ID.String() {
		t.Errorf("session created_by = %q, want %q (the auto-linked user, not bot attribution)", createdBy, matchedUser.ID.String())
	}

	identity, err := narvipg.NewIdentityStore(pool).GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderLinear, "linear-creator-1")
	if err != nil {
		t.Fatalf("GetByProviderAndExternalID: %v", err)
	}
	if identity.LinkedVia != sqlcgen.IdentityLinkedViaAutoEmail {
		t.Errorf("LinkedVia = %v, want auto_email", identity.LinkedVia)
	}
}

// TestWebhookHandler_Prompted_UnknownUserCreatesLinkPromptButStillCreatesTurn
// proves a `prompted` reply from a NEVER-BEFORE-SEEN Linear user whose
// fetched email matches no one still creates the turn (bot attribution,
// §13.2's own "the action proceeds ... until linked") AND leaves a
// link-prompt row behind (§13.2 step 4).
func TestWebhookHandler_Prompted_UnknownUserCreatesLinkPromptButStillCreatesTurn(t *testing.T) {
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)

	organizationID := "org-prompt-1"
	installLinearFixture(context.Background(), t, pool, organizationID, deps.TokenEncryptionKey)

	graphqlStub := newLinearGraphQLStub(t, "nobody-matches@example.com")
	deps.LinearClient = linearapi.New(graphqlStub.Client(), graphqlStub.URL)

	deps.IdentityLink = identitylink.Deps{
		Pool:          pool,
		Users:         narvipg.NewUserStore(pool),
		Identities:    narvipg.NewIdentityStore(pool),
		LinkPrompts:   narvipg.NewIdentityLinkPromptStore(pool),
		AuditLog:      deps.AuditLog,
		PublicBaseURL: "https://narvi.example.com",
		PromptTTL:     time.Hour,
	}

	handler := linear.NewWebhookHandler(deps)

	agentSessionID := "agent-session-prompt-1"

	// First: `created`, no creatorId (automation-initiated) -- keeps this
	// test focused purely on the `prompted` reply's own actor resolution.
	createdBody := []byte(fmt.Sprintf(`{
		"action": "created",
		"type": "AgentSessionEvent",
		"organizationId": %q,
		"webhookTimestamp": %d,
		"agentSession": {"id": %q},
		"promptContext": "context"
	}`, organizationID, time.Now().UnixMilli(), agentSessionID))
	rec := postWebhook(t, handler, createdBody, "delivery-prompt-created-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("created status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// The `created` event's own initial turn stays 'pending' in this test
	// (no real sandbox provider is wired, so nothing ever dispatches it) --
	// mark it 'completed' so the `prompted` event below can create its OWN
	// turn instead of being dropped by the EXISTING (unrelated to this
	// Step) "one open turn per session" precondition (webhook.go's own
	// hasOpenTurn), keeping this test's own focus on identity resolution,
	// not turn-lifecycle mechanics.
	if _, err := pool.Exec(context.Background(),
		`UPDATE turns SET status = 'completed' WHERE session_id = (SELECT session_id FROM linear_agent_sessions WHERE agent_session_id = $1)`,
		agentSessionID,
	); err != nil {
		t.Fatalf("mark fixture turn completed: %v", err)
	}

	promptedBody := agentSessionPromptedPayloadWithUser(agentSessionID, organizationID, "linear-unknown-user-1", "please also fix the tests")
	rec = postWebhook(t, handler, promptedBody, "delivery-prompt-prompted-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("prompted status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ctx := context.Background()
	var turnCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM turns`).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != 2 {
		t.Errorf("turnCount = %d, want 2 (the created event's own initial turn, plus the prompted reply's)", turnCount)
	}

	linkPrompts := narvipg.NewIdentityLinkPromptStore(pool)
	if _, err := linkPrompts.GetLatestForProviderAndExternalID(ctx, sqlcgen.IdentityProviderLinear, "linear-unknown-user-1"); err != nil {
		t.Errorf("GetLatestForProviderAndExternalID = %v, want a real link-prompt row", err)
	}
}
