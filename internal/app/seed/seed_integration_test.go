//go:build integration

// Integration tests for internal/app/seed against a REAL Postgres
// instance (§9.1) -- gated behind the "integration" build tag, mirroring
// internal/app/identitylink's own conventions exactly (testcontainers
// Postgres, embedded migrations via golang-migrate's iofs source
// driver). Run via `make test-integration`.
//
// This is the primary evidence file for §10's own load-bearing
// guards: participants map by GitHub id only and never touch an
// existing user's role (TestRun_Participant*), secrets are create-if-
// absent so an out-of-band rotation survives a re-run
// (TestRun_Secrets_*), automations/repo settings prove their own
// declared idempotency semantics under a genuine "modified between
// runs" scenario (TestRun_Automations_*, TestRun_RepoSettings_*), and a
// dry run writes nothing at all (TestRun_DryRun_WritesNothing).
package seed_test

import (
	"context"
	"database/sql"
	"errors"
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

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/seed"
	"github.com/khazaddev/narvi/internal/domain/seedmanifest"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// testTokenEncryptionKey is a fixed, obviously-fake 32-byte AES-256 key
// -- test-only, never used outside this throwaway container's own
// process lifetime.
var testTokenEncryptionKey = []byte("test-key-not-for-real-use-000000")[:32]

// newTestPool spins up a throwaway Postgres container, runs every
// embedded migration up, and returns a ready *pgxpool.Pool -- mirrors
// internal/app/identitylink's own identical helper (that package's own
// doc comment has the full "why a goroutine + watchdog race" writeup for
// the container-start call; reproduced unchanged here).
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

func newTestDeps(t *testing.T, initialAdminEmails []string) seed.Deps {
	t.Helper()
	pool := newTestPool(t)
	return seed.NewDeps(pool, testTokenEncryptionKey, initialAdminEmails)
}

func requireNoItemErrors(t *testing.T, report *seed.Report) {
	t.Helper()
	if report.HasErrors() {
		t.Fatalf("report has item errors:\n%s", report.String())
	}
}

// --- Participants -----------------------------------------------------

func TestRun_ParticipantsRoleFromInitialAdminEmailsOnly(t *testing.T) {
	t.Parallel()
	deps := newTestDeps(t, []string{"admin@example.test"})
	ctx := context.Background()

	m := &seedmanifest.Manifest{Participants: []seedmanifest.Participant{
		{GitHubID: 1001, Email: "admin@example.test", DisplayName: "Admin Person"},
		{GitHubID: 1002, Email: "member@example.test", DisplayName: "Member Person"},
	}}

	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)

	role := func(email string) string {
		var r string
		if err := deps.Pool.QueryRow(ctx, "SELECT role::text FROM users WHERE primary_email = $1", email).Scan(&r); err != nil {
			t.Fatalf("query role for %s: %v", email, err)
		}
		return r
	}
	if got := role("admin@example.test"); got != "admin" {
		t.Errorf("role(admin@example.test) = %s, want admin", got)
	}
	if got := role("member@example.test"); got != "member" {
		t.Errorf("role(member@example.test) = %s, want member", got)
	}

	var auditCount int
	if err := deps.Pool.QueryRow(ctx,
		"SELECT count(*) FROM audit_log WHERE action = 'seed.participant_created' AND actor_user_id IS NULL",
	).Scan(&auditCount); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if auditCount != 2 {
		t.Errorf("audit_log seed.participant_created count = %d, want 2", auditCount)
	}
}

