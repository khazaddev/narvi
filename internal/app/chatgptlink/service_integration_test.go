//go:build integration

// Integration tests for StartLink/PollLink/Unlink against a REAL Postgres
// instance (§9.1) -- gated behind the "integration" build tag, mirroring
// internal/app/identitylink/service_integration_test.go's own conventions
// exactly (testcontainers Postgres, embedded migrations via golang-
// migrate's iofs source driver) -- PLUS a fake auth.openai.com
// (httptest.Server) standing in for the real device-flow endpoints, since
// this environment has no real OpenAI credential (this Step's own hard
// constraint). Run via `make test-integration`.
package chatgptlink_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	"github.com/narvidev/narvi/internal/adapters/outbound/chatgptoauth"
	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/chatgptlink"
	"github.com/narvidev/narvi/internal/platform"
	"github.com/narvidev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every embedded
// migration up, and returns a ready *pgxpool.Pool -- mirrors internal/app/
// identitylink's own identical helper (that file's own doc comment has
// the full "why a watchdog alongside ctx cancellation" reasoning).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

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
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s", containerStartWatchdog)
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
		t.Fatalf("migrate postgres driver: %v", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("migrate iofs source: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

const testTokenEncryptionKey = "01234567890123456789012345678901" // 32 bytes, test-only

func newDeps(pool *pgxpool.Pool, deviceFlowBaseURL string) chatgptlink.Deps {
	timeouts := platform.DefaultTimeouts()
	return chatgptlink.Deps{
		Pool:                pool,
		LinkAttempts:        narvipg.NewChatGPTLinkAttemptStore(pool),
		ProviderCredentials: narvipg.NewProviderCredentialStore(pool),
		AuditLog:            narvipg.NewAuditLogStore(pool),
		DeviceFlow:          chatgptoauth.New(http.DefaultClient, deviceFlowBaseURL, timeouts.ChatGPTOAuthHTTPClientTimeout),
		TokenEncryptionKey:  []byte(testTokenEncryptionKey)[:32],
		Timeouts:            timeouts,
	}
}

func createFixtureUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, email string) sqlcgen.User {
	t.Helper()
	users := narvipg.NewUserStore(pool)
	u, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: email, DisplayName: email, Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture user %q: %v", email, err)
	}
	return u
}

// fakeJWT mirrors chatgptoauth's own identically-named test helper
// (client_test.go) -- duplicated here (that one is unexported, and this
// is a different package/binary) rather than shared, matching this
// codebase's own small-test-helper-duplication precedent.
func fakeJWT(payloadJSON string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return header + "." + payload + ".sig"
}

// fakeAuthServer is a small, configurable fake auth.openai.com covering
// the 2 endpoints these tests actually exercise -- each test wires only
// the handler(s) it needs; an unwired endpoint 404s (surfacing as a clear
// test failure if a test's own assumptions about which endpoints get
// called are wrong).
type fakeAuthServer struct {
	usercodeCalls   int
	tokenPollCalls  int
	tokenPollStatus int // set by the test; 200 = grant, 403 = pending

	// usercodeExpiresAt overrides the usercode endpoint's own "expires_at"
	// response field -- the zero value keeps the previous default
	// (now+15m). M3 (adversarial review): parameterized so
	// TestStartLink_ExpiresAtCappedAtChatGPTLinkAttemptTTL below can drive
	// StartLink's own min(server expires_at, now+TTL) cap from BOTH
	// sides -- the un-parameterized default happened to sit exactly AT
	// ChatGPTLinkAttemptTTL (15m), making the cap comparison's own
	// outcome an unasserted coin flip.
	usercodeExpiresAt time.Time
}

