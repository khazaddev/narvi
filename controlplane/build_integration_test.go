//go:build integration

// This file is the pure-move refactor's proof obligation: it builds the
// REAL composition root (Build) against a real, migrated Postgres and
// asserts App.Routes() -- the live chi router's own route table -- equals
// testdata/routes.golden exactly. That golden was captured, via
// internal/ops.ScanRegisteredRoutes, from cmd/control-plane's OWN wiring
// BEFORE this package existed at all (see this PR's own commit message
// for exactly how and when); if this test still passes after the move,
// the move changed no route.
//
// newTestPool below mirrors test/resilience/harness_test.go's own
// newHarness and internal/adapters/inbound/httpapi's own newTestPool
// (container-start-plus-migrate) exactly -- this package builds its own
// copy rather than importing either of theirs, per this repo's own
// established "each DB-touching test package owns a small copy of this
// helper" precedent (see either file's own doc comment for the same
// reasoning). setRequiredEnv/testGitHubAppPrivateKeyPEM below are an
// identical copy of internal/platform/config_test.go's own fixtures of the
// same names, for the same reason: this is the first package outside
// internal/platform itself that needs a platform.Load()-valid Config
// (Build's own signature takes *platform.Config directly, and constructing
// one field-by-field would either duplicate platform.Load()'s own
// defaulting/parsing logic or risk drifting from it -- going through Load
// itself, exactly like production boot does, avoids both).
package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver, used only for the migrate handle below
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/platform"
	"github.com/narvidev/narvi/migrations"
)

// setRequiredEnv sets every env var platform.Load requires to succeed to a
// valid dummy value, for the duration of the calling test, via t.Setenv --
// an exact copy of internal/platform/config_test.go's own helper of the
// same name (see this file's own top doc comment for why this package
// keeps its own copy rather than importing that one). NARVI_DATABASE_URL
// is overwritten by the caller immediately afterward to point at this
// test's own real container instead of this fixture's placeholder value.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("NARVI_STAGE", "development")
	t.Setenv("NARVI_DATABASE_URL", "postgres://narvi:narvi@localhost:5432/narvi_test?sslmode=disable")
	t.Setenv("NARVI_HMAC_SANDBOX_SECRET", "test-sandbox-secret")
	t.Setenv("NARVI_HMAC_BOTS_SECRET", "test-bots-secret")
	t.Setenv("NARVI_HMAC_WEBHOOK_SECRET", "test-webhook-secret")
	t.Setenv("NARVI_GITHUB_CLIENT_ID", "test-github-client-id")
	t.Setenv("NARVI_GITHUB_CLIENT_SECRET", "test-github-client-secret")
	t.Setenv("NARVI_GITHUB_WEBHOOK_SECRET", "test-github-webhook-secret")
	t.Setenv("NARVI_GITHUB_BOT_HANDLE", "test-bot")
	t.Setenv("NARVI_GITHUB_BOT_TOKEN", "test-github-bot-token")
	t.Setenv("NARVI_PUBLIC_BASE_URL", "http://localhost:8080")
	t.Setenv("NARVI_TOKEN_ENCRYPTION_KEY", "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=") // base64 of exactly 32 bytes
	t.Setenv("NARVI_ALLOWED_EMAIL_DOMAINS", "example.com")
	t.Setenv("NARVI_ALLOWED_GITHUB_ORGS", "")
	t.Setenv("NARVI_ALLOWED_EMAILS", "")
	t.Setenv("NARVI_INITIAL_ADMIN_EMAILS", "")
	t.Setenv("NARVI_MODAL_BASE_URL", "https://modal.example.test")
	t.Setenv("NARVI_MODAL_AUTH_TOKEN", "test-modal-auth-token")
	t.Setenv("NARVI_MODAL_EGRESS_PROXY_URL", "")
	t.Setenv("NARVI_OPENCODE_RUNTIME_VERSION", "")
	t.Setenv("NARVI_LINEAR_WEBHOOK_SECRET", "test-linear-webhook-secret")
	t.Setenv("NARVI_LINEAR_CLIENT_ID", "test-linear-client-id")
	t.Setenv("NARVI_LINEAR_CLIENT_SECRET", "test-linear-client-secret")
	t.Setenv("NARVI_LINEAR_DEFAULT_REPO_NAME", "narvi")
	t.Setenv("NARVI_LINEAR_DEFAULT_REPO_URL", "https://github.com/narvidev/narvi")
	t.Setenv("NARVI_SLACK_SIGNING_SECRET", "test-slack-signing-secret")
	t.Setenv("NARVI_SLACK_BOT_TOKEN", "test-slack-bot-token")
	t.Setenv("NARVI_ANTHROPIC_API_KEY", "test-anthropic-api-key")
	t.Setenv("NARVI_INTENT_CLASSIFIER_PROVIDER", "anthropic")
	t.Setenv("NARVI_INTENT_CLASSIFIER_MODEL", "claude-haiku-4-5")
	t.Setenv("NARVI_GITHUB_APP_ID", "123456")
	t.Setenv("NARVI_GITHUB_APP_PRIVATE_KEY", testGitHubAppPrivateKeyPEM)
}

