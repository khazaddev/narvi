//go:build integration

// Integration tests proving Step 39's ("identities + full RBAC", §13.2)
// own auto-linking wiring actually fires from a REAL POST
// /webhooks/slack/interactive request -- mirrors
// interactive_integration_test.go's own conventions (testcontainers
// Postgres, a real slack.NewInteractivityHandler, synthetic real-shaped
// payloads), plus a fake Slack API server that ALSO answers /users.info
// realistically (interactive_integration_test.go's own fakeSlack answers
// every path with a bare {"ok":true}, which is enough for chat.update/
// views.open but reports no email at all for users.info -- this file's
// own stub is deliberately richer).
package slack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// newFakeSlackWithUsersInfo builds a fake Slack API server answering
// EVERY path (chat.postMessage/chat.update/views.open) with a bare
// {"ok":true} -- EXCEPT /users.info, which answers with a real profile
// email for userID, any other user id getting {"ok":true} with no email
// at all (mirrors newLinearGraphQLStub's own "one email, any id" simplicity
// for these tests' own purposes).
func newFakeSlackWithUsersInfo(t *testing.T, userID, email string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/users.info" && r.URL.Query().Get("user") == userID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"user": map[string]any{"profile": map[string]any{"email": email}},
			})
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// blockActionsPayloadJSONWithUser mirrors blockActionsPayloadJSON
// (interactive_integration_test.go, same package) but also sets the
// top-level "user" field -- the field this Step's own auto-linking wiring
// reads (interactive.go's own blockActionsPayload.User).
func blockActionsPayloadJSONWithUser(actionID, value, channel, messageTS, triggerID, userID string) string {
	payload := map[string]any{
		"type":       "block_actions",
		"trigger_id": triggerID,
		"channel":    map[string]string{"id": channel},
		"message":    map[string]string{"ts": messageTS},
		"actions":    []map[string]string{{"action_id": actionID, "value": value}},
		"user":       map[string]string{"id": userID},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func newIdentityLinkDepsForTest(pool *pgxpool.Pool, auditLog *narvipg.AuditLogStore) identitylink.Deps {
	return identitylink.Deps{
		Pool:          pool,
		Users:         narvipg.NewUserStore(pool),
		Identities:    narvipg.NewIdentityStore(pool),
		LinkPrompts:   narvipg.NewIdentityLinkPromptStore(pool),
		AuditLog:      auditLog,
		PublicBaseURL: "https://narvi.example.com",
		PromptTTL:     time.Hour,
	}
}

// TestInteractivityHandler_BlockActions_ApprovePlan_AutoLinksUniqueMatch
// proves an approve_plan click from a Slack user whose fetched profile
// email matches EXACTLY one existing user results in plans.decided_by
// being that user's own id -- not bot attribution -- and creates the
// identities row (linked_via=auto_email).
func TestInteractivityHandler_BlockActions_ApprovePlan_AutoLinksUniqueMatch(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "slack-clicker@example.com", DisplayName: "Slack Clicker", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	fakeSlack := newFakeSlackWithUsersInfo(t, "U-CLICKER", "slack-clicker@example.com")
	slackClient := slackapi.New(fakeSlack.Client(), fakeSlack.URL, "test-bot-token")

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	handler := slack.NewInteractivityHandler(slack.InteractiveDeps{
		Pool:                pool,
		Sessions:            sessions,
		Turns:               turns,
		Plans:               plans,
		Outbox:              narvipg.NewOutboxStore(pool),
		LinearAgentSessions: narvipg.NewLinearAgentSessionStore(pool),
		Registry:            registry,
		SlackClient:         slackClient,
		AuditLog:            auditLog,
		IdentityLink:        newIdentityLinkDepsForTest(pool, auditLog),
		SigningSecret:       testSigningSecret,
		Timeouts:            platform.DefaultTimeouts(),
	})

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("seed producing turn: %v", err)
	}
	plan, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: session.ID, TurnID: turn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval})
	if err != nil {
		t.Fatalf("seed awaiting_approval plan: %v", err)
	}

	value := slackapi.EncodePlanActionValue(plan.ID.String(), session.ID.String())
	payload := blockActionsPayloadJSONWithUser(slackapi.ActionApprovePlan, value, "C123", "1700000000.000100", "trigger-1", "U-CLICKER")

	req := signedInteractivityRequest(t, payload)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updatedPlan, err := plans.Get(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if !updatedPlan.DecidedBy.Valid || updatedPlan.DecidedBy != matchedUser.ID {
		t.Errorf("DecidedBy = %v, want %v (the auto-linked user, not bot attribution)", updatedPlan.DecidedBy, matchedUser.ID)
	}

	identity, err := narvipg.NewIdentityStore(pool).GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderSlack, "U-CLICKER")
	if err != nil {
		t.Fatalf("GetByProviderAndExternalID: %v", err)
	}
	if identity.LinkedVia != sqlcgen.IdentityLinkedViaAutoEmail {
		t.Errorf("LinkedVia = %v, want auto_email", identity.LinkedVia)
	}
}