func (f *fakeAuthServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, _ *http.Request) {
		f.usercodeCalls++
		// interval is a STRING and expires_at is present -- the real,
		// live-verified shape (chatgptoauth's own usercode canary), not
		// §29.2's own original, incomplete field-type assumption. "0" as
		// the interval string means every PollLink call in these tests is
		// immediately due (never throttled) unless a test explicitly sets
		// its own last_polled_at, exactly like this fake's own pre-Step-
		// 59-fix "interval": 0 behaved.
		expiresAt := f.usercodeExpiresAt
		if expiresAt.IsZero() {
			expiresAt = time.Now().Add(15 * time.Minute)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "dev-123", "user_code": "WDJB-MJHT", "interval": "0",
			"expires_at": expiresAt.Format(time.RFC3339Nano),
		})
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, _ *http.Request) {
		f.tokenPollCalls++
		if f.tokenPollStatus != http.StatusOK {
			w.WriteHeader(f.tokenPollStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_code": "auth-code-xyz", "code_verifier": "verifier-abc",
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		idToken := fakeJWT(`{"chatgpt_account_id":"acct-xyz-789"}`)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 864000, "id_token": idToken,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestStartLink_MintsFreshAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "start-link@example.com")

	status, err := chatgptlink.StartLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("StartLink() error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusPending {
		t.Errorf("Status = %q, want %q", status.Status, chatgptlink.StatusPending)
	}
	if status.UserCode != "WDJB-MJHT" {
		t.Errorf("UserCode = %q, want %q", status.UserCode, "WDJB-MJHT")
	}
	if status.VerificationURL != chatgptoauth.VerificationURL {
		t.Errorf("VerificationURL = %q, want %q", status.VerificationURL, chatgptoauth.VerificationURL)
	}
	if status.ExpiresAt == nil || !status.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want a real future time", status.ExpiresAt)
	}
	if fake.usercodeCalls != 1 {
		t.Errorf("usercodeCalls = %d, want 1", fake.usercodeCalls)
	}
}

func TestStartLink_ReusesLiveAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "reuse-link@example.com")

	first, err := chatgptlink.StartLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("StartLink() [1st] error = %v, want nil", err)
	}
	second, err := chatgptlink.StartLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("StartLink() [2nd] error = %v, want nil", err)
	}
	if second.UserCode != first.UserCode {
		t.Errorf("2nd StartLink() UserCode = %q, want the SAME code as the 1st (%q) -- a still-live attempt must be reused, never re-minted", second.UserCode, first.UserCode)
	}
	if fake.usercodeCalls != 1 {
		t.Errorf("usercodeCalls = %d, want 1 (the 2nd StartLink call must not hit upstream again)", fake.usercodeCalls)
	}
}

func TestPollLink_Unlinked_NoAttemptNoCredential(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "poll-unlinked@example.com")

	status, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusUnlinked {
		t.Errorf("Status = %q, want %q", status.Status, chatgptlink.StatusUnlinked)
	}
}

func TestPollLink_ThrottledWithinInterval_NeverCallsUpstream(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{tokenPollStatus: http.StatusOK}
	deps := newDeps(pool, fake.start(t))
	// A LONG interval (1 hour) so "just polled" definitely still counts as
	// throttled for the immediate 2nd PollLink call below.
	user := createFixtureUser(ctx, t, pool, "throttled@example.com")
	attempt, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-123", "WDJB-MJHT", int32((1 * time.Hour).Seconds()), time.Now().Add(deps.Timeouts.ChatGPTLinkAttemptTTL))
	if err != nil {
		t.Fatalf("create fixture attempt: %v", err)
	}
	if _, err := deps.LinkAttempts.UpdateLastPolledAt(ctx, attempt.ID, time.Now()); err != nil {
		t.Fatalf("set last_polled_at: %v", err)
	}

	status, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusPending {
		t.Errorf("Status = %q, want %q", status.Status, chatgptlink.StatusPending)
	}
	if fake.tokenPollCalls != 0 {
		t.Errorf("tokenPollCalls = %d, want 0 (throttled -- must not call upstream before the interval elapses)", fake.tokenPollCalls)
	}
}

