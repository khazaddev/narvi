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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// recordedIdentityRequest captures one request a fake Slack API server
// observed -- enough for this file's own ephemeral-vs-public-delivery
// assertions (which endpoint, the decoded JSON body).
type recordedIdentityRequest struct {
	path string
	body map[string]any
}

// newFakeSlackRecordingWithUsersInfo mirrors newFakeSlackWithUsersInfo
// above, but ALSO records every request it observes (path + decoded JSON
// body) onto the returned channel -- used by this file's own tests
// proving the magic-link identity notice is delivered via
// chat.postEphemeral (scoped to one user), never chat.postMessage (the
// whole channel/thread), per this Step's own security-remediation fix
// (ack.go's own postEphemeral doc comment).
func newFakeSlackRecordingWithUsersInfo(t *testing.T, userID, email string) (*httptest.Server, <-chan recordedIdentityRequest) {
	t.Helper()
	requests := make(chan recordedIdentityRequest, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/users.info" && r.URL.Query().Get("user") == userID {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"user": map[string]any{"profile": map[string]any{"email": email}},
			})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- recordedIdentityRequest{path: r.URL.Path, body: body}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server, requests
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
		Participants:        narvipg.NewParticipantStore(pool),
		SigningSecret:       testSigningSecret,
		Timeouts:            platform.DefaultTimeouts(),
	})

	// CreatedBy = matchedUser.ID: this Step's own RBAC gate (domain/authz.
	// Authorize(ActionApprovePlan)) now requires a `member` actor to own
	// or have joined the target session -- see
	// TestInteractivityHandler_BlockActions_ApprovePlan_DeniedForUnownedMember
	// below for the counterpart proving a member WITHOUT ownership is
	// rejected.
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack, CreatedBy: matchedUser.ID})
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

// TestInteractivityHandler_BlockActions_ApprovePlan_DeniedForUnownedMember
// is this Step's own regression test for a confirmed security review
// finding: BEFORE this fix, an approve_plan click from a Slack user who
// auto-links to a REAL Narvi user (any role, any ownership) unconditionally
// decided the plan -- domain/authz.Authorize was never consulted at all.
// This proves a `member` whose auto-linked account neither created NOR
// joined the target session is now REJECTED: the plan stays
// awaiting_approval, decided_by stays NULL -- exactly like the REST
// /api/sessions/:id/plans/:planId/approve endpoint already behaves for
// the identical (role, ownership) combination (canActOnPlan, planauthz.go).
// The identity itself still auto-links (this Step's own auto-linking work
// is not rolled back by a denied action) -- only the STATE-CHANGING EFFECT
// is refused.
func TestInteractivityHandler_BlockActions_ApprovePlan_DeniedForUnownedMember(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "unowned-member@example.com", DisplayName: "Unowned Member", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	fakeSlack := newFakeSlackWithUsersInfo(t, "U-UNOWNED", "unowned-member@example.com")
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
		Participants:        narvipg.NewParticipantStore(pool),
		SigningSecret:       testSigningSecret,
		Timeouts:            platform.DefaultTimeouts(),
	})

	// Deliberately NO CreatedBy set, and no participants row -- matchedUser
	// neither created nor joined this session.
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
	payload := blockActionsPayloadJSONWithUser(slackapi.ActionApprovePlan, value, "C123", "1700000000.000200", "trigger-2", "U-UNOWNED")

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
	if updatedPlan.Status != sqlcgen.PlanStatusAwaitingApproval {
		t.Errorf("Status = %v, want %v (denied by authz, must not decide)", updatedPlan.Status, sqlcgen.PlanStatusAwaitingApproval)
	}
	if updatedPlan.DecidedBy.Valid {
		t.Errorf("DecidedBy = %v, want invalid (denied -- never decided)", updatedPlan.DecidedBy)
	}

	// The identity itself still auto-links -- only the effect is denied.
	identity, err := narvipg.NewIdentityStore(pool).GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderSlack, "U-UNOWNED")
	if err != nil {
		t.Fatalf("GetByProviderAndExternalID: %v", err)
	}
	if identity.UserID != matchedUser.ID {
		t.Errorf("identity.UserID = %v, want %v", identity.UserID, matchedUser.ID)
	}
}

