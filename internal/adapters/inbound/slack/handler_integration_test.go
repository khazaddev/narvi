//go:build integration

// Full HTTP-level integration tests for internal/adapters/inbound/slack's
// POST /webhooks/slack handler, against a real Postgres instance --
// gated behind the "integration" build tag, matching this codebase's own
// testcontainers-Postgres-plus-embedded-migrations convention (each
// DB-touching package builds its own copy of this small helper rather
// than sharing one across package boundaries). Run via `make
// test-integration`.
package slack_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

const testSigningSecret = "test-signing-secret"

// newTestPool spins up a throwaway Postgres container with every
// embedded migration applied (including migration 000028's own
// slack_thread_sessions table) and returns a ready *pgxpool.Pool.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	// startCtx bounds the container-startup call below via the ambient
	// context (image pull + Docker daemon round trip + Postgres's own
	// internal ready-wait) -- kept as defense in depth, but NOT solely
	// relied upon any more: CI run 30834918806 showed this exact bound
	// (added after CI run 30831633470's own ContainerStart hang) itself
	// fail to actually cut the call off when the hang recurred one layer
	// deeper, inside testcontainers-go's own wait.(*LogStrategy).
	// WaitUntilReady -- the goroutine dump showed it looping on a 100ms
	// poll for the FULL 10-minute panic window, never once observing
	// ctx.Done(), despite this same context chain being correctly wired
	// all the way through (confirmed directly: reproducing an
	// impossible-to-satisfy wait condition locally against this exact
	// call DOES correctly time out via this same context mechanism, at
	// testcontainers' own hardcoded 60s deadline -- so the mechanism is
	// sound in isolation, but evidently not dependable against whatever a
	// genuinely stalled CI-runner Docker daemon does to it in practice).
	//
	// Rather than keep chasing exactly why context cancellation isn't
	// always honored deep inside a third-party library under conditions
	// this dev machine cannot reproduce, the startup call now ALSO runs on
	// its own goroutine (via errgroup.Group.Go -- no naked `go` statement,
	// §11) raced against an independent, plain time.After watchdog:
	// whichever of "the call returned" or "the watchdog fired" happens
	// first decides the outcome, with no dependency on any context
	// cancellation actually being honored by anything downstream. If the
	// watchdog wins, the goroutine is deliberately abandoned (leaked, not
	// joined) rather than blocking this test's own cleanup on a call that
	// has already demonstrated it can ignore its own cancellation signal.
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	const containerStartWatchdog = 2*time.Minute + 15*time.Second
	type containerStartResult struct {
		container *tcpostgres.PostgresContainer
		err       error
	}
	startCh := make(chan containerStartResult, 1)
	var startGroup errgroup.Group
	startGroup.Go(func() error {
		container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
			tcpostgres.WithDatabase("narvi_test"),
			tcpostgres.WithUsername("narvi"),
			tcpostgres.WithPassword("narvi"),
			tcpostgres.BasicWaitStrategies(),
		)
		startCh <- containerStartResult{container: container, err: err}
		return nil
	})

	var container *tcpostgres.PostgresContainer
	var err error
	select {
	case res := <-startCh:
		container, err = res.container, res.err
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
	case <-time.After(containerStartWatchdog):
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s -- Docker daemon likely "+
			"stalled without honoring context cancellation (see this function's own doc comment)", containerStartWatchdog)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	migrateDB, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })

	dbDriver, err := migratepg.WithInstance(migrateDB, &migratepg.Config{})
	if err != nil {
		t.Fatalf("migratepg.WithInstance: %v", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// slackTestRig bundles a fully-wired handler plus the stores/registry
// needed to assert against Postgres directly.
type slackTestRig struct {
	handler  http.HandlerFunc
	pool     *pgxpool.Pool
	sessions *narvipg.SessionStore
	turns    *narvipg.TurnStore
	threads  *narvipg.SlackThreadSessionStore
	plans    *narvipg.PlanStore
}

// linkSlackIdentityForTest links slackUserID directly to a NEW fixture
// Narvi user (role) via identities.Create -- bypassing any profile-email
// fetch entirely: identitylink.Resolve's own fast path
// (GetByProviderAndExternalID) always wins first regardless of what any
// fake Slack /users.info stub answers, so this package's many baseline
// HTTP-level tests (which only care about session/turn/audit-log/redelivery
// mechanics, never about the auto-linking algorithm itself) can exercise a
// genuinely LINKED, authorized actor.
//
// Audit-fix batch addition ("block unlinked actor state changes"): BEFORE
// this batch, every one of this package's baseline tests relied
// (incidentally, never as their own point) on the OLD "an actor that never
// resolves at all still gets its state-changing action allowed through,
// under bot attribution" precedent -- resolveSlackActor's own
// GetUserEmail call against these tests' generic {"ok":true}-only fake
// Slack servers never found a real email, so EVERY fixture event's own
// actor was, and stayed, unresolved. That precedent is exactly what this
// batch's own hardening removes (actorauthz.AuthorizeLinkedActor), so every
// baseline test's own fixture Slack user id must now be pre-linked here
// instead, to a role with unconditional (no ownership-carve-out-dependent)
// permission for every action these tests exercise.
func linkSlackIdentityForTest(ctx context.Context, t *testing.T, pool *pgxpool.Pool, slackUserID string, role sqlcgen.UserRole) sqlcgen.User {
	t.Helper()
	user, err := narvipg.NewUserStore(pool).Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: slackUserID + "@narvi-test.example.com",
		DisplayName:  slackUserID,
		Role:         role,
	})
	if err != nil {
		t.Fatalf("create fixture user for %s: %v", slackUserID, err)
	}
	if _, err := narvipg.NewIdentityStore(pool).Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:     user.ID,
		Provider:   sqlcgen.IdentityProviderSlack,
		ExternalID: slackUserID,
		LinkedVia:  sqlcgen.IdentityLinkedViaAdmin,
	}); err != nil {
		t.Fatalf("link fixture identity for %s: %v", slackUserID, err)
	}
	return user
}