// testGitHubAppPrivateKeyPEM is a fixed, test-only 2048-bit RSA private
// key, base64-encoded PEM (PKCS#1, "BEGIN RSA PRIVATE KEY" -- the shape
// GitHub itself issues) -- an exact copy of internal/platform/config_test.
// go's own fixture of the same name, never used against any real GitHub
// App or any other credential in this codebase.
const testGitHubAppPrivateKeyPEM = "LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQpNSUlFcEFJQkFBS0NBUUVBMlozYVRnSXR6cE1YaXd0UVZGZW16VlZsd0JYY1E1RUJ1UUNnT281SG1tNnpyV25DCjliR0xUSzF4S1I1eFVqLy9Rck9taXJBb25lZlB3QmhnVmloTXpoL1NYMHpPOUgvWWhiS3F5aVVsRHI3ck0vMlYKMDZhVWxFdEtLcEZwbWVDemNSaUtPaGFTeWt1Q09XYVJzQWpWMzFSUGVVTi9MaVl6VUswTmlhL2piU05BRENYQwp3MzJKUjh0TmE1U1VwOFJ6amJkdWdUd0EvT1l4SXZTNmZSTnYvM0lWUXVBSGZaTFhHTmFhZGt3KzBPem1LY2x3Ck9NQmZZejRYRjdzOHFGOWR5eFhscm84eTNZak1COGcxYXJBcG9IeExyVlZ5S0xmN3h6VWJ3cG1uTW9QS2NKanYKMXN4bG5BMFhWcEwrZk1RN3RoV3dkOUZuSDBpOS8vcCs0c0dmeXdJREFRQUJBb0lCQUNIUngyQ0NOQzQ3YTlnLwpETi9lczF5TDNnRkpKRzhYdFFYVVZCSmxsRGtxNVIrWkpTUmIwRU05WFMyL3ZtckM2VityWGNHRitQbjVVYThQCjJzRHBDRzZzUVZ4d0ttV1RETXBTWnZwOVpWSHlWOGsvcXE0MjREWmZzUW9HaVR2UjBQRk5tQVhKQmswTUNSUDAKbmNXV3llNG9ReVdjV01LS1MwVkpiNllyUUpQd01lYVpwbkxEUGsvUDFhZnZyVkxHMG81SXRZNUxGa1dHaVdTYgpDbCtwOERGbytSWlFmVW1ERzdEY2hPWHIvZTJvd1NEODFIV2N3SXlzUlZxcGxPL3d4M2xuMjJIaUR5dVVxWTN4CmNQS2w5RHZ6c0I1NzZVU09MS0IvSGkvTjNIemc1bCt2V1MzZVZYeXovWlZYRnkrY3NkVkV6eG03QjA4QWlZK1UKRmo3Q3hjRUNnWUVBLzVDMkRWazMwQXUzRElYTnpxalFXUWJYV3BsWG5MNFU1NUJsUExYSWt3QlRHVU4rNFlOMQppQUNOM00wMUkyVnRHQ1B4a2pVbTg1WWxMc2tJYitaNGpyeW9NWk5XQzd1dzNyQ01LYit4aCtmZmdpdWNjSnRMClBMWGNsU203NzFBQ2FBT2FOR0R5RkduK3V3UzM5WDZ6MGJvaXIzc1I2NkFkVGx6TzJVYlhDNnNDZ1lFQTJmeWQKemhaYXRaMGxsYnZrcHdTbnozOEd5aWpnQjI1NmNyQ1dBSFo1dmwrVndvZ2Y0ZnllT0tacTFIalZkVSs3cUJOUgpEcGJDYVlkQWEwTXdMdXZ2Ti9jWVROVkpYUlM2S2ZOL3ZLUXBFQVFSMXN4L29Rd2s5TW9lbjJoSFoxVnBzM0pCClYrdVk3cDE0bVYxR2JIdjM1V3hQeXdKR1FWdlZiMklVWnNaK25HRUNnWUVBNVYwNUpxM0YyNkJIL3FNdjNLUEIKcWNUc0RsSEZRZFdPNld5OGowb080Mi9OSk1WZzRJQ2RRUnhPTmJhdVZFQTVNd3MvU1pzT2hGdGlyNlNaUCtTMgptbFJURjN0R0pHMmxCWmVwaythSkxKSThGSldUWjdUWVIzcG9xQzYyanNkZUFZQUtLNncrVjNmeHVHTTV2c2lpCkZqNVoxdWc3WXg5bWJlZjVkU09RNk5VQ2dZQlBWRGx4aUgwV1hzd1F3OElnYmZkTDhlUmNxYWR0ek96TzFDaWkKbm5zTHB1bHZVKzZXWlVLSFJ6alZmZXZndDFXSmd3NGFpdzdSTEtGcTU1YWZYTWsveXJLVE00TnhWbHV4YktYdAoxcWdDNWhnLzNVZ05LY2hCTlZVVG1mVnlTNGtkL3RSODFJWmhQL2xsaHFaY1VIa1VpdWcyN3VyMldoOUFXNmNsCkI5T0h3UUtCZ1FEekt2YzMvWDZ6NzdMeUFqb1BIZUpIbXQyL2tSWllJQjNmUUlsRkg0R3JoVUg1TXdLNklIeUkKWGxPUU53ZHVwdm5QaXlHS0dYeUwvcHJSVGdxQXpGMUFPNW0xWG8wVlJnMVZTeGp6Y1RPTU0zVGpnYU5GYmlMdwpreUovZjlhdzhrUTU2RFA2OWlzV1BKaVUyQko1blZLUTJPVEJwSHNTa2h5eS94amZaT29zbFE9PQotLS0tLUVORCBSU0EgUFJJVkFURSBLRVktLS0tLQo="