// TestRun_ParticipantAlreadyLinked_RoleNeverTouched is the direct proof
// of §13.4's "the role-granting path must never escalate an existing
// user silently": an identity that already resolves to a user is left
// completely alone, in BOTH directions -- neither escalated when the
// manifest/config would now imply admin, nor (implicitly, since no path
// exists to change it at all) ever demoted either.
func TestRun_ParticipantAlreadyLinked_RoleNeverTouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil) // no initial admins configured yet

	// Pre-create a user+identity exactly like a first run would, with
	// role=admin (as if a human admin had promoted them via Settings ->
	// Members after the first seed run).
	user, err := deps.Users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "already-linked@example.test", DisplayName: "Already Linked", Role: sqlcgen.UserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("pre-create user: %v", err)
	}
	if _, err := deps.Identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID: user.ID, Provider: sqlcgen.IdentityProviderGithub, ExternalID: "2001", LinkedVia: sqlcgen.IdentityLinkedViaAdmin,
	}); err != nil {
		t.Fatalf("pre-create identity: %v", err)
	}

	// Re-run seed for the SAME github id -- deliberately NOT listing this
	// email in InitialAdminEmails this time, simulating an operator who
	// re-runs after editing the admin config to remove this person, or
	// simply forgot to re-list them. If this run ever silently demoted
	// them, that would be exactly the "silently escalate/de-escalate an
	// existing user" bug this guard exists to catch.
	m := &seedmanifest.Manifest{Participants: []seedmanifest.Participant{
		{GitHubID: 2001, Email: "already-linked@example.test", DisplayName: "Already Linked"},
	}}
	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)
	if len(report.Items) != 1 || report.Items[0].Outcome != seed.OutcomeSkipped {
		t.Fatalf("report.Items = %+v, want exactly 1 Skipped item", report.Items)
	}

	var role string
	if err := deps.Pool.QueryRow(ctx, "SELECT role::text FROM users WHERE id = $1", user.ID).Scan(&role); err != nil {
		t.Fatalf("query role: %v", err)
	}
	if role != "admin" {
		t.Errorf("role after re-run = %s, want admin (unchanged)", role)
	}

	// The other direction: a member whose email NOW matches
	// InitialAdminEmails must NOT be silently escalated to admin on
	// re-run either.
	deps2 := seed.NewDeps(deps.Pool, testTokenEncryptionKey, []string{"member-now-admin-listed@example.test"})
	memberUser, err := deps2.Users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "member-now-admin-listed@example.test", DisplayName: "Member Person", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("pre-create member user: %v", err)
	}
	if _, err := deps2.Identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID: memberUser.ID, Provider: sqlcgen.IdentityProviderGithub, ExternalID: "2002", LinkedVia: sqlcgen.IdentityLinkedViaAdmin,
	}); err != nil {
		t.Fatalf("pre-create member identity: %v", err)
	}
	m2 := &seedmanifest.Manifest{Participants: []seedmanifest.Participant{
		{GitHubID: 2002, Email: "member-now-admin-listed@example.test", DisplayName: "Member Person"},
	}}
	report2, err := seed.Run(ctx, deps2, m2, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report2)

	var role2 string
	if err := deps2.Pool.QueryRow(ctx, "SELECT role::text FROM users WHERE id = $1", memberUser.ID).Scan(&role2); err != nil {
		t.Fatalf("query role: %v", err)
	}
	if role2 != "member" {
		t.Errorf("role after re-run with email now admin-listed = %s, want member (NEVER silently escalated)", role2)
	}
}

// TestSeedParticipant_ExternalIDIsNumericGitHubID proves identities.
// external_id is written as the plain decimal GitHub numeric id (the
// join key point A requires), never anything login-shaped.
func TestSeedParticipant_ExternalIDIsNumericGitHubID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)

	m := &seedmanifest.Manifest{Participants: []seedmanifest.Participant{
		{GitHubID: 987654321, Email: "numeric-id-check@example.test", DisplayName: "Numeric Id Check"},
	}}
	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)

	var externalID string
	if err := deps.Pool.QueryRow(ctx,
		"SELECT external_id FROM identities WHERE provider = 'github' AND external_id = '987654321'",
	).Scan(&externalID); err != nil {
		t.Fatalf("query identity by numeric external_id: %v (external_id was not written as the exact decimal GitHub id)", err)
	}
	if externalID != "987654321" {
		t.Errorf("external_id = %q, want exactly \"987654321\"", externalID)
	}
}

// --- Secrets ------------------------------------------------------------