// newSlackTestRig wires a real handler against pool -- a fake Slack Web
// API server (ackServer) stands in for chat.postMessage, so this
// package's own tests never make a real network call.
func newSlackTestRig(t *testing.T, pool *pgxpool.Pool) *slackTestRig {
	t.Helper()
	ctx := context.Background()

	// Audit-fix batch addition: appMentionEnvelope/messageEnvelope's own
	// fixed "U0TESTUSER"/"U0OTHERUSER" ids (below) must now resolve to a
	// genuinely LINKED, sufficiently-privileged actor -- see
	// linkSlackIdentityForTest's own doc comment above for why. RoleMaintainer
	// is allowed unconditionally for every action this package's baseline
	// tests exercise (ActionCreateSession/ActionPromptSession/
	// ActionApprovePlan), regardless of session ownership.
	linkSlackIdentityForTest(ctx, t, pool, "U0TESTUSER", sqlcgen.UserRoleMaintainer)
	linkSlackIdentityForTest(ctx, t, pool, "U0OTHERUSER", sqlcgen.UserRoleMaintainer)

	ackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ackServer.Close)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	deliveries := narvipg.NewWebhookDeliveryStore(pool)
	threads := narvipg.NewSlackThreadSessionStore(pool)
	plans := narvipg.NewPlanStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	handler := slack.NewHandler(slack.Deps{
		Pool:         pool,
		Sessions:     sessions,
		Turns:        turns,
		Environments: environments,
		Registry:     registry,
		Deliveries:   deliveries,
		Threads:      threads,
		Plans:        plans,
		AuditLog:     auditLog,
		// Participants (Step 39's own SECOND fix-pass addition, "identities
		// + full RBAC", §13.2/§13.3): authorizeSessionAction (identity.go)
		// needs this even though this rig's own fixture users never
		// auto-link (see this func's own doc comment) -- mirrors every
		// other Deps field here, always a real, non-nil store.
		Participants:    narvipg.NewParticipantStore(pool),
		SigningSecret:   testSigningSecret,
		BotToken:        "test-bot-token",
		DefaultRepoName: "narvi",
		DefaultRepoURL:  "https://github.com/khazaddev/narvi",
		TimestampWindow: 5 * time.Minute,
		SlackAPIBaseURL: ackServer.URL,
		AckTimeout:      platform.DefaultTimeouts().SlackAckTimeout,
		// IdentityLink/SlackClient/Timeouts (Step 39, "identities + full
		// RBAC", §13.2): ackServer above answers EVERY path (including
		// /users.info) with a bare {"ok":true}, so GetUserEmail resolves
		// to (email="", ok=false) for every fixture event's own "user" id
		// -- resolveSlackActor's own identity.Resolve call still needs
		// REAL (non-nil) stores to run against, even though this rig's
		// own fixture users never match anything (see
		// identity_integration_test.go for a rig that actually exercises
		// a real match).
		SlackClient: slackapi.New(ackServer.Client(), ackServer.URL, "test-bot-token"),
		Timeouts:    platform.DefaultTimeouts(),
		IdentityLink: identitylink.Deps{
			Pool:          pool,
			Users:         narvipg.NewUserStore(pool),
			Identities:    narvipg.NewIdentityStore(pool),
			LinkPrompts:   narvipg.NewIdentityLinkPromptStore(pool),
			AuditLog:      auditLog,
			PublicBaseURL: "https://narvi.example.com",
			PromptTTL:     time.Hour,
		},
	})

	return &slackTestRig{handler: handler, pool: pool, sessions: sessions, turns: turns, threads: threads, plans: plans}
}