// newTestPool spins up one throwaway Postgres container, applies every
// embedded migration, and returns a ready pool plus that container's own
// connection string (so the caller can point NARVI_DATABASE_URL at it
// before calling platform.Load). t.Cleanup tears down the pool and the
// container. Mirrors test/resilience/harness_test.go's own newHarness
// (same testcontainers image/options, same golang-migrate iofs source) --
// see this file's own top doc comment for why this package builds its own
// copy rather than importing that one.
func newTestPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("narvi_test"),
		tcpostgres.WithUsername("narvi"),
		tcpostgres.WithPassword("narvi"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
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
	defer func() { _ = migrateDB.Close() }()

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

	return pool, connStr
}

// TestBuild_RouteTableMatchesGolden is this PR's own proof obligation --
// see this file's own top doc comment.
func TestBuild_RouteTableMatchesGolden(t *testing.T) {
	setRequiredEnv(t)

	pool, connStr := newTestPool(t)
	t.Setenv("NARVI_DATABASE_URL", connStr)

	cfg, err := platform.Load()
	if err != nil {
		t.Fatalf("platform.Load: %v", err)
	}

	app, err := Build(context.Background(), cfg, pool)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "routes.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	wantRoutes := strings.Split(strings.TrimRight(string(goldenBytes), "\n"), "\n")
	sort.Strings(wantRoutes)

	gotRoutes := app.Routes()

	wantSet := make(map[string]bool, len(wantRoutes))
	for _, r := range wantRoutes {
		wantSet[r] = true
	}
	gotSet := make(map[string]bool, len(gotRoutes))
	for _, r := range gotRoutes {
		gotSet[r] = true
	}

	var missing, extra []string
	for _, r := range wantRoutes {
		if !gotSet[r] {
			missing = append(missing, r)
		}
	}
	for _, r := range gotRoutes {
		if !wantSet[r] {
			extra = append(extra, r)
		}
	}

	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("App.Routes() does not match testdata/routes.golden.\nmissing (in golden, not in App.Routes()): %s\nextra (in App.Routes(), not in golden): %s",
			formatRouteList(missing), formatRouteList(extra))
	}
}

// formatRouteList renders routes for TestBuild_RouteTableMatchesGolden's
// own failure message -- "(none)" rather than an empty, easy-to-miss line
// when one side of the diff is clean.
func formatRouteList(routes []string) string {
	if len(routes) == 0 {
		return "(none)"
	}
	return "\n  " + strings.Join(routes, "\n  ")
}
