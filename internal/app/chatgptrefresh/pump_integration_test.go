//go:build integration

// Integration tests for Pump.PumpOnce against a REAL Postgres instance
// (§9.1), mirroring internal/app/chatgptlink/service_integration_test.go
// 's own conventions exactly (same testcontainers Postgres helper, same
// fake auth.openai.com approach -- this environment has no real OpenAI
// credential, this Step's own hard constraint). Run via
// `make test-integration`.
package chatgptrefresh_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/outbound/chatgptoauth"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/chatgptrefresh"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool mirrors internal/app/chatgptlink's own identically-named
// helper exactly (that file's own doc comment has the full "why a
// watchdog alongside ctx cancellation" reasoning).
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

// fakeRefreshServer is a minimal fake auth.openai.com covering ONLY
// /oauth/token (the only endpoint Pump ever calls) -- status/body fully
// caller-controlled per test.
type fakeRefreshServer struct {
	calls      int
	statusCode int
	errorCode  string // set when statusCode is non-2xx
	accountID  string // "" omits id_token from the response entirely
}

func (f *fakeRefreshServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.calls++
		if f.statusCode != 0 && f.statusCode != http.StatusOK {
			w.WriteHeader(f.statusCode)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": f.errorCode})
			return
		}
		resp := map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 864000}
		if f.accountID != "" {
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
			payload, _ := json.Marshal(map[string]string{"chatgpt_account_id": f.accountID})
			resp["id_token"] = header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func newTestDeps(pool *pgxpool.Pool, deviceFlowBaseURL string) (*narvipg.ProviderCredentialStore, *chatgptrefresh.Pump) {
	timeouts := platform.DefaultTimeouts()
	store := narvipg.NewProviderCredentialStore(pool)
	deviceFlow := chatgptoauth.New(http.DefaultClient, deviceFlowBaseURL, timeouts.ChatGPTOAuthHTTPClientTimeout)
	pump := chatgptrefresh.NewPump(store, pool, deviceFlow, []byte(testTokenEncryptionKey)[:32], timeouts)
	return store, pump
}

func createExpiringCredential(ctx context.Context, t *testing.T, store *narvipg.ProviderCredentialStore, userID string, accountID string, expiresAt time.Time) sqlcgen.ProviderCredential {
	t.Helper()
	return createExpiringCredentialWithRefreshToken(ctx, t, store, userID, accountID, "old-refresh", expiresAt)
}

// createExpiringCredentialWithRefreshToken is createExpiringCredential's
// own parameterized twin -- the S1 (adversarial review) concurrency/
// interruption tests below each need MULTIPLE rows whose own upstream
// /oauth/token calls are individually distinguishable (by the posted
// "refresh_token" form value), which createExpiringCredential's hardcoded
// "old-refresh" can't provide.
func createExpiringCredentialWithRefreshToken(ctx context.Context, t *testing.T, store *narvipg.ProviderCredentialStore, userID, accountID, refreshToken string, expiresAt time.Time) sqlcgen.ProviderCredential {
	t.Helper()
	blob, _ := json.Marshal(map[string]any{
		"access": "old-access", "refresh": refreshToken, "expires_ms": expiresAt.UnixMilli(), "account_id": accountID,
	})
	encrypted, err := platform.EncryptToken([]byte(testTokenEncryptionKey)[:32], blob)
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	row, err := store.UpsertOAuth(ctx, userID, sqlcgen.ProviderCredentialProviderOpenai, encrypted, expiresAt)
	if err != nil {
		t.Fatalf("UpsertOAuth: %v", err)
	}
	return row
}

func TestPumpOnce_RefreshesExpiringCredential(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeRefreshServer{statusCode: http.StatusOK}
	store, pump := newTestDeps(pool, fake.start(t))

	// Expires in 1h -- comfortably within the default 72h margin.
	row := createExpiringCredential(ctx, t, store, "11111111-1111-1111-1111-111111111111", "acct-original", time.Now().Add(time.Hour))

	if err := pump.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce() error = %v, want nil", err)
	}
	if fake.calls != 1 {
		t.Fatalf("upstream refresh calls = %d, want 1", fake.calls)
	}

	got, err := store.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.OauthExpiresAt.Valid || !got.OauthExpiresAt.Time.After(time.Now().Add(24*time.Hour)) {
		t.Errorf("OauthExpiresAt = %v, want pushed comfortably into the future by the refresh", got.OauthExpiresAt)
	}
	if got.OauthNeedsRelink {
		t.Error("OauthNeedsRelink = true after a successful refresh, want false")
	}

	plaintext, err := platform.DecryptToken([]byte(testTokenEncryptionKey)[:32], got.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	var blob map[string]any
	if err := json.Unmarshal(plaintext, &blob); err != nil {
		t.Fatalf("unmarshal blob: %v", err)
	}
	if blob["access"] != "new-access" || blob["refresh"] != "new-refresh" {
		t.Errorf("blob = %+v, want the rotated access/refresh tokens", blob)
	}
	if blob["account_id"] != "acct-original" {
		t.Errorf("blob[account_id] = %v, want %q PRESERVED from before the refresh (§29.10 risk 7) -- the refresh response never carried an id_token at all here", blob["account_id"], "acct-original")
	}
}

func TestPumpOnce_TerminalFailureMarksNeedsRelink(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeRefreshServer{statusCode: http.StatusBadRequest, errorCode: "refresh_token_reused"}
	store, pump := newTestDeps(pool, fake.start(t))
	row := createExpiringCredential(ctx, t, store, "22222222-2222-2222-2222-222222222222", "acct-x", time.Now().Add(time.Hour))

	if err := pump.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce() error = %v, want nil (a terminal per-row failure must not fail the whole batch)", err)
	}

	got, err := store.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.OauthNeedsRelink {
		t.Error("OauthNeedsRelink = false after a refresh_token_reused failure, want true")
	}
}