func TestRun_Secrets_CreateIfAbsent_SurvivesOutOfBandRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)

	m := &seedmanifest.Manifest{Secrets: []seedmanifest.Secret{
		{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_ROTATION_TOKEN", Value: "original-value"},
	}}
	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)
	if report.Items[0].Outcome != seed.OutcomeCreated {
		t.Fatalf("first run outcome = %s, want created", report.Items[0].Outcome)
	}

	rows, err := deps.Secrets.ListByScope(ctx, sqlcgen.SandboxSecretScopeGlobal, nil)
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	var secretID = findSecretID(t, rows, "EXAMPLE_ROTATION_TOKEN")

	decrypt := func() string {
		row, err := deps.Secrets.Get(ctx, secretID)
		if err != nil {
			t.Fatalf("get secret: %v", err)
		}
		plain, err := platform.DecryptToken(testTokenEncryptionKey, row.ValueEncrypted)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		return string(plain)
	}
	if got := decrypt(); got != "original-value" {
		t.Fatalf("value after first run = %q, want original-value", got)
	}

	// Simulate an out-of-band rotation (e.g. via the existing PUT
	// /api/.../sandbox-secrets/{id} endpoint, after the original value
	// leaked) -- this bypasses seed entirely, exactly like a real
	// operator action would.
	rotated, err := platform.EncryptToken(testTokenEncryptionKey, []byte("rotated-after-leak"))
	if err != nil {
		t.Fatalf("encrypt rotated value: %v", err)
	}
	if _, err := deps.Secrets.UpdateValue(ctx, secretID, rotated); err != nil {
		t.Fatalf("simulate rotation: %v", err)
	}
	if got := decrypt(); got != "rotated-after-leak" {
		t.Fatalf("value after simulated rotation = %q, want rotated-after-leak", got)
	}

	// Re-run seed with the EXACT SAME manifest (still carrying the OLD,
	// pre-rotation plaintext value). If this run ever overwrote the row,
	// it would silently restore a value already known to be
	// compromised -- the specific hazard create-if-absent exists to
	// prevent for secrets.
	report2, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() (2nd) error = %v", err)
	}
	requireNoItemErrors(t, report2)
	if report2.Items[0].Outcome != seed.OutcomeSkipped {
		t.Fatalf("second run outcome = %s, want skipped", report2.Items[0].Outcome)
	}
	if got := decrypt(); got != "rotated-after-leak" {
		t.Fatalf("value after re-running seed = %q, want STILL rotated-after-leak (must never revert to the manifest's stale value)", got)
	}
}

func findSecretID(t *testing.T, rows []sqlcgen.SandboxSecret, name string) pgtype.UUID {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r.ID
		}
	}
	t.Fatalf("no secret named %q found among %d rows", name, len(rows))
	return pgtype.UUID{}
}

// --- Automations ---------------------------------------------------------

func TestRun_Automations_CreateIfAbsent_SecondRunSkipsNoDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)

	m := &seedmanifest.Manifest{Automations: []seedmanifest.Automation{
		{
			Name:        "example-nightly-sweep",
			Repos:       []seedmanifest.RepoTarget{{Name: "widget-app", URL: "https://github.com/example-org/widget-app"}},
			TriggerType: seedmanifest.AutomationTriggerManual,
		},
	}}

	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)
	if report.Items[0].Outcome != seed.OutcomeCreated {
		t.Fatalf("first run outcome = %s, want created", report.Items[0].Outcome)
	}

	report2, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() (2nd) error = %v", err)
	}
	requireNoItemErrors(t, report2)
	if report2.Items[0].Outcome != seed.OutcomeSkipped {
		t.Fatalf("second run outcome = %s, want skipped", report2.Items[0].Outcome)
	}

	var count int
	if err := deps.Pool.QueryRow(ctx, "SELECT count(*) FROM automations WHERE name = $1", "example-nightly-sweep").Scan(&count); err != nil {
		t.Fatalf("count automations: %v", err)
	}
	if count != 1 {
		t.Errorf("automations named example-nightly-sweep = %d, want exactly 1 (no duplicate row)", count)
	}
}

func TestRun_Automations_WebhookTriggerMintsTokenExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)

	m := &seedmanifest.Manifest{Automations: []seedmanifest.Automation{
		{
			Name:        "example-webhook-automation",
			Repos:       []seedmanifest.RepoTarget{{Name: "widget-app", URL: "https://github.com/example-org/widget-app"}},
			TriggerType: seedmanifest.AutomationTriggerWebhook,
		},
	}}
	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)

	const marker = "webhook token (shown once): "
	idx := strings.Index(report.Items[0].Detail, marker)
	if idx == -1 {
		t.Fatalf("Item.Detail = %q, want it to contain the plaintext webhook token exactly once", report.Items[0].Detail)
	}
	token := report.Items[0].Detail[idx+len(marker):]
	if token == "" {
		t.Fatal("extracted webhook token is empty")
	}

	var hashInDB string
	if err := deps.Pool.QueryRow(ctx, "SELECT webhook_token_hash FROM automations WHERE name = $1", "example-webhook-automation").Scan(&hashInDB); err != nil {
		t.Fatalf("query webhook_token_hash: %v", err)
	}
	if hashInDB != platform.HashToken(token) {
		t.Errorf("stored webhook_token_hash does not match hash of the token surfaced in the report")
	}
}

