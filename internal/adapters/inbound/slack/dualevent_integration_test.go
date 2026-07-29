//go:build integration

// Integration tests for the L3 audit fix ("Slack's own dual-delivery for
// one logical mention isn't coalesced", internal/adapters/inbound/slack/
// handler.go's own slackMessageClaimProvider/messageClaimKey): Slack sends
// BOTH an app_mention event AND a message event (two DISTINCT event_id
// values) for a single @mention posted inside a thread this adapter
// already has mapped to a session -- these tests prove that dual delivery
// now results in exactly ONE turn and exactly ONE in-thread ack, that a
// genuinely different message (a different ts) in the SAME thread is
// still NOT coalesced, and that a genuine backend failure on the first of
// the twin events releases BOTH the outer (event_id) and the new inner
// (channel:ts) claims so a later real retry can still succeed.
//
// Mirrors this package's own established conventions: turn_integration_test.go's
// own slackAckTestRig (a request-capturing fake Slack API server) and
// identity_integration_test.go's own appMentionEnvelopeWithUser/
// messageEnvelopeWithUser/newFakeSlackWithUsersInfo/newIdentityLinkDepsForTest
// (same package, so all directly reusable here) -- a real Slack
// dual-delivery for one logical mention always carries the SAME "user"
// field on both twin events, since they describe the same human action
// twice, so these tests deliberately use the WithUser variants rather than
// handler_integration_test.go's own fixed-distinct-user appMentionEnvelope/
// messageEnvelope helpers.
package slack_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// drainAllSlackRequests reads every request rig's fake Slack API server
// observed off rig.requests -- unlike slackAckTestRig's own drainAckTexts
// (turn_integration_test.go), which only keeps a request's "text" field
// (silently discarding anything without one), this keeps every captured
// request including a bare /users.info call, so this file's own tests can
// count how many times resolveSlackActor's own users.info call actually
// happened -- the L3 fix's own "no redundant resolveSlackActor call"
// half.
func drainAllSlackRequests(rig *slackAckTestRig) []recordedSlackRequestBody {
	var out []recordedSlackRequestBody
	for {
		select {
		case req := <-rig.requests:
			out = append(out, req)
		default:
			return out
		}
	}
}

// countRequestsByPath counts how many captured requests hit path exactly
// (e.g. "/users.info").
func countRequestsByPath(requests []recordedSlackRequestBody, path string) int {
	n := 0
	for _, r := range requests {
		if r.path == path {
			n++
		}
	}
	return n
}

// countPostMessageForThread counts how many captured chat.postMessage
// (never chat.postEphemeral) calls carried the given thread_ts -- this
// file's own proxy for "how many in-thread acks were posted for this
// thread", since ack.go's postAck always threads its reply via
// thread_ts.
func countPostMessageForThread(requests []recordedSlackRequestBody, threadTS string) int {
	n := 0
	for _, r := range requests {
		if r.path != "/chat.postMessage" {
			continue
		}
		if ts, _ := r.body["thread_ts"].(string); ts == threadTS {
			n++
		}
	}
	return n
}

// completeOpenTurn drives whatever non-terminal turn currently exists for
// sessionID to Completed -- mirrors this package's own established
// "drive the first turn terminal so the next reply is free to add its
// own" precedent (e.g. TestHandler_ReplyOnMappedThread_AddsTurnToSameSession,
// handler_integration_test.go), factored into a helper since this file's
// own regression test needs it more than once.
func completeOpenTurn(t *testing.T, turns *narvipg.TurnStore, sessionID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	existing, err := turns.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	var openTurnID pgtype.UUID
	for _, tn := range existing {
		if tn.Status != sqlcgen.TurnStatusCompleted {
			openTurnID = tn.ID
		}
	}
	if !openTurnID.Valid {
		t.Fatal("completeOpenTurn: expected an open (non-Completed) turn, found none")
	}
	if _, err := turns.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID: openTurnID, Status: sqlcgen.TurnStatusCompleted, CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("completeOpenTurn: UpdateStatus: %v", err)
	}
}