func TestPumpOnce_TransientFailureLeavesRowUnchanged(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeRefreshServer{statusCode: http.StatusServiceUnavailable}
	store, pump := newTestDeps(pool, fake.start(t))
	originalExpiry := time.Now().Add(time.Hour)
	row := createExpiringCredential(ctx, t, store, "33333333-3333-3333-3333-333333333333", "acct-x", originalExpiry)

	if err := pump.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce() error = %v, want nil", err)
	}

	got, err := store.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.OauthNeedsRelink {
		t.Error("OauthNeedsRelink = true after a transient (503) failure, want false -- must retry next cycle, not force a relink")
	}
	if got.OauthExpiresAt.Time.Unix() != originalExpiry.Unix() {
		t.Errorf("OauthExpiresAt changed after a transient failure: got %v, want unchanged from %v (the last stored pair)", got.OauthExpiresAt.Time, originalExpiry)
	}
}

func TestPumpOnce_IgnoresRowsNotYetDue(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeRefreshServer{statusCode: http.StatusOK}
	store, pump := newTestDeps(pool, fake.start(t))
	// Expires in 100h -- well beyond the default 72h margin.
	createExpiringCredential(ctx, t, store, "44444444-4444-4444-4444-444444444444", "acct-x", time.Now().Add(100*time.Hour))

	if err := pump.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce() error = %v, want nil", err)
	}
	if fake.calls != 0 {
		t.Errorf("upstream refresh calls = %d, want 0 (not yet within the refresh margin)", fake.calls)
	}
}

func TestPumpOnce_SkipsRowsAlreadyNeedsRelink(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	fake := &fakeRefreshServer{statusCode: http.StatusOK}
	store, pump := newTestDeps(pool, fake.start(t))
	row := createExpiringCredential(ctx, t, store, "55555555-5555-5555-5555-555555555555", "acct-x", time.Now().Add(time.Hour))
	if _, err := store.MarkNeedsRelink(ctx, row.ID); err != nil {
		t.Fatalf("MarkNeedsRelink: %v", err)
	}

	if err := pump.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce() error = %v, want nil", err)
	}
	if fake.calls != 0 {
		t.Errorf("upstream refresh calls = %d, want 0 (a needs-relink row must never be auto-retried by the pump)", fake.calls)
	}
}

