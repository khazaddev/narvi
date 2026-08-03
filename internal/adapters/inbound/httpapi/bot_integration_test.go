//go:build integration

// Integration tests for the two small, EXPORTED bot-ingress wrappers in
// bot.go (Step 32, "GitHub ingress") -- CreateSessionForBot and
// CreateTurnForBot. Lives in package httpapi (not httpapi_test), mirroring
// createcore_integration_test.go's own precedent, since it exercises
// createSessionCore indirectly through CreateSessionForBot. Builds its
// own minimal testcontainers-Postgres rig rather than reusing
// httpapi_test's own newTestPool/newTestRig, for the SAME reason
// createcore_integration_test.go's own doc comment already gives: an
// external test package's unexported helpers are not reachable from this
// internal one.
package httpapi

import (
	"context"
	"database/sql"
	"errors"
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

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newBotTestPool is this file's own copy of the testcontainers-Postgres-
// plus-embedded-migrations helper createcore_integration_test.go's own
// newCoreTestPool already implements.
func newBotTestPool(t *testing.T) *pgxpool.Pool {
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

// TestCreateSessionForBot_CreatesNullCreatorSession proves the exported
// wrapper forwards to createSessionCore with a genuine NULL creator and
// surfaces its result as a plain error (not *createSessionError) on
// success.
func TestCreateSessionForBot_CreatesNullCreatorSession(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	prompt := "review this PR please"
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "widgets", Url: "https://github.com/acme/widgets.git"},
		},
	}

	created, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, req)
	if err != nil {
		t.Fatalf("CreateSessionForBot: %v", err)
	}
	if created.CreatedBy.Valid {
		t.Error("CreatedBy.Valid = true, want false (NULL) for a bot-created session")
	}
	if created.SpawnSource != sqlcgen.SessionSpawnSourceGithub {
		t.Errorf("SpawnSource = %q, want %q", created.SpawnSource, sqlcgen.SessionSpawnSourceGithub)
	}

	turnRows, err := turns.ListForSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnRows) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turnRows))
	}
}

// TestCreateSessionForBot_ValidationFailureSurfacesAsError proves an
// invalid repo (rejected by internal/domain/reposource before any
// Postgres write, exactly like createSessionCore's own doc comment
// describes) surfaces as a plain, non-nil error -- CreateSessionForBot
// flattens *createSessionError into the error interface, so a caller in
// another package (which cannot reference the unexported type itself)
// still gets a usable, non-nil error to log/act on.
func TestCreateSessionForBot_ValidationFailureSurfacesAsError(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos:       []restdtos.CreateSessionRequestReposElem{}, // empty -- rejected before any Postgres write.
	}

	if _, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, req); err == nil {
		t.Fatal("CreateSessionForBot() error = nil, want non-nil for an empty repos list")
	}
}

// TestCreateTurnForBot_EnqueuesTurnOnExistingSession proves
// CreateTurnForBot enqueues a new Pending turn on an EXISTING session,
// and that it does NOT apply CreateTurn's own hasOpenTurn 409 gate -- a
// SECOND call while the first turn is still Pending must still succeed
// (see bot.go's own doc comment for why: this is exactly the coalesced-
// backlog behavior Step 32's own per-PR reuse needs).
func TestCreateTurnForBot_EnqueuesTurnOnExistingSession(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	plans := narvipg.NewPlanStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	created, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "widgets", Url: "https://github.com/acme/widgets.git"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSessionForBot (setup): %v", err)
	}

	first, err := CreateTurnForBot(ctx, pool, sessions, turns, plans, auditLog, registry, created.ID, "first mention", nil, false, pgtype.UUID{})
	if err != nil {
		t.Fatalf("CreateTurnForBot (first): %v", err)
	}

	second, err := CreateTurnForBot(ctx, pool, sessions, turns, plans, auditLog, registry, created.ID, "second concurrent mention", nil, false, pgtype.UUID{})
	if err != nil {
		t.Fatalf("CreateTurnForBot (second, while first still pending): %v", err)
	}
	if second.ID == first.ID {
		t.Error("second turn has the same id as the first, want a distinct new turn")
	}

	turnRows, err := turns.ListForSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnRows) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (both enqueued despite neither having reached a terminal state)", len(turnRows))
	}
}

