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
	"fmt"
	"net/http"
	"net/url"
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

// maintenanceDatabaseDSN rewrites connStr's own database name to
// "postgres" -- every real Postgres server carries this built-in
// maintenance database alongside whatever this test binary's own shared
// container additionally created (tcpostgres.WithDatabase("narvi_test"),
// sharedpool_integration_test.go), so it is reachable with the SAME
// host/port/credentials while carrying NONE of this codebase's own
// migrated tables. See TestWebhookHandler_Created_
// TransientRolloutReadError_RetriesRatherThanTerminal's own doc comment
// for why this, rather than a timing- or lock-based fault injection, is
// the deterministic way to make exactly ONE query (repo_settings) fail
// while every other query this test's own webhook request issues (on a
// DIFFERENT, healthy pool) succeeds normally.
func maintenanceDatabaseDSN(connStr string) (string, error) {
	u, err := url.Parse(connStr)
	if err != nil {
		return "", fmt.Errorf("parse connection string: %w", err)
	}
	u.Path = "/postgres"
	return u.String(), nil
}

// TestWebhookHandler_Created_TransientRolloutReadError_RetriesRatherThanTerminal
// is the MUTATION-TESTABLE guard for Root Cause 2 of this Step's own
// adversarial review: a repo_settings read failure that is NOT a
// demonstrated policy decision (a database blip, a context cancellation,
// a timeout) must refuse this ONE attempt but must NOT take the SAME
// terminal, permanent-denial path TestWebhookHandler_Created_
// RolloutRefusal_AcknowledgesReleasesOnlyAgentSessionClaim (immediately
// above) proves for a GENUINE "this repo is not enrolled" refusal --
// checkRolloutGate's own doc comment (rolloutgate.go) has the full "why".
//
// Fault injection: deps.Pool (the ONLY dependency httpapi.
// CreateSessionCore uses to open its own fresh transaction --
// create.go's own `tx, err := pool.Begin(ctx)`) is swapped for a pool
// pointed at Postgres's own built-in "postgres" maintenance database
// (maintenanceDatabaseDSN, above) -- reachable on the SAME server/
// credentials as this test binary's shared container, but never
// migrated. checkRolloutGate's own repoSettings.WithTx(tx).Get is the
// FIRST query CreateSessionOnTx ever attempts on that fresh transaction
// (right after validateCreateSessionRequest, which is pure, and BEFORE
// this Step's own primary gate lets anything else run) -- so this
// deterministically fails EXACTLY that one query with a genuine Postgres
// error ("relation repo_settings does not exist"), with zero timing or
// locking tricks, while every dependency THIS webhook request needs
// BEFORE reaching CreateSessionCore (deps.Deliveries/deps.AgentSessions/
// deps.IdentityLink -- handleCreated's own claim + authorize steps, both
// well before its own httpapi.CreateSessionCore call) stays bound to the
// real, healthy shared pool and succeeds normally.
//
// Mutation anchor: re-marking checkRolloutGate's own read-error refusal
// as RolloutRefusal: true (reverting Root Cause 2's own fix) makes this
// test fail -- the SAME cerr.RolloutRefusal check handleCreated already
// has (webhook.go) would then take the terminal branch instead (status
// 200, claim kept), exactly like TestWebhookHandler_Created_
// RolloutRefusal_AcknowledgesReleasesOnlyAgentSessionClaim's own genuine-
// refusal scenario, flipping every assertion below.
func TestWebhookHandler_Created_TransientRolloutReadError_RetriesRatherThanTerminal(t *testing.T) {
	ctx := context.Background()
	pool, connStr := IntegrationTestPoolAndConnStr(t)
	deps := newHandlerDeps(t, pool)
	deps.RolloutMode = platform.RolloutModeCohort
	deps.RepoSettings = narvipg.NewRepoSettingsStore(pool)

	brokenDSN, err := maintenanceDatabaseDSN(connStr)
	if err != nil {
		t.Fatalf("derive maintenance-database DSN: %v", err)
	}
	brokenPool, err := narvipg.NewPool(ctx, brokenDSN)
	if err != nil {
		t.Fatalf("open pool against maintenance database: %v", err)
	}
	t.Cleanup(brokenPool.Close)
	deps.Pool = brokenPool

	agentSessionID := "agent-session-" + t.Name()
	organizationID := "org-" + t.Name()
	deliveryID := "delivery-" + t.Name()
	body := agentSessionCreatedPayload(agentSessionID, organizationID)

	rec := postWebhook(t, linear.NewWebhookHandler(deps), body, deliveryID)

	// The RETRY path (NewWebhookHandler's own generic ok=false branch:
	// release the webhook-delivery claim, answer 500) must run -- never
	// the RolloutRefusal terminal path (200, claim kept).
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (a transient repo_settings read failure must take the RETRY path, never the permanent-refusal path)", rec.Code, http.StatusInternalServerError)
	}

	// The webhook-delivery claim must have been RELEASED -- Claim on the
	// SAME (provider, deliveryID) must report a fresh claim (Inserted ==
	// true), proving a redelivery of this SAME Linear-Delivery id can
	// actually retry once the database recovers -- the opposite of
	// TestWebhookHandler_Created_RolloutRefusal_
	// AcknowledgesReleasesOnlyAgentSessionClaim's own "claim KEPT" proof
	// for a genuine, permanent refusal.
	deliveries := narvipg.NewWebhookDeliveryStore(pool)
	claim, err := deliveries.Claim(ctx, "linear", deliveryID)
	if err != nil {
		t.Fatalf("re-claim webhook delivery: %v", err)
	}
	if !claim.Inserted {
		t.Error("webhook delivery claim was NOT re-claimable (Inserted = false) -- want it released so a redelivery can retry once Postgres recovers")
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'linear'`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Errorf("sessions count = %d, want 0 -- a refused attempt (transient or not) must never create a session", count)
	}
}