// TestHandler_DualDelivery_AppMentionAndMessage_CoalescesToOneTurnAndOneAck
// is the L3 audit fix's own headline proof, the exact scenario the audit
// finding names: a synthetic app_mention+message pair (two separate
// webhook POST requests, distinct event_ids, IDENTICAL channel/ts/text,
// referencing an ALREADY-MAPPED thread) results in exactly ONE turn
// created and exactly ONE in-thread ack posted -- never two -- and
// resolveSlackActor's own users.info call happens only once, not once per
// twin.
func TestHandler_DualDelivery_AppMentionAndMessage_CoalescesToOneTurnAndOneAck(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackAckTestRig(t, pool)

	const dualUser = "U0DUALDELIVERY"
	linkSlackIdentityForTest(ctx, t, pool, dualUser, sqlcgen.UserRoleMaintainer)

	const channel = "C0DUAL"
	const rootTS = "1700000100.000100"

	rootEnvelope := appMentionEnvelopeWithUser("Ev0DUALROOT", channel, rootTS, "", "start this task", dualUser)
	req := signedSlackRequest(t, rootEnvelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("root mention: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	drainAllSlackRequests(rig) // discard the root mention's own ack + users.info call

	mapping, err := rig.threads.Get(ctx, channel, rootTS)
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}
	rootTurns, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil || len(rootTurns) != 1 {
		t.Fatalf("ListForSession after root mention: turns=%v err=%v, want exactly 1", rootTurns, err)
	}
	completeOpenTurn(t, rig.turns, mapping.SessionID)

	// The dual-delivery pair Slack's own real behavior sends for a SINGLE
	// @mention posted inside this already-mapped thread: an app_mention
	// event AND a plain message event, DIFFERENT event_ids, IDENTICAL
	// channel/ts/text/user.
	const replyTS = "1700000100.000200"
	const replyText = "please continue with the fix"
	appMentionTwin := appMentionEnvelopeWithUser("Ev0DUALTWINA", channel, replyTS, rootTS, replyText, dualUser)
	messageTwin := messageEnvelopeWithUser("Ev0DUALTWINB", channel, replyTS, rootTS, replyText, dualUser)

	req1 := signedSlackRequest(t, appMentionTwin)
	rec1 := httptest.NewRecorder()
	rig.handler(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("twin 1 (app_mention): status = %d, want 200 (body=%s)", rec1.Code, rec1.Body.String())
	}

	req2 := signedSlackRequest(t, messageTwin)
	rec2 := httptest.NewRecorder()
	rig.handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("twin 2 (message): status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}

	// Exactly ONE new turn -- never two, despite two independent, fully
	// successful (200 OK) webhook deliveries for the same logical mention.
	turnsAfter, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession after dual delivery: %v", err)
	}
	if len(turnsAfter) != 2 {
		t.Fatalf("len(turns) after dual delivery = %d, want 2 (root + exactly ONE new turn, not two)", len(turnsAfter))
	}

	captured := drainAllSlackRequests(rig)

	// Exactly ONE in-thread ack for this reply -- never a confusing "on
	// it" immediately followed by a second, separate "still working on
	// the previous message" ack for its own twin.
	if got := countPostMessageForThread(captured, rootTS); got != 1 {
		t.Errorf("chat.postMessage count for thread %s = %d, want exactly 1 (the dual-delivery pair must produce ONE ack, not two)", rootTS, got)
	}

	// Exactly ONE resolveSlackActor call (a real users.info API call) --
	// never a redundant second one for the twin event.
	if got := countRequestsByPath(captured, "/users.info"); got != 1 {
		t.Errorf("/users.info call count = %d, want exactly 1 (resolveSlackActor must run only once for this dual-delivery pair)", got)
	}
}

// TestHandler_DualDelivery_GenuinelyDifferentMessages_NotCoalesced is this
// batch's own explicit regression test: TWO GENUINELY DIFFERENT messages
// (different ts) posted in the SAME already-mapped thread must each still
// get their own turn/ack -- proving the L3 fix's own message-level claim
// is scoped to the SAME underlying message (channel, ts) and never
// over-coalesces an entire thread the way a mistaken channel+threadKey()
// (rather than channel+ts) key would.
func TestHandler_DualDelivery_GenuinelyDifferentMessages_NotCoalesced(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackAckTestRig(t, pool)

	const dualUser = "U0DUALDIFFERENT"
	linkSlackIdentityForTest(ctx, t, pool, dualUser, sqlcgen.UserRoleMaintainer)

	const channel = "C0DUALDIFF"
	const rootTS = "1700000110.000100"

	rootEnvelope := appMentionEnvelopeWithUser("Ev0DIFFROOT", channel, rootTS, "", "start this task", dualUser)
	req := signedSlackRequest(t, rootEnvelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("root mention: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	drainAllSlackRequests(rig) // discard the root mention's own ack

	mapping, err := rig.threads.Get(ctx, channel, rootTS)
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}
	completeOpenTurn(t, rig.turns, mapping.SessionID)

	// Message A: a genuinely different message (its own distinct ts) in
	// the SAME thread.
	const tsA = "1700000110.000200"
	envelopeA := messageEnvelopeWithUser("Ev0DIFFA", channel, tsA, rootTS, "first follow-up", dualUser)
	reqA := signedSlackRequest(t, envelopeA)
	recA := httptest.NewRecorder()
	rig.handler(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("message A: status = %d, want 200 (body=%s)", recA.Code, recA.Body.String())
	}
	turnsAfterA, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession after A: %v", err)
	}
	if len(turnsAfterA) != 2 {
		t.Fatalf("len(turns) after message A = %d, want 2 (a genuinely different message must get its own turn)", len(turnsAfterA))
	}
	completeOpenTurn(t, rig.turns, mapping.SessionID)

	// Message B: yet ANOTHER genuinely different message, same thread.
	const tsB = "1700000110.000300"
	envelopeB := messageEnvelopeWithUser("Ev0DIFFB", channel, tsB, rootTS, "second follow-up", dualUser)
	reqB := signedSlackRequest(t, envelopeB)
	recB := httptest.NewRecorder()
	rig.handler(recB, reqB)
	if recB.Code != http.StatusOK {
		t.Fatalf("message B: status = %d, want 200 (body=%s)", recB.Code, recB.Body.String())
	}
	turnsAfterB, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession after B: %v", err)
	}
	if len(turnsAfterB) != 3 {
		t.Fatalf("len(turns) after message B = %d, want 3 (each genuinely different message must get its own turn -- the L3 fix must not over-coalesce a whole thread)", len(turnsAfterB))
	}

	captured := drainAllSlackRequests(rig)
	if got := countPostMessageForThread(captured, rootTS); got != 2 {
		t.Errorf("chat.postMessage count for thread %s = %d, want 2 (one ack per genuinely distinct message, never coalesced)", rootTS, got)
	}
}