// signedSlackRequest builds a real, correctly-signed POST /webhooks/slack
// request carrying body -- mirrors Slack's own real "v0:{ts}:{body}"
// HMAC-SHA256 scheme exactly (see handler_test.go's own identical
// signRequest, duplicated here since this file lives in the external
// slack_test package).
func signedSlackRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	ts := time.Now().Unix()
	signedPayload := "v0:" + strconv.FormatInt(ts, 10) + ":" + body
	mac := hmac.New(sha256.New, []byte(testSigningSecret))
	mac.Write([]byte(signedPayload))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// appMentionEnvelope builds a real-shaped "event_callback"/"app_mention"
// Slack Events API envelope (confirmed against Slack's own current
// documentation at this Step's own design time).
func appMentionEnvelope(eventID, channel, ts, threadTS, text string) string {
	event := map[string]string{
		"type":    "app_mention",
		"channel": channel,
		"user":    "U0TESTUSER",
		"text":    text,
		"ts":      ts,
	}
	if threadTS != "" {
		event["thread_ts"] = threadTS
	}
	eventJSON, _ := json.Marshal(event)
	envelope := fmt.Sprintf(`{"type":"event_callback","event_id":%q,"team_id":"T0TEST","event":%s}`, eventID, eventJSON)
	return envelope
}

// messageEnvelope builds a real-shaped "event_callback"/"message" envelope
// for a plain reply within an existing thread.
func messageEnvelope(eventID, channel, ts, threadTS, text string) string {
	event := map[string]string{
		"type":      "message",
		"channel":   channel,
		"user":      "U0OTHERUSER",
		"text":      text,
		"ts":        ts,
		"thread_ts": threadTS,
	}
	eventJSON, _ := json.Marshal(event)
	envelope := fmt.Sprintf(`{"type":"event_callback","event_id":%q,"team_id":"T0TEST","event":%s}`, eventID, eventJSON)
	return envelope
}

