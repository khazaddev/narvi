//go:build integration

// Integration test for the MEDIUM audit fix ("authorizeSessionAction
// conflates a genuine backend error with a real authorization denial",
// internal/adapters/inbound/slack/identity.go): a transient backend
// failure encountered WHILE checking authorization must be distinguished
// from a genuine denial -- the former now flows into the SAME
// release-the-claim-and-retry path H2 already wired up for every other
// post-claim failure, rather than being silently treated as "skip, no
// release" the way a one-off DB blip previously was. Mirrors
// identity_integration_test.go's own conventions (a real, auto-linkable
// fixture user, a real slack.NewHandler) exactly, except deps.Sessions is
// built on a pool that's already been closed -- every call through it
// fails deterministically (pgxpool.ErrClosedPool), with no timing
// dependency, simulating "deps.Sessions.Get hitting a dropped connection"
// (authorizeSessionAction's own ErrActorNotAuthorized doc comment) without
// needing an actual dropped network connection.
package slack_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/inbound/slack"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestHandler_ReplyOnMappedThread_AuthzBackendErrorReleasesClaim is the
// MEDIUM audit fix's own headline proof: a genuine backend failure INSIDE
// authorizeSessionAction (deps.Sessions.Get erroring for a reason having
// nothing to do with the actor's own authorization) must NOT be silently
// conflated with a real denial. Before this fix, authorizeSessionAction's
// own bare bool made the two indistinguishable: the claim was never
// released, the webhook answered 200, and the reply was silently dropped
// forever with no chance of a redelivery ever retrying it. This proves the
// SAME release-the-claim-and-answer-non-2xx path H2 already wired up for
// every other post-claim failure now fires here too.
func TestHandler_ReplyOnMappedThread_AuthzBackendErrorReleasesClaim(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	users := narvipg.NewUserStore(pool)
	matchedUser, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "backend-error-replier@example.com", DisplayName: "Backend Error Replier", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	fakeSlack := newFakeSlackWithUsersInfo(t, "U-BACKEND-ERROR", "backend-error-replier@example.com")
	auditLog := narvipg.NewAuditLogStore(pool)

	sessions := narvipg.NewSessionStore(pool)
	threads := narvipg.NewSlackThreadSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)

	// CreatedBy = matchedUser.ID: ownership is irrelevant to this test (the
	// broken Sessions store below fails BEFORE OwnedOrJoined is ever
	// reached), but mirrors this package's own established fixture shape.
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack, CreatedBy: matchedUser.ID})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := threads.Claim(ctx, "C0BACKENDERROR", "1700000090.000100", session.ID); err != nil {
		t.Fatalf("claim thread mapping: %v", err)
	}

	// A SEPARATE pool, pointed at the SAME database, closed immediately --
	// every subsequent call through a store built on it fails
	// deterministically (pgxpool.ErrClosedPool), simulating a genuine
	// "backend call failed while checking" with no timing dependency.
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

	handler := slack.NewHandler(slack.Deps{
		Pool:            pool,
		Sessions:        brokenSessions, // the deliberately-broken store
		Turns:           turns,
		Environments:    narvipg.NewEnvironmentStore(pool),
		Registry:        registry,
		Deliveries:      narvipg.NewWebhookDeliveryStore(pool),
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

	const eventID = "Ev0BACKENDERROR001"
	envelope := messageEnvelopeWithUser(eventID, "C0BACKENDERROR", "1700000090.000200", "1700000090.000100", "please continue", "U-BACKEND-ERROR")
	req := signedSlackRequest(t, envelope)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (a genuine backend failure during the authz check, not a denial); body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var deliveryRowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'slack' AND delivery_id = $1`, eventID,
	).Scan(&deliveryRowCount); err != nil {
		t.Fatalf("count webhook_deliveries: %v", err)
	}
	if deliveryRowCount != 0 {
		t.Errorf("webhook_deliveries row count = %d, want 0 (the claim must be released so a redelivery can retry)", deliveryRowCount)
	}

	turnsAfter, err := turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(turnsAfter) != 0 {
		t.Errorf("len(turns) = %d, want 0 (must not have proceeded past the failed authz check)", len(turnsAfter))
	}
}