// TestHandler_DualDelivery_FailedFirstAttemptReleasesBothClaimsForRedelivery
// is the "release-on-failure" subtlety the L3 audit finding itself calls
// out: a genuine backend failure processing the FIRST of the twin events
// (mirroring authz_backend_error_integration_test.go's own deliberately-
// closed-pool technique for a deterministic, non-timing-dependent
// failure) must release BOTH the outer (provider="slack", event_id) claim
// H2 already covers AND the L3 fix's own NEW inner (provider=
// "slack-message", channel:ts) claim -- otherwise a later genuine retry
// (here, redelivering the TWIN event type for the SAME underlying
// message) would find the inner claim already taken and be silently,
// incorrectly dropped forever.
func TestHandler_DualDelivery_FailedFirstAttemptReleasesBothClaimsForRedelivery(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "dual-backend-error@example.com", DisplayName: "Dual Backend Error", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	const slackUserID = "U-DUAL-BACKEND-ERROR"
	fakeSlack := newFakeSlackWithUsersInfo(t, slackUserID, "dual-backend-error@example.com")
	auditLog := narvipg.NewAuditLogStore(pool)

	sessions := narvipg.NewSessionStore(pool)
	threads := narvipg.NewSlackThreadSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	deliveries := narvipg.NewWebhookDeliveryStore(pool)

	// CreatedBy = matchedUser.ID so the existing-mapping reply below passes
	// the own/joined carve-out once auth actually runs (irrelevant to the
	// FIRST, broken-backend attempt, which fails before ever reaching that
	// check, but needed for the SECOND, working redelivery to succeed).
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack, CreatedBy: matchedUser.ID})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	const channel = "C0DUALBACKENDERROR"
	const rootTS = "1700000120.000100"
	if _, _, err := threads.Claim(ctx, channel, rootTS, session.ID); err != nil {
		t.Fatalf("claim thread mapping: %v", err)
	}

	// A SEPARATE pool, pointed at the SAME database, closed immediately --
	// every call through a store built on it fails deterministically
	// (pgxpool.ErrClosedPool), simulating a genuine backend failure with no
	// timing dependency -- mirrors authz_backend_error_integration_test.go's
	// own identical TestHandler_ReplyOnMappedThread_AuthzBackendErrorReleasesClaim
	// precedent exactly.
	brokenPool, err := narvipg.NewPool(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("new broken pool: %v", err)
	}
	brokenPool.Close()
	brokenSessions := narvipg.NewSessionStore(brokenPool)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	brokenHandler := slack.NewHandler(slack.Deps{
		Pool:            pool,
		Sessions:        brokenSessions, // the deliberately-broken store
		Turns:           turns,
		Environments:    narvipg.NewEnvironmentStore(pool),
		Registry:        registry,
		Deliveries:      deliveries,
		Threads:         threads,
		AuditLog:        auditLog,
		Participants:    narvipg.NewParticipantStore(pool),
		SigningSecret:   testSigningSecret,
		BotToken:        "test-bot-token",
		DefaultRepoName: "narvi",
		DefaultRepoURL:  "https://github.com/khazaddev/narvi",
		TimestampWindow: 5 * time.Minute,
		SlackAPIBaseURL: fakeSlack.URL,
		AckTimeout:      platform.DefaultTimeouts().SlackAckTimeout,
		SlackClient:     slackapi.New(fakeSlack.Client(), fakeSlack.URL, "test-bot-token"),
		Timeouts:        platform.DefaultTimeouts(),
		IdentityLink:    newIdentityLinkDepsForTest(pool, auditLog),
	})

	// The FIRST of the twin events (an app_mention posted inside this
	// already-mapped thread) hits the genuinely broken Sessions store
	// INSIDE authorizeExistingSessionReply -- a real backend failure, not
	// an authz denial (matchedUser's own profile email still auto-links via
	// the WORKING IdentityLink stores, which run before authorizeSessionAction
	// ever touches the broken Sessions store).
	const twinTS = "1700000120.000200"
	const firstEventID = "Ev0DUALBACKENDERRORA"
	firstEnvelope := appMentionEnvelopeWithUser(firstEventID, channel, twinTS, rootTS, "please continue", slackUserID)
	req := signedSlackRequest(t, firstEnvelope)
	rec := httptest.NewRecorder()
	brokenHandler(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("first (broken-backend) delivery status = %d, want %d (a genuine backend failure, not a denial); body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var outerClaimCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'slack' AND delivery_id = $1`, firstEventID,
	).Scan(&outerClaimCount); err != nil {
		t.Fatalf("count outer claim rows: %v", err)
	}
	if outerClaimCount != 0 {
		t.Fatalf("outer (event_id) claim row count after failure = %d, want 0 (H2: must be released so a redelivery can retry)", outerClaimCount)
	}

	var innerClaimCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'slack-message' AND delivery_id = $1`, channel+":"+twinTS,
	).Scan(&innerClaimCount); err != nil {
		t.Fatalf("count inner claim rows: %v", err)
	}
	if innerClaimCount != 0 {
		t.Fatalf("inner (channel:ts) claim row count after failure = %d, want 0 (L3: must ALSO be released on this same failure path)", innerClaimCount)
	}

	turnsAfterFailure, err := turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListForSession after failure: %v", err)
	}
	if len(turnsAfterFailure) != 0 {
		t.Fatalf("len(turns) after broken-backend attempt = %d, want 0 (must not have proceeded past the failed authz check)", len(turnsAfterFailure))
	}

	// A genuine redelivery of the TWIN event type (message, not
	// app_mention) for the SAME underlying message, now against a WORKING
	// handler -- proves the inner claim's release actually allows a real
	// retry to succeed, not just that the row disappeared.
	workingHandler := slack.NewHandler(slack.Deps{
		Pool:            pool,
		Sessions:        sessions, // the REAL, working store this time
		Turns:           turns,
		Environments:    narvipg.NewEnvironmentStore(pool),
		Registry:        registry,
		Deliveries:      deliveries,
		Threads:         threads,
		AuditLog:        auditLog,
		Participants:    narvipg.NewParticipantStore(pool),
		SigningSecret:   testSigningSecret,
		BotToken:        "test-bot-token",
		DefaultRepoName: "narvi",
		DefaultRepoURL:  "https://github.com/khazaddev/narvi",
		TimestampWindow: 5 * time.Minute,
		SlackAPIBaseURL: fakeSlack.URL,
		AckTimeout:      platform.DefaultTimeouts().SlackAckTimeout,
		SlackClient:     slackapi.New(fakeSlack.Client(), fakeSlack.URL, "test-bot-token"),
		Timeouts:        platform.DefaultTimeouts(),
		IdentityLink:    newIdentityLinkDepsForTest(pool, auditLog),
	})

	const secondEventID = "Ev0DUALBACKENDERRORB"
	secondEnvelope := messageEnvelopeWithUser(secondEventID, channel, twinTS, rootTS, "please continue", slackUserID)
	req2 := signedSlackRequest(t, secondEnvelope)
	rec2 := httptest.NewRecorder()
	workingHandler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("redelivered twin (message) status = %d, want %d (body=%s)", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	turnsAfterRetry, err := turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListForSession after retry: %v", err)
	}
	if len(turnsAfterRetry) != 1 {
		t.Errorf("len(turns) after redelivery = %d, want exactly 1 (the released inner claim must let a genuine retry actually succeed)", len(turnsAfterRetry))
	}
}