// TestHandler_NewMention_CreatesSessionAndTurn proves a synthetic,
// correctly-signed app_mention event with no existing thread mapping
// results in a real session + turn in Postgres, and a slack_thread_sessions
// mapping row.
func TestHandler_NewMention_CreatesSessionAndTurn(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackTestRig(t, pool)

	envelope := appMentionEnvelope("Ev0NEWTHREAD001", "C0CHANNEL", "1700000010.000100", "", "<@U0BOT> please *fix* the build")

	req := signedSlackRequest(t, envelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	mapping, err := rig.threads.Get(ctx, "C0CHANNEL", "1700000010.000100")
	if err != nil {
		t.Fatalf("expected a thread mapping row, Get: %v", err)
	}

	session, err := rig.sessions.Get(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if session.SpawnSource != sqlcgen.SessionSpawnSourceSlack {
		t.Errorf("SpawnSource = %q, want %q", session.SpawnSource, sqlcgen.SessionSpawnSourceSlack)
	}

	turns, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turns))
	}
	if turns[0].Prompt == nil || !strings.Contains(*turns[0].Prompt, "**fix**") {
		t.Errorf("turn prompt = %v, want normalized mrkdwn containing **fix**", turns[0].Prompt)
	}
	if turns[0].Prompt == nil || !strings.Contains(*turns[0].Prompt, "@U0BOT") {
		t.Errorf("turn prompt = %v, want the unwrapped @U0BOT mention", turns[0].Prompt)
	}
}

// TestHandler_Redelivery_DoesNotDoubleProcess proves WebhookDeliveryStore's
// dedupe claim actually protects this handler end to end: POSTing the
// exact same signed event_id twice results in exactly one session/turn,
// never two.
func TestHandler_Redelivery_DoesNotDoubleProcess(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackTestRig(t, pool)

	envelope := appMentionEnvelope("Ev0REDELIVER001", "C0REDELIVER", "1700000020.000100", "", "please help")

	for i := 0; i < 2; i++ {
		req := signedSlackRequest(t, envelope)
		rec := httptest.NewRecorder()
		rig.handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200 (body=%s)", i, rec.Code, rec.Body.String())
		}
	}

	mapping, err := rig.threads.Get(ctx, "C0REDELIVER", "1700000020.000100")
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}

	turns, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("len(turns) = %d, want exactly 1 (redelivery must not double-process)", len(turns))
	}

	var mappingCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM slack_thread_sessions WHERE channel_id = $1 AND thread_ts = $2`,
		"C0REDELIVER", "1700000020.000100",
	).Scan(&mappingCount); err != nil {
		t.Fatalf("count mapping rows: %v", err)
	}
	if mappingCount != 1 {
		t.Errorf("mapping row count = %d, want exactly 1", mappingCount)
	}
}

// TestHandler_FailedFirstAttemptReleasesClaimForRedelivery is the H2
// audit fix's own headline proof, mirroring github's own identical
// TestGitHubIntegration_FailedFirstAttemptReleasesClaimForRedelivery
// (handler_integration_test.go): the webhook-delivery dedupe claim must
// NOT permanently poison an event_id when the first attempt fails AFTER
// the claim succeeds but BEFORE the event is actually processed --
// Slack's own real redelivery behavior (retrying on a non-2xx response or
// a timeout) means the SAME event_id must be reprocessable, not silently
// swallowed as an "already claimed" duplicate forever.
func TestHandler_FailedFirstAttemptReleasesClaimForRedelivery(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackTestRig(t, pool)

	const eventID = "Ev0RETRYAFTERFAIL001"

	// First attempt: the outer envelope is well-formed (so it gets past
	// url_verification detection and claims the delivery), but the inner
	// "event" field is a JSON string, not the object slackEvent requires --
	// json.Unmarshal into ev fails AFTER the claim has already succeeded.
	malformedEnvelope := fmt.Sprintf(`{"type":"event_callback","event_id":%q,"team_id":"T0TEST","event":"not-an-object"}`, eventID)
	req := signedSlackRequest(t, malformedEnvelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("first (malformed) delivery status = %d, want %d (body=%s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// The claim row must have been released by the failure path, not left
	// behind poisoning this event_id.
	var deliveryRowCountAfterFailure int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'slack' AND delivery_id = $1`, eventID,
	).Scan(&deliveryRowCountAfterFailure); err != nil {
		t.Fatalf("count webhook_deliveries rows after failure: %v", err)
	}
	if deliveryRowCountAfterFailure != 0 {
		t.Fatalf("webhook_deliveries row count after failed attempt = %d, want 0 (claim must be released on failure)", deliveryRowCountAfterFailure)
	}

	// Redelivery: the SAME event_id, this time a genuine, well-formed
	// app_mention payload. It must be processed, not skipped as an
	// already-claimed duplicate.
	validEnvelope := appMentionEnvelope(eventID, "C0RETRYAFTERFAIL", "1700000040.000100", "", "please help again")
	req2 := signedSlackRequest(t, validEnvelope)
	rec2 := httptest.NewRecorder()
	rig.handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("redelivered (valid) status = %d, want %d (body=%s)", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	mapping, err := rig.threads.Get(ctx, "C0RETRYAFTERFAIL", "1700000040.000100")
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}

	turns, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(turns) != 1 {
		t.Errorf("len(turns) = %d, want exactly 1 (the redelivered valid payload must actually be processed)", len(turns))
	}
}