// --- repo_settings / RWX preview ----------------------------------------

// TestRun_RepoSettings_ReconcileToDeclared_PreservesUndeclaredFields is
// the "modified between runs" proof for repo_settings: run 1 declares
// only sentinelAutofixEnabled; between runs, autoMergeEnabled is changed
// out-of-band (as if by an admin through the future Settings UI); run 2
// declares only blockOnHighRisk. Both declared values must take effect
// (reconcile-to-declared), and BOTH the undeclared field from run 1 and
// the out-of-band change must survive completely untouched.
func TestRun_RepoSettings_ReconcileToDeclared_PreservesUndeclaredFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)
	const repo = "example-org/settings-repo"

	trueVal := true
	m1 := &seedmanifest.Manifest{RepoSettings: []seedmanifest.RepoSetting{
		{RepoFullName: repo, SentinelAutofixEnabled: &trueVal},
	}}
	report1, err := seed.Run(ctx, deps, m1, false)
	if err != nil {
		t.Fatalf("Run() (1st) error = %v", err)
	}
	requireNoItemErrors(t, report1)

	// Simulate an admin's own out-of-band change to a field NEITHER
	// manifest run below ever declares.
	if _, err := deps.RepoSettings.UpsertAutoMergeToggle(ctx, repo, true); err != nil {
		t.Fatalf("simulate manual auto-merge toggle: %v", err)
	}

	m2 := &seedmanifest.Manifest{RepoSettings: []seedmanifest.RepoSetting{
		{RepoFullName: repo, BlockOnHighRisk: &trueVal},
	}}
	report2, err := seed.Run(ctx, deps, m2, false)
	if err != nil {
		t.Fatalf("Run() (2nd) error = %v", err)
	}
	requireNoItemErrors(t, report2)

	row, err := deps.RepoSettings.Get(ctx, repo)
	if err != nil {
		t.Fatalf("get repo settings: %v", err)
	}
	if !row.BlockOnHighRisk {
		t.Error("block_on_high_risk = false, want true (declared in run 2, must be applied)")
	}
	if !row.SentinelAutofixEnabled {
		t.Error("sentinel_autofix_enabled = false, want true (declared in run 1 only; must survive run 2's own undeclared omission)")
	}
	if !row.AutoMergeEnabled {
		t.Error("auto_merge_enabled = false, want true (set out-of-band; never declared by either manifest; must survive both runs untouched)")
	}
}

func TestRun_RWXPreview_ReconcilesEveryRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)
	const repo = "example-org/preview-repo"

	m1 := &seedmanifest.Manifest{RWXPreview: []seedmanifest.RWXPreview{
		{RepoFullName: repo, DispatchKey: "key-v1", EndpointTemplate: "https://preview.example.test/v1/{{id}}", OrgSlug: "example-org"},
	}}
	if _, err := seed.Run(ctx, deps, m1, false); err != nil {
		t.Fatalf("Run() (1st) error = %v", err)
	}

	m2 := &seedmanifest.Manifest{RWXPreview: []seedmanifest.RWXPreview{
		{RepoFullName: repo, DispatchKey: "key-v2", EndpointTemplate: "https://preview.example.test/v2/{{id}}", OrgSlug: "example-org"},
	}}
	report2, err := seed.Run(ctx, deps, m2, false)
	if err != nil {
		t.Fatalf("Run() (2nd) error = %v", err)
	}
	requireNoItemErrors(t, report2)
	if report2.Items[0].Outcome != seed.OutcomeUpserted {
		t.Fatalf("second run outcome = %s, want upserted", report2.Items[0].Outcome)
	}

	row, err := deps.RepoSettings.Get(ctx, repo)
	if err != nil {
		t.Fatalf("get repo settings: %v", err)
	}
	if row.RwxPreviewEndpointTemplate == nil || *row.RwxPreviewEndpointTemplate != "https://preview.example.test/v2/{{id}}" {
		t.Errorf("rwx_preview_endpoint_template = %v, want the run-2 declared value (reconcile-to-declared)", row.RwxPreviewEndpointTemplate)
	}
}

// --- Dry run --------------------------------------------------------------

