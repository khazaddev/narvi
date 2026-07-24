//go:build integration

// This file proves Step 38's ("plan mode, cross-channel", §8.1/§13.3) own
// Linear text-verdict parsing: handlePrompted's new check, ahead of its
// existing unconditional turn-creation, against a REAL Postgres instance
// -- mirrors webhook_integration_test.go's own newTestPool/newHandlerDeps/
// postWebhook conventions exactly (same package, same file's own helpers
// reused directly).
package linear_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// agentSessionPromptedPayload builds a synthetic, real-shaped
// AgentSessionEvent "prompted" webhook body carrying body as the reply's
// own agentActivity.content.body -- mirrors agentSessionCreatedPayload's
// own identical "every field name mirrors Linear's own live schema"
// precedent (webhook_integration_test.go).
func agentSessionPromptedPayload(agentSessionID, organizationID, body string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"action":           "prompted",
		"type":             "AgentSessionEvent",
		"organizationId":   organizationID,
		"webhookTimestamp": time.Now().UnixMilli(),
		"agentSession":     map[string]string{"id": agentSessionID},
		"agentActivity": map[string]any{
			"content": map[string]string{"type": "prompt", "body": body},
		},
	})
	return raw
}

// TestWebhookHandler_Prompted_ApproveKeyword_DecidesPlan proves a
// deterministic approve-keyword reply calls the shared decide-plan path
// instead of creating an ordinary turn: the plan flips to 'approved' with
// decided_by NULL (bot/channel attribution, no per-user gate for these two
// channels) and a new implementation turn is created -- exactly the SAME
// outcome the Slack button / REST endpoint produce.
func TestWebhookHandler_Prompted_ApproveKeyword_DecidesPlan(t *testing.T) {
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)
	deps.Plans = narvipg.NewPlanStore(pool)
	deps.Outbox = narvipg.NewOutboxStore(pool)
	handler := linear.NewWebhookHandler(deps)

	ctx := context.Background()
	agentSessionID := "agent-session-plan-approve"
	organizationID := "org-plan-approve"

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)
	agentSessions := narvipg.NewLinearAgentSessionStore(pool)

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceLinear})
	if err != nil {
		t.Fatalf("create linear-origin session: %v", err)
	}
	if _, err := agentSessions.Claim(ctx, agentSessionID, organizationID); err != nil {
		t.Fatalf("claim agent session: %v", err)
	}
	if err := agentSessions.SetSessionID(ctx, agentSessionID, session.ID); err != nil {
		t.Fatalf("attach session id: %v", err)
	}
	producingTurn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("seed producing turn: %v", err)
	}
	plan, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: session.ID, TurnID: producingTurn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval})
	if err != nil {
		t.Fatalf("seed awaiting_approval plan: %v", err)
	}

	body := agentSessionPromptedPayload(agentSessionID, organizationID, "approve")
	rec := postWebhook(t, handler, body, "delivery-plan-approve")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var dbStatus sqlcgen.PlanStatus
	var decidedBy pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT status, decided_by FROM plans WHERE id = $1`, plan.ID).Scan(&dbStatus, &decidedBy); err != nil {
		t.Fatalf("query plan row: %v", err)
	}
	if dbStatus != sqlcgen.PlanStatusApproved {
		t.Errorf("db status = %q, want %q", dbStatus, sqlcgen.PlanStatusApproved)
	}
	if decidedBy.Valid {
		t.Errorf("decided_by = %v, want NULL", decidedBy)
	}

	allTurns, err := turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(allTurns) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (the seeded producing turn + the new implementation turn -- the reply must NOT have created a third, ordinary turn)", len(allTurns))
	}
}

// TestWebhookHandler_Prompted_NonKeywordText_FallsThroughToOrdinaryTurn
// proves ANY non-keyword reply (including while a plan IS awaiting
// approval) falls through to the EXISTING create-turn behavior completely
// unchanged -- this Step's own explicit "no change to that half" note.
func TestWebhookHandler_Prompted_NonKeywordText_FallsThroughToOrdinaryTurn(t *testing.T) {
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)
	deps.Plans = narvipg.NewPlanStore(pool)
	deps.Outbox = narvipg.NewOutboxStore(pool)
	handler := linear.NewWebhookHandler(deps)

	ctx := context.Background()
	agentSessionID := "agent-session-plan-feedback"
	organizationID := "org-plan-feedback"

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	plans := narvipg.NewPlanStore(pool)
	agentSessions := narvipg.NewLinearAgentSessionStore(pool)

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceLinear})
	if err != nil {
		t.Fatalf("create linear-origin session: %v", err)
	}
	if _, err := agentSessions.Claim(ctx, agentSessionID, organizationID); err != nil {
		t.Fatalf("claim agent session: %v", err)
	}
	if err := agentSessions.SetSessionID(ctx, agentSessionID, session.ID); err != nil {
		t.Fatalf("attach session id: %v", err)
	}
	producingTurn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("seed producing turn: %v", err)
	}
	plan, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: session.ID, TurnID: producingTurn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval})
	if err != nil {
		t.Fatalf("seed awaiting_approval plan: %v", err)
	}

	body := agentSessionPromptedPayload(agentSessionID, organizationID, "please keep the env fallback path")
	rec := postWebhook(t, handler, body, "delivery-plan-feedback")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var dbStatus sqlcgen.PlanStatus
	if err := pool.QueryRow(ctx, `SELECT status FROM plans WHERE id = $1`, plan.ID).Scan(&dbStatus); err != nil {
		t.Fatalf("query plan row: %v", err)
	}
	if dbStatus != sqlcgen.PlanStatusAwaitingApproval {
		t.Errorf("db status = %q, want %q (a non-keyword reply must never decide the plan)", dbStatus, sqlcgen.PlanStatusAwaitingApproval)
	}

	allTurns, err := turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(allTurns) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (the seeded producing turn + one ordinary new turn from the fallback path)", len(allTurns))
	}
	var newTurn *sqlcgen.Turn
	for i := range allTurns {
		if allTurns[i].ID != producingTurn.ID {
			newTurn = &allTurns[i]
		}
	}
	if newTurn == nil {
		t.Fatal("no new turn found")
	}
	if newTurn.Prompt == nil || *newTurn.Prompt != "please keep the env fallback path" {
		t.Errorf("new turn prompt = %v, want %q", newTurn.Prompt, "please keep the env fallback path")
	}
}

// Cross-channel "already decided" honesty (this Step's own point 5) is
// proved two ways elsewhere, deliberately NOT re-derived as a third,
// timing-sensitive integration test here: (1) renderLinearPlanOutcomeText
// itself is table-driven unit-tested (planverdict_test.go, same package,
// no DB/network) over every DecidePlanOutcome shape handlePlanVerdict can
// ever observe, won or lost; (2) the actual cross-channel RACE this text
// reacts to -- two different verdicts concurrently deciding the SAME
// plan, exactly one winning -- is proved at the shared httpapi.DecidePlan
// level by TestDecidePlan_FirstWinsAcrossChannels_ApproveVsReject
// (internal/adapters/inbound/httpapi), which handlePlanVerdict calls
// UNCHANGED. Reproducing that same race reliably a third time, HERE,
// through two full HTTP-shaped call paths of very different cost (a
// direct Go call vs. this package's own sign/parse/dedupe-claim/lookup
// webhook pipeline) is not a genuine, deterministic race at all -- one
// side (the direct call) wins essentially every time, which would make
// this file's own version of the test either flaky or silently
// non-representative rather than a meaningful proof.
