//go:build integration

// This file proves Step 76's own per-channel refusal contract (§10 Phase
// 6, §32) for Linear specifically: a rollout refusal must take the SAME
// terminal, authz-denial shape handleCreated's own `if !authorize(...)`
// branch immediately above it already uses (webhook.go) -- acknowledge,
// release ONLY the linear_agent_sessions claim, and answer 200 (so the
// webhook-delivery claim itself is KEPT, never released for a redelivery
// retry that would only reproduce this exact same refusal forever, since
// repo_settings.sessions_enabled does not change between redeliveries of
// the same event). Mirrors webhook_integration_test.go's own end-to-end
// HTTP-level shape exactly.
package linear_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/inbound/linear"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestWebhookHandler_Created_RolloutRefusal_AcknowledgesReleasesOnlyAgentSessionClaim
// is the MUTATION-TESTABLE guard for §32's own Linear refusal contract:
// deps.RolloutMode is armed to cohort mode and deps.DefaultRepoURL is
// NEVER enrolled, so httpapi.CreateSessionCore's own gate refuses with
// CreateSessionError.RolloutRefusal == true. Proves, in one HTTP round
// trip: (1) the response is 200, never 500 (mirrors the authz-denial
// branch's identical status); (2) the webhook-delivery claim is KEPT
// (a redelivery of the SAME Linear-Delivery id must be treated as an
// already-claimed duplicate, not reprocessed); (3) the
// linear_agent_sessions claim IS released (a later, genuinely different
// agent session for this SAME repo, once it IS enrolled, must not be
// blocked by a stale claim this refused attempt left behind); (4) no
// session was ever created.
func TestWebhookHandler_Created_RolloutRefusal_AcknowledgesReleasesOnlyAgentSessionClaim(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)
	deps.RolloutMode = platform.RolloutModeCohort
	deps.RepoSettings = narvipg.NewRepoSettingsStore(pool)
	// deps.DefaultRepoURL ("https://github.com/khazaddev/narvi",
	// newHandlerDeps' own fixed default) is deliberately left unenrolled --
	// no UpsertSessionsEnabled call for it anywhere in this test.

	agentSessionID := "agent-session-" + t.Name()
	organizationID := "org-" + t.Name()
	deliveryID := "delivery-" + t.Name()
	body := agentSessionCreatedPayload(agentSessionID, organizationID)

	rec := postWebhook(t, linear.NewWebhookHandler(deps), body, deliveryID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (terminal acknowledgment, mirroring the authz-denial branch's identical status)", rec.Code, http.StatusOK)
	}

	// The webhook-delivery claim must be KEPT -- Claim on the SAME
	// (provider, deliveryID) must report a duplicate (Inserted == false),
	// never a fresh claim, proving this delivery id was never released.
	deliveries := narvipg.NewWebhookDeliveryStore(pool)
	claim, err := deliveries.Claim(ctx, "linear", deliveryID)
	if err != nil {
		t.Fatalf("re-claim webhook delivery: %v", err)
	}
	if claim.Inserted {
		t.Error("webhook delivery claim was re-claimable (Inserted = true) -- want it to remain held after a rollout refusal, exactly like an authz denial")
	}

	// The linear_agent_sessions claim, by contrast, MUST have been
	// released -- GetByAgentSessionID must report it gone.
	agentSessions := narvipg.NewLinearAgentSessionStore(pool)
	if _, err := agentSessions.GetByAgentSessionID(ctx, agentSessionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("agent session claim lookup: err=%v, want pgx.ErrNoRows (the claim must have been released, mirroring the authz-denial branch)", err)
	}

	// No session was ever created for this repo.
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'linear'`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("sessions count = %d, want 0 -- a rollout refusal must never create a session", count)
	}
}

// TestWebhookHandler_Created_RolloutGate_EnrolledRepoStillCreatesSession
// is the refusal test's own positive control: the IDENTICAL setup, except
// deps.DefaultRepoURL IS enrolled -- proves cohort mode is a real,
// bidirectional gate here too, not something that happens to always
// refuse.
func TestWebhookHandler_Created_RolloutGate_EnrolledRepoStillCreatesSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	deps := newHandlerDeps(t, pool)
	deps.RolloutMode = platform.RolloutModeCohort
	deps.RepoSettings = narvipg.NewRepoSettingsStore(pool)
	if _, err := deps.RepoSettings.UpsertSessionsEnabled(ctx, "khazaddev/narvi", true); err != nil {
		t.Fatalf("seed enrollment: %v", err)
	}

	agentSessionID := "agent-session-" + t.Name()
	organizationID := "org-" + t.Name()
	body := agentSessionCreatedPayload(agentSessionID, organizationID)

	rec := postWebhook(t, linear.NewWebhookHandler(deps), body, "delivery-"+t.Name())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	agentSessions := narvipg.NewLinearAgentSessionStore(pool)
	row, err := agentSessions.GetByAgentSessionID(ctx, agentSessionID)
	if err != nil {
		t.Fatalf("GetByAgentSessionID: %v (want a real, committed session id -- an enrolled repo must not be refused)", err)
	}
	if !row.SessionID.Valid {
		t.Error("agent session row has no session_id -- want a real created session for an enrolled repo")
	}
}