func TestRun_DryRun_WritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, []string{"admin@example.test"})

	trueVal := true
	m := &seedmanifest.Manifest{
		Participants: []seedmanifest.Participant{{GitHubID: 3001, Email: "admin@example.test", DisplayName: "Dry Run Admin"}},
		Secrets:      []seedmanifest.Secret{{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_DRYRUN_TOKEN", Value: "should-never-be-written"}},
		Automations: []seedmanifest.Automation{{
			Name:        "example-dryrun-automation",
			Repos:       []seedmanifest.RepoTarget{{Name: "widget-app", URL: "https://github.com/example-org/widget-app"}},
			TriggerType: seedmanifest.AutomationTriggerManual,
		}},
		RepoSettings: []seedmanifest.RepoSetting{{RepoFullName: "example-org/dryrun-repo", BlockOnHighRisk: &trueVal}},
		RWXPreview:   []seedmanifest.RWXPreview{{RepoFullName: "example-org/dryrun-repo", DispatchKey: "k", EndpointTemplate: "t", OrgSlug: "o"}},
	}

	report, err := seed.Run(ctx, deps, m, true)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)
	if !report.DryRun {
		t.Error("report.DryRun = false, want true")
	}
	for _, it := range report.Items {
		if !strings.HasPrefix(string(it.Outcome), "would_") {
			t.Errorf("item %+v: outcome %s does not start with \"would_\" -- dry run must never report a real write outcome", it, it.Outcome)
		}
	}

	assertZero := func(query string, args ...any) {
		var n int
		if err := deps.Pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if n != 0 {
			t.Errorf("query %q = %d, want 0 (dry run must write nothing)", query, n)
		}
	}
	assertZero("SELECT count(*) FROM users WHERE primary_email = $1", "admin@example.test")
	assertZero("SELECT count(*) FROM sandbox_secrets WHERE name = $1", "EXAMPLE_DRYRUN_TOKEN")
	assertZero("SELECT count(*) FROM automations WHERE name = $1", "example-dryrun-automation")
	assertZero("SELECT count(*) FROM repo_settings WHERE repo_full_name = $1", "example-org/dryrun-repo")
	assertZero("SELECT count(*) FROM audit_log WHERE action LIKE 'seed.%'")
}

// --- Audit log -------------------------------------------------------------

func TestRun_AuditLog_OneEntryPerRealWriteSharingOneCorrelationID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	deps := newTestDeps(t, nil)

	trueVal := true
	m := &seedmanifest.Manifest{
		Participants: []seedmanifest.Participant{{GitHubID: 4001, Email: "audit-check@example.test", DisplayName: "Audit Check"}},
		Secrets:      []seedmanifest.Secret{{Scope: seedmanifest.SecretScopeGlobal, Name: "EXAMPLE_AUDIT_TOKEN", Value: "v"}},
		Automations: []seedmanifest.Automation{{
			Name:        "example-audit-automation",
			Repos:       []seedmanifest.RepoTarget{{Name: "widget-app", URL: "https://github.com/example-org/widget-app"}},
			TriggerType: seedmanifest.AutomationTriggerManual,
		}},
		RepoSettings: []seedmanifest.RepoSetting{{RepoFullName: "example-org/audit-repo", BlockOnHighRisk: &trueVal}},
	}

	report, err := seed.Run(ctx, deps, m, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	requireNoItemErrors(t, report)

	rows, err := deps.Pool.Query(ctx, "SELECT action, correlation_id, actor_user_id FROM audit_log WHERE action LIKE 'seed.%'")
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()

	correlationIDs := map[string]bool{}
	actions := map[string]int{}
	for rows.Next() {
		var action string
		var correlationID *string
		var actorUserID *string
		if err := rows.Scan(&action, &correlationID, &actorUserID); err != nil {
			t.Fatalf("scan audit_log row: %v", err)
		}
		if actorUserID != nil {
			t.Errorf("audit_log row action=%s has actor_user_id=%v, want NULL (system-driven)", action, *actorUserID)
		}
		if correlationID == nil {
			t.Errorf("audit_log row action=%s has NULL correlation_id, want the run's shared id", action)
		} else {
			correlationIDs[*correlationID] = true
		}
		actions[action]++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log rows: %v", err)
	}

	for _, want := range []string{"seed.participant_created", "seed.secret_created", "seed.automation_created", "seed.repo_setting_upserted"} {
		if actions[want] != 1 {
			t.Errorf("audit_log count for action=%s = %d, want 1", want, actions[want])
		}
	}
	if len(correlationIDs) != 1 {
		t.Errorf("distinct correlation ids across this run's audit rows = %d, want exactly 1", len(correlationIDs))
	}
}