// TestHandler_ReplyOnMappedThread_AddsTurnToSameSession proves the core
// thread<->session mapping requirement: a first message in a NEW thread
// creates a session, and a second message with the SAME (channel_id,
// thread_ts) creates a NEW TURN on the SAME session -- never a second
// session. The first turn is driven to a terminal state directly (this
// test environment's registry has no real sandbox provider wired, so
// nothing would ever organically complete it) purely to prove the
// mapping/turn-add logic in isolation, mirroring this codebase's own
// precedent (dispatch.go's own tests) of asserting persistence-layer
// outcomes directly rather than depending on a real end-to-end dispatch.
func TestHandler_ReplyOnMappedThread_AddsTurnToSameSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackTestRig(t, pool)

	firstEnvelope := appMentionEnvelope("Ev0THREAD001", "C0THREAD", "1700000030.000100", "", "start this task")
	req := signedSlackRequest(t, firstEnvelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first mention: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	mapping, err := rig.threads.Get(ctx, "C0THREAD", "1700000030.000100")
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}
	firstSessionID := mapping.SessionID

	firstTurns, err := rig.turns.ListForSession(ctx, firstSessionID)
	if err != nil || len(firstTurns) != 1 {
		t.Fatalf("ListForSession after first mention: turns=%v err=%v, want exactly 1", firstTurns, err)
	}

	// Drive the first turn to a terminal state so the reply below is free
	// to add a second one (see this test's own doc comment).
	if _, err := rig.turns.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:          firstTurns[0].ID,
		Status:      sqlcgen.TurnStatusCompleted,
		CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	replyEnvelope := messageEnvelope("Ev0THREAD002", "C0THREAD", "1700000031.000200", "1700000030.000100", "here is more context")
	req2 := signedSlackRequest(t, replyEnvelope)
	rec2 := httptest.NewRecorder()
	rig.handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("reply: status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}

	// Still exactly ONE mapping row for this thread -- a reply must never
	// create a second session/mapping.
	var mappingCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM slack_thread_sessions WHERE channel_id = $1 AND thread_ts = $2`,
		"C0THREAD", "1700000030.000100",
	).Scan(&mappingCount); err != nil {
		t.Fatalf("count mapping rows: %v", err)
	}
	if mappingCount != 1 {
		t.Errorf("mapping row count = %d, want exactly 1 (reply must reuse the existing mapping)", mappingCount)
	}

	finalTurns, err := rig.turns.ListForSession(ctx, firstSessionID)
	if err != nil {
		t.Fatalf("ListForSession after reply: %v", err)
	}
	if len(finalTurns) != 2 {
		t.Fatalf("len(turns) after reply = %d, want 2 (the reply must add a NEW turn on the SAME session)", len(finalTurns))
	}
}

// TestHandler_URLVerification_DoesNotProcessAsEvent proves the
// url_verification handshake is handled distinctly from a real event:
// it echoes the challenge and never touches WebhookDeliveryStore/creates
// any session at all.
func TestHandler_URLVerification_DoesNotProcessAsEvent(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackTestRig(t, pool)

	body := `{"type":"url_verification","challenge":"test-challenge-value","token":"xyz"}`
	req := signedSlackRequest(t, body)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "test-challenge-value") {
		t.Errorf("body = %q, want it to contain the echoed challenge", rec.Body.String())
	}

	var sessionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Errorf("session count = %d, want 0 (url_verification must never create a session)", sessionCount)
	}
}