// TestPumpOnce_ConcurrentTicksNeverDoubleRefreshTheSameRow is S1's own
// (adversarial review) concurrency regression test: every one of this
// file's five OTHER tests above is single-row and strictly sequential, so
// the FOR UPDATE SKIP LOCKED guarantee -- this package's own doc.go names
// it the design's whole reason for holding a lock across a live upstream
// call at all ("the failure mode a held lock prevents -- two pump
// instances concurrently refreshing the SAME row -- is exactly the
// 'concurrent jobs sharing one credential' case OpenAI's own docs
// prohibit") -- was never actually exercised under real concurrency.
//
// Two independent Pump instances (mirroring two separate pods/processes
// sharing one DB, doc.go's own framing) each call PumpOnce REPEATEDLY,
// concurrently with each other, against N=25 due rows -- deliberately
// MORE than the production pumpBatchSize (20), so a single PumpOnce call
// on either side can never claim every row alone; full coverage requires
// genuine interleaving across both goroutines' own multiple ticks. The
// one property that must hold regardless of how those ticks interleave:
// every row's own upstream /oauth/token call count is EXACTLY 1, never 0
// (eventually covered) and never 2+ (never double-refreshed).
func TestPumpOnce_ConcurrentTicksNeverDoubleRefreshTheSameRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	var mu sync.Mutex
	callsByToken := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		refreshToken := r.FormValue("refresh_token")
		mu.Lock()
		callsByToken[refreshToken]++
		mu.Unlock()
		resp := map[string]any{
			"access_token": "new-access-" + refreshToken, "refresh_token": "new-refresh-" + refreshToken, "expires_in": 864000,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	_, pump1 := newTestDeps(pool, srv.URL)
	_, pump2 := newTestDeps(pool, srv.URL)

	const n = 25 // > the production pumpBatchSize (20).
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		refreshToken := fmt.Sprintf("old-refresh-%02d", i)
		tokens[i] = refreshToken
		createExpiringCredentialWithRefreshToken(ctx, t, narvipg.NewProviderCredentialStore(pool),
			fmt.Sprintf("concurrent-user-%02d", i), "acct-x", refreshToken, time.Now().Add(time.Hour))
	}

	// Each goroutine ticks multiple times (not just once): pumpBatchSize
	// bounds a SINGLE PumpOnce call's own candidate snapshot, so with
	// n=25 > 20, one tick per side is not guaranteed to cover every row
	// even in the ABSENCE of contention -- looping guarantees eventual
	// full coverage while the two goroutines' own ticks still genuinely
	// overlap in wall-clock time (especially their very first ticks,
	// racing for the SAME initial due set).
	const ticksPerGoroutine = 4
	var eg errgroup.Group
	for _, p := range []*chatgptrefresh.Pump{pump1, pump2} {
		eg.Go(func() error {
			for i := 0; i < ticksPerGoroutine; i++ {
				if err := p.PumpOnce(ctx); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("PumpOnce (concurrent) error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, tok := range tokens {
		if callsByToken[tok] != 1 {
			t.Errorf("upstream refresh calls for token %q = %d, want exactly 1 (never un-covered, never double-refreshed)", tok, callsByToken[tok])
		}
	}
}

// TestPumpOnce_InterruptionAfterOneRowCommitNeverLosesItsRotation is S1's
// own (adversarial review) regression test for the PRIMARY named
// mechanism: the OLD PumpOnce ran an entire batch inside ONE shared
// transaction, committed once at the end -- so an interruption AFTER one
// row's own upstream refresh had already rotated (and so, upstream,
// CONSUMED) its refresh token, but BEFORE that single final commit,
// rolled back that row's own already-consumed rotation along with the
// rest of the batch. The next tick would then replay the stale,
// already-consumed token straight into a terminal
// refresh_token_reused/invalid_grant failure -- exactly the forced-relink
// lockout §29.5's single-pump design exists to prevent.
//
// Reproduced deterministically, not by timing luck: row A (expires
// soonest, so claimed first) gets a fast, real refresh response; row B
// (claimed second) gets a response that hangs indefinitely, well past
// pumpCtx's own short deadline -- so pumpCtx is GUARANTEED to already be
// Done by the time row B's own claim/refresh step runs, while row A's own
// claim+refresh+write had ample time to finish first (PumpOnce processes
// candidates strictly in order). Before the fix (one shared batch
// transaction), the batch's own single final commit then runs against an
// already-expired ctx and fails, losing row A's own already-rotated
// tokens too, even though they were never actually rolled back upstream.
// After the fix (commit per row), row A's own commit already landed,
// independently, before row B is ever even attempted -- it must survive
// regardless of what happens to row B or to pumpCtx afterward.
func TestPumpOnce_InterruptionAfterOneRowCommitNeverLosesItsRotation(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.FormValue("refresh_token") {
		case "old-refresh-A":
			resp := map[string]any{"access_token": "new-access-A", "refresh_token": "new-refresh-A", "expires_in": 864000}
			_ = json.NewEncoder(w).Encode(resp)
		case "old-refresh-B":
			// Blocks until the CLIENT (pumpCtx below) gives up and tears
			// down the connection -- r.Context() is cancelled when that
			// happens, so this handler goroutine reliably unblocks itself
			// rather than hanging forever (which would otherwise wedge
			// httptest.Server's own graceful Close/t.Cleanup below).
			<-r.Context().Done()
		}
	}))
	t.Cleanup(srv.Close)

	store, pump := newTestDeps(pool, srv.URL)
	rowA := createExpiringCredentialWithRefreshToken(ctx, t, store, "row-a-user", "acct-a", "old-refresh-A", time.Now().Add(30*time.Minute))
	createExpiringCredentialWithRefreshToken(ctx, t, store, "row-b-user", "acct-b", "old-refresh-B", time.Now().Add(time.Hour))

	// Comfortably longer than a fast local round-trip for row A, but
	// short enough that row B's own deliberately-hanging call is
	// guaranteed to still be in flight when it fires -- ChatGPTOAuthHTTPClientTimeout's
	// own 15s default (platform.DefaultTimeouts) is far longer than
	// this, so THIS deadline (not that inner one) is what actually
	// aborts row B's own call.
	const pumpDeadline = 300 * time.Millisecond
	pumpCtx, cancel := context.WithTimeout(ctx, pumpDeadline)
	defer cancel()

	// PumpOnce's own return value is deliberately not asserted here: this
	// test is about whether row A's own already-committed rotation
	// survives, not about what error (if any) a claim step interrupted
	// mid-flight happens to surface.
	_ = pump.PumpOnce(pumpCtx)

	gotA, err := store.Get(ctx, rowA.ID)
	if err != nil {
		t.Fatalf("Get row A: %v", err)
	}
	if !gotA.OauthExpiresAt.Time.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("row A's own already-completed rotation was lost after row B's own interruption -- OauthExpiresAt = %v, want pushed comfortably into the future by the refresh", gotA.OauthExpiresAt.Time)
	}

	plaintext, err := platform.DecryptToken([]byte(testTokenEncryptionKey)[:32], gotA.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken row A: %v", err)
	}
	var blob map[string]any
	if err := json.Unmarshal(plaintext, &blob); err != nil {
		t.Fatalf("unmarshal row A blob: %v", err)
	}
	if blob["refresh"] != "new-refresh-A" {
		t.Errorf("row A's own rotated refresh token was lost after row B's own interruption: blob[refresh] = %v, want %q", blob["refresh"], "new-refresh-A")
	}
}