// TestInteractivityHandler_BlockActions_ApprovePlan_DeniedForViewerEvenIfOwned
// proves the role gate itself, independent of ownership: a `viewer` who
// DOES own the session (CreatedBy == the auto-linked user) is STILL denied
// -- §13.3's own matrix has no own/joined carve-out for viewer at all
// (domain/authz's own matrix.go: ActionApprovePlan's allowIfOwned set is
// {member} only).
func TestInteractivityHandler_BlockActions_ApprovePlan_DeniedForViewerEvenIfOwned(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "owner-viewer@example.com", DisplayName: "Owner Viewer", Role: sqlcgen.UserRoleViewer,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	fakeSlack := newFakeSlackWithUsersInfo(t, "U-VIEWER", "owner-viewer@example.com")
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
		Participants:        narvipg.NewParticipantStore(pool),
		SigningSecret:       testSigningSecret,
		Timeouts:            platform.DefaultTimeouts(),
	})

	// Owned BY the viewer -- proves the denial is the ROLE gate, not the
	// ownership carve-out.
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack, CreatedBy: matchedUser.ID})
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
	payload := blockActionsPayloadJSONWithUser(slackapi.ActionApprovePlan, value, "C123", "1700000000.000300", "trigger-3", "U-VIEWER")

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
	if updatedPlan.Status != sqlcgen.PlanStatusAwaitingApproval {
		t.Errorf("Status = %v, want %v (viewer must never decide, even on an owned session)", updatedPlan.Status, sqlcgen.PlanStatusAwaitingApproval)
	}
	if updatedPlan.DecidedBy.Valid {
		t.Errorf("DecidedBy = %v, want invalid", updatedPlan.DecidedBy)
	}
}

