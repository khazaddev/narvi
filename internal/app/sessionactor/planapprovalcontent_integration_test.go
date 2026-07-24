//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves Step 38's ("plan mode, cross-channel", §8.1/§13.3) own
// best-effort plan-content extraction (planapprovalcontent.go's
// planContentText) against a REAL Postgres instance, specifically the case
// planContentEventFetchLimit's own doc comment describes: a session that
// has already accumulated more events than that fixed fetch limit BEFORE
// the plan-mode turn under test ever runs. planContentText must still find
// this turn's own final token event -- the most RECENT activity in the
// session -- rather than silently falling back to
// planContentFallbackText because the fetch window looked at the WRONG
// end of a long event log.

// TestPlanContentText_LongEventHistory_StillFindsCurrentTurnsTokenText
// proves planContentText finds THIS plan-mode turn's own final streamed
// token text even when the session already has more prior events than
// planContentEventFetchLimit -- the real regression this Step's own fix
// (ListRecentForSession, newest-first) closes: an oldest-first fetch
// bounded to the same limit would never even reach this turn's own event,
// silently rendering the Slack/Linear plan-approval notification with
// planContentFallbackText instead of the real plan content.
func TestPlanContentText_LongEventHistory_StillFindsCurrentTurnsTokenText(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithSpawnSource(ctx, t, pool, sqlcgen.SessionSpawnSourceSlack)
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, ok, err := narvipg.NewSlackThreadSessionStore(pool).Claim(ctx, "C123", "1700000000.000100", sessionID); err != nil || !ok {
		t.Fatalf("claim slack thread session: ok=%v err=%v", ok, err)
	}

	eventStore := narvipg.NewEventStore(pool)

	// More filler events than planContentEventFetchLimit, all BEFORE the
	// plan-mode turn's own real token event below -- an oldest-first fetch
	// bounded to planContentEventFetchLimit would exhaust its whole budget
	// on these and never see the real one.
	const fillerCount = planContentEventFetchLimit + 500
	for i := 0; i < fillerCount; i++ {
		if _, err := eventStore.Create(ctx, sqlcgen.CreateEventParams{
			SessionID: sessionID,
			Type:      "message",
			MessageID: fmt.Sprintf("filler-%d", i),
			Payload:   json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("seed filler event %d: %v", i, err)
		}
	}

	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurnWithPlanMode(ctx, t, turnStore, sessionID, true, nil)

	const wantPlanText = "1. Do the thing\n2. Do the other thing"
	tokenPayload, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "token", Text: wantPlanText})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	if _, err := eventStore.Create(ctx, sqlcgen.CreateEventParams{
		SessionID: sessionID,
		Type:      "token",
		MessageID: "plan-turn-final-token",
		Payload:   tokenPayload,
	}); err != nil {
		t.Fatalf("seed plan turn's own token event: %v", err)
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	row := getSoleOutboxRowForSession(ctx, t, pool, sessionID)
	if row.Kind != "slack_plan_approval" {
		t.Fatalf("Kind = %q, want %q", row.Kind, "slack_plan_approval")
	}

	var payload slackapi.PlanApprovalPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload as slackapi.PlanApprovalPayload: %v", err)
	}
	if payload.Text != wantPlanText {
		t.Errorf("Text = %q, want %q (the plan-mode turn's own real token text, not the fallback)", payload.Text, wantPlanText)
	}
}