// TestCreateTurnForBot_WritesAuditLogRowWithActor is the audit-fix batch's
// own regression test for H7 ("every turn created via ... GitHub bot-
// ingress is invisible in the audit trail"): CreateTurnForBot now writes
// the SAME turn.create audit_log row every other createTurnLocked caller
// gets, attributing the SAME resolved actor github/coalesce.go's own
// CreateOrJoin already passes through to it (a real user_id when the
// commenter is linked, an explicit invalid pgtype.UUID{} -- proven by
// TestCreateTurnForBot_EnqueuesTurnOnExistingSession above -- otherwise).
func TestCreateTurnForBot_WritesAuditLogRowWithActor(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	plans := narvipg.NewPlanStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	users := narvipg.NewUserStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	actor, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "bot-turn-actor@example.com", DisplayName: "Linked Commenter", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture actor: %v", err)
	}

	created, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "widgets", Url: "https://github.com/acme/widgets.git"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSessionForBot (setup): %v", err)
	}

	turnRow, err := CreateTurnForBot(ctx, pool, sessions, turns, plans, auditLog, registry, created.ID, "please take a look", nil, false, actor.ID)
	if err != nil {
		t.Fatalf("CreateTurnForBot: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'turn.create' AND resource_type = 'turn' AND resource_id = $1`,
		turnRow.ID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit_log row count = %d, want 1", count)
	}

	// Postgres's built-in uuid type has no MAX/MIN aggregate registered
	// (unlike text/int/timestamp) despite supporting ordering operators --
	// a plain, non-aggregated SELECT is used instead, safe here since
	// count == 1 was just proven above.
	var actorUserID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT actor_user_id FROM audit_log WHERE action = 'turn.create' AND resource_type = 'turn' AND resource_id = $1`,
		turnRow.ID.String(),
	).Scan(&actorUserID); err != nil {
		t.Fatalf("query actor_user_id: %v", err)
	}
	if actorUserID != actor.ID {
		t.Errorf("audit_log.actor_user_id = %v, want %v", actorUserID, actor.ID)
	}
}

// TestCreateTurnForBot_PlanAwaitingApproval_PreservesSentinel is Finding
// 1's own regression test (Step 37/38 follow-up fix): before this fix,
// CreateTurnForBot re-wrapped createTurnLocked's own *CreateTurnError via
// fmt.Errorf's "%s" verb, which discarded the error chain entirely --
// errors.Is(err, ErrPlanAwaitingApproval) could never succeed for ANY
// caller of this function (github/coalesce.go's REUSE path is that one
// real caller, handler_integration_test.go's own
// TestGitHubIntegration_AwaitingPlanBlocksReuseTurn_HonestReplyNoRelease
// proves the full, further-wrapped chain through THAT caller). This test
// proves the fix at the source: CreateTurnForBot's own plain returned
// error now still satisfies errors.Is(err, ErrPlanAwaitingApproval) after
// being wrapped with "%w" instead.
func TestCreateTurnForBot_PlanAwaitingApproval_PreservesSentinel(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	plans := narvipg.NewPlanStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	created, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "widgets", Url: "https://github.com/acme/widgets.git"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSessionForBot (setup): %v", err)
	}

	// Seed a producing turn (Completed, plan_mode true) and an
	// awaiting_approval plans row atop it -- mirrors turncore_integration_
	// test.go's own identical seedAwaitingApprovalPlan helper, duplicated
	// here since this file cannot reach that unexported one either (both
	// live in package httpapi but in different files of the SAME package,
	// so this could reuse it directly -- kept as a short inline seed here
	// instead, since this is this file's own only use of the pattern).
	producingTurn, err := turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: created.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("seed producing turn: %v", err)
	}
	if _, err := plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: created.ID, TurnID: producingTurn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval}); err != nil {
		t.Fatalf("seed awaiting_approval plan: %v", err)
	}

	_, err = CreateTurnForBot(ctx, pool, sessions, turns, plans, auditLog, registry, created.ID, "please build this now", nil, false, pgtype.UUID{})
	if err == nil {
		t.Fatal("CreateTurnForBot() error = nil, want a non-nil error wrapping ErrPlanAwaitingApproval")
	}
	if !errors.Is(err, ErrPlanAwaitingApproval) {
		t.Errorf("errors.Is(err, ErrPlanAwaitingApproval) = false, want true (err = %v) -- the sentinel must survive CreateTurnForBot's own re-wrap", err)
	}

	// The gate must have actually declined the insert -- only the seeded
	// producing turn is present.
	turnRows, err := turns.ListForSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnRows) != 1 {
		t.Fatalf("len(turns) = %d, want exactly 1 (the seeded producing turn only)", len(turnRows))
	}
}