func TestPollLink_StillPending_UpstreamNotYetGranted(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{tokenPollStatus: http.StatusForbidden}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "still-pending@example.com")
	if _, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-123", "WDJB-MJHT", 0, time.Now().Add(deps.Timeouts.ChatGPTLinkAttemptTTL)); err != nil {
		t.Fatalf("create fixture attempt: %v", err)
	}

	status, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusPending {
		t.Errorf("Status = %q, want %q", status.Status, chatgptlink.StatusPending)
	}
	if fake.tokenPollCalls != 1 {
		t.Errorf("tokenPollCalls = %d, want 1", fake.tokenPollCalls)
	}
}

func TestPollLink_GrantedTransitionsToLinked(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{tokenPollStatus: http.StatusOK}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "granted@example.com")
	if _, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-123", "WDJB-MJHT", 0, time.Now().Add(deps.Timeouts.ChatGPTLinkAttemptTTL)); err != nil {
		t.Fatalf("create fixture attempt: %v", err)
	}

	status, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusLinked {
		t.Errorf("Status = %q, want %q", status.Status, chatgptlink.StatusLinked)
	}

	// The pending attempt must be gone (no longer "in flight").
	if _, err := deps.LinkAttempts.GetLatestForUser(ctx, user.ID); err == nil {
		t.Error("GetLatestForUser after a grant: got a row, want none (the attempt must be deleted on success)")
	}

	// A stored, resolvable oauth credential must now exist, and a SECOND
	// poll must report "linked" purely from provider_credentials, with NO
	// further upstream calls at all.
	callsBeforeSecondPoll := fake.tokenPollCalls
	second, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() [2nd, post-link] error = %v, want nil", err)
	}
	if second.Status != chatgptlink.StatusLinked {
		t.Errorf("2nd PollLink() Status = %q, want %q", second.Status, chatgptlink.StatusLinked)
	}
	if fake.tokenPollCalls != callsBeforeSecondPoll {
		t.Errorf("tokenPollCalls after 2nd poll = %d, want unchanged from %d (nothing pending left to poll)", fake.tokenPollCalls, callsBeforeSecondPoll)
	}
}

func TestUnlink_RemovesCredentialAndAttempts(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{tokenPollStatus: http.StatusOK}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "unlink@example.com")
	if _, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-123", "WDJB-MJHT", 0, time.Now().Add(deps.Timeouts.ChatGPTLinkAttemptTTL)); err != nil {
		t.Fatalf("create fixture attempt: %v", err)
	}
	if _, err := chatgptlink.PollLink(ctx, deps, user.ID); err != nil {
		t.Fatalf("PollLink() (to establish a link) error = %v, want nil", err)
	}

	if err := chatgptlink.Unlink(ctx, deps, user.ID); err != nil {
		t.Fatalf("Unlink() error = %v, want nil", err)
	}

	status, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() (post-unlink) error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusUnlinked {
		t.Errorf("Status after Unlink() = %q, want %q", status.Status, chatgptlink.StatusUnlinked)
	}

	// Idempotent: unlinking an already-unlinked user must not error.
	if err := chatgptlink.Unlink(ctx, deps, user.ID); err != nil {
		t.Errorf("2nd Unlink() (already unlinked) error = %v, want nil (idempotent)", err)
	}
}