// TestInteractivityHandler_ViewSubmission_DeniedForUnownedMember proves
// the "Request changes" modal submission -- Linear's/Slack's own
// ActionPromptSession-gated turn creation -- is likewise denied for an
// auto-linked `member` with no ownership/participation in the target
// session, and responds with Slack's own "response_action": "errors"
// shape (this payload type has no channel/thread to post an ordinary
// denial message into, see interactive.go's own handleViewSubmission doc
// comment) instead of silently closing the modal.
func TestInteractivityHandler_ViewSubmission_DeniedForUnownedMember(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	users := narvipg.NewUserStore(pool)
	_, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "unowned-submitter@example.com", DisplayName: "Unowned Submitter", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	fakeSlack := newFakeSlackWithUsersInfo(t, "U-SUBMITTER", "unowned-submitter@example.com")
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
		Participants:        narvipg.NewParticipantStore(pool),
		SigningSecret:       testSigningSecret,
		Timeouts:            platform.DefaultTimeouts(),
	})

	// No CreatedBy, no participants row -- the submitter neither created
	// nor joined this session.
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

	privateMetadata := slackapi.EncodePlanActionValue(plan.ID.String(), session.ID.String())
	viewSubmission := map[string]any{
		"type": "view_submission",
		"user": map[string]string{"id": "U-SUBMITTER"},
		"view": map[string]any{
			"callback_id":      slackapi.RequestChangesCallbackID,
			"private_metadata": privateMetadata,
			"state": map[string]any{
				"values": map[string]any{
					slackapi.RequestChangesBlockID: map[string]any{
						slackapi.RequestChangesActionID: map[string]any{
							"type":  "plain_text_input",
							"value": "please also fix the tests",
						},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(viewSubmission)
	if err != nil {
		t.Fatalf("marshal view_submission payload: %v", err)
	}

	req := signedInteractivityRequest(t, string(raw))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"response_action":"errors"`) {
		t.Errorf("body = %s, want a response_action:errors body (denied by authz)", rec.Body.String())
	}

	turnsAfter, err := turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnsAfter) != 1 {
		t.Errorf("len(turns) = %d, want 1 (only the seeded producing turn -- denied request-changes turn must never be created)", len(turnsAfter))
	}
}

// appMentionEnvelopeWithUser mirrors handler_integration_test.go's own
// appMentionEnvelope exactly, except the event's "user" field is a
// parameter rather than the fixed "U0TESTUSER" -- this file's own tests
// need to control which Slack user id an app_mention is attributed to, so
// resolveSlackActor (identity.go) can be exercised against a REAL,
// controllable identity.
func appMentionEnvelopeWithUser(eventID, channel, ts, threadTS, text, userID string) string {
	event := map[string]string{
		"type":    "app_mention",
		"channel": channel,
		"user":    userID,
		"text":    text,
		"ts":      ts,
	}
	if threadTS != "" {
		event["thread_ts"] = threadTS
	}
	eventJSON, _ := json.Marshal(event)
	return fmt.Sprintf(`{"type":"event_callback","event_id":%q,"team_id":"T0TEST","event":%s}`, eventID, eventJSON)
}

// newSlackHandlerRigForIdentityTests wires a real slack.NewHandler (Events
// API ingress) against pool, using recordingSlackServer as its SlackClient
// AND its in-thread ack client -- unlike newSlackTestRig
// (handler_integration_test.go), this rig is given a REAL, wired
// identitylink.Deps, so a fixture user can actually auto-link.
func newSlackHandlerRigForIdentityTests(t *testing.T, pool *pgxpool.Pool, recordingSlackServer *httptest.Server, auditLog *narvipg.AuditLogStore) *slackTestRig {
	t.Helper()
	ctx := context.Background()

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	deliveries := narvipg.NewWebhookDeliveryStore(pool)
	threads := narvipg.NewSlackThreadSessionStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	handler := slack.NewHandler(slack.Deps{
		Pool:            pool,
		Sessions:        sessions,
		Turns:           turns,
		Environments:    environments,
		Registry:        registry,
		Deliveries:      deliveries,
		Threads:         threads,
		AuditLog:        auditLog,
		SigningSecret:   testSigningSecret,
		BotToken:        "test-bot-token",
		DefaultRepoName: "narvi",
		DefaultRepoURL:  "https://github.com/khazaddev/narvi",
		TimestampWindow: 5 * time.Minute,
		SlackAPIBaseURL: recordingSlackServer.URL,
		AckTimeout:      platform.DefaultTimeouts().SlackAckTimeout,
		SlackClient:     slackapi.New(recordingSlackServer.Client(), recordingSlackServer.URL, "test-bot-token"),
		Timeouts:        platform.DefaultTimeouts(),
		IdentityLink:    newIdentityLinkDepsForTest(pool, auditLog),
	})

	return &slackTestRig{handler: handler, pool: pool, sessions: sessions, turns: turns, threads: threads}
}

// TestHandler_AppMention_CreateSessionDeniedForViewer is this Step's own
// regression test for a confirmed security review finding: BEFORE this
// fix, an app_mention from a Slack user who auto-links to a REAL Narvi
// user (any role) unconditionally created a new session -- domain/authz.
// Authorize was never consulted at all. This proves a `viewer`'s
// auto-linked account is now REJECTED: no session/thread mapping is ever
// created for this event -- exactly like the REST /api/sessions endpoint
// already rejects a viewer's own CreateSession call.
func TestHandler_AppMention_CreateSessionDeniedForViewer(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "viewer-mentioner@example.com", DisplayName: "Viewer Mentioner", Role: sqlcgen.UserRoleViewer,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	fakeSlack := newFakeSlackWithUsersInfo(t, "U-VIEWER-MENTION", "viewer-mentioner@example.com")
	auditLog := narvipg.NewAuditLogStore(pool)
	rig := newSlackHandlerRigForIdentityTests(t, pool, fakeSlack, auditLog)

	envelope := appMentionEnvelopeWithUser("Ev0VIEWERDENY001", "C0VIEWERDENY", "1700000040.000100", "", "please help", "U-VIEWER-MENTION")
	req := signedSlackRequest(t, envelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	if _, err := rig.threads.Get(ctx, "C0VIEWERDENY", "1700000040.000100"); err == nil {
		t.Error("expected NO thread mapping row (denied by authz), got one")
	}

	var sessionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Errorf("session count = %d, want 0 (a viewer must never create a session, even auto-linked)", sessionCount)
	}

	// The identity itself still auto-links -- only the effect is denied.
	identity, err := narvipg.NewIdentityStore(pool).GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderSlack, "U-VIEWER-MENTION")
	if err != nil {
		t.Fatalf("GetByProviderAndExternalID: %v", err)
	}
	if identity.UserID != matchedUser.ID {
		t.Errorf("identity.UserID = %v, want %v", identity.UserID, matchedUser.ID)
	}
}

// TestHandler_AppMention_IdentityNoticeDeliveredEphemerally is this Step's
// own regression test for the SECOND confirmed security finding (magic-
// link identity hijack via non-ephemeral channel delivery): BEFORE this
// fix, the magic-link notice (or the "connected your account" confirmation)
// was appended to the ordinary, whole-channel-visible chat.postMessage ack
// -- ANY other member of a shared channel with an authenticated Narvi web
// session could open the link first and hijack the pending identity link.
// This proves the notice is now delivered via chat.postEphemeral, scoped
// to the mentioning user (Slack's own "user" field), and NEVER appears in
// the whole-channel-visible chat.postMessage ack at all.
func TestHandler_AppMention_IdentityNoticeDeliveredEphemerally(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	// "nobody-matches" -- zero email matches, so Resolve mints a fresh
	// magic-link prompt (identitylink.Resolve's own "never guess" branch),
	// giving this test a real, sensitive magic-link URL as its own
	// notice text (a stronger proof than the "connected your account"
	// confirmation alone).
	fakeSlack, requests := newFakeSlackRecordingWithUsersInfo(t, "U-EPHEMERAL", "nobody-matches@example.com")
	auditLog := narvipg.NewAuditLogStore(pool)
	rig := newSlackHandlerRigForIdentityTests(t, pool, fakeSlack, auditLog)

	envelope := appMentionEnvelopeWithUser("Ev0EPHEMERAL001", "C0EPHEMERAL", "1700000050.000100", "", "please help", "U-EPHEMERAL")
	req := signedSlackRequest(t, envelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	linkPrompts := narvipg.NewIdentityLinkPromptStore(pool)
	if _, err := linkPrompts.GetLatestForProviderAndExternalID(ctx, sqlcgen.IdentityProviderSlack, "U-EPHEMERAL"); err != nil {
		t.Fatalf("GetLatestForProviderAndExternalID: %v", err)
	}
	// The prompt row's own existence (above) proves a link was minted; its
	// plaintext nonce is never persisted (only its hash), so this test
	// inspects the REQUESTS this handler actually made instead, below.
	magicLinkPath := identitylink.MagicLinkPath

	// rig.handler(rec, req) above already ran the WHOLE handler
	// synchronously (every ack/notice call it makes happens before that
	// call returns) -- requests is a buffered channel, so every request
	// it ever sends is already sitting in it by now; drained
	// non-blockingly, never waiting on a timer.
	var sawEphemeralWithLink, sawPublicWithLink bool
drain:
	for {
		select {
		case got := <-requests:
			text, _ := got.body["text"].(string)
			switch got.path {
			case "/chat.postEphemeral":
				if strings.Contains(text, magicLinkPath) {
					sawEphemeralWithLink = true
					if got.body["user"] != "U-EPHEMERAL" {
						t.Errorf("chat.postEphemeral user = %v, want %q (scoped to the mentioning user)", got.body["user"], "U-EPHEMERAL")
					}
				}
			case "/chat.postMessage":
				if strings.Contains(text, magicLinkPath) {
					sawPublicWithLink = true
				}
			}
		default:
			break drain
		}
	}

	if !sawEphemeralWithLink {
		t.Error("no chat.postEphemeral call carried the magic-link notice -- want it delivered privately")
	}
	if sawPublicWithLink {
		t.Error("chat.postMessage (whole-channel-visible) carried the magic-link notice -- this is the confirmed hijack path, must never happen")
	}
}