// TestStartLink_ExpiresAtCappedAtChatGPTLinkAttemptTTL is M3's own
// regression test (adversarial review) for StartLink's own
// min(server-provided expires_at, now+ChatGPTLinkAttemptTTL) cap
// (service.go). Before this test, the cap was untestable-by-construction:
// this file's own fakeAuthServer hardcoded "expires_at": now+15m, exactly
// ChatGPTLinkAttemptTTL's own default value -- so the `expiresAtCap.
// Before(expiresAt)` comparison's own outcome was a coin flip on wall-clock
// jitter between the fake's own time.Now() call and StartLink's, and
// nothing asserted it either way. A hostile or drifting far-future
// expires_at (there is no background sweep for this table) would create a
// link attempt that never expires and is reused forever, entirely
// unnoticed. Drives the cap from BOTH sides via fakeAuthServer's own new
// usercodeExpiresAt field.
func TestStartLink_ExpiresAtCappedAtChatGPTLinkAttemptTTL(t *testing.T) {
	tests := []struct {
		name            string
		serverExpiresIn time.Duration
		wantCapped      bool
	}{
		{"server value comfortably below the cap is honored verbatim", 2 * time.Minute, false},
		{"server value far above the cap is clamped to the TTL", 100 * time.Hour, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)
			fake := &fakeAuthServer{usercodeExpiresAt: time.Now().Add(tt.serverExpiresIn)}
			deps := newDeps(pool, fake.start(t))
			user := createFixtureUser(ctx, t, pool, fmt.Sprintf("cap-test-capped-%v@example.com", tt.wantCapped))

			before := time.Now()
			status, err := chatgptlink.StartLink(ctx, deps, user.ID)
			if err != nil {
				t.Fatalf("StartLink() error = %v, want nil", err)
			}
			if status.ExpiresAt == nil {
				t.Fatal("ExpiresAt is nil, want a real time")
			}

			ttlCeiling := before.Add(deps.Timeouts.ChatGPTLinkAttemptTTL)
			serverValue := before.Add(tt.serverExpiresIn)
			const slack = 10 * time.Second // generous test wall-clock jitter allowance.

			if tt.wantCapped {
				if status.ExpiresAt.After(ttlCeiling.Add(slack)) {
					t.Errorf("ExpiresAt = %v, want capped at approximately now+TTL (%v)", status.ExpiresAt, ttlCeiling)
				}
				if status.ExpiresAt.After(serverValue.Add(-time.Minute)) {
					t.Errorf("ExpiresAt = %v, want clamped well BELOW the server's own far-future value %v, not honored verbatim", status.ExpiresAt, serverValue)
				}
			} else {
				if status.ExpiresAt.After(serverValue.Add(slack)) || status.ExpiresAt.Before(serverValue.Add(-slack)) {
					t.Errorf("ExpiresAt = %v, want honored verbatim, close to the server's own value %v (uncapped -- well under the TTL)", status.ExpiresAt, serverValue)
				}
			}
		})
	}
}

// TestStartLink_ExpiredAttemptIsReplacedWithAFreshOne is M3's own
// regression test (adversarial review) for StartLink's own
// expired-then-remint branch (service.go: "Expired -- fall through and
// mint a fresh one"), previously entirely uncovered -- every existing
// StartLink test seeded either no attempt at all or a still-live one.
func TestStartLink_ExpiredAttemptIsReplacedWithAFreshOne(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "expired-remint@example.com")

	expired, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-old", "OLD-STALE-CODE", 0, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("seed expired attempt: %v", err)
	}

	status, err := chatgptlink.StartLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("StartLink() error = %v, want nil", err)
	}
	if status.UserCode != "WDJB-MJHT" {
		t.Errorf("UserCode = %q, want the freshly minted %q, not the expired attempt's own stale code", status.UserCode, "WDJB-MJHT")
	}
	if fake.usercodeCalls != 1 {
		t.Errorf("usercodeCalls = %d, want 1 (a fresh device code must actually be minted for an expired attempt, not silently reused)", fake.usercodeCalls)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM chatgpt_link_attempts WHERE user_id = $1`, user.ID).Scan(&count); err != nil {
		t.Fatalf("count attempt rows: %v", err)
	}
	if count != 1 {
		t.Errorf("chatgpt_link_attempts row count for this user = %d, want exactly 1 (the expired row must be deleted, never left lingering alongside the fresh one)", count)
	}

	latest, err := deps.LinkAttempts.GetLatestForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetLatestForUser: %v", err)
	}
	if latest.ID == expired.ID {
		t.Error("the latest attempt row is still the expired one, want a freshly minted replacement")
	}
}

// TestPollLink_ExpiredAttemptIsDeletedAndFallsThroughToCredentialState is
// M3's own regression test (adversarial review) for PollLink's own
// expired-attempt-cleanup branch (service.go: "An attempt row exists but
// has expired -> best-effort delete, then [report whatever
// provider_credentials already says]"), previously entirely uncovered.
func TestPollLink_ExpiredAttemptIsDeletedAndFallsThroughToCredentialState(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeAuthServer{tokenPollStatus: http.StatusOK}
	deps := newDeps(pool, fake.start(t))
	user := createFixtureUser(ctx, t, pool, "poll-expired@example.com")

	if _, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-old", "OLD-STALE-CODE", 0, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("seed expired attempt: %v", err)
	}

	status, err := chatgptlink.PollLink(ctx, deps, user.ID)
	if err != nil {
		t.Fatalf("PollLink() error = %v, want nil", err)
	}
	if status.Status != chatgptlink.StatusUnlinked {
		t.Errorf("Status = %q, want %q (no provider_credentials row exists, and the expired attempt must not itself be treated as still-pending)", status.Status, chatgptlink.StatusUnlinked)
	}
	if fake.tokenPollCalls != 0 {
		t.Errorf("tokenPollCalls = %d, want 0 (an EXPIRED attempt must never be polled upstream at all)", fake.tokenPollCalls)
	}

	if _, err := deps.LinkAttempts.GetLatestForUser(ctx, user.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetLatestForUser after PollLink observed an expired attempt: err = %v, want pgx.ErrNoRows (the expired row must be deleted)", err)
	}
}

// TestPollLink_NonPendingUpstreamFailureDegradesToPending is M3's own
// regression test (adversarial review) for PollLink's own third failure
// path: PollDeviceToken failing with something OTHER than the pending-
// shaped 403/404 (service.go's own switch, the `case err != nil:` arm) --
// logged and degraded to "still pending" for this one call, never a hard
// error, since the human is mid-flow watching the Settings page and a
// single transient upstream hiccup must not read as "link failed".
// Previously entirely uncovered: this file's own fakeAuthServer had only
// ever been driven with tokenPollStatus 200 or 403.
func TestPollLink_NonPendingUpstreamFailureDegradesToPending(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
	}{
		{"upstream 400 degrades to pending, not a hard error", http.StatusBadRequest},
		{"upstream 500 degrades to pending, not a hard error", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)
			fake := &fakeAuthServer{tokenPollStatus: tt.httpStatus}
			deps := newDeps(pool, fake.start(t))
			user := createFixtureUser(ctx, t, pool, fmt.Sprintf("nonpending-%d@example.com", tt.httpStatus))
			if _, err := deps.LinkAttempts.Create(ctx, user.ID, "dev-123", "WDJB-MJHT", 0, time.Now().Add(deps.Timeouts.ChatGPTLinkAttemptTTL)); err != nil {
				t.Fatalf("create fixture attempt: %v", err)
			}

			status, err := chatgptlink.PollLink(ctx, deps, user.ID)
			if err != nil {
				t.Fatalf("PollLink() error = %v, want nil (a genuine upstream failure must degrade to pending, not fail the whole poll)", err)
			}
			if status.Status != chatgptlink.StatusPending {
				t.Errorf("Status = %q, want %q", status.Status, chatgptlink.StatusPending)
			}
			if fake.tokenPollCalls != 1 {
				t.Errorf("tokenPollCalls = %d, want 1", fake.tokenPollCalls)
			}

			// The attempt row must still be alive (not deleted) -- a
			// transient/unexpected upstream failure must not destroy state
			// the human is still mid-flow on.
			if _, err := deps.LinkAttempts.GetLatestForUser(ctx, user.ID); err != nil {
				t.Errorf("GetLatestForUser after a degrade-to-pending poll: err = %v, want the attempt row to still exist", err)
			}
		})
	}
}
